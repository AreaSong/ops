package runner

import (
	"context"
	"errors"
	"strings"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	"github.com/AreaSong/ops/services/areasong-ops/internal/store"
)

func (engine *Engine) replayPlanCreation(ctx context.Context, actor string, request model.PreviewRequest) (model.ReleasePlan, bool, error) {
	service, exists := engine.catalog.Object(request.Service)
	if !exists {
		return model.ReleasePlan{}, false, model.ErrReleaseLifecycle
	}
	if err := engine.authorize(ctx, actor, permissionForAction(request.Action), service.ObjectID); err != nil {
		return model.ReleasePlan{}, false, err
	}
	if request.IdempotencyKey == "" {
		return model.ReleasePlan{}, false, nil
	}
	plan, found, err := engine.store.GetReleasePlanByRequest(ctx, request.IdempotencyKey)
	if err != nil || !found {
		return plan, found, err
	}
	if plan.ActorHash != actor {
		return model.ReleasePlan{}, false, store.ErrActorMismatch
	}
	digest := manualPlanRequestDigest(actor, request)
	if plan.ApprovalSummary.SchemaVersion != 2 {
		digest = request.RequestDigest
		if digest == "" {
			digest = digestText(strings.Join([]string{actor, request.Service, request.Action, request.Target,
				request.RestoreMode, request.RecoveryPointID, scheduleText(request.ScheduleAt)}, "\x00"))
		}
	}
	if plan.RequestDigest != digest || plan.Service != request.Service || plan.Action != request.Action || plan.Target != request.Target {
		return model.ReleasePlan{}, false, store.ErrIdempotency
	}
	return plan, true, nil
}

// 内部自动更新／批量／恢复不被误判为 HTTP 人工来源。
func (engine *Engine) CreateReleasePlan(ctx context.Context, actor string, request model.PreviewRequest) (model.ReleasePlan, error) {
	plan, found, err := engine.replayPlanCreation(ctx, actor, request)
	if err != nil {
		return model.ReleasePlan{}, err
	}
	if found {
		// 内部来源不能借人工 v2 的键，把读取接成批量批准或自动更新关联。
		if plan.ApprovalSummary.SchemaVersion == 2 || plan.ApprovalSummary.Lifecycle != nil {
			return model.ReleasePlan{}, model.ErrReleaseNotIntegrated
		}
		return plan, nil
	}
	return model.ReleasePlan{}, model.ErrReleaseNotIntegrated
}

func (engine *Engine) ApproveReleasePlan(ctx context.Context, actor, id string, request model.ApprovePlanRequest) (model.ReleasePlan, error) {
	if !actorPattern.MatchString(actor) || !uuidPattern.MatchString(id) {
		return model.ReleasePlan{}, model.ErrReleaseLifecycle
	}
	plan, err := engine.store.GetReleasePlan(ctx, id)
	if err != nil {
		return plan, err
	}
	service, exists := engine.catalog.Object(plan.Service)
	if !exists {
		return model.ReleasePlan{}, model.ErrReleaseLifecycle
	}
	authority, err := engine.releaseAuthority(ctx, actor, service.ObjectID)
	if err != nil {
		return model.ReleasePlan{}, err
	}
	if plan.ApprovalSummary.SchemaVersion != 2 || plan.ApprovalSummary.Lifecycle == nil {
		return model.ReleasePlan{}, model.ErrReleaseLifecycle
	}
	_, action, err := engine.resolveAction(plan.Service, plan.Action, plan.Target)
	if err != nil {
		return model.ReleasePlan{}, err
	}
	if err = engine.verifyReleaseScope(ctx, service, action, *plan.ApprovalSummary.Lifecycle, releaseScopeInput{target: plan.Target}); err != nil {
		return model.ReleasePlan{}, err
	}
	return engine.store.ApprovePreparedReleasePlan(ctx, id, authority, request)
}

func (engine *Engine) ExecuteReleasePlan(ctx context.Context, actor, id string, request model.ExecutePlanRequest) (model.Task, bool, error) {
	if !actorPattern.MatchString(actor) || !uuidPattern.MatchString(id) || !uuidPattern.MatchString(request.IdempotencyKey) {
		return model.Task{}, false, model.ErrReleaseLifecycle
	}
	plan, err := engine.store.GetReleasePlan(ctx, id)
	if err != nil {
		return model.Task{}, false, err
	}
	service, exists := engine.catalog.Object(plan.Service)
	if !exists {
		return model.Task{}, false, model.ErrReleaseLifecycle
	}
	if err = engine.authorize(ctx, actor, permissionForAction(plan.Action), service.ObjectID); err != nil {
		return model.Task{}, false, err
	}
	if !plan.AllowsExecutor(actor) {
		return model.Task{}, false, store.ErrActorMismatch
	}
	task, err := engine.store.ReplayReleaseTask(ctx, plan, actor, request.IdempotencyKey)
	if err == nil {
		return task, false, nil
	}
	if !errors.Is(err, model.ErrReleaseNotIntegrated) {
		return model.Task{}, false, err
	}
	return engine.executeRegisteredPlan(ctx, actor, plan, request)
}

func (engine *Engine) CloseReleasePlan(ctx context.Context, actor, id string, request model.ClosePlanRequest) (model.ReleasePlan, error) {
	if !actorPattern.MatchString(actor) || !uuidPattern.MatchString(id) || !uuidPattern.MatchString(request.IdempotencyKey) {
		return model.ReleasePlan{}, model.ErrReleaseLifecycle
	}
	plan, err := engine.store.GetReleasePlan(ctx, id)
	if err != nil {
		return plan, err
	}
	service, exists := engine.catalog.Object(plan.Service)
	if !exists {
		return model.ReleasePlan{}, model.ErrReleaseLifecycle
	}
	if err = engine.authorize(ctx, actor, permissionForAction(plan.Action), service.ObjectID); err != nil {
		return model.ReleasePlan{}, err
	}
	if plan.ActorHash != actor {
		return model.ReleasePlan{}, store.ErrActorMismatch
	}
	if plan.State == model.PlanCompleted {
		return engine.store.CloseReleasePlan(ctx, id, actor, request.IdempotencyKey, model.AuditEntry{})
	}
	return engine.closeRegisteredPlan(ctx, actor, plan, request.IdempotencyKey)
}

func (engine *Engine) CreatePreview(ctx context.Context, actor string, request model.PreviewRequest) (model.Preview, error) {
	service, ok := engine.catalog.Object(request.Service)
	if !ok {
		return model.Preview{}, model.ErrReleaseLifecycle
	}
	if err := engine.authorize(ctx, actor, permissionForAction(request.Action), service.ObjectID); err != nil {
		return model.Preview{}, err
	}
	return model.Preview{}, model.ErrReleaseNotIntegrated
}
