package runner

import (
	"context"
	"errors"

	"github.com/AreaSong/ops/services/areasong-ops/internal/config"
	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

func (engine *Engine) KubernetesView(
	ctx context.Context,
	actor string,
) (map[string]any, error) {
	if err := engine.authorizePlatform(ctx, actor, model.PermissionRead, "kubernetes"); err != nil {
		return nil, err
	}
	if len(engine.catalog.Kubernetes) == 0 {
		return nil, errors.New("Kubernetes 尚未配置")
	}
	policy, _, err := engine.effectiveAccessPolicy(ctx)
	if err != nil {
		return nil, err
	}
	tenantID := accessPolicyActorTenant(policy, actor)
	platform := accessPolicyPlatformAdmin(policy, actor)
	operations, err := engine.store.ListKubernetesOperations(ctx, 50)
	if err != nil {
		return nil, err
	}
	if !platform {
		operations, err = engine.store.ListKubernetesOperationsForTenant(ctx, tenantID, 50)
		if err != nil {
			return nil, err
		}
	}
	targets := make(map[string]model.KubernetesTarget)
	for name, target := range engine.catalog.Kubernetes {
		if platform || target.TenantID == tenantID {
			targets[name] = target
		}
	}
	return map[string]any{"enabled": true, "targets": targets, "operations": operations}, nil
}

func (engine *Engine) ExtensionPolicyView(
	ctx context.Context,
	actor string,
) (map[string]any, error) {
	if err := engine.authorizePlatform(ctx, actor, model.PermissionRead, "extensions"); err != nil {
		return nil, err
	}
	if engine.catalog.Extensions == nil {
		return nil, errors.New("扩展策略尚未配置")
	}
	packages, err := engine.store.ListExtensionPackages(ctx)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"enabled":             engine.catalog.Extensions.Enabled,
		"trustedPublishers":   engine.catalog.Extensions.TrustedPublishers,
		"requireSignature":    engine.catalog.Extensions.RequireSignature,
		"sandbox":             engine.catalog.Extensions.Sandbox,
		"maxPackageBytes":     engine.catalog.Extensions.MaxPackageBytes,
		"maxInputBytes":       engine.catalog.Extensions.MaxInputBytes,
		"maxOutputBytes":      engine.catalog.Extensions.MaxOutputBytes,
		"maxExecutionSeconds": engine.catalog.Extensions.MaxExecutionSeconds,
		"maxMemoryPages":      engine.catalog.Extensions.MaxMemoryPages,
		"extensions":          packages,
	}, nil
}

func accessPolicyActorTenant(policy *config.AccessPolicy, actor string) string {
	if policy != nil {
		if principal, ok := policy.Principals[actor]; ok && principal.TenantID != "" {
			return principal.TenantID
		}
		if policy.DefaultTenant != "" {
			return policy.DefaultTenant
		}
	}
	return "default"
}

func accessPolicyPlatformAdmin(policy *config.AccessPolicy, actor string) bool {
	if policy == nil {
		return false
	}
	principal, ok := policy.Principals[actor]
	return ok && principalIsPlatformAdmin(policy, principal)
}
