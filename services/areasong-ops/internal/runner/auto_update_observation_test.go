package runner

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
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
	engine.backupRoot = t.TempDir()
	service := enableRecoveryActions(engine)
	point := createVerifiedRecoveryPoint(t, engine, database, service)
	task := seedHistoricalRunnerTask(t, engine, model.Task{ActorHash: actorHash(), Service: plan.Service, Action: plan.Action, Target: plan.Target, Risk: plan.Risk, State: model.TaskSucceeded, PlanID: plan.ID, PlanDigest: plan.Digest, RecoveryPointID: point.ID, Snapshot: plan.ApprovalSummary.ExpectedBefore})
	raw := historicalRunnerDB(t, engine)
	now := time.Now().UTC()
	if _, err := raw.Exec("UPDATE release_plans SET state=?,task_id=?,approved_by_hash=?,executed_by_hash=?,observation_started_at=?,observation_ends_at=? WHERE id=?", model.PlanObserving, task.ID, strings.Repeat("b", 64), actorHash(), now.Format(time.RFC3339Nano), now.Add(time.Hour).Format(time.RFC3339Nano), plan.ID); err != nil {
		t.Fatal(err)
	}
	plan, err := database.GetReleasePlan(context.Background(), plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	engine.alertmanager.(*fakeAlertmanager).alerts = []model.ActiveAlert{{Fingerprint: "alert-automatic-update", AlertName: "AppHttpProbeFailed", Labels: map[string]string{"service": "demo"}}}
	return engine, database, plan, task
}

func TestBPAutomaticObservationNeverStartsRollback(t *testing.T) {
	engine, database, _, task := observingAutomaticPlan(t)
	ctx := context.Background()
	if err := engine.ReconcileAutoUpdateObservations(ctx); err == nil {
		t.Fatal(err)
	}
	completed, err := database.GetTask(ctx, task.ID)
	if err != nil || completed.State != model.TaskSucceeded {
		t.Fatalf("告警未触发回滚: %+v error=%v", completed, err)
	}
	if err := engine.ReconcileAutoUpdateObservations(ctx); err == nil {
		t.Fatal(err)
	}
	executor := engine.executor.(*automaticTestExecutor)
	count := 0
	for _, call := range executor.calls {
		if call.Phase == "rollback" {
			count++
		}
	}
	if count != 0 {
		t.Fatalf("回滚执行次数=%d，期望1", count)
	}
}

func TestBPAutomaticObservationCannotRetryHistoricalWork(t *testing.T) {
	engine, database, plan, task := observingAutomaticPlan(t)
	executor := engine.executor.(*automaticTestExecutor)
	executor.failPhase = "rollback"
	ctx := context.Background()
	if err := engine.ReconcileAutoUpdateObservations(ctx); err == nil {
		t.Fatal("回滚失败被隐藏")
	}
	completed, err := database.GetTask(ctx, task.ID)
	if err != nil || completed.State != model.TaskSucceeded {
		t.Fatalf("回滚失败状态=%+v error=%v", completed, err)
	}
	failed, err := database.GetReleasePlan(ctx, plan.ID)
	if err != nil || failed.State != model.PlanObserving {
		t.Fatalf("计划未进入人工关注: %+v error=%v", failed, err)
	}
	before := len(executor.calls)
	if err := engine.ReconcileAutoUpdateObservations(ctx); err == nil || len(executor.calls) != before {
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
			if err := engine.ReconcileAutoUpdateObservations(ctx); err == nil {
				t.Fatal(err)
			}
			blocked, err := database.GetReleasePlan(ctx, plan.ID)
			if err != nil || blocked.State != model.PlanObserving {
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

func TestBPAutomaticObservationDoesNotProbeUnhealthyApplication(t *testing.T) {
	engine, database, _, task := observingAutomaticPlan(t)
	executor := engine.executor.(*automaticTestExecutor)
	executor.healthFailed = true
	if err := engine.ReconcileAutoUpdateObservations(context.Background()); err == nil {
		t.Fatal(err)
	}
	finished, err := database.GetTask(context.Background(), task.ID)
	if err != nil || finished.State != model.TaskSucceeded {
		t.Fatalf("应用不健康但身份明确时未完成回滚: %+v error=%v", finished, err)
	}
}

func TestBPAutomaticObservationMonitorDoesNotStart(t *testing.T) {
	engine, database, _, task := observingAutomaticPlan(t)
	engine.StartAutoUpdateObservationMonitor(context.Background())
	engine.Stop()
	engine.Wait()
	finished, err := database.GetTask(context.Background(), task.ID)
	if err != nil || finished.State != model.TaskSucceeded || len(engine.executor.(*automaticTestExecutor).calls) != 0 {
		t.Fatal("B-P启动了自动观察回滚", err)
	}
}

func TestBPAutomaticObservationDoesNotWriteTerminalEvenWithFaults(t *testing.T) {
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
	if err := engine.ReconcileAutoUpdateObservations(context.Background()); err == nil {
		t.Fatal(err)
	}
	finished, err := database.GetTask(context.Background(), task.ID)
	if err != nil || finished.State != model.TaskSucceeded || len(executor.calls) != before {
		t.Fatalf("回执收口失败或重放了回滚: %+v error=%v", finished, err)
	}
}

func TestBPAutomaticObservationDoesNotClaimWhileBackupLocked(t *testing.T) {
	engine, database, plan, task := observingAutomaticPlan(t)
	if !engine.acquire([]string{"backup:global"}, "another-service") {
		t.Fatal("无法设置并发夹具")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := engine.reconcileAutoUpdateObservations(ctx, true); err == nil {
		engine.release([]string{"backup:global"}, "another-service")
		t.Fatal(err)
	}
	queued, err := database.GetReleasePlan(ctx, plan.ID)
	if err != nil || queued.State != model.PlanObserving {
		engine.release([]string{"backup:global"}, "another-service")
		t.Fatalf("等待其他服务时没有及时固定回滚请求: %+v error=%v", queued, err)
	}
	engine.release([]string{"backup:global"}, "another-service")
	engine.Wait()
	finished, err := database.GetTask(ctx, task.ID)
	if err != nil || finished.State != model.TaskSucceeded {
		t.Fatalf("等待锁后回滚未完成: %+v error=%v", finished, err)
	}
}

func TestAutomaticRollbackReceiptSurvivesRunnerRestart(t *testing.T) {
	engine, database, plan, task := observingAutomaticPlan(t)
	raw := historicalRunnerDB(t, engine)
	if _, err := raw.Exec("UPDATE tasks SET state=? WHERE id=?", model.TaskRollingBack, task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec("UPDATE release_plans SET state=? WHERE id=?", model.PlanExecuting, plan.ID); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(engine.stateRoot, "operations", task.ID)
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	receipt := automaticRollbackReceipt{Version: 1, TaskID: task.ID, PlanID: plan.ID, PlanDigest: plan.Digest, State: model.TaskRolledBack, Summary: "历史回执", CompletedAt: time.Now().UTC()}
	before, _ := json.Marshal(receipt)
	file := filepath.Join(directory, automaticRollbackReceiptName)
	if err := os.WriteFile(file, before, 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(engine.stateRoot, "ops.db")
	raw.Close()
	database.Close()
	reopened, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	count, err := RecoverAutomaticRollbackReceipts(context.Background(), engine.catalog, reopened, engine.stateRoot, engine.alertmanager)
	if err != nil || count != 0 {
		t.Fatal("重启接管旧回执", err)
	}
	if _, err = reopened.RecoverInterrupted(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(file)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("历史回执改写", err)
	}
	current, err := reopened.GetTask(context.Background(), task.ID)
	if err != nil || current.State != model.TaskRollingBack {
		t.Fatal("B-P伪造旧任务终态", err)
	}
	if len(engine.alertmanager.(*fakeAlertmanager).expired) != 0 {
		t.Fatal("接管静默清理")
	}
}
