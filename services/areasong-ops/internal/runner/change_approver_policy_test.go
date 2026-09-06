package runner

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/AreaSong/ops/services/areasong-ops/internal/config"
	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	"github.com/AreaSong/ops/services/areasong-ops/internal/store"
)

func TestProductionChangeApproverHasOnlyDeclaredApprovalScope(t *testing.T) {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime caller unavailable")
	}
	catalogPath := filepath.Join(filepath.Dir(currentFile), "..", "..", "config", "services.example.json")
	catalog, err := config.Load(catalogPath, false)
	if err != nil {
		t.Fatal(err)
	}
	database, err := store.Open(filepath.Join(t.TempDir(), "ops.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	engine, err := NewEngineChecked(catalog, database, &fakeExecutor{}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	approver := config.AccessHashForEmail("3177348309@qq.com")
	ctx := context.Background()

	serviceChecks := []struct {
		permission model.Permission
		objectID   string
	}{
		{model.PermissionLifecycle, "service:areaforge"},
		{model.PermissionDeploy, "service:sub2api"},
		{model.PermissionBatch, "service:areaforge"},
		{model.PermissionRecover, "service:sub2api"},
		{model.PermissionManageConfig, "service:areaforge"},
		{model.PermissionBreakGlass, "service:sub2api"},
		{model.PermissionManageConfig, "file:ops-config"},
	}
	for _, check := range serviceChecks {
		if err := engine.authorize(ctx, approver, check.permission, check.objectID); err != nil {
			t.Fatalf("authorize %s on %s: %v", check.permission, check.objectID, err)
		}
	}
	platformChecks := []struct {
		permission model.Permission
		objectID   string
	}{
		{model.PermissionManageAccess, "access"},
		{model.PermissionManageConfig, "extensions"},
		{model.PermissionRead, "terminal"},
		{model.PermissionRead, "kubernetes"},
		{model.PermissionDeploy, "kubernetes:plans"},
		{model.PermissionDeploy, "kubernetes:areasong-production"},
		{model.PermissionRunnerUpdate, "runner:runner-losangeles"},
	}
	for _, check := range platformChecks {
		if err := engine.authorizePlatform(ctx, approver, check.permission, check.objectID); err != nil {
			t.Fatalf("authorize platform %s on %s: %v", check.permission, check.objectID, err)
		}
	}
	deniedChecks := []struct {
		permission model.Permission
		objectID   string
	}{
		{model.PermissionManageConfig, "credentials"},
		{model.PermissionManageFleet, "fleet"},
		{model.PermissionRunnerUpdate, "runner:unregistered"},
	}
	for _, check := range deniedChecks {
		if err := engine.authorizePlatform(ctx, approver, check.permission, check.objectID); err == nil {
			t.Fatalf("unexpected platform authorization %s on %s", check.permission, check.objectID)
		}
	}
}
