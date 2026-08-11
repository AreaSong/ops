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

func (engine *Engine) CreateReleasePlan(
	ctx context.Context,
	actorHash string,
	request model.PreviewRequest,
) (model.ReleasePlan, error) {
	if !actorPattern.MatchString(actorHash) {
		return model.ReleasePlan{}, errors.New("操作者标识无效")
	}
	service, action, err := engine.resolveAction(request.Service, request.Action, request.Target)
	if err != nil {
		return model.ReleasePlan{}, err
	}
	if active, found, err := engine.store.ActiveTask(ctx, service.Name); err != nil {
		return model.ReleasePlan{}, err
	} else if found {
		return model.ReleasePlan{}, fmt.Errorf("服务已有活动任务: %s", active.ID)
	}
	snapshot, err := engine.inspect(ctx, service)
	if err != nil {
		return model.ReleasePlan{}, fmt.Errorf("创建计划前检查失败: %w", err)
	}
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
		Risk: action.Risk, Impact: action.Impact, Rollback: action.Rollback,
		Scope: action.Scope, Steps: append([]string(nil), action.Steps...),
		PhaseSemantics: action.PhaseSemantics, ConfirmationPhrase: phrase,
		ExpectedBefore: snapshot, TargetEvidence: targetEvidence,
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
		Digest: digest, ApprovalSummary: summary, ConfirmationPhrase: phrase,
		RequiresConfirmation: action.Risk != model.RiskReadOnly, CreatedAt: now, UpdatedAt: now,
	}
	if err := engine.store.CreateReleasePlan(ctx, store.ReleasePlanInput{
		Plan: plan, ConfirmationHash: store.HashConfirmation(phrase),
	}); err != nil {
		return model.ReleasePlan{}, err
	}
	_, _ = engine.store.AppendAudit(ctx, model.AuditEntry{
		ActorHash: actorHash, Event: "plan.created", Resource: plan.ID, Outcome: "accepted",
		Detail: map[string]any{"service": plan.Service, "action": plan.Action, "target": plan.Target, "digest": plan.Digest},
	})
	return plan, nil
}

func (engine *Engine) ApproveReleasePlan(
	ctx context.Context,
	actorHash, id string,
	request model.ApprovePlanRequest,
) (model.ReleasePlan, error) {
	if !actorPattern.MatchString(actorHash) || !uuidPattern.MatchString(id) {
		return model.ReleasePlan{}, errors.New("发布计划请求标识无效")
	}
	plan, err := engine.store.ApproveReleasePlan(ctx, id, actorHash, request.Digest, request.Confirmation)
	if err != nil {
		return model.ReleasePlan{}, err
	}
	_, _ = engine.store.AppendAudit(ctx, model.AuditEntry{
		ActorHash: actorHash, Event: "plan.approved", Resource: plan.ID, Outcome: "approved",
		Detail: map[string]any{"digest": plan.Digest},
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
	if plan.State == model.PlanExecuting || plan.State == model.PlanCompleted {
		task, taskErr := engine.store.GetTask(ctx, plan.TaskID)
		if taskErr == nil && task.ActorHash == actorHash && task.PlanID == plan.ID &&
			task.IdempotencyKey == request.IdempotencyKey {
			return task, false, nil
		}
		return model.Task{}, false, store.ErrIdempotency
	}
	if plan.State != model.PlanApproved {
		return model.Task{}, false, errors.New("发布计划尚未批准或已经执行")
	}
	service, action, err := engine.resolveAction(plan.Service, plan.Action, plan.Target)
	if err != nil {
		_ = engine.store.InvalidateReleasePlan(ctx, plan.ID, "服务能力或目标策略已变化")
		return model.Task{}, false, err
	}
	observed, err := engine.inspect(ctx, service)
	if err != nil {
		return model.Task{}, false, fmt.Errorf("执行计划前检查失败: %w", err)
	}
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
		Risk: action.Risk, Impact: action.Impact, Rollback: action.Rollback, Scope: action.Scope,
		Steps: append([]string(nil), action.Steps...), PhaseSemantics: action.PhaseSemantics,
		ConfirmationPhrase: renderConfirmation(action.ConfirmationTemplate, service.Name, plan.Target),
		ExpectedBefore:     observed, TargetEvidence: targetEvidence,
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
	taskID, err := newUUID()
	if err != nil {
		return model.Task{}, false, err
	}
	task, created, err := engine.store.StartPlanTask(ctx, plan, actorHash, request.IdempotencyKey, taskID)
	if err != nil || !created {
		return task, created, err
	}
	engine.enqueue(task)
	return task, true, nil
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
