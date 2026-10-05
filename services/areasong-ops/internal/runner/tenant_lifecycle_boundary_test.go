package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/AreaSong/ops/services/areasong-ops/internal/config"
	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	"github.com/AreaSong/ops/services/areasong-ops/internal/store"
)

func TestTenantLifecycleGenericHTTPAndStoredPayloadBoundary(t *testing.T) {
	f := newTenantReviewFixture(t)
	ctx := context.Background()
	request := model.TenantLifecycleTransitionRequest{Kind: model.TenantLifecycleKind, TenantID: "isolated", ExpectedStatus: "active", ExpectedGeneration: 1,
		TargetStatus: "disabled", ExpectedVersion: 1, RequiresDualApproval: true, IdempotencyKey: "00000000-0000-4000-8000-000000000777"}
	before := f.durableState(t)
	f.call(t, "creator", "POST", "/v1/access/changes", request, 400)
	f.call(t, "creator", "PUT", "/v1/access", request, 400)
	if f.durableState(t) != before {
		t.Fatal("rejected HTTP changed database")
	}
	raw, _ := json.Marshal(request)
	for i, payload := range []string{string(raw), `{"expectedGeneration":1,"requiresDualApproval":true,"idempotencyKey":"00000000-0000-4000-8000-000000000778"}`, `{"kind":"other","tenants":[],"idempotencyKey":"00000000-0000-4000-8000-000000000779"}`} {
		var keys struct {
			IdempotencyKey string `json:"idempotencyKey"`
		}
		if err := json.Unmarshal([]byte(payload), &keys); err != nil {
			t.Fatal(err)
		}
		change := model.AccessChange{ID: fmt.Sprintf("synthetic-lifecycle-%d", i), IdempotencyKey: keys.IdempotencyKey, ActorHash: f.actors["creator"], RequestDigest: digestText(payload),
			RequiresDualApproval: true, ApprovalPolicy: model.ApprovalPolicyTwoParty, ConfirmationPhrase: "synthetic lifecycle"}
		if _, _, err := f.db.CreateAccessChange(ctx, change, payload, store.HashConfirmation(change.ConfirmationPhrase)); err != nil {
			t.Fatal(err)
		}
		if _, err := f.db.ApproveAccessChange(ctx, change.ID, f.actors["approver"], change.RequestDigest, change.ConfirmationPhrase); err != nil {
			t.Fatal(err)
		}
		before := f.durableState(t)
		if _, err := f.engine.ApplyAccessChange(ctx, f.actors["creator"], change.ID); !errors.Is(err, model.ErrTenantLifecyclePayload) {
			t.Fatal(err)
		}
		detail := f.detail(t, "creator", change)
		if detail.Kind != "unsupported" || detail.Availability != "unsupported" {
			t.Fatalf("%+v", detail)
		}
		if f.durableState(t) != before {
			t.Fatal("unsupported payload was applied")
		}
	}
}

// Runner 边界夹具直接注入合成的已知停用状态；真正转换由 Store 同包测试验证。
func seedKnownDisabledTenant(t *testing.T, f *tenantReviewFixture) config.AccessPolicy {
	t.Helper()
	ctx := context.Background()
	change := f.proposal(t, "ordinary", "Ordinary")
	f.apply(t, change)
	policy, snapshot, err := f.engine.effectiveAccessPolicy(ctx)
	if err != nil {
		t.Fatal(err)
	}
	policy = cloneAccessPolicy(policy)
	tenant := policy.Tenants["ordinary"]
	tenant.Status = "disabled"
	policy.Tenants["ordinary"] = tenant
	policy.Principals[f.actors["viewer"]] = config.AccessPrincipal{Subject: f.actors["viewer"], TenantID: "ordinary", Roles: []string{"viewer"}, Status: "active"}
	binding := model.RoleBinding{ID: "retained-binding", Subject: f.actors["viewer"], TenantID: "ordinary", RoleID: "viewer", ObjectIDs: []string{"service:synthetic"}, CreatedBy: f.actors["creator"]}
	if err := f.db.UpsertRoleBinding(ctx, binding); err != nil {
		t.Fatal(err)
	}
	policy.Bindings = append(policy.Bindings, binding)
	raw, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := f.sql.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE tenants SET status='disabled',lifecycle_generation=2 WHERE id='ordinary'`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO access_policy_snapshots(version,digest,policy_json,actor_hash,created_at) VALUES(?,?,?,?,?)`, snapshot.Version+1, digestText(string(raw)), string(raw), f.actors["creator"], snapshot.CreatedAt.Format("2006-01-02T15:04:05.999999999Z07:00")); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return *policy
}

func TestTenantLifecycleRetainedReferenceCompatibility(t *testing.T) {
	f := newTenantReviewFixture(t)
	seedKnownDisabledTenant(t, f)
	ctx := context.Background()
	// 普通提案和角色/绑定审批保持可用；停用租户原引用保持原值。
	change := f.proposal(t, "another", "Another")
	f.apply(t, change)
	change = f.proposal(t, "another", "Renamed")
	f.apply(t, change)
	snapshot, _, err := f.db.GetAccessPolicySnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	request := model.AccessControlUpdateRequest{Roles: []model.Role{{ID: "custom", DisplayName: "Custom", Permissions: []model.Permission{model.PermissionRead}}}, ExpectedVersion: snapshot.Version, RequiresDualApproval: true, IdempotencyKey: "00000000-0000-4000-8000-000000000801"}
	roleChange, _, err := f.engine.CreateAccessChange(ctx, f.actors["creator"], request)
	if err != nil {
		t.Fatal(err)
	}
	f.apply(t, roleChange)
	snapshot, _, _ = f.db.GetAccessPolicySnapshot(ctx)
	request = model.AccessControlUpdateRequest{Bindings: []model.RoleBinding{{ID: "active-binding", Subject: f.actors["viewer"], TenantID: "another", RoleID: "custom", ObjectIDs: []string{"*"}}}, ExpectedVersion: snapshot.Version, RequiresDualApproval: true, IdempotencyKey: "00000000-0000-4000-8000-000000000802"}
	bindingChange, _, err := f.engine.CreateAccessChange(ctx, f.actors["creator"], request)
	if err != nil {
		t.Fatal(err)
	}
	f.apply(t, bindingChange)
	value, err := f.db.GetTenantLifecycle(ctx, "ordinary")
	if err != nil || value.Generation != 2 || value.Status != "disabled" {
		t.Fatalf("%+v %v", value, err)
	}
	before := f.durableState(t)
	for i, request := range []model.AccessControlUpdateRequest{
		{Tenants: []model.Tenant{{ID: "ordinary", DisplayName: "Revive"}}},
		{Tenants: []model.Tenant{{ID: "bad", DisplayName: "Bad", Status: "other"}}},
		{Principals: []model.AccessPrincipal{{Subject: f.actors["viewer"], TenantID: "ordinary", Roles: []string{"custom"}}}},
		{Bindings: []model.RoleBinding{{ID: "new-disabled", Subject: f.actors["viewer"], TenantID: "ordinary", RoleID: "viewer"}}},
	} {
		request.IdempotencyKey = fmt.Sprintf("00000000-0000-4000-8000-%012d", 900+i)
		if _, err := f.engine.UpdateAccess(ctx, f.actors["creator"], request); err == nil {
			t.Fatal("ordinary update accepted", i)
		}
	}
	if before != f.durableState(t) {
		t.Fatal("rejected request changed database")
	}
	if _, err := f.sql.Exec(`UPDATE tenants SET lifecycle_generation=0 WHERE id='ordinary'`); err != nil {
		t.Fatal(err)
	}
	before = f.durableState(t)
	if _, err := f.engine.UpdateAccess(ctx, f.actors["creator"], model.AccessControlUpdateRequest{Tenants: []model.Tenant{{ID: "another", DisplayName: "Unknown must block"}}, IdempotencyKey: "00000000-0000-4000-8000-000000000999"}); err == nil {
		t.Fatal("unknown disabled reference accepted")
	}
	if before != f.durableState(t) {
		t.Fatal("unknown reference changed database")
	}
}

func TestTenantLifecycleColdStartAndCatalogIsolation(t *testing.T) {
	f := newTenantReviewFixture(t)
	policy := seedKnownDisabledTenant(t, f)
	ctx := context.Background()
	before := f.durableState(t)
	if err := f.engine.seedAccessPolicy(); err != nil {
		t.Fatal(err)
	}
	if before != f.durableState(t) {
		t.Fatal("repeat seed changed lifecycle")
	}
	// 配置入口真实 Load(false)，数据与路径均由本测试生成。
	catalog := *f.engine.catalog
	catalog.Access = cloneAccessPolicy(&policy)
	tenant := catalog.Access.Tenants["ordinary"]
	tenant.Status = "active"
	tenant.DisplayName = "Catalog must not overwrite"
	catalog.Access.Tenants["ordinary"] = tenant
	catalog.Adapters = map[string]model.AdapterDefinition{"synthetic": {Path: "/synthetic/never-executed", AllowedTypes: []string{"service"}}}
	catalog.Services = map[string]model.ServiceDefinition{"synthetic": {
		Name: "synthetic", ObjectID: "service:synthetic", DisplayName: "Synthetic", Description: "本测试合成对象", TenantID: "ordinary", Template: "custom", AdapterRef: "synthetic",
		Metadata: model.ObjectMetadata{Type: "service", Environment: "production", Owner: "test", Criticality: "important", Lifecycle: "active", Maturity: "inspect_only"},
		Actions:  map[string]model.ActionDefinition{"inspect": {Name: "inspect", DisplayName: "检查", Enabled: true, Risk: model.RiskReadOnly, TargetMode: "none", Steps: []string{"inspect"}, TimeoutSeconds: 30, Impact: "无变更", Rollback: "无需回滚", Scope: "合成对象"}},
	}}
	raw, err := json.Marshal(catalog)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "synthetic-catalog.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load(path, false)
	if err != nil {
		t.Fatal(err)
	}
	var databasePath string
	if err := f.sql.QueryRow(`SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&databasePath); err != nil {
		t.Fatal(err)
	}
	f.engine.Stop()
	f.db.Close()
	reopened, err := store.Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	engine, err := NewEngineChecked(loaded, reopened, &fakeExecutor{}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Stop()
	effective, _, err := engine.effectiveAccessPolicy(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(effective.Tenants, policy.Tenants) {
		t.Fatal("catalog replaced durable tenants")
	}
	value, err := reopened.GetTenantLifecycle(ctx, "ordinary")
	if err != nil || value.Generation != 2 || value.Status != "disabled" {
		t.Fatalf("%+v %v", value, err)
	}
	if before != f.durableState(t) {
		t.Fatal("cold start changed lifecycle or history")
	}
}

func TestTenantLifecycleHasNoProductionCaller(t *testing.T) {
	definitions, calls := 0, 0
	err := filepath.WalkDir("../../", func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(node ast.Node) bool {
			if fn, ok := node.(*ast.FuncDecl); ok && fn.Name.Name == "applyTenantLifecycleChange" {
				definitions++
			}
			if sel, ok := node.(*ast.SelectorExpr); ok && sel.Sel.Name == "applyTenantLifecycleChange" {
				calls++
			}
			return true
		})
		return nil
	})
	if err != nil || definitions != 1 || calls != 0 {
		t.Fatalf("definitions=%d references=%d err=%v", definitions, calls, err)
	}
}
