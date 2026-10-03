package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/config"
	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

// 复用临时 SQLite 和身份夹具，所有操作经真实回环 HTTP 到 Runner。
func newRoleReviewFixture(t *testing.T) *tenantReviewFixture {
	t.Helper()
	f := newTenantReviewFixture(t)
	server := httptest.NewServer(f.handler)
	t.Cleanup(server.Close)
	f.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req, err := http.NewRequestWithContext(r.Context(), r.Method, server.URL+r.URL.RequestURI(), r.Body)
		if err != nil {
			t.Fatal(err)
		}
		req.Header = r.Header.Clone()
		response, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		for key, values := range response.Header {
			w.Header()[key] = values
		}
		w.WriteHeader(response.StatusCode)
		if _, err := io.Copy(w, response.Body); err != nil {
			t.Fatal(err)
		}
	})
	return f
}

func roleRequest(id, name string, permissions ...model.Permission) model.AccessControlUpdateRequest {
	return model.AccessControlUpdateRequest{Roles: []model.Role{{ID: id, DisplayName: name, Permissions: permissions}}}
}

func submitReviewRequest(t *testing.T, f *tenantReviewFixture, r model.AccessControlUpdateRequest) model.AccessChange {
	t.Helper()
	snapshot, found, err := f.db.GetAccessPolicySnapshot(context.Background())
	if err != nil || !found {
		t.Fatalf("snapshot: %v", err)
	}
	f.seq++
	r.ExpectedVersion, r.RequiresDualApproval = snapshot.Version, true
	r.IdempotencyKey = fmt.Sprintf("00000000-0000-4000-8000-%012d", f.seq)
	var change model.AccessChange
	if err := json.Unmarshal(f.call(t, "creator", "POST", "/v1/access/changes", r, 202), &change); err != nil {
		t.Fatal(err)
	}
	return change
}

func readRoleDetail(t *testing.T, f *tenantReviewFixture, change model.AccessChange) accessChangeDetail {
	t.Helper()
	raw := f.call(t, "approver", "GET", "/v1/access/changes/"+change.ID+"/detail", nil, 200)
	var detail accessChangeDetail
	if err := json.Unmarshal(raw, &detail); err != nil {
		t.Fatal(err)
	}
	if detail.ID != change.ID || detail.RequestDigest != change.RequestDigest || detail.ReviewerHash != f.actors["approver"] {
		t.Fatal("unbound response")
	}
	assertReviewFields(t, raw, "reviewerHash id requestDigest state expectedVersion currentVersion kind availability operation role reason")
	if detail.Before != nil || detail.After != nil {
		t.Fatal("role rendered as tenant")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if role, ok := fields["role"]; ok {
		assertReviewFields(t, role, "before after permissionDiff impact")
		var projected map[string]json.RawMessage
		if err := json.Unmarshal(role, &projected); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"before", "after"} {
			if value, ok := projected[key]; ok {
				assertReviewFields(t, value, "id displayName permissions")
			}
		}
		assertReviewFields(t, projected["permissionDiff"], "added removed unchanged")
		assertReviewFields(t, projected["impact"], "bindingCount tenantCount affectsExistingBindings")
	}
	for _, forbidden := range []string{"@example.test", f.actors["viewer"], "payload_json", "objectIds", "createdBy", "principals", "bindings"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("leaked %s", forbidden)
		}
	}
	return detail
}

func assertReviewFields(t *testing.T, raw []byte, allowed string) {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	for key := range fields {
		if !strings.Contains(" "+allowed+" ", " "+key+" ") {
			t.Fatalf("unexpected field %s", key)
		}
	}
}

func assertRoleApplied(t *testing.T, f *tenantReviewFixture, change model.AccessChange, detail accessChangeDetail) {
	t.Helper()
	f.apply(t, change)
	policy, _, err := f.engine.effectiveAccessPolicy(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	actual := policy.Roles[detail.Role.After.ID]
	if !reflect.DeepEqual(roleReviewValue{actual.ID, actual.DisplayName, actual.Permissions}, detail.Role.After) {
		t.Fatal("snapshot differs from reviewed proposal")
	}
	roles, err := f.db.ListRoles(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, role := range roles {
		if role.ID == actual.ID {
			if !reflect.DeepEqual(roleReviewValue{role.ID, role.DisplayName, role.Permissions}, detail.Role.After) {
				t.Fatal("stored role differs from review")
			}
			return
		}
	}
	t.Fatal("role missing in durable rows")
}

func TestRoleChangeDetailHTTP(t *testing.T) {
	f := newRoleReviewFixture(t)
	create := submitReviewRequest(t, f, roleRequest(" CuStOm ", " Original ", model.PermissionRead, model.PermissionInspect))
	unchanged := f.durableState(t)
	detail := readRoleDetail(t, f, create)
	if detail.Kind != "role" || detail.Availability != "ready" || detail.Operation != "create" || detail.Role.Before != nil || detail.Role.After.ID != "custom" || detail.Role.After.DisplayName != "Original" {
		t.Fatalf("%+v", detail)
	}
	if !reflect.DeepEqual(detail.Role.Impact, roleReviewImpact{}) || !reflect.DeepEqual(detail.Role.PermissionDiff, rolePermissionDiff{[]model.Permission{model.PermissionInspect, model.PermissionRead}, []model.Permission{}, []model.Permission{}}) {
		t.Fatalf("%+v", detail.Role)
	}
	readRoleDetail(t, f, create)
	if f.durableState(t) != unchanged {
		t.Fatal("GET changed persistent tables")
	}
	policy, _, _ := f.engine.effectiveAccessPolicy(context.Background())
	if _, exists := policy.Roles["custom"]; exists {
		t.Fatal("read applied role")
	}
	assertRoleApplied(t, f, create, detail)
	rename := submitReviewRequest(t, f, roleRequest("custom", " Renamed ", model.PermissionRead, model.PermissionInspect))
	detail = readRoleDetail(t, f, rename)
	if detail.Operation != "edit" || detail.Role.Before.DisplayName != "Original" || detail.Role.After.DisplayName != "Renamed" || len(detail.Role.PermissionDiff.Added) != 0 || len(detail.Role.PermissionDiff.Removed) != 0 {
		t.Fatalf("%+v", detail.Role)
	}
	assertRoleApplied(t, f, rename, detail)
	expired := time.Now().Add(-time.Hour).UTC()
	bindings := []model.RoleBinding{
		{ID: "one", Subject: f.actors["viewer"], TenantID: "default", RoleID: "custom", ObjectIDs: []string{"private-object"}},
		{ID: "two", Subject: f.actors["viewer"], TenantID: "default", RoleID: "custom", ExpiresAt: &expired},
		{ID: "three", Subject: f.actors["tenant-manager"], TenantID: "isolated", RoleID: "custom"},
	}
	f.apply(t, submitReviewRequest(t, f, model.AccessControlUpdateRequest{Bindings: bindings}))
	edit := submitReviewRequest(t, f, roleRequest("custom", "Renamed", model.PermissionInspect, model.PermissionDeploy))
	unchanged = f.durableState(t)
	detail = readRoleDetail(t, f, edit)
	if !reflect.DeepEqual(detail.Role.PermissionDiff, rolePermissionDiff{[]model.Permission{model.PermissionDeploy}, []model.Permission{model.PermissionRead}, []model.Permission{model.PermissionInspect}}) || detail.Role.Impact != (roleReviewImpact{3, 2, true}) {
		t.Fatalf("%+v", detail.Role)
	}
	// 当前行表故意与版本快照不同，证明统计只取可信基准，不读当前绑定行。
	if _, err := f.sql.Exec(`UPDATE role_bindings SET tenant_id='isolated' WHERE id='one'`); err != nil {
		t.Fatal(err)
	}
	afterInjection := f.durableState(t)
	if got := readRoleDetail(t, f, edit); !reflect.DeepEqual(detail, got) {
		t.Fatal("detail used live rows instead of baseline")
	}
	if f.durableState(t) != afterInjection {
		t.Fatal("read wrote rows")
	}
	if _, err := f.sql.Exec(`UPDATE role_bindings SET tenant_id='default' WHERE id='one'`); err != nil {
		t.Fatal(err)
	}
	if f.durableState(t) != unchanged {
		t.Fatal("fixture not restored")
	}
	assertRoleApplied(t, f, edit, detail)
	order := submitReviewRequest(t, f, roleRequest("custom", "Renamed", model.PermissionDeploy, model.PermissionInspect, model.PermissionDeploy))
	detail = readRoleDetail(t, f, order)
	if len(detail.Role.PermissionDiff.Added) != 0 || len(detail.Role.PermissionDiff.Removed) != 0 || len(detail.Role.PermissionDiff.Unchanged) != 2 {
		t.Fatal("order or duplicates caused false permission diff")
	}
	assertRoleApplied(t, f, order, detail)
	got, err := f.db.ListRoleBindings(context.Background())
	if err != nil || len(got) != 3 {
		t.Fatal("role edit rewrote bindings", err)
	}
}

func TestRoleChangeDetailRejectedRequests(t *testing.T) {
	cases := []struct {
		name   string
		modify func(*model.AccessControlUpdateRequest)
	}{
		{"tenant mix", func(r *model.AccessControlUpdateRequest) { r.Tenants = []model.Tenant{{ID: "new", DisplayName: "New"}} }},
		{"binding mix", func(r *model.AccessControlUpdateRequest) { r.Bindings = []model.RoleBinding{{ID: "binding"}} }},
		{"principal mix", func(r *model.AccessControlUpdateRequest) { r.Principals = []model.AccessPrincipal{{Subject: "hidden"}} }},
		{"multiple", func(r *model.AccessControlUpdateRequest) { r.Roles = append(r.Roles, r.Roles[0]) }},
		{"delete", func(r *model.AccessControlUpdateRequest) { r.Roles = nil; r.RemoveRoleIDs = []string{"old"} }},
		{"replace ID", func(r *model.AccessControlUpdateRequest) { r.RemoveRoleIDs = []string{"old"} }},
		{"remove tenant", func(r *model.AccessControlUpdateRequest) { r.RemoveTenantIDs = []string{"old"} }},
		{"remove binding", func(r *model.AccessControlUpdateRequest) { r.RemoveBindingIDs = []string{"old"} }},
		{"remove principal", func(r *model.AccessControlUpdateRequest) { r.RemovePrincipalSubjects = []string{"old"} }},
		{"enforced", func(r *model.AccessControlUpdateRequest) { v := true; r.Enforced = &v }},
		{"confirmation", func(r *model.AccessControlUpdateRequest) { r.Confirmation = "legacy" }},
		{"built in", func(r *model.AccessControlUpdateRequest) { r.Roles[0].BuiltIn = true }},
		{"bootstrap", func(r *model.AccessControlUpdateRequest) { r.Roles[0].ID = "manager" }},
		{"default viewer", func(r *model.AccessControlUpdateRequest) { r.Roles[0].ID = "viewer" }},
		{"default operator absent from snapshot", func(r *model.AccessControlUpdateRequest) { r.Roles[0].ID = "operator" }},
		{"default release", func(r *model.AccessControlUpdateRequest) { r.Roles[0].ID = "release-manager" }},
		{"default admin", func(r *model.AccessControlUpdateRequest) { r.Roles[0].ID = "platform-admin" }},
		{"wildcard", func(r *model.AccessControlUpdateRequest) { r.Roles[0].Permissions = []model.Permission{"*"} }},
		{"pattern", func(r *model.AccessControlUpdateRequest) { r.Roles[0].Permissions = []model.Permission{"ops.*"} }},
		{"unknown", func(r *model.AccessControlUpdateRequest) { r.Roles[0].Permissions = []model.Permission{"future"} }},
		{"empty permissions", func(r *model.AccessControlUpdateRequest) { r.Roles[0].Permissions = nil }},
		{"empty ID", func(r *model.AccessControlUpdateRequest) { r.Roles[0].ID = " " }},
		{"empty name", func(r *model.AccessControlUpdateRequest) { r.Roles[0].DisplayName = " " }},
		{"metadata", func(r *model.AccessControlUpdateRequest) { r.Roles[0].CreatedBy = "hidden" }},
		{"timestamp", func(r *model.AccessControlUpdateRequest) { r.Roles[0].CreatedAt = time.Now().UTC() }},
	}
	f := newRoleReviewFixture(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := roleRequest("custom", "Custom", model.PermissionRead)
			tc.modify(&request)
			var change model.AccessChange
			switch tc.name {
			case "wildcard", "pattern", "unknown", "empty permissions":
				// 新写入已拒绝这些值；继续以旧版本合成记录核验历史只读合同。
				change = saveHistoricalRoleRequest(t, f, request)
			default:
				change = submitReviewRequest(t, f, request)
			}
			before := f.durableState(t)
			detail := readRoleDetail(t, f, change)
			if detail.Availability != "unsupported" || detail.Kind == "other" || detail.Role != nil || detail.Operation != "" {
				t.Fatalf("%+v", detail)
			}
			if f.durableState(t) != before {
				t.Fatal("unsupported GET wrote state")
			}
		})
	}
}

func TestRoleChangeDetailAuthorizationAndDrift(t *testing.T) {
	f := newRoleReviewFixture(t)
	change := submitReviewRequest(t, f, roleRequest("custom", "Custom", model.PermissionRead))
	before := f.durableState(t)
	for _, actor := range []string{"viewer", "tenant-manager", "unknown"} {
		status := 403
		if actor == "unknown" {
			status = 401
		}
		for _, id := range []string{change.ID, "missing"} {
			f.call(t, actor, "GET", "/v1/access/changes/"+id+"/detail", nil, status)
		}
	}
	f.call(t, "approver", "GET", "/v1/access/changes/missing/detail", nil, 404)
	if f.durableState(t) != before {
		t.Fatal("denied GET wrote state")
	}
	f.apply(t, f.proposal(t, "new-tenant", "New"))
	detail := readRoleDetail(t, f, change)
	if detail.Availability != "stale" || detail.Role != nil || detail.CurrentVersion == detail.ExpectedVersion {
		t.Fatalf("%+v", detail)
	}
	f.call(t, "approver", "POST", "/v1/access/changes/"+change.ID+"/approve", model.AccessChangeApprovalRequest{Digest: change.RequestDigest, Confirmation: change.ConfirmationPhrase}, 200)
	f.call(t, "creator", "POST", "/v1/access/changes/"+change.ID+"/apply", nil, 409)
	for _, mode := range []string{"revoked", "expired", "disabled"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newRoleReviewFixture(t)
			c := submitReviewRequest(t, fixture, roleRequest("custom", "Custom", model.PermissionRead))
			mutateReviewBaseline(t, fixture, func(p *config.AccessPolicy) {
				principal := p.Principals[fixture.actors["approver"]]
				switch mode {
				case "revoked":
					principal.Roles = []string{"viewer"}
				case "expired":
					v := time.Now().Add(-time.Hour).UTC()
					principal.ExpiresAt = &v
				case "disabled":
					principal.Status = "disabled"
				}
				p.Principals[fixture.actors["approver"]] = principal
			})
			before := fixture.durableState(t)
			fixture.call(t, "approver", "GET", "/v1/access/changes/"+c.ID+"/detail", nil, 403)
			if fixture.durableState(t) != before {
				t.Fatal("denied GET wrote state")
			}
		})
	}
}

// 仅故障注入临时数据库：保持摘要一致，以区分历史合同拒绝和摘要拒绝。
func mutateReviewBaseline(t *testing.T, f *tenantReviewFixture, modify func(*config.AccessPolicy)) {
	t.Helper()
	policy, snapshot, err := f.engine.effectiveAccessPolicy(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	modify(policy)
	raw, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.sql.Exec(`UPDATE access_policy_snapshots SET policy_json=?,digest=? WHERE version=?`, string(raw), digestText(string(raw)), snapshot.Version); err != nil {
		t.Fatal(err)
	}
}
