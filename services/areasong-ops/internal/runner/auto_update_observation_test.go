package runner

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	"github.com/AreaSong/ops/services/areasong-ops/internal/store"
)

func observingAutomaticPlan(t *testing.T) (*Engine, *store.Store, model.ReleasePlan, model.Task) {
	t.Helper()
	engine, database, plan, _ := automaticPlanFixture(t)
	ctx := context.Background()
	if _, err := engine.ApproveReleasePlan(ctx, strings.Repeat("b", 64), plan.ID,
		model.ApprovePlanRequest{Digest: plan.Digest, Confirmation: plan.ConfirmationPhrase}); err != nil {
		t.Fatal(err)
	}
	task, _, err := engine.ExecuteReleasePlan(ctx, actorHash(), plan.ID, model.ExecutePlanRequest{IdempotencyKey: mustUUID(t)})
	if err != nil {
		t.Fatal(err)
	}
	engine.Wait()
	task, err = database.GetTask(ctx, task.ID)
	if err != nil || task.State != model.TaskSucceeded {
		t.Fatalf("自动更新未成功: %+v error=%v", task, err)
	}
	plan, err = database.GetReleasePlan(ctx, plan.ID)
	if err != nil || plan.State != model.PlanObserving {
		t.Fatalf("自动更新未进入观察: %+v error=%v", plan, err)
	}
	manager := engine.alertmanager.(*fakeAlertmanager)
	manager.mu.Lock()
	manager.alerts = []model.ActiveAlert{{Fingerprint: "alert-automatic-update", AlertName: "AppHttpProbeFailed",
		Severity: "critical", Labels: map[string]string{"service": "demo"}}}
	manager.mu.Unlock()
	return engine, database, plan, task
}

func TestAutomaticObservationAlertRollsBackExactlyOnce(t *testing.T) {
	engine, database, _, task := observingAutomaticPlan(t)
	ctx := context.Background()
	if err := engine.ReconcileAutoUpdateObservations(ctx); err != nil {
		t.Fatal(err)
	}
	completed, err := database.GetTask(ctx, task.ID)
	if err != nil || completed.State != model.TaskRolledBack {
		t.Fatalf("告警未触发回滚: %+v error=%v", completed, err)
	}
	if err := engine.ReconcileAutoUpdateObservations(ctx); err != nil {
		t.Fatal(err)
	}
	executor := engine.executor.(*automaticTestExecutor)
	count := 0
	for _, call := range executor.calls {
		if call.Phase == "rollback" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("回滚执行次数=%d，期望1", count)
	}
}

func TestAutomaticObservationRollbackFailureDoesNotRetry(t *testing.T) {
	engine, database, plan, task := observingAutomaticPlan(t)
	executor := engine.executor.(*automaticTestExecutor)
	executor.failPhase = "rollback"
	ctx := context.Background()
	if err := engine.ReconcileAutoUpdateObservations(ctx); err == nil {
		t.Fatal("回滚失败被隐藏")
	}
	completed, err := database.GetTask(ctx, task.ID)
	if err != nil || completed.State != model.TaskNeedsAttention {
		t.Fatalf("回滚失败状态=%+v error=%v", completed, err)
	}
	failed, err := database.GetReleasePlan(ctx, plan.ID)
	if err != nil || failed.State != model.PlanNeedsAttention {
		t.Fatalf("计划未进入人工关注: %+v error=%v", failed, err)
	}
	before := len(executor.calls)
	if err := engine.ReconcileAutoUpdateObservations(ctx); err != nil || len(executor.calls) != before {
		t.Fatalf("失败后重新调用了适配器: %v", err)
	}
}

func TestAutomaticObservationRefusesChangedEvidenceOrRuntime(t *testing.T) {
	for _, tamperBackup := range []bool{true, false} {
		name := "runtime"
		if tamperBackup {
			name = "backup"
		}
		t.Run(name, func(t *testing.T) {
			engine, database, plan, task := observingAutomaticPlan(t)
			ctx := context.Background()
			executor := engine.executor.(*automaticTestExecutor)
			if tamperBackup {
				point, err := database.GetRecoveryPoint(ctx, task.RecoveryPointID)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(point.Evidence.Artifacts[0].Path, []byte("tampered"), 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				executor.updated = false
			}
			if err := engine.ReconcileAutoUpdateObservations(ctx); err != nil {
				t.Fatal(err)
			}
			blocked, err := database.GetReleasePlan(ctx, plan.ID)
			if err != nil || blocked.State != model.PlanNeedsAttention {
				t.Fatalf("证据变化未阻断: %+v error=%v", blocked, err)
			}
			for _, call := range executor.calls {
				if call.Phase == "rollback" {
					t.Fatal("证据变化仍执行了回滚")
				}
			}
		})
	}
}

func TestAutomaticObservationUsesIdentityWithoutHealthyApplication(t *testing.T) {
	engine, database, _, task := observingAutomaticPlan(t)
	executor := engine.executor.(*automaticTestExecutor)
	executor.healthFailed = true
	if err := engine.ReconcileAutoUpdateObservations(context.Background()); err != nil {
		t.Fatal(err)
	}
	finished, err := database.GetTask(context.Background(), task.ID)
	if err != nil || finished.State != model.TaskRolledBack {
		t.Fatalf("应用不健康但身份明确时未完成回滚: %+v error=%v", finished, err)
	}
}

func TestAutomaticObservationStopWaitsForRollbackTerminal(t *testing.T) {
	engine, database, _, task := observingAutomaticPlan(t)
	executor := engine.executor.(*automaticTestExecutor)
	executor.rollbackEntered = make(chan struct{})
	engine.startAutoUpdateObservationMonitor(context.Background(), time.Millisecond)
	select {
	case <-executor.rollbackEntered:
	case <-time.After(3 * time.Second):
		engine.Stop()
		t.Fatal("观察期回滚未启动")
	}
	engine.Stop()
	done := make(chan struct{})
	go func() { engine.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("停止后观察器/回滚未收口")
	}
	finished, err := database.GetTask(context.Background(), task.ID)
	if err != nil || finished.State != model.TaskNeedsAttention {
		t.Fatalf("中断回滚未标人工关注: %+v error=%v", finished, err)
	}
}

func TestAutomaticObservationPersistsResultWithoutRepeatingRollback(t *testing.T) {
	engine, database, _, task := observingAutomaticPlan(t)
	raw, err := sql.Open("sqlite", filepath.Join(engine.stateRoot, "ops.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	_, err = raw.Exec(`CREATE TRIGGER reject_automatic_rollback_terminal BEFORE INSERT ON audit_entries
		WHEN NEW.event='task.terminal' AND NEW.outcome='rolled_back'
		BEGIN SELECT RAISE(ABORT,'injected rollback terminal failure'); END;`)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.ReconcileAutoUpdateObservations(context.Background()); err == nil {
		t.Fatal("终态持久化失败被当作回滚成功")
	}
	executor := engine.executor.(*automaticTestExecutor)
	before := len(executor.calls)
	if _, err := raw.Exec(`DROP TRIGGER reject_automatic_rollback_terminal`); err != nil {
		t.Fatal(err)
	}
	if err := engine.ReconcileAutoUpdateObservations(context.Background()); err != nil {
		t.Fatal(err)
	}
	finished, err := database.GetTask(context.Background(), task.ID)
	if err != nil || finished.State != model.TaskRolledBack || len(executor.calls) != before {
		t.Fatalf("回执收口失败或重放了回滚: %+v error=%v", finished, err)
	}
}

func TestAutomaticObservationClaimsBeforeWaitingForGlobalBackupLock(t *testing.T) {
	engine, database, plan, task := observingAutomaticPlan(t)
	if !engine.acquire([]string{"backup:global"}, "another-service") {
		t.Fatal("无法设置并发夹具")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := engine.reconcileAutoUpdateObservations(ctx, true); err != nil {
		engine.release([]string{"backup:global"}, "another-service")
		t.Fatal(err)
	}
	queued, err := database.GetReleasePlan(ctx, plan.ID)
	if err != nil || queued.State != model.PlanExecuting {
		engine.release([]string{"backup:global"}, "another-service")
		t.Fatalf("等待其他服务时没有及时固定回滚请求: %+v error=%v", queued, err)
	}
	engine.release([]string{"backup:global"}, "another-service")
	engine.Wait()
	finished, err := database.GetTask(ctx, task.ID)
	if err != nil || finished.State != model.TaskRolledBack {
		t.Fatalf("等待锁后回滚未完成: %+v error=%v", finished, err)
	}
}

func TestAutomaticRollbackReceiptSurvivesRunnerRestart(t *testing.T) {
	engine, database, _, task := observingAutomaticPlan(t)
	path := filepath.Join(engine.stateRoot, "ops.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = raw.Exec(`CREATE TRIGGER reject_restart_receipt BEFORE INSERT ON audit_entries
		WHEN NEW.event='task.terminal' AND NEW.outcome='rolled_back'
		BEGIN SELECT RAISE(ABORT,'injected failure'); END;`)
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.ReconcileAutoUpdateObservations(context.Background()); err == nil {
		t.Fatal("终态故障未上报")
	}
	if _, err := raw.Exec(`DROP TRIGGER reject_restart_receipt`); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	count, err := RecoverAutomaticRollbackReceipts(context.Background(), engine.catalog, reopened, engine.stateRoot, engine.alertmanager)
	if err != nil || count != 1 {
		t.Fatalf("重启补交count=%d err=%v", count, err)
	}
	if _, err := reopened.RecoverInterrupted(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	finished, err := reopened.GetTask(context.Background(), task.ID)
	if err != nil || finished.State != model.TaskRolledBack {
		t.Fatalf("有效回执被中断分类覆盖: %+v err=%v", finished, err)
	}
}
