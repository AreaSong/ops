package runner

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	"github.com/AreaSong/ops/services/areasong-ops/internal/store"
)

func TestAutoUpdateWindowUsesExplicitTimezone(t *testing.T) {
	now := time.Date(2026, 9, 3, 18, 30, 0, 0, time.UTC)
	if !autoUpdateWindowOpen("02:00-04:00", "Asia/Shanghai", now) {
		t.Fatal("Shanghai maintenance window should be open")
	}
	if autoUpdateWindowOpen("02:00-04:00", "UTC", now) {
		t.Fatal("UTC maintenance window should be closed")
	}
	if !autoUpdateWindowOpen("23:00-03:00", "Asia/Shanghai", now) {
		t.Fatal("cross-midnight maintenance window should be open")
	}
	if autoUpdateWindowOpen("02:00-04:00", "Not/AZone", now) {
		t.Fatal("invalid timezone should never open a maintenance window")
	}
}

func TestEvaluateAutoUpdatesPropagatesEvaluationWriteFailure(t *testing.T) {
	ctx := context.Background()
	engine, database := automaticTestEngine(t)
	discoverRelease(t, engine)
	if err := database.UpsertAutoUpdatePolicy(ctx, model.AutoUpdatePolicyView{
		Service: "demo", ObjectID: "service:demo", TenantID: "default",
		Enabled: true, Channel: "stable", MaintenanceTimezone: "UTC",
		RequireBackup: true, RequireApproval: true, RollbackOnAlert: true,
		ObservationSeconds: 300,
	}); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", filepath.Join(engine.stateRoot, "ops.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.ExecContext(ctx, `CREATE TRIGGER reject_auto_update_evaluation
		BEFORE UPDATE ON auto_update_policies
		WHEN NEW.last_evaluation_at IS NOT NULL
		BEGIN SELECT RAISE(ABORT, 'injected evaluation failure'); END;`); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.EvaluateAutoUpdates(ctx, actorHash()); err == nil || !strings.Contains(err.Error(), "injected evaluation failure") {
		t.Fatalf("err=%v, want evaluation persistence failure", err)
	}
	if _, err := raw.ExecContext(ctx, `DROP TRIGGER reject_auto_update_evaluation`); err != nil {
		t.Fatal(err)
	}
	evaluations, err := engine.EvaluateAutoUpdates(ctx, actorHash())
	if err != nil || len(evaluations) != 1 || evaluations[0].UpdateCreated {
		t.Fatalf("retry evaluations=%+v err=%v", evaluations, err)
	}
	var plans, createdAudits, linkedAudits int
	if err := raw.QueryRowContext(ctx, `SELECT COUNT(*) FROM release_plans`).Scan(&plans); err != nil {
		t.Fatal(err)
	}
	if err := raw.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_entries WHERE event='plan.created'`).Scan(&createdAudits); err != nil {
		t.Fatal(err)
	}
	if err := raw.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_entries WHERE event='auto_update.plan.created'`).Scan(&linkedAudits); err != nil {
		t.Fatal(err)
	}
	if plans != 0 || createdAudits != 0 || linkedAudits != 0 {
		t.Fatalf("plans=%d createdAudits=%d linkedAudits=%d", plans, createdAudits, linkedAudits)
	}
}

func TestBPAutoUpdateRejectionPreservesDualApprovalPolicy(t *testing.T) {
	ctx := context.Background()
	engine, database := automaticTestEngine(t)
	discoverRelease(t, engine)
	if err := database.UpsertAutoUpdatePolicy(ctx, model.AutoUpdatePolicyView{
		Service: "demo", ObjectID: "service:demo", TenantID: "default",
		Enabled: true, Channel: "stable", MaintenanceTimezone: "UTC",
		RequireBackup: true, RequireApproval: true, RollbackOnAlert: true,
		ObservationSeconds: 300,
	}); err != nil {
		t.Fatal(err)
	}
	evaluations, err := engine.EvaluateAutoUpdates(ctx, actorHash())
	if err != nil || len(evaluations) != 1 || evaluations[0].UpdateCreated {
		t.Fatalf("evaluations=%+v err=%v", evaluations, err)
	}

	policy, err := database.GetAutoUpdatePolicy(ctx, "demo")
	if err != nil || !policy.RequireApproval || !policy.RequireBackup || policy.ObservationSeconds != 300 {
		t.Fatal("拒绝后降低自动更新安全策略", err)
	}
	plans, err := database.ListReleasePlans(ctx, 200, 0)
	if err != nil || len(plans) != 0 {
		t.Fatal("自动更新产生新计划", err)
	}
}

func TestValidateAutoUpdatePolicyNormalizesAndRejectsInvalidTimePolicy(t *testing.T) {
	policy := &model.AutoUpdatePolicy{Channel: "stable", MaintenanceWindow: "02:00-04:00Z"}
	if err := validateAutoUpdatePolicyInput("demo", policy); err != nil {
		t.Fatal(err)
	}
	if policy.MaintenanceWindow != "02:00-04:00" || policy.MaintenanceTimezone != "UTC" {
		t.Fatalf("policy was not normalized: %+v", policy)
	}
	for _, invalid := range []*model.AutoUpdatePolicy{
		{Channel: "stable", MaintenanceWindow: "2:00-04:00", MaintenanceTimezone: "UTC"},
		{Channel: "stable", MaintenanceWindow: "02:00-04:00", MaintenanceTimezone: "Not/AZone"},
	} {
		if err := validateAutoUpdatePolicyInput("demo", invalid); err == nil {
			t.Fatalf("invalid time policy accepted: %+v", invalid)
		}
	}
}

func automaticPlanFixture(t *testing.T) (*Engine, *store.Store, model.ReleasePlan, model.AutoUpdatePolicyView) {
	t.Helper()
	engine, database := automaticTestEngine(t)
	discoverRelease(t, engine)
	policy := model.AutoUpdatePolicyView{
		Service: "demo", ObjectID: "service:demo", TenantID: "default", Enabled: true,
		Channel: "stable", MaintenanceTimezone: "UTC", ObservationSeconds: 300,
		RequireApproval: true, RequireBackup: true, RollbackOnAlert: true,
	}
	if err := database.UpsertAutoUpdatePolicy(context.Background(), policy); err != nil {
		t.Fatal(err)
	}

	value := autoUpdatePlanPolicy(policy)
	service := engine.catalog.Services["demo"]
	action := service.Actions["update"]
	action.ObservationSeconds = 300
	service.Actions["update"] = action
	engine.catalog.Services["demo"] = service
	plan := historicalRunnerPlan(t, engine, actorHash(), model.PreviewRequest{Service: "demo", Action: "update", Target: "v1.1.0", AutoUpdatePolicy: &value})
	return engine, database, plan, policy
}

func TestBPAutomaticUpdatePreservesPolicyWithoutNewExecution(t *testing.T) {
	engine, database, plan, policy := automaticPlanFixture(t)
	want := autoUpdatePlanPolicy(policy)
	if plan.ApprovalSummary.AutoUpdatePolicy == nil || *plan.ApprovalSummary.AutoUpdatePolicy != want {
		t.Fatal("审批摘要没有保存完整的自动更新安全策略")
	}

	if _, err := engine.ApproveReleasePlan(context.Background(), strings.Repeat("b", 64), plan.ID, model.ApprovePlanRequest{Digest: plan.Digest, Confirmation: plan.ConfirmationPhrase}); err == nil {
		t.Fatal("旧自动计划获新批准")
	}
	assertBPPlanFrozen(t, engine, plan)
	stored, err := database.GetReleasePlan(context.Background(), plan.ID)
	if err != nil || stored.Digest != plan.Digest {
		t.Fatal("改写历史摘要", err)
	}
}

func TestAutomaticUpdatePolicyDriftRejectsApprovalAndExecution(t *testing.T) {
	for _, beforeApproval := range []bool{true, false} {
		t.Run(fmt.Sprintf("beforeApproval=%t", beforeApproval), func(t *testing.T) {
			engine, database, plan, policy := automaticPlanFixture(t)
			ctx := context.Background()
			if !beforeApproval {
				raw := historicalRunnerDB(t, engine)
				if _, err := raw.Exec("UPDATE release_plans SET state=?,approved_by_hash=? WHERE id=?", model.PlanApproved, strings.Repeat("b", 64), plan.ID); err != nil {
					t.Fatal(err)
				}
			}
			policy.ObservationSeconds = 600
			if err := database.UpsertAutoUpdatePolicy(ctx, policy); err != nil {
				t.Fatal(err)
			}
			var err error
			if beforeApproval {
				_, err = engine.ApproveReleasePlan(ctx, strings.Repeat("b", 64), plan.ID,
					model.ApprovePlanRequest{Digest: plan.Digest, Confirmation: plan.ConfirmationPhrase})
			} else {
				_, _, err = engine.ExecuteReleasePlan(ctx, actorHash(), plan.ID, model.ExecutePlanRequest{IdempotencyKey: mustUUID(t)})
			}
			if err == nil {
				t.Fatalf("策略漂移没有拒绝: %v", err)
			}
			if _, active, err := database.ActiveTask(ctx, "demo"); err != nil || active {
				t.Fatalf("拒绝后不应创建任务: active=%t error=%v", active, err)
			}
			results, err := engine.EvaluateAutoUpdates(ctx, actorHash())
			if err != nil || len(results) != 1 || results[0].UpdateCreated {
				t.Fatalf("策略变化后不能生成新计划: %+v error=%v", results, err)
			}
		})
	}
}

func TestAutomaticUpdateRejectsSingleTargetCanary(t *testing.T) {
	engine, database, _, policy := automaticPlanFixture(t)
	policy.LastPlanID, policy.CanaryPercent = "", 10
	if err := database.UpsertAutoUpdatePolicy(context.Background(), policy); err != nil {
		t.Fatal(err)
	}
	results, err := engine.EvaluateAutoUpdates(context.Background(), actorHash())
	if err != nil || len(results) != 1 || results[0].UpdateCreated || !strings.Contains(results[0].Reason, "单实例") {
		t.Fatalf("无效灰度策略未明确拒绝: %+v error=%v", results, err)
	}
}

func TestAutoUpdateIdempotencyBindsEffectivePolicy(t *testing.T) {
	policy := model.AutoUpdatePolicyView{Service: "demo", Channel: "stable", ObservationSeconds: 300}
	first, digest := autoUpdatePlanRequestIdentity(policy, "v1.2.0")
	policy.ObservationSeconds = 600
	second, changedDigest := autoUpdatePlanRequestIdentity(policy, "v1.2.0")
	if first == second || digest == changedDigest {
		t.Fatal("修改策略后仍复用了旧请求摘要")
	}
}

func TestAutomaticUpdateStartTransactionRejectsPolicyDrift(t *testing.T) {
	engine, database, plan, policy := automaticPlanFixture(t)
	ctx := context.Background()
	raw := historicalRunnerDB(t, engine)
	if _, err := raw.Exec("UPDATE release_plans SET state=?,approved_by_hash=? WHERE id=?", model.PlanApproved, strings.Repeat("b", 64), plan.ID); err != nil {
		t.Fatal(err)
	}
	plan, err := database.GetReleasePlan(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	policy.ObservationSeconds = 600
	if err := database.UpsertAutoUpdatePolicy(ctx, policy); err != nil {
		t.Fatal(err)
	}
	_, err = database.StartPlanTaskWithEvent(ctx, plan, actorHash(), mustUUID(t), mustUUID(t), nil)
	if err == nil {
		t.Fatalf("启动事务接受了失效策略: %v", err)
	}
}

func TestAutomaticUpdatePolicyReplayReturnsStoredPolicy(t *testing.T) {
	engine, database := automaticTestEngine(t)
	ctx := context.Background()
	request := model.AutoUpdatePolicyRequest{Service: "demo", Enabled: true, Channel: "stable",
		MaintenanceTimezone: "UTC", RequireApproval: true, RequireBackup: true, RollbackOnAlert: true,
		ObservationSeconds: 300, IdempotencyKey: mustUUID(t)}
	if _, err := engine.UpdateAutoUpdatePolicy(ctx, actorHash(), request); err != nil {
		t.Fatal(err)
	}
	changed := request
	changed.ObservationSeconds, changed.IdempotencyKey = 600, mustUUID(t)
	if _, err := engine.UpdateAutoUpdatePolicy(ctx, actorHash(), changed); err != nil {
		t.Fatal(err)
	}
	view, err := engine.UpdateAutoUpdatePolicy(ctx, actorHash(), request)
	stored, getErr := database.GetAutoUpdatePolicy(ctx, "demo")
	if err != nil || getErr != nil || view.ObservationSeconds != 600 || view.ObservationSeconds != stored.ObservationSeconds {
		t.Fatalf("幂等重放回显过时策略: view=%+v stored=%+v errors=%v/%v", view, stored, err, getErr)
	}
}

func TestBPAutomaticUpdateNeverAppliesWithoutIntegration(t *testing.T) {
	engine, database, plan, _ := automaticPlanFixture(t)
	executor := engine.executor.(*automaticTestExecutor)
	executor.missingBackup = true

	assertBPPlanFrozen(t, engine, plan)
	taskList, err := database.ListTasks(context.Background(), 200, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range taskList {
		if task.Action == "update" {
			t.Fatal("缺少准入仍启动更新")
		}
	}
	for _, call := range executor.calls {
		if call.Phase == "apply" {
			t.Fatal("仍调用apply")
		}
	}
}

func TestBPAutoEvaluationPreservesPriorApproval(t *testing.T) {
	engine, database, plan, policy := automaticPlanFixture(t)
	ctx := context.Background()
	raw := historicalRunnerDB(t, engine)
	if _, err := raw.Exec("UPDATE release_plans SET state=?,approved_by_hash=?,approved_at=? WHERE id=?", model.PlanApproved, strings.Repeat("b", 64), time.Now().UTC().Format(time.RFC3339Nano), plan.ID); err != nil {
		t.Fatal(err)
	}
	before, err := database.GetReleasePlan(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	policy.LastPlanID = plan.ID
	policy.ObservationSeconds = 600
	if err = database.UpsertAutoUpdatePolicy(ctx, policy); err != nil {
		t.Fatal(err)
	}
	evaluations, err := engine.EvaluateAutoUpdates(ctx, actorHash())
	if err != nil || len(evaluations) != 1 || evaluations[0].UpdateCreated {
		t.Fatal("陈旧计划评估产生新工作", err)
	}
	after, err := database.GetReleasePlan(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(before)
	b, _ := json.Marshal(after)
	if string(a) != string(b) {
		t.Fatal("自动评估改写历史批准")
	}
	if len(engine.executor.(*automaticTestExecutor).calls) != 0 {
		t.Fatal("评估调用适配器")
	}
}
