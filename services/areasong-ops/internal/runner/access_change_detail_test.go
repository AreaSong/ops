package runner

import (
	"bytes"
	"context"
	"database/sql"
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

type tenantReviewFixture struct {
	engine  *Engine
	db      *store.Store
	sql     *sql.DB
	handler http.Handler
	actors  map[string]string
	seq     int
}

func newTenantReviewFixture(t *testing.T) *tenantReviewFixture {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "ops.db")
	db, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	actors := map[string]string{}
	principals := map[string]config.AccessPrincipal{}
	for _, name := range []string{"creator", "approver", "other", "viewer", "tenant-manager"} {
		actors[name] = config.AccessHashForEmail(name + "@example.test")
		role, tenant := "platform-admin", "default"
		if name == "viewer" {
			role = "viewer"
		}
		if name == "tenant-manager" {
			role, tenant = "manager", "isolated"
		}
		principals[actors[name]] = config.AccessPrincipal{Subject: actors[name], TenantID: tenant, Roles: []string{role}}
	}
	catalog := &config.Catalog{SchemaVersion: 4, Access: &config.AccessPolicy{
		Enforced: true, DefaultTenant: "default", Principals: principals,
		Tenants: map[string]model.Tenant{"default": {ID: "default", DisplayName: "Default", Status: "active"}, "isolated": {ID: "isolated", DisplayName: "Isolated", Status: "active"}},
		Roles: map[string]model.Role{
			"platform-admin": {ID: "platform-admin", DisplayName: "Admin", BuiltIn: true, Permissions: []model.Permission{model.Permission("*")}},
			"viewer":         {ID: "viewer", DisplayName: "Viewer", BuiltIn: true, Permissions: []model.Permission{model.PermissionRead}},
			"manager":        {ID: "manager", DisplayName: "Manager", Permissions: []model.Permission{model.PermissionManageAccess, model.PermissionRead}},
		},
	}}
	engine, err := NewEngineChecked(catalog, db, &fakeExecutor{}, root)
	if err != nil {
		t.Fatal(err)
	}
	return &tenantReviewFixture{engine: engine, db: db, sql: conn, handler: NewServer(engine, db), actors: actors}
}

func (f *tenantReviewFixture) call(t *testing.T, actor, method, path string, body any, status int) []byte {
	t.Helper()
	data, _ := json.Marshal(body)
	req := httptest.NewRequest(method, path, bytes.NewReader(data))
	req.Header.Set(actorHeader, f.actors[actor])
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	f.handler.ServeHTTP(response, req)
	if response.Code != status {
		t.Fatalf("%s %s: %d want %d: %s", actor, path, response.Code, status, response.Body.String())
	}
	if method == http.MethodGet && response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("cacheable detail")
	}
	return response.Body.Bytes()
}
func (f *tenantReviewFixture) proposal(t *testing.T, tenant, name string) model.AccessChange {
	t.Helper()
	snapshot, _, err := f.db.GetAccessPolicySnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	f.seq++
	body := model.AccessControlUpdateRequest{Tenants: []model.Tenant{{ID: tenant, DisplayName: name, Status: "active"}}, ExpectedVersion: snapshot.Version, RequiresDualApproval: true, IdempotencyKey: fmt.Sprintf("00000000-0000-4000-8000-%012d", f.seq)}
	var change model.AccessChange
	json.Unmarshal(f.call(t, "creator", "POST", "/v1/access/changes", body, 202), &change)
	return change
}
func (f *tenantReviewFixture) detail(t *testing.T, actor string, change model.AccessChange) accessChangeDetail {
	t.Helper()
	raw := f.call(t, actor, "GET", "/v1/access/changes/"+change.ID+"/detail", nil, 200)
	var detail accessChangeDetail
	if err := json.Unmarshal(raw, &detail); err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	json.Unmarshal(raw, &fields)
	allowed := map[string]bool{"id": true, "requestDigest": true, "state": true, "expectedVersion": true, "currentVersion": true, "kind": true, "availability": true, "operation": true, "before": true, "after": true, "reviewerHash": true, "binding": true}
	for key := range fields {
		if !allowed[key] {
			t.Fatalf("unexpected field %s", key)
		}
	}
	for _, key := range []string{"before", "after"} {
		if val, ok := fields[key]; ok {
			var tenant map[string]any
			json.Unmarshal(val, &tenant)
			if len(tenant) != 3 || tenant["id"] == nil || tenant["displayName"] == nil || tenant["status"] == nil {
				t.Fatalf("unexpected tenant fields %v", tenant)
			}
		}
	}
	if detail.ID != change.ID || detail.RequestDigest != change.RequestDigest || detail.ReviewerHash != f.actors[actor] {
		t.Fatal("unbound detail")
	}
	return detail
}
func (f *tenantReviewFixture) apply(t *testing.T, change model.AccessChange) {
	t.Helper()
	path := "/v1/access/changes/" + change.ID
	body := model.AccessChangeApprovalRequest{Digest: change.RequestDigest, Confirmation: change.ConfirmationPhrase}
	f.call(t, "creator", "POST", path+"/approve", body, 409)
	f.call(t, "approver", "POST", path+"/approve", model.AccessChangeApprovalRequest{Digest: "wrong", Confirmation: change.ConfirmationPhrase}, 409)
	f.call(t, "approver", "POST", path+"/approve", body, 200)
	f.call(t, "approver", "POST", path+"/approve", body, 200)
	f.call(t, "approver", "POST", path+"/apply", nil, 409)
	f.call(t, "other", "POST", path+"/apply", nil, 409)
	f.call(t, "creator", "POST", path+"/apply", nil, 200)
	f.call(t, "creator", "POST", path+"/apply", nil, 200)
}

// 对所有持久化表作内容快照，而非仅比较行数，捕获任何读路径写入。
func (f *tenantReviewFixture) durableState(t *testing.T) string {
	t.Helper()
	rows, err := f.sql.Query(`SELECT name FROM sqlite_master WHERE type='table' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for rows.Next() {
		var n string
		rows.Scan(&n)
		names = append(names, n)
	}
	rows.Close()
	all := map[string][][]any{}
	for _, name := range names {
		rows, err := f.sql.Query(`SELECT * FROM "` + name + `" ORDER BY rowid`)
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			values := make([]any, len(cols))
			ptr := make([]any, len(cols))
			for i := range values {
				ptr[i] = &values[i]
			}
			if err := rows.Scan(ptr...); err != nil {
				t.Fatal(err)
			}
			all[name] = append(all[name], values)
		}
		rows.Close()
	}
	data, _ := json.Marshal(all)
	return string(data)
}
func TestTenantChangeDetailHTTP(t *testing.T) {
	f := newTenantReviewFixture(t)
	c := f.proposal(t, " tenant-one ", " One ")
	before := f.durableState(t)
	a := f.detail(t, "creator", c)
	b := f.detail(t, "approver", c)
	a.ReviewerHash = b.ReviewerHash
	if !reflect.DeepEqual(a, b) || a.Availability != "ready" || a.Operation != "create" || a.Before != nil || a.After.ID != "tenant-one" || a.After.DisplayName != "One" {
		t.Fatal(a, b)
	}
	f.detail(t, "approver", c)
	for _, actor := range []string{"viewer", "tenant-manager", "unknown"} {
		status := 403
		if actor == "unknown" {
			status = 401
		}
		for _, id := range []string{c.ID, "missing"} {
			f.call(t, actor, "GET", "/v1/access/changes/"+id+"/detail", nil, status)
		}
	}
	f.call(t, "approver", "GET", "/v1/access/changes/missing/detail", nil, 404)
	if f.durableState(t) != before {
		t.Fatal("detail mutated persistent state")
	}
	f.apply(t, c)
	edit := f.proposal(t, "tenant-one", " Renamed ")
	detail := f.detail(t, "approver", edit)
	if detail.Operation != "rename" || detail.Before.DisplayName != "One" || detail.After.DisplayName != "Renamed" {
		t.Fatal(detail)
	}
	f.apply(t, edit)
	policy, _, err := f.engine.effectiveAccessPolicy(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if policy.Tenants["tenant-one"].DisplayName != detail.After.DisplayName {
		t.Fatal("projection differs from application")
	}
	stale := f.detail(t, "approver", edit)
	if stale.Availability != "stale" || stale.Before != nil || stale.After != nil {
		t.Fatal(stale)
	}
	drift := f.proposal(t, "tenant-one", "Future")
	other := f.proposal(t, "tenant-two", "Two")
	f.apply(t, other)
	if got := f.detail(t, "approver", drift); got.Availability != "stale" || got.Before != nil {
		t.Fatal(got)
	}
	path := "/v1/access/changes/" + drift.ID
	f.call(t, "approver", "POST", path+"/approve", model.AccessChangeApprovalRequest{Digest: drift.RequestDigest, Confirmation: drift.ConfirmationPhrase}, 200)
	f.call(t, "creator", "POST", path+"/apply", nil, 409)
	// 通过现有策略更新撤回测试身份；详情不得借历史快照继续放行。
	snapshot, _, _ := f.db.GetAccessPolicySnapshot(context.Background())
	policy, _, _ = f.engine.effectiveAccessPolicy(context.Background())
	p := policy.Principals[f.actors["approver"]]
	p.Roles = []string{"viewer"}
	policy.Principals[f.actors["approver"]] = p
	raw, _ := json.Marshal(policy)
	_, err = f.db.SaveAccessPolicySnapshot(context.Background(), model.AccessPolicySnapshot{Digest: digestText(string(raw)), PolicyJSON: string(raw), ActorHash: f.actors["creator"]}, snapshot.Version)
	if err != nil {
		t.Fatal(err)
	}
	f.call(t, "approver", "GET", "/v1/access/changes/"+c.ID+"/detail", nil, 403)
}

func TestTenantChangeDetailUnsupported(t *testing.T) {
	f := newTenantReviewFixture(t)
	c := f.proposal(t, "tenant-one", "One")
	_, original, _ := f.db.GetAccessChangeWithPayload(context.Background(), c.ID)
	cases := []struct {
		name      string
		modify    func(map[string]any)
		digestBad bool
		want      string
	}{
		{"mixed", func(v map[string]any) { v["removeRoleIds"] = []string{"viewer"} }, false, "unsupported"},
		{"unknown", func(v map[string]any) { v["future"] = true }, false, "unsupported"},
		{"historical", func(v map[string]any) { delete(v, "expectedVersion") }, false, "unavailable"},
		{"missing snapshot", func(v map[string]any) { v["expectedVersion"] = 999 }, false, "unavailable"},
		{"digest", func(v map[string]any) {}, true, "unsupported"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var request map[string]any
			json.Unmarshal([]byte(original), &request)
			tc.modify(request)
			// 已知模型格式按真实服务端编码；未知字段刻意保留模拟历史记录。
			raw, _ := json.Marshal(request)
			if tc.name != "unknown" {
				var typed model.AccessControlUpdateRequest
				json.Unmarshal(raw, &typed)
				raw, _ = json.Marshal(typed)
			}
			digest := digestText(string(raw))
			if tc.digestBad {
				digest = "broken"
			}
			_, err := f.sql.Exec(`UPDATE access_changes SET payload_json=?,request_digest=? WHERE id=?`, string(raw), digest, c.ID)
			if err != nil {
				t.Fatal(err)
			}
			changed := c
			changed.RequestDigest = digest
			before := f.durableState(t)
			detail := f.detail(t, "approver", changed)
			if detail.Availability != tc.want || detail.Before != nil || detail.After != nil {
				t.Fatal(detail)
			}
			if f.durableState(t) != before {
				t.Fatal("write on unsupported read")
			}
		})
	}
}

func TestTenantChangeDetailScope(t *testing.T) {
	f := newTenantReviewFixture(t)
	for _, id := range []string{"default", "isolated"} {
		change := f.proposal(t, id, "Changed")
		if detail := f.detail(t, "approver", change); detail.Availability != "unsupported" || detail.Before != nil || detail.After != nil {
			t.Fatal(detail)
		}
	}
	current, _, _ := f.db.GetAccessPolicySnapshot(context.Background())
	cases := []struct {
		name    string
		request model.AccessControlUpdateRequest
		kind    string
	}{
		{"multiple", model.AccessControlUpdateRequest{Tenants: []model.Tenant{{ID: "one", DisplayName: "One"}, {ID: "two", DisplayName: "Two"}}}, "unsupported"},
		{"status", model.AccessControlUpdateRequest{Tenants: []model.Tenant{{ID: "one", DisplayName: "One", Status: "suspended"}}}, "tenant"},
		{"metadata", model.AccessControlUpdateRequest{Tenants: []model.Tenant{{ID: "one", DisplayName: "One", CreatedBy: "hidden"}}}, "tenant"},
		{"binding", model.AccessControlUpdateRequest{Bindings: []model.RoleBinding{{ID: "binding", Subject: f.actors["viewer"], TenantID: "default", RoleID: "viewer"}}}, "binding"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f.seq++
			r := tc.request
			r.ExpectedVersion = current.Version
			r.RequiresDualApproval = true
			r.IdempotencyKey = fmt.Sprintf("00000000-0000-4000-8000-%012d", f.seq)
			var change model.AccessChange
			json.Unmarshal(f.call(t, "creator", "POST", "/v1/access/changes", r, 202), &change)
			detail := f.detail(t, "approver", change)
			availability := "unsupported"
			if tc.name == "binding" {
				availability = "ready"
			}
			if detail.Kind != tc.kind || detail.Availability != availability || detail.Before != nil || detail.After != nil {
				t.Fatal(detail)
			}
			if tc.name == "binding" {
				f.apply(t, change)
			}
		})
	}
}
