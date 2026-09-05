package store

import (
	"context"
	"testing"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

func TestListServiceStatesClosesOuterRowsBeforeLoadingState(t *testing.T) {
	database := openTestStore(t)
	if _, err := database.SetDesiredState(context.Background(), DesiredStateInput{
		Service: "demo", ObjectID: "service:demo", TenantID: "default", Desired: model.DesiredRunning,
	}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	states, err := database.ListServiceStates(ctx)
	if err != nil {
		t.Fatalf("list service states: %v", err)
	}
	if len(states) != 1 || states[0].Service != "demo" {
		t.Fatalf("states=%+v", states)
	}
}

func TestListRecoveryPointsClosesOuterRowsBeforeLoadingPoint(t *testing.T) {
	database := openTestStore(t)
	now := time.Now().UTC()
	preview := testPreview(now)
	if err := database.CreatePreview(context.Background(), PreviewInput{
		Preview: preview, ConfirmationHash: HashConfirmation("重启 demo"),
	}); err != nil {
		t.Fatal(err)
	}
	task, _, err := database.StartTask(context.Background(), "a", model.StartTaskRequest{
		PreviewID: preview.ID, Confirmation: "重启 demo", IdempotencyKey: "recovery-list",
	}, "task-recovery-list")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.MarkRunning(context.Background(), task.ID, "backup"); err != nil {
		t.Fatal(err)
	}
	point := model.RecoveryPoint{
		ID: "point-list", TaskID: task.ID, Service: task.Service, Status: "verified",
		Evidence: model.RecoveryPointEvidence{
			SchemaVersion: 1, Service: task.Service, TaskID: task.ID, CreatedAt: now,
		},
		EvidenceDigest: "sha256:evidence", CreatedAt: now,
	}
	if err := database.SaveRecoveryPoint(context.Background(), point); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	points, err := database.ListRecoveryPoints(ctx, task.Service, 20)
	if err != nil {
		t.Fatalf("list recovery points: %v", err)
	}
	if len(points) != 1 || points[0].ID != point.ID {
		t.Fatalf("points=%+v", points)
	}
}
