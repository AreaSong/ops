package runner

import (
	"context"
	"errors"
	"fmt"
	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	"github.com/AreaSong/ops/services/areasong-ops/internal/store"
	"os"
	"path/filepath"
	"time"
)

// executeAdapterPhase只能通过此包装调用可信能力，每次调用核验关联并等待停止证明。
type registeredExecutor struct {
	engine *Engine
	lease  *releaseExecutionLease
}

func (r registeredExecutor) Execute(ctx context.Context, input ExecuteInput) (model.AdapterResult, error) {
	l := r.lease
	if l.uncertain || r.engine.releaseLease(l.taskID) != l {
		return model.AdapterResult{}, store.ErrWorkAdmissionConflict
	}
	if _, err := r.engine.store.CheckRegisteredTask(ctx, l.mutation()); err != nil {
		return model.AdapterResult{}, err
	}
	_, _, profile, err := r.engine.verifyExecutionProfile(ctx, l.plan, releaseScopeInput{target: l.plan.Target, call: l.callContext, phase: input.Phase})
	if err != nil || profile != l.executor {
		return model.AdapterResult{}, errors.Join(model.ErrReleaseLifecycle, err)
	}
	input.releaseContext = l.callContext
	call := l.executor.executeReleaseCall(ctx, input)
	if call.Settled == nil {
		l.uncertain = true
		return model.AdapterResult{}, errors.New("执行调用缺少收敛证明")
	}
	select {
	case <-call.Settled:
	case <-ctx.Done():
		l.uncertain = true
		return model.AdapterResult{}, errors.New("执行调用仍可能在途")
	}
	if !model.ValidWorkDigest(call.EvidenceDigest) {
		l.uncertain = true
		return model.AdapterResult{}, model.ErrReleaseLifecycle
	}
	l.calls = append(l.calls, call.EvidenceDigest)
	if call.Err != nil {
		return call.Result, call.Err
	}
	if !call.Result.OK {
		return call.Result, errors.New("执行器返回失败")
	}
	return call.Result, nil
}

func (engine *Engine) runRegistered(task model.Task, l *releaseExecutionLease) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.running || l.uncertain || engine.remoteDispatch || engine.releaseLease(task.ID) != l || task.ID != l.taskID {
		return
	}
	stored, err := engine.store.CheckRegisteredTask(context.Background(), l.mutation())
	if err != nil || stored.State != model.TaskQueued {
		return
	}
	service, action, profile, err := engine.verifyExecutionProfile(context.Background(), l.plan, l.scopeInput())
	if err != nil || profile != l.executor || len(action.Steps) == 0 {
		return
	}
	task = stored
	task.TrafficPolicyDigest = l.plan.ApprovalSummary.TrafficPolicyDigest
	authority, err := engine.releaseAuthority(context.Background(), task.ActorHash, service.ObjectID)
	if err != nil {
		return
	}
	if err = engine.store.MarkRegisteredRunningOwned(context.Background(), l.mutation(), action.Steps[0], engine.owner, authority); err != nil {
		return
	}
	l.running = true
	resources := lockResources(task.Service, task.Action)
	if !engine.acquire(resources, task.ID) {
		l.uncertain = true
		return
	}
	done, exited := make(chan struct{}), make(chan struct{})
	go func() { defer close(exited); engine.heartbeat(task.ID, done) }()
	directory := filepath.Join(engine.stateRoot, "operations", task.ID)
	err = os.MkdirAll(directory, 0700)
	if err == nil {
		err = os.Chmod(directory, 0700)
	}
	if err == nil {
		err = writeTaskContract(directory, task)
	}
	state, summary, failure := model.TaskFailedRecoverable, "任务未开始", err
	if err == nil {
		state, summary, failure = engine.performRegistered(task, l, service, action, directory)
	}
	// 心跳确认退出及锁释放先于finalizer；调用不确定时仍保持登记阻断。
	close(done)
	<-exited
	l.heartbeatStopped = true
	engine.release(resources, task.ID)
	l.locksReleased = true
	if l.uncertain {
		state = model.TaskNeedsAttention
	}
	message := ""
	if failure != nil {
		message = redactText(failure.Error())
	}
	completed := task
	completed.State = state
	event, err := engine.store.CompleteRegisteredTask(context.Background(), store.RegisteredTaskCompletion{Mutation: l.mutation(), State: state, Summary: summary, Error: message, FailureCode: failureCode(failure), Retryable: state == model.TaskFailedRecoverable, Desired: engine.desiredStateInputForTask(completed)})
	if err != nil {
		l.uncertain = true
		return
	}
	engine.broker.Publish(event.Sequence)
	if l.uncertain {
		_ = engine.uncertainRelease(l, errors.New("执行未收敛"))
		return
	}
	l.executionSettled = true
	plan, err := engine.store.GetReleasePlan(context.Background(), l.plan.ID)
	if err != nil {
		l.uncertain = true
		return
	}
	if state == model.TaskSucceeded && plan.ObservationSeconds > 0 {
		return
	}
	key, err := newUUID()
	if err != nil {
		l.uncertain = true
		return
	}
	if state == model.TaskSucceeded {
		_, _ = engine.finishClosureAttempt(context.Background(), plan.ActorHash, plan, key, l)
	} else {
		_, err = engine.settleFailedRelease(context.Background(), plan, l)
		if err != nil {
			l.uncertain = true
		}
	}
}
func failureCode(err error) string {
	if err != nil {
		return "adapter_phase_failed"
	}
	return ""
}
func (engine *Engine) performRegistered(task model.Task, l *releaseExecutionLease, service model.ServiceDefinition, action model.ActionDefinition, directory string) (model.TaskState, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(action.TimeoutSeconds)*time.Second)
	defer cancel()
	executor := registeredExecutor{engine, l}
	changed := false
	summary := ""
	recoveryID := ""
	for _, phase := range action.Steps {
		semantics := phaseSemantics(action, phase)
		failureSemantics := model.EffectiveFailureSemantics(action, phase, changed)
		if semantics.RequiresRecoveryPoint {
			if err := engine.verifyRecoveryPoint(ctx, task, service, recoveryID); err != nil {
				return engine.failRegistered(task, l, service, action, failureSemantics, directory, changed, summary, err)
			}
		}
		if err := engine.store.SetPhase(ctx, task.ID, phase, summary); err != nil {
			return model.TaskNeedsAttention, summary, err
		}
		result, err := executeAdapterPhase(ctx, executor, service, action.Name, phase, directory, task.Target, "")
		if err != nil {
			return engine.failRegistered(task, l, service, action, failureSemantics, directory, changed || mutationSemantics(semantics), summary, err)
		}
		if semantics.ProducesRecoveryPoint {
			point, e := engine.persistRecoveryPoint(ctx, task, service, result.RecoveryPoint)
			if e != nil {
				return engine.failRegistered(task, l, service, action, failureSemantics, directory, changed, summary, e)
			}
			recoveryID = point.ID
		}
		mutation, _ := result.Data["productionChanged"].(bool)
		if mutation || mutationSemantics(semantics) {
			changed = true
			if err = engine.store.MarkProductionChanged(ctx, task.ID, semantics.FailurePolicy == "rollback", "仅回滚应用产物"); err != nil {
				return model.TaskNeedsAttention, summary, err
			}
		}
		summary = result.Summary
		event, e := engine.store.AppendEvent(ctx, model.Event{TaskID: task.ID, Level: "info", Phase: phase, Message: summary, Data: result.Data})
		if e != nil {
			return model.TaskNeedsAttention, summary, e
		}
		engine.broker.Publish(event.Sequence)
	}
	return model.TaskSucceeded, summary, nil
}
func (engine *Engine) failRegistered(task model.Task, l *releaseExecutionLease, service model.ServiceDefinition, action model.ActionDefinition, semantics model.PhaseSemantics, directory string, changed bool, summary string, cause error) (model.TaskState, string, error) {
	if l.uncertain {
		return model.TaskNeedsAttention, summary, cause
	}
	if !changed && semantics.FailurePolicy != "needs_attention" {
		return model.TaskFailedRecoverable, summary, cause
	}
	if semantics.FailurePolicy != "rollback" || semantics.RecoveryPhase == "" {
		return model.TaskNeedsAttention, summary, cause
	}
	if err := engine.store.StartRecovery(context.Background(), task.ID, semantics.RecoveryPhase, redactText(cause.Error())); err != nil {
		return model.TaskNeedsAttention, summary, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(action.TimeoutSeconds)*time.Second)
	defer cancel()
	result, err := executeAdapterPhase(ctx, registeredExecutor{engine, l}, service, action.Name, semantics.RecoveryPhase, directory, task.Target, "")
	if err != nil {
		return model.TaskNeedsAttention, summary, fmt.Errorf("%w; 回滚未完成: %v", cause, err)
	}
	return model.TaskRolledBack, result.Summary, cause
}
