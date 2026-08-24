package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/config"
	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	"github.com/AreaSong/ops/services/areasong-ops/internal/store"
)

func TestAccessChangeAppliesRBACMutationOnlyAfterIndependentExecution(t *testing.T) {
	root := t.TempDir()
	database, err := store.Open(filepath.Join(root, "ops.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Now().UTC()
	creator := config.AccessHashForEmail("creator@example.test")
	approver := config.AccessHashForEmail("approver@example.test")
	secondApprover := config.AccessHashForEmail("second@example.test")
	executor := config.AccessHashForEmail("executor@example.test")
	admin := model.Role{ID: "platform-admin", DisplayName: "Platform admin", BuiltIn: true, Permissions: []model.Permission{model.Permission("*"), model.PermissionManageAccess}}
	catalog := &config.Catalog{
		SchemaVersion: 4,
		Access: &config.AccessPolicy{
			Enforced: true, DefaultTenant: "default",
			Tenants: map[string]model.Tenant{"default": {ID: "default", DisplayName: "Default", Status: "active", CreatedAt: now}},
			Roles:   map[string]model.Role{"platform-admin": admin},
			Principals: map[string]config.AccessPrincipal{
				creator:        {Subject: creator, TenantID: "default", Roles: []string{"platform-admin"}},
				approver:       {Subject: approver, TenantID: "default", Roles: []string{"platform-admin"}},
				secondApprover: {Subject: secondApprover, TenantID: "default", Roles: []string{"platform-admin"}},
				executor:       {Subject: executor, TenantID: "default", Roles: []string{"platform-admin"}},
			},
		},
	}
	engine, err := NewEngineChecked(catalog, database, &fakeExecutor{}, root)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	request := model.AccessControlUpdateRequest{
		RequiresDualApproval: true,
		Tenants:              []model.Tenant{{ID: "tenant-new", DisplayName: "New tenant", Status: "active"}},
		IdempotencyKey:       "11111111-1111-4111-8111-111111111111",
	}
	change, created, err := engine.CreateAccessChange(ctx, creator, request)
	if err != nil || !created {
		t.Fatalf("create change=%+v created=%v err=%v", change, created, err)
	}
	if _, err := engine.ApproveAccessChange(ctx, creator, change.ID, model.AccessChangeApprovalRequest{Digest: change.RequestDigest, Confirmation: change.ConfirmationPhrase}); err == nil {
		t.Fatal("creator unexpectedly approved own change")
	}
	if _, err := engine.ApproveAccessChange(ctx, approver, change.ID, model.AccessChangeApprovalRequest{Digest: "sha256:tampered", Confirmation: change.ConfirmationPhrase}); err == nil {
		t.Fatal("approver unexpectedly approved a changed digest")
	}
	unchanged, err := database.GetAccessChange(ctx, change.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.ApprovedByHash != "" || unchanged.State != model.AccessChangePendingApproval {
		t.Fatalf("failed digest approval changed state: %+v", unchanged)
	}
	if _, err := engine.ApproveAccessChange(ctx, approver, change.ID, model.AccessChangeApprovalRequest{Digest: change.RequestDigest, Confirmation: change.ConfirmationPhrase}); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.ApplyAccessChange(ctx, secondApprover, change.ID); err == nil {
		t.Fatal("second approver unexpectedly executed change")
	}
	change, err = engine.ApproveAccessChange(ctx, secondApprover, change.ID, model.AccessChangeApprovalRequest{Digest: change.RequestDigest, Confirmation: change.ConfirmationPhrase})
	if err != nil || change.State != model.AccessChangeApproved {
		t.Fatalf("second approval=%+v err=%v", change, err)
	}
	if _, err := engine.ApplyAccessChange(ctx, approver, change.ID); err == nil {
		t.Fatal("approver unexpectedly executed change")
	}
	change, err = engine.ApplyAccessChange(ctx, executor, change.ID)
	if err != nil || change.State != model.AccessChangeApplied {
		t.Fatalf("apply=%+v err=%v", change, err)
	}
	view, err := engine.AccessControl(ctx, executor)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tenant := range view.Tenants {
		if tenant.ID == "tenant-new" {
			found = true
		}
	}
	if !found {
		t.Fatalf("applied tenant missing: %+v", view.Tenants)
	}
}

func TestSchema4AccessPutCannotBypassApproval(t *testing.T) {
	root := t.TempDir()
	database, err := store.Open(filepath.Join(root, "ops.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Now().UTC()
	creator := config.AccessHashForEmail("creator@example.test")
	catalog := &config.Catalog{
		SchemaVersion: 4,
		Access: &config.AccessPolicy{
			Enforced: true, DefaultTenant: "default",
			Tenants: map[string]model.Tenant{
				"default": {ID: "default", DisplayName: "Default", Status: "active", CreatedAt: now},
			},
			Roles: map[string]model.Role{
				"platform-admin": {ID: "platform-admin", DisplayName: "Platform admin", BuiltIn: true, Permissions: []model.Permission{model.Permission("*"), model.PermissionManageAccess}},
			},
			Principals: map[string]config.AccessPrincipal{
				creator: {Subject: creator, TenantID: "default", Roles: []string{"platform-admin"}},
			},
		},
	}
	engine, err := NewEngineChecked(catalog, database, &fakeExecutor{}, root)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(model.AccessControlUpdateRequest{
		// Deliberately request the unsafe value. The public schema-4 endpoint
		// must normalize this to a dual-approval change.
		RequiresDualApproval: false,
		Tenants:              []model.Tenant{{ID: "tenant-new", DisplayName: "New tenant", Status: "active"}},
		IdempotencyKey:       "55555555-5555-4555-8555-555555555555",
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPut, "/v1/access", bytes.NewReader(payload))
	request.Header.Set(actorHeader, creator)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	NewServer(engine, database).ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("schema4 PUT status=%d body=%s", response.Code, response.Body.String())
	}
	var change model.AccessChange
	if err := json.Unmarshal(response.Body.Bytes(), &change); err != nil {
		t.Fatalf("decode change: %v; body=%s", err, response.Body.String())
	}
	if change.State != model.AccessChangePendingApproval || !change.RequiresDualApproval {
		t.Fatalf("unsafe PUT did not create pending dual-approval change: %+v", change)
	}
	if !strings.Contains(response.Body.String(), "pending_approval") {
		t.Fatalf("response omitted approval state: %s", response.Body.String())
	}
	view, err := engine.AccessControl(context.Background(), creator)
	if err != nil {
		t.Fatal(err)
	}
	for _, tenant := range view.Tenants {
		if tenant.ID == "tenant-new" {
			t.Fatal("schema4 PUT mutated policy before approvals")
		}
	}
	changes, err := database.ListAccessChanges(context.Background(), 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || changes[0].ID != change.ID || !changes[0].RequiresDualApproval {
		t.Fatalf("unexpected persisted access changes: %+v", changes)
	}

	// Replaying the same unsafe request remains idempotent and cannot turn it
	// into a direct policy write.
	request = httptest.NewRequest(http.MethodPut, "/v1/access", bytes.NewReader(payload))
	request.Header.Set(actorHeader, creator)
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	NewServer(engine, database).ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), change.ID) {
		t.Fatalf("schema4 PUT replay status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestSchema3AccessPutRetainsDirectWriteCompatibility(t *testing.T) {
	root := t.TempDir()
	database, err := store.Open(filepath.Join(root, "ops.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Now().UTC()
	creator := config.AccessHashForEmail("legacy@example.test")
	catalog := &config.Catalog{
		SchemaVersion: 3,
		Access: &config.AccessPolicy{
			DefaultTenant: "default",
			Tenants: map[string]model.Tenant{
				"default": {ID: "default", DisplayName: "Default", Status: "active", CreatedAt: now},
			},
			Roles: map[string]model.Role{
				"platform-admin": {ID: "platform-admin", DisplayName: "Platform admin", BuiltIn: true, Permissions: []model.Permission{model.Permission("*"), model.PermissionManageAccess}},
			},
			Principals: map[string]config.AccessPrincipal{
				creator: {Subject: creator, TenantID: "default", Roles: []string{"platform-admin"}},
			},
		},
	}
	engine, err := NewEngineChecked(catalog, database, &fakeExecutor{}, root)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(model.AccessControlUpdateRequest{
		RequiresDualApproval: false,
		Tenants:              []model.Tenant{{ID: "legacy-tenant", DisplayName: "Legacy tenant", Status: "active"}},
		IdempotencyKey:       "66666666-6666-4666-8666-666666666666",
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPut, "/v1/access", bytes.NewReader(payload))
	request.Header.Set(actorHeader, creator)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	NewServer(engine, database).ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("schema3 PUT status=%d body=%s", response.Code, response.Body.String())
	}
	var view model.AccessControlView
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode schema3 view: %v; body=%s", err, response.Body.String())
	}
	found := false
	for _, tenant := range view.Tenants {
		if tenant.ID == "legacy-tenant" {
			found = true
		}
	}
	if !found {
		t.Fatalf("schema3 direct write did not persist tenant: %+v", view.Tenants)
	}
}

func TestAccessPolicyRevocationIsImmediateAndStaleVersionIsAtomic(t *testing.T) {
	root := t.TempDir()
	database, err := store.Open(filepath.Join(root, "ops.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	adminActor := config.AccessHashForEmail("admin@example.test")
	revokedActor := config.AccessHashForEmail("revoked@example.test")
	now := time.Now().UTC()
	catalog := &config.Catalog{
		SchemaVersion: 4,
		Services: map[string]model.ServiceDefinition{
			"svc": {Name: "svc", ObjectID: "service:svc", TenantID: "default", DisplayName: "Service"},
		},
		Access: &config.AccessPolicy{
			Enforced: true, DefaultTenant: "default",
			Tenants: map[string]model.Tenant{
				"default": {ID: "default", DisplayName: "Default", Status: "active", CreatedAt: now},
			},
			Roles: map[string]model.Role{
				"platform-admin": {ID: "platform-admin", DisplayName: "Platform admin", BuiltIn: true, Permissions: []model.Permission{model.Permission("*")}},
				"viewer":         {ID: "viewer", DisplayName: "Viewer", BuiltIn: true, Permissions: []model.Permission{model.PermissionRead}},
			},
			Principals: map[string]config.AccessPrincipal{
				adminActor:   {Subject: adminActor, TenantID: "default", Roles: []string{"platform-admin"}},
				revokedActor: {Subject: revokedActor, TenantID: "default", Roles: []string{"viewer"}},
			},
		},
	}
	engine, err := NewEngineChecked(catalog, database, &fakeExecutor{}, root)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := engine.authorize(ctx, revokedActor, model.PermissionRead, "service:svc"); err != nil {
		t.Fatalf("initial authorization failed: %v", err)
	}
	view, err := engine.AccessControl(ctx, adminActor)
	if err != nil {
		t.Fatal(err)
	}
	view, err = engine.UpdateAccess(ctx, adminActor, model.AccessControlUpdateRequest{
		RemovePrincipalSubjects: []string{revokedActor}, ExpectedVersion: view.Version,
		IdempotencyKey: "22222222-2222-4222-8222-222222222222",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.authorize(ctx, revokedActor, model.PermissionRead, "service:svc"); err == nil {
		t.Fatal("revoked principal remained authorized without Runner restart")
	}
	staleVersion := view.Version
	view, err = engine.UpdateAccess(ctx, adminActor, model.AccessControlUpdateRequest{
		Tenants:         []model.Tenant{{ID: "tenant-one", DisplayName: "Tenant one", Status: "active"}},
		ExpectedVersion: staleVersion,
		IdempotencyKey:  "33333333-3333-4333-8333-333333333333",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.UpdateAccess(ctx, adminActor, model.AccessControlUpdateRequest{
		Tenants:         []model.Tenant{{ID: "tenant-two", DisplayName: "Tenant two", Status: "active"}},
		ExpectedVersion: staleVersion,
		IdempotencyKey:  "44444444-4444-4444-8444-444444444444",
	}); !errors.Is(err, store.ErrAccessVersion) {
		t.Fatalf("stale update err=%v want ErrAccessVersion", err)
	}
	tenants, err := database.ListTenants(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, tenant := range tenants {
		if tenant.ID == "tenant-two" {
			t.Fatal("stale update wrote tenant row before version failure")
		}
	}
	if view.Version <= staleVersion {
		t.Fatalf("version did not advance: stale=%d current=%d", staleVersion, view.Version)
	}
}
