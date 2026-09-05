package store

import (
	"context"
	"strings"
	"testing"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

func TestAutoUpdatePolicyTimezoneRoundTripAndLegacyDefault(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	policy := model.AutoUpdatePolicyView{
		Service: "demo", ObjectID: "service:demo", TenantID: "tenant-a",
		Channel: "stable", MaintenanceWindow: "02:00-04:00", MaintenanceTimezone: "Asia/Shanghai",
		RequireBackup: true, RequireApproval: true, RollbackOnAlert: true, ObservationSeconds: 300,
	}
	if err := database.UpsertAutoUpdatePolicy(ctx, policy); err != nil {
		t.Fatal(err)
	}
	stored, err := database.GetAutoUpdatePolicy(ctx, policy.Service)
	if err != nil || stored.MaintenanceTimezone != policy.MaintenanceTimezone {
		t.Fatalf("stored=%+v err=%v", stored, err)
	}

	legacy := model.AutoUpdatePolicyView{
		Service: "legacy", ObjectID: "service:legacy", TenantID: "tenant-a",
		Channel: "stable", MaintenanceWindow: "03:00-05:00", ObservationSeconds: 300,
	}
	if err := database.UpsertAutoUpdatePolicy(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	stored, err = database.GetAutoUpdatePolicy(ctx, legacy.Service)
	if err != nil || stored.MaintenanceTimezone != "UTC" {
		t.Fatalf("legacy stored=%+v err=%v", stored, err)
	}
}

func TestApplyAutoUpdatePolicyRollsBackWhenAuditFails(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	if _, err := database.db.ExecContext(ctx, `CREATE TRIGGER reject_auto_update_policy_audit
		BEFORE INSERT ON audit_entries
		WHEN NEW.event = 'auto_update.policy.changed'
		BEGIN SELECT RAISE(ABORT, 'injected audit failure'); END;`); err != nil {
		t.Fatal(err)
	}
	policy := model.AutoUpdatePolicyView{
		Service: "demo", ObjectID: "service:demo", TenantID: "default", Enabled: true,
		Channel: "stable", MaintenanceTimezone: "UTC", RequireBackup: true,
		RequireApproval: true, RollbackOnAlert: true, ObservationSeconds: 300,
	}
	created, err := database.ApplyAutoUpdatePolicy(ctx, strings.Repeat("a", 64),
		"11111111-1111-4111-8111-111111111111", "sha256:request", policy,
		model.AuditEntry{Event: "auto_update.policy.changed", Resource: policy.ObjectID, Outcome: "accepted"})
	if err == nil || created {
		t.Fatalf("created=%v err=%v, want atomic audit failure", created, err)
	}
	if _, err := database.GetAutoUpdatePolicy(ctx, policy.Service); err != ErrNotFound {
		t.Fatalf("policy survived rollback: %v", err)
	}
	var receipts int
	if err := database.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM auto_update_receipts`).Scan(&receipts); err != nil || receipts != 0 {
		t.Fatalf("receipts=%d err=%v", receipts, err)
	}
}
