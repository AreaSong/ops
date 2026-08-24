package updater

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	"github.com/AreaSong/ops/services/areasong-ops/internal/store"
)

type fakeController struct {
	restarts     int
	failTarget   bool
	failPrevious bool
	identities   []string
}

func (controller *fakeController) Restart(context.Context, string) error {
	controller.restarts++
	return nil
}

func (controller *fakeController) WaitIdentity(
	_ context.Context,
	_, _, version, revision string,
	_ time.Duration,
) error {
	controller.identities = append(controller.identities, version+"@"+revision)
	if (version == "v2" && controller.failTarget) ||
		(version == "v1" && controller.failPrevious) {
		return errors.New("identity unavailable")
	}
	return nil
}

type updaterFixture struct {
	executor   Executor
	database   *store.Store
	controller *fakeController
	update     model.RunnerUpdate
	binaryPath string
	oldContent []byte
	newContent []byte
}

func newUpdaterFixture(t *testing.T, controller *fakeController) updaterFixture {
	t.Helper()
	base := t.TempDir()
	root, err := filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}
	database, err := store.Open(filepath.Join(root, "ops.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	id := "11111111-1111-4111-8111-111111111111"
	oldContent := []byte("old-runner-binary")
	newContent := []byte("new-runner-binary")
	binaryPath := filepath.Join(root, "bin", "areasong-ops-runner")
	stagedPath := filepath.Join(root, "runner-updates", "staged", id+".runner")
	for _, directory := range []string{filepath.Dir(binaryPath), filepath.Dir(stagedPath)} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(binaryPath, oldContent, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stagedPath, newContent, 0o700); err != nil {
		t.Fatal(err)
	}
	oldDigest, err := hashFile(binaryPath, maxArtifactBytes)
	if err != nil {
		t.Fatal(err)
	}
	newDigest, err := hashFile(stagedPath, maxArtifactBytes)
	if err != nil {
		t.Fatal(err)
	}
	update := model.RunnerUpdate{
		ID: id, IdempotencyKey: "22222222-2222-4222-8222-222222222222",
		RequestDigest: "sha256:request", RunnerID: "runner-test", TargetVersion: "v2",
		ArtifactPath: "incoming/runner", ArtifactDigest: newDigest,
		ArtifactRevision: strings.Repeat("a", 40), Publisher: "test",
		ArtifactSignature: "signature", StagedPath: stagedPath, BinaryPath: binaryPath,
		UnitName: "areasong-ops-runner.service", HealthTimeoutSeconds: 5,
		State: "prepared", Phase: "prepared", PreviousVersion: "v1",
		PreviousRevision: strings.Repeat("b", 40), PreviousDigest: oldDigest,
		ConfirmationPhrase: "activate", CreatedAt: time.Now().UTC(),
	}
	preparedBy := strings.Repeat("1", 64)
	if _, created, err := database.ReserveRunnerUpdate(context.Background(), update, preparedBy); err != nil || !created {
		t.Fatalf("reserve created=%v err=%v", created, err)
	}
	approvedBy := strings.Repeat("2", 64)
	activated, created, err := database.BeginRunnerUpdateActivation(
		context.Background(), id, approvedBy,
		"33333333-3333-4333-8333-333333333333", "activate",
	)
	if err != nil || !created {
		t.Fatalf("activate update=%+v created=%v err=%v", activated, created, err)
	}
	return updaterFixture{
		executor: Executor{Store: database, StateRoot: root, Controller: controller},
		database: database, controller: controller, update: activated,
		binaryPath: binaryPath, oldContent: oldContent, newContent: newContent,
	}
}

func TestExecutorInstallsRestartsAndVerifiesIdentity(t *testing.T) {
	fixture := newUpdaterFixture(t, &fakeController{})
	outcome, err := fixture.executor.Run(context.Background(), fixture.update.ID)
	if err != nil || outcome != stateSucceeded {
		t.Fatalf("outcome=%s err=%v", outcome, err)
	}
	assertBinaryContent(t, fixture.binaryPath, fixture.newContent)
	stored, err := fixture.database.GetRunnerUpdate(context.Background(), fixture.update.ID)
	if err != nil || stored.State != stateSucceeded || stored.Phase != "verified" {
		t.Fatalf("stored=%+v err=%v", stored, err)
	}
	if fixture.controller.restarts != 1 || len(fixture.controller.identities) != 1 {
		t.Fatalf("controller=%+v", fixture.controller)
	}
}

func TestExecutorRollsBackWhenTargetIdentityFails(t *testing.T) {
	fixture := newUpdaterFixture(t, &fakeController{failTarget: true})
	outcome, err := fixture.executor.Run(context.Background(), fixture.update.ID)
	if err != nil || outcome != stateRolledBack {
		t.Fatalf("outcome=%s err=%v", outcome, err)
	}
	assertBinaryContent(t, fixture.binaryPath, fixture.oldContent)
	stored, err := fixture.database.GetRunnerUpdate(context.Background(), fixture.update.ID)
	if err != nil || stored.State != stateRolledBack || stored.Phase != "rollback_verified" {
		t.Fatalf("stored=%+v err=%v", stored, err)
	}
	if fixture.controller.restarts != 2 || len(fixture.controller.identities) != 2 {
		t.Fatalf("controller=%+v", fixture.controller)
	}
}

func TestExecutorNeedsAttentionWhenRollbackIdentityCannotBeVerified(t *testing.T) {
	fixture := newUpdaterFixture(t, &fakeController{failTarget: true, failPrevious: true})
	outcome, err := fixture.executor.Run(context.Background(), fixture.update.ID)
	if err == nil || outcome != stateNeedsAttention {
		t.Fatalf("outcome=%s err=%v", outcome, err)
	}
	assertBinaryContent(t, fixture.binaryPath, fixture.oldContent)
	stored, getErr := fixture.database.GetRunnerUpdate(context.Background(), fixture.update.ID)
	if getErr != nil || stored.State != stateNeedsAttention || stored.Phase != "rollback_verify_failed" {
		t.Fatalf("stored=%+v err=%v", stored, getErr)
	}
}

func assertBinaryContent(t *testing.T, path string, expected []byte) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != string(expected) {
		t.Fatalf("binary=%q expected=%q", content, expected)
	}
}
