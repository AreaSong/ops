package runner

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	"github.com/AreaSong/ops/services/areasong-ops/internal/store"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 这些夹具只写自行创建的测试库，明确模拟升级前事实，不调用关闭入口或 legacy 实现。
func historicalRunnerDB(t *testing.T, engine *Engine) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(engine.stateRoot, "ops.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func seedHistoricalRunnerTask(t *testing.T, engine *Engine, task model.Task) model.Task {
	t.Helper()
	db := historicalRunnerDB(t, engine)
	if task.ID == "" {
		task.ID = mustUUID(t)
	}
	if task.IdempotencyKey == "" {
		task.IdempotencyKey = mustUUID(t)
	}
	if task.CreatedAt.IsZero() {
		task.CreatedAt = time.Now().UTC()
	}
	if task.State == "" {
		task.State = model.TaskSucceeded
	}
	if task.RequestHash == "" {
		task.RequestHash = store.HashConfirmation(task.PlanID + "\x00" + task.PlanDigest)
	}
	snapshot, _ := json.Marshal(task.Snapshot)
	stages, _ := json.Marshal(task.Stages)
	var started, finished any
	if task.State != model.TaskQueued {
		started = task.CreatedAt.Format(time.RFC3339Nano)
	}
	if task.State.Terminal() {
		finished = task.CreatedAt.Format(time.RFC3339Nano)
	}
	_, err := db.Exec(`INSERT INTO tasks(id,idempotency_key,request_hash,actor_hash,service,action,target,risk,state,preview_id,plan_id,plan_digest,snapshot_json,stages_json,created_at,started_at,finished_at,runner_owner,production_changed,recovery_point_id,retryable)
 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		task.ID, task.IdempotencyKey, task.RequestHash, task.ActorHash, task.Service, task.Action, task.Target, task.Risk, task.State, task.PreviewID, task.PlanID, task.PlanDigest, snapshot, stages,
		task.CreatedAt.Format(time.RFC3339Nano), started, finished, engine.owner, task.ProductionChanged, task.RecoveryPointID, task.Retryable)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := engine.store.GetTask(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	return saved
}

func seedRunnerPreviewTask(t *testing.T, engine *Engine, ctx context.Context, actor string, request model.StartTaskRequest, id string) (model.Task, bool, error) {
	t.Helper()
	preview, err := engine.store.GetPreview(ctx, request.PreviewID)
	if err != nil {
		return model.Task{}, false, err
	}
	stages := []model.TaskStage{}
	for _, step := range preview.Steps {
		stages = append(stages, model.TaskStage{Name: step, State: model.StagePending})
	}
	task := seedHistoricalRunnerTask(t, engine, model.Task{ID: id, IdempotencyKey: request.IdempotencyKey, RequestHash: store.HashConfirmation(preview.ID + "\x00" + request.Confirmation),
		ActorHash: actor, Service: preview.Service, Action: preview.Action, Target: preview.Target, Risk: preview.Risk, State: model.TaskRunning, PreviewID: preview.ID, Snapshot: preview.Snapshot, Stages: stages})
	return task, true, nil
}

func historicalRunnerPlan(t *testing.T, engine *Engine, actor string, request model.PreviewRequest) model.ReleasePlan {
	t.Helper()
	service, action, err := engine.resolveAction(request.Service, request.Action, request.Target)
	if err != nil {
		t.Fatal(err)
	}
	tenant := service.TenantID
	if tenant == "" {
		tenant = "default"
	}
	dual := requiredDualApproval(service, tenant, action, request.RequiresDualApproval)
	policy := approvalPolicyFor(service, action, dual)
	phrase := renderConfirmation(action.ConfirmationTemplate, service.Name, request.Target)
	summary := model.ApprovalSummary{SchemaVersion: 1, ApprovalPolicy: policy, Service: service.Name, Action: action.Name, Target: request.Target, TenantID: tenant, ServerID: service.ServerID,
		Risk: action.Risk, Steps: action.Steps, Scope: action.Scope, Impact: action.Impact, Rollback: action.Rollback, ObservationSeconds: action.ObservationSeconds,
		ScheduleAt: request.ScheduleAt, ConfirmationPhrase: phrase, ExpectedBefore: map[string]any{"currentVersion": "1.0.0"}, AutoUpdatePolicy: request.AutoUpdatePolicy}
	summary.ApprovalException = approvalExceptionFor(service, tenant, action.Name, dual)
	digest, err := model.ReleaseApprovalDigest(summary)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	plan := model.ReleasePlan{ID: mustUUID(t), ActorHash: actor, Service: service.Name, Action: action.Name, Target: request.Target, TenantID: tenant, ServerID: service.ServerID,
		Risk: action.Risk, State: model.PlanPendingApproval, Digest: digest, ApprovalSummary: summary, ConfirmationPhrase: phrase, RequiresConfirmation: action.Risk != model.RiskReadOnly,
		RequiresDualApproval: dual, ApprovalPolicy: policy, ObservationSeconds: action.ObservationSeconds, ScheduleAt: request.ScheduleAt, CreatedAt: now, UpdatedAt: now}
	plan.RequestIdempotencyKey = request.IdempotencyKey
	plan.RequestDigest = request.RequestDigest
	if plan.RequestDigest == "" {
		plan.RequestDigest = digestText(strings.Join([]string{actor, request.Service, request.Action, request.Target, request.RestoreMode, request.RecoveryPointID, scheduleText(request.ScheduleAt)}, "\x00"))
	}
	if err = engine.store.CreateReleasePlan(context.Background(), store.ReleasePlanInput{Plan: plan, ConfirmationHash: store.HashConfirmation(phrase)}); err != nil {
		t.Fatal(err)
	}
	return plan
}

func historicalObservedPlan(t *testing.T, engine *Engine, action string) (model.ReleasePlan, model.Task) {
	t.Helper()
	target := ""
	if action == "update" {
		target = "v1.1.0"
	}
	plan := historicalRunnerPlan(t, engine, actorHash(), model.PreviewRequest{Service: "demo", Action: action, Target: target})
	task := seedHistoricalRunnerTask(t, engine, model.Task{ActorHash: actorHash(), Service: plan.Service, Action: plan.Action, Target: plan.Target, Risk: plan.Risk, State: model.TaskSucceeded,
		PlanID: plan.ID, PlanDigest: plan.Digest, Snapshot: plan.ApprovalSummary.ExpectedBefore})
	db := historicalRunnerDB(t, engine)
	now := time.Now().UTC()
	_, err := db.Exec(`UPDATE release_plans SET state=?,task_id=?,approved_by_hash=?,observation_started_at=?,observation_ends_at=?,maintenance_silence_id=? WHERE id=?`,
		model.PlanObserving, task.ID, stringsForHistoricalApprover(), now.Add(-time.Hour).Format(time.RFC3339Nano), now.Add(-time.Second).Format(time.RFC3339Nano), "historical-silence", plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	plan, err = engine.store.GetReleasePlan(context.Background(), plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	return plan, task
}

func stringsForHistoricalApprover() string {
	return "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
}

func historicalRunnerPreview(t *testing.T, engine *Engine, actor string, request model.PreviewRequest) model.Preview {
	t.Helper()
	service, action, err := engine.resolveAction(request.Service, request.Action, request.Target)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	preview := model.Preview{ID: mustUUID(t), ActorHash: actor, Service: service.Name, Action: action.Name, Target: request.Target, Risk: action.Risk,
		Steps: action.Steps, Snapshot: map[string]any{"currentVersion": "1.0.0"}, ConfirmationPhrase: renderConfirmation(action.ConfirmationTemplate, service.Name, request.Target), CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	if err = engine.store.CreatePreview(context.Background(), store.PreviewInput{Preview: preview, ConfirmationHash: store.HashConfirmation(preview.ConfirmationPhrase)}); err != nil {
		t.Fatal(err)
	}
	return preview
}

func assertBPPlanFrozen(t *testing.T, engine *Engine, plan model.ReleasePlan) {
	t.Helper()
	ctx := context.Background()
	before, err := engine.store.GetReleasePlan(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := engine.store.ListTasks(ctx, 200, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"execute", "close"} {
		response := postPreparation(t, engine, plan.ActorHash, "/v1/plans/"+plan.ID+"/"+action, map[string]string{"idempotencyKey": mustUUID(t)})
		if response.Code < 400 {
			t.Fatal("B-P放行新" + action)
		}
	}
	after, err := engine.store.GetReleasePlan(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(before)
	b, _ := json.Marshal(after)
	if string(a) != string(b) {
		t.Fatal("拒绝改写历史计划")
	}
	remaining, err := engine.store.ListTasks(ctx, 200, 0)
	if err != nil {
		t.Fatal(err)
	}
	a, _ = json.Marshal(tasks)
	b, _ = json.Marshal(remaining)
	if string(a) != string(b) {
		t.Fatal("拒绝改写历史任务")
	}
	if executor, ok := engine.executor.(*fakeExecutor); ok {
		executor.mu.Lock()
		defer executor.mu.Unlock()
		if len(executor.calls) != 0 {
			t.Fatal("拒绝路径调用适配器")
		}
	}
	if manager, ok := engine.alertmanager.(*fakeAlertmanager); ok {
		if len(manager.created) != 0 || len(manager.expired) != 0 {
			t.Fatal("拒绝路径更改静默")
		}
	}
}
