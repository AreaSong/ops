package runner

import (
	"regexp"
	"strings"

	"github.com/AreaSong/ops/services/areasong-ops/internal/config"
	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

// 以下限制仅定义只读审阅支持范围，不参与删除写入校验或归一化。
var reviewRoleIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

type roleDeletionReferences struct {
	Bindings         string `json:"bindings"`
	DirectPrincipals string `json:"directPrincipals"`
}

type roleDeletionReview struct {
	Before     roleReviewValue        `json:"before"`
	References roleDeletionReferences `json:"references"`
}

func pureRoleDeletionRequest(r model.AccessControlUpdateRequest) bool {
	return len(r.RemoveRoleIDs) == 1 && len(r.Roles) == 0 && len(r.Tenants) == 0 && len(r.Bindings) == 0 &&
		len(r.Principals) == 0 && len(r.RemoveTenantIDs) == 0 && len(r.RemoveBindingIDs) == 0 &&
		len(r.RemovePrincipalSubjects) == 0 && r.Enforced == nil && r.Confirmation == "" && r.RequiresDualApproval
}

func roleDeletionDiff(id string, policy *config.AccessPolicy) (*roleDeletionReview, string) {
	if !reviewRoleIDPattern.MatchString(id) {
		return nil, "invalid_role_id"
	}
	if protectedRoleID(id) {
		return nil, "protected_role"
	}
	before, exists := policy.Roles[id]
	if !exists {
		return nil, "role_not_found"
	}
	if before.BuiltIn || !actorPattern.MatchString(before.CreatedBy) {
		return nil, "protected_role"
	}
	if before.ID != id || strings.TrimSpace(before.DisplayName) == "" || !registeredRolePermissions(before.Permissions) {
		return nil, "incompatible_role"
	}
	for key, role := range policy.Roles {
		if key != id && (strings.ToLower(strings.TrimSpace(key)) == id || strings.ToLower(strings.TrimSpace(role.ID)) == id) {
			return nil, "incompatible_role"
		}
	}
	if reason := roleDeletionReferenceReason(policy, id); reason != "" {
		return nil, reason
	}
	return &roleDeletionReview{
		Before:     roleReviewValue{before.ID, before.DisplayName, before.Permissions},
		References: roleDeletionReferences{Bindings: "none", DirectPrincipals: "none"},
	}, ""
}

// 与 before 共用已验摘要的历史快照。损坏、悬空或含糊的引用不作乐观零值投影。
func roleDeletionReferenceReason(policy *config.AccessPolicy, roleID string) string {
	for subject, principal := range policy.Principals {
		if !actorPattern.MatchString(subject) || principal.Subject != subject || !reviewReferenceTenant(policy, principal.TenantID) {
			return "unsupported_reference_scope"
		}
		for _, id := range principal.Roles {
			if !reviewReferenceRole(policy, id) {
				return "unsupported_reference_scope"
			}
			if id == roleID {
				return "principal_reference"
			}
		}
	}
	seen := map[string]bool{}
	bound := false
	for _, binding := range policy.Bindings {
		if binding.ID == "" || binding.ID != strings.TrimSpace(binding.ID) || seen[binding.ID] ||
			!actorPattern.MatchString(binding.Subject) || !reviewReferenceRole(policy, binding.RoleID) ||
			!reviewReferenceTenant(policy, binding.TenantID) {
			return "unsupported_reference_scope"
		}
		seen[binding.ID] = true
		// 不按有效期、审批状态或主体状态过滤，最终写入同样检查所有绑定。
		bound = bound || binding.RoleID == roleID
	}
	if bound {
		return "binding_reference"
	}
	return ""
}

func reviewReferenceRole(policy *config.AccessPolicy, id string) bool {
	role, found := policy.Roles[id]
	return found && role.ID == id && reviewRoleIDPattern.MatchString(id)
}

func reviewReferenceTenant(policy *config.AccessPolicy, id string) bool {
	tenant, found := policy.Tenants[id]
	return found && tenant.ID == id && id != "" && id != "*" && id == strings.ToLower(strings.TrimSpace(id)) && tenantIsActive(policy, id)
}
