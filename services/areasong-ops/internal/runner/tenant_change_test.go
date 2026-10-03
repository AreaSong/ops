package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/AreaSong/ops/services/areasong-ops/internal/config"
	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	"github.com/AreaSong/ops/services/areasong-ops/internal/store"
)

// 真实 Runner HTTP handler + 临时 SQLite；无生产数据与网络执行器。
func TestTenantProposalHTTPContract(t *testing.T) {
	root := t.TempDir()
	db, err := store.Open(filepath.Join(root, "ops.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	actors := map[string]string{}
	principals := map[string]config.AccessPrincipal{}
	for _, name := range []string{"creator", "approver", "other", "viewer"} {
		actors[name] = config.AccessHashForEmail(name + "@example.test")
		role := "platform-admin"
		if name == "viewer" {
			role = "viewer"
		}
		principals[actors[name]] = config.AccessPrincipal{Subject: actors[name], TenantID: "default", Roles: []string{role}}
	}
	catalog := &config.Catalog{SchemaVersion: 4, Access: &config.AccessPolicy{
		Enforced: true, DefaultTenant: "default", Principals: principals,
		Tenants: map[string]model.Tenant{"default": {ID: "default", DisplayName: "Default", Status: "active"}},
		Roles: map[string]model.Role{
			"platform-admin": {ID: "platform-admin", DisplayName: "Admin", BuiltIn: true, Permissions: []model.Permission{model.Permission("*")}},
			"viewer":         {ID: "viewer", DisplayName: "Viewer", BuiltIn: true, Permissions: []model.Permission{model.PermissionRead}},
		},
	}}
	engine, err := NewEngineChecked(catalog, db, &fakeExecutor{}, root)
	if err != nil {
		t.Fatal(err)
	}
	handler := NewServer(engine, db)
	call := func(actor, path string, body any, status int) model.AccessChange {
		t.Helper()
		data, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(data))
		req.Header.Set(actorHeader, actors[actor])
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		if response.Code != status {
			t.Fatalf("%s %s status=%d want=%d body=%s", actor, path, response.Code, status, response.Body.String())
		}
		var change model.AccessChange
		_ = json.Unmarshal(response.Body.Bytes(), &change)
		return change
	}
	view := func() model.AccessControlView {
		t.Helper()
		v, e := engine.AccessControl(context.Background(), actors["creator"])
		if e != nil {
			t.Fatal(e)
		}
		return v
	}
	initial := view()
	seq := 0
	proposal := func(id, name string, version int64) (model.AccessChange, map[string]any) {
		t.Helper()
		seq++
		body := map[string]any{"tenants": []map[string]string{{"id": id, "displayName": name, "status": "active"}}, "expectedVersion": version, "requiresDualApproval": true, "idempotencyKey": fmt.Sprintf("00000000-0000-4000-8000-%012d", seq)}
		call("viewer", "/v1/access/changes", body, http.StatusForbidden)
		change := call("creator", "/v1/access/changes", body, http.StatusAccepted)
		replay := call("creator", "/v1/access/changes", body, http.StatusOK)
		if replay.ID != change.ID {
			t.Fatal("duplicate proposal")
		}
		return change, body
	}
	approve := func(change model.AccessChange) {
		t.Helper()
		body := map[string]string{"digest": change.RequestDigest, "confirmation": change.ConfirmationPhrase}
		path := "/v1/access/changes/" + change.ID + "/approve"
		call("creator", path, body, http.StatusConflict)
		call("viewer", path, body, http.StatusForbidden)
		call("approver", path, map[string]string{"digest": "tampered", "confirmation": change.ConfirmationPhrase}, http.StatusConflict)
		got := call("approver", path, body, http.StatusOK)
		if got.State != model.AccessChangeApproved {
			t.Fatal(got.State)
		}
	}
	apply := func(change model.AccessChange) {
		t.Helper()
		path := "/v1/access/changes/" + change.ID + "/apply"
		call("approver", path, map[string]string{}, http.StatusConflict)
		call("other", path, map[string]string{}, http.StatusConflict)
		call("viewer", path, map[string]string{}, http.StatusForbidden)
		got := call("creator", path, map[string]string{}, http.StatusOK)
		if got.State != model.AccessChangeApplied {
			t.Fatal(got.State)
		}
		call("creator", path, map[string]string{}, http.StatusOK)
	}
	created, body := proposal("tenant-one", "One", initial.Version)
	before := view()
	if before.Version != initial.Version || !reflect.DeepEqual(before.Tenants, initial.Tenants) {
		t.Fatal("proposal mutated policy")
	}
	stored, payload, e := db.GetAccessChangeWithPayload(context.Background(), created.ID)
	if e != nil || stored.RequestDigest != created.RequestDigest {
		t.Fatal(e)
	}
	var raw map[string]any
	_ = json.Unmarshal([]byte(payload), &raw)
	if len(raw) != 4 || raw["expectedVersion"] != float64(initial.Version) {
		t.Fatalf("unexpected payload %s", payload)
	}
	body["tenants"] = []map[string]string{{"id": "tenant-one", "displayName": "Tampered", "status": "active"}}
	call("creator", "/v1/access/changes", body, http.StatusConflict)
	approve(created)
	apply(created)
	afterCreate := view()
	if afterCreate.Version != initial.Version+1 {
		t.Fatal("replay incremented version")
	}
	entries, err := db.ListAudit(context.Background(), 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, entry := range entries {
		if entry.Resource == "access/"+created.ID {
			counts[entry.Event]++
		}
	}
	for _, event := range []string{"access.change.created", "access.change.approved", "access.change.applied"} {
		if counts[event] != 1 {
			t.Fatalf("%s audit count=%d, want 1", event, counts[event])
		}
	}
	edited, _ := proposal("tenant-one", "Renamed", afterCreate.Version)
	approve(edited)
	apply(edited)
	afterEdit := view()
	if !reflect.DeepEqual(afterEdit.Roles, initial.Roles) || !reflect.DeepEqual(afterEdit.Bindings, initial.Bindings) || !reflect.DeepEqual(afterEdit.Principals, initial.Principals) || !afterEdit.Enforced || afterEdit.DefaultTenant != initial.DefaultTenant {
		t.Fatal("unrelated policy changed")
	}
	for _, tenant := range afterEdit.Tenants {
		if tenant.ID == "tenant-one" && (tenant.DisplayName != "Renamed" || tenant.Status != "active") {
			t.Fatal(tenant)
		}
		if tenant.ID == "default" && !reflect.DeepEqual(tenant, initial.Tenants[0]) {
			t.Fatal("protected tenant changed")
		}
	}
	stale, _ := proposal("tenant-one", "Stale", afterEdit.Version)
	approve(stale)
	fresh, _ := proposal("tenant-two", "Two", afterEdit.Version)
	approve(fresh)
	apply(fresh)
	current := view()
	call("creator", "/v1/access/changes/"+stale.ID+"/apply", map[string]string{}, http.StatusConflict)
	if got := view(); got.Version != current.Version || !reflect.DeepEqual(got.Tenants, current.Tenants) {
		t.Fatal("stale change overwrote current policy")
	}
	protected, _ := proposal("default", "Overwrite", current.Version)
	approve(protected)
	call("creator", "/v1/access/changes/"+protected.ID+"/apply", map[string]string{}, http.StatusConflict)
	// 严格 JSON 解码拒绝未知字段，审批创建本身不冒充字段已通过应用校验。
	call("creator", "/v1/access/changes", map[string]any{"tenants": []map[string]string{{"id": "x", "displayName": "X", "unknown": "bad"}}}, http.StatusBadRequest)
	for _, tenant := range []map[string]string{{"id": "tenant-bad", "displayName": " "}, {"id": "tenant-bad", "displayName": "Bad", "status": "disabled"}} {
		seq++
		b := map[string]any{"tenants": []map[string]string{tenant}, "expectedVersion": current.Version, "idempotencyKey": fmt.Sprintf("00000000-0000-4000-8000-%012d", seq)}
		c := call("creator", "/v1/access/changes", b, http.StatusAccepted)
		approve(c)
		call("creator", "/v1/access/changes/"+c.ID+"/apply", map[string]string{}, http.StatusConflict)
	}
}
