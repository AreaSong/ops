package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

func (engine *Engine) run(task model.Task) {
	service, action, err := engine.resolveAction(task.Service, task.Action, task.Target)
	if err != nil {
		engine.failBeforeRun(task, err)
		return
	}
	resources := lockResources(task.Service, task.Action)
	if !engine.acquire(resources, task.ID) {
		engine.failBeforeRun(task, fmt.Errorf("任务所需资源正被其他操作占用"))
		return
	}
	defer engine.release(resources, task.ID)
	operationDir := filepath.Join(engine.stateRoot, "operations", task.ID)
	if err := os.MkdirAll(operationDir, 0o700); err != nil {
		engine.failBeforeRun(task, err)
		return
	}
	if err := os.Chmod(operationDir, 0o700); err != nil {
		engine.failBeforeRun(task, err)
		return
	}
	if err := writeTaskContract(operationDir, task); err != nil {
		engine.failBeforeRun(task, err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(action.TimeoutSeconds)*time.Second)
	defer cancel()
	if err := engine.store.MarkRunningOwned(ctx, task.ID, action.Steps[0], engine.owner); err != nil {
		engine.failBeforeRun(task, err)
		return
	}
	heartbeatDone := make(chan struct{})
	defer close(heartbeatDone)
	go engine.heartbeat(task.ID, heartbeatDone)
	engine.event(ctx, model.Event{TaskID: task.ID, Level: "info", Phase: action.Steps[0], Message: "任务开始执行"})
	productionChanged := false
	recoveryPointReady := false
	lastSummary := ""
	for _, phase := range action.Steps {
		semantics := phaseSemantics(action, phase)
		if semantics.RequiresRecoveryPoint && !recoveryPointReady {
			engine.finishFailure(task, service, operationDir, productionChanged, false, lastSummary,
				errors.New("变更阶段缺少已验证恢复点，已拒绝执行"))
			return
		}
		if err := engine.store.SetPhase(ctx, task.ID, phase, lastSummary); err != nil {
			engine.finishFailure(task, service, operationDir, productionChanged, false, lastSummary, err)
			return
		}
		result, err := engine.executor.Execute(ctx, ExecuteInput{
			Service: service, Action: action.Name, Phase: phase,
			OperationDir: operationDir, Target: task.Target,
			SourceDir: engine.sourceDir(task),
		})
		if err != nil {
			engine.finishFailure(task, service, operationDir, productionChanged, mutationSemantics(semantics), lastSummary, err)
			return
		}
		if semantics.ProducesRecoveryPoint {
			point, err := engine.persistRecoveryPoint(context.Background(), task, result.RecoveryPoint)
			if err != nil {
				engine.finishFailure(task, service, operationDir, productionChanged, false, lastSummary, err)
				return
			}
			recoveryPointReady = true
			if result.Data == nil {
				result.Data = make(map[string]any)
			}
			result.Data["recoveryPointId"] = point.ID
			result.Data["recoveryPointDigest"] = point.EvidenceDigest
		}
		changed, explicit := result.Data["productionChanged"].(bool)
		if changed || (!explicit && mutationSemantics(semantics)) {
			productionChanged = true
			rollbackAvailable := task.Action == "update"
			_ = engine.store.MarkProductionChanged(context.Background(), task.ID, rollbackAvailable,
				"仅回滚应用版本和 Compose，不自动恢复业务数据库")
		}
		lastSummary = result.Summary
		engine.event(ctx, model.Event{
			TaskID: task.ID, Level: "info", Phase: phase,
			Message: result.Summary, Data: result.Data,
		})
	}
	engine.completeTask(task, model.TaskSucceeded, lastSummary, "", "", false, false, "")
}

func (engine *Engine) heartbeat(taskID string, done <-chan struct{}) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = engine.store.Heartbeat(ctx, taskID, engine.owner)
			cancel()
		}
	}
}

func writeTaskContract(operationDir string, task model.Task) error {
	contract := map[string]any{
		"schemaVersion":  1,
		"taskId":         task.ID,
		"actorHash":      task.ActorHash,
		"service":        task.Service,
		"action":         task.Action,
		"target":         task.Target,
		"expectedBefore": task.Snapshot,
		"createdAt":      task.CreatedAt,
	}
	data, err := json.Marshal(contract)
	if err != nil {
		return err
	}
	path := filepath.Join(operationDir, "task-contract.json")
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

func (engine *Engine) finishFailure(
	task model.Task,
	service model.ServiceDefinition,
	operationDir string,
	productionChanged bool,
	mutationUncertain bool,
	lastSummary string,
	failure error,
) {
	message := redactText(failure.Error())
	engine.event(context.Background(), model.Event{TaskID: task.ID, Level: "error", Phase: "failed", Message: message})
	state := model.TaskFailedRecoverable
	retryable := !productionChanged && !mutationUncertain
	rollbackAvailable := false
	rollbackReason := ""
	if productionChanged || mutationUncertain {
		_ = engine.store.MarkProductionChanged(context.Background(), task.ID, task.Action == "update",
			"变更阶段失败，生产是否已改变需按已改变处理")
	}
	if (productionChanged || mutationUncertain) && task.Action == "update" {
		if err := engine.store.StartRollback(context.Background(), task.ID, message); err != nil {
			message += "; 无法进入回滚状态: " + redactText(err.Error())
			engine.completeTask(task, model.TaskNeedsAttention, lastSummary, message,
				"rollback_transition_failed", false, true, "需人工核对更新产物")
			return
		}
		rollbackCtx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		result, err := engine.executor.Execute(rollbackCtx, ExecuteInput{
			Service: service, Action: "update", Phase: "rollback",
			OperationDir: operationDir, Target: task.Target,
		})
		if err == nil {
			state = model.TaskRolledBack
			lastSummary = result.Summary
			engine.event(rollbackCtx, model.Event{TaskID: task.ID, Level: "warning", Phase: "rollback", Message: result.Summary, Data: result.Data})
		} else {
			state = model.TaskNeedsAttention
			message += "; 回滚失败: " + redactText(err.Error())
			rollbackAvailable = false
			rollbackReason = "自动回滚失败，禁止自动重试，必须人工核对"
		}
	} else if productionChanged || mutationUncertain {
		state = model.TaskNeedsAttention
		retryable = false
		rollbackReason = "生产可能已改变，必须人工核对"
	}
	if lastSummary == "" {
		lastSummary = "任务执行失败"
	}
	engine.completeTask(task, state, lastSummary, message, "adapter_phase_failed",
		retryable, rollbackAvailable, rollbackReason)
}

func (engine *Engine) failBeforeRun(task model.Task, err error) {
	message := redactText(err.Error())
	engine.completeTask(task, model.TaskFailedRecoverable, "任务未开始", message,
		"preflight_failed", true, false, "")
}

func (engine *Engine) sourceDir(task model.Task) string {
	if task.Action != "rollback" || !uuidPattern.MatchString(task.Target) {
		return ""
	}
	return filepath.Join(engine.stateRoot, "operations", task.Target)
}

func lockResources(service, action string) []string {
	resources := []string{"service:" + service}
	if action == "backup" || action == "update" || action == "rollback" || action == "restore-drill" {
		resources = append(resources, "backup:global")
	}
	sort.Strings(resources)
	return resources
}

func (engine *Engine) acquire(resources []string, taskID string) bool {
	engine.lockMu.Lock()
	defer engine.lockMu.Unlock()
	for _, resource := range resources {
		if _, held := engine.locks[resource]; held {
			return false
		}
	}
	for _, resource := range resources {
		engine.locks[resource] = taskID
	}
	return true
}

func (engine *Engine) release(resources []string, taskID string) {
	engine.lockMu.Lock()
	defer engine.lockMu.Unlock()
	for _, resource := range resources {
		if engine.locks[resource] == taskID {
			delete(engine.locks, resource)
		}
	}
}

func (engine *Engine) event(ctx context.Context, event model.Event) {
	persisted, err := engine.store.AppendEvent(ctx, event)
	if err == nil {
		engine.broker.Publish(persisted.Sequence)
	} else {
		slog.Error("任务事件持久化失败", "task", event.TaskID, "phase", event.Phase, "error", err)
	}
}

func (engine *Engine) completeTask(
	task model.Task,
	state model.TaskState,
	summary, message, failureCode string,
	retryable, rollbackAvailable bool,
	rollbackReason string,
) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	level := "info"
	if state != model.TaskSucceeded {
		level = "error"
	}
	event, err := engine.store.CompleteTask(ctx, task.ID, state, summary, message, failureCode,
		retryable, rollbackAvailable, rollbackReason,
		model.Event{TaskID: task.ID, Level: level, Phase: "terminal", Message: string(state),
			Data: map[string]any{"state": state}},
		model.AuditEntry{ActorHash: task.ActorHash, Event: "task.terminal", Resource: task.ID,
			Outcome: string(state), Detail: map[string]any{"service": task.Service, "action": task.Action, "error": message}},
	)
	if err == nil {
		engine.broker.Publish(event.Sequence)
		if state != model.TaskSucceeded && task.PlanID != "" {
			plan, planErr := engine.store.GetReleasePlan(context.Background(), task.PlanID)
			if planErr == nil {
				releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				planErr = engine.releasePlanSilence(releaseCtx, task.ActorHash, &plan)
				cancel()
			}
			if planErr != nil {
				slog.Error("失败计划维护静默解除失败", "plan", task.PlanID, "error", planErr)
			}
		}
	} else {
		slog.Error("任务终态事务提交失败", "task", task.ID, "state", state, "error", err)
	}
}

func (engine *Engine) auditTerminal(task model.Task, state model.TaskState, message string) {
	_, _ = engine.store.AppendAudit(context.Background(), model.AuditEntry{
		ActorHash: task.ActorHash, Event: "task.terminal", Resource: task.ID,
		Outcome: string(state), Detail: map[string]any{"service": task.Service, "action": task.Action, "error": message},
	})
}
