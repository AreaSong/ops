package runner

import (
	"context"
	"encoding/json"
	"fmt"
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
	if err := engine.store.MarkRunning(ctx, task.ID, action.Steps[0]); err != nil {
		engine.failBeforeRun(task, err)
		return
	}
	engine.event(ctx, model.Event{TaskID: task.ID, Level: "info", Phase: action.Steps[0], Message: "任务开始执行"})
	mutated := false
	lastSummary := ""
	for _, phase := range action.Steps {
		if phase == "apply" || phase == "restart" {
			mutated = true
		}
		if err := engine.store.SetPhase(ctx, task.ID, phase, lastSummary); err != nil {
			engine.finishFailure(task, service, operationDir, mutated, lastSummary, err)
			return
		}
		result, err := engine.executor.Execute(ctx, ExecuteInput{
			Service: service, Action: action.Name, Phase: phase,
			OperationDir: operationDir, Target: task.Target,
			SourceDir: engine.sourceDir(task),
		})
		if err != nil {
			engine.finishFailure(task, service, operationDir, mutated, lastSummary, err)
			return
		}
		lastSummary = result.Summary
		engine.event(ctx, model.Event{
			TaskID: task.ID, Level: "info", Phase: phase,
			Message: result.Summary, Data: result.Data,
		})
	}
	if err := engine.store.FinishTask(ctx, task.ID, model.TaskSucceeded, lastSummary, ""); err != nil {
		engine.event(context.Background(), model.Event{TaskID: task.ID, Level: "error", Phase: "terminal", Message: redactText(err.Error())})
		return
	}
	engine.event(context.Background(), model.Event{TaskID: task.ID, Level: "info", Phase: "terminal", Message: "任务执行成功"})
	engine.auditTerminal(task, model.TaskSucceeded, "")
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
	mutated bool,
	lastSummary string,
	failure error,
) {
	message := redactText(failure.Error())
	engine.event(context.Background(), model.Event{TaskID: task.ID, Level: "error", Phase: "failed", Message: message})
	state := model.TaskFailed
	if mutated && task.Action == "update" {
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
			state = model.TaskRecoveryUncertain
			message += "; 回滚失败: " + redactText(err.Error())
		}
	} else if mutated {
		state = model.TaskRecoveryUncertain
	}
	if lastSummary == "" {
		lastSummary = "任务执行失败"
	}
	_ = engine.store.FinishTask(context.Background(), task.ID, state, lastSummary, message)
	engine.event(context.Background(), model.Event{TaskID: task.ID, Level: "error", Phase: "terminal", Message: string(state)})
	engine.auditTerminal(task, state, message)
}

func (engine *Engine) failBeforeRun(task model.Task, err error) {
	message := redactText(err.Error())
	_ = engine.store.FinishTask(context.Background(), task.ID, model.TaskFailed, "任务未开始", message)
	engine.event(context.Background(), model.Event{TaskID: task.ID, Level: "error", Phase: "terminal", Message: message})
	engine.auditTerminal(task, model.TaskFailed, message)
}

func (engine *Engine) sourceDir(task model.Task) string {
	if task.Action != "rollback" || !uuidPattern.MatchString(task.Target) {
		return ""
	}
	return filepath.Join(engine.stateRoot, "operations", task.Target)
}

func lockResources(service, action string) []string {
	resources := []string{"service:" + service}
	if action == "backup" || action == "update" || action == "restore-drill" {
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
	}
}

func (engine *Engine) auditTerminal(task model.Task, state model.TaskState, message string) {
	_, _ = engine.store.AppendAudit(context.Background(), model.AuditEntry{
		ActorHash: task.ActorHash, Event: "task.terminal", Resource: task.ID,
		Outcome: string(state), Detail: map[string]any{"service": task.Service, "action": task.Action, "error": message},
	})
}
