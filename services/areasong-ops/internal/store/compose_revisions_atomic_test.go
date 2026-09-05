package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

func TestComposeRevisionStateAndAuditAreAtomic(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "ops.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	ctx := context.Background()
	now := time.Now().UTC().Round(0)
	database.now = func() time.Time { return now }
	revision := atomicComposeRevision(now)

	installComposeAuditFailure(t, database)
	if _, _, err := database.SaveComposeRevisionIdempotent(ctx, revision, "sha256:request"); err == nil {
		t.Fatal("proposal succeeded when its audit insert failed")
	}
	if _, err := database.GetComposeRevision(ctx, revision.ID); err != ErrNotFound {
		t.Fatalf("proposal survived audit rollback: %v", err)
	}
	dropComposeAuditFailure(t, database)
	if _, created, err := database.SaveComposeRevisionIdempotent(ctx, revision, "sha256:request"); err != nil || !created {
		t.Fatalf("created=%v err=%v", created, err)
	}

	installComposeAuditFailure(t, database)
	if _, err := database.ApproveComposeRevision(ctx, revision.ID, strings.Repeat("b", 64), revision.Digest, revision.ConfirmationPhrase); err == nil {
		t.Fatal("approval succeeded when its audit insert failed")
	}
	stored, err := database.GetComposeRevision(ctx, revision.ID)
	if err != nil || stored.State != "proposed" || stored.ApprovedBy != "" {
		t.Fatalf("approval state was not rolled back: %+v err=%v", stored, err)
	}
	dropComposeAuditFailure(t, database)

	verifiedAt, recoverableUntil := now, now.Add(time.Hour)
	if _, err := database.db.Exec(`INSERT INTO tasks(id,idempotency_key,request_hash,actor_hash,service,action,target,risk,state,current_phase,summary,error,preview_id,snapshot_json,created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, "task-compose", "task-compose-key", "sha256:request",
		strings.Repeat("a", 64), revision.Service, "backup", "", "high", "running", "backup", "", "",
		"preview-compose", `{}`, timeText(now)); err != nil {
		t.Fatal(err)
	}
	if _, err := database.db.Exec(`INSERT INTO recovery_points(
		id,task_id,service,status,evidence_json,evidence_digest,expected_before_digest,
		required_roles_json,created_at,verified_at,recoverable_until,tenant_id,server_id,
		expected_before_json,binding_digest,restore_outcome,restore_evidence_digest)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		revision.RecoveryPointID, "task-compose", revision.Service, "verified", `{}`,
		revision.RecoveryPointEvidenceDigest, revision.RecoveryPointExpectedDigest, `[]`,
		timeText(now), timeText(verifiedAt), timeText(recoverableUntil), revision.TenantID,
		revision.ServerID, `{}`, revision.RecoveryPointBindingDigest, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := database.db.Exec(`UPDATE compose_revisions SET state='approved',approved_by=?,second_approved_by_hash=?,recovery_point_verified_at=?,recovery_point_recoverable_until=? WHERE id=?`,
		strings.Repeat("b", 64), strings.Repeat("c", 64), timeText(verifiedAt), timeText(recoverableUntil), revision.ID); err != nil {
		t.Fatal(err)
	}
	revision.RecoveryPointVerifiedAt, revision.RecoveryPointRecoverableUntil = &verifiedAt, &recoverableUntil

	installComposeAuditFailure(t, database)
	gate := model.ComposeExecutionGate{
		PolicyDigest: revision.PolicyDigest, RecoveryPointID: revision.RecoveryPointID,
		RecoveryPointExpectedDigest: revision.RecoveryPointExpectedDigest,
		RecoveryPointBindingDigest:  revision.RecoveryPointBindingDigest,
		RecoveryPointEvidenceDigest: revision.RecoveryPointEvidenceDigest,
		AlertEvidenceDigest:         "sha256:alerts", CheckedAt: now,
		ExpectedRuntimeIdentityDigest: revision.ExpectedRuntimeIdentityDigest,
	}
	if _, _, err := database.StartComposeApply(ctx, revision.ID, strings.Repeat("d", 64), "apply-key", gate); err == nil {
		t.Fatal("apply start succeeded when its audit insert failed")
	}
	stored, _ = database.GetComposeRevision(ctx, revision.ID)
	if stored.State != "approved" || stored.ApplyIdempotencyKey != "" {
		t.Fatalf("apply start state was not rolled back: %+v", stored)
	}
	dropComposeAuditFailure(t, database)
	if _, err := database.db.Exec(`UPDATE compose_revisions SET state='applying',applied_by_hash=?,apply_idempotency_key=? WHERE id=?`, strings.Repeat("d", 64), "apply-key", revision.ID); err != nil {
		t.Fatal(err)
	}
	installComposeAuditFailure(t, database)
	if err := database.FinishComposeApply(ctx, revision.ID, "applied", "/backup/a", "/backup/b", "", "sha256:runtime"); err == nil {
		t.Fatal("apply finish succeeded when its audit insert failed")
	}
	stored, _ = database.GetComposeRevision(ctx, revision.ID)
	if stored.State != "applying" || stored.BackupControlledPath != "" {
		t.Fatalf("apply finish survived rollback: %+v", stored)
	}
	dropComposeAuditFailure(t, database)

	if _, err := database.db.Exec(`UPDATE compose_revisions SET state='applied',backup_controlled_path='/backup/a',backup_runtime_path='/backup/b' WHERE id=?`, revision.ID); err != nil {
		t.Fatal(err)
	}
	installComposeAuditFailure(t, database)
	if _, _, err := database.StartComposeRollback(ctx, revision.ID, strings.Repeat("e", 64), "rollback-key"); err == nil {
		t.Fatal("rollback start succeeded when its audit insert failed")
	}
	stored, _ = database.GetComposeRevision(ctx, revision.ID)
	if stored.State != "applied" || stored.RollbackIdempotencyKey != "" {
		t.Fatalf("rollback start survived rollback: %+v", stored)
	}
	dropComposeAuditFailure(t, database)
	if _, err := database.db.Exec(`UPDATE compose_revisions SET state='rolling_back',rolled_back_by_hash=?,rollback_idempotency_key=? WHERE id=?`, strings.Repeat("e", 64), "rollback-key", revision.ID); err != nil {
		t.Fatal(err)
	}
	installComposeAuditFailure(t, database)
	if err := database.FinishComposeRollback(ctx, revision.ID, "rolled_back", ""); err == nil {
		t.Fatal("rollback finish succeeded when its audit insert failed")
	}
	stored, _ = database.GetComposeRevision(ctx, revision.ID)
	if stored.State != "rolling_back" || stored.RollbackFinishedAt != nil {
		t.Fatalf("rollback finish survived rollback: %+v", stored)
	}
	dropComposeAuditFailure(t, database)
	recovered, fresh, err := database.StartComposeRollback(
		ctx, revision.ID, strings.Repeat("e", 64), "rollback-key",
	)
	if err == nil || fresh || recovered.State != "needs_attention" {
		t.Fatalf("interrupted rollback replay=%+v fresh=%v err=%v", recovered, fresh, err)
	}
	stored, _ = database.GetComposeRevision(ctx, revision.ID)
	if stored.State != "needs_attention" || stored.RollbackFinishedAt == nil {
		t.Fatalf("interrupted rollback was not closed: %+v", stored)
	}
	audits, err := database.ListAudit(ctx, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	foundRecovery := false
	for _, audit := range audits {
		if audit.Resource == revision.ID && audit.Event == "compose.revision.recovered" {
			foundRecovery = true
		}
	}
	if !foundRecovery {
		t.Fatal("interrupted rollback recovery audit missing")
	}
}

func TestComposeRevisionExpiryIsFailClosedAndAudited(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "ops.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	ctx := context.Background()
	now := time.Now().UTC().Round(0)
	database.now = func() time.Time { return now }
	revision := atomicComposeRevision(now)
	if _, _, err := database.SaveComposeRevisionIdempotent(ctx, revision, "sha256:request"); err != nil {
		t.Fatal(err)
	}
	now = revision.ExpiresAt.Add(time.Second)
	if _, err := database.ApproveComposeRevision(ctx, revision.ID, strings.Repeat("b", 64), revision.Digest, revision.ConfirmationPhrase); err == nil {
		t.Fatal("expired proposal was approved")
	}
	stored, err := database.GetComposeRevision(ctx, revision.ID)
	if err != nil || stored.State != "expired" {
		t.Fatalf("stored=%+v err=%v", stored, err)
	}
	audits, err := database.ListAudit(ctx, 20, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, audit := range audits {
		if audit.Event == "compose.revision.expired" {
			found = true
		}
	}
	if !found {
		t.Fatal("expiry audit missing")
	}
}

func atomicComposeRevision(now time.Time) model.ComposeRevision {
	return model.ComposeRevision{
		ID: "11111111-1111-4111-8111-111111111111", IdempotencyKey: "proposal-key",
		Service: "demo", Digest: "sha256:candidate", ExpectedDigest: "sha256:baseline",
		Source: "test", Content: "services: {}", Validated: true, State: "proposed",
		ActorHash: strings.Repeat("a", 64), ConfirmationPhrase: "confirm", CreatedAt: now,
		ExpiresAt: now.Add(15 * time.Minute), TenantID: "production", ServerID: "server-demo",
		ProjectName: "demo", PolicyDigest: "sha256:policy",
		RecoveryPointID: "point-compose", RecoveryPointExpectedDigest: "sha256:before",
		RecoveryPointBindingDigest: "sha256:binding", RecoveryPointEvidenceDigest: "sha256:evidence",
		ExpectedRuntimeIdentityDigest: "sha256:identity",
	}
}

func installComposeAuditFailure(t *testing.T, database *Store) {
	t.Helper()
	if _, err := database.db.Exec(`CREATE TRIGGER fail_compose_audit BEFORE INSERT ON audit_entries
		WHEN NEW.event LIKE 'compose.%' BEGIN SELECT RAISE(ABORT, 'forced compose audit failure'); END`); err != nil {
		t.Fatal(err)
	}
}

func dropComposeAuditFailure(t *testing.T, database *Store) {
	t.Helper()
	if _, err := database.db.Exec(`DROP TRIGGER fail_compose_audit`); err != nil {
		t.Fatal(err)
	}
}
