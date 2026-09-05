package runner

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	"github.com/AreaSong/ops/services/areasong-ops/internal/reconcile"
	"github.com/AreaSong/ops/services/areasong-ops/internal/store"
)

func lifecycleAction(service model.ServiceDefinition, name string) (model.ActionDefinition, bool) {
	if service.Metadata.Type != "service" || service.Metadata.Lifecycle != "active" {
		return model.ActionDefinition{}, false
	}
	if (name == "enter-maintenance" || name == "drain" || name == "resume-traffic") && service.TrafficPolicy == nil {
		// A desired-state write alone cannot claim that public traffic changed.
		// Services without an explicitly trusted traffic policy must fail closed.
		return model.ActionDefinition{}, false
	}
	risk := model.RiskMedium
	steps := []string{"preflight", name, "health"}
	compositeWebsiteAction := service.TrafficPolicy != nil
	if name == "stop" && compositeWebsiteAction {
		// Drain existing application requests, then leave the public endpoint in
		// maintenance before stopping the app. This avoids breaking in-flight
		// requests or exposing a 502.
		steps = []string{"preflight", "drain", "enter-maintenance", "stop", "health"}
	}
	if name == "start" && compositeWebsiteAction {
		// Establish the maintenance barrier first and only restore public traffic
		// after the application health phase passes.
		steps = []string{"preflight", "enter-maintenance", "start", "health", "resume-traffic", "verify"}
	}
	phaseSemantics := map[string]model.PhaseSemantics{
		"preflight": {Effect: "observe", FailurePolicy: "fail"},
		name:        {Effect: "runtime_mutation", FailurePolicy: "needs_attention"},
		"health":    {Effect: "observe", FailurePolicy: "needs_attention"},
	}
	if name == "stop" && compositeWebsiteAction {
		phaseSemantics["drain"] = model.PhaseSemantics{Effect: "runtime_mutation", FailurePolicy: "needs_attention"}
		phaseSemantics["enter-maintenance"] = model.PhaseSemantics{Effect: "runtime_mutation", FailurePolicy: "needs_attention"}
	}
	if name == "start" && compositeWebsiteAction {
		phaseSemantics["enter-maintenance"] = model.PhaseSemantics{Effect: "runtime_mutation", FailurePolicy: "needs_attention"}
		phaseSemantics["resume-traffic"] = model.PhaseSemantics{Effect: "runtime_mutation", FailurePolicy: "needs_attention"}
		phaseSemantics["verify"] = model.PhaseSemantics{Effect: "observe", FailurePolicy: "needs_attention"}
	}
	impact := "通过受控适配器改变服务流量或运行状态。"
	rollback := "动作失败后保持当前状态并进入人工核对；不会自动恢复业务数据库。"
	scope := "受管服务运行状态与流量"
	switch name {
	case "enter-maintenance":
		impact = "通过受控流量适配器切换维护页，并将服务目标状态设为维护。"
		scope = "服务目标状态与 Nginx 受控 include"
	case "drain":
		impact = "通过受控流量适配器停止新请求并等待现有请求自然结束。"
		scope = "服务目标状态与 Nginx 受控 include"
	case "resume-traffic":
		impact = "通过受控流量适配器恢复公网请求，并重新检查应用健康。"
		scope = "服务目标状态与 Nginx 受控 include"
	case "stop":
		risk = model.RiskHigh
		impact = "先排空请求并切换维护页，再停止受管应用服务，避免公网出现 502。"
	case "start":
		impact = "在维护屏障内启动应用，健康检查通过后再恢复公网流量。"
	default:
		return model.ActionDefinition{}, false
	}
	return model.ActionDefinition{Name: name, DisplayName: lifecycleDisplayName(name), Enabled: true,
		Risk: risk, TargetMode: "none", Steps: steps, TimeoutSeconds: 1800,
		ConfirmationTemplate: lifecycleConfirmation(name), Impact: impact, Rollback: rollback, Scope: scope,
		PhaseSemantics: phaseSemantics, ObservationSeconds: 300}, true
}

func (engine *Engine) lifecycleAction(service model.ServiceDefinition, name string) (model.ActionDefinition, bool) {
	action, ok := lifecycleAction(service, name)
	if ok && engine.lifecycleObservationSeconds >= 0 {
		action.ObservationSeconds = engine.lifecycleObservationSeconds
	}
	return action, ok
}

func lifecycleDisplayName(name string) string {
	switch name {
	case "enter-maintenance":
		return "进入维护"
	case "drain":
		return "排空流量"
	case "resume-traffic":
		return "恢复流量"
	case "start":
		return "启动服务"
	case "stop":
		return "停止服务"
	}
	return name
}

func lifecycleConfirmation(name string) string { return lifecycleDisplayName(name) + " {service}" }

func (engine *Engine) ServiceState(ctx context.Context, serviceName string) (model.ServiceState, error) {
	service, ok := engine.catalog.Object(serviceName)
	if !ok {
		return model.ServiceState{}, errors.New("受管对象未纳入控制面")
	}
	if service.Metadata.Type != "service" {
		return model.ServiceState{}, errors.New("只有服务支持生命周期状态")
	}
	state, found, err := engine.store.GetServiceState(ctx, serviceName)
	if err != nil {
		return model.ServiceState{}, err
	}
	if !found {
		desired := model.DesiredRunning
		if service.StatePolicy != nil && service.StatePolicy.DefaultDesired != "" {
			desired = service.StatePolicy.DefaultDesired
		}
		tenantID := service.TenantID
		if tenantID == "" && engine.catalog.Access != nil {
			tenantID = engine.catalog.Access.DefaultTenant
		}
		if tenantID == "" {
			tenantID = "default"
		}
		// Catalog defaults are a read-only projection until a successful plan
		// commits a desired-state transition. A status read must never initialize
		// durable production intent or emit a desired_state.changed event.
		state = model.ServiceState{
			Service: service.Name, ObjectID: service.ObjectID, TenantID: tenantID,
			Desired: desired, Reason: "服务目录默认目标状态（未提交）",
		}
	}
	// A stopped service may legitimately reject its normal inspect command. In
	// that case use the same narrowly-scoped lifecycle fallback as plan creation
	// so the control plane can still report the durable stopped state.
	inspectAction := "inspect"
	if state.Desired == model.DesiredStopped {
		inspectAction = "start"
	}
	status, inspectErr := engine.inspectForAction(ctx, service, inspectAction)
	if inspectErr != nil {
		state.Actual = model.ActualUnknown
		state.Health = model.HealthUnknown
		state.Reason = redactText(inspectErr.Error())
		state.ObservedAt = time.Now().UTC()
		state.Drift = &model.StateDrift{Detected: true, Expected: string(state.Desired), Observed: string(state.Actual), Reason: state.Reason, DetectedAt: state.ObservedAt}
		return state, nil
	}
	observed, drift := reconcile.Reconcile(status, state.Desired, time.Now().UTC())
	tenantID := service.TenantID
	if tenantID == "" {
		tenantID = "default"
	}
	observed.Service, observed.ObjectID, observed.TenantID = service.Name, service.ObjectID, tenantID
	observed.Desired = state.Desired
	observed.DesiredUpdatedAt = state.DesiredUpdatedAt
	observed.MaintenanceUntil = state.MaintenanceUntil
	observed.Generation = state.Generation
	observed.Data = status
	observed.Drift = drift
	if err := engine.store.SaveObservation(ctx, model.StateObservation{
		Service: observed.Service, ObjectID: observed.ObjectID, TenantID: observed.TenantID,
		Actual: observed.Actual, Health: observed.Health, Reason: observed.Reason,
		Data: status, ObservedAt: observed.ObservedAt, Drift: drift != nil && drift.Detected,
	}); err != nil {
		return model.ServiceState{}, err
	}
	if drift != nil && drift.Detected {
		_ = engine.store.AppendControlEvent(ctx, model.ControlPlaneEvent{Type: "state.drift", Resource: service.ObjectID,
			TenantID: service.TenantID, Data: map[string]any{"expected": drift.Expected, "observed": drift.Observed, "reason": drift.Reason}})
	}
	return observed, nil
}

func (engine *Engine) ServiceStates(ctx context.Context) []model.ServiceState {
	names := engine.catalog.ServiceNames()
	return engine.serviceStates(ctx, names)
}

func (engine *Engine) ServiceStatesForActor(
	ctx context.Context,
	actor string,
) ([]model.ServiceState, error) {
	names, err := engine.authorizedObjectNames(ctx, actor, engine.catalog.ServiceNames())
	if err != nil {
		return nil, err
	}
	return engine.serviceStates(ctx, names), nil
}

func (engine *Engine) serviceStates(
	ctx context.Context,
	names []string,
) []model.ServiceState {
	states := make([]model.ServiceState, 0, len(names))
	for _, name := range names {
		state, err := engine.ServiceState(ctx, name)
		if err != nil {
			states = append(states, model.ServiceState{Service: name, Actual: model.ActualUnknown, Health: model.HealthUnknown, Reason: redactText(err.Error())})
			continue
		}
		states = append(states, state)
	}
	return states
}

func (engine *Engine) SetDesiredState(ctx context.Context, actorHash, serviceName string, desired model.DesiredState, reason string, ttlSeconds int) (model.ServiceState, error) {
	return engine.setDesiredState(ctx, actorHash, serviceName, desired, reason, ttlSeconds, "")
}

// SetDesiredStateWithRequest 是公开生命周期 API 使用的请求绑定入口。
// 幂等键绑定完整规范化请求摘要，store 另行将其绑定到操作者。
func (engine *Engine) SetDesiredStateWithRequest(
	ctx context.Context,
	actorHash, serviceName string,
	desired model.DesiredState,
	reason string,
	ttlSeconds int,
	idempotencyKey string,
) (model.ServiceState, error) {
	if idempotencyKey == "" {
		return model.ServiceState{}, errors.New("缺少幂等键")
	}
	return engine.setDesiredState(ctx, actorHash, serviceName, desired, reason, ttlSeconds, idempotencyKey)
}

func (engine *Engine) setDesiredState(
	ctx context.Context,
	actorHash, serviceName string,
	desired model.DesiredState,
	reason string,
	ttlSeconds int,
	idempotencyKey string,
) (model.ServiceState, error) {
	if !actorPattern.MatchString(actorHash) {
		return model.ServiceState{}, errors.New("操作者标识无效")
	}
	service, ok := engine.catalog.Services[serviceName]
	if !ok {
		return model.ServiceState{}, errors.New("服务未纳入控制面")
	}
	if err := engine.authorize(ctx, actorHash, model.PermissionLifecycle, service.ObjectID); err != nil {
		return model.ServiceState{}, err
	}
	if desired != model.DesiredRunning && desired != model.DesiredStopped && desired != model.DesiredMaintenance && desired != model.DesiredDrained {
		return model.ServiceState{}, errors.New("目标状态无效")
	}
	if engine.catalog.SchemaVersion >= 4 &&
		(desired == model.DesiredMaintenance || desired == model.DesiredDrained) && service.TrafficPolicy == nil {
		return model.ServiceState{}, errors.New("服务未声明受信流量策略，拒绝写入维护或排空状态")
	}
	if ttlSeconds == 0 && service.StatePolicy != nil {
		ttlSeconds = service.StatePolicy.MaintenanceTTLSeconds
	}
	var until *time.Time
	if desired == model.DesiredMaintenance || desired == model.DesiredDrained {
		if ttlSeconds < 60 {
			ttlSeconds = 4 * 60 * 60
		}
		if ttlSeconds > 7*24*60*60 {
			return model.ServiceState{}, errors.New("维护 TTL 超出允许范围")
		}
		value := time.Now().UTC().Add(time.Duration(ttlSeconds) * time.Second)
		until = &value
	}
	tenantID := service.TenantID
	if tenantID == "" {
		if engine.catalog.Access != nil {
			tenantID = engine.catalog.Access.DefaultTenant
		}
		if tenantID == "" {
			tenantID = "default"
		}
	}
	input := store.DesiredStateInput{
		Service: service.Name, ObjectID: service.ObjectID, TenantID: tenantID,
		Desired: desired, Reason: reason, ActorHash: actorHash, MaintenanceUntil: until,
	}
	requestCreated := false
	if idempotencyKey == "" {
		_, err := engine.store.SetDesiredState(ctx, input)
		if err != nil {
			return model.ServiceState{}, err
		}
	} else {
		digestService := service
		digestService.TenantID = tenantID
		requestDigest := desiredStateRequestDigest(digestService, desired, reason, ttlSeconds)
		state, replayed, err := engine.store.SetDesiredStateIdempotent(ctx, input, idempotencyKey, requestDigest)
		if err != nil {
			return model.ServiceState{}, err
		}
		if replayed {
			// 重放直接返回 receipt；再次 inspect 可能追加漂移事件，
			// 使本应只读的重放改动事件流。
			return state, nil
		}
		requestCreated = true
	}
	_, _ = engine.store.AppendAudit(ctx, model.AuditEntry{ActorHash: actorHash, Event: "desired_state.changed", Resource: service.ObjectID,
		Outcome: "accepted", Detail: map[string]any{"desiredState": desired, "reason": reason, "ttlSeconds": ttlSeconds}})
	state, err := engine.ServiceState(ctx, serviceName)
	if err != nil {
		return model.ServiceState{}, err
	}
	if requestCreated {
		if err := engine.store.UpdateDesiredStateReceiptState(ctx, idempotencyKey, actorHash, state); err != nil {
			return model.ServiceState{}, err
		}
	}
	return state, nil
}

func desiredStateRequestDigest(
	service model.ServiceDefinition,
	desired model.DesiredState,
	reason string,
	ttlSeconds int,
) string {
	// running/stopped 请求的 TTL 没有语义影响；先归一化，避免等价请求
	// 消耗不同的 receipt。
	if desired != model.DesiredMaintenance && desired != model.DesiredDrained {
		ttlSeconds = 0
	}
	payload, _ := json.Marshal(struct {
		Service        string             `json:"service"`
		ObjectID       string             `json:"objectId"`
		TenantID       string             `json:"tenantId"`
		Desired        model.DesiredState `json:"desiredState"`
		Reason         string             `json:"reason"`
		MaintenanceTTL int                `json:"maintenanceTtlSeconds"`
	}{
		Service: service.Name, ObjectID: service.ObjectID, TenantID: service.TenantID,
		Desired: desired, Reason: reason, MaintenanceTTL: ttlSeconds,
	})
	return digestText(string(payload))
}

func desiredStateForAction(action string) (model.DesiredState, bool) {
	switch action {
	case "start", "resume-traffic":
		return model.DesiredRunning, true
	case "stop":
		return model.DesiredStopped, true
	case "enter-maintenance":
		return model.DesiredMaintenance, true
	case "drain":
		return model.DesiredDrained, true
	default:
		return "", false
	}
}
