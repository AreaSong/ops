package runner

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/config"
	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

func deletionFixture(t *testing.T) *tenantReviewFixture {
	t.Helper()
	f := newRoleReviewFixture(t)
	f.apply(t, submitReviewRequest(t, f, roleRequest("custom", "待删角色", model.PermissionInspect, model.PermissionRead, model.PermissionRead)))
	return f
}

func deletionRequest(ids ...string) model.AccessControlUpdateRequest {
	return model.AccessControlUpdateRequest{RemoveRoleIDs: ids}
}

func readDeletionDetail(t *testing.T, f *tenantReviewFixture, c model.AccessChange) accessChangeDetail {
	t.Helper()
	raw := f.call(t, "approver", "GET", "/v1/access/changes/"+c.ID+"/detail", nil, 200)
	var d accessChangeDetail
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	if d.ID != c.ID || d.RequestDigest != c.RequestDigest || d.ReviewerHash != f.actors["approver"] {
		t.Fatal("unbound detail")
	}
	assertReviewFields(t, raw, "reviewerHash id requestDigest state expectedVersion currentVersion kind availability operation reason roleDeletion")
	var fields map[string]json.RawMessage
	json.Unmarshal(raw, &fields)
	if value, ok := fields["roleDeletion"]; ok {
		assertReviewFields(t, value, "before references")
		var projection map[string]json.RawMessage
		json.Unmarshal(value, &projection)
		assertReviewFields(t, projection["before"], "id displayName permissions")
		assertReviewFields(t, projection["references"], "bindings directPrincipals")
	}
	for _, forbidden := range []string{"createdBy", "createdAt", "updatedAt", "objectIds", "payload", "@example.test", f.actors["creator"], f.actors["viewer"]} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("leaked %s", forbidden)
		}
	}
	if d.Availability != "ready" && (d.RoleDeletion != nil || d.Operation != "") {
		t.Fatal("non-ready values leaked")
	}
	return d
}

func assertDeletionReadOnly(t *testing.T, f *tenantReviewFixture, c model.AccessChange, availability, reason string) accessChangeDetail {
	t.Helper()
	before := f.durableState(t)
	d := readDeletionDetail(t, f, c)
	if d.Availability != availability || d.Reason != reason {
		t.Fatalf("detail: %+v", d)
	}
	if next := readDeletionDetail(t, f, c); !reflect.DeepEqual(d, next) {
		t.Fatal("unstable detail")
	}
	if f.durableState(t) != before {
		t.Fatal("GET mutated durable tables")
	}
	return d
}

func deletionApprove(t *testing.T, f *tenantReviewFixture, c model.AccessChange) {
	t.Helper()
	f.call(t, "approver", "POST", "/v1/access/changes/"+c.ID+"/approve", model.AccessChangeApprovalRequest{Digest: c.RequestDigest, Confirmation: c.ConfirmationPhrase}, 200)
}

func deletionDeniedApply(t *testing.T, f *tenantReviewFixture, c model.AccessChange) {
	t.Helper()
	before := f.durableState(t)
	f.call(t, "creator", "POST", "/v1/access/changes/"+c.ID+"/apply", nil, 409)
	if before != f.durableState(t) {
		t.Fatal("failed apply left partial rows, version, receipt or audit")
	}
	got, err := f.db.GetAccessChange(context.Background(), c.ID)
	if err != nil || got.State != model.AccessChangeApproved {
		t.Fatalf("failed state: %+v %v", got, err)
	}
}

func TestRoleDeletionHTTPAndReplay(t *testing.T) {
	f := deletionFixture(t)
	before, snapshot, _ := f.engine.effectiveAccessPolicy(context.Background())
	rolesBefore, _ := f.db.ListRoles(context.Background())
	bindingsBefore, _ := f.db.ListRoleBindings(context.Background())
	c := submitReviewRequest(t, f, deletionRequest("custom"))
	d := assertDeletionReadOnly(t, f, c, "ready", "")
	want := roleReviewValue{"custom", "待删角色", []model.Permission{model.PermissionInspect, model.PermissionRead, model.PermissionRead}}
	if d.Kind != "role_deletion" || d.Operation != "delete" || d.RoleDeletion == nil || !reflect.DeepEqual(d.RoleDeletion.Before, want) || d.RoleDeletion.References != (roleDeletionReferences{"none", "none"}) {
		t.Fatalf("%+v", d)
	}
	rows, _ := f.db.ListRoles(context.Background())
	if !reflect.DeepEqual(rows, rolesBefore) {
		t.Fatal("proposal/GET changed roles")
	}
	// 当前行值变化不拼入历史 before；GET 也不能修补行或历史快照。
	execReviewSQL(t, f, `UPDATE roles SET display_name='current row' WHERE id='custom'`)
	if got := assertDeletionReadOnly(t, f, c, "ready", ""); !reflect.DeepEqual(got, d) {
		t.Fatal("used live role")
	}
	execReviewSQL(t, f, `UPDATE roles SET display_name=? WHERE id='custom'`, want.DisplayName)
	path := "/v1/access/changes/" + c.ID
	for _, actor := range []string{"creator", "approver"} {
		body := model.AccessChangeApprovalRequest{Digest: c.RequestDigest, Confirmation: c.ConfirmationPhrase}
		if actor == "approver" {
			body.Digest = "wrong"
		}
		state := f.durableState(t)
		f.call(t, actor, "POST", path+"/approve", body, 409)
		if state != f.durableState(t) {
			t.Fatal("invalid approval wrote state")
		}
	}
	deletionApprove(t, f, c)
	approvedState := f.durableState(t)
	deletionApprove(t, f, c)
	if approvedState != f.durableState(t) {
		t.Fatal("approval replay wrote state")
	}
	if got := assertDeletionReadOnly(t, f, c, "ready", ""); got.State != model.AccessChangeApproved {
		t.Fatal(got)
	}
	for _, actor := range []string{"approver", "other"} {
		f.call(t, actor, "POST", path+"/apply", nil, 409)
		if approvedState != f.durableState(t) {
			t.Fatal("wrong executor wrote state")
		}
	}
	f.call(t, "creator", "POST", path+"/apply", nil, 200)
	after, next, _ := f.engine.effectiveAccessPolicy(context.Background())
	delete(before.Roles, "custom")
	if !reflect.DeepEqual(before, after) || next.Version != snapshot.Version+1 {
		t.Fatal("unexpected policy change")
	}
	rolesAfter, _ := f.db.ListRoles(context.Background())
	wantRows := rolesBefore[:0]
	for _, r := range rolesBefore {
		if r.ID != "custom" {
			wantRows = append(wantRows, r)
		}
	}
	bindingsAfter, _ := f.db.ListRoleBindings(context.Background())
	if !reflect.DeepEqual(wantRows, rolesAfter) || !reflect.DeepEqual(bindingsBefore, bindingsAfter) {
		t.Fatal("unexpected row change")
	}
	state := f.durableState(t)
	f.call(t, "creator", "POST", path+"/apply", nil, 200)
	f.call(t, "approver", "POST", path+"/apply", nil, 409)
	if state != f.durableState(t) {
		t.Fatal("replay wrote state")
	}
	assertDeletionReadOnly(t, f, c, "stale", "")
	// 后续无关策略变化不能把成功重放变成冲突。
	f.apply(t, f.proposal(t, "later", "Later"))
	state = f.durableState(t)
	f.call(t, "creator", "POST", path+"/apply", nil, 200)
	if state != f.durableState(t) {
		t.Fatal("late replay wrote state")
	}
}

func TestRoleDeletionApplyProtections(t *testing.T) {
	for _, id := range []string{"viewer", "operator", "release-manager", "platform-admin", "manager"} {
		t.Run(id, func(t *testing.T) {
			f := deletionFixture(t)
			c := submitReviewRequest(t, f, deletionRequest(id))
			assertDeletionReadOnly(t, f, c, "unsupported", "protected_role")
			deletionApprove(t, f, c)
			deletionDeniedApply(t, f, c)
		})
	}
	for _, mode := range []string{"version", "binding", "expired binding", "principal", "row binding", "audit failure"} {
		t.Run(mode, func(t *testing.T) {
			f := deletionFixture(t)
			c := submitReviewRequest(t, f, deletionRequest("custom"))
			deletionApprove(t, f, c)
			switch mode {
			case "version":
				f.apply(t, f.proposal(t, "later", "Later"))
			case "binding", "expired binding":
				b := model.RoleBinding{ID: "late", Subject: f.actors["viewer"], TenantID: "default", RoleID: "custom"}
				if mode == "expired binding" {
					v := time.Now().Add(-time.Hour).UTC()
					b.ExpiresAt = &v
				}
				f.apply(t, submitReviewRequest(t, f, model.AccessControlUpdateRequest{Bindings: []model.RoleBinding{b}}))
			case "principal":
				f.apply(t, submitReviewRequest(t, f, model.AccessControlUpdateRequest{Principals: []model.AccessPrincipal{{Subject: f.actors["viewer"], TenantID: "default", Roles: []string{"custom"}}}}))
			case "row binding":
				// 故障注入：行表出现历史快照之外的引用，事务必须再次阻止删除。
				if err := f.db.UpsertRoleBinding(context.Background(), model.RoleBinding{ID: "row-only", Subject: f.actors["viewer"], TenantID: "default", RoleID: "custom", CreatedBy: f.actors["creator"]}); err != nil {
					t.Fatal(err)
				}
				assertDeletionReadOnly(t, f, c, "ready", "")
			case "audit failure":
				execReviewSQL(t, f, `CREATE TRIGGER fail_delete_audit BEFORE INSERT ON audit_entries WHEN NEW.event='access.change.applied' BEGIN SELECT RAISE(ABORT, 'synthetic closure failure'); END`)
			}
			deletionDeniedApply(t, f, c)
		})
	}
}

func TestRoleDeletionAuthorization(t *testing.T) {
	f := deletionFixture(t)
	c := submitReviewRequest(t, f, deletionRequest("custom"))
	f.actors["malformed"] = "invalid"
	f.actors["unregistered"] = config.AccessHashForEmail("absent@example.test")
	before := f.durableState(t)
	for _, actor := range []string{"viewer", "tenant-manager", "unknown", "malformed", "unregistered"} {
		status := 403
		if actor == "unknown" || actor == "malformed" {
			status = 401
		}
		for _, id := range []string{c.ID, "missing"} {
			f.call(t, actor, "GET", "/v1/access/changes/"+id+"/detail", nil, status)
		}
	}
	if f.durableState(t) != before {
		t.Fatal("unauthorized GET wrote state")
	}
	for _, mode := range []string{"revoked", "disabled", "expired"} {
		t.Run(mode, func(t *testing.T) {
			f := deletionFixture(t)
			c := submitReviewRequest(t, f, deletionRequest("custom"))
			mutateReviewBaseline(t, f, func(p *config.AccessPolicy) {
				v := p.Principals[f.actors["approver"]]
				switch mode {
				case "revoked":
					v.Roles = []string{"viewer"}
				case "disabled":
					v.Status = "disabled"
				case "expired":
					expiry := time.Now().Add(-time.Hour).UTC()
					v.ExpiresAt = &expiry
				}
				p.Principals[f.actors["approver"]] = v
			})
			state := f.durableState(t)
			f.call(t, "approver", "GET", "/v1/access/changes/"+c.ID+"/detail", nil, 403)
			if state != f.durableState(t) {
				t.Fatal("revoked GET wrote state")
			}
		})
	}
}
