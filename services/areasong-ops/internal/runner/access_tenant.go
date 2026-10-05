package runner

import (
	"context"
	"errors"
	"reflect"

	"github.com/AreaSong/ops/services/areasong-ops/internal/config"
	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	"github.com/AreaSong/ops/services/areasong-ops/internal/store"
)

func validateOrdinaryTenantUpdate(policy *config.AccessPolicy, tenant model.Tenant) error {
	if current, found := policy.Tenants[tenant.ID]; found && current.Status != "" && current.Status != tenant.Status {
		return store.ErrTenantLifecycleConflict
	}
	return nil
}

// 仅保留内容未改、当前代次已知的停用引用；Store 提交事务再次核验镜像。
func (engine *Engine) validateAccessTenantReferences(ctx context.Context, before, proposed *config.AccessPolicy, version int64) error {
	checked := make(map[string]bool)
	retained := func(id string) bool {
		if checked[id] {
			return true
		}
		old, ok := before.Tenants[id]
		current, exists := proposed.Tenants[id]
		if !ok || !exists || old.Status != "disabled" || current.Status != "disabled" {
			return false
		}
		value, err := engine.store.GetTenantLifecycle(ctx, id)
		if err != nil || value.Status != "disabled" || value.Generation <= 0 || value.PolicyVersion != version {
			return false
		}
		checked[id] = true
		return true
	}
	bindings := make(map[string]model.RoleBinding)
	for _, binding := range before.Bindings {
		bindings[binding.ID] = binding
	}
	for _, binding := range proposed.Bindings {
		if _, ok := proposed.Roles[binding.RoleID]; !ok {
			return errors.New("现有绑定引用了已删除角色")
		}
		if binding.TenantID == "*" || tenantIsActive(proposed, binding.TenantID) {
			continue
		}
		old, ok := bindings[binding.ID]
		if !ok || !reflect.DeepEqual(old, binding) || !retained(binding.TenantID) {
			return errors.New("现有绑定引用了不可用租户")
		}
	}
	for subject, principal := range proposed.Principals {
		if !tenantIsActive(proposed, principal.TenantID) {
			old, ok := before.Principals[subject]
			if !ok || !reflect.DeepEqual(old, principal) || !retained(principal.TenantID) {
				return errors.New("现有访问主体引用了不可用租户")
			}
		}
		for _, roleID := range principal.Roles {
			if _, ok := proposed.Roles[roleID]; !ok {
				return errors.New("现有访问主体引用了已删除角色")
			}
		}
	}
	return nil
}
