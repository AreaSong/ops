package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type lostPreparationResponse struct{ header http.Header }

func (w *lostPreparationResponse) Header() http.Header     { return w.header }
func (*lostPreparationResponse) WriteHeader(int)           {}
func (*lostPreparationResponse) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestPreparationLostResponseIsReadOnlyReplay(t *testing.T) {
	engine, profile := preparationEngine(t)
	request := bpRequest(t)
	payload, _ := json.Marshal(request)
	req := httptest.NewRequest(http.MethodPost, "/v1/plans", bytes.NewReader(payload))
	req.Header.Set(actorHeader, actorHash())
	NewServer(engine, engine.store).ServeHTTP(&lostPreparationResponse{header: make(http.Header)}, req)
	r := postPreparation(t, engine, actorHash(), "/v1/plans", request)
	if r.Code != http.StatusCreated || profile.calls != 1 {
		t.Fatal("响应丢失重复检查", r.Code, r.Body.String())
	}
}

func TestPreparationCommitFailuresDoNotRepeatInspection(t *testing.T) {
	for _, point := range []string{"begin", "finish"} {
		t.Run(point, func(t *testing.T) {
			engine, profile := preparationEngine(t)
			raw := historicalRunnerDB(t, engine)
			if _, err := raw.Exec(`PRAGMA foreign_keys=ON; CREATE TABLE preparation_fault(ref TEXT REFERENCES release_plan_preparations(id) DEFERRABLE INITIALLY DEFERRED)`); err != nil {
				t.Fatal(err)
			}
			statement := `CREATE TRIGGER preparation_fault_trigger AFTER UPDATE ON release_plan_preparations WHEN NEW.state='preparing' BEGIN INSERT INTO preparation_fault VALUES('absent'); END`
			if point == "finish" {
				statement = `CREATE TRIGGER preparation_fault_trigger AFTER INSERT ON release_plans BEGIN INSERT INTO preparation_fault VALUES('absent'); END`
			}
			if _, err := raw.Exec(statement); err != nil {
				t.Fatal(err)
			}
			request := bpRequest(t)
			if _, err := engine.createManualReleasePlan(context.Background(), actorHash(), request); err == nil {
				t.Fatal("COMMIT故障未生效")
			}
			count := profile.calls
			if (point == "begin" && count != 0) || (point == "finish" && count != 1) {
				t.Fatal("失败边界调用次数", count)
			}
			r, found, err := engine.store.GetPlanPreparationByRequest(context.Background(), request.IdempotencyKey)
			if err != nil || !found || r.State == model.WorkClosed {
				t.Fatal("提交失败释放登记", err)
			}
			if _, err = engine.createManualReleasePlan(context.Background(), actorHash(), request); err == nil {
				t.Fatal("失败请求重新执行")
			}
			if profile.calls != count {
				t.Fatal("重复检查")
			}
			if _, err = raw.Exec("DROP TRIGGER preparation_fault_trigger"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPreparationCleanupFailureRetainsBlocker(t *testing.T) {
	engine, profile := preparationEngine(t)
	engine.inspectionCleanup = func(string) error { return errors.New("injected cleanup failure") }
	_ = profile
	request := bpRequest(t)
	_, err := engine.createManualReleasePlan(context.Background(), actorHash(), request)
	if err == nil {
		t.Fatal("目录清理故障未生效")
	}
	r, found, err := engine.store.GetPlanPreparationByRequest(context.Background(), request.IdempotencyKey)
	if err != nil || !found || r.State != model.WorkUncertain || r.ProducedPlanID != "" {
		t.Fatal("清理失败未保持阻断", err)
	}
}

func TestPreparationCallerAndTargetGates(t *testing.T) {
	for _, scenario := range []string{"actor_disabled", "target_disabled", "generation_zero", "approval_generation_changed", "actor_moved"} {
		t.Run(scenario, func(t *testing.T) {
			engine, profile := preparationEngine(t)
			raw := historicalRunnerDB(t, engine)
			ctx := context.Background()
			request := bpRequest(t)
			var plan model.ReleasePlan
			var err error
			if scenario == "approval_generation_changed" {
				plan, err = engine.createManualReleasePlan(ctx, actorHash(), request)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = raw.Exec(`UPDATE tenants SET lifecycle_generation=3 WHERE id='team'`); err != nil {
					t.Fatal(err)
				}
				_, err = engine.ApproveReleasePlan(ctx, stringsForHistoricalApprover(), plan.ID, model.ApprovePlanRequest{Digest: plan.Digest, Confirmation: plan.ConfirmationPhrase})
				if err == nil {
					t.Fatal("新代次补给旧批准")
				}
				return
			}
			policy, snap, err := engine.effectiveAccessPolicy(ctx)
			if err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "actor_disabled":
				p := policy.Principals[actorHash()]
				p.Status = "disabled"
				policy.Principals[actorHash()] = p
			case "actor_moved":
				p := policy.Principals[actorHash()]
				p.TenantID = "missing"
				policy.Principals[actorHash()] = p
			case "target_disabled":
				tenant := policy.Tenants["team"]
				tenant.Status = "disabled"
				policy.Tenants["team"] = tenant
				if _, err = raw.Exec(`UPDATE tenants SET status='disabled',lifecycle_generation=2 WHERE id='team'`); err != nil {
					t.Fatal(err)
				}
			case "generation_zero":
				if _, err = raw.Exec(`UPDATE tenants SET lifecycle_generation=0 WHERE id='team'`); err != nil {
					t.Fatal(err)
				}
			}
			data, _ := json.Marshal(policy)
			// 私有生命周期真实转换由 Store 双连接测试覆盖；这里仅合成当前权威快照以测试 HTTP/Runner 门控。
			if _, err = raw.Exec(`UPDATE access_policy_snapshots SET policy_json=?,digest=? WHERE version=?`, string(data), "sha256:"+model.WorkDigest(string(data)), snap.Version); err != nil {
				t.Fatal(err)
			}
			if _, err = engine.createManualReleasePlan(ctx, actorHash(), request); err == nil {
				t.Fatal("无效主体/目标放行")
			}
			if profile.calls != 0 {
				t.Fatal("门控发生在副作用之后")
			}
		})
	}
}

func TestPreparationGenericOriginsRemainClosed(t *testing.T) {
	engine, profile := preparationEngine(t)
	ctx := context.Background()
	for _, action := range []string{"update", "restart", "inspect", "rollback"} {
		request := bpRequest(t)
		request.Action = action
		if _, err := engine.CreateReleasePlan(ctx, actorHash(), request); !errors.Is(err, model.ErrReleaseNotIntegrated) {
			t.Fatal(action, err)
		}
	}
	if profile.calls != 0 {
		t.Fatal("内部来源绕入人工检查")
	}
}

func TestPreparationInternalKeyCollisionCannotBorrowManualPlan(t *testing.T) {
	engine, profile := preparationEngine(t)
	ctx := context.Background()
	op := model.BatchOperation{ID: "synthetic-batch", ActorHash: actorHash(), ApprovedByHash: stringsForHistoricalApprover(), ExecutedByHash: actorHash(), Action: "update", Target: "v1.1.0", RequiresDualApproval: true, ApprovalPolicy: model.ApprovalPolicyTwoParty, ApprovalPolicyVersion: model.CurrentBatchApprovalPolicyVersion}
	item := model.BatchItem{ID: "synthetic-item", Service: "demo", State: model.BatchNodeReady}
	request := bpRequest(t)
	request.IdempotencyKey = batchItemIdempotencyKey(op.ID, item.ID, "plan")
	plan, err := engine.createManualReleasePlan(ctx, actorHash(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = engine.CreateReleasePlan(ctx, actorHash(), request); !errors.Is(err, model.ErrReleaseNotIntegrated) {
		t.Fatal("内部来源借用人工v2", err)
	}
	engine.startBatchItem(ctx, op, item)
	engine.Wait()
	stored, err := engine.store.GetReleasePlan(ctx, plan.ID)
	if err != nil || stored.State != model.PlanPendingApproval || stored.ApprovedByHash != "" {
		t.Fatal("批量推进了人工批准", err)
	}
	changed := request
	changed.RequiresDualApproval = true
	if _, err = engine.createManualReleasePlan(ctx, actorHash(), changed); err == nil {
		t.Fatal("来源载荷变更仍当重放")
	}
	if profile.calls != 1 {
		t.Fatal("键冲突重复检查")
	}
}

func TestPreparationLoopbackHTTPContract(t *testing.T) {
	engine, profile := preparationEngine(t)
	server := httptest.NewServer(NewServer(engine, engine.store))
	defer server.Close()
	post := func(actor, path string, input any, status int) []byte {
		t.Helper()
		body, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		request, err := http.NewRequest(http.MethodPost, server.URL+path, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set(actorHeader, actor)
		request.Header.Set("Content-Type", "application/json")
		response, err := server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		if err != nil || response.StatusCode != status {
			t.Fatalf("HTTP=%d expected=%d: %s %v", response.StatusCode, status, data, err)
		}
		return data
	}
	request := bpRequest(t)
	data := post(actorHash(), "/v1/plans", request, http.StatusCreated)
	var plan model.ReleasePlan
	if err := json.Unmarshal(data, &plan); err != nil {
		t.Fatal(err)
	}
	if plan.ApprovalSummary.Lifecycle == nil || len(plan.ApprovalSummary.Lifecycle.Targets) != 3 {
		t.Fatal("HTTP 遗失目标代次")
	}
	post(actorHash(), "/v1/plans", request, http.StatusCreated)
	post(stringsForHistoricalApprover(), "/v1/plans/"+plan.ID+"/approve",
		model.ApprovePlanRequest{Digest: plan.Digest, Confirmation: plan.ConfirmationPhrase}, http.StatusOK)
	post(actorHash(), "/v1/plans/"+plan.ID+"/execute", model.ExecutePlanRequest{IdempotencyKey: mustUUID(t)}, http.StatusConflict)
	post(actorHash(), "/v1/plans/"+plan.ID+"/close", model.ClosePlanRequest{IdempotencyKey: mustUUID(t)}, http.StatusConflict)
	if profile.calls != 1 {
		t.Fatal("HTTP 重放或执行重复检查")
	}
}

func TestPreparationScheduleOffsetUsesStableUTCBinding(t *testing.T) {
	engine, profile := preparationEngine(t)
	ctx := context.Background()
	request := bpRequest(t)
	at := time.Now().UTC().Add(time.Hour).In(time.FixedZone("CST", 8*60*60))
	request.ScheduleAt = &at
	plan, err := engine.createManualReleasePlan(ctx, actorHash(), request)
	if err != nil {
		t.Fatal(err)
	}
	utc := at.UTC()
	request.ScheduleAt = &utc
	replay, err := engine.createManualReleasePlan(ctx, actorHash(), request)
	if err != nil || replay.ID != plan.ID || replay.Digest != plan.Digest {
		t.Fatal("等价时区未安全重放", err)
	}
	approved, err := engine.ApproveReleasePlan(ctx, stringsForHistoricalApprover(), plan.ID, model.ApprovePlanRequest{Digest: plan.Digest, Confirmation: plan.ConfirmationPhrase})
	if err != nil || approved.State != model.PlanScheduled || !approved.ScheduleAt.Equal(at) || profile.calls != 1 {
		t.Fatal("时区转换破坏批准绑定", err)
	}
}
