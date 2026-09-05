package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

func TestRunnerUpdateStateTransitionsAndAuditAreAtomic(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, time.September, 5, 2, 0, 0, 0, time.UTC)
	database.now = func() time.Time { return now }
	update := runnerUpdateAtomicFixture(now, "11111111-1111-4111-8111-111111111111")
	prepareActor := strings.Repeat("1", 64)
	activateActor := strings.Repeat("2", 64)
	resolveActor := strings.Repeat("3", 64)

	installRunnerUpdateAuditFailure(t, database)
	if _, _, err := database.ReserveRunnerUpdate(ctx, update, prepareActor); err == nil {
		t.Fatal("Runner update reservation survived audit failure")
	}
	if _, err := database.GetRunnerUpdate(ctx, update.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("failed reservation persisted update: %v", err)
	}
	dropRunnerUpdateAuditFailure(t, database)
	if _, fresh, err := database.ReserveRunnerUpdate(ctx, update, prepareActor); err != nil || !fresh {
		t.Fatalf("reserve fresh=%v err=%v", fresh, err)
	}

	installRunnerUpdateAuditFailure(t, database)
	if _, _, err := database.BeginRunnerUpdateActivation(
		ctx, update.ID, activateActor, "22222222-2222-4222-8222-222222222222", update.ConfirmationPhrase,
	); err == nil {
		t.Fatal("Runner update activation survived audit failure")
	}
	assertRunnerUpdateState(t, database, update.ID, "prepared", "prepared")
	dropRunnerUpdateAuditFailure(t, database)
	activated, fresh, err := database.BeginRunnerUpdateActivation(
		ctx, update.ID, activateActor, "22222222-2222-4222-8222-222222222222", update.ConfirmationPhrase,
	)
	if err != nil || !fresh {
		t.Fatalf("activate fresh=%v err=%v", fresh, err)
	}

	installRunnerUpdateAuditFailure(t, database)
	if err := database.UpdateRunnerUpdatePhase(ctx, update.ID, "validating", "", activated.FencingToken); err == nil {
		t.Fatal("Runner update phase survived audit failure")
	}
	assertRunnerUpdateState(t, database, update.ID, "activating", "queued")
	dropRunnerUpdateAuditFailure(t, database)
	if err := database.UpdateRunnerUpdatePhase(ctx, update.ID, "validating", "", activated.FencingToken); err != nil {
		t.Fatal(err)
	}

	installRunnerUpdateAuditFailure(t, database)
	if err := database.FinishRunnerUpdate(
		ctx, update.ID, "needs_attention", "verification_failed", "", "health failed", activated.FencingToken,
	); err == nil {
		t.Fatal("Runner update terminal state survived audit failure")
	}
	assertRunnerUpdateState(t, database, update.ID, "activating", "validating")
	dropRunnerUpdateAuditFailure(t, database)
	if err := database.FinishRunnerUpdate(
		ctx, update.ID, "needs_attention", "verification_failed", "", "health failed", activated.FencingToken,
	); err != nil {
		t.Fatal(err)
	}

	evidence := model.RunnerUpdateResolutionEvidence{
		Decision: "abort", ObservedVersion: "v1", ObservedRevision: strings.Repeat("b", 40),
		ObservedDigest: "sha256:" + strings.Repeat("c", 64), Reason: "verified unhealthy candidate",
	}
	installRunnerUpdateAuditFailure(t, database)
	if _, _, err := database.ResolveRunnerUpdate(
		ctx, update.ID, resolveActor, "33333333-3333-4333-8333-333333333333", evidence,
	); err == nil {
		t.Fatal("Runner update resolution survived audit failure")
	}
	assertRunnerUpdateState(t, database, update.ID, "needs_attention", "verification_failed")
	dropRunnerUpdateAuditFailure(t, database)
	resolved, fresh, err := database.ResolveRunnerUpdate(
		ctx, update.ID, resolveActor, "33333333-3333-4333-8333-333333333333", evidence,
	)
	if err != nil || !fresh || resolved.State != "failed" {
		t.Fatalf("resolve fresh=%v state=%s err=%v", fresh, resolved.State, err)
	}
}

func TestRunnerUpdateCancellationAndInterruptedRecoveryAuditsAreAtomic(t *testing.T) {
	t.Run("cancel", func(t *testing.T) {
		database := openTestStore(t)
		ctx := context.Background()
		now := time.Date(2026, time.September, 5, 3, 0, 0, 0, time.UTC)
		database.now = func() time.Time { return now }
		update := runnerUpdateAtomicFixture(now, "44444444-4444-4444-8444-444444444444")
		actor := strings.Repeat("4", 64)
		if _, _, err := database.ReserveRunnerUpdate(ctx, update, actor); err != nil {
			t.Fatal(err)
		}
		installRunnerUpdateAuditFailure(t, database)
		if _, _, err := database.CancelRunnerUpdate(
			ctx, update.ID, actor, "55555555-5555-4555-8555-555555555555",
		); err == nil {
			t.Fatal("Runner update cancellation survived audit failure")
		}
		assertRunnerUpdateState(t, database, update.ID, "prepared", "prepared")
	})

	t.Run("interrupted", func(t *testing.T) {
		database := openTestStore(t)
		ctx := context.Background()
		now := time.Date(2026, time.September, 5, 4, 0, 0, 0, time.UTC)
		database.now = func() time.Time { return now }
		update := runnerUpdateAtomicFixture(now, "66666666-6666-4666-8666-666666666666")
		if _, _, err := database.ReserveRunnerUpdate(ctx, update, strings.Repeat("6", 64)); err != nil {
			t.Fatal(err)
		}
		if _, _, err := database.BeginRunnerUpdateActivation(
			ctx, update.ID, strings.Repeat("7", 64), "77777777-7777-4777-8777-777777777777", update.ConfirmationPhrase,
		); err != nil {
			t.Fatal(err)
		}
		now = now.Add(defaultRunnerUpdateLease + time.Second)
		installRunnerUpdateAuditFailure(t, database)
		if _, err := database.RecoverInterruptedRunnerUpdates(ctx); err == nil {
			t.Fatal("interrupted Runner update recovery survived audit failure")
		}
		assertRunnerUpdateState(t, database, update.ID, "activating", "queued")
		dropRunnerUpdateAuditFailure(t, database)
		if count, err := database.RecoverInterruptedRunnerUpdates(ctx); err != nil || count != 1 {
			t.Fatalf("recover count=%d err=%v", count, err)
		}
		assertRunnerUpdateState(t, database, update.ID, "needs_attention", "interrupted")
	})
}

func runnerUpdateAtomicFixture(now time.Time, id string) model.RunnerUpdate {
	return model.RunnerUpdate{
		ID: id, IdempotencyKey: id, RequestDigest: "sha256:request-" + id,
		RunnerID: "runner-test", TargetVersion: "v2", ArtifactPath: "runner-v2",
		ArtifactDigest: "sha256:" + strings.Repeat("a", 64), ArtifactRevision: strings.Repeat("a", 40),
		Publisher: "release", StagedPath: "/state/runner-v2", BinaryPath: "/usr/local/bin/runner",
		UnitName: "areasong-ops-runner.service", HealthTimeoutSeconds: 30,
		State: "prepared", Phase: "prepared", PreviousVersion: "v1",
		PreviousRevision: strings.Repeat("b", 40), PreviousDigest: "sha256:" + strings.Repeat("b", 64),
		ConfirmationPhrase: "activate", CreatedAt: now,
	}
}

func installRunnerUpdateAuditFailure(t *testing.T, database *Store) {
	t.Helper()
	if _, err := database.db.Exec(`CREATE TRIGGER fail_runner_update_audit BEFORE INSERT ON audit_entries
		WHEN NEW.event LIKE 'runner.update.%' BEGIN SELECT RAISE(ABORT, 'forced Runner update audit failure'); END`); err != nil {
		t.Fatal(err)
	}
}

func dropRunnerUpdateAuditFailure(t *testing.T, database *Store) {
	t.Helper()
	if _, err := database.db.Exec(`DROP TRIGGER fail_runner_update_audit`); err != nil {
		t.Fatal(err)
	}
}

func assertRunnerUpdateState(t *testing.T, database *Store, id, state, phase string) {
	t.Helper()
	update, err := database.GetRunnerUpdate(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if update.State != state || update.Phase != phase {
		t.Fatalf("Runner update state=%s phase=%s, want %s/%s", update.State, update.Phase, state, phase)
	}
}
