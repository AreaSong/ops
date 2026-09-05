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
		RequiresDualApproval: true, CreatedAt: now,
	}
	created, wasCreated, err := engine.store.CreateKubernetesPlan(ctx, plan, request.Manifest, store.HashConfirmation(phrase))
	if err != nil {
		return model.KubernetesPlan{}, false, err
	}
	if wasCreated {
		_, _ = engine.store.AppendAudit(ctx, model.AuditEntry{ActorHash: actor, Event: "kubernetes.plan.created", Resource: plan.ID, Outcome: "accepted", Detail: map[string]any{
			"tenantId": target.TenantID, "cluster": target.Cluster, "namespace": target.Namespace, "manifestDigest": digest,
		}})
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
	if !uuidPattern.MatchString(request.IdempotencyKey) {
		return model.KubernetesPlan{}, false, errors.New("Kubernetes 回滚计划幂等键无效")
	}
	if len(request.Manifest) == 0 || len(request.Manifest) > maxKubernetesManifestBytes || strings.ContainsRune(request.Manifest, '\x00') {
		return model.KubernetesPlan{}, false, errors.New("Kubernetes 回滚 manifest 为空、过大或包含非法字符")
	}
	source, err := engine.store.GetKubernetesPlan(ctx, sourcePlanID)
	if err != nil {
		return model.KubernetesPlan{}, false, err
	}
	if err := engine.authorizePlatform(ctx, actor, model.PermissionDeploy, "kubernetes:"+source.Target.Cluster); err != nil {
		return model.KubernetesPlan{}, false, err
	}
	if err := engine.authorizeKubernetesTenant(ctx, actor, source.Target); err != nil {
		return model.KubernetesPlan{}, false, err
	}
	if source.State != "succeeded" || source.OperationID == "" {
		return model.KubernetesPlan{}, false, errors.New("只有成功且具备操作记录的 Kubernetes 计划可以创建回滚")
	}
	sourceOperation, _, err := engine.store.GetKubernetesOperation(ctx, source.OperationID)
	if err != nil {
		return model.KubernetesPlan{}, false, errors.New("Kubernetes 回滚来源操作记录不可用")
	}
	if sourceOperation.State != "succeeded" || sourceOperation.ManifestDigest != source.ManifestDigest ||
		(sourceOperation.RolloutState != "succeeded" && sourceOperation.RolloutState != "not_required") ||
		sourceOperation.Target.Cluster != source.Target.Cluster ||
		sourceOperation.Target.Context != source.Target.Context ||
		sourceOperation.Target.Namespace != source.Target.Namespace ||
		sourceOperation.TenantID != source.TenantID {
		return model.KubernetesPlan{}, false, errors.New("Kubernetes 回滚来源身份或健康状态不一致")
	}
	target, err := engine.kubernetesTarget(source.Target)
	if err != nil {
		return model.KubernetesPlan{}, false, err
	}
	if _, err := validateKubernetesManifest(request.Manifest, target); err != nil {
		return model.KubernetesPlan{}, false, err
	}
	digest := digestText(request.Manifest)
	if digest == source.ManifestDigest {
		return model.KubernetesPlan{}, false, errors.New("Kubernetes 回滚 manifest 与当前计划相同")
	}
	requestDigest := digestText(strings.Join([]string{
		actor, sourcePlanID, source.ManifestDigest, target.Cluster, target.Context,
		target.Namespace, target.TenantID, digest,
	}, "\x00"))
	phrase := fmt.Sprintf("%s %s %s/%s %s", kubernetesRollbackConfirmationPrefix,
		sourcePlanID, target.Cluster, target.Namespace, digest[:minInt(22, len(digest))])
	plan := model.KubernetesPlan{
		ID: newPlanID(), IdempotencyKey: request.IdempotencyKey, RequestDigest: requestDigest,
		ActorHash: actor, TenantID: target.TenantID, Target: target, ManifestDigest: digest,
		Action: "rollback", State: "pending_approval", RollbackOfPlanID: sourcePlanID,
		SourceManifestDigest: source.ManifestDigest, ConfirmationPhrase: phrase,
		RequiresDualApproval: true, CreatedAt: time.Now().UTC(),
	}
	created, wasCreated, err := engine.store.CreateKubernetesPlan(
		ctx, plan, request.Manifest, store.HashConfirmation(phrase),
	)
	if err != nil {
		return model.KubernetesPlan{}, false, err
	}
	if wasCreated {
		_, _ = engine.store.AppendAudit(ctx, model.AuditEntry{
			ActorHash: actor, Event: "kubernetes.rollback_plan.created", Resource: plan.ID,
			Outcome: "accepted", Detail: map[string]any{
				"sourcePlanId": sourcePlanID, "sourceManifestDigest": source.ManifestDigest,
				"rollbackManifestDigest": digest, "tenantId": target.TenantID,
			},
		})
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
	_, _ = engine.store.AppendAudit(ctx, model.AuditEntry{ActorHash: actor, Event: "kubernetes.plan.approved", Resource: id, Outcome: "accepted", Detail: map[string]any{
		"second": updated.SecondApprovedByHash != "", "manifestDigest": updated.ManifestDigest,
	}})
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
	if plan.RequiresDualApproval && (plan.ApprovedByHash == "" || plan.SecondApprovedByHash == "" || actor == plan.ApprovedByHash || actor == plan.SecondApprovedByHash) {
		return model.KubernetesOperation{}, errors.New("Kubernetes 执行人必须独立于两名批准人")
	}
	if plan.ExecuteIdempotencyKey != "" && plan.ExecuteIdempotencyKey != request.IdempotencyKey {
		return model.KubernetesOperation{}, store.ErrIdempotency
	}
	if plan.State == "succeeded" || plan.State == "needs_attention" {
		return engine.kubernetesPlanOperation(ctx, plan)
	}
	executionKey := "plan-" + id
	if plan.State == "running" {
		op, _, getErr := engine.lookupKubernetesPlanOperation(ctx, plan, executionKey)
		if getErr != nil {
			if errors.Is(getErr, store.ErrNotFound) {
				reason := "Kubernetes 计划已启动但缺少操作记录，执行结果无法证明"
				_ = engine.store.FinishKubernetesPlan(context.Background(), id, "needs_attention", reason)
				return model.KubernetesOperation{}, errors.New(reason)
			}
			return model.KubernetesOperation{}, getErr
		}
		if op.State != "pending" {
			if op.State == "succeeded" {
				_ = engine.store.SetKubernetesPlanOperationID(context.Background(), id, op.ID)
				if err := engine.store.FinishKubernetesPlan(context.Background(), id, "succeeded", ""); err != nil {
					return op, err
				}
				return op, nil
			}
			if op.State == "running" {
				reason := "Runner 在 Kubernetes apply 执行中断，结果未知，禁止自动重试"
				engine.markKubernetesNeedsAttention(context.Background(), op.ID, "", reason)
				_ = engine.store.FinishKubernetesPlan(context.Background(), id, "needs_attention", reason)
				return op, errors.New(reason)
			}
			_ = engine.store.FinishKubernetesPlan(context.Background(), id, "needs_attention", op.Error)
			return op, fmt.Errorf("Kubernetes 操作已处于 %s: %s", op.State, op.Error)
		}
	}
	if digestText(manifest) != plan.ManifestDigest {
		_ = engine.store.FinishKubernetesPlan(ctx, id, "needs_attention", "持久化 manifest 摘要与计划不一致")
		return model.KubernetesOperation{}, errors.New("Kubernetes 计划 manifest 摘要不一致")
	}
	operationID := executionKey
	if _, started, startErr := engine.store.StartKubernetesPlan(ctx, id, operationID, actor, request.IdempotencyKey); startErr != nil {
		return model.KubernetesOperation{}, startErr
	} else if !started {
		plan, err = engine.store.GetKubernetesPlan(ctx, id)
		if err != nil || plan.OperationID == "" {
			return model.KubernetesOperation{}, err
		}
		if plan.OperationID == executionKey {
			op, _, err := engine.store.GetKubernetesOperationByIdempotency(ctx, executionKey)
			return op, err
		}
		op, _, err := engine.store.GetKubernetesOperation(ctx, plan.OperationID)
		return op, err
	}
	confirmation := "应用 Kubernetes 清单"
	if plan.Action == "rollback" {
		confirmation = "回滚 Kubernetes 清单"
	}
	op, _, runErr := engine.applyKubernetesPlan(ctx, actor, model.KubernetesRequest{
		Target: plan.Target, Action: plan.Action, Manifest: manifest,
		Confirmation: confirmation, IdempotencyKey: executionKey,
		RollbackOfPlanID: plan.RollbackOfPlanID,
	})
	if op.ID != "" {
		_ = engine.store.SetKubernetesPlanOperationID(context.Background(), id, op.ID)
	}
	if runErr != nil {
		state := "needs_attention"
		_ = engine.store.FinishKubernetesPlan(context.Background(), id, state, redactText(runErr.Error()))
		_, _ = engine.store.AppendAudit(context.Background(), model.AuditEntry{ActorHash: actor, Event: "kubernetes.plan.failed", Resource: id, Outcome: state, Detail: map[string]any{"operationId": op.ID, "error": redactText(runErr.Error())}})
		return op, runErr
	}
	if err := engine.store.FinishKubernetesPlan(context.Background(), id, "succeeded", ""); err != nil {
		return op, fmt.Errorf("Kubernetes 计划已执行但状态收口失败: %w", err)
	}
	_, _ = engine.store.AppendAudit(context.Background(), model.AuditEntry{ActorHash: actor, Event: "kubernetes.plan.executed", Resource: id, Outcome: "succeeded", Detail: map[string]any{"operationId": op.ID}})
	return op, nil
}

func (engine *Engine) verifyKubernetesRollbackSource(ctx context.Context, plan model.KubernetesPlan) error {
	if !uuidPattern.MatchString(plan.RollbackOfPlanID) || plan.SourceManifestDigest == "" {
		return errors.New("Kubernetes 回滚计划缺少来源身份")
	}
	source, err := engine.store.GetKubernetesPlan(ctx, plan.RollbackOfPlanID)
	if err != nil {
		return errors.New("Kubernetes 回滚来源计划不可用")
	}
	if source.State != "succeeded" || source.ManifestDigest != plan.SourceManifestDigest ||
		source.TenantID != plan.TenantID || source.Target.Cluster != plan.Target.Cluster ||
		source.Target.Context != plan.Target.Context || source.Target.Namespace != plan.Target.Namespace {
		return errors.New("Kubernetes 回滚来源计划身份已变化")
	}
	return nil
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
