package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

const bindingRemovalError = "绑定撤销 ID 不支持大小写规范化后改变目标"

func removalBinding(f *tenantReviewFixture, id string) model.RoleBinding {
	return model.RoleBinding{ID: id, Subject: f.actors["viewer"], TenantID: "default", RoleID: "viewer"}
}

func seedRemovalBindings(t *testing.T, f *tenantReviewFixture, ids ...string) {
	t.Helper()
	if len(ids) == 0 {
		return
	}
	r := model.AccessControlUpdateRequest{}
	for _, id := range ids {
		r.Bindings = append(r.Bindings, removalBinding(f, id))
	}
	f.apply(t, submitReviewRequest(t, f, r))
}

func removalBindingIDs(t *testing.T, f *tenantReviewFixture) []string {
	t.Helper()
	rows, err := f.db.ListRoleBindings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	return ids
}

func assertRemovalRejected(t *testing.T, f *tenantReviewFixture, method, path string, request any) {
	t.Helper()
	before := f.durableState(t)
	raw := f.call(t, "creator", method, path, request, 409)
	var body map[string]string
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(body, map[string]string{"error": bindingRemovalError}) {
		t.Fatalf("错误原因不符：%s", raw)
	}
	if f.durableState(t) != before {
		t.Fatal("拒绝改变了绑定、策略版本、快照、receipt、提案、审批或审计")
	}
}

func TestBindingRemovalRejectsHistoricalTarget(t *testing.T) {
	f := newRoleReviewFixture(t)
	seedRemovalBindings(t, f, "Mixed", "mixed")
	c := saveHistoricalRoleRequest(t, f, model.AccessControlUpdateRequest{RemoveBindingIDs: []string{"Mixed"}})
	deletionApprove(t, f, c)
	before := f.durableState(t)
	_, err := f.engine.ApplyAccessChange(context.Background(), f.actors["creator"], c.ID)
	if err == nil || err.Error() != bindingRemovalError {
		t.Errorf("应拒绝原始目标 Mixed，实际 err=%v，剩余绑定=%v", err, removalBindingIDs(t, f))
	}
	if !reflect.DeepEqual(removalBindingIDs(t, f), []string{"Mixed", "mixed"}) || f.durableState(t) != before {
		t.Fatal("异常撤销造成目标误删或持久化变化")
	}
}

func TestBindingRemovalRejectsNewRequests(t *testing.T) {
	for _, entry := range []string{"POST", "PUT-schema4", "PUT-schema3", "Engine.UpdateAccess"} {
		for _, ids := range [][]string{{"Mixed", "mixed"}, {"Mixed"}, {"mixed"}, {}} {
			for i, removals := range [][]string{{"Mixed"}, {" Mixed "}, {"mixed", "Mixed", "mixed"}, {"Mixed", "mixed"}, {"\t", "safe", "\u00a0MİXED\u00a0"}} {
				t.Run(fmt.Sprintf("%s/%v/%d", entry, ids, i), func(t *testing.T) {
					f := newRoleReviewFixture(t)
					seedRemovalBindings(t, f, ids...)
					r := versionedRoleRequest(t, f, model.AccessControlUpdateRequest{RemoveBindingIDs: removals})
					r.RequiresDualApproval = false
					method, path := "PUT", "/v1/access"
					if entry == "POST" {
						method, path = "POST", "/v1/access/changes"
					}
					if entry == "PUT-schema3" {
						f.engine.catalog.SchemaVersion = 3
					}
					if entry == "Engine.UpdateAccess" {
						before := f.durableState(t)
						_, err := f.engine.UpdateAccess(context.Background(), f.actors["creator"], r)
						if err == nil || err.Error() != bindingRemovalError || f.durableState(t) != before {
							t.Fatalf("直接入口未整体拒绝：%v", err)
						}
						return
					}
					assertRemovalRejected(t, f, method, path, r)
				})
			}
		}
	}
}

func TestBindingRemovalMixedRequestAtomic(t *testing.T) {
	for _, stage := range []string{"create", "direct", "pending", "approved"} {
		for _, removals := range [][]string{{"Mixed", "remove", "mixed"}, {"mixed", "remove", " Mixed ", "mixed"}} {
			t.Run(fmt.Sprintf("%s/%v", stage, removals), func(t *testing.T) {
				f := newRoleReviewFixture(t)
				seedRemovalBindings(t, f, "Mixed", "mixed", "edit", "remove")
				edit := removalBinding(f, "edit")
				edit.ObjectIDs = []string{"changed"}
				r := model.AccessControlUpdateRequest{Bindings: []model.RoleBinding{removalBinding(f, "added"), edit}, RemoveBindingIDs: removals}
				if stage == "pending" || stage == "approved" {
					assertHistoricalRemovalRejected(t, f, r, stage == "approved")
					return
				}
				r = versionedRoleRequest(t, f, r)
				method, path := "POST", "/v1/access/changes"
				if stage == "direct" {
					f.engine.catalog.SchemaVersion = 3
					r.RequiresDualApproval = false
					method, path = "PUT", "/v1/access"
				}
				assertRemovalRejected(t, f, method, path, r)
			})
		}
	}
}

func assertHistoricalRemovalRejected(t *testing.T, f *tenantReviewFixture, request model.AccessControlUpdateRequest, approved bool) {
	t.Helper()
	c := saveHistoricalRoleRequest(t, f, request)
	path := "/v1/access/changes/" + c.ID
	if !approved {
		before := f.durableState(t)
		raw := f.call(t, "creator", "POST", path+"/apply", nil, 409)
		if !strings.Contains(string(raw), "尚未完成双人批准") || before != f.durableState(t) {
			t.Fatal("pending 提案未保留原状态合同")
		}
	}
	deletionApprove(t, f, c)
	stored, payload, err := f.db.GetAccessChangeWithPayload(context.Background(), c.ID)
	if err != nil || digestText(payload) != stored.RequestDigest || stored.ApprovedByHash != f.actors["approver"] {
		t.Fatal("历史夹具必须有有效原始摘要及独立审批", err)
	}
	// 比较基线位于夹具构造、审批完成之后，不能将它们计为 apply 副作用。
	assertRemovalRejected(t, f, "POST", path+"/apply", nil)
	after, raw, err := f.db.GetAccessChangeWithPayload(context.Background(), c.ID)
	if err != nil || raw != payload || !reflect.DeepEqual(after, stored) || after.State != model.AccessChangeApproved {
		t.Fatal("拒绝改写了历史载荷、摘要或批准记录", err)
	}
	if err := json.Unmarshal([]byte(payload), &request); err != nil {
		t.Fatal(err)
	}
	assertRemovalRejected(t, f, "POST", "/v1/access/changes", request)
}

func TestBindingRemovalHistoricalInventoryIndependent(t *testing.T) {
	for _, ids := range [][]string{{"Mixed"}, {"mixed"}, {}} {
		t.Run(fmt.Sprint(ids), func(t *testing.T) {
			f := newRoleReviewFixture(t)
			seedRemovalBindings(t, f, ids...)
			assertHistoricalRemovalRejected(t, f, model.AccessControlUpdateRequest{RemoveBindingIDs: []string{" Mixed "}}, true)
		})
	}
}

func TestBindingRemovalDecodedInputs(t *testing.T) {
	for _, tc := range []struct {
		fragment string
		status   int
	}{
		{`"removeBindingIds":["\u004dixed"]`, 409},
		{`"RemoveBindingIDs":["Mixed"]`, 409},
		{`"removeBindingIds":["mixed"],"removeBindingIds":["Mixed"]`, 409},
		{`"removeBindingIds":[null," "," mixed ","mixed"]`, 202},
		{`"removeBindingIds":null`, 202},
		{`"removeBindingIds":[7]`, 400},
	} {
		t.Run(tc.fragment, func(t *testing.T) {
			f := newRoleReviewFixture(t)
			seedRemovalBindings(t, f, "Mixed", "mixed")
			body := `{` + tc.fragment + `,"idempotencyKey":"dddddddd-dddd-4ddd-8ddd-dddddddddddd"}`
			before := f.durableState(t)
			req := httptest.NewRequest("POST", "/v1/access/changes", strings.NewReader(body))
			req.Header.Set(actorHeader, f.actors["creator"])
			req.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			f.handler.ServeHTTP(response, req)
			if response.Code != tc.status {
				t.Fatalf("status=%d body=%s", response.Code, response.Body)
			}
			if tc.status != 202 {
				if before != f.durableState(t) || (tc.status == 409 && !strings.Contains(response.Body.String(), bindingRemovalError)) {
					t.Fatal("解码后未按目标保护整体拒绝")
				}
				return
			}
			var c model.AccessChange
			if err := json.Unmarshal(response.Body.Bytes(), &c); err != nil {
				t.Fatal(err)
			}
			f.apply(t, c)
		})
	}
}
