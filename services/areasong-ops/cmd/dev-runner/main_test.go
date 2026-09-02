package main

import (
	"path/filepath"
	"testing"

	"github.com/AreaSong/ops/services/areasong-ops/internal/config"
	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

func TestApplyDevelopmentFeatureOverridesIsOptInAndScoped(t *testing.T) {
	runtimeRoot := t.TempDir()
	catalog := &config.Catalog{
		Terminal:     &config.TerminalPolicy{},
		Files:        &config.FilePolicy{},
		Extensions:   &config.ExtensionPolicy{},
		RunnerUpdate: &config.RunnerUpdatePolicy{},
	}
	applyDevelopmentFeatureOverrides(catalog)
	if catalog.Terminal.Enabled || catalog.Files.Enabled || catalog.Extensions.Enabled || catalog.RunnerUpdate.Enabled {
		t.Fatal("development features changed without explicit opt-in")
	}
	t.Setenv("OPS_DEV_ENABLE_FEATURES", "all")
	t.Setenv("OPS_DEV_RUNTIME_ROOT", runtimeRoot)
	applyDevelopmentFeatureOverrides(catalog)
	if !catalog.Terminal.Enabled || catalog.Terminal.Commands["service-status"].Executable != "/bin/echo" {
		t.Fatalf("terminal override=%+v", catalog.Terminal)
	}
	if !catalog.Files.Enabled || catalog.Files.Roots["ops-config"] != runtimeRoot {
		t.Fatalf("files override=%+v", catalog.Files)
	}
	if !catalog.Extensions.Enabled || !catalog.RunnerUpdate.Enabled {
		t.Fatalf("policy overrides extensions=%+v runner=%+v", catalog.Extensions, catalog.RunnerUpdate)
	}
	if catalog.RunnerUpdate.ArtifactRoot != filepath.Join(runtimeRoot, "runner-updates", "incoming") {
		t.Fatalf("runner artifact root=%s", catalog.RunnerUpdate.ArtifactRoot)
	}
	if catalog.Terminal.BreakGlass {
		t.Fatal("break-glass must remain opt-in")
	}
	t.Setenv("OPS_DEV_ENABLE_BREAK_GLASS", "1")
	applyDevelopmentFeatureOverrides(catalog)
	if !catalog.Terminal.BreakGlass || catalog.Terminal.ShellWorkingDir != filepath.Join(runtimeRoot, "shell") {
		t.Fatalf("break-glass override=%+v", catalog.Terminal)
	}
}

func TestApplyDevelopmentAccessOverrideIsExplicit(t *testing.T) {
	catalog := &config.Catalog{Access: &config.AccessPolicy{
		DefaultTenant: "production",
		Principals:    map[string]config.AccessPrincipal{},
		Roles: map[string]model.Role{
			"platform-admin": {ID: "platform-admin", Permissions: []model.Permission{"*"}},
		},
	}}
	if err := applyDevelopmentAccessOverride(catalog); err != nil {
		t.Fatal(err)
	}
	if len(catalog.Access.Principals) != 0 {
		t.Fatal("development admin changed without explicit opt-in")
	}
	t.Setenv("OPS_DEV_ADMIN_EMAIL", "Admin@Example.Test")
	if err := applyDevelopmentAccessOverride(catalog); err != nil {
		t.Fatal(err)
	}
	hash := config.AccessHashForEmail("admin@example.test")
	principal := catalog.Access.Principals[hash]
	if principal.Email != "admin@example.test" || principal.TenantID != "production" ||
		len(principal.Roles) != 1 || principal.Roles[0] != "platform-admin" {
		t.Fatalf("principal=%+v", principal)
	}
	if err := applyDevelopmentAccessOverride(catalog); err != nil {
		t.Fatal(err)
	}
	principal = catalog.Access.Principals[hash]
	if len(principal.Roles) != 1 {
		t.Fatalf("repeat principal=%+v", principal)
	}
}
