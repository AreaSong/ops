package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

func TestFleetRunnerUpdateStateMachineStopsAndRollsBack(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, time.September, 3, 2, 0, 0, 0, time.UTC)
	database.now = func() time.Time { return now }
	actors := []string{
		strings.Repeat("1", 64), strings.Repeat("2", 64),
		strings.Repeat("3", 64), strings.Repeat("4", 64),
	}
	plan := fleetRunnerUpdateStorePlan(now, actors[0])
	created, fresh, err := database.CreateFleetRunnerUpdatePlan(ctx, plan)
	if err != nil || !fresh || created.ID != plan.ID {
		t.Fatalf("create fresh=%v plan=%+v err=%v", fresh, created, err)
	}
	if _, fresh, err := database.CreateFleetRunnerUpdatePlan(ctx, plan); err != nil || fresh {
		t.Fatalf("replay fresh=%v err=%v", fresh, err)
	}
	if _, err := database.ApproveFleetRunnerUpdatePlan(ctx, plan.ID, actors[0], plan.PlanDigest, plan.ConfirmationPhrase); err == nil {
		t.Fatal("creator approved its own fleet update")
	}
	approved, err := database.ApproveFleetRunnerUpdatePlan(ctx, plan.ID, actors[1], plan.PlanDigest, plan.ConfirmationPhrase)
	if err != nil || approved.State != model.FleetRunnerUpdatePendingSecondApproval {
		t.Fatalf("first approval=%+v err=%v", approved, err)
	}
	if _, err := database.ApproveFleetRunnerUpdatePlan(ctx, plan.ID, actors[1], plan.PlanDigest, plan.ConfirmationPhrase); err == nil {
		t.Fatal("first approver completed second approval")
	}
	approved, err = database.ApproveFleetRunnerUpdatePlan(ctx, plan.ID, actors[2], plan.PlanDigest, plan.ConfirmationPhrase)
	if err != nil || approved.State != model.FleetRunnerUpdateApproved {
		t.Fatalf("second approval=%+v err=%v", approved, err)
	}
	for _, actor := range actors[:3] {
		if _, _, err := database.StartFleetRunnerUpdatePlan(ctx, plan.ID, actor, "execution-blocked-"+actor[:4]); err == nil {
			t.Fatalf("non-independent actor %s executed plan", actor[:4])
		}
	}
	started, fresh, err := database.StartFleetRunnerUpdatePlan(ctx, plan.ID, actors[3], "execution-key")
	if err != nil || !fresh || started.State != model.FleetRunnerUpdateRunning {
		t.Fatalf("start fresh=%v plan=%+v err=%v", fresh, started, err)
	}
	if _, claimed, err := database.ClaimFleetRunnerUpdate(ctx, "runner-b", time.Minute); err != nil || claimed {
		t.Fatalf("later batch claimed=%v err=%v", claimed, err)
	}
	assignment, claimed, err := database.ClaimFleetRunnerUpdate(ctx, "runner-a", time.Minute)
	if err != nil || !claimed || assignment.Action != "update" {
		t.Fatalf("canary claim=%+v claimed=%v err=%v", assignment, claimed, err)
	}
	invalid := model.FleetRunnerUpdateCompletionRequest{
		FleetRunnerUpdateFence: model.FleetRunnerUpdateFence{Generation: assignment.Fence.Generation, ClaimToken: "wrong"},
		IdempotencyKey:         "completion-invalid", State: "succeeded",
	}
	if _, _, err := database.CompleteFleetRunnerUpdate(ctx, "runner-a", assignment.ItemID, invalid); err == nil {
		t.Fatal("completion accepted an invalid fence")
	}
	completed := model.FleetRunnerUpdateCompletionRequest{
		FleetRunnerUpdateFence: assignment.Fence, IdempotencyKey: "completion-a",
		State: "succeeded", ObservedVersion: "v2", ObservedRevision: strings.Repeat("b", 40),
		ObservedDigest: "sha256:" + strings.Repeat("b", 64),
	}
	if _, fresh, err := database.CompleteFleetRunnerUpdate(ctx, "runner-a", assignment.ItemID, completed); err != nil || !fresh {
		t.Fatalf("complete canary fresh=%v err=%v", fresh, err)
	}
	if replayed, fresh, err := database.CompleteFleetRunnerUpdate(ctx, "runner-a", assignment.ItemID, completed); err != nil || fresh || replayed.State != model.FleetRunnerUpdateItemSucceeded {
		t.Fatalf("completion replay=%+v fresh=%v err=%v", replayed, fresh, err)
	}
	tampered := completed
	tampered.ObservedRevision = strings.Repeat("c", 40)
	if _, _, err := database.CompleteFleetRunnerUpdate(ctx, "runner-a", assignment.ItemID, tampered); !errors.Is(err, ErrIdempotency) {
		t.Fatalf("tampered completion replay err=%v", err)
	}
	wrongFence := completed
	wrongFence.ClaimToken = "wrong"
	if _, _, err := database.CompleteFleetRunnerUpdate(ctx, "runner-a", assignment.ItemID, wrongFence); !errors.Is(err, ErrFleetRunnerUpdateFence) {
		t.Fatalf("completion replay fence err=%v", err)
	}
	if err := database.BeginFleetRunnerUpdateObservation(ctx, plan.ID, now, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	if err := database.ReleaseFleetRunnerUpdateBatch(ctx, plan.ID, 1, now); err != nil {
		t.Fatal(err)
	}
	assignment, claimed, err = database.ClaimFleetRunnerUpdate(ctx, "runner-b", time.Minute)
	if err != nil || !claimed {
		t.Fatalf("second batch claim=%+v claimed=%v err=%v", assignment, claimed, err)
	}
	failed := model.FleetRunnerUpdateCompletionRequest{
		FleetRunnerUpdateFence: assignment.Fence, IdempotencyKey: "completion-b",
		State: "failed", ObservedVersion: "v1", ObservedRevision: strings.Repeat("a", 40),
		ObservedDigest: "sha256:" + strings.Repeat("a", 64), Error: "preflight failed",
	}
	if _, _, err := database.CompleteFleetRunnerUpdate(ctx, "runner-b", assignment.ItemID, failed); err != nil {
		t.Fatal(err)
	}
	if err := database.BeginFleetRunnerUpdateRollback(ctx, plan.ID, failed.Error); err != nil {
		t.Fatal(err)
	}
	rollback, claimed, err := database.ClaimFleetRunnerUpdate(ctx, "runner-a", time.Minute)
	if err != nil || !claimed || rollback.Action != "rollback" {
		t.Fatalf("rollback claim=%+v claimed=%v err=%v", rollback, claimed, err)
	}
	rolledBack := model.FleetRunnerUpdateCompletionRequest{
		FleetRunnerUpdateFence: rollback.Fence, IdempotencyKey: "rollback-a",
		State: "rolled_back", ObservedVersion: "v1", ObservedRevision: strings.Repeat("a", 40),
		ObservedDigest: "sha256:" + strings.Repeat("a", 64),
	}
	if _, _, err := database.CompleteFleetRunnerUpdate(ctx, "runner-a", rollback.ItemID, rolledBack); err != nil {
		t.Fatal(err)
	}
	if err := database.FinishFleetRunnerUpdatePlan(ctx, plan.ID, model.FleetRunnerUpdateRolledBack, "rollback complete", failed.Error); err != nil {
		t.Fatal(err)
	}
	stored, err := database.GetFleetRunnerUpdatePlan(ctx, plan.ID)
	if err != nil || stored.State != model.FleetRunnerUpdateRolledBack {
		t.Fatalf("stored=%+v err=%v", stored, err)
	}
}

func TestFleetRunnerUpdateReceiptIsDurableAndIdempotent(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	receipt := model.FleetRunnerUpdateReceipt{
		ItemID: "11111111-1111-4111-8111-111111111111", AssignmentGeneration: 1,
		PlanID:               "22222222-2222-4222-8222-222222222222",
		Fence:                model.FleetRunnerUpdateFence{Generation: 1, ClaimToken: "secret-token"},
		ControlPlaneEndpoint: "https://control.example.test", Action: "update",
		Assignment: model.FleetRunnerUpdateAssignment{
			PlanID: "22222222-2222-4222-8222-222222222222",
			ItemID: "11111111-1111-4111-8111-111111111111",
			Action: "update", PlanDigest: "sha256:plan",
			Fence: model.FleetRunnerUpdateFence{Generation: 1, ClaimToken: "secret-token"},
		},
	}
	stored, fresh, err := database.SaveFleetRunnerUpdateReceipt(ctx, receipt)
	if err != nil || !fresh || stored.State != "prepared" {
		t.Fatalf("save receipt=%+v fresh=%v err=%v", stored, fresh, err)
	}
	if _, fresh, err := database.SaveFleetRunnerUpdateReceipt(ctx, receipt); err != nil || fresh {
		t.Fatalf("replay fresh=%v err=%v", fresh, err)
	}
	conflict := receipt
	conflict.Fence.ClaimToken = "different"
	conflict.Assignment.Fence.ClaimToken = "different"
	if _, _, err := database.SaveFleetRunnerUpdateReceipt(ctx, conflict); !errors.Is(err, ErrIdempotency) {
		t.Fatalf("conflicting receipt err=%v", err)
	}
	if err := database.UpdateFleetRunnerUpdateReceipt(ctx, receipt.ItemID, 1, "launching", "local-update", ""); err != nil {
		t.Fatal(err)
	}
	if err := database.UpdateFleetRunnerUpdateReceipt(ctx, receipt.ItemID, 1, "launched", "local-update", ""); err != nil {
		t.Fatal(err)
	}
	pending, err := database.ListPendingFleetRunnerUpdateReceipts(ctx)
	if err != nil || len(pending) != 1 || pending[0].Fence.ClaimToken != "secret-token" || pending[0].LocalUpdateID != "local-update" {
		t.Fatalf("pending=%+v err=%v", pending, err)
	}
	if err := database.UpdateFleetRunnerUpdateReceipt(ctx, receipt.ItemID, 1, "reported", "", ""); err != nil {
		t.Fatal(err)
	}
	pending, err = database.ListPendingFleetRunnerUpdateReceipts(ctx)
	if err != nil || len(pending) != 0 {
		t.Fatalf("reported pending=%+v err=%v", pending, err)
	}
}

func fleetRunnerUpdateStorePlan(now time.Time, actor string) model.FleetRunnerUpdatePlan {
	planID := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	window := model.ChangeWindow{StartAt: now.Add(-time.Minute), EndAt: now.Add(time.Hour), Timezone: "UTC"}
	return model.FleetRunnerUpdatePlan{
		ID: planID, IdempotencyKey: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
		RequestDigest: "sha256:request", PlanDigest: "sha256:plan", PolicyDigest: "sha256:policy",
		ActorHash: actor, TenantID: "tenant-a",
		Manifest: model.FleetRunnerUpdateManifest{
			Purpose: model.FleetRunnerUpdateManifestPurpose, Schema: model.FleetRunnerUpdateManifestSchema,
			GOOS: "linux", GOARCH: "amd64", TargetVersion: "v2",
			ArtifactDigest: "sha256:" + strings.Repeat("b", 64), ArtifactRevision: strings.Repeat("b", 40), Publisher: "release",
		},
		ArtifactPath: "runner-v2", ArtifactSignature: "signature", StagedPath: "/state/staged/runner-v2",
		TargetRunnerIDs: []string{"runner-a", "runner-b"},
		BatchPolicy:     model.BatchPolicy{Strategy: model.BatchCanary, CanarySize: 1, BatchSize: 1, ObservationSeconds: 60},
		MaxConcurrent:   1, ChangeWindow: &window, RollbackOnFailure: true,
		State: model.FleetRunnerUpdatePendingApproval, CurrentBatch: -1,
		ConfirmationPhrase: "更新 Runner Fleet 到 v2", CreatedAt: now, ExpiresAt: now.Add(time.Hour), UpdatedAt: now,
		Items: []model.FleetRunnerUpdateItem{
			{ID: "cccccccc-cccc-4ccc-8ccc-cccccccccccc", PlanID: planID, RunnerID: "runner-a", ServerID: "server-a", BatchIndex: 0, State: model.FleetRunnerUpdateItemPending, PreviousVersion: "v1", PreviousRevision: strings.Repeat("a", 40), PreviousDigest: "sha256:" + strings.Repeat("a", 64), ExpectedLeaseGeneration: 1, UpdatedAt: now},
			{ID: "dddddddd-dddd-4ddd-8ddd-dddddddddddd", PlanID: planID, RunnerID: "runner-b", ServerID: "server-b", BatchIndex: 1, State: model.FleetRunnerUpdateItemPending, PreviousVersion: "v1", PreviousRevision: strings.Repeat("a", 40), PreviousDigest: "sha256:" + strings.Repeat("a", 64), ExpectedLeaseGeneration: 1, UpdatedAt: now},
		},
	}
}
