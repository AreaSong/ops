package runner

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

func TestAutoUpdateWindowUsesExplicitTimezone(t *testing.T) {
	now := time.Date(2026, 9, 3, 18, 30, 0, 0, time.UTC)
	if !autoUpdateWindowOpen("02:00-04:00", "Asia/Shanghai", now) {
		t.Fatal("Shanghai maintenance window should be open")
	}
	if autoUpdateWindowOpen("02:00-04:00", "UTC", now) {
		t.Fatal("UTC maintenance window should be closed")
	}
	if !autoUpdateWindowOpen("23:00-03:00", "Asia/Shanghai", now) {
		t.Fatal("cross-midnight maintenance window should be open")
	}
	if autoUpdateWindowOpen("02:00-04:00", "Not/AZone", now) {
		t.Fatal("invalid timezone should never open a maintenance window")
	}
}

func TestEvaluateAutoUpdatesPropagatesEvaluationWriteFailure(t *testing.T) {
	ctx := context.Background()
	engine, database := testEngine(t, &fakeExecutor{})
	if err := database.UpsertAutoUpdatePolicy(ctx, model.AutoUpdatePolicyView{
		Service: "demo", ObjectID: "service:demo", TenantID: "default",
		Enabled: true, Channel: "stable", MaintenanceTimezone: "UTC",
		RequireBackup: true, RequireApproval: true, RollbackOnAlert: true,
		ObservationSeconds: 300,
	}); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", filepath.Join(engine.stateRoot, "ops.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.ExecContext(ctx, `CREATE TRIGGER reject_auto_update_evaluation
		BEFORE UPDATE ON auto_update_policies
		WHEN NEW.last_evaluation_at IS NOT NULL
		BEGIN SELECT RAISE(ABORT, 'injected evaluation failure'); END;`); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.EvaluateAutoUpdates(ctx, actorHash()); err == nil || !strings.Contains(err.Error(), "injected evaluation failure") {
		t.Fatalf("err=%v, want evaluation persistence failure", err)
	}
}

func TestEvaluateAutoUpdatesCreatesDualApprovalPlan(t *testing.T) {
	ctx := context.Background()
	engine, database := testEngine(t, &fakeExecutor{})
	discoverRelease(t, engine)
	if err := database.UpsertAutoUpdatePolicy(ctx, model.AutoUpdatePolicyView{
		Service: "demo", ObjectID: "service:demo", TenantID: "default",
		Enabled: true, Channel: "stable", MaintenanceTimezone: "UTC",
		RequireBackup: true, RequireApproval: true, RollbackOnAlert: true,
		ObservationSeconds: 300,
	}); err != nil {
		t.Fatal(err)
	}
	evaluations, err := engine.EvaluateAutoUpdates(ctx, actorHash())
	if err != nil || len(evaluations) != 1 || !evaluations[0].UpdateCreated {
		t.Fatalf("evaluations=%+v err=%v", evaluations, err)
	}
	plan, err := database.GetReleasePlan(ctx, evaluations[0].PlanID)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Risk != model.RiskHigh || !plan.RequiresDualApproval || plan.State != model.PlanPendingApproval {
		t.Fatalf("automatic update plan weakened approval policy: %+v", plan)
	}
}

func TestValidateAutoUpdatePolicyNormalizesAndRejectsInvalidTimePolicy(t *testing.T) {
	policy := &model.AutoUpdatePolicy{Channel: "stable", MaintenanceWindow: "02:00-04:00Z"}
	if err := validateAutoUpdatePolicyInput("demo", policy); err != nil {
		t.Fatal(err)
	}
	if policy.MaintenanceWindow != "02:00-04:00" || policy.MaintenanceTimezone != "UTC" {
		t.Fatalf("policy was not normalized: %+v", policy)
	}
	for _, invalid := range []*model.AutoUpdatePolicy{
		{Channel: "stable", MaintenanceWindow: "2:00-04:00", MaintenanceTimezone: "UTC"},
		{Channel: "stable", MaintenanceWindow: "02:00-04:00", MaintenanceTimezone: "Not/AZone"},
	} {
		if err := validateAutoUpdatePolicyInput("demo", invalid); err == nil {
			t.Fatalf("invalid time policy accepted: %+v", invalid)
		}
	}
}
