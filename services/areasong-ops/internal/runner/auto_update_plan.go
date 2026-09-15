package runner

import (
	"context"
	"errors"
	"fmt"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

func autoUpdatePlanPolicy(view model.AutoUpdatePolicyView) model.AutoUpdatePolicy {
	return model.AutoUpdatePolicy{
		Enabled: view.Enabled, Channel: view.Channel,
		MaintenanceWindow: view.MaintenanceWindow, MaintenanceTimezone: view.MaintenanceTimezone,
		CanaryPercent: view.CanaryPercent, MaxUnavailable: view.MaxUnavailable,
		RequireApproval: view.RequireApproval, RequireBackup: view.RequireBackup,
		RollbackOnAlert: view.RollbackOnAlert, ObservationSeconds: view.ObservationSeconds,
	}
}

// 百分比灰度必须有明确的多目标批次，单服务计划不能假装执行了灰度。
func validateSingleTargetAutoUpdate(policy model.AutoUpdatePolicy) error {
	if policy.CanaryPercent != 0 || policy.MaxUnavailable != 0 {
		return errors.New("单实例自动更新不接受灰度或最大不可用比例，请在批量作业中配置显式目标和批次")
	}
	return nil
}

func (engine *Engine) resolveAutoUpdateAction(
	ctx context.Context, service model.ServiceDefinition, action model.ActionDefinition,
	approved *model.AutoUpdatePolicy,
) (model.ActionDefinition, error) {
	if approved == nil {
		return action, nil
	}
	if action.Name != "update" {
		return action, errors.New("自动更新策略只能绑定 update 计划")
	}
	view, err := engine.store.GetAutoUpdatePolicy(ctx, service.Name)
	if err != nil {
		return action, fmt.Errorf("自动更新策略不可用: %w", err)
	}
	current := autoUpdatePlanPolicy(view)
	if err := validateAutoUpdatePolicyInput(service.Name, &current); err != nil {
		return action, err
	}
	if !current.Enabled || current != *approved {
		return action, errors.New("自动更新策略已变化或停用，请重新评估并批准计划")
	}
	if err := validateSingleTargetAutoUpdate(current); err != nil {
		return action, err
	}
	if err := validateAutomaticActionContract(service, action); err != nil {
		return action, err
	}
	action.ObservationSeconds = current.ObservationSeconds
	return action, nil
}

func validateAutomaticActionContract(service model.ServiceDefinition, action model.ActionDefinition) error {
	if service.RecoveryPointPolicy == nil || len(service.RecoveryPointPolicy.RequiredArtifactRoles) == 0 {
		return errors.New("自动更新缺少可验证的恢复点策略")
	}
	backupReady, canRollback := false, false
	for _, phase := range action.Steps {
		semantics := model.EffectivePhaseSemantics(action, phase)
		if mutationSemantics(semantics) && (!backupReady || !semantics.RequiresRecoveryPoint) {
			return errors.New("自动更新变更阶段必须复验先前产生的恢复点")
		}
		backupReady = backupReady || semantics.ProducesRecoveryPoint
		if semantics.FailurePolicy == "rollback" && semantics.RecoveryPhase != "" {
			canRollback = true
		}
	}
	if !backupReady || !canRollback {
		return errors.New("自动更新动作缺少备份证据或受控回滚阶段")
	}
	return nil
}

func autoUpdatePlanIsStale(plan model.ReleasePlan, policy model.AutoUpdatePolicy) bool {
	if plan.State != model.PlanPendingApproval && plan.State != model.PlanApproved && plan.State != model.PlanScheduled {
		return false
	}
	return plan.ApprovalSummary.AutoUpdatePolicy == nil || *plan.ApprovalSummary.AutoUpdatePolicy != policy
}

func (engine *Engine) rejectLegacyAutoUpdatePlan(ctx context.Context, actor string, plan model.ReleasePlan) error {
	if plan.ApprovalSummary.AutoUpdatePolicy != nil {
		return nil
	}
	policy, err := engine.store.GetAutoUpdatePolicy(ctx, plan.Service)
	if err == nil && policy.LastPlanID == plan.ID {
		reason := "旧自动更新计划未绑定安全策略，请重新评估"
		return engine.invalidateReleasePlan(ctx, actor, plan.ID, reason, errors.New(reason))
	}
	return nil
}
