package runner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	"github.com/AreaSong/ops/services/areasong-ops/internal/store"
)

func (engine *Engine) captureAutomaticObservation(ctx context.Context, task model.Task, service model.ServiceDefinition) error {
	if task.PlanID == "" {
		return nil
	}
	plan, err := engine.store.GetReleasePlan(ctx, task.PlanID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil || plan.ApprovalSummary.AutoUpdatePolicy == nil {
		return err
	}
	if plan.Digest != task.PlanDigest || plan.Action != "update" {
		return errors.New("自动更新观察基线的任务身份不一致")
	}
	observed, err := engine.inspectForAction(ctx, service, task.Action)
	if err != nil {
		return err
	}
	if err := engine.verifyClosureIdentity(ctx, plan, observed); err != nil {
		return err
	}
	if !hasRuntimeIdentity(observed) {
		return errors.New("自动更新缺少完整的更新后运行身份")
	}
	identity, err := engine.inspectRollbackIdentity(ctx, service)
	if err != nil {
		return err
	}
	for _, key := range []string{"currentVersion", "currentImage", "currentImageId"} {
		if observed[key] != identity[key] {
			return errors.New("应用健康身份与独立运行身份不一致")
		}
	}
	baseline := map[string]any{"planDigest": plan.Digest}
	for _, key := range []string{"currentVersion", "currentImage", "currentImageId", "runtimeIdentityHash"} {
		baseline[key] = identity[key]
	}
	_, err = engine.store.AppendEvent(ctx, model.Event{TaskID: task.ID, Level: "info", Phase: store.AutomaticObservationPhase,
		Message: "自动更新观察基线已固定", Data: baseline})
	return err
}

// 独立于 GET 请求运行；只消费已批准、已执行且仍在观察窗口内的计划。
func (engine *Engine) StartAutoUpdateObservationMonitor(ctx context.Context) {
	engine.startAutoUpdateObservationMonitor(ctx, 5*time.Second)
}

func (engine *Engine) startAutoUpdateObservationMonitor(ctx context.Context, interval time.Duration) {
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(engine.fleetUpdateCtx, cancel)
	engine.wait.Add(1)
	go func() {
		defer engine.wait.Done()
		defer cancel()
		defer stop()
		engine.monitorAutoUpdateObservations(ctx, interval)
	}()
}

func (engine *Engine) monitorAutoUpdateObservations(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := engine.reconcileAutoUpdateObservations(ctx, true); err != nil {
				slog.Error("自动更新观察检查失败", "error", err)
			}
		}
	}
}

func (engine *Engine) ReconcileAutoUpdateObservations(ctx context.Context) error {
	return engine.reconcileAutoUpdateObservations(ctx, false)
}

func (engine *Engine) reconcileAutoUpdateObservations(ctx context.Context, asynchronous bool) error {
	if err := engine.reconcileAutomaticRollbackReceipts(ctx); err != nil {
		return err
	}
	plans, err := engine.store.AutomaticObservationPlans(ctx, time.Now().UTC())
	if err != nil {
		return err
	}
	for _, plan := range plans {
		service, exists := engine.catalog.Object(plan.Service)
		if !exists {
			continue
		}
		alerts, err := engine.blockingAlerts(ctx, service)
		if err != nil {
			return fmt.Errorf("无法核验观察期告警: %w", err)
		}
		if len(alerts) == 0 {
			continue
		}
		if err := engine.rollbackAutomaticObservation(ctx, plan, service, "观察期出现阻断告警: "+alertNames(alerts), asynchronous); err != nil {
			return err
		}
	}
	return nil
}

func (engine *Engine) rollbackAutomaticObservation(ctx context.Context, plan model.ReleasePlan, service model.ServiceDefinition, reason string, asynchronous bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	resources := []string{"service:" + plan.Service}
	if !engine.acquire(resources, plan.TaskID) {
		return nil
	}
	phase, err := engine.verifyAutomaticRollback(ctx, plan, service)
	if err != nil {
		engine.release(resources, plan.TaskID)
		return engine.store.BlockAutomaticObservation(ctx, plan, "自动回滚已阻止: "+redactText(err.Error()))
	}
	task, started, err := engine.store.StartAutomaticRollback(ctx, store.AutomaticRollbackInput{
		PlanID: plan.ID, Digest: plan.Digest, Phase: phase, Owner: engine.owner, Reason: reason,
	})
	if err != nil {
		engine.release(resources, plan.TaskID)
		return engine.store.BlockAutomaticObservation(ctx, plan, "自动回滚启动已阻止: "+redactText(err.Error()))
	}
	if !started {
		engine.release(resources, plan.TaskID)
		return nil
	}
	run := func() error {
		defer engine.release(resources, plan.TaskID)
		return engine.runAutomaticRollback(ctx, plan, task, service, phase, reason)
	}
	if !asynchronous {
		return run()
	}
	engine.wait.Add(1)
	go func() {
		defer engine.wait.Done()
		if err := run(); err != nil {
			slog.Error("自动更新回滚未完成", "plan", plan.ID, "error", err)
		}
	}()
	return nil
}

func (engine *Engine) runAutomaticRollback(ctx context.Context, plan model.ReleasePlan, task model.Task, service model.ServiceDefinition, phase, reason string) error {
	heartbeatDone := make(chan struct{})
	defer close(heartbeatDone)
	go engine.heartbeat(task.ID, heartbeatDone)
	rollbackCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	if err := engine.acquireAutomaticRollbackBackupLock(rollbackCtx, task.ID); err != nil {
		return errors.Join(err, engine.finishAutomaticRollback(task, model.TaskNeedsAttention, "观察期回滚未开始", err.Error(),
			"auto_update_rollback_cancelled"))
	}
	defer engine.release([]string{"backup:global"}, task.ID)
	operationDir := filepath.Join(engine.stateRoot, "operations", task.ID)
	result, runErr := engine.executePhase(rollbackCtx, service, task.Action, phase, operationDir, task.Target, "")
	if runErr == nil {
		observed, inspectErr := engine.inspectForAction(rollbackCtx, service, "rollback")
		if inspectErr != nil || !sameRuntimeIdentity(plan.ApprovalSummary.ExpectedBefore, observed) {
			runErr = errors.New("回滚后的运行身份未恢复，需人工核对")
		}
	}
	if runErr != nil {
		finishErr := engine.finishAutomaticRollback(task, model.TaskNeedsAttention, "观察期回滚失败", redactText(runErr.Error()),
			"auto_update_rollback_failed")
		return errors.Join(runErr, finishErr)
	}
	return engine.finishAutomaticRollback(task, model.TaskRolledBack, result.Summary, reason, "auto_update_alert_rollback")
}

func (engine *Engine) acquireAutomaticRollbackBackupLock(ctx context.Context, taskID string) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if engine.acquire([]string{"backup:global"}, taskID) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (engine *Engine) inspectRollbackIdentity(ctx context.Context, service model.ServiceDefinition) (map[string]any, error) {
	directory, err := os.MkdirTemp(engine.stateRoot, ".rollback-identity-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(directory)
	result, err := engine.executePhase(ctx, service, "inspect", "lifecycle", directory, "", "")
	if err != nil {
		return nil, err
	}
	if !hasRuntimeIdentity(result.Data) {
		return nil, errors.New("独立运行身份探针没有返回完整身份")
	}
	return result.Data, nil
}

func (engine *Engine) verifyAutomaticRollback(ctx context.Context, plan model.ReleasePlan, service model.ServiceDefinition) (string, error) {
	if engine.remoteDispatch {
		return "", errors.New("远程任务的观察期回滚需要独立恢复计划")
	}
	if err := engine.authorize(ctx, plan.ActorHash, model.PermissionDeploy, service.ObjectID); err != nil {
		return "", err
	}
	_, action, err := engine.resolveAction(plan.Service, plan.Action, plan.Target)
	if err != nil {
		return "", err
	}
	if err := validateAutomaticActionContract(service, action); err != nil {
		return "", err
	}
	phase := ""
	for _, step := range plan.ApprovalSummary.Steps {
		approved := plan.ApprovalSummary.PhaseSemantics[step]
		if approved != model.EffectivePhaseSemantics(action, step) {
			return "", errors.New("回滚动作合同已变化")
		}
		if approved.FailurePolicy == "rollback" {
			phase = approved.RecoveryPhase
		}
	}
	task, err := engine.store.GetTask(ctx, plan.TaskID)
	if err != nil {
		return "", err
	}
	if err := engine.verifyRecoveryPoint(ctx, task, service, task.RecoveryPointID); err != nil {
		return "", err
	}
	baseline, err := engine.store.AutomaticObservationBaseline(ctx, task.ID)
	if err != nil || baseline["planDigest"] != plan.Digest {
		return "", errors.New("更新后观察基线缺失或不匹配")
	}
	observed, err := engine.inspectRollbackIdentity(ctx, service)
	if err != nil || !sameRuntimeIdentity(baseline, observed) {
		return "", errors.New("当前运行身份已偏离更新后基线")
	}
	return phase, nil
}
