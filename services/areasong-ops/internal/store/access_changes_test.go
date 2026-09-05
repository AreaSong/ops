package store

import (
	"context"
	"errors"
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
	retriedFirst, err := database.ApproveAccessChange(ctx, change.ID, first, change.RequestDigest, change.ConfirmationPhrase)
	if err != nil || retriedFirst.State != model.AccessChangePendingApproval || retriedFirst.ApprovedByHash != first {
		t.Fatalf("same approver retry=%+v err=%v", retriedFirst, err)
	}
	approved, err = database.ApproveAccessChange(ctx, change.ID, second, change.RequestDigest, change.ConfirmationPhrase)
	if err != nil || approved.State != model.AccessChangeApproved || approved.SecondApprovedByHash != second {
		t.Fatalf("second approval=%+v err=%v", approved, err)
	}
	retriedSecond, err := database.ApproveAccessChange(ctx, change.ID, second, change.RequestDigest, change.ConfirmationPhrase)
	if err != nil || retriedSecond.State != model.AccessChangeApproved || retriedSecond.SecondApprovedByHash != second {
		t.Fatalf("second approver retry=%+v err=%v", retriedSecond, err)
	}
	duplicate, fresh, err := database.CreateAccessChange(ctx, change, `{"enforced":true}`, HashConfirmation(change.ConfirmationPhrase))
	if err != nil || fresh || duplicate.State != model.AccessChangeApproved {
		t.Fatalf("duplicate=%+v fresh=%v err=%v", duplicate, fresh, err)
	}
	entries, err := database.ListAudit(ctx, 20, 0)
	if err != nil {
		t.Fatal(err)
	}
	if countAuditEvent(entries, "access.change.created") != 1 || countAuditEvent(entries, "access.change.approved") != 2 {
		t.Fatalf("approval retries duplicated audit entries: %+v", entries)
	}
}

func TestAccessChangeApprovalAndRejectionAuditAreAtomic(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "ops.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	change := model.AccessChange{
		ID: "atomic-audit-change", IdempotencyKey: "atomic-audit-key", RequestDigest: "sha256:atomic-audit",
		ActorHash: "creator", State: model.AccessChangePendingApproval, RequiresDualApproval: true,
		ConfirmationPhrase: "批准访问策略变更 sha256:atomic-audit", CreatedAt: time.Now().UTC(),
	}

	installAuditFailureTrigger(t, database)
	if _, created, err := database.CreateAccessChange(ctx, change, `{}`, HashConfirmation(change.ConfirmationPhrase)); err == nil || created {
		t.Fatalf("create unexpectedly committed while audit failed: created=%v err=%v", created, err)
	}
	assertAccessChangeAndAuditCounts(t, database, 0, 0)
	dropAuditFailureTrigger(t, database)

	if _, created, err := database.CreateAccessChange(ctx, change, `{}`, HashConfirmation(change.ConfirmationPhrase)); err != nil || !created {
		t.Fatalf("create after trigger removal created=%v err=%v", created, err)
	}
	assertAccessChangeAndAuditCounts(t, database, 1, 1)

	installAuditFailureTrigger(t, database)
	if _, err := database.ApproveAccessChange(ctx, change.ID, "first", change.RequestDigest, change.ConfirmationPhrase); err == nil {
		t.Fatal("first approval unexpectedly committed while audit failed")
	}
	stored, err := database.GetAccessChange(ctx, change.ID)
	if err != nil || stored.State != model.AccessChangePendingApproval || stored.ApprovedByHash != "" {
		t.Fatalf("failed first approval left state=%+v err=%v", stored, err)
	}
	assertAccessChangeAndAuditCounts(t, database, 1, 1)
	dropAuditFailureTrigger(t, database)

	if _, err := database.ApproveAccessChange(ctx, change.ID, "first", change.RequestDigest, change.ConfirmationPhrase); err != nil {
		t.Fatal(err)
	}
	installAuditFailureTrigger(t, database)
	if _, err := database.ApproveAccessChange(ctx, change.ID, "second", change.RequestDigest, change.ConfirmationPhrase); err == nil {
		t.Fatal("second approval unexpectedly committed while audit failed")
	}
	stored, err = database.GetAccessChange(ctx, change.ID)
	if err != nil || stored.State != model.AccessChangePendingApproval || stored.SecondApprovedByHash != "" {
		t.Fatalf("failed second approval left state=%+v err=%v", stored, err)
	}
	assertAccessChangeAndAuditCounts(t, database, 1, 2)
	dropAuditFailureTrigger(t, database)
	if approved, err := database.ApproveAccessChange(ctx, change.ID, "second", change.RequestDigest, change.ConfirmationPhrase); err != nil || approved.State != model.AccessChangeApproved {
		t.Fatalf("second approval after trigger removal=%+v err=%v", approved, err)
	}

	rejected := change
	rejected.ID = "atomic-reject-change"
	rejected.IdempotencyKey = "atomic-reject-key"
	rejected.RequestDigest = "sha256:atomic-reject"
	rejected.ConfirmationPhrase = "批准访问策略变更 sha256:atomic-reject"
	if _, created, err := database.CreateAccessChange(ctx, rejected, `{}`, HashConfirmation(rejected.ConfirmationPhrase)); err != nil || !created {
		t.Fatalf("reject fixture create=%v err=%v", created, err)
	}
	installAuditFailureTrigger(t, database)
	if _, err := database.RejectAccessChange(ctx, rejected.ID, rejected.ActorHash, "取消"); err == nil {
		t.Fatal("rejection unexpectedly committed while audit failed")
	}
	stored, err = database.GetAccessChange(ctx, rejected.ID)
	if err != nil || stored.State != model.AccessChangePendingApproval || stored.Error != "" {
		t.Fatalf("failed rejection left state=%+v err=%v", stored, err)
	}
	dropAuditFailureTrigger(t, database)
	if result, err := database.RejectAccessChange(ctx, rejected.ID, rejected.ActorHash, "取消"); err != nil || result.State != model.AccessChangeRejected {
		t.Fatalf("rejection after trigger removal=%+v err=%v", result, err)
	}
	if result, err := database.RejectAccessChange(ctx, rejected.ID, rejected.ActorHash, "取消"); err != nil || result.State != model.AccessChangeRejected {
		t.Fatalf("rejection retry=%+v err=%v", result, err)
	}
	entries, err := database.ListAudit(ctx, 20, 0)
	if err != nil {
		t.Fatal(err)
	}
	if countAuditEvent(entries, "access.change.rejected") != 1 {
		t.Fatalf("rejection retry duplicated audit entries: %+v", entries)
	}
}

func TestRejectedAccessChangeDoesNotReplayHistoricalApproval(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "ops.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	change := model.AccessChange{
		ID: "rejected-approval-replay", IdempotencyKey: "rejected-approval-key",
		RequestDigest: "sha256:rejected-approval", ActorHash: "creator",
		State: model.AccessChangePendingApproval, RequiresDualApproval: true,
		ConfirmationPhrase: "批准访问策略变更 sha256:rejected-approval", CreatedAt: time.Now().UTC(),
	}
	if _, created, err := database.CreateAccessChange(
		ctx, change, "{}", HashConfirmation(change.ConfirmationPhrase),
	); err != nil || !created {
		t.Fatalf("create change created=%v err=%v", created, err)
	}
	if _, err := database.ApproveAccessChange(
		ctx, change.ID, "first", change.RequestDigest, change.ConfirmationPhrase,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := database.RejectAccessChange(ctx, change.ID, change.ActorHash, "取消"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ApproveAccessChange(
		ctx, change.ID, "first", change.RequestDigest, change.ConfirmationPhrase,
	); err == nil {
		t.Fatal("historical approval unexpectedly replayed after rejection")
	}
	stored, err := database.GetAccessChange(ctx, change.ID)
	if err != nil || stored.State != model.AccessChangeRejected || stored.Error != "取消" {
		t.Fatalf("rejected change=%+v err=%v", stored, err)
	}
	entries, err := database.ListAudit(ctx, 20, 0)
	if err != nil {
		t.Fatal(err)
	}
	if countAuditEvent(entries, "access.change.approved") != 1 ||
		countAuditEvent(entries, "access.change.rejected") != 1 {
		t.Fatalf("historical replay changed audit entries: %+v", entries)
	}
}

func installAuditFailureTrigger(t *testing.T, database *Store) {
	t.Helper()
	if _, err := database.db.Exec(`CREATE TRIGGER audit_failure BEFORE INSERT ON audit_entries BEGIN SELECT RAISE(ABORT, 'audit failure'); END`); err != nil {
		t.Fatal(err)
	}
}

func dropAuditFailureTrigger(t *testing.T, database *Store) {
	t.Helper()
	if _, err := database.db.Exec(`DROP TRIGGER audit_failure`); err != nil {
		t.Fatal(err)
	}
}

func assertAccessChangeAndAuditCounts(t *testing.T, database *Store, changes, audits int) {
	t.Helper()
	var changeCount, auditCount int
	if err := database.db.QueryRow(`SELECT COUNT(*) FROM access_changes`).Scan(&changeCount); err != nil {
		t.Fatal(err)
	}
	if err := database.db.QueryRow(`SELECT COUNT(*) FROM audit_entries`).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if changeCount != changes || auditCount != audits {
		t.Fatalf("access changes=%d audits=%d want changes=%d audits=%d", changeCount, auditCount, changes, audits)
	}
}

func countAuditEvent(entries []model.AuditEntry, event string) int {
	count := 0
	for _, entry := range entries {
		if entry.Event == event {
			count++
		}
	}
	return count
}

func TestCreateAccessChangeRejectsExistingMutationReceipt(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "ops.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	ctx := context.Background()
	key := "receipt-key"
	if _, err := database.db.ExecContext(ctx, `INSERT INTO access_mutation_receipts(
		idempotency_key,actor_hash,request_digest,created_at) VALUES(?,?,?,?)`,
		key, "actor-a", "sha256:policy", timeText(time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	change := model.AccessChange{
		ID: "change-receipt-conflict", IdempotencyKey: key, RequestDigest: "sha256:change",
		ActorHash: "actor-a", State: model.AccessChangePendingApproval,
		ConfirmationPhrase: "批准访问策略变更 sha256:change", RequiresDualApproval: true,
		CreatedAt: time.Now().UTC(),
	}
	if _, created, err := database.CreateAccessChange(ctx, change, `{}`, HashConfirmation(change.ConfirmationPhrase)); !errors.Is(err, ErrIdempotency) || created {
		t.Fatalf("same actor conflict created=%v err=%v", created, err)
	}
	change.ID = "change-receipt-other-actor"
	change.ActorHash = "actor-b"
	if _, created, err := database.CreateAccessChange(ctx, change, `{}`, HashConfirmation(change.ConfirmationPhrase)); !errors.Is(err, ErrActorMismatch) || created {
		t.Fatalf("other actor conflict created=%v err=%v", created, err)
	}
	var count int
	if err := database.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM access_changes`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("access changes count=%d err=%v", count, err)
	}
}
