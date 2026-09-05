package store

import (
	"context"
	"testing"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

func TestBatchCreationAndAuditAreAtomic(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, time.September, 5, 5, 0, 0, 0, time.UTC)
	op := model.BatchOperation{
		ID: "batch-atomic", IdempotencyKey: "11111111-1111-4111-8111-111111111111",
		ActorHash: "actor-a", TenantID: "tenant-a", Action: "restart", Digest: "sha256:batch",
		State: model.BatchPendingApproval, CreatedAt: now, UpdatedAt: now,
		Task:  model.BatchTask{ID: "batch-atomic", Action: "restart", State: model.BatchTaskPending, CreatedAt: now},
		Items: []model.BatchItem{{ID: "item-a", ObjectID: "service:a", Service: "a", ServerID: "server-a", RunnerID: "runner-a", State: model.BatchNodePending, UpdatedAt: now}},
	}
	if _, err := database.db.Exec(`CREATE TRIGGER fail_batch_create_audit BEFORE INSERT ON audit_entries
		WHEN NEW.event='batch.created' BEGIN SELECT RAISE(ABORT, 'forced batch audit failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := database.CreateBatchOperation(ctx, BatchOperationInput{
		Operation: op, ConfirmationHash: HashConfirmation("confirm"),
	}); err == nil {
		t.Fatal("batch creation survived audit failure")
	}
	var jobs, items int
	if err := database.db.QueryRow(`SELECT COUNT(*) FROM batch_jobs`).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if err := database.db.QueryRow(`SELECT COUNT(*) FROM batch_items`).Scan(&items); err != nil {
		t.Fatal(err)
	}
	if jobs != 0 || items != 0 {
		t.Fatalf("failed batch creation persisted jobs=%d items=%d", jobs, items)
	}
}
