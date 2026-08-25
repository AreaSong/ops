package runner

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	"github.com/AreaSong/ops/services/areasong-ops/internal/store"
)

// inspectForAction keeps lifecycle planning usable when an application that is
// already stopped cannot answer its normal inspect probe. The fallback is
// deliberately narrow: it requires a durable stopped desired state and binds
// the plan to the last known runtime identity instead of inventing a new one.
func (engine *Engine) inspectForAction(
	ctx context.Context,
	service model.ServiceDefinition,
	action string,
) (map[string]any, error) {
	status, inspectErr := engine.inspect(ctx, service)
	if inspectErr == nil {
		return status, nil
	}
	if action != "start" && action != "stop" {
		return nil, inspectErr
	}
	state, found, stateErr := engine.store.GetServiceState(ctx, service.Name)
	if stateErr != nil || !found || state.Desired != model.DesiredStopped {
		return nil, inspectErr
	}

	// Prefer observer data, then the immutable pre-stop snapshot. Both are
	// persisted by the control plane and therefore remain stable across a
	// Runner restart.
	snapshot := cloneAnyMap(state.Data)
	if !hasRuntimeIdentity(snapshot) {
		if tasks, listErr := engine.store.ListTasks(ctx, 200, 0); listErr == nil {
			for _, task := range tasks {
				if task.Service == service.Name && task.Action == "stop" &&
					task.State == model.TaskSucceeded && hasRuntimeIdentity(task.Snapshot) {
					snapshot = cloneAnyMap(task.Snapshot)
					break
				}
			}
		}
	}
	if snapshot == nil {
		snapshot = make(map[string]any)
	}
	// Reconcile must see the durable lifecycle state, while runtime identity
	// fields remain whatever was observed before the stop.
	snapshot["objectId"] = service.ObjectID
	snapshot["service"] = service.Name
	snapshot["state"] = "stopped"
	snapshot["actualState"] = "stopped"
	snapshot["desiredState"] = string(state.Desired)
	snapshot["stateGeneration"] = state.Generation
	snapshot["lifecycleInspectFallback"] = true
	return snapshot, nil
}

func cloneAnyMap(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	result := make(map[string]any, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}

func hasRuntimeIdentity(snapshot map[string]any) bool {
	if snapshot == nil {
		return false
	}
	for _, field := range []string{"currentVersion", "currentImage", "currentImageId", "runtimeIdentityHash"} {
		value, _ := snapshot[field].(string)
		if value == "" {
			return false
		}
	}
	return true
}

func (engine *Engine) CreateReleasePlan(
	ctx context.Context,
	actorHash string,
	request model.PreviewRequest,
) (model.ReleasePlan, error) {
	if !actorPattern.MatchString(actorHash) {
		return model.ReleasePlan{}, errors.New("操作者标识无效")
	}
	requestDigest := request.RequestDigest
	if requestDigest == "" {
		requestDigest = digestText(strings.Join([]string{actorHash, request.Service, request.Action, request.Target,
			request.RestoreMode, request.RecoveryPointID, scheduleText(request.ScheduleAt)}, "\x00"))
	}
	if request.IdempotencyKey != "" {
		if existing, found, err := engine.store.GetReleasePlanByRequest(ctx, request.IdempotencyKey); err != nil {
			return model.ReleasePlan{}, err
		} else if found {
			if existing.ActorHash != actorHash {
				return model.ReleasePlan{}, store.ErrActorMismatch
			}
			if existing.RequestDigest != requestDigest {
				return model.ReleasePlan{}, store.ErrIdempotency
			}
			return existing, nil
		}
	}
	service, action, err := engine.resolveAction(request.Service, request.Action, request.Target)
	if err != nil {
		return model.ReleasePlan{}, err
	}
	tenantID := service.TenantID
	if tenantID == "" {
		tenantID = "default"
	}
	var restorePoint *model.RecoveryPoint
	if request.RestoreMode != "" {
		if request.RecoveryPointID == "" || (request.RestoreMode != "isolated" && request.RestoreMode != "production") {
			return model.ReleasePlan{}, errors.New("恢复计划绑定信息无效")
		}
		point, pointErr := engine.store.GetRecoveryPoint(ctx, request.RecoveryPointID)
		if pointErr != nil || point.Status != "verified" || point.Service != service.Name {
			return model.ReleasePlan{}, errors.New("恢复点不可用于当前服务")
		}
		pointTenantID := point.TenantID
		if pointTenantID == "" {
			pointTenantID = request.RestoreTenantID
		}
		pointServerID := point.ServerID
		if pointServerID == "" {
			pointServerID = request.RestoreServerID
		}
		if request.RestoreTenantID != "" && request.RestoreTenantID != pointTenantID {
			return model.ReleasePlan{}, errors.New("恢复计划租户绑定不一致")
		}
		if request.RestoreServerID != "" && request.RestoreServerID != pointServerID {
			return model.ReleasePlan{}, errors.New("恢复计划服务器绑定不一致")
		}
		if request.RestoreExpectedBeforeDigest != "" && request.RestoreExpectedBeforeDigest != point.ExpectedBeforeDigest {
			return model.ReleasePlan{}, errors.New("恢复计划变更前身份绑定不一致")
		}
		request.RestoreTenantID = pointTenantID
		request.RestoreServerID = pointServerID
		request.RestoreExpectedBeforeDigest = point.ExpectedBeforeDigest
		request.RestoreContractDigest = point.BindingDigest
		request.RestoreEvidenceDigest = point.EvidenceDigest
		if request.RestoreContractDigest == "" || request.RestoreEvidenceDigest == "" {
			return model.ReleasePlan{}, errors.New("恢复点缺少不可变绑定摘要")
		}
		restorePoint = &point
	}
	if err := engine.authorize(ctx, actorHash, permissionForAction(action.Name), service.ObjectID); err != nil {
		return model.ReleasePlan{}, err
	}
	if active, found, err := engine.store.ActiveTask(ctx, service.Name); err != nil {
		return model.ReleasePlan{}, err
	} else if found {
		return model.ReleasePlan{}, fmt.Errorf("服务已有活动任务: %s", active.ID)
	}
	snapshot, err := engine.inspectForAction(ctx, service, action.Name)
	if err != nil {
		return model.ReleasePlan{}, fmt.Errorf("创建计划前检查失败: %w", err)
	}
	snapshot = approvalSnapshot(service, snapshot)
	if action.TargetMode == "controlled_rollback" {
		if err := engine.validateRollbackSource(request.Service, request.Target, snapshot); err != nil {
			return model.ReleasePlan{}, err
		}
		snapshot["rollbackSourceTaskId"] = request.Target
	}
	phrase := renderConfirmation(action.ConfirmationTemplate, service.Name, request.Target)
	targetEvidence, err := engine.targetEvidence(ctx, service.Name, action.TargetMode, request.Target)
	if err != nil {
		return model.ReleasePlan{}, err
	}
	summary := model.ApprovalSummary{
		SchemaVersion: 1, Service: service.Name, Action: action.Name, Target: request.Target,
		TrafficPolicyDigest: service.PolicyDigest(),
		Risk:                action.Risk, Impact: action.Impact, Rollback: action.Rollback,
		Scope: action.Scope, Steps: append([]string(nil), action.Steps...),
		PhaseSemantics: resolvedPhaseSemantics(action), ObservationSeconds: action.ObservationSeconds, TimeoutSeconds: action.TimeoutSeconds,
		AlertPolicy:        service.AlertPolicy,
		ConfirmationPhrase: phrase,
		ExpectedBefore:     snapshot, TargetEvidence: targetEvidence,
		RestoreMode: request.RestoreMode, RecoveryPointID: request.RecoveryPointID,
		TenantID: tenantID, ServerID: service.ServerID, ScheduleAt: request.ScheduleAt,
		ExpectedBeforeDigest:        request.RestoreExpectedBeforeDigest,
		RecoveryPointBindingDigest:  request.RestoreContractDigest,
		RecoveryPointEvidenceDigest: request.RestoreEvidenceDigest,
	}
	digest, err := approvalDigest(summary)
	if err != nil {
		return model.ReleasePlan{}, err
	}
	id, err := newUUID()
	if err != nil {
		return model.ReleasePlan{}, err
	}
	now := time.Now().UTC()
	plan := model.ReleasePlan{
		ID: id, ActorHash: actorHash, Service: service.Name, Action: action.Name,
		Target: request.Target, Risk: action.Risk, State: model.PlanPendingApproval,
		TenantID: tenantID, ServerID: service.ServerID, ScheduleAt: request.ScheduleAt,
		Digest: digest, ApprovalSummary: summary, ConfirmationPhrase: phrase,
		RequiresConfirmation: action.Risk != model.RiskReadOnly,
		ObservationSeconds:   action.ObservationSeconds, CreatedAt: now, UpdatedAt: now,
		RequestIdempotencyKey: request.IdempotencyKey, RequestDigest: requestDigest,
		RestoreMode: request.RestoreMode, RecoveryPointID: request.RecoveryPointID,
		RequiresDualApproval: request.RequiresDualApproval,
		RestoreTenantID:      request.RestoreTenantID, RestoreServerID: request.RestoreServerID,
		RestoreExpectedBeforeDigest: request.RestoreExpectedBeforeDigest,
		RestoreContractDigest:       request.RestoreContractDigest,
		RestoreEvidenceDigest:       request.RestoreEvidenceDigest,
	}
	_ = restorePoint // retained to make the binding lookup explicit above
	input := store.ReleasePlanInput{Plan: plan, ConfirmationHash: store.HashConfirmation(phrase)}
	if request.IdempotencyKey != "" {
		stored, created, err := engine.store.CreateReleasePlanIdempotent(ctx, input, actorHash, request.IdempotencyKey, requestDigest)
		if err != nil {
			return model.ReleasePlan{}, err
		}
		event := "plan.created.replayed"
		outcome := "replayed"
		if created {
			event, outcome = "plan.created", "accepted"
		}
		_, _ = engine.store.AppendAudit(ctx, model.AuditEntry{ActorHash: actorHash, Event: event, Resource: stored.ID, Outcome: outcome,
			Detail: map[string]any{"service": stored.Service, "action": stored.Action, "target": stored.Target, "digest": stored.Digest, "idempotencyKey": request.IdempotencyKey}})
		return stored, nil
	}
	if err := engine.store.CreateReleasePlan(ctx, input); err != nil {
		return model.ReleasePlan{}, err
	}
	_, _ = engine.store.AppendAudit(ctx, model.AuditEntry{
		ActorHash: actorHash, Event: "plan.created", Resource: plan.ID, Outcome: "accepted",
		Detail: map[string]any{"service": plan.Service, "action": plan.Action, "target": plan.Target, "digest": plan.Digest},
	})
	return plan, nil
}

func scheduleText(value *time.Time) string {
	if value == nil {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func (engine *Engine) ApproveReleasePlan(
	ctx context.Context,
	actorHash, id string,
	request model.ApprovePlanRequest,
) (model.ReleasePlan, error) {
	if !actorPattern.MatchString(actorHash) || !uuidPattern.MatchString(id) {
		return model.ReleasePlan{}, errors.New("发布计划请求标识无效")
	}
	plan, err := engine.store.GetReleasePlan(ctx, id)
	if err != nil {
		return model.ReleasePlan{}, err
	}
	if plan.Risk == model.RiskHigh && plan.ActorHash == actorHash {
		return model.ReleasePlan{}, store.ErrActorMismatch
	}
	service, exists := engine.catalog.Object(plan.Service)
	if !exists {
		return model.ReleasePlan{}, errors.New("受管对象声明不存在")
	}
	if err := engine.authorize(ctx, actorHash, permissionForAction(plan.Action), service.ObjectID); err != nil {
		return model.ReleasePlan{}, err
	}
	plan, err = engine.store.ApproveReleasePlan(ctx, id, actorHash, request.Digest, request.Confirmation)
	if err != nil {
		return model.ReleasePlan{}, err
	}
	_, _ = engine.store.AppendAudit(ctx, model.AuditEntry{
		ActorHash: actorHash, Event: "plan.approved", Outcome: string(plan.State), Resource: plan.ID,
		Detail: map[string]any{"digest": plan.Digest, "scheduleAt": plan.ScheduleAt},
	})
	return plan, nil
}

func (engine *Engine) ExecuteReleasePlan(
	ctx context.Context,
	actorHash, id string,
	request model.ExecutePlanRequest,
) (model.Task, bool, error) {
	if !actorPattern.MatchString(actorHash) || !uuidPattern.MatchString(id) ||
		!uuidPattern.MatchString(request.IdempotencyKey) {
		return model.Task{}, false, errors.New("执行计划请求标识无效")
	}
	plan, err := engine.store.GetReleasePlan(ctx, id)
	if err != nil {
		return model.Task{}, false, err
	}
	if plan.ActorHash != actorHash {
		return model.Task{}, false, store.ErrActorMismatch
	}
	serviceForAuth, exists := engine.catalog.Object(plan.Service)
	if !exists {
		return model.Task{}, false, errors.New("受管对象声明不存在")
	}
	if err := engine.authorize(ctx, actorHash, permissionForAction(plan.Action), serviceForAuth.ObjectID); err != nil {
		return model.Task{}, false, err
	}
	if plan.State == model.PlanScheduled {
		if plan.ScheduleAt == nil || time.Now().UTC().Before(*plan.ScheduleAt) {
			return model.Task{}, false, errors.New("发布计划尚未到达调度时间")
		}
		activated, activateErr := engine.store.ActivateScheduledPlan(ctx, plan.ID, time.Now().UTC())
		if activateErr != nil {
			return model.Task{}, false, activateErr
		}
		plan, err = engine.store.GetReleasePlan(ctx, id)
		if err != nil {
			return model.Task{}, false, err
		}
		if activated {
			_, _ = engine.store.AppendAudit(ctx, model.AuditEntry{ActorHash: actorHash, Event: "plan.schedule.released", Resource: plan.ID, Outcome: "approved",
				Detail: map[string]any{"scheduleAt": plan.ScheduleAt}})
		}
	}
	if plan.State == model.PlanExecuting || plan.State == model.PlanObserving ||
		plan.State == model.PlanNeedsAttention || plan.State == model.PlanCompleted {
		task, taskErr := engine.store.GetTask(ctx, plan.TaskID)
		if taskErr == nil && task.ActorHash == actorHash && task.PlanID == plan.ID &&
			task.IdempotencyKey == request.IdempotencyKey {
			return task, false, nil
		}
		return model.Task{}, false, store.ErrIdempotency
	}
	if plan.RequiresDualApproval && (plan.SecondApprovedByHash == "" || plan.SecondApprovedByHash == plan.ApprovedByHash) {
		return model.Task{}, false, errors.New("生产恢复计划尚未完成独立第二批准")
	}
	if plan.State != model.PlanApproved {
		return model.Task{}, false, errors.New("发布计划尚未批准或已经执行")
	}
	service, action, err := engine.resolveAction(plan.Service, plan.Action, plan.Target)
	if err != nil {
		_ = engine.store.InvalidateReleasePlan(ctx, plan.ID, "服务能力或目标策略已变化")
		return model.Task{}, false, err
	}
	observed, err := engine.inspectForAction(ctx, service, action.Name)
	if err != nil {
		return model.Task{}, false, fmt.Errorf("执行计划前检查失败: %w", err)
	}
	observed = approvalSnapshot(service, observed)
	if plan.Action == "rollback" {
		if err := engine.validateRollbackSource(plan.Service, plan.Target, observed); err != nil {
			_ = engine.store.InvalidateReleasePlan(ctx, plan.ID, err.Error())
			return model.Task{}, false, err
		}
		observed["rollbackSourceTaskId"] = plan.Target
	}
	targetEvidence, err := engine.targetEvidence(ctx, service.Name, action.TargetMode, plan.Target)
	if err != nil {
		_ = engine.store.InvalidateReleasePlan(ctx, plan.ID, err.Error())
		return model.Task{}, false, err
	}
	currentSummary := model.ApprovalSummary{
		SchemaVersion: 1, Service: service.Name, Action: action.Name, Target: plan.Target,
		TrafficPolicyDigest: service.PolicyDigest(),
		Risk:                action.Risk, Impact: action.Impact, Rollback: action.Rollback, Scope: action.Scope,
		Steps: append([]string(nil), action.Steps...), PhaseSemantics: resolvedPhaseSemantics(action),
		ObservationSeconds: action.ObservationSeconds, TimeoutSeconds: action.TimeoutSeconds,
		AlertPolicy:        actionAlertPolicy(service),
		ConfirmationPhrase: renderConfirmation(action.ConfirmationTemplate, service.Name, plan.Target),
		ExpectedBefore:     observed, TargetEvidence: targetEvidence,
		RestoreMode: plan.RestoreMode, RecoveryPointID: plan.RecoveryPointID,
		TenantID: plan.TenantID, ServerID: plan.ServerID, ScheduleAt: plan.ScheduleAt,
		ExpectedBeforeDigest:        plan.RestoreExpectedBeforeDigest,
		RecoveryPointBindingDigest:  plan.RestoreContractDigest,
		RecoveryPointEvidenceDigest: plan.RestoreEvidenceDigest,
	}
	currentDigest, digestErr := approvalDigest(currentSummary)
	if digestErr != nil {
		return model.Task{}, false, digestErr
	}
	expected, _ := json.Marshal(plan.ApprovalSummary.ExpectedBefore)
	actual, _ := json.Marshal(observed)
	if currentDigest != plan.Digest || !bytes.Equal(expected, actual) {
		reason := "运行身份或计划内容与批准摘要不一致"
		if invalidateErr := engine.store.InvalidateReleasePlan(ctx, plan.ID, reason); invalidateErr != nil {
			return model.Task{}, false, fmt.Errorf("%s，且计划失效写入失败: %w", reason, invalidateErr)
		}
		return model.Task{}, false, errors.New(reason + "，请重新创建计划")
	}
	if plan.RestoreMode != "" {
		point, verifyErr := engine.verifyRestorePlanBinding(ctx, plan, service)
		if verifyErr != nil {
			reason := "恢复点执行前复验失败: " + redactText(verifyErr.Error())
			if invalidateErr := engine.store.InvalidateReleasePlan(ctx, plan.ID, reason); invalidateErr != nil {
				return model.Task{}, false, fmt.Errorf("%s，且计划失效写入失败: %w", reason, invalidateErr)
			}
			return model.Task{}, false, errors.New(reason + "，请重新创建并批准恢复计划")
		}
		revalidatedAt := time.Now().UTC()
		if err := engine.store.MarkRestorePlanRevalidated(
			ctx, plan.ID, actorHash, point.BindingDigest, revalidatedAt,
		); err != nil {
			return model.Task{}, false, fmt.Errorf("恢复计划复验结果无法持久化: %w", err)
		}
		plan.RestoreRevalidationDigest = point.BindingDigest
		plan.RestoreRevalidatedAt = &revalidatedAt
		plan.ExecutedByHash = actorHash
	}
	// Approval can outlive the Fleet heartbeat lease. A fresh execution must
	// therefore re-check the service's server/Runner binding immediately before
	// any maintenance silence or task is created. Idempotent replays returned
	// above do not need a second gate because they cannot start another task.
	if err := engine.ensureServiceTargetAvailable(ctx, service.Name); err != nil {
		return model.Task{}, false, fmt.Errorf("目标 Runner 当前不可执行: %w", err)
	}
	silence, err := engine.prepareMaintenanceSilence(ctx, plan, service, action)
	if err != nil {
		return model.Task{}, false, err
	}
	taskID, err := newUUID()
	if err != nil {
		if silence != nil {
			_ = engine.alertmanager.ExpireSilence(context.Background(), silence.ID)
		}
		return model.Task{}, false, err
	}
	task, created, err := engine.store.StartPlanTask(ctx, plan, actorHash, request.IdempotencyKey, taskID, silence)
	if err != nil || !created {
		if silence != nil {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			cleanupErr := engine.alertmanager.ExpireSilence(cleanupCtx, silence.ID)
			cancel()
			if cleanupErr != nil && err != nil {
				return model.Task{}, false, fmt.Errorf("%w，且临时维护静默解除失败: %v", err, cleanupErr)
			}
		}
		return task, created, err
	}
	task.TrafficPolicyDigest = plan.ApprovalSummary.TrafficPolicyDigest
	engine.enqueue(task)
	return task, true, nil
}

func approvalSnapshot(object model.ServiceDefinition, observed map[string]any) map[string]any {
	var result map[string]any
	if object.Metadata.Type != "automatic_task" {
		result = cloneAnyMap(observed)
	} else {
		result = make(map[string]any)
		for _, key := range []string{"objectId", "taskName", "scheduleSource", "enabled"} {
			if value, exists := observed[key]; exists {
				result[key] = value
			}
		}
	}
	if result == nil {
		result = make(map[string]any)
	}
	if digest := object.PolicyDigest(); digest != "" {
		// Persist the contract in the task snapshot as well as the signed
		// summary. The existing task schema stores this map, so remote dispatch
		// can carry the digest without a database migration.
		result["trafficPolicyDigest"] = digest
	}
	return result
}

func actionAlertPolicy(service model.ServiceDefinition) model.AlertPolicyDefinition {
	return model.AlertPolicyDefinition{
		Matchers:          cloneStringMap(service.AlertPolicy.Matchers),
		BlockingAlerts:    append([]string(nil), service.AlertPolicy.BlockingAlerts...),
		MaintenanceAlerts: append([]string(nil), service.AlertPolicy.MaintenanceAlerts...),
	}
}

func cloneStringMap(input map[string]string) map[string]string {
	result := make(map[string]string, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}

func resolvedPhaseSemantics(action model.ActionDefinition) map[string]model.PhaseSemantics {
	result := make(map[string]model.PhaseSemantics, len(action.Steps))
	for _, phase := range action.Steps {
		result[phase] = model.EffectivePhaseSemantics(action, phase)
	}
	return result
}

func (engine *Engine) CloseReleasePlan(
	ctx context.Context,
	actorHash, id string,
	request model.ClosePlanRequest,
) (model.ReleasePlan, error) {
	if !actorPattern.MatchString(actorHash) || !uuidPattern.MatchString(id) ||
		!uuidPattern.MatchString(request.IdempotencyKey) {
		return model.ReleasePlan{}, errors.New("收口计划请求标识无效")
	}
	plan, err := engine.store.GetReleasePlan(ctx, id)
	if err != nil {
		return model.ReleasePlan{}, err
	}
	if plan.ActorHash != actorHash {
		return model.ReleasePlan{}, store.ErrActorMismatch
	}
	serviceForAuth, exists := engine.catalog.Object(plan.Service)
	if !exists {
		return model.ReleasePlan{}, errors.New("受管对象声明不存在")
	}
	if err := engine.authorize(ctx, actorHash, permissionForAction(plan.Action), serviceForAuth.ObjectID); err != nil {
		return model.ReleasePlan{}, err
	}
	if plan.State == model.PlanCompleted {
		return engine.store.CloseReleasePlan(ctx, id, actorHash, request.IdempotencyKey, model.AuditEntry{})
	}
	if plan.State != model.PlanObserving || plan.ObservationEndsAt == nil {
		return model.ReleasePlan{}, errors.New("计划当前不能收口")
	}
	if time.Now().UTC().Before(*plan.ObservationEndsAt) {
		return model.ReleasePlan{}, engine.blockPlanClosure(ctx, actorHash, plan.ID, "观察窗口尚未结束", nil)
	}
	task, err := engine.store.GetTask(ctx, plan.TaskID)
	if err != nil || task.State != model.TaskSucceeded {
		return model.ReleasePlan{}, engine.blockPlanClosure(ctx, actorHash, plan.ID, "执行任务未成功，计划不能收口", nil)
	}
	service, ok := engine.catalog.Object(plan.Service)
	if !ok {
		return model.ReleasePlan{}, engine.blockPlanClosure(ctx, actorHash, plan.ID, "受管对象声明不存在", nil)
	}
	if err := engine.releasePlanSilence(ctx, actorHash, &plan); err != nil {
		return model.ReleasePlan{}, engine.blockPlanClosure(ctx, actorHash, plan.ID,
			"维护静默无法解除: "+redactText(err.Error()), nil)
	}
	blockers, err := engine.blockingAlerts(ctx, service)
	if err != nil {
		return model.ReleasePlan{}, engine.blockPlanClosure(ctx, actorHash, plan.ID,
			"收口无法读取 Alertmanager: "+redactText(err.Error()), nil)
	}
	if len(blockers) > 0 {
		return model.ReleasePlan{}, engine.blockPlanClosure(ctx, actorHash, plan.ID,
			"关联阻断告警仍在触发: "+alertNames(blockers), alertFingerprints(blockers))
	}
	observed, err := engine.inspectForAction(ctx, service, plan.Action)
	if err != nil {
		return model.ReleasePlan{}, engine.blockPlanClosure(ctx, actorHash, plan.ID, "收口身份检查失败: "+redactText(err.Error()), nil)
	}
	if err := engine.verifyClosureIdentity(ctx, plan, observed); err != nil {
		return model.ReleasePlan{}, engine.blockPlanClosure(ctx, actorHash, plan.ID, err.Error(), nil)
	}
	audit := model.AuditEntry{
		ActorHash: actorHash, Event: "plan.closed", Resource: plan.ID, Outcome: "completed",
		Detail: map[string]any{"taskId": plan.TaskID, "observationSeconds": plan.ObservationSeconds},
	}
	closed, err := engine.store.CloseReleasePlan(ctx, id, actorHash, request.IdempotencyKey, audit)
	if err != nil {
		return model.ReleasePlan{}, err
	}
	return closed, nil
}

func (engine *Engine) blockPlanClosure(
	ctx context.Context,
	actorHash, id, reason string,
	blockingAlertFingerprints []string,
) error {
	reason = redactText(reason)
	audit := model.AuditEntry{
		ActorHash: actorHash, Event: "plan.close_rejected", Resource: id, Outcome: "rejected",
		Detail: map[string]any{"reason": reason},
	}
	if err := engine.store.RecordPlanClosureBlocker(ctx, id, reason, blockingAlertFingerprints, audit); err != nil {
		return fmt.Errorf("%s，且阻断原因写入失败: %w", reason, err)
	}
	return errors.New(reason)
}

func (engine *Engine) releasePlanSilence(
	ctx context.Context,
	actorHash string,
	plan *model.ReleasePlan,
) error {
	if plan.MaintenanceSilenceID == "" || plan.MaintenanceSilenceReleasedAt != nil {
		return nil
	}
	if err := engine.alertmanager.ExpireSilence(ctx, plan.MaintenanceSilenceID); err != nil {
		return err
	}
	audit := model.AuditEntry{
		ActorHash: actorHash, Event: "plan.maintenance_silence_released",
		Resource: plan.ID, Outcome: "released",
		Detail: map[string]any{"silenceId": plan.MaintenanceSilenceID},
	}
	if err := engine.store.RecordPlanSilenceReleased(ctx, plan.ID, audit); err != nil {
		return err
	}
	now := time.Now().UTC()
	plan.MaintenanceSilenceReleasedAt = &now
	return nil
}

func (engine *Engine) verifyClosureIdentity(
	ctx context.Context,
	plan model.ReleasePlan,
	observed map[string]any,
) error {
	actualVersion, _ := observed["currentVersion"].(string)
	switch plan.Action {
	case "update":
		if actualVersion == "" || actualVersion != strings.TrimPrefix(plan.Target, "v") {
			return errors.New("当前版本与计划目标不一致")
		}
		if expectedImage, _ := plan.ApprovalSummary.TargetEvidence["webImageDigest"].(string); expectedImage != "" {
			actualImage, _ := observed["currentImage"].(string)
			if actualImage != expectedImage {
				return errors.New("当前镜像与计划目标摘要不一致")
			}
		}
	case "rollback":
		source, err := engine.store.GetTask(ctx, plan.Target)
		if err != nil {
			return errors.New("回滚来源任务不可用")
		}
		if !sameRuntimeIdentity(source.Snapshot, observed) {
			return errors.New("当前版本与回滚目标身份不一致")
		}
	default:
		if !sameRuntimeIdentity(plan.ApprovalSummary.ExpectedBefore, observed) {
			return errors.New("当前运行身份与计划预期不一致")
		}
	}
	return nil
}

func sameRuntimeIdentity(expected, observed map[string]any) bool {
	for _, field := range []string{"currentVersion", "currentImage", "currentImageId", "runtimeIdentityHash"} {
		expectedValue, _ := expected[field].(string)
		observedValue, _ := observed[field].(string)
		if expectedValue == "" || observedValue != expectedValue {
			return false
		}
	}
	return true
}

func (engine *Engine) targetEvidence(ctx context.Context, service, targetMode, target string) (map[string]any, error) {
	if target == "" || (targetMode != "signed_release_tag" && targetMode != "allowlist") {
		return nil, nil
	}
	discovery, found, err := engine.store.LatestSuccessfulDiscovery(ctx, service)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, errors.New("目标版本缺少已验证的发布发现证据")
	}
	latest, _ := discovery["latestTag"].(string)
	if latest == "" {
		if version, ok := discovery["manifestVersion"].(string); ok {
			latest = "v" + version
		}
	}
	if latest != target {
		return nil, errors.New("目标版本与最近发布发现证据不一致")
	}
	return discovery, nil
}

func (engine *Engine) validateRollbackSource(service, sourceID string, snapshot map[string]any) error {
	source, err := engine.store.GetTask(context.Background(), sourceID)
	if err != nil || source.Service != service || source.Action != "update" || source.State != model.TaskSucceeded {
		return errors.New("回滚来源必须是同服务已成功的受控更新任务")
	}
	sourceDir := filepath.Join(engine.stateRoot, "operations", source.ID)
	if info, err := os.Lstat(sourceDir); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("回滚来源产物不存在或不安全")
	}
	currentVersion, _ := snapshot["currentVersion"].(string)
	if currentVersion != strings.TrimPrefix(source.Target, "v") {
		return errors.New("回滚来源不再对应当前生产版本")
	}
	return nil
}

func approvalDigest(summary model.ApprovalSummary) (string, error) {
	data, err := json.Marshal(summary)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func (engine *Engine) CreateRecoveryPlan(
	ctx context.Context,
	actorHash, taskID string,
	request model.RecoveryRequest,
) (model.ReleasePlan, error) {
	task, err := engine.store.GetTask(ctx, taskID)
	if err != nil {
		return model.ReleasePlan{}, err
	}
	if task.ActorHash != actorHash {
		return model.ReleasePlan{}, store.ErrActorMismatch
	}
	switch request.Action {
	case "inspect":
		return engine.CreateReleasePlan(ctx, actorHash, model.PreviewRequest{Service: task.Service, Action: "inspect"})
	case "retry":
		if task.State != model.TaskFailedRecoverable || !task.Retryable {
			return model.ReleasePlan{}, errors.New("该任务不允许自动重试")
		}
		return engine.CreateReleasePlan(ctx, actorHash, model.PreviewRequest{
			Service: task.Service, Action: task.Action, Target: task.Target,
		})
	default:
		return model.ReleasePlan{}, errors.New("该恢复动作必须按运行手册人工处理")
	}
}
