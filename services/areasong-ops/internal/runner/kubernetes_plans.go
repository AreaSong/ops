package runner

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	"github.com/AreaSong/ops/services/areasong-ops/internal/store"
)

const kubernetesApplyConfirmationPrefix = "批准 Kubernetes 变更"

const kubernetesRollbackConfirmationPrefix = "批准 Kubernetes 回滚"

func (engine *Engine) CreateKubernetesPlan(
	ctx context.Context,
	actor string,
	request model.KubernetesPlanRequest,
) (model.KubernetesPlan, bool, error) {
	if !actorPattern.MatchString(actor) {
		return model.KubernetesPlan{}, false, errors.New("操作者标识无效")
	}
	if !uuidPattern.MatchString(request.IdempotencyKey) {
		return model.KubernetesPlan{}, false, errors.New("Kubernetes 计划幂等键无效")
	}
	if len(request.Manifest) == 0 || len(request.Manifest) > maxKubernetesManifestBytes || strings.ContainsRune(request.Manifest, '\x00') {
		return model.KubernetesPlan{}, false, errors.New("Kubernetes manifest 为空、过大或包含非法字符")
	}
	if err := engine.authorizePlatform(ctx, actor, model.PermissionDeploy, "kubernetes:"+request.Target.Cluster); err != nil {
		return model.KubernetesPlan{}, false, err
	}
	target, err := engine.kubernetesTarget(request.Target)
	if err != nil {
		return model.KubernetesPlan{}, false, err
	}
	if err := engine.authorizeKubernetesTenant(ctx, actor, target); err != nil {
		return model.KubernetesPlan{}, false, err
	}
	if _, err := validateKubernetesManifest(request.Manifest, target); err != nil {
		return model.KubernetesPlan{}, false, err
	}
	digest := digestText(request.Manifest)
	requestDigest := digestText(strings.Join([]string{actor, target.Cluster, target.Context, target.Namespace, target.TenantID, digest}, "\x00"))
	now := time.Now().UTC()
	phrase := fmt.Sprintf("%s %s/%s %s", kubernetesApplyConfirmationPrefix, target.Cluster, target.Namespace, digest[:minInt(22, len(digest))])
	plan := model.KubernetesPlan{
		ID: newPlanID(), IdempotencyKey: request.IdempotencyKey, RequestDigest: requestDigest,
		ActorHash: actor, TenantID: target.TenantID, Target: target, ManifestDigest: digest,
		Action: "apply", State: "pending_approval", ConfirmationPhrase: phrase,
		RequiresDualApproval: true, ApprovalPolicy: model.ApprovalPolicyTwoParty, CreatedAt: now,
	}
	created, wasCreated, err := engine.store.CreateKubernetesPlan(ctx, plan, request.Manifest, store.HashConfirmation(phrase))
	if err != nil {
		return model.KubernetesPlan{}, false, err
	}
	return created, wasCreated, nil
}

func (engine *Engine) CreateKubernetesRollbackPlan(
	ctx context.Context,
	actor, sourcePlanID string,
	request model.KubernetesRollbackPlanRequest,
) (model.KubernetesPlan, bool, error) {
	if !actorPattern.MatchString(actor) || !uuidPattern.MatchString(sourcePlanID) {
		return model.KubernetesPlan{}, false, errors.New("Kubernetes 回滚身份无效")
	}
	if !uuidPattern.MatchString(request.IdempotencyKey) || !uuidPattern.MatchString(request.RollbackToPlanID) {
		return model.KubernetesPlan{}, false, errors.New("Kubernetes 回滚计划幂等键无效")
	}
	source, err := engine.successfulKubernetesPlan(ctx, sourcePlanID)
	if err != nil {
		return model.KubernetesPlan{}, false, err
	}
	if err := engine.authorizePlatform(ctx, actor, model.PermissionDeploy, "kubernetes:"+source.Target.Cluster); err != nil {
		return model.KubernetesPlan{}, false, err
	}
	if err := engine.authorizeKubernetesTenant(ctx, actor, source.Target); err != nil {
		return model.KubernetesPlan{}, false, err
	}
	if source.Action != "apply" {
		return model.KubernetesPlan{}, false, errors.New("只有成功的 Kubernetes apply 计划可以作为回滚来源")
	}
	target, err := engine.kubernetesTarget(source.Target)
	if err != nil {
		return model.KubernetesPlan{}, false, err
	}
	rollbackTarget, manifest, err := engine.store.GetKubernetesPlanWithManifest(ctx, request.RollbackToPlanID)
	if err != nil {
		return model.KubernetesPlan{}, false, errors.New("Kubernetes 历史回滚目标不可用")
	}
	verifiedTarget, err := engine.successfulKubernetesPlan(ctx, rollbackTarget.ID)
	if err != nil || verifiedTarget.Action != "apply" || !verifiedTarget.CreatedAt.Before(source.CreatedAt) ||
		!sameKubernetesPlanTarget(source, verifiedTarget) || digestText(manifest) != verifiedTarget.ManifestDigest {
		return model.KubernetesPlan{}, false, errors.New("Kubernetes 历史回滚目标身份、顺序或健康状态无效")
	}
	if _, err := validateKubernetesManifest(manifest, target); err != nil {
		return model.KubernetesPlan{}, false, err
	}
	digest := digestText(manifest)
	if digest == source.ManifestDigest {
		return model.KubernetesPlan{}, false, errors.New("Kubernetes 回滚 manifest 与当前计划相同")
	}
	requestDigest := digestText(strings.Join([]string{
		actor, sourcePlanID, request.RollbackToPlanID, source.ManifestDigest,
		target.Cluster, target.Context, target.Namespace, target.TenantID, digest,
	}, "\x00"))
	phrase := fmt.Sprintf("%s %s %s/%s %s", kubernetesRollbackConfirmationPrefix,
		sourcePlanID, target.Cluster, target.Namespace, digest[:minInt(22, len(digest))])
	plan := model.KubernetesPlan{
		ID: newPlanID(), IdempotencyKey: request.IdempotencyKey, RequestDigest: requestDigest,
		ActorHash: actor, TenantID: target.TenantID, Target: target, ManifestDigest: digest,
		Action: "rollback", State: "pending_approval", RollbackOfPlanID: sourcePlanID,
		RollbackTargetPlanID: request.RollbackToPlanID, SourceManifestDigest: source.ManifestDigest, ConfirmationPhrase: phrase,
		RequiresDualApproval: true, ApprovalPolicy: model.ApprovalPolicyTwoParty, CreatedAt: time.Now().UTC(),
	}
	created, wasCreated, err := engine.store.CreateKubernetesPlan(
		ctx, plan, manifest, store.HashConfirmation(phrase),
	)
	if err != nil {
		return model.KubernetesPlan{}, false, err
	}
	return created, wasCreated, nil
}

func (engine *Engine) KubernetesPlan(ctx context.Context, actor, id string) (model.KubernetesPlan, error) {
	if !actorPattern.MatchString(actor) || !uuidPattern.MatchString(id) {
		return model.KubernetesPlan{}, errors.New("Kubernetes 计划标识无效")
	}
	plan, err := engine.store.GetKubernetesPlan(ctx, id)
	if err != nil {
		return model.KubernetesPlan{}, err
	}
	if err := engine.authorizePlatform(ctx, actor, model.PermissionDeploy, "kubernetes:"+plan.Target.Cluster); err != nil {
		return model.KubernetesPlan{}, err
	}
	if err := engine.authorizeKubernetesTenant(ctx, actor, plan.Target); err != nil {
		return model.KubernetesPlan{}, err
	}
	return plan, nil
}

func (engine *Engine) KubernetesPlans(ctx context.Context, actor string, limit int) ([]model.KubernetesPlan, error) {
	if !actorPattern.MatchString(actor) {
		return nil, errors.New("操作者标识无效")
	}
	if err := engine.authorizePlatform(ctx, actor, model.PermissionDeploy, "kubernetes:plans"); err != nil {
		return nil, err
	}
	policy, _, err := engine.effectiveAccessPolicy(ctx)
	if err != nil {
		return nil, authorizationError{message: "访问策略快照不可用"}
	}
	tenant := accessPolicyActorTenant(policy, actor)
	if tenant == "" {
		tenant = "default"
	}
	return engine.store.ListKubernetesPlans(ctx, tenant, limit)
}

func (engine *Engine) ApproveKubernetesPlan(
	ctx context.Context,
	actor, id string,
	request model.KubernetesPlanApprovalRequest,
) (model.KubernetesPlan, error) {
	if !actorPattern.MatchString(actor) || !uuidPattern.MatchString(id) || request.Digest == "" || request.Confirmation == "" {
		return model.KubernetesPlan{}, errors.New("Kubernetes 批准请求无效")
	}
	plan, err := engine.store.GetKubernetesPlan(ctx, id)
	if err != nil {
		return model.KubernetesPlan{}, err
	}
	if err := engine.authorizePlatform(ctx, actor, model.PermissionDeploy, "kubernetes:"+plan.Target.Cluster); err != nil {
		return model.KubernetesPlan{}, err
	}
	if err := engine.authorizeKubernetesTenant(ctx, actor, plan.Target); err != nil {
		return model.KubernetesPlan{}, err
	}
	updated, err := engine.store.ApproveKubernetesPlan(ctx, id, actor, request.Digest, request.Confirmation)
	if err != nil {
		return model.KubernetesPlan{}, err
	}
	return updated, nil
}

func (engine *Engine) ExecuteKubernetesPlan(
	ctx context.Context,
	actor, id string,
	request model.KubernetesPlanExecuteRequest,
) (model.KubernetesOperation, error) {
	if !actorPattern.MatchString(actor) || !uuidPattern.MatchString(id) || !uuidPattern.MatchString(request.IdempotencyKey) {
		return model.KubernetesOperation{}, errors.New("Kubernetes 执行请求标识无效")
	}
	plan, manifest, err := engine.store.GetKubernetesPlanWithManifest(ctx, id)
	if err != nil {
		return model.KubernetesOperation{}, err
	}
	if err := engine.authorizePlatform(ctx, actor, model.PermissionDeploy, "kubernetes:"+plan.Target.Cluster); err != nil {
		return model.KubernetesOperation{}, err
	}
	if err := engine.authorizeKubernetesTenant(ctx, actor, plan.Target); err != nil {
		return model.KubernetesOperation{}, err
	}
	if plan.State != "approved" && plan.State != "running" && plan.State != "succeeded" && plan.State != "needs_attention" {
		return model.KubernetesOperation{}, errors.New("Kubernetes 计划尚未完成批准")
	}
	if plan.Action != "apply" && plan.Action != "rollback" {
		return model.KubernetesOperation{}, errors.New("Kubernetes 计划动作无效")
	}
	if plan.Action == "rollback" {
		if err := engine.verifyKubernetesRollbackSource(ctx, plan); err != nil {
			return model.KubernetesOperation{}, err
		}
	}
	if plan.RequiresDualApproval {
		if model.UsesTwoPartyApproval(plan.ApprovalPolicy) {
			if actor != plan.ActorHash || plan.ApprovedByHash == "" || plan.ApprovedByHash == actor {
				return model.KubernetesOperation{}, errors.New("Kubernetes 执行必须由创建人完成，且批准人必须独立")
			}
		} else if !model.IndependentExecutor(actor, plan.ActorHash, plan.ApprovedByHash, plan.SecondApprovedByHash) {
			return model.KubernetesOperation{}, errors.New("Kubernetes 执行人必须独立于创建人和两名批准人")
		}
	}
	if plan.ExecutedByHash != "" && plan.ExecutedByHash != actor {
		return model.KubernetesOperation{}, store.ErrActorMismatch
	}
	if plan.ExecuteIdempotencyKey != "" && plan.ExecuteIdempotencyKey != request.IdempotencyKey {
		return model.KubernetesOperation{}, store.ErrIdempotency
	}
	if digestText(manifest) != plan.ManifestDigest {
		return engine.rejectKubernetesManifestDrift(ctx, plan)
	}
	if plan.State == "succeeded" || plan.State == "needs_attention" {
		return engine.kubernetesPlanOperation(ctx, plan)
	}
	executionKey := "plan-" + id
	if plan.State == "running" {
		op, resumable, err := engine.reconcileKubernetesRunningPlan(plan, executionKey)
		if err != nil || !resumable {
			return op, err
		}
	}
	operationRequest := model.KubernetesRequest{
		Target: plan.Target, Action: plan.Action, Manifest: manifest,
		IdempotencyKey: executionKey, RollbackOfPlanID: plan.RollbackOfPlanID,
	}
	operation, err := prepareKubernetesPlanOperation(operationRequest, executionKey)
	if err != nil {
		return model.KubernetesOperation{}, err
	}
	if _, _, startErr := engine.store.StartKubernetesPlan(ctx, id, actor, request.IdempotencyKey, operation); startErr != nil {
		return model.KubernetesOperation{}, startErr
	}
	confirmation := "应用 Kubernetes 清单"
	if plan.Action == "rollback" {
		confirmation = "回滚 Kubernetes 清单"
	}
	operationRequest.Confirmation = confirmation
	op, _, runErr := engine.applyKubernetesPlan(ctx, actor, operationRequest, executionKey)
	if runErr != nil {
		if finishErr := engine.store.FinishKubernetesPlan(context.Background(), id, "needs_attention", redactText(runErr.Error())); finishErr != nil {
			return op, fmt.Errorf("%w；Kubernetes 计划失败状态收口失败: %v", runErr, finishErr)
		}
		return op, runErr
	}
	if err := engine.store.FinishKubernetesPlan(context.Background(), id, "succeeded", ""); err != nil {
		return op, fmt.Errorf("Kubernetes 计划已执行但状态收口失败: %w", err)
	}
	return op, nil
}

func prepareKubernetesPlanOperation(
	request model.KubernetesRequest,
	operationID string,
) (model.KubernetesOperation, error) {
	objects, err := validateKubernetesManifest(request.Manifest, request.Target)
	if err != nil {
		return model.KubernetesOperation{}, err
	}
	return model.KubernetesOperation{
		ID: operationID, IdempotencyKey: operationID,
		RequestDigest: kubernetesRequestDigest(request, request.Target),
		Target:        request.Target, TenantID: request.Target.TenantID, Action: request.Action,
		ManifestDigest: digestText(request.Manifest), State: "pending", Phase: "preflight",
		RolloutState: "pending", RolloutResources: kubernetesRolloutResources(objects),
		RollbackOfPlanID: request.RollbackOfPlanID,
	}, nil
}

func (engine *Engine) rejectKubernetesManifestDrift(
	ctx context.Context,
	plan model.KubernetesPlan,
) (model.KubernetesOperation, error) {
	reason := "Kubernetes 计划 manifest 摘要不一致"
	if plan.State == "running" {
		if err := engine.store.FinishKubernetesPlan(ctx, plan.ID, "needs_attention", reason); err != nil {
			return model.KubernetesOperation{}, fmt.Errorf("%s；状态收口失败: %w", reason, err)
		}
	}
	return model.KubernetesOperation{}, errors.New(reason)
}

func (engine *Engine) reconcileKubernetesRunningPlan(
	plan model.KubernetesPlan,
	executionKey string,
) (model.KubernetesOperation, bool, error) {
	ctx := context.Background()
	op, _, err := engine.lookupKubernetesPlanOperation(ctx, plan, executionKey)
	if errors.Is(err, store.ErrNotFound) {
		reason := "Kubernetes 计划已启动但缺少操作记录，执行结果无法证明"
		finishErr := engine.store.FinishKubernetesPlan(ctx, plan.ID, "needs_attention", reason)
		if finishErr != nil {
			return op, false, fmt.Errorf("%s；状态收口失败: %w", reason, finishErr)
		}
		return op, false, errors.New(reason)
	}
	if err != nil {
		return op, false, err
	}
	switch op.State {
	case "pending":
		return op, true, nil
	case "succeeded":
		return op, false, engine.store.FinishKubernetesPlan(ctx, plan.ID, "succeeded", "")
	case "running":
		return engine.closeInterruptedKubernetesPlan(ctx, plan, op)
	default:
		reason := fmt.Sprintf("Kubernetes 操作已处于 %s: %s", op.State, op.Error)
		if finishErr := engine.store.FinishKubernetesPlan(ctx, plan.ID, "needs_attention", op.Error); finishErr != nil {
			return op, false, fmt.Errorf("%s；计划状态收口失败: %w", reason, finishErr)
		}
		return op, false, errors.New(reason)
	}
}

func (engine *Engine) closeInterruptedKubernetesPlan(
	ctx context.Context,
	plan model.KubernetesPlan,
	op model.KubernetesOperation,
) (model.KubernetesOperation, bool, error) {
	reason := "Runner 在 Kubernetes apply 执行中断，结果未知，禁止自动重试"
	if err := engine.markKubernetesNeedsAttention(ctx, op.ID, "", reason); err != nil {
		return op, false, fmt.Errorf("%s；操作状态收口失败: %w", reason, err)
	}
	if err := engine.store.FinishKubernetesPlan(ctx, plan.ID, "needs_attention", reason); err != nil {
		return op, false, fmt.Errorf("%s；计划状态收口失败: %w", reason, err)
	}
	op.State, op.Error = "needs_attention", reason
	return op, false, errors.New(reason)
}

func (engine *Engine) verifyKubernetesRollbackSource(ctx context.Context, plan model.KubernetesPlan) error {
	if !uuidPattern.MatchString(plan.RollbackOfPlanID) || !uuidPattern.MatchString(plan.RollbackTargetPlanID) || plan.SourceManifestDigest == "" {
		return errors.New("Kubernetes 回滚计划缺少来源身份")
	}
	source, err := engine.successfulKubernetesPlan(ctx, plan.RollbackOfPlanID)
	if err != nil {
		return errors.New("Kubernetes 回滚来源计划不可用")
	}
	target, manifest, err := engine.store.GetKubernetesPlanWithManifest(ctx, plan.RollbackTargetPlanID)
	if err != nil {
		return errors.New("Kubernetes 历史回滚目标不可用")
	}
	verifiedTarget, err := engine.successfulKubernetesPlan(ctx, target.ID)
	if err != nil || source.Action != "apply" || verifiedTarget.Action != "apply" ||
		source.ManifestDigest != plan.SourceManifestDigest || !verifiedTarget.CreatedAt.Before(source.CreatedAt) ||
		!sameKubernetesPlanTarget(source, plan) || !sameKubernetesPlanTarget(source, verifiedTarget) ||
		verifiedTarget.ManifestDigest != plan.ManifestDigest || digestText(manifest) != plan.ManifestDigest {
		return errors.New("Kubernetes 回滚来源计划身份已变化")
	}
	return nil
}

func (engine *Engine) successfulKubernetesPlan(ctx context.Context, id string) (model.KubernetesPlan, error) {
	plan, err := engine.store.GetKubernetesPlan(ctx, id)
	if err != nil || plan.State != "succeeded" || plan.OperationID == "" {
		return model.KubernetesPlan{}, errors.New("Kubernetes 计划没有可证明的成功操作")
	}
	operation, _, err := engine.store.GetKubernetesOperation(ctx, plan.OperationID)
	if err != nil || operation.State != "succeeded" || operation.ManifestDigest != plan.ManifestDigest ||
		(operation.RolloutState != "succeeded" && operation.RolloutState != "not_required") ||
		operation.TenantID != plan.TenantID || operation.Target.Cluster != plan.Target.Cluster ||
		operation.Target.Context != plan.Target.Context || operation.Target.Namespace != plan.Target.Namespace {
		return model.KubernetesPlan{}, errors.New("Kubernetes 计划操作身份或健康状态不一致")
	}
	return plan, nil
}

func sameKubernetesPlanTarget(left, right model.KubernetesPlan) bool {
	return left.TenantID == right.TenantID && left.Target.Cluster == right.Target.Cluster &&
		left.Target.Context == right.Target.Context && left.Target.Namespace == right.Target.Namespace
}

func (engine *Engine) kubernetesPlanOperation(
	ctx context.Context,
	plan model.KubernetesPlan,
) (model.KubernetesOperation, error) {
	op, _, err := engine.lookupKubernetesPlanOperation(ctx, plan, "plan-"+plan.ID)
	if errors.Is(err, store.ErrNotFound) {
		return model.KubernetesOperation{}, errors.New("Kubernetes 计划缺少操作记录")
	}
	return op, err
}

func (engine *Engine) lookupKubernetesPlanOperation(
	ctx context.Context,
	plan model.KubernetesPlan,
	executionKey string,
) (model.KubernetesOperation, string, error) {
	if plan.OperationID != "" && plan.OperationID != executionKey {
		return engine.store.GetKubernetesOperation(ctx, plan.OperationID)
	}
	return engine.store.GetKubernetesOperationByIdempotency(ctx, executionKey)
}

func newPlanID() string {
	id, err := newUUID()
	if err != nil {
		return fmt.Sprintf("plan-%d", time.Now().UnixNano())
	}
	return id
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}
