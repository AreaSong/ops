package runner

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	"github.com/AreaSong/ops/services/areasong-ops/internal/store"
)

func TestAccessChangePayloadRejectsCorruption(t *testing.T) {
	for _, tc := range []struct {
		name, payload string
		matchDigest   bool
	}{
		{"invalid JSON stale digest", "{", false},
		{"empty stale digest", "", false},
		{"invalid JSON matching digest", "{", true},
		{"empty matching digest", "", true},
		{"whitespace changes raw bytes", " ", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRoleReviewFixture(t)
			c := submitReviewRequest(t, f, roleRequest("payload-role", "Payload", model.PermissionRead))
			deletionApprove(t, f, c)
			payload := tc.payload
			if tc.name == "whitespace changes raw bytes" {
				_, original, err := f.db.GetAccessChangeWithPayload(context.Background(), c.ID)
				if err != nil {
					t.Fatal(err)
				}
				payload += original
			}
			digest := c.RequestDigest
			if tc.matchDigest {
				digest = digestText(payload)
			}
			execReviewSQL(t, f, `UPDATE access_changes SET payload_json=?,request_digest=? WHERE id=?`, payload, digest, c.ID)
			before := f.durableState(t)
			raw := f.call(t, "creator", "POST", "/v1/access/changes/"+c.ID+"/apply", nil, 409)
			var body map[string]string
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(body, map[string]string{"error": "访问策略审批载荷损坏"}) {
				t.Fatalf("unexpected error: %s", raw)
			}
			if f.durableState(t) != before {
				t.Fatal("corruption rejection changed durable state")
			}
		})
	}
}

// 隔离夹具固定“Runner 已读并验摘要，最终事务尚未开始”的边界；不声称注入了运行中 goroutine 的并发窗口。
func TestAccessChangePayloadChangesBeforeFinalTransaction(t *testing.T) {
	for _, entry := range []string{"runner continuation", "direct store"} {
		for _, changeDigest := range []bool{false, true} {
			t.Run(entry+map[bool]string{false: " payload only", true: " payload and digest"}[changeDigest], func(t *testing.T) {
				f := newRoleReviewFixture(t)
				c := submitReviewRequest(t, f, roleRequest("payload-role", "Approved name", model.PermissionRead))
				deletionApprove(t, f, c)
				read, payload, err := f.db.GetAccessChangeWithPayload(context.Background(), c.ID)
				if err != nil || digestText(payload) != read.RequestDigest {
					t.Fatal("invalid baseline", err)
				}
				var request model.AccessControlUpdateRequest
				if err := json.Unmarshal([]byte(payload), &request); err != nil {
					t.Fatal(err)
				}
				request.RequiresDualApproval = false
				mutation := legacyRoleMutation(t, f, request)
				mutation.AccessChangeDigest = read.RequestDigest
				// 用另一组完整有效字节替换；摘要一起改变时必须拒绝旧执行上下文。
				altered := payload + " "
				digest := read.RequestDigest
				if changeDigest {
					digest = digestText(altered)
				}
				execReviewSQL(t, f, `UPDATE access_changes SET payload_json=?,request_digest=? WHERE id=?`, altered, digest, c.ID)
				before := f.durableState(t)
				if entry == "runner continuation" {
					_, err = f.engine.updateAccess(context.Background(), f.actors["creator"], request, &approvedAccessExecution{changeID: c.ID, requestDigest: read.RequestDigest})
				} else {
					_, err = f.db.ApplyAccessChangeMutation(context.Background(), c.ID, f.actors["creator"], mutation)
				}
				if !errors.Is(err, store.ErrIdempotency) {
					t.Fatalf("got %v, want digest rejection", err)
				}
				if f.durableState(t) != before {
					t.Fatal("final transaction rejection changed durable state")
				}
			})
		}
	}
}

func TestAccessChangePayloadRawBytesAndAppliedReplay(t *testing.T) {
	f := newRoleReviewFixture(t)
	request := versionedRoleRequest(t, f, roleRequest("raw-role", "Raw bytes", model.PermissionRead))
	raw, err := json.MarshalIndent(request, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	// 使用合法非紧凑编码，证明 apply 不强制重新编码或详情投影的精确编码合同。
	payload := " \n" + string(raw) + "\n"
	c := model.AccessChange{ID: request.IdempotencyKey, IdempotencyKey: request.IdempotencyKey,
		ActorHash: f.actors["creator"], RequestDigest: digestText(payload), State: model.AccessChangePendingApproval,
		RequiresDualApproval: true, ApprovalPolicy: model.ApprovalPolicyTwoParty, ConfirmationPhrase: "批准合成原始字节提案"}
	c, created, err := f.db.CreateAccessChange(context.Background(), c, payload, store.HashConfirmation(c.ConfirmationPhrase))
	if err != nil || !created {
		t.Fatal("create", err)
	}
	mutation := legacyRoleMutation(t, f, request)
	mutation.AccessChangeDigest = c.RequestDigest
	deletionApprove(t, f, c)
	first := f.call(t, "creator", "POST", "/v1/access/changes/"+c.ID+"/apply", nil, 200)
	// 其他策略更新后，原执行结果仍是重放依据；故障载荷不触发重新解析。
	f.apply(t, submitReviewRequest(t, f, roleRequest("unrelated", "Unrelated", model.PermissionRead)))
	execReviewSQL(t, f, `UPDATE access_changes SET payload_json='{' WHERE id=?`, c.ID)
	before := f.durableState(t)
	replay := f.call(t, "creator", "POST", "/v1/access/changes/"+c.ID+"/apply", nil, 200)
	if string(first) != string(replay) {
		t.Fatal("HTTP replay result changed")
	}
	applied, err := f.db.ApplyAccessChangeMutation(context.Background(), c.ID, f.actors["creator"], mutation)
	if err != nil || applied.State != model.AccessChangeApplied {
		t.Fatal("store replay", err)
	}
	f.call(t, "approver", "POST", "/v1/access/changes/"+c.ID+"/apply", nil, 409)
	other := mutation
	other.Actor = f.actors["approver"]
	if _, err = f.db.ApplyAccessChangeMutation(context.Background(), c.ID, other.Actor, other); !errors.Is(err, store.ErrActorMismatch) {
		t.Fatal("store wrong actor", err)
	}
	changed := mutation
	changed.AccessChangeDigest = digestText("other")
	if _, err = f.db.ApplyAccessChangeMutation(context.Background(), c.ID, changed.Actor, changed); !errors.Is(err, store.ErrIdempotency) {
		t.Fatal("store changed digest", err)
	}
	if f.durableState(t) != before {
		t.Fatal("replay changed durable state")
	}
}

func TestAccessChangePayloadActualExecutorRevocationAndLegacyReplay(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "two_party_v1", true: "legacy_two_approvers"}[legacy], func(t *testing.T) {
			f := newRoleReviewFixture(t)
			executor := "creator"
			if legacy {
				executor = "viewer"
				f.apply(t, submitReviewRequest(t, f, model.AccessControlUpdateRequest{Principals: []model.AccessPrincipal{{Subject: f.actors[executor], TenantID: "default", Roles: []string{"platform-admin"}}}}))
			}
			c := submitReviewRequest(t, f, model.AccessControlUpdateRequest{RemovePrincipalSubjects: []string{f.actors[executor]}})
			if legacy {
				// 隔离保存仍受支持的旧策略，原始 payload 和摘要不变；审批仍走真实 HTTP。
				execReviewSQL(t, f, `UPDATE access_changes SET approval_policy='' WHERE id=?`, c.ID)
			}
			approval := model.AccessChangeApprovalRequest{Digest: c.RequestDigest, Confirmation: c.ConfirmationPhrase}
			f.call(t, "creator", "POST", "/v1/access/changes/"+c.ID+"/approve", approval, 409)
			f.call(t, "approver", "POST", "/v1/access/changes/"+c.ID+"/approve", approval, 200)
			if legacy {
				f.call(t, executor, "POST", "/v1/access/changes/"+c.ID+"/apply", nil, 409)
				f.call(t, "other", "POST", "/v1/access/changes/"+c.ID+"/approve", approval, 200)
				f.call(t, "creator", "POST", "/v1/access/changes/"+c.ID+"/apply", nil, 409)
			}
			f.call(t, "approver", "POST", "/v1/access/changes/"+c.ID+"/apply", nil, 409)
			first := f.call(t, executor, "POST", "/v1/access/changes/"+c.ID+"/apply", nil, 200)
			// 真实授权查询必须拒绝刚执行完成的身份；该拒绝可能写审计，故在其后取基线。
			f.call(t, executor, "GET", "/v1/access", nil, 403)
			before := f.durableState(t)
			replay := f.call(t, executor, "POST", "/v1/access/changes/"+c.ID+"/apply", nil, 200)
			if string(first) != string(replay) || f.durableState(t) != before {
				t.Fatal("revoked executor replay changed result or durable state")
			}
		})
	}
}
