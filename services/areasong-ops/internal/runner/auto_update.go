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

const autoUpdateEvaluationInterval = 15 * time.Minute

// AutoUpdatePolicies returns only policies for objects visible to the actor.
func (engine *Engine) AutoUpdatePolicies(ctx context.Context, actor string) ([]model.AutoUpdatePolicyView, error) {
	if err := engine.authorizePlatform(ctx, actor, model.PermissionRead, "auto-updates"); err != nil {
		return nil, err
	}
	items, err := engine.store.ListAutoUpdatePolicies(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]model.AutoUpdatePolicyView, 0, len(items))
	for _, item := range items {
		if engine.authorize(ctx, actor, model.PermissionRead, item.ObjectID) == nil {
			result = append(result, item)
		}
	}
	return result, nil
}

func (engine *Engine) UpdateAutoUpdatePolicy(
	ctx context.Context, actor string, request model.AutoUpdatePolicyRequest,
) (model.AutoUpdatePolicyView, error) {
	service, ok := engine.catalog.Services[request.Service]
	if !ok {
		return model.AutoUpdatePolicyView{}, errors.New("服务未纳入控制面")
	}
	if err := engine.authorize(ctx, actor, model.PermissionManageConfig, service.ObjectID); err != nil {
		return model.AutoUpdatePolicyView{}, err
	}
	if request.IdempotencyKey == "" {
		return model.AutoUpdatePolicyView{}, errors.New("自动更新策略缺少幂等键")
	}
	policy := &model.AutoUpdatePolicy{
		Enabled: request.Enabled, Channel: request.Channel,
		MaintenanceWindow: request.MaintenanceWindow, CanaryPercent: request.CanaryPercent,
		MaxUnavailable: request.MaxUnavailable, RequireBackup: request.RequireBackup,
		RequireApproval: request.RequireApproval, RollbackOnAlert: request.RollbackOnAlert,
		ObservationSeconds: request.ObservationSeconds,
	}
	if err := validateAutoUpdatePolicyInput(service.Name, policy); err != nil {
		return model.AutoUpdatePolicyView{}, err
	}
	view := model.AutoUpdatePolicyView{Service: service.Name, ObjectID: service.ObjectID, TenantID: service.TenantID,
		Enabled: policy.Enabled, Channel: policy.Channel, MaintenanceWindow: policy.MaintenanceWindow,
		CanaryPercent: policy.CanaryPercent, MaxUnavailable: policy.MaxUnavailable,
		RequireBackup: policy.RequireBackup, RequireApproval: policy.RequireApproval,
		RollbackOnAlert: policy.RollbackOnAlert, ObservationSeconds: policy.ObservationSeconds}
	digest := digestText(fmt.Sprintf("%s\x00%s\x00%t\x00%s\x00%s\x00%d\x00%d\x00%t\x00%t\x00%t\x00%d",
		actor, service.ObjectID, policy.Enabled, policy.Channel, policy.MaintenanceWindow,
		policy.CanaryPercent,
		policy.MaxUnavailable, policy.RequireBackup, policy.RequireApproval,
		policy.RollbackOnAlert, policy.ObservationSeconds))
	if _, err := engine.store.ApplyAutoUpdatePolicy(ctx, actor, request.IdempotencyKey, digest, view); err != nil {
		return model.AutoUpdatePolicyView{}, err
	}
	if _, err := engine.store.GetAutoUpdatePolicy(ctx, service.Name); err != nil {
		return model.AutoUpdatePolicyView{}, err
	}
	_, _ = engine.store.AppendAudit(ctx, model.AuditEntry{ActorHash: actor, Event: "auto_update.policy.changed", Resource: service.ObjectID, Outcome: "accepted", Detail: map[string]any{
		"enabled": policy.Enabled, "channel": policy.Channel, "requireApproval": policy.RequireApproval,
	}})
	return view, nil
}

func validateAutoUpdatePolicyInput(service string, policy *model.AutoUpdatePolicy) error {
	if policy.Channel == "" {
		policy.Channel = "stable"
	}
	if policy.Channel != "stable" && policy.Channel != "candidate" && policy.Channel != "security" {
		return fmt.Errorf("服务 %s 的自动更新 channel 无效", service)
	}
	if policy.Enabled && (!policy.RequireApproval || !policy.RequireBackup || !policy.RollbackOnAlert) {
		return errors.New("自动更新必须同时启用人工批准、新鲜备份和告警回滚门禁")
	}
	if policy.CanaryPercent < 0 || policy.CanaryPercent > 100 || policy.MaxUnavailable < 0 || policy.MaxUnavailable > 100 {
		return errors.New("自动更新批次参数无效")
	}
	if policy.ObservationSeconds == 0 {
		policy.ObservationSeconds = 300
	}
	if policy.ObservationSeconds < 60 || policy.ObservationSeconds > 24*60*60 {
		return errors.New("自动更新观察窗口必须为 60 到 86400 秒")
	}
	return nil
}

// SeedAutoUpdatePolicies copies only catalog declarations into the durable
// store. Existing operator-owned evaluation metadata is retained.
func (engine *Engine) seedAutoUpdatePolicies() error {
	for _, service := range engine.catalog.Services {
		if service.AutoUpdate == nil {
			continue
		}
		if _, err := engine.store.GetAutoUpdatePolicy(context.Background(), service.Name); err == nil {
			continue
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		policy := service.AutoUpdate
		if err := validateAutoUpdatePolicyInput(service.Name, policy); err != nil {
			return err
		}
		if err := engine.store.UpsertAutoUpdatePolicy(context.Background(), model.AutoUpdatePolicyView{
			Service: service.Name, ObjectID: service.ObjectID, TenantID: service.TenantID,
			Enabled: policy.Enabled, Channel: policy.Channel, MaintenanceWindow: policy.MaintenanceWindow,
			CanaryPercent: policy.CanaryPercent, MaxUnavailable: policy.MaxUnavailable,
			RequireBackup: policy.RequireBackup, RequireApproval: policy.RequireApproval,
			RollbackOnAlert: policy.RollbackOnAlert, ObservationSeconds: policy.ObservationSeconds,
		}); err != nil {
			return err
		}
	}
	return nil
}

// EvaluateAutoUpdates evaluates due policies and creates normal release plans.
// It intentionally never approves or executes a plan. A caller may run this
// from a scheduler using a dedicated, explicitly-bound actor identity.
func (engine *Engine) EvaluateAutoUpdates(ctx context.Context, actor string) ([]model.AutoUpdateEvaluation, error) {
	if err := engine.authorizePlatform(ctx, actor, model.PermissionDeploy, "auto-updates"); err != nil {
		return nil, err
	}
	policies, err := engine.store.ListAutoUpdatePolicies(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	result := make([]model.AutoUpdateEvaluation, 0, len(policies))
	for _, policy := range policies {
		evaluation := model.AutoUpdateEvaluation{Service: policy.Service, EvaluatedAt: now}
		if !policy.Enabled {
			evaluation.Reason = "自动更新策略未启用"
			result = append(result, evaluation)
			continue
		}
		if policy.NextEvaluationAt != nil && now.Before(*policy.NextEvaluationAt) {
			evaluation.Reason = "尚未到下一次评估时间"
			result = append(result, evaluation)
			continue
		}
		if policy.LastPlanID != "" {
			if plan, getErr := engine.store.GetReleasePlan(ctx, policy.LastPlanID); getErr == nil &&
				plan.State != model.PlanCompleted && plan.State != model.PlanInvalidated {
				evaluation.Reason = "已有未收口的自动更新计划"
				_ = engine.store.MarkAutoUpdateEvaluation(ctx, policy.Service, &now, autoTimePtr(now.Add(autoUpdateEvaluationInterval)), policy.LastPlanID, evaluation.Reason)
				result = append(result, evaluation)
				continue
			}
		}
		if !autoUpdateWindowOpen(policy.MaintenanceWindow, now) {
			evaluation.Reason = "当前不在维护窗口"
			_ = engine.store.MarkAutoUpdateEvaluation(ctx, policy.Service, &now, autoTimePtr(nextWindowEvaluation(now)), "", evaluation.Reason)
			result = append(result, evaluation)
			continue
		}
		discovery, found, discoveryErr := engine.store.LatestSuccessfulDiscovery(ctx, policy.Service)
		if discoveryErr != nil {
			return nil, discoveryErr
		}
		if !found {
			evaluation.Reason = "缺少成功的发布发现证据"
			_ = engine.store.MarkAutoUpdateEvaluation(ctx, policy.Service, &now, autoTimePtr(now.Add(autoUpdateEvaluationInterval)), "", evaluation.Reason)
			result = append(result, evaluation)
			continue
		}
		target, _ := discovery["latestTag"].(string)
		if target == "" {
			if version, ok := discovery["manifestVersion"].(string); ok && version != "" {
				target = "v" + strings.TrimPrefix(version, "v")
			}
		}
		current, _ := discovery["currentVersion"].(string)
		if target == "" || strings.TrimPrefix(target, "v") == strings.TrimPrefix(current, "v") {
			evaluation.Reason = "没有可用更新"
			_ = engine.store.MarkAutoUpdateEvaluation(ctx, policy.Service, &now, autoTimePtr(now.Add(autoUpdateEvaluationInterval)), "", evaluation.Reason)
			result = append(result, evaluation)
			continue
		}
		requestKey, keyErr := newUUID()
		if keyErr != nil {
			return nil, keyErr
		}
		plan, planErr := engine.CreateReleasePlan(ctx, actor, model.PreviewRequest{Service: policy.Service, Action: "update", Target: target, IdempotencyKey: requestKey, RequestDigest: digestText(strings.Join([]string{"auto-update", policy.Service, target, policy.Channel}, "\x00"))})
		if planErr != nil {
			evaluation.Reason = redactText(planErr.Error())
			_ = engine.store.MarkAutoUpdateEvaluation(ctx, policy.Service, &now, autoTimePtr(now.Add(autoUpdateEvaluationInterval)), "", evaluation.Reason)
			result = append(result, evaluation)
			continue
		}
		evaluation.Eligible, evaluation.UpdateCreated, evaluation.PlanID, evaluation.Target = true, true, plan.ID, target
		_ = engine.store.MarkAutoUpdateEvaluation(ctx, policy.Service, &now, autoTimePtr(now.Add(autoUpdateEvaluationInterval)), plan.ID, "")
		_, _ = engine.store.AppendAudit(ctx, model.AuditEntry{ActorHash: actor, Event: "auto_update.plan.created", Resource: plan.ID, Outcome: "accepted", Detail: map[string]any{"service": policy.Service, "target": target, "channel": policy.Channel}})
		result = append(result, evaluation)
	}
	return result, nil
}

func autoTimePtr(value time.Time) *time.Time { return &value }

func autoUpdateWindowOpen(window string, now time.Time) bool {
	if window == "" {
		return true
	}
	window = strings.TrimSuffix(window, "Z")
	parts := strings.Split(window, "-")
	if len(parts) != 2 {
		return false
	}
	start, err1 := time.Parse("15:04", parts[0])
	end, err2 := time.Parse("15:04", parts[1])
	if err1 != nil || err2 != nil {
		return false
	}
	minute := now.Hour()*60 + now.Minute()
	from, to := start.Hour()*60+start.Minute(), end.Hour()*60+end.Minute()
	if from == to {
		return true
	}
	if from < to {
		return minute >= from && minute < to
	}
	return minute >= from || minute < to
}

func nextWindowEvaluation(now time.Time) time.Time { return now.Add(autoUpdateEvaluationInterval) }
