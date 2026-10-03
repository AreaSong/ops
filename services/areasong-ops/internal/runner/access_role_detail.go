package runner

import (
	"sort"
	"strings"

	"github.com/AreaSong/ops/services/areasong-ops/internal/config"
	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

// 独立白名单类型避免模型新增字段自动扩大读取范围。
type roleReviewValue struct {
	ID          string             `json:"id"`
	DisplayName string             `json:"displayName"`
	Permissions []model.Permission `json:"permissions"`
}

type rolePermissionDiff struct {
	Added     []model.Permission `json:"added"`
	Removed   []model.Permission `json:"removed"`
	Unchanged []model.Permission `json:"unchanged"`
}

type roleReviewImpact struct {
	BindingCount            int  `json:"bindingCount"`
	TenantCount             int  `json:"tenantCount"`
	AffectsExistingBindings bool `json:"affectsExistingBindings"`
}

type roleReview struct {
	Before         *roleReviewValue   `json:"before,omitempty"`
	After          roleReviewValue    `json:"after"`
	PermissionDiff rolePermissionDiff `json:"permissionDiff"`
	Impact         roleReviewImpact   `json:"impact"`
}

func pureRoleRequest(r model.AccessControlUpdateRequest) bool {
	return len(r.Roles) == 1 && len(r.Tenants) == 0 && len(r.Bindings) == 0 && len(r.Principals) == 0 &&
		len(r.RemoveTenantIDs) == 0 && len(r.RemoveRoleIDs) == 0 && len(r.RemoveBindingIDs) == 0 &&
		len(r.RemovePrincipalSubjects) == 0 && r.Enforced == nil && r.Confirmation == "" && r.RequiresDualApproval
}

func roleReviewDiff(raw model.Role, actor string, policy *config.AccessPolicy) (*roleReview, string, string) {
	if raw.CreatedBy != "" || !raw.CreatedAt.IsZero() || !raw.UpdatedAt.IsZero() {
		return nil, "", "role_metadata"
	}
	after, err := normalizeAccessRole(raw, actor)
	if raw.BuiltIn || protectedRoleID(after.ID) {
		return nil, "", "protected_role"
	}
	if err != nil || !registeredRolePermissions(after.Permissions) {
		return nil, "", "invalid_role_or_permissions"
	}
	before, exists := policy.Roles[after.ID]
	if exists && (before.BuiltIn || before.CreatedBy == "" || before.CreatedBy == "bootstrap") {
		return nil, "", "protected_role"
	}
	if exists && (before.ID != after.ID || before.DisplayName == "" || !registeredRolePermissions(before.Permissions)) {
		return nil, "", "incompatible_role"
	}
	impact, supported := roleBindingImpact(policy, after.ID)
	if !supported || (!exists && impact.BindingCount != 0) {
		return nil, "", "unsupported_reference_scope"
	}
	result := &roleReview{After: roleReviewValue{after.ID, after.DisplayName, after.Permissions}, Impact: impact}
	operation := "create"
	if exists {
		result.Before = &roleReviewValue{before.ID, before.DisplayName, before.Permissions}
		operation = "edit"
	}
	result.PermissionDiff = permissionSetDiff(before.Permissions, after.Permissions)
	return result, operation, ""
}

// Store 默认种子可能不在某个目录快照中；这些 ID 仍不能被误判为可新增。
func protectedRoleID(id string) bool {
	switch id {
	case "viewer", "operator", "release-manager", "platform-admin":
		return true
	}
	return false
}

func permissionSetDiff(before, after []model.Permission) rolePermissionDiff {
	old, next := map[model.Permission]bool{}, map[model.Permission]bool{}
	for _, p := range before {
		old[p] = true
	}
	for _, p := range after {
		next[p] = true
	}
	result := rolePermissionDiff{Added: []model.Permission{}, Removed: []model.Permission{}, Unchanged: []model.Permission{}}
	for p := range next {
		if old[p] {
			result.Unchanged = append(result.Unchanged, p)
		} else {
			result.Added = append(result.Added, p)
		}
	}
	for p := range old {
		if !next[p] {
			result.Removed = append(result.Removed, p)
		}
	}
	for _, values := range [][]model.Permission{result.Added, result.Removed, result.Unchanged} {
		sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	}
	return result
}

func roleBindingImpact(policy *config.AccessPolicy, roleID string) (roleReviewImpact, bool) {
	result := roleReviewImpact{}
	// 主体直接角色和通配租户不能伪装成零绑定/一个租户；留给后续明确的影响合同。
	for _, principal := range policy.Principals {
		for _, id := range principal.Roles {
			if id == roleID {
				return result, false
			}
		}
	}
	tenants, bindings := map[string]bool{}, map[string]bool{}
	for _, binding := range policy.Bindings {
		if binding.RoleID != roleID {
			continue
		}
		tenant, found := policy.Tenants[binding.TenantID]
		if !found || tenant.ID != binding.TenantID || binding.TenantID == "*" ||
			binding.TenantID != strings.ToLower(strings.TrimSpace(binding.TenantID)) || binding.ID == "" || bindings[binding.ID] {
			return result, false
		}
		bindings[binding.ID], tenants[binding.TenantID] = true, true
		result.BindingCount++
	}
	result.TenantCount = len(tenants)
	result.AffectsExistingBindings = result.BindingCount > 0
	return result, true
}
