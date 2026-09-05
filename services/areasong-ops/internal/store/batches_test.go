package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

func TestListBatchOperationsClosesOuterRowsBeforeLoadingItems(t *testing.T) {
	database := openTestStore(t)
	now := time.Now().UTC()
	operation := testBatchOperation(now, "batch-list")
	if _, created, err := database.CreateBatchOperation(context.Background(), BatchOperationInput{
		Operation:        operation,
		ConfirmationHash: HashConfirmation(operation.ConfirmationPhrase),
	}); err != nil || !created {
		t.Fatalf("create batch: created=%v err=%v", created, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	operations, err := database.ListBatchOperations(ctx, 20, 0)
	if err != nil {
		t.Fatalf("list batch operations: %v", err)
	}
	if len(operations) != 1 || operations[0].ID != operation.ID {
		t.Fatalf("operations=%+v", operations)
	}
	if len(operations[0].Items) != 1 || operations[0].Items[0].ID != operation.Items[0].ID {
		t.Fatalf("items=%+v", operations[0].Items)
	}
}

func TestBatchDualApprovalMigrationAppendsToExistingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(schema); err != nil {
		t.Fatal(err)
	}
	for index, migration := range migrations[:len(migrations)-1] {
		if _, err := raw.Exec(migration); err != nil {
			t.Fatalf("migration %d: %v", index+1, err)
		}
	}
	if _, err := raw.Exec(`PRAGMA user_version = ` + fmt.Sprint(len(migrations)-1)); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	database, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	for _, column := range []string{"requires_dual_approval", "second_approved_by_hash", "second_approved_at", "confirmation_phrase"} {
		var found string
		if err := database.db.QueryRow(`SELECT name FROM pragma_table_info('batch_jobs') WHERE name=?`, column).Scan(&found); err != nil || found != column {
			t.Fatalf("column=%q found=%q err=%v", column, found, err)
		}
	}
}

func TestBatchApprovalPolicyMigrationFailsClosedForUnprovenPlans(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-policy.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(schema); err != nil {
		t.Fatal(err)
	}
	policyMigration := -1
	for index, migration := range migrations {
		if strings.Contains(migration, "ADD COLUMN approval_policy_version") {
			policyMigration = index
			break
		}
	}
	if policyMigration < 0 {
		t.Fatal("approval policy migration not found")
	}
	for index, migration := range migrations[:policyMigration] {
		if _, err := raw.Exec(migration); err != nil {
			t.Fatalf("migration %d: %v", index+1, err)
		}
	}
	for _, state := range []model.BatchOperationState{
		model.BatchPendingApproval, model.BatchApproved, model.BatchRunning, model.BatchSucceeded,
	} {
		now := time.Now().UTC()
		op := testBatchOperation(now, "legacy-"+string(state))
		op.State = state
		taskJSON, err := encodeJSON(op.Task)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := raw.Exec(`INSERT INTO batch_jobs(
			id,idempotency_key,actor_hash,tenant_id,action,target,strategy,policy_json,task_json,
			digest,confirmation_hash,state,failure_policy,approved_by_hash,summary,error,created_at,updated_at)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			op.ID, op.IdempotencyKey, op.ActorHash, op.TenantID, op.Action, op.Target, "fixed", `{}`, taskJSON,
			op.Digest, HashConfirmation(op.ConfirmationPhrase), state, model.FailureStop, op.ApprovedByHash,
			op.Summary, op.Error, timeText(now), timeText(now)); err != nil {
			t.Fatalf("insert %s: %v", state, err)
		}
	}
	if _, err := raw.Exec(`PRAGMA user_version = ` + fmt.Sprint(policyMigration)); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	database, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	for _, test := range []struct {
		before model.BatchOperationState
		after  model.BatchOperationState
	}{
		{model.BatchPendingApproval, model.BatchNeedsAttention},
		{model.BatchApproved, model.BatchNeedsAttention},
		{model.BatchRunning, model.BatchRunning},
		{model.BatchSucceeded, model.BatchSucceeded},
	} {
		stored, err := database.GetBatchOperation(context.Background(), "legacy-"+string(test.before))
		if err != nil || stored.State != test.after || stored.ApprovalPolicyVersion != 0 {
			t.Fatalf("state %s migrated to %+v err=%v, want %s with unproven version", test.before, stored, err, test.after)
		}
	}
}

func TestDualApprovalBatchIsIdempotentAndRequiresIndependentSecondApprover(t *testing.T) {
	database := openTestStore(t)
	now := time.Now().UTC()
	operation := testBatchOperation(now, "batch-dual")
	operation.State = model.BatchPendingApproval
	operation.RequiresDualApproval = true
	operation.ActorHash = strings.Repeat("1", 64)
	first := strings.Repeat("2", 64)
	second := strings.Repeat("3", 64)
	if _, created, err := database.CreateBatchOperation(context.Background(), BatchOperationInput{
		Operation:        operation,
		ConfirmationHash: HashConfirmation(operation.ConfirmationPhrase),
	}); err != nil || !created {
		t.Fatalf("create batch: created=%v err=%v", created, err)
	}

	approved, err := database.ApproveBatchOperation(context.Background(), operation.ID, first, operation.Digest, operation.ConfirmationPhrase)
	if err != nil || approved.State != model.BatchPendingApproval || approved.ApprovedByHash != first || approved.SecondApprovedByHash != "" {
		t.Fatalf("first approval=%+v err=%v", approved, err)
	}
	retriedFirst, err := database.ApproveBatchOperation(context.Background(), operation.ID, first, operation.Digest, operation.ConfirmationPhrase)
	if err != nil || retriedFirst.ApprovedByHash != first || retriedFirst.State != model.BatchPendingApproval {
		t.Fatalf("first retry=%+v err=%v", retriedFirst, err)
	}
	if _, err := database.ApproveBatchOperation(context.Background(), operation.ID, first, operation.Digest, operation.ConfirmationPhrase); err != nil {
		// The call above is intentionally a second retry; it must remain harmless.
		t.Fatalf("repeated first retry err=%v", err)
	}
	completed, err := database.ApproveBatchOperation(context.Background(), operation.ID, second, operation.Digest, operation.ConfirmationPhrase)
	if err != nil || completed.State != model.BatchApproved || completed.SecondApprovedByHash != second {
		t.Fatalf("second approval=%+v err=%v", completed, err)
	}
	retriedSecond, err := database.ApproveBatchOperation(context.Background(), operation.ID, second, operation.Digest, operation.ConfirmationPhrase)
	if err != nil || retriedSecond.State != model.BatchApproved || retriedSecond.SecondApprovedByHash != second {
		t.Fatalf("second retry=%+v err=%v", retriedSecond, err)
	}
	executor := strings.Repeat("4", 64)
	runKey := "batch-run-key"
	if _, started, err := database.StartBatchOperation(context.Background(), operation.ID, executor, runKey); err != nil || !started {
		t.Fatalf("start batch: started=%v err=%v", started, err)
	}
	if _, started, err := database.StartBatchOperation(context.Background(), operation.ID, executor, runKey); err != nil || started {
		t.Fatalf("same executor replay: started=%v err=%v", started, err)
	}
	if _, _, err := database.StartBatchOperation(context.Background(), operation.ID, strings.Repeat("5", 64), runKey); !errors.Is(err, ErrActorMismatch) {
		t.Fatalf("different executor replay err=%v, want ErrActorMismatch", err)
	}
}
