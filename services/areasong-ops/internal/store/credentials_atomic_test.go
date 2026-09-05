package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

func TestCredentialRotationStateAndAuditAreAtomic(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, time.September, 5, 6, 0, 0, 0, time.UTC)
	database.now = func() time.Time { return now }
	rotation := credentialRotationAtomicFixture(now, "rotation-a")

	installCredentialAuditFailure(t, database)
	if _, _, err := database.StartCredentialRotation(ctx, rotation); err == nil {
		t.Fatal("credential rotation start survived audit failure")
	}
	if _, err := database.GetCredentialRotation(ctx, rotation.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("failed start persisted rotation: %v", err)
	}
	dropCredentialAuditFailure(t, database)
	if _, fresh, err := database.StartCredentialRotation(ctx, rotation); err != nil || !fresh {
		t.Fatalf("start fresh=%v err=%v", fresh, err)
	}

	result := model.CredentialRotationResult{
		State:            model.CredentialRotationSwitchedPendingRevocation,
		ValidationResult: "validated", Outcome: "switched", RollbackResult: "available",
	}
	installCredentialAuditFailure(t, database)
	if err := database.FinishCredentialRotation(ctx, rotation.ID, result); err == nil {
		t.Fatal("credential rotation finish survived audit failure")
	}
	assertCredentialRotationState(t, database, rotation.ID, model.CredentialRotationRunning)
	dropCredentialAuditFailure(t, database)
	if err := database.FinishCredentialRotation(ctx, rotation.ID, result); err != nil {
		t.Fatal(err)
	}

	installCredentialAuditFailure(t, database)
	if _, err := database.MarkCredentialRevocationVerified(
		ctx, rotation.ID, rotation.ActorHash, "closure-a",
	); err == nil {
		t.Fatal("credential revocation verification survived audit failure")
	}
	assertCredentialRotationState(t, database, rotation.ID, model.CredentialRotationSwitchedPendingRevocation)
	dropCredentialAuditFailure(t, database)
	if _, err := database.MarkCredentialRevocationVerified(
		ctx, rotation.ID, rotation.ActorHash, "closure-a",
	); err != nil {
		t.Fatal(err)
	}

	installCredentialAuditFailure(t, database)
	if _, _, err := database.CloseCredentialRotation(
		ctx, rotation.ID, rotation.ActorHash, "closure-a", "complete",
	); err == nil {
		t.Fatal("credential rotation close survived audit failure")
	}
	assertCredentialRotationState(t, database, rotation.ID, model.CredentialRotationRevocationVerified)
	dropCredentialAuditFailure(t, database)
	closed, fresh, err := database.CloseCredentialRotation(
		ctx, rotation.ID, rotation.ActorHash, "closure-a", "complete",
	)
	if err != nil || !fresh || closed.State != model.CredentialRotationCompleted {
		t.Fatalf("close fresh=%v state=%s err=%v", fresh, closed.State, err)
	}
}

func TestInterruptedCredentialRotationAndAuditAreAtomic(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, time.September, 5, 7, 0, 0, 0, time.UTC)
	database.now = func() time.Time { return now }
	rotation := credentialRotationAtomicFixture(now, "rotation-interrupted")
	if _, _, err := database.StartCredentialRotation(ctx, rotation); err != nil {
		t.Fatal(err)
	}
	installCredentialAuditFailure(t, database)
	if _, err := database.RecoverInterruptedCredentialRotations(ctx); err == nil {
		t.Fatal("credential interruption recovery survived audit failure")
	}
	assertCredentialRotationState(t, database, rotation.ID, model.CredentialRotationRunning)
	dropCredentialAuditFailure(t, database)
	if count, err := database.RecoverInterruptedCredentialRotations(ctx); err != nil || count != 1 {
		t.Fatalf("recover count=%d err=%v", count, err)
	}
	assertCredentialRotationState(t, database, rotation.ID, model.CredentialRotationNeedsAttention)
}

func credentialRotationAtomicFixture(now time.Time, id string) model.CredentialRotation {
	return model.CredentialRotation{
		ID: id, IdempotencyKey: "idem-" + id, ActorHash: "actor-a",
		CredentialType: model.GitHubAlertmanagerCredential, Target: "alertmanager-github.env",
		State: model.CredentialRotationRunning, Fingerprint: "sha256:fingerprint",
		ExpiresAt: "2027-08-12", CreatedAt: now,
	}
}

func installCredentialAuditFailure(t *testing.T, database *Store) {
	t.Helper()
	if _, err := database.db.Exec(`CREATE TRIGGER fail_credential_audit BEFORE INSERT ON audit_entries
		WHEN NEW.event LIKE 'credential.rotation.%' BEGIN SELECT RAISE(ABORT, 'forced credential audit failure'); END`); err != nil {
		t.Fatal(err)
	}
}

func dropCredentialAuditFailure(t *testing.T, database *Store) {
	t.Helper()
	if _, err := database.db.Exec(`DROP TRIGGER fail_credential_audit`); err != nil {
		t.Fatal(err)
	}
}

func assertCredentialRotationState(
	t *testing.T,
	database *Store,
	id string,
	want model.CredentialRotationState,
) {
	t.Helper()
	rotation, err := database.GetCredentialRotation(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if rotation.State != want {
		t.Fatalf("credential rotation state=%s, want %s", rotation.State, want)
	}
}
