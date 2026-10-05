package runner

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/config"
	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	"github.com/AreaSong/ops/services/areasong-ops/internal/store"
)

func (engine *Engine) AccessControl(
	ctx context.Context,
	actor string,
) (model.AccessControlView, error) {
	if err := engine.authorizePlatform(ctx, actor, model.PermissionRead, "access"); err != nil {
		return model.AccessControlView{}, err
	}
	policy, snapshot, err := engine.effectiveAccessPolicy(ctx)
	if err != nil {
		return model.AccessControlView{}, err
	}
	pendingChanges, err := engine.store.ListAccessChanges(ctx, 100)
	if err != nil {
		return model.AccessControlView{}, err
	}
	if policy == nil {
		return model.AccessControlView{}, errors.New("访问策略尚未配置")
	}
	canManage := engine.authorizePlatform(ctx, actor, model.PermissionManageAccess, "access") == nil
	tenants, err := engine.store.ListTenants(ctx)
	if err != nil {
		return model.AccessControlView{}, err
	}
	roles, err := engine.store.ListRoles(ctx)
	if err != nil {
		return model.AccessControlView{}, err
	}
	bindings, err := engine.store.ListRoleBindings(ctx)
	if err != nil {
		return model.AccessControlView{}, err
	}
	principals := make(map[string]any, len(policy.Principals))
	for subject, principal := range policy.Principals {
		principals[subject] = principal
	}
	principalList := make([]model.AccessPrincipal, 0, len(policy.Principals))
	for subject, principal := range policy.Principals {
		principalList = append(principalList, model.AccessPrincipal{
			Subject: subject, Email: principal.Email, EmailHash: principal.EmailHash,
			TenantID: principal.TenantID, Roles: append([]string(nil), principal.Roles...),
			Status: principal.Status, ExpiresAt: principal.ExpiresAt, JIT: principal.JIT,
		})
	}
	return model.AccessControlView{
		Enforced: policy.Enforced, CanManage: canManage, DefaultTenant: policy.DefaultTenant,
		Principals: principals, PrincipalList: principalList, Tenants: tenants, Roles: roles, Bindings: bindings,
		CurrentSubject: model.AccessSubject{Subject: actor, TenantID: policy.Principals[actor].TenantID},
		Version:        snapshot.Version, Digest: snapshot.Digest, PendingChanges: pendingChanges,
	}, nil
}

func (engine *Engine) UpdateAccess(
	ctx context.Context,
	actor string,
	request model.AccessControlUpdateRequest,
) (model.AccessControlView, error) {
	if err := engine.authorizePlatform(ctx, actor, model.PermissionManageAccess, "access"); err != nil {
		return model.AccessControlView{}, err
	}
	return engine.updateAccess(ctx, actor, request, nil)
}

type approvedAccessExecution struct {
	changeID      string
	requestDigest string
}

func (engine *Engine) updateAccess(
	ctx context.Context,
	actor string,
	request model.AccessControlUpdateRequest,
	execution *approvedAccessExecution,
) (model.AccessControlView, error) {
	if !uuidPattern.MatchString(request.IdempotencyKey) {
		return model.AccessControlView{}, errors.New("访问策略幂等键无效")
	}
	if !actorPattern.MatchString(actor) {
		return model.AccessControlView{}, errors.New("操作者标识无效")
	}
	policy, snapshot, err := engine.effectiveAccessPolicy(ctx)
	if err != nil {
		return model.AccessControlView{}, err
	}
	if policy == nil {
		return model.AccessControlView{}, errors.New("访问策略尚未配置")
	}
	if request.Enforced != nil && !*request.Enforced && engine.catalog.SchemaVersion >= 4 {
		return model.AccessControlView{}, errors.New("生产 schema 4 不允许关闭访问策略")
	}
	if request.RequiresDualApproval {
		return model.AccessControlView{}, errors.New("访问策略高风险变更必须通过独立审批流程")
	}
	if err := validateAccessRolePermissions(request.Roles); err != nil {
		return model.AccessControlView{}, err
	}
	if err := validateBindingRemovalIDs(request.RemoveBindingIDs); err != nil {
		return model.AccessControlView{}, err
	}

	// Validate against the proposed in-memory view so a newly-created custom
	// role or tenant can be used by a binding in the same atomic request.
	proposed := cloneAccessPolicy(policy)
	normalizedTenants := make([]model.Tenant, 0, len(request.Tenants))
	for _, rawTenant := range request.Tenants {
		tenant, err := normalizeAccessTenant(rawTenant, actor)
		if err != nil {
			return model.AccessControlView{}, err
		}
		if err := validateOrdinaryTenantUpdate(policy, tenant); err != nil {
			return model.AccessControlView{}, err
		}
		proposed.Tenants[tenant.ID] = tenant
		normalizedTenants = append(normalizedTenants, tenant)
	}
	normalizedRoles := make([]model.Role, 0, len(request.Roles))
	for _, rawRole := range request.Roles {
		role, err := normalizeAccessRole(rawRole, actor)
		if err != nil {
			return model.AccessControlView{}, err
		}
		proposed.Roles[role.ID] = role
		normalizedRoles = append(normalizedRoles, role)
	}
	normalizedPrincipals := make([]model.AccessPrincipal, 0, len(request.Principals))
	for _, rawPrincipal := range request.Principals {
		principal := rawPrincipal
		subject := canonicalAccessSubject(principal.Subject)
		if subject == "" {
			subject = canonicalAccessSubject(principal.Email)
		}
		if !config.IsAccessHash(subject) {
			return model.AccessControlView{}, errors.New("访问主体必须使用邮箱或 SHA-256 标识")
		}
		principal.Subject = subject
		principal.Email = config.NormalizeAccessSubject(principal.Email)
		if principal.Email != "" {
			principal.EmailHash = config.AccessHashForEmail(principal.Email)
		} else {
			principal.EmailHash = config.NormalizeAccessSubject(principal.EmailHash)
			if principal.EmailHash != "" && !config.IsAccessHash(principal.EmailHash) {
				return model.AccessControlView{}, errors.New("访问主体 emailHash 无效")
			}
		}
		if principal.EmailHash != "" && principal.EmailHash != subject {
			return model.AccessControlView{}, errors.New("访问主体邮箱与 subject 不一致")
		}
		if principal.TenantID == "" {
			principal.TenantID = proposed.DefaultTenant
		}
		principal.TenantID = strings.ToLower(strings.TrimSpace(principal.TenantID))
		if !tenantIsActive(proposed, principal.TenantID) {
			return model.AccessControlView{}, errors.New("访问主体租户无效")
		}
		if principal.Status == "" {
			principal.Status = "active"
		}
		if principal.Status != "active" && principal.Status != "disabled" && principal.Status != "suspended" {
			return model.AccessControlView{}, errors.New("访问主体状态无效")
		}
		if principal.ExpiresAt != nil {
			value := principal.ExpiresAt.UTC()
			principal.ExpiresAt = &value
		}
		for _, roleID := range principal.Roles {
			if _, ok := proposed.Roles[roleID]; !ok {
				return model.AccessControlView{}, errors.New("访问主体引用了未知角色")
			}
		}
		principal.CreatedBy = actor
		proposed.Principals[subject] = config.AccessPrincipal{
			Subject: subject, Email: principal.Email, EmailHash: principal.EmailHash,
			TenantID: principal.TenantID, Roles: append([]string(nil), principal.Roles...),
			Status: principal.Status, ExpiresAt: principal.ExpiresAt, JIT: principal.JIT,
		}
		principal.Roles = append([]string(nil), principal.Roles...)
		normalizedPrincipals = append(normalizedPrincipals, principal)
	}
	normalizedBindings := make([]model.RoleBinding, 0, len(request.Bindings))
	for _, rawBinding := range request.Bindings {
		binding := normalizeAccessBinding(rawBinding, actor)
		if err := engine.validateBinding(proposed, binding); err != nil {
			return model.AccessControlView{}, err
		}
		normalizedBindings = append(normalizedBindings, binding)
	}

	normalizedRemovePrincipals := make([]string, 0, len(request.RemovePrincipalSubjects))
	for _, subject := range request.RemovePrincipalSubjects {
		canonical := canonicalAccessSubject(subject)
		if !config.IsAccessHash(canonical) {
			return model.AccessControlView{}, errors.New("待删除访问主体无效")
		}
		delete(proposed.Principals, canonical)
		normalizedRemovePrincipals = append(normalizedRemovePrincipals, canonical)
	}
	normalizedRemoveRoles := normalizeIDs(request.RemoveRoleIDs)
	normalizedRemoveTenants := normalizeIDs(request.RemoveTenantIDs)
	normalizedRemoveBindings := normalizeIDs(request.RemoveBindingIDs)
	for _, roleID := range normalizedRemoveRoles {
		role, exists := proposed.Roles[roleID]
		if !exists {
			continue
		}
		if role.BuiltIn {
			return model.AccessControlView{}, errors.New("内置角色不可删除")
		}
		delete(proposed.Roles, roleID)
	}
	for _, tenantID := range normalizedRemoveTenants {
		if tenantID == proposed.DefaultTenant {
			return model.AccessControlView{}, errors.New("默认租户不可删除")
		}
		delete(proposed.Tenants, tenantID)
	}
	if request.Enforced != nil {
		proposed.Enforced = *request.Enforced
	}
	proposed.Bindings = mergeBindings(policy.Bindings, normalizedBindings)
	proposed.Bindings = removeBindings(proposed.Bindings, normalizedRemoveBindings)
	if err := engine.validateAccessTenantReferences(ctx, policy, proposed, snapshot.Version); err != nil {
		return model.AccessControlView{}, err
	}
	if !hasPlatformAdmin(proposed) {
		return model.AccessControlView{}, errors.New("不能撤销最后一个平台管理员")
	}
	if request.Enforced != nil {
		// The production schema gate above already rejects disabling enforcement;
		// keeping this assignment explicit makes the canonical snapshot complete.
		proposed.Enforced = *request.Enforced
	}
	policyJSON, err := json.Marshal(&proposed)
	if err != nil {
		return model.AccessControlView{}, err
	}
	requestDigest := digestText(string(policyJSON))
	expectedVersion := snapshot.Version
	if request.ExpectedVersion > 0 {
		expectedVersion = request.ExpectedVersion
	}
	mutation := store.AccessPolicyMutation{
		Actor: actor, IdempotencyKey: request.IdempotencyKey, RequestDigest: requestDigest,
		ExpectedVersion: expectedVersion,
		Snapshot:        model.AccessPolicySnapshot{Digest: requestDigest, PolicyJSON: string(policyJSON), ActorHash: actor},
		Tenants:         normalizedTenants, Roles: normalizedRoles, Bindings: normalizedBindings,
		RemoveTenantIDs: normalizedRemoveTenants, RemoveRoleIDs: normalizedRemoveRoles,
		RemoveBindingIDs: normalizedRemoveBindings,
		Audit: &model.AuditEntry{
			ActorHash: actor, Event: "access.policy.updated", Resource: "access", Outcome: "accepted",
			Detail: map[string]any{"bindingCount": len(normalizedBindings), "enforced": proposed.Enforced},
		},
	}
	if execution != nil {
		mutation.AccessChangeDigest = execution.requestDigest
		applied, err := engine.store.ApplyAccessChangeMutation(ctx, execution.changeID, actor, mutation)
		if err != nil {
			return model.AccessControlView{}, err
		}
		return model.AccessControlView{
			CurrentSubject: model.AccessSubject{Subject: actor},
			Version:        applied.AppliedPolicyVersion,
			Digest:         applied.AppliedPolicyDigest,
		}, nil
	}
	if _, _, err := engine.store.ApplyAccessPolicyMutation(ctx, mutation); err != nil {
		return model.AccessControlView{}, err
	}
	return engine.AccessControl(ctx, actor)
}

func (engine *Engine) validateBinding(
	policy *config.AccessPolicy,
	binding model.RoleBinding,
) error {
	subject := config.NormalizeAccessSubject(binding.Subject)
	if strings.Contains(subject, "@") {
		subject = config.AccessHashForEmail(subject)
	}
	if !config.IsAccessHash(subject) {
		return errors.New("角色绑定主体必须是邮箱或 SHA-256 标识")
	}
	if binding.TenantID == "" || (binding.TenantID != "*" && !tenantIsActive(policy, binding.TenantID)) {
		return errors.New("角色绑定租户无效")
	}
	if _, ok := policy.Roles[binding.RoleID]; !ok {
		return errors.New("角色绑定角色不存在")
	}
	binding.Subject = subject
	return nil
}

func mergeBindings(current, additions []model.RoleBinding) []model.RoleBinding {
	result := append([]model.RoleBinding(nil), current...)
	for _, addition := range additions {
		found := false
		for index := range result {
			if result[index].ID == addition.ID {
				result[index] = addition
				found = true
				break
			}
		}
		if !found {
			result = append(result, addition)
		}
	}
	return result
}

func removeBindings(current []model.RoleBinding, ids []string) []model.RoleBinding {
	removed := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		removed[id] = struct{}{}
	}
	result := make([]model.RoleBinding, 0, len(current))
	for _, binding := range current {
		if _, ok := removed[binding.ID]; !ok {
			result = append(result, binding)
		}
	}
	return result
}

func cloneRoles(input map[string]model.Role) map[string]model.Role {
	result := make(map[string]model.Role, len(input))
	for key, value := range input {
		value.Permissions = append([]model.Permission(nil), value.Permissions...)
		result[key] = value
	}
	return result
}

func cloneTenants(input map[string]model.Tenant) map[string]model.Tenant {
	result := make(map[string]model.Tenant, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}

func clonePrincipals(input map[string]config.AccessPrincipal) map[string]config.AccessPrincipal {
	result := make(map[string]config.AccessPrincipal, len(input))
	for key, value := range input {
		value.Roles = append([]string(nil), value.Roles...)
		result[key] = value
	}
	return result
}

func cloneAccessPolicy(input *config.AccessPolicy) *config.AccessPolicy {
	if input == nil {
		return nil
	}
	return &config.AccessPolicy{
		Enforced:      input.Enforced,
		DefaultTenant: input.DefaultTenant,
		Roles:         cloneRoles(input.Roles),
		Tenants:       cloneTenants(input.Tenants),
		Principals:    clonePrincipals(input.Principals),
		Bindings:      cloneBindings(input.Bindings),
	}
}

func cloneBindings(input []model.RoleBinding) []model.RoleBinding {
	result := make([]model.RoleBinding, 0, len(input))
	for _, value := range input {
		value.ObjectIDs = append([]string(nil), value.ObjectIDs...)
		result = append(result, value)
	}
	return result
}

func canonicalAccessSubject(value string) string {
	value = config.NormalizeAccessSubject(value)
	if strings.Contains(value, "@") {
		return config.AccessHashForEmail(value)
	}
	return value
}

// 必须在规范化前检查原始输入，避免撤销目标被转成另一条绑定的 ID。
func validateBindingRemovalIDs(values []string) error {
	for _, value := range values {
		id := strings.TrimSpace(value)
		if id != "" && strings.ToLower(id) != id {
			return errors.New("绑定撤销 ID 不支持大小写规范化后改变目标")
		}
	}
	return nil
}

func normalizeIDs(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(strings.ToLower(value))
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func hasPlatformAdmin(policy *config.AccessPolicy) bool {
	if policy == nil {
		return false
	}
	adminRoles := make(map[string]struct{})
	for roleID, role := range policy.Roles {
		if role.Allows(model.Permission("*")) {
			adminRoles[roleID] = struct{}{}
		}
	}
	now := time.Now().UTC()
	for subject, principal := range policy.Principals {
		if !principalUsable(policy, subject, principal, now) {
			continue
		}
		for _, roleID := range principal.Roles {
			if _, ok := adminRoles[roleID]; ok {
				return true
			}
		}
	}
	for _, binding := range policy.Bindings {
		if binding.ExpiresAt != nil && !now.Before(*binding.ExpiresAt) {
			continue
		}
		if _, ok := adminRoles[binding.RoleID]; ok {
			return true
		}
	}
	return false
}

// 审阅与应用共用字段规范化，避免展示值和最终写入值分叉。
func normalizeAccessTenant(tenant model.Tenant, actor string) (model.Tenant, error) {
	tenant.ID = strings.ToLower(strings.TrimSpace(tenant.ID))
	tenant.DisplayName = strings.TrimSpace(tenant.DisplayName)
	if tenant.ID == "" || tenant.DisplayName == "" || (tenant.Status != "" && tenant.Status != "active") {
		return model.Tenant{}, errors.New("租户定义无效或已禁用")
	}
	if tenant.Status == "" {
		tenant.Status = "active"
	}
	tenant.CreatedBy = actor
	return tenant, nil
}

// 与实际应用共用同一完整替换规范化，不合并遗漏字段。
func normalizeAccessBinding(rawBinding model.RoleBinding, actor string) model.RoleBinding {
	binding := rawBinding
	binding.ID = strings.TrimSpace(binding.ID)
	binding.Subject = canonicalAccessSubject(binding.Subject)
	binding.TenantID = strings.ToLower(strings.TrimSpace(binding.TenantID))
	binding.RoleID = strings.ToLower(strings.TrimSpace(binding.RoleID))
	binding.CreatedBy = actor
	return binding
}
