package runner

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	"github.com/AreaSong/ops/services/areasong-ops/internal/store"
)

func manualPlanRequestDigest(actor string, r model.PreviewRequest) string {
	raw, _ := json.Marshal(struct {
		Actor, Service, Action, Target                                                                                                                            string
		ScheduleAt                                                                                                                                                *time.Time
		RestoreMode, RecoveryPointID, RestoreTenantID, RestoreServerID, RestoreExpectedBeforeDigest, RestoreContractDigest, RestoreEvidenceDigest, ProvidedDigest string
		RequiresDualApproval                                                                                                                                      bool
		AutoUpdatePolicy                                                                                                                                          *model.AutoUpdatePolicy
	}{actor, r.Service, r.Action, r.Target, r.ScheduleAt, r.RestoreMode, r.RecoveryPointID, r.RestoreTenantID, r.RestoreServerID, r.RestoreExpectedBeforeDigest, r.RestoreContractDigest, r.RestoreEvidenceDigest, r.RequestDigest, r.RequiresDualApproval, r.AutoUpdatePolicy})
	return model.WorkDigest(string(raw))
}

func preparationMutation(r model.PlanPreparation, owner string) store.PlanPreparationMutation {
	return store.PlanPreparationMutation{ID: r.ID, PlanID: r.Request.PlanID, IdempotencyKey: r.Request.IdempotencyKey, ActorHash: r.Request.Authority.ActorHash,
		RequestDigest: r.RequestDigest, ScopeDigest: r.Request.Binding.ScopeDigest, OwnerToken: owner, ExpectedState: r.State, ExpectedRevision: r.Revision}
}

func (engine *Engine) createManualReleasePlan(ctx context.Context, actor string, request model.PreviewRequest) (model.ReleasePlan, error) {
	// 持久化时间使用 UTC；相同瞬间的时区写法不能破坏重放或批准摘要绑定。
	if request.ScheduleAt != nil {
		at := request.ScheduleAt.UTC()
		request.ScheduleAt = &at
	}

	if !actorPattern.MatchString(actor) || !uuidPattern.MatchString(request.IdempotencyKey) {
		return model.ReleasePlan{}, model.ErrReleaseLifecycle
	}
	if plan, found, err := engine.replayPlanCreation(ctx, actor, request); err != nil || found {
		return plan, err
	}
	if request.Action != "update" || request.RestoreMode != "" || request.RecoveryPointID != "" || request.AutoUpdatePolicy != nil || request.RequiresDualApproval {
		return model.ReleasePlan{}, model.ErrReleaseNotIntegrated
	}
	service, action, err := engine.resolveAction(request.Service, request.Action, request.Target)
	if err != nil {
		return model.ReleasePlan{}, err
	}
	if _, ok := engine.catalog.Services[service.Name]; !ok || (action.TargetMode != "signed_release_tag" && action.TargetMode != "allowlist") {
		return model.ReleasePlan{}, model.ErrReleaseNotIntegrated
	}
	authority, err := engine.releaseAuthority(ctx, actor, service.ObjectID)
	if err != nil {
		return model.ReleasePlan{}, err
	}
	objects, scopeDigest, err := engine.resolveReleaseScope(ctx, service, action, releaseScopeInput{target: request.Target})
	if err != nil {
		return model.ReleasePlan{}, err
	}
	for _, object := range objects {
		if err := engine.authorize(ctx, actor, permissionForAction("update"), object.ObjectID); err != nil {
			return model.ReleasePlan{}, err
		}
	}
	targetEvidence, err := engine.targetEvidence(ctx, service.Name, action.TargetMode, request.Target)
	if err != nil {
		return model.ReleasePlan{}, err
	}
	if _, found, err := engine.store.ActiveTask(ctx, service.Name); err != nil {
		return model.ReleasePlan{}, err
	} else if found {
		return model.ReleasePlan{}, errors.New("服务已有活动任务")
	}
	if existing, found, e := engine.store.GetPlanPreparationByRequest(ctx, request.IdempotencyKey); e != nil {
		return model.ReleasePlan{}, e
	} else if found {
		if existing.Request.Authority.ActorHash != actor || existing.Request.InputDigest != manualPlanRequestDigest(actor, request) {
			return model.ReleasePlan{}, store.ErrIdempotency
		}
		return model.ReleasePlan{}, model.ErrReleaseNotIntegrated
	}
	preparationID, err := newUUID()
	if err != nil {
		return model.ReleasePlan{}, err
	}
	planID, err := newUUID()
	if err != nil {
		return model.ReleasePlan{}, err
	}
	owner, err := model.NewWorkOwnerToken()
	if err != nil {
		return model.ReleasePlan{}, err
	}
	binding, err := engine.store.CaptureReleaseLifecycle(ctx, authority, model.ReleaseLifecycleBinding{Version: 1, Source: "manual_single", ExecutionMode: "local",
		PreparationID: preparationID, CreatorTenantID: authority.TenantID, TargetObjects: objects, ScopeDigest: scopeDigest})
	if err != nil {
		return model.ReleasePlan{}, err
	}
	input := store.PlanPreparationInput{ID: preparationID, OwnerToken: owner, Request: model.PlanPreparationRequest{Version: 1, Kind: model.PlanPreparationKind, PlanID: planID,
		IdempotencyKey: request.IdempotencyKey, InputDigest: manualPlanRequestDigest(actor, request), Authority: authority, Service: service.Name, Action: action.Name, Target: request.Target, ScheduleAt: request.ScheduleAt, Binding: binding}}
	record, created, err := engine.store.AdmitPlanPreparation(ctx, input)
	if err != nil {
		return model.ReleasePlan{}, err
	}
	if !created {
		return model.ReleasePlan{}, model.ErrReleaseNotIntegrated
	}
	record, advanced, err := engine.store.BeginPlanInspection(ctx, preparationMutation(record, owner))
	if err != nil || !advanced {
		return model.ReleasePlan{}, errors.Join(model.ErrReleaseNotIntegrated, err)
	}
	return engine.finishManualPlan(ctx, record, owner, service, action, targetEvidence)
}

func (engine *Engine) finishManualPlan(ctx context.Context, record model.PlanPreparation, owner string, service model.ServiceDefinition, action model.ActionDefinition, evidence map[string]any) (model.ReleasePlan, error) {
	if err := engine.verifyReleaseScope(ctx, service, action, record.Request.Binding, releaseScopeInput{target: record.Request.Target}); err != nil {
		_, markErr := engine.store.MarkPlanPreparationUncertain(context.Background(), preparationMutation(record, owner))
		return model.ReleasePlan{}, errors.Join(err, markErr)
	}
	result, err := engine.inspectPreparedPlan(ctx, record, service)
	if err != nil {
		_, markErr := engine.store.MarkPlanPreparationUncertain(context.Background(), preparationMutation(record, owner))
		return model.ReleasePlan{}, errors.Join(err, markErr)
	}
	record, err = engine.store.RecordPlanInspectionDone(ctx, preparationMutation(record, owner), result)
	if err != nil {
		return model.ReleasePlan{}, err
	}
	if !result.Succeeded {
		_, err = engine.store.FinishPreparedReleasePlan(ctx, preparationMutation(record, owner), nil)
		return model.ReleasePlan{}, errors.Join(errors.New(result.Failure), err)
	}
	if err = engine.verifyReleaseScope(ctx, service, action, record.Request.Binding, releaseScopeInput{target: record.Request.Target}); err != nil {
		_, markErr := engine.store.MarkPlanPreparationUncertain(context.Background(), preparationMutation(record, owner))
		return model.ReleasePlan{}, errors.Join(err, markErr)
	}
	plan, err := preparedReleasePlan(record, service, action, evidence)
	if err != nil {
		return model.ReleasePlan{}, err
	}
	_, err = engine.store.FinishPreparedReleasePlan(ctx, preparationMutation(record, owner), &store.ReleasePlanInput{Plan: plan, ConfirmationHash: store.HashConfirmation(plan.ConfirmationPhrase)})
	return plan, err
}

func (engine *Engine) inspectPreparedPlan(ctx context.Context, record model.PlanPreparation, service model.ServiceDefinition, contexts ...*releaseCallContext) (model.PlanInspectionResult, error) {
	callContext := preparationCallContext(record, contexts)
	result := model.PlanInspectionResult{Kind: "inspection_settled_v1", ScopeDigest: record.Request.Binding.ScopeDigest, CallDigests: []string{}}
	directory, err := os.MkdirTemp(engine.stateRoot, ".plan-inspection-")
	if err != nil {
		return result, err
	}
	profile := engine.executor.(planInspectionExecutor)
	checkCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	kinds := []string{adapterKindService}
	if service.TrafficPolicy != nil {
		kinds = append(kinds, adapterKindTraffic)
	}
	observed := make(map[string]any)
	for _, kind := range kinds {
		if err := checkCtx.Err(); err != nil {
			return result, err
		}
		call := profile.executePlanInspection(checkCtx, ExecuteInput{Service: service, Action: "inspect", Phase: "inspect", OperationDir: directory, AdapterKind: kind, releaseContext: callContext})
		if call.Settled == nil {
			return result, errors.New("创建检查未提供停止证明")
		}
		select {
		case <-call.Settled:
		case <-checkCtx.Done():
			return result, errors.New("创建检查结果不确定")
		}
		if !model.ValidWorkDigest(call.EvidenceDigest) {
			return result, model.ErrReleaseLifecycle
		}
		result.CallDigests = append(result.CallDigests, call.EvidenceDigest)
		if call.Err != nil {
			result.Failure = redactText(call.Err.Error())
			break
		}
		if !call.Result.OK || strings.TrimSpace(call.Result.Summary) == "" {
			result.Failure = "创建检查返回失败或无效结果"
			break
		}
		if kind == adapterKindService {
			observed = cloneAnyMap(call.Result.Data)
			if observed == nil {
				observed = map[string]any{}
			}
			observed["application"] = cloneAnyMap(call.Result.Data)
		} else {
			observed["traffic"] = cloneAnyMap(call.Result.Data)
			for _, key := range []string{"trafficState", "includeDigest", "hostname", "drainTimeoutSeconds"} {
				if value, ok := call.Result.Data[key]; ok {
					observed[key] = value
				}
			}
		}
	}
	cleanup := engine.inspectionCleanup
	if cleanup == nil {
		cleanup = os.RemoveAll
	}
	if err = cleanup(directory); err != nil {
		return result, err
	}
	if _, err = os.Lstat(directory); !os.IsNotExist(err) {
		return result, errors.New("创建检查临时目录清理未确认")
	}
	result.CleanupDigest = model.WorkDigest(record.ID + "\x00" + filepath.Base(directory) + "\x00removed")
	result.Succeeded = result.Failure == ""
	result.Snapshot = approvalSnapshot(service, redactValue(observed).(map[string]any))
	return result, nil
}

func preparedReleasePlan(record model.PlanPreparation, service model.ServiceDefinition, action model.ActionDefinition, evidence map[string]any) (model.ReleasePlan, error) {
	r := record.Request
	var result model.PlanInspectionResult
	if err := json.Unmarshal([]byte(record.ResultJSON), &result); err != nil {
		return model.ReleasePlan{}, err
	}
	dual := requiredDualApproval(service, service.TenantID, action, false)
	policy := approvalPolicyFor(service, action, dual)
	phrase := renderConfirmation(action.ConfirmationTemplate, service.Name, r.Target)
	summary := model.ApprovalSummary{SchemaVersion: 2, ApprovalPolicy: policy, Service: service.Name, Action: action.Name, Target: r.Target,
		TrafficPolicyDigest: service.PolicyDigest(), TenantID: service.TenantID, ServerID: service.ServerID, ScheduleAt: r.ScheduleAt,
		Risk: action.Risk, Impact: action.Impact, Rollback: action.Rollback, Scope: action.Scope, Steps: append([]string(nil), action.Steps...),
		PhaseSemantics: resolvedPhaseSemantics(action), ObservationSeconds: action.ObservationSeconds, TimeoutSeconds: action.TimeoutSeconds,
		AlertPolicy: actionAlertPolicy(service), ConfirmationPhrase: phrase, ExpectedBefore: result.Snapshot, TargetEvidence: evidence, Lifecycle: &r.Binding}
	digest, err := model.ReleaseApprovalDigest(summary)
	if err != nil {
		return model.ReleasePlan{}, err
	}
	now := time.Now().UTC()
	return model.ReleasePlan{ID: r.PlanID, ActorHash: r.Authority.ActorHash, Service: service.Name, Action: action.Name, Target: r.Target,
		TenantID: service.TenantID, ServerID: service.ServerID, ScheduleAt: r.ScheduleAt, Risk: action.Risk, State: model.PlanPendingApproval, Digest: digest,
		ApprovalSummary: summary, ApprovalPolicy: policy, RequiresConfirmation: action.Risk != model.RiskReadOnly, ConfirmationPhrase: phrase, RequiresDualApproval: dual,
		RequestIdempotencyKey: r.IdempotencyKey, RequestDigest: r.InputDigest, ObservationSeconds: action.ObservationSeconds, CreatedAt: now, UpdatedAt: now}, nil
}
