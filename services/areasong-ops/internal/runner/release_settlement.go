package runner

import (
	"context"
	"errors"
	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	"github.com/AreaSong/ops/services/areasong-ops/internal/store"
	"strings"
)

type planClosureBlocked struct{ AttemptID, Reason string }

func (e *planClosureBlocked) Error() string { return e.Reason }

type silenceConfirmation interface {
	SilenceExpired(context.Context, string) (bool, error)
}

func (engine *Engine) closeRegisteredPlan(ctx context.Context, actor string, plan model.ReleasePlan, key string) (model.ReleasePlan, error) {
	l := engine.releaseLease(plan.TaskID)
	if l == nil || !l.mu.TryLock() {
		return model.ReleasePlan{}, errors.New("执行所有权缺失或收口仍在进行")
	}
	defer l.mu.Unlock()
	return engine.finishClosureAttempt(ctx, actor, plan, key, l)
}
func (engine *Engine) finishClosureAttempt(ctx context.Context, actor string, plan model.ReleasePlan, key string, l *releaseExecutionLease) (model.ReleasePlan, error) {
	if engine.releaseLease(plan.TaskID) != l || l.uncertain || !l.executionSettled || !l.heartbeatStopped || !l.locksReleased {
		return model.ReleasePlan{}, store.ErrWorkAdmissionConflict
	}
	if prior, ok := l.attempts[key]; ok {
		return prior.plan, prior.err
	}
	if l.blockedAttempt != "" {
		return model.ReleasePlan{}, errors.New("前次收口结果不确定，禁止新尝试")
	}
	l.blockedAttempt = key
	closed, err := engine.performReleaseClosure(ctx, actor, plan, key, l)
	var blocked *planClosureBlocked
	retry := errors.As(err, &blocked)
	l.attempts[key] = closureAttempt{plan: closed, err: err, retryAllowed: retry}
	if err == nil || retry {
		l.blockedAttempt = ""
	}
	return closed, err
}
func (engine *Engine) performReleaseClosure(ctx context.Context, actor string, plan model.ReleasePlan, key string, l *releaseExecutionLease) (model.ReleasePlan, error) {
	task, err := engine.store.CheckRegisteredTask(ctx, l.mutation())
	if err != nil {
		return model.ReleasePlan{}, err
	}
	if actor != plan.ActorHash || task.State != model.TaskSucceeded || plan.State != model.PlanObserving || plan.ObservationEndsAt == nil {
		return model.ReleasePlan{}, store.ErrWorkAdmissionConflict
	}
	service, action, profile, err := engine.verifyExecutionProfile(ctx, plan, l.scopeInput())
	if err != nil || profile != l.executor {
		return model.ReleasePlan{}, errors.Join(model.ErrReleaseLifecycle, err)
	}
	if _, err = engine.releaseAuthority(ctx, actor, service.ObjectID); err != nil {
		return model.ReleasePlan{}, err
	}
	if engine.releaseTime().Before(*plan.ObservationEndsAt) {
		return model.ReleasePlan{}, engine.finishedClosureBlock(ctx, l, actor, key, "观察窗口尚未结束", nil)
	}
	if err = engine.releaseRegisteredSilence(ctx, plan, l); err != nil {
		return model.ReleasePlan{}, err
	}
	blockers, err := engine.blockingAlerts(ctx, service)
	if err != nil {
		return model.ReleasePlan{}, err
	}
	if len(blockers) > 0 {
		return model.ReleasePlan{}, engine.finishedClosureBlock(ctx, l, actor, key, "关联阻断告警仍在触发: "+alertNames(blockers), alertFingerprints(blockers))
	}
	result, err := engine.inspectPreparedPlan(ctx, model.PlanPreparation{ID: l.record.ID, Request: model.PlanPreparationRequest{Binding: *plan.ApprovalSummary.Lifecycle}}, service, l.callContext)
	if err != nil {
		l.uncertain = true
		return model.ReleasePlan{}, err
	}
	l.calls = append(l.calls, result.CallDigests...)
	l.cleanup = append(l.cleanup, result.CleanupDigest)
	if !result.Succeeded {
		return model.ReleasePlan{}, engine.finishedClosureBlock(ctx, l, actor, key, result.Failure, nil)
	}
	if err = engine.verifyClosureIdentity(ctx, plan, result.Snapshot); err != nil {
		return model.ReleasePlan{}, engine.finishedClosureBlock(ctx, l, actor, key, err.Error(), nil)
	}
	if err = engine.verifyReleaseScope(ctx, service, action, *plan.ApprovalSummary.Lifecycle, l.scopeInput()); err != nil {
		return model.ReleasePlan{}, err
	}
	authority, err := engine.releaseAuthority(ctx, actor, service.ObjectID)
	if err != nil {
		return model.ReleasePlan{}, err
	}
	closed, err := engine.store.ClosePlanAndRegisteredWork(ctx, store.RegisteredPlanClose{Work: releaseCloseEvidence(l), Authority: authority, IdempotencyKey: key})
	if err != nil {
		// 只读恢复提交响应丢失；不能再检查、解除静默或重新获取执行能力。
		existing, e := engine.store.CloseReleasePlan(context.Background(), plan.ID, actor, key, model.AuditEntry{})
		record, rerr := engine.store.GetWorkAdmission(context.Background(), l.record.ID)
		if e == nil && rerr == nil && record.State == model.WorkClosed {
			l.record = record
			return existing, nil
		}
		return model.ReleasePlan{}, err
	}
	return closed, nil
}
func (engine *Engine) finishedClosureBlock(ctx context.Context, l *releaseExecutionLease, actor, key, reason string, fingerprints []string) error {
	service, exists := engine.catalog.Object(l.plan.Service)
	if !exists {
		return model.ErrReleaseLifecycle
	}
	authority, err := engine.releaseAuthority(ctx, actor, service.ObjectID)
	if err != nil {
		return err
	}
	reason = redactText(reason)
	err = engine.store.RecordRegisteredClosureBlocker(ctx, store.RegisteredClosureBlocker{Mutation: l.mutation(), Authority: authority, IdempotencyKey: key, Reason: reason, Fingerprints: fingerprints})
	if err != nil {
		return err
	}
	return &planClosureBlocked{AttemptID: key, Reason: reason}
}

func (engine *Engine) releaseRegisteredSilence(ctx context.Context, plan model.ReleasePlan, l *releaseExecutionLease) error {
	if plan.MaintenanceSilenceID == "" {
		l.cleanup = append(l.cleanup, model.WorkDigest("no-maintenance-silence:"+plan.ID))
		return nil
	}
	if plan.MaintenanceSilenceReleasedAt != nil {
		return nil
	}
	confirm, ok := engine.alertmanager.(silenceConfirmation)
	if !ok {
		return errors.New("静默服务不支持精确ID确认")
	}
	if !l.silenceDeleteConfirmed {
		if err := engine.alertmanager.ExpireSilence(ctx, plan.MaintenanceSilenceID); err != nil {
			l.uncertain = true
			return err
		}
		l.silenceDeleteConfirmed = true
	}
	expired, err := confirm.SilenceExpired(ctx, plan.MaintenanceSilenceID)
	if err != nil {
		return err
	}
	if !expired {
		return errors.New("静默解除尚未确认")
	}
	err = engine.store.RecordRegisteredSilenceReleased(ctx, l.mutation(), plan.MaintenanceSilenceID)
	if err != nil {
		// 仅本地提交明确失败时，原调用收据仍在：再确认、补交本地事实一次。
		expired, e := confirm.SilenceExpired(ctx, plan.MaintenanceSilenceID)
		if e != nil || !expired {
			return errors.Join(err, e)
		}
		if e = engine.store.RecordRegisteredSilenceReleased(ctx, l.mutation(), plan.MaintenanceSilenceID); e != nil {
			return errors.Join(err, e)
		}
	}
	l.cleanup = append(l.cleanup, model.WorkDigest("silence-expired:"+plan.MaintenanceSilenceID))
	return nil
}
func releaseCloseEvidence(l *releaseExecutionLease) store.WorkAdmissionClose {
	return store.WorkAdmissionClose{Mutation: l.mutation(), CloseKind: "settled", Evidence: model.WorkCloseEvidence{Kind: "execution_and_cleanup_settled_v1", ExecutorStopped: l.executionSettled && l.heartbeatStopped, CleanupConfirmed: l.locksReleased && !l.uncertain, ResultDigest: model.WorkDigest(strings.Join(l.calls, "\x00")), CleanupDigest: model.WorkDigest(strings.Join(l.cleanup, "\x00") + "\x00heartbeat-exited;locks-released;task-evidence-retained")}}
}
func (engine *Engine) settleFailedRelease(ctx context.Context, plan model.ReleasePlan, l *releaseExecutionLease) (model.ReleasePlan, error) {
	if !l.executionSettled || l.uncertain || !l.heartbeatStopped || !l.locksReleased {
		return model.ReleasePlan{}, store.ErrWorkAdmissionConflict
	}
	if err := engine.releaseRegisteredSilence(ctx, plan, l); err != nil {
		return model.ReleasePlan{}, err
	}
	return engine.store.ClosePlanAndRegisteredWork(ctx, store.RegisteredPlanClose{Work: releaseCloseEvidence(l)})
}
