package runner

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

// 记录现有写入合同；这些请求均不属于本阶段只读详情的支持范围。
func TestRoleDeletionExistingNormalization(t *testing.T) {
	for _, ids := range [][]string{{" CUSTOM ", "custom", "", " "}, {"missing"}, {"", " "}} {
		t.Run("IDs", func(t *testing.T) {
			f := deletionFixture(t)
			before, snap, _ := f.engine.effectiveAccessPolicy(context.Background())
			c := submitReviewRequest(t, f, deletionRequest(ids...))
			f.apply(t, c)
			after, next, _ := f.engine.effectiveAccessPolicy(context.Background())
			if len(ids) == 4 {
				delete(before.Roles, "custom")
			}
			if !reflect.DeepEqual(before, after) || next.Version != snap.Version+1 {
				t.Fatal("normalization/no-op contract changed")
			}
		})
	}
}

func TestRoleDeletionExistingMixedRemoval(t *testing.T) {
	f := deletionFixture(t)
	f.apply(t, submitReviewRequest(t, f, model.AccessControlUpdateRequest{Bindings: []model.RoleBinding{{ID: "held", RoleID: "custom", TenantID: "default", Subject: f.actors["viewer"]}}}))
	c := submitReviewRequest(t, f, model.AccessControlUpdateRequest{RemoveRoleIDs: []string{"custom"}, RemoveBindingIDs: []string{"held"}})
	assertDeletionReadOnly(t, f, c, "unsupported", "unsupported_binding_request")
	f.apply(t, c)
	p, _, _ := f.engine.effectiveAccessPolicy(context.Background())
	if _, found := p.Roles["custom"]; found {
		t.Fatal("mixed removal kept role")
	}
	rows, err := f.db.ListRoleBindings(context.Background())
	if err != nil || len(rows) != 0 {
		t.Fatal("mixed removal kept binding", err)
	}
}

func TestRoleDeletionExistingUpsertThenDelete(t *testing.T) {
	f := deletionFixture(t)
	r := roleRequest("custom", "Replacement", model.PermissionRead)
	r.RemoveRoleIDs = []string{"custom"}
	c := submitReviewRequest(t, f, r)
	assertDeletionReadOnly(t, f, c, "unsupported", "")
	f.apply(t, c)
	p, _, _ := f.engine.effectiveAccessPolicy(context.Background())
	if _, found := p.Roles["custom"]; found {
		t.Fatal("upsert+delete did not end deleted")
	}
}

// 批准后改变原始载荷必须拒绝，且保留故障注入后的全部持久化状态。
// 仅通过临时 SQLite 故障注入改变已批准载荷；正常 HTTP 无此写入入口。
func TestRoleDeletionExistingPayloadDigestGap(t *testing.T) {
	f := deletionFixture(t)
	f.apply(t, submitReviewRequest(t, f, roleRequest("another", "另一合成角色", model.PermissionRead)))
	c := submitReviewRequest(t, f, deletionRequest("custom"))
	deletionApprove(t, f, c)
	_, payload, err := f.db.GetAccessChangeWithPayload(context.Background(), c.ID)
	if err != nil {
		t.Fatal(err)
	}
	var request model.AccessControlUpdateRequest
	if err := json.Unmarshal([]byte(payload), &request); err != nil {
		t.Fatal(err)
	}
	request.RemoveRoleIDs = []string{"another"}
	changed, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	execReviewSQL(t, f, `UPDATE access_changes SET payload_json=? WHERE id=?`, string(changed), c.ID)
	before := f.durableState(t)
	f.call(t, "creator", "POST", "/v1/access/changes/"+c.ID+"/apply", nil, 409)
	assertDeletionReadOnly(t, f, c, "unsupported", "")
	if !reflect.DeepEqual(before, f.durableState(t)) {
		t.Fatal("digest rejection changed persisted state")
	}
	policy, _, err := f.engine.effectiveAccessPolicy(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_, originalRemains := policy.Roles["custom"]
	_, changedRemains := policy.Roles["another"]
	if !originalRemains || !changedRemains {
		t.Fatal("digest rejection removed a role")
	}
}
