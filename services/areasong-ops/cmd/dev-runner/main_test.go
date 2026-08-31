package main

import (
	"path/filepath"
	"testing"

	"github.com/AreaSong/ops/services/areasong-ops/internal/config"
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
