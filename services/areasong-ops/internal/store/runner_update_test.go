package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

func TestRunnerUpdateRecoveryOnlyClosesStaleExecutors(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, time.August, 20, 12, 0, 0, 0, time.UTC)
	database.now = func() time.Time { return now }
	update := model.RunnerUpdate{
		ID:             "11111111-1111-4111-8111-111111111111",
		IdempotencyKey: "22222222-2222-4222-8222-222222222222",
		RequestDigest:  "sha256:request", RunnerID: "runner-test", TargetVersion: "v2",
		ArtifactPath: "runner", ArtifactDigest: "sha256:" + strings.Repeat("a", 64),
		ArtifactRevision: strings.Repeat("b", 40), Publisher: "publisher",
		StagedPath: "/state/staged", BinaryPath: "/usr/local/libexec/areasong-ops/runner/areasong-ops-runner",
		UnitName: "areasong-ops-runner.service", HealthTimeoutSeconds: 30,
		State: "prepared", Phase: "prepared", PreviousVersion: "v1",
		PreviousRevision: strings.Repeat("c", 40), PreviousDigest: "sha256:" + strings.Repeat("d", 64),
		ConfirmationPhrase: "activate", CreatedAt: now,
	}
	if _, created, err := database.ReserveRunnerUpdate(ctx, update, strings.Repeat("1", 64)); err != nil || !created {
		t.Fatalf("reserve created=%v err=%v", created, err)
	}
	activated, created, err := database.BeginRunnerUpdateActivation(
		ctx, update.ID, strings.Repeat("2", 64),
		"33333333-3333-4333-8333-333333333333", "activate",
	)
	if err != nil || !created {
		t.Fatalf("activate created=%v err=%v", created, err)
	}
	if count, err := database.RecoverInterruptedRunnerUpdates(ctx); err != nil || count != 0 {
		t.Fatalf("fresh recovery count=%d err=%v", count, err)
	}
	now = now.Add(90 * time.Second)
	if err := database.HeartbeatRunnerUpdate(ctx, update.ID, activated.FencingToken); err != nil {
		t.Fatal(err)
	}
	now = now.Add(90 * time.Second)
	if count, err := database.RecoverInterruptedRunnerUpdates(ctx); err != nil || count != 0 {
		t.Fatalf("renewed recovery count=%d err=%v", count, err)
	}
	now = now.Add(121 * time.Second)
	if count, err := database.RecoverInterruptedRunnerUpdates(ctx); err != nil || count != 1 {
		t.Fatalf("stale recovery count=%d err=%v", count, err)
	}
	stored, err := database.GetRunnerUpdate(ctx, update.ID)
	if err != nil || stored.State != "needs_attention" || stored.Phase != "interrupted" {
		t.Fatalf("stored=%+v err=%v", stored, err)
	}
}

func TestOpenExistingPreservesSharedStateDirectoryMode(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "ops.db")
	database, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o710); err != nil {
		t.Fatal(err)
	}
	existing, err := OpenExisting(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := existing.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o710 {
		t.Fatalf("state root mode=%o", info.Mode().Perm())
	}
}
