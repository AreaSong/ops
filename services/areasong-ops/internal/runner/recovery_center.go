package runner

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

const restoreDrillFreshness = 30 * 24 * time.Hour

func restoreDrillFresh(task model.Task, now time.Time) bool {
	if task.FinishedAt == nil {
		return false
	}
	age := now.Sub(task.FinishedAt.UTC())
	return age >= -time.Minute && age <= restoreDrillFreshness
}

func (engine *Engine) RecoveryCenter(ctx context.Context, serviceName string) (model.RecoveryCenterView, error) {
	service, ok := engine.catalog.Services[serviceName]
	if !ok {
		return model.RecoveryCenterView{}, errors.New("服务未纳入控制面")
	}
	points, err := engine.store.ListRecoveryPoints(ctx, serviceName, 20)
	if err != nil {
		return model.RecoveryCenterView{}, err
	}
	view := model.RecoveryCenterView{Service: serviceName, AvailableActions: []model.RecoveryAction{
		{Name: "inspect", Label: "检查恢复点", Enabled: true},
		{Name: "restore-drill", Label: "隔离恢复演练", Enabled: service.Actions["restore-drill"].Enabled},
	}}
	if action, exists := service.Actions["restore"]; exists {
		view.AvailableActions = append(view.AvailableActions, model.RecoveryAction{
			Name: "restore", Label: "生产恢复", Enabled: action.Enabled,
			Reason: action.DisabledReason,
		})
	}
	if len(points) > 0 {
		view.Latest = &points[0]
	}
	if view.Latest == nil {
		view.DrillReason = "尚无已验证恢复点，无法证明隔离恢复能力"
		return view, nil
	}
	drill, found, err := engine.store.LatestSucceededRestoreDrill(ctx, serviceName, view.Latest.EvidenceDigest)
	if err != nil {
		return model.RecoveryCenterView{}, err
	}
	if !found || drill.FinishedAt == nil {
		view.DrillReason = "当前恢复点尚无成功的隔离恢复演练证据"
		return view, nil
	}
	view.DrillLastSuccessAt = drill.FinishedAt
	view.DrillFresh = restoreDrillFresh(drill, time.Now().UTC())
	if !view.DrillFresh {
		view.DrillReason = "当前恢复点的隔离恢复演练已超过 30 天"
	}
	return view, nil
}

func (engine *Engine) CreateRestorePlan(ctx context.Context, actor string, request model.RestoreRequest) (model.ReleasePlan, error) {
	if !uuidPattern.MatchString(request.IdempotencyKey) {
		return model.ReleasePlan{}, errors.New("恢复请求幂等键无效")
	}
	if request.Service == "" || request.RecoveryPointID == "" {
		return model.ReleasePlan{}, errors.New("恢复请求必须明确服务和恢复点")
	}
	if request.Mode == "" {
		request.Mode = "isolated"
	}
	if request.Mode != "isolated" && request.Mode != "production" {
		return model.ReleasePlan{}, errors.New("恢复模式无效")
	}
	service, ok := engine.catalog.Services[request.Service]
	if !ok {
		return model.ReleasePlan{}, errors.New("服务未纳入控制面")
	}
	// Use the catalog's immutable object identity. A synthesized
	// "service:<name>" value can diverge from enforced tenant/RBAC bindings.
	if err := engine.authorize(ctx, actor, model.PermissionRecover, service.ObjectID); err != nil {
		return model.ReleasePlan{}, err
	}
	point, err := engine.store.GetRecoveryPoint(ctx, request.RecoveryPointID)
	if err != nil {
		return model.ReleasePlan{}, err
	}
	if point.Service != request.Service || point.Status != "verified" {
		return model.ReleasePlan{}, errors.New("恢复点不可用于该服务")
	}
	tenantID := service.TenantID
	if tenantID == "" && engine.catalog.Access != nil {
		tenantID = engine.catalog.Access.DefaultTenant
	}
	if tenantID == "" {
		tenantID = "default"
	}
	if request.TenantID != "" && request.TenantID != tenantID {
		return model.ReleasePlan{}, errors.New("恢复请求租户与服务租户不一致")
	}
	if request.ServerID != "" && request.ServerID != service.ServerID {
		return model.ReleasePlan{}, errors.New("恢复请求服务器与服务绑定不一致")
	}
	if request.ExpectedBeforeDigest != "" && request.ExpectedBeforeDigest != point.ExpectedBeforeDigest {
		return model.ReleasePlan{}, errors.New("恢复请求变更前身份摘要不一致")
	}
	request.TenantID, request.ServerID = tenantID, service.ServerID
	request.ExpectedBeforeDigest = point.ExpectedBeforeDigest
	if request.Mode == "production" && point.BindingDigest == "" {
		return model.ReleasePlan{}, errors.New("生产恢复点缺少不可变绑定摘要")
	}
	now := time.Now().UTC()
	if point.VerifiedAt == nil || point.EvidenceDigest == "" || point.ExpectedBeforeDigest == "" || len(point.RequiredArtifactRoles) == 0 {
		return model.ReleasePlan{}, errors.New("恢复点缺少完整证据或制品角色")
	}
	if point.RecoverableUntil != nil && !now.Before(*point.RecoverableUntil) {
		return model.ReleasePlan{}, errors.New("恢复点已过期")
	}
	if len(point.Evidence.Artifacts) == 0 {
		return model.ReleasePlan{}, errors.New("恢复点没有可验证的制品证据")
	}
	if request.Mode == "production" {
		drill, found, drillErr := engine.store.LatestSucceededRestoreDrill(ctx, request.Service, point.EvidenceDigest)
		if drillErr != nil {
			return model.ReleasePlan{}, drillErr
		}
		if !found || !restoreDrillFresh(drill, now) {
			return model.ReleasePlan{}, errors.New("当前恢复点缺少 30 天内成功的隔离恢复演练证据")
		}
	}
	confirmationLabel := "隔离恢复演练"
	if request.Mode == "production" {
		confirmationLabel = "生产"
	}
	expectedConfirmation := fmt.Sprintf("创建%s恢复计划 %s", confirmationLabel, request.Service)
	if request.Confirmation != expectedConfirmation {
		return model.ReleasePlan{}, errors.New("恢复请求确认短语不匹配")
	}
	actionName := "restore-drill"
	if request.Mode == "production" {
		actionName = "restore"
		action, exists := service.Actions[actionName]
		if !exists || !action.Enabled || action.Risk != model.RiskHigh {
			return model.ReleasePlan{}, errors.New("生产数据库恢复未被服务声明为高风险受控动作")
		}
	}
	if action, exists := service.Actions[actionName]; !exists || !action.Enabled {
		return model.ReleasePlan{}, errors.New("服务未开放恢复动作")
	}
	requestDigest := digestText(strings.Join([]string{actor, request.Service, request.RecoveryPointID, request.Mode,
		request.TenantID, request.ServerID, request.ExpectedBeforeDigest, point.BindingDigest,
		point.EvidenceDigest, request.Confirmation}, "\x00"))
	return engine.CreateReleasePlan(ctx, actor, model.PreviewRequest{
		Service: request.Service, Action: actionName, Target: request.RecoveryPointID,
		IdempotencyKey: request.IdempotencyKey, RequestDigest: requestDigest,
		RestoreMode: request.Mode, RecoveryPointID: request.RecoveryPointID,
		RestoreTenantID: request.TenantID, RestoreServerID: request.ServerID,
		RestoreExpectedBeforeDigest: request.ExpectedBeforeDigest,
		RestoreContractDigest:       point.BindingDigest, RestoreEvidenceDigest: point.EvidenceDigest,
		RequiresDualApproval: request.Mode == "production",
	})
}
