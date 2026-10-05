package runner

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	"github.com/AreaSong/ops/services/areasong-ops/internal/store"
	"sync"
	"time"
)

// 私有能力必须由实现证明执行、回滚和所有后继工作收敛；检查能力不能替代它。
// 生产实现允许集合仍为空；不提供HTTP、配置或CommandExecutor自报入口。
type releaseExecutor interface {
	planInspectionExecutor
	resolveReleaseExecutionScope(context.Context, model.ServiceDefinition, model.ActionDefinition, releaseScopeInput) (model.ReleaseScopeDefinition, error)
	executeReleaseCall(context.Context, ExecuteInput) inspectionCall
}

type closureAttempt struct {
	plan         model.ReleasePlan
	err          error
	retryAllowed bool
}
type releaseExecutionLease struct {
	mu                                                                    sync.Mutex
	callContext                                                           *releaseCallContext
	record                                                                model.WorkAdmission
	owner, taskID, scopeDigest                                            string
	plan                                                                  model.ReleasePlan
	executor                                                              releaseExecutor
	running, executionSettled, heartbeatStopped, locksReleased, uncertain bool
	calls, cleanup                                                        []string
	attempts                                                              map[string]closureAttempt
	blockedAttempt                                                        string
	silenceDeleteConfirmed                                                bool
}

func workMutation(r model.WorkAdmission, owner string) store.WorkAdmissionMutation {
	return store.WorkAdmissionMutation{ID: r.ID, Kind: r.Request.Kind, WorkID: r.Request.WorkID, IdempotencyKey: r.Request.IdempotencyKey, ActorHash: r.Request.ActorHash, ApprovalDigest: r.Request.ApprovalDigest, RequestDigest: r.RequestDigest, OwnerToken: owner, ExpectedState: r.State, ExpectedRevision: r.Revision, TaskID: r.TaskID}
}
func (l *releaseExecutionLease) mutation() store.WorkAdmissionMutation {
	return workMutation(l.record, l.owner)
}
func (engine *Engine) releaseTime() time.Time {
	if engine.releaseNow != nil {
		return engine.releaseNow()
	}
	return time.Now().UTC()
}
func (engine *Engine) releaseLease(taskID string) *releaseExecutionLease {
	engine.releaseMu.Lock()
	defer engine.releaseMu.Unlock()
	return engine.releases[taskID]
}
func (engine *Engine) insertReleaseLease(l *releaseExecutionLease) error {
	engine.releaseMu.Lock()
	defer engine.releaseMu.Unlock()
	if engine.releases == nil {
		engine.releases = make(map[string]*releaseExecutionLease)
	}
	if prior := engine.releases[l.taskID]; prior != nil && prior != l {
		return store.ErrWorkAdmissionConflict
	}
	engine.releases[l.taskID] = l
	return nil
}
func (engine *Engine) verifyExecutionProfile(ctx context.Context, plan model.ReleasePlan, inputs ...releaseScopeInput) (model.ServiceDefinition, model.ActionDefinition, releaseExecutor, error) {
	input := scopeInput(inputs)
	input.target = plan.Target
	service, action, err := engine.resolveAction(plan.Service, plan.Action, plan.Target)
	if err != nil {
		return service, action, nil, err
	}
	profile, ok := engine.executor.(releaseExecutor)
	if !ok || engine.remoteDispatch || plan.Action != "update" || plan.ApprovalSummary.SchemaVersion != 2 || plan.ApprovalSummary.Lifecycle == nil {
		return service, action, nil, model.ErrReleaseNotIntegrated
	}
	if err = engine.verifyReleaseScope(ctx, service, action, *plan.ApprovalSummary.Lifecycle, input); err != nil {
		return service, action, nil, err
	}
	actual, err := profile.resolveReleaseExecutionScope(ctx, service, action, input)
	if err != nil {
		return service, action, nil, err
	}
	a, e := model.CanonicalReleaseScope(actual)
	b, f := model.CanonicalReleaseScope(*service.ReleaseScope)
	if e != nil || f != nil || a != b {
		return service, action, nil, model.ErrReleaseLifecycle
	}
	return service, action, profile, nil
}
func (engine *Engine) executeRegisteredPlan(ctx context.Context, actor string, plan model.ReleasePlan, request model.ExecutePlanRequest) (model.Task, bool, error) {
	service, action, profile, err := engine.verifyExecutionProfile(ctx, plan)
	if err != nil {
		return model.Task{}, false, err
	}
	authority, err := engine.releaseAuthority(ctx, actor, service.ObjectID)
	if err != nil {
		return model.Task{}, false, err
	}
	if plan.ScheduleAt != nil && engine.releaseTime().Before(*plan.ScheduleAt) {
		return model.Task{}, false, errors.New("发布计划尚未到达调度时间")
	}
	targets, err := plan.ApprovalSummary.Lifecycle.WorkTargets()
	if err != nil {
		return model.Task{}, false, err
	}
	id, err := newUUID()
	if err != nil {
		return model.Task{}, false, err
	}
	taskID, err := newUUID()
	if err != nil {
		return model.Task{}, false, err
	}
	owner, err := model.NewWorkOwnerToken()
	if err != nil {
		return model.Task{}, false, err
	}
	record, created, err := engine.store.AdmitReleaseWork(ctx, store.ReleaseWorkInput{Authority: authority, Admission: store.WorkAdmissionInput{AdmissionID: id, OwnerToken: owner, Request: model.WorkAdmissionRequest{Version: 1, Kind: model.WorkAdmissionKind, WorkID: plan.ID, IdempotencyKey: request.IdempotencyKey, ActorHash: actor, ApprovalDigest: plan.Digest, Targets: targets}}})
	if err != nil || !created {
		return model.Task{}, false, errors.Join(store.ErrWorkAdmissionConflict, err)
	}
	record, advanced, err := engine.store.BeginReleaseWorkPreparation(ctx, workMutation(record, owner), authority)
	if err != nil || !advanced {
		return model.Task{}, false, errors.Join(store.ErrWorkAdmissionConflict, err)
	}
	l := &releaseExecutionLease{record: record, owner: owner, taskID: taskID, scopeDigest: plan.ApprovalSummary.Lifecycle.ScopeDigest, plan: plan, executor: profile, attempts: make(map[string]closureAttempt)}
	l.callContext = executionCallContext(l)
	// 从这里开始，失败保留preparing/uncertain；绝不伪造no_work或重获owner。
	if err = engine.revalidateRegisteredPlan(ctx, l, service, action); err != nil {
		return model.Task{}, false, engine.uncertainRelease(l, err)
	}
	silence, err := engine.prepareMaintenanceSilence(ctx, plan, service, action)
	if err != nil {
		return model.Task{}, false, engine.uncertainRelease(l, err)
	}
	authority, err = engine.releaseAuthority(ctx, actor, service.ObjectID)
	if err != nil {
		return model.Task{}, false, engine.uncertainRelease(l, err)
	}
	started, bound, err := engine.store.StartRegisteredPlanTaskWithEvent(ctx, store.RegisteredPlanTaskInput{Mutation: l.mutation(), Authority: authority, TaskID: taskID, Silence: silence})
	if err != nil || !started.Created {
		return model.Task{}, false, engine.uncertainRelease(l, errors.Join(store.ErrWorkAdmissionConflict, err))
	}
	l.record = bound
	if err = engine.enqueueRegistered(started, l); err != nil {
		return model.Task{}, false, err
	}
	return started.Task, true, nil
}
func (engine *Engine) uncertainRelease(l *releaseExecutionLease, cause error) error {
	l.uncertain = true
	record, err := engine.store.MarkWorkUncertain(context.Background(), l.mutation())
	if err == nil {
		l.record = record
	}
	return errors.Join(cause, err)
}
func (engine *Engine) revalidateRegisteredPlan(ctx context.Context, l *releaseExecutionLease, service model.ServiceDefinition, action model.ActionDefinition) error {
	if _, _, _, err := engine.verifyExecutionProfile(ctx, l.plan, l.scopeInput()); err != nil {
		return err
	}
	result, err := engine.inspectPreparedPlan(ctx, model.PlanPreparation{ID: l.record.ID, Request: model.PlanPreparationRequest{Binding: *l.plan.ApprovalSummary.Lifecycle}}, service, l.callContext)
	if err != nil {
		return err
	}
	if !result.Succeeded {
		return errors.New(result.Failure)
	}
	l.calls = append(l.calls, result.CallDigests...)
	l.cleanup = append(l.cleanup, result.CleanupDigest)
	evidence, err := engine.targetEvidence(ctx, service.Name, action.TargetMode, l.plan.Target)
	if err != nil {
		return err
	}
	r := model.PlanPreparation{Request: model.PlanPreparationRequest{Binding: *l.plan.ApprovalSummary.Lifecycle, Target: l.plan.Target, ScheduleAt: l.plan.ScheduleAt}}
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	r.ResultJSON = string(raw)
	current, err := preparedReleasePlan(r, service, action, evidence)
	if err != nil {
		return err
	}
	if current.Digest != l.plan.Digest {
		return model.ErrReleaseLifecycle
	}
	return engine.ensureServiceTargetAvailable(ctx, service.Name)
}
func (engine *Engine) enqueueRegistered(started store.TaskStartResult, l *releaseExecutionLease) error {
	if engine.remoteDispatch || !started.Created || started.Task.ID != l.taskID {
		return model.ErrReleaseNotIntegrated
	}
	if err := engine.insertReleaseLease(l); err != nil {
		return err
	}
	engine.broker.Publish(started.QueuedEvent.Sequence)
	engine.wait.Add(1)
	go func() { defer engine.wait.Done(); engine.runRegistered(started.Task, l) }()
	return nil
}
