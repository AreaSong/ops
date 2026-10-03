package runner

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	"github.com/AreaSong/ops/services/areasong-ops/internal/store"
)

func mixedRoleRequest(f *tenantReviewFixture, roles []model.Role) model.AccessControlUpdateRequest {
	return model.AccessControlUpdateRequest{
		Roles: roles, Tenants: []model.Tenant{{ID: "new-tenant", DisplayName: "New"}},
		Bindings: []model.RoleBinding{{ID: "new-binding", Subject: f.actors["viewer"], TenantID: "new-tenant", RoleID: "good"}},
	}
}

func TestRoleWriteMixedRequestsAtomic(t *testing.T) {
	good := model.Role{ID: "good", DisplayName: "Good", Permissions: []model.Permission{model.PermissionRead}}
	bad := model.Role{ID: "bad", DisplayName: "Bad", Permissions: []model.Permission{"*"}}
	duplicate := bad
	duplicate.ID = " GOOD "
	for _, tc := range []struct {
		name  string
		roles []model.Role
	}{
		{"invalid last", []model.Role{good, bad}},
		{"invalid first", []model.Role{bad, good}},
		{"overwritten invalid", []model.Role{duplicate, good}},
		{"overwrite valid", []model.Role{good, duplicate}},
	} {
		for _, stage := range []string{"create", "direct", "pending", "approved"} {
			t.Run(tc.name+"/"+stage, func(t *testing.T) {
				f := newRoleReviewFixture(t)
				r := mixedRoleRequest(f, tc.roles)
				if stage == "create" {
					assertRoleWriteRejected(t, f, "POST", "/v1/access/changes", versionedRoleRequest(t, f, r))
					return
				}
				if stage == "direct" {
					f.engine.catalog.SchemaVersion = 3
					r = versionedRoleRequest(t, f, r)
					r.RequiresDualApproval = false
					assertRoleWriteRejected(t, f, "PUT", "/v1/access", r)
					return
				}
				assertHistoricalRoleRejection(t, f, r, stage == "approved")
			})
		}
	}
}

func assertHistoricalRoleRejection(t *testing.T, f *tenantReviewFixture, r model.AccessControlUpdateRequest, preapproved bool) {
	t.Helper()
	c := saveHistoricalRoleRequest(t, f, r)
	path := "/v1/access/changes/" + c.ID
	if preapproved {
		if _, err := f.db.ApproveAccessChange(context.Background(), c.ID, f.actors["approver"], c.RequestDigest, c.ConfirmationPhrase); err != nil {
			t.Fatal(err)
		}
	} else {
		f.call(t, "creator", "POST", path+"/apply", nil, 409)
		f.call(t, "approver", "POST", path+"/approve", model.AccessChangeApprovalRequest{Digest: c.RequestDigest, Confirmation: c.ConfirmationPhrase}, 200)
	}
	_, payload, err := f.db.GetAccessChangeWithPayload(context.Background(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertRoleWriteRejected(t, f, "POST", path+"/apply", nil)
	stored, after, err := f.db.GetAccessChangeWithPayload(context.Background(), c.ID)
	if err != nil || stored.State != model.AccessChangeApproved || stored.RequestDigest != c.RequestDigest || after != payload || stored.AppliedAt != nil || stored.Error != "" {
		t.Fatalf("historical proposal changed: %+v %v", stored, err)
	}
	if err := json.Unmarshal([]byte(payload), &r); err != nil {
		t.Fatal(err)
	}
	assertRoleWriteRejected(t, f, "POST", "/v1/access/changes", r)
}

func TestRoleWriteHistoricalInvalidPermissions(t *testing.T) {
	for _, permission := range []model.Permission{"future", "ops.*", "", " ops.read", "OPS.READ"} {
		t.Run(string(permission), func(t *testing.T) {
			f := newRoleReviewFixture(t)
			assertHistoricalRoleRejection(t, f, roleRequest("legacy", "Legacy", model.PermissionRead, permission), true)
		})
	}
	for _, permissions := range [][]model.Permission{nil, {}} {
		f := newRoleReviewFixture(t)
		assertHistoricalRoleRejection(t, f, roleRequest("legacy", "Legacy", permissions...), true)
	}
}

func TestRoleWriteLegalMixedChange(t *testing.T) {
	f := newRoleReviewFixture(t)
	roles := []model.Role{
		{ID: "good", DisplayName: "Good", Permissions: []model.Permission{model.PermissionRead}},
		{ID: "other-role", DisplayName: "Other", Permissions: []model.Permission{model.PermissionInspect, model.PermissionRead, model.PermissionInspect}},
	}
	c := submitReviewRequest(t, f, mixedRoleRequest(f, roles))
	f.apply(t, c)
	policy, _, err := f.engine.effectiveAccessPolicy(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, role := range roles {
		if !reflect.DeepEqual(policy.Roles[role.ID].Permissions, role.Permissions) {
			t.Fatal("mixed role missing or changed")
		}
	}
	if policy.Tenants["new-tenant"].ID == "" || len(policy.Bindings) != 1 || policy.Bindings[0].RoleID != "good" {
		t.Fatal("mixed request did not apply together")
	}
}

// 模拟已在旧版本生效的行与快照；只在临时 SQLite 中调用可信 Store 夹具入口。
func seedLegacyRoles(t *testing.T, f *tenantReviewFixture) {
	t.Helper()
	policy, snapshot, err := f.engine.effectiveAccessPolicy(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for id, permission := range map[string]model.Permission{"legacy-unknown": "future", "legacy-wildcard": "*"} {
		role := model.Role{ID: id, DisplayName: "Legacy", Permissions: []model.Permission{permission}, CreatedBy: f.actors["creator"]}
		if err := f.db.UpsertRole(context.Background(), role); err != nil {
			t.Fatal(err)
		}
		policy.Roles[id] = role
	}
	payload, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.db.SaveAccessPolicySnapshot(context.Background(), model.AccessPolicySnapshot{PolicyJSON: string(payload), Digest: digestText(string(payload)), ActorHash: "bootstrap"}, snapshot.Version)
	if err != nil {
		t.Fatal(err)
	}
}

func TestRoleWritePreservesLegacyPolicy(t *testing.T) {
	f := newRoleReviewFixture(t)
	seedLegacyRoles(t, f)
	before, _, err := f.engine.effectiveAccessPolicy(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	rowsBefore, err := f.db.ListRoles(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	f.apply(t, f.proposal(t, "unrelated", "Unrelated"))
	r := model.AccessControlUpdateRequest{Bindings: []model.RoleBinding{{ID: "legacy-binding", Subject: f.actors["viewer"], TenantID: "default", RoleID: "legacy-wildcard"}}}
	f.apply(t, submitReviewRequest(t, f, r))
	after, _, err := f.engine.effectiveAccessPolicy(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	rowsAfter, err := f.db.ListRoles(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before.Roles, after.Roles) || !reflect.DeepEqual(rowsBefore, rowsAfter) {
		t.Fatal("unrelated change rewrote legacy/bootstrap roles")
	}
	if !after.Roles["platform-admin"].Allows(model.PermissionManageConfig) || !after.Roles["legacy-wildcard"].Allows(model.PermissionManageConfig) || !after.Roles["legacy-unknown"].Allows("future") {
		t.Fatal("legacy interpretation changed")
	}
	decision, err := f.db.Authorize(context.Background(), f.actors["viewer"], "default", "object", model.PermissionManageConfig)
	if err != nil || !decision.Allowed {
		t.Fatalf("legacy Store interpretation changed: %+v %v", decision, err)
	}
	assertLegacyRoleEdits(t, f, before.Roles)
}

func assertLegacyRoleEdits(t *testing.T, f *tenantReviewFixture, roles map[string]model.Role) {
	t.Helper()
	for _, id := range []string{"legacy-unknown", "legacy-wildcard"} {
		r := roleRequest(id, "Renamed", roles[id].Permissions...)
		assertRoleWriteRejected(t, f, "POST", "/v1/access/changes", versionedRoleRequest(t, f, r))
		assertHistoricalRoleRejection(t, f, r, true)
	}
	bindingsBefore, err := f.db.ListRoleBindings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	c := submitReviewRequest(t, f, roleRequest("legacy-unknown", "Explicit replacement", model.PermissionRead, model.PermissionRead))
	detail := readRoleDetail(t, f, c)
	if detail.Availability != "unsupported" || detail.Reason != "incompatible_role" {
		t.Fatal("legacy detail support expanded")
	}
	f.apply(t, c)
	after, _, err := f.engine.effectiveAccessPolicy(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after.Roles["legacy-unknown"].Permissions, []model.Permission{model.PermissionRead, model.PermissionRead}) {
		t.Fatal("explicit replacement failed")
	}
	bindingsAfter, err := f.db.ListRoleBindings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(bindingsBefore, bindingsAfter) {
		t.Fatal("replacement changed bindings")
	}
}

func legacyRoleMutation(t *testing.T, f *tenantReviewFixture, r model.AccessControlUpdateRequest) store.AccessPolicyMutation {
	t.Helper()
	policy, snapshot, err := f.engine.effectiveAccessPolicy(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for i := range r.Roles {
		r.Roles[i].CreatedBy = f.actors["creator"]
		policy.Roles[r.Roles[i].ID] = r.Roles[i]
	}
	payload, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	return store.AccessPolicyMutation{Actor: f.actors["creator"], IdempotencyKey: r.IdempotencyKey, RequestDigest: digestText(string(payload)), ExpectedVersion: snapshot.Version, Roles: r.Roles,
		Snapshot: model.AccessPolicySnapshot{PolicyJSON: string(payload), Digest: digestText(string(payload)), ActorHash: f.actors["creator"]}}
}

func TestRoleWriteHistoricalAppliedReplay(t *testing.T) {
	f := newRoleReviewFixture(t)
	c := saveHistoricalRoleRequest(t, f, roleRequest("already-applied", "Legacy", "*"))
	_, payload, err := f.db.GetAccessChangeWithPayload(context.Background(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	var r model.AccessControlUpdateRequest
	if err := json.Unmarshal([]byte(payload), &r); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.ApproveAccessChange(context.Background(), c.ID, f.actors["approver"], c.RequestDigest, c.ConfirmationPhrase); err != nil {
		t.Fatal(err)
	}
	mutation := legacyRoleMutation(t, f, r)
	mutation.AccessChangeDigest = c.RequestDigest
	if _, err := f.db.ApplyAccessChangeMutation(context.Background(), c.ID, f.actors["creator"], mutation); err != nil {
		t.Fatal(err)
	}
	before := f.durableState(t)
	f.call(t, "creator", "POST", "/v1/access/changes/"+c.ID+"/apply", nil, 200)
	f.call(t, "approver", "POST", "/v1/access/changes/"+c.ID+"/apply", nil, 409)
	if f.durableState(t) != before {
		t.Fatal("historical applied replay changed state")
	}
	assertRoleWriteRejected(t, f, "POST", "/v1/access/changes", r)
}

func TestRoleWriteHistoricalDirectReplay(t *testing.T) {
	f := newRoleReviewFixture(t)
	f.engine.catalog.SchemaVersion = 3
	r := versionedRoleRequest(t, f, roleRequest("already-direct", "Legacy", "*"))
	r.RequiresDualApproval = false
	if _, _, err := f.db.ApplyAccessPolicyMutation(context.Background(), legacyRoleMutation(t, f, r)); err != nil {
		t.Fatal(err)
	}
	assertRoleWriteRejected(t, f, "PUT", "/v1/access", r)
}

func TestRoleWriteHistoricalProtectedDetail(t *testing.T) {
	f := newRoleReviewFixture(t)
	c := saveHistoricalRoleRequest(t, f, roleRequest("viewer", "Protected", "*"))
	detail := readRoleDetail(t, f, c)
	if detail.Reason != "protected_role" {
		t.Fatalf("detail priority changed: %+v", detail)
	}
}
