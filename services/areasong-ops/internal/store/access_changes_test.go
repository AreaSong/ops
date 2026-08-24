package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

func TestAccessChangeRequiresIndependentApprovalsAndExecutor(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "ops.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	creator := "creator"
	first := "first-approver"
	second := "second-approver"
	executor := "executor"
	change := model.AccessChange{
		ID: "change-1", IdempotencyKey: "change-key-1", RequestDigest: "sha256:digest",
		ActorHash: creator, State: model.AccessChangePendingApproval,
		ConfirmationPhrase: "批准访问策略变更 sha256:digest", RequiresDualApproval: true,
		CreatedAt: time.Now().UTC(),
	}
	stored, created, err := database.CreateAccessChange(ctx, change, `{"enforced":true}`, HashConfirmation(change.ConfirmationPhrase))
	if err != nil || !created || stored.ID != change.ID {
		t.Fatalf("create=%+v created=%v err=%v", stored, created, err)
	}
	if _, err := database.ApproveAccessChange(ctx, change.ID, creator, change.RequestDigest, change.ConfirmationPhrase); err == nil {
		t.Fatal("creator unexpectedly approved own access change")
	}
	approved, err := database.ApproveAccessChange(ctx, change.ID, first, change.RequestDigest, change.ConfirmationPhrase)
	if err != nil || approved.ApprovedByHash != first || approved.State != model.AccessChangePendingApproval {
		t.Fatalf("first approval=%+v err=%v", approved, err)
	}
	if _, err := database.ApproveAccessChange(ctx, change.ID, first, change.RequestDigest, change.ConfirmationPhrase); err == nil {
		t.Fatal("same approver unexpectedly completed second approval")
	}
	approved, err = database.ApproveAccessChange(ctx, change.ID, second, change.RequestDigest, change.ConfirmationPhrase)
	if err != nil || approved.State != model.AccessChangeApproved || approved.SecondApprovedByHash != second {
		t.Fatalf("second approval=%+v err=%v", approved, err)
	}
	if _, err := database.MarkAccessChangeApplied(ctx, change.ID, second); err == nil {
		t.Fatal("second approver unexpectedly executed access change")
	}
	applied, err := database.MarkAccessChangeApplied(ctx, change.ID, executor)
	if err != nil || applied.State != model.AccessChangeApplied {
		t.Fatalf("apply=%+v err=%v", applied, err)
	}
	duplicate, fresh, err := database.CreateAccessChange(ctx, change, `{"enforced":true}`, HashConfirmation(change.ConfirmationPhrase))
	if err != nil || fresh || duplicate.State != model.AccessChangeApplied {
		t.Fatalf("duplicate=%+v fresh=%v err=%v", duplicate, fresh, err)
	}
}
