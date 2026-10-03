package runner

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	"github.com/AreaSong/ops/services/areasong-ops/internal/store"
)

func TestBindingRemovalLowercaseAndReplay(t *testing.T) {
	for _, direct := range []bool{false, true} {
		t.Run(map[bool]string{false: "approved", true: "schema3"}[direct], func(t *testing.T) {
			f := newRoleReviewFixture(t)
			seedRemovalBindings(t, f, "Mixed", "mixed", "alpha", "zulu", "中文")
			before, err := f.db.ListRoleBindings(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			ids := []string{" zulu ", "", "mixed", " alpha", " mixed ", "\t", "missing", "中文"}
			r := versionedRoleRequest(t, f, model.AccessControlUpdateRequest{RemoveBindingIDs: ids})
			if !reflect.DeepEqual(normalizeIDs(ids), []string{"alpha", "missing", "mixed", "zulu", "中文"}) {
				t.Fatal("小写撤销的去空、去重、排序改变")
			}
			method, path := "POST", ""
			var request any
			if direct {
				f.engine.catalog.SchemaVersion = 3
				r.RequiresDualApproval = false
				method, path, request = "PUT", "/v1/access", r
			} else {
				var c model.AccessChange
				json.Unmarshal(f.call(t, "creator", "POST", "/v1/access/changes", r, 202), &c)
				deletionApprove(t, f, c)
				path = "/v1/access/changes/" + c.ID + "/apply"
			}
			first := f.call(t, "creator", method, path, request, 200)
			after, err := f.db.ListRoleBindings(context.Background())
			if err != nil || len(after) != 1 || !reflect.DeepEqual(after[0], before[0]) || after[0].ID != "Mixed" {
				t.Fatal("规范小写撤销误伤大写项", after, err)
			}
			state := f.durableState(t)
			replay := f.call(t, "creator", method, path, request, 200)
			if !direct && string(first) != string(replay) {
				t.Fatal("applied 重放改变原结果")
			}
			if direct {
				// AccessControl 含 map 派生的无序主体列表；直接重放比较策略标识和全表内容。
				var initialView, replayView model.AccessControlView
				if err := json.Unmarshal(first, &initialView); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(replay, &replayView); err != nil {
					t.Fatal(err)
				}
				if initialView.Version != replayView.Version || initialView.Digest != replayView.Digest {
					t.Fatal("直接重放改变策略标识")
				}
			}
			if state != f.durableState(t) {
				t.Fatal("成功重放改变结果或产生写入")
			}
		})
	}
}

// 仅合成旧代码已成功执行的结果：原批准目标仍是 Mixed，旧规范化删除的是 mixed。
func historicalRemovalMutation(t *testing.T, f *tenantReviewFixture, r model.AccessControlUpdateRequest) store.AccessPolicyMutation {
	t.Helper()
	policy, snapshot, err := f.engine.effectiveAccessPolicy(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	policy.Bindings = removeBindings(policy.Bindings, normalizeIDs(r.RemoveBindingIDs))
	raw, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	digest := digestText(string(raw))
	return store.AccessPolicyMutation{Actor: f.actors["creator"], IdempotencyKey: r.IdempotencyKey,
		RequestDigest: digest, ExpectedVersion: snapshot.Version, RemoveBindingIDs: normalizeIDs(r.RemoveBindingIDs),
		Snapshot: model.AccessPolicySnapshot{PolicyJSON: string(raw), Digest: digest, ActorHash: f.actors["creator"]}}
}

func TestBindingRemovalHistoricalAppliedAndDirectReplay(t *testing.T) {
	for _, direct := range []bool{false, true} {
		t.Run(map[bool]string{false: "applied", true: "direct receipt"}[direct], func(t *testing.T) {
			f := newRoleReviewFixture(t)
			seedRemovalBindings(t, f, "Mixed", "mixed")
			r := versionedRoleRequest(t, f, model.AccessControlUpdateRequest{RemoveBindingIDs: []string{"Mixed"}})
			if direct {
				f.engine.catalog.SchemaVersion = 3
				r.RequiresDualApproval = false
				if _, _, err := f.db.ApplyAccessPolicyMutation(context.Background(), historicalRemovalMutation(t, f, r)); err != nil {
					t.Fatal(err)
				}
				assertRemovalRejected(t, f, "PUT", "/v1/access", r)
				return
			}
			c := saveHistoricalRoleRequest(t, f, r)
			_, payload, err := f.db.GetAccessChangeWithPayload(context.Background(), c.ID)
			if err != nil || digestText(payload) != c.RequestDigest {
				t.Fatal("历史载荷摘要无效", err)
			}
			if err := json.Unmarshal([]byte(payload), &r); err != nil {
				t.Fatal(err)
			}
			deletionApprove(t, f, c)
			mutation := historicalRemovalMutation(t, f, r)
			mutation.AccessChangeDigest = c.RequestDigest
			applied, err := f.db.ApplyAccessChangeMutation(context.Background(), c.ID, f.actors["creator"], mutation)
			if err != nil || applied.State != model.AccessChangeApplied {
				t.Fatal("构造历史 applied", err)
			}
			assertHistoricalRemovalReplay(t, f, applied)
			assertRemovalRejected(t, f, "POST", "/v1/access/changes", r)
		})
	}
}

func assertHistoricalRemovalReplay(t *testing.T, f *tenantReviewFixture, applied model.AccessChange) {
	t.Helper()
	// 后续版本变化不应让原 applied 提案重新解释目标。
	f.apply(t, submitReviewRequest(t, f, model.AccessControlUpdateRequest{Bindings: []model.RoleBinding{removalBinding(f, "unrelated")}}))
	before := f.durableState(t)
	path := "/v1/access/changes/" + applied.ID + "/apply"
	expected, err := json.Marshal(applied)
	if err != nil {
		t.Fatal(err)
	}
	// 按公共 JSON 合同比较，内部 IdempotencyKey 不对 HTTP 暴露。
	replay := f.call(t, "creator", "POST", path, nil, 200)
	if strings.TrimSpace(string(replay)) != string(expected) {
		t.Fatal("历史 applied 结果被改变")
	}
	f.call(t, "approver", "POST", path, nil, 409)
	if before != f.durableState(t) {
		t.Fatal("历史重放或错误身份产生持久化增量")
	}
}

func TestBindingRemovalVersionAndProtection(t *testing.T) {
	for _, scenario := range []string{"version", "bootstrap", "last-admin"} {
		t.Run(scenario, func(t *testing.T) {
			f := newRoleReviewFixture(t)
			seedRemovalBindings(t, f, "Mixed", "mixed")
			r := model.AccessControlUpdateRequest{RemoveBindingIDs: []string{"mixed"}}
			want := "版本"
			if scenario == "bootstrap" {
				b := removalBinding(f, "protected")
				b.CreatedBy = "bootstrap"
				if err := f.db.UpsertRoleBinding(context.Background(), b); err != nil {
					t.Fatal(err)
				}
				r.RemoveBindingIDs, want = []string{"protected"}, "bootstrap 绑定不可撤销"
			}
			if scenario == "last-admin" {
				for _, actor := range []string{"creator", "approver", "other"} {
					r.RemovePrincipalSubjects = append(r.RemovePrincipalSubjects, f.actors[actor])
				}
				want = "不能撤销最后一个平台管理员"
			}
			c := submitReviewRequest(t, f, r)
			deletionApprove(t, f, c)
			if scenario == "version" {
				seedRemovalBindings(t, f, "newer")
			}
			before := f.durableState(t)
			raw := f.call(t, "creator", "POST", "/v1/access/changes/"+c.ID+"/apply", nil, 409)
			if !strings.Contains(string(raw), want) || before != f.durableState(t) {
				t.Fatalf("既有保护或原子性改变：%s", raw)
			}
		})
	}
}

func TestBindingRemovalRoleTenantNormalization(t *testing.T) {
	f := newRoleReviewFixture(t)
	f.apply(t, submitReviewRequest(t, f, model.AccessControlUpdateRequest{
		Roles:   []model.Role{{ID: " disposable-role ", DisplayName: "Role", Permissions: []model.Permission{model.PermissionRead}}},
		Tenants: []model.Tenant{{ID: " disposable-tenant ", DisplayName: "Tenant"}},
	}))
	seedRemovalBindings(t, f, "Mixed", "mixed")
	f.apply(t, submitReviewRequest(t, f, model.AccessControlUpdateRequest{
		RemoveRoleIDs:    []string{" DISPOSABLE-ROLE ", "disposable-role", ""},
		RemoveTenantIDs:  []string{" DISPOSABLE-TENANT ", "disposable-tenant", " "},
		RemoveBindingIDs: []string{" mixed ", "mixed"},
	}))
	policy, _, err := f.engine.effectiveAccessPolicy(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_, role := policy.Roles["disposable-role"]
	_, tenant := policy.Tenants["disposable-tenant"]
	if role || tenant || !reflect.DeepEqual(removalBindingIDs(t, f), []string{"Mixed"}) {
		t.Fatal("角色、租户或合法绑定撤销规范化改变")
	}
}

func TestBindingRemovalPreservesCaseSensitiveReplacement(t *testing.T) {
	f := newRoleReviewFixture(t)
	stamp := time.Date(2030, 1, 2, 3, 4, 5, 0, time.FixedZone("offset", 8*3600))
	b := removalBinding(f, " Mixed ")
	b.ObjectIDs, b.ExpiresAt, b.JIT = []string{"unknown", "unknown", "*"}, &stamp, true
	b.RequiresDualApproval, b.ApprovalState = true, "pending"
	b.ApprovedByHash, b.SecondApprovedByHash = "client-first", "client-second"
	b.ApprovedAt, b.SecondApprovedAt, b.CreatedAt, b.UpdatedAt = &stamp, &stamp, stamp, stamp
	f.apply(t, submitReviewRequest(t, f, model.AccessControlUpdateRequest{Bindings: []model.RoleBinding{b, removalBinding(f, "mixed")}}))
	rows, err := f.db.ListRoleBindings(context.Background())
	if err != nil || len(rows) != 2 || rows[0].ID != "Mixed" || rows[0].SecondApprovedAt == nil || !rows[0].JIT || !reflect.DeepEqual(rows[0].ObjectIDs, b.ObjectIDs) {
		t.Fatal("大小写并存或完整绑定字段改变", err)
	}
	replacement := removalBinding(f, "Mixed")
	f.apply(t, submitReviewRequest(t, f, model.AccessControlUpdateRequest{Bindings: []model.RoleBinding{replacement}}))
	policy, _, err := f.engine.effectiveAccessPolicy(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	replacement.CreatedBy = f.actors["creator"]
	if len(policy.Bindings) != 2 || !reflect.DeepEqual(policy.Bindings[0], replacement) {
		t.Fatal("完整替换未清除省略的对象、期限、JIT、审批及元数据字段")
	}
	updated, err := f.db.ListRoleBindings(context.Background())
	if err != nil || !updated[0].CreatedAt.Equal(stamp) || !updated[0].UpdatedAt.IsZero() || !reflect.DeepEqual(updated[1], rows[1]) {
		t.Fatal("表行元数据或小写绑定被改变", err)
	}
}
