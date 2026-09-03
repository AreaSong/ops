package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/config"
	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

// effectiveAccessPolicy makes the root-owned SQLite snapshot authoritative
// after bootstrap. The catalog remains the bootstrap source, but a running
// Runner must observe a revoke or tenant change without requiring restart.
func (engine *Engine) effectiveAccessPolicy(
	ctx context.Context,
) (*config.AccessPolicy, model.AccessPolicySnapshot, error) {
	snapshot, found, err := engine.store.GetAccessPolicySnapshot(ctx)
	if err != nil {
		return nil, model.AccessPolicySnapshot{}, err
	}
	if !found {
		if engine.catalog.Access == nil {
			return nil, model.AccessPolicySnapshot{}, nil
		}
		return engine.catalog.Access, model.AccessPolicySnapshot{}, nil
	}
	var policy config.AccessPolicy
	if err := json.Unmarshal([]byte(snapshot.PolicyJSON), &policy); err != nil {
		return nil, snapshot, fmt.Errorf("访问策略快照损坏: %w", err)
	}
	return &policy, snapshot, nil
}

type authorizationError struct {
	message string
}

func (err authorizationError) Error() string {
	return err.message
}

func isAuthorizationError(err error) bool {
	var target authorizationError
	return errors.As(err, &target)
}

func (engine *Engine) authorize(
	ctx context.Context,
	actor string,
	permission model.Permission,
	objectID string,
) error {
	if !actorPattern.MatchString(actor) {
		return authorizationError{message: "操作者标识无效"}
	}
	policy, _, policyErr := engine.effectiveAccessPolicy(ctx)
	if policyErr != nil {
		return authorizationError{message: "访问策略快照不可用"}
	}
	if policy == nil || !policy.Enforced {
		if engine.catalog.SchemaVersion <= 3 {
			return nil
		}
		return authorizationError{message: "生产访问策略未启用"}
	}
	principal, ok := policy.Principals[actor]
	if !ok {
		return authorizationError{message: "操作者未登记"}
	}
	if !principalUsable(policy, actor, principal, time.Now().UTC()) {
		return authorizationError{message: "操作者已禁用、过期或不满足 JIT 授权"}
	}
	tenantID := principal.TenantID
	if tenantID == "" {
		tenantID = policy.DefaultTenant
	}
	if !tenantIsActive(policy, tenantID) {
		return authorizationError{message: "操作者所属租户未启用"}
	}
	objectTenant, tenantKnown := engine.objectTenantForPolicy(objectID, policy)
	if !tenantKnown && !principalIsPlatformAdmin(policy, principal) {
		if !policyBindingsAllow(policy, actor, tenantID, objectID, permission, time.Now().UTC()) {
			return authorizationError{message: "受管对象未登记或不在授权范围"}
		}
		return nil
	}
	if tenantKnown && objectTenant != tenantID && !principalIsPlatformAdmin(policy, principal) {
		return authorizationError{message: "受管对象不属于操作者租户"}
	}
	if principalRolesAllow(policy, principal, permission) {
		return nil
	}
	if policyBindingsAllow(policy, actor, tenantID, objectID, permission, time.Now().UTC()) {
		return nil
	}
	decision, err := engine.store.Authorize(ctx, actor, tenantID, objectID, permission)
	if err != nil {
		return fmt.Errorf("读取授权策略失败: %w", err)
	}
	if decision.Allowed {
		return nil
	}
	return authorizationError{message: "当前角色没有执行该操作的权限"}
}

// authorizePlatform gates control-plane resources that do not belong to one
// service tenant (RBAC, fleet inventory, policy views, and runner metadata).
// A tenant role must not gain a global view merely by carrying the same
// permission name; platform-admin or an explicit wildcard binding is needed.
func (engine *Engine) authorizePlatform(
	ctx context.Context,
	actor string,
	permission model.Permission,
	resource string,
) error {
	if !actorPattern.MatchString(actor) {
		return authorizationError{message: "操作者标识无效"}
	}
	policy, _, policyErr := engine.effectiveAccessPolicy(ctx)
	if policyErr != nil {
		return authorizationError{message: "访问策略快照不可用"}
	}
	if policy == nil || !policy.Enforced {
		if engine.catalog.SchemaVersion <= 3 {
			return nil
		}
		return authorizationError{message: "生产访问策略未启用"}
	}
	principal, ok := policy.Principals[actor]
	if !ok {
		return authorizationError{message: "操作者未登记"}
	}
	if !principalUsable(policy, actor, principal, time.Now().UTC()) {
		return authorizationError{message: "操作者已禁用、过期或不满足 JIT 授权"}
	}
	tenantID := principal.TenantID
	if tenantID == "" {
		tenantID = policy.DefaultTenant
	}
	if !tenantIsActive(policy, tenantID) {
		return authorizationError{message: "操作者所属租户未启用"}
	}
	if principalIsPlatformAdmin(policy, principal) || policyBindingsAllow(policy, actor, tenantID, resource, permission, time.Now().UTC()) {
		return nil
	}
	// Catalog policy is only the bootstrap layer. Dynamic bindings are stored in
	// SQLite and must be consulted here as well, otherwise a platform role
	// created through the Access API would work for service objects but fail (or
	// be accidentally bypassed) for control-plane resources.
	decision, err := engine.store.Authorize(ctx, actor, tenantID, resource, permission)
	if err != nil {
		return fmt.Errorf("读取平台授权策略失败: %w", err)
	}
	if decision.Allowed {
		return nil
	}
	return authorizationError{message: "当前角色没有平台资源权限"}
}

func (engine *Engine) actorTenantID(ctx context.Context, actor string) (string, error) {
	if !actorPattern.MatchString(actor) {
		return "", authorizationError{message: "操作者标识无效"}
	}
	policy, _, err := engine.effectiveAccessPolicy(ctx)
	if err != nil {
		return "", authorizationError{message: "访问策略快照不可用"}
	}
	if policy == nil || !policy.Enforced {
		if engine.catalog.SchemaVersion <= 3 {
			return "default", nil
		}
		return "", authorizationError{message: "生产访问策略未启用"}
	}
	principal, ok := policy.Principals[actor]
	if !ok || !principalUsable(policy, actor, principal, time.Now().UTC()) {
		return "", authorizationError{message: "操作者未登记、已禁用或授权已过期"}
	}
	tenantID := principal.TenantID
	if tenantID == "" {
		tenantID = policy.DefaultTenant
	}
	if !tenantIsActive(policy, tenantID) {
		return "", authorizationError{message: "操作者所属租户未启用"}
	}
	return tenantID, nil
}

func (engine *Engine) authorizeTask(ctx context.Context, actor string, task model.Task, permission model.Permission) error {
	service, ok := engine.catalog.Object(task.Service)
	if !ok {
		return authorizationError{message: "任务所属对象未登记"}
	}
	return engine.authorize(ctx, actor, permission, service.ObjectID)
}

func (engine *Engine) authorizePlan(ctx context.Context, actor string, plan model.ReleasePlan, permission model.Permission) error {
	service, ok := engine.catalog.Object(plan.Service)
	if !ok {
		return authorizationError{message: "计划所属对象未登记"}
	}
	return engine.authorize(ctx, actor, permission, service.ObjectID)
}

func (engine *Engine) authorizeBatchOperation(ctx context.Context, actor string, operation model.BatchOperation) error {
	if err := engine.authorizePlatform(ctx, actor, model.PermissionBatch, "batch"); err != nil {
		// A tenant-scoped batch permission is valid only after every item is
		// checked. This keeps the sentinel from becoming an IDOR primitive.
		if !isAuthorizationError(err) {
			return err
		}
	}
	for _, item := range operation.Items {
		service, ok := engine.catalog.Object(item.Service)
		if !ok {
			return authorizationError{message: "批量项目所属对象未登记"}
		}
		if err := engine.authorize(ctx, actor, model.PermissionBatch, service.ObjectID); err != nil {
			return err
		}
		if err := engine.authorize(ctx, actor, permissionForAction(operation.Action), service.ObjectID); err != nil {
			return err
		}
		if operation.TenantID != "" && service.TenantID != "" && operation.TenantID != service.TenantID {
			return authorizationError{message: "批量作业租户与目标对象不一致"}
		}
	}
	if len(operation.Items) == 0 {
		return authorizationError{message: "批量作业没有可授权项目"}
	}
	return nil
}

func (engine *Engine) visibleAlert(ctx context.Context, actor string, alert model.ActiveAlert) bool {
	return engine.authorize(ctx, actor, model.PermissionRead, alert.ObjectID) == nil
}

func (engine *Engine) visibleEvent(ctx context.Context, actor string, event model.Event) bool {
	if event.TaskID == "" {
		return engine.authorizePlatform(ctx, actor, model.PermissionRead, "events") == nil
	}
	task, err := engine.store.GetTask(ctx, event.TaskID)
	if err != nil {
		return false
	}
	return engine.authorizeTask(ctx, actor, task, model.PermissionRead) == nil
}

func tenantIsActive(policy *config.AccessPolicy, tenantID string) bool {
	tenant, ok := policy.Tenants[tenantID]
	if !ok {
		return tenantID == policy.DefaultTenant && len(policy.Tenants) == 0
	}
	return tenant.Status == "" || tenant.Status == "active"
}

func principalUsable(policy *config.AccessPolicy, actor string, principal config.AccessPrincipal, now time.Time) bool {
	if principal.Status != "" && principal.Status != "active" {
		return false
	}
	if principal.ExpiresAt != nil && !now.Before(*principal.ExpiresAt) {
		return false
	}
	if !principal.JIT {
		return true
	}
	for _, binding := range policy.Bindings {
		if !binding.JIT || binding.Subject != actor {
			continue
		}
		if binding.TenantID != "*" && binding.TenantID != principal.TenantID {
			continue
		}
		if binding.ExpiresAt != nil && !now.Before(*binding.ExpiresAt) {
			continue
		}
		return true
	}
	return false
}

func principalIsPlatformAdmin(
	policy *config.AccessPolicy,
	principal config.AccessPrincipal,
) bool {
	for _, roleID := range principal.Roles {
		if role, ok := policy.Roles[roleID]; ok && role.Allows(model.Permission("*")) {
			return true
		}
	}
	return false
}

func principalRolesAllow(
	policy *config.AccessPolicy,
	principal config.AccessPrincipal,
	permission model.Permission,
) bool {
	for _, roleID := range principal.Roles {
		if role, ok := policy.Roles[roleID]; ok && role.Allows(permission) {
			return true
		}
	}
	return false
}

func policyBindingsAllow(
	policy *config.AccessPolicy,
	actor, tenantID, objectID string,
	permission model.Permission,
	now time.Time,
) bool {
	for _, binding := range policy.Bindings {
		if !bindingMatches(binding, actor, tenantID, objectID, now) {
			continue
		}
		if role, ok := policy.Roles[binding.RoleID]; ok && role.Allows(permission) {
			return true
		}
	}
	return false
}

func bindingMatches(
	binding model.RoleBinding,
	actor, tenantID, objectID string,
	now time.Time,
) bool {
	subject := config.NormalizeAccessSubject(binding.Subject)
	if strings.Contains(subject, "@") {
		subject = config.AccessHashForEmail(subject)
	}
	if subject != actor || (binding.TenantID != tenantID && binding.TenantID != "*") {
		return false
	}
	if binding.ExpiresAt != nil && !now.Before(*binding.ExpiresAt) {
		return false
	}
	return len(binding.ObjectIDs) == 0 || contains(binding.ObjectIDs, objectID) ||
		contains(binding.ObjectIDs, "*")
}

func (engine *Engine) objectTenant(objectID string) (string, bool) {
	policy := engine.catalog.Access
	if policy == nil {
		return engine.objectTenantForPolicy(objectID, nil)
	}
	return engine.objectTenantForPolicy(objectID, policy)
}

func (engine *Engine) objectTenantForPolicy(objectID string, policy *config.AccessPolicy) (string, bool) {
	for _, name := range engine.catalog.ObjectNames() {
		object, _ := engine.catalog.Object(name)
		if object.ObjectID == objectID {
			tenantID := object.TenantID
			if tenantID == "" && policy != nil {
				tenantID = policy.DefaultTenant
			}
			return tenantID, tenantID != ""
		}
	}
	return "", false
}

func permissionForAction(action string) model.Permission {
	switch action {
	case "inspect", "check":
		return model.PermissionInspect
	case "backup", "restore", "restore-drill":
		return model.PermissionRecover
	case "update", "rollback", "prepare":
		return model.PermissionDeploy
	case "enter-maintenance", "drain", "start", "stop", "resume-traffic", "restart":
		return model.PermissionLifecycle
	default:
		return model.PermissionRead
	}
}
