package runner

import (
	"errors"
	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	"strings"
)

// 复用既有应用规范化；只读投影的支持范围不改变写入规则。
func normalizeAccessRole(role model.Role, actor string) (model.Role, error) {
	role.ID = strings.ToLower(strings.TrimSpace(role.ID))
	role.DisplayName = strings.TrimSpace(role.DisplayName)
	if role.ID == "" || role.DisplayName == "" || role.BuiltIn {
		return model.Role{}, errors.New("自定义角色定义无效")
	}
	if len(role.Permissions) == 0 {
		return model.Role{}, errors.New("自定义角色必须至少包含一个权限")
	}
	role.CreatedBy = actor
	role.Permissions = append([]model.Permission(nil), role.Permissions...)
	return role, nil
}

// 仅检查本次提交的角色，避免无关变更重验存量或 bootstrap 策略。
func validateAccessRolePermissions(roles []model.Role) error {
	for _, role := range roles {
		if len(role.Permissions) == 0 {
			return errors.New("自定义角色必须至少包含一个权限")
		}
		if !registeredRolePermissions(role.Permissions) {
			return errors.New("自定义角色包含未登记权限")
		}
	}
	return nil
}

// 写入与只读投影共用 model/control.go 的登记值，不改变存量授权解释。
func registeredRolePermissions(permissions []model.Permission) bool {
	if len(permissions) == 0 {
		return false
	}
	for _, permission := range permissions {
		switch permission {
		case model.PermissionRead, model.PermissionInspect, model.PermissionLifecycle,
			model.PermissionDeploy, model.PermissionBatch, model.PermissionRecover,
			model.PermissionManageFleet, model.PermissionManageAccess, model.PermissionManageConfig,
			model.PermissionBreakGlass, model.PermissionRunnerUpdate:
		default:
			return false
		}
	}
	return true
}
