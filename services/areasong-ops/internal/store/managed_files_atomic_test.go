package store

import (
	"context"
	"testing"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

func TestManagedFileStateAndAuditAreAtomic(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Round(0)
	database.now = func() time.Time { return now }
	proposal := atomicManagedFileProposal(now)

	installManagedFileAuditFailure(t, database)
	if _, _, err := database.SaveManagedFileProposal(ctx, proposal, "sha256:request"); err == nil {
		t.Fatal("managed file proposal creation survived audit failure")
	}
	assertManagedFileState(t, database, proposal.ID, ErrNotFound, "")
	dropManagedFileAuditFailure(t, database)
	if _, created, err := database.SaveManagedFileProposal(ctx, proposal, "sha256:request"); err != nil || !created {
		t.Fatalf("created=%v err=%v", created, err)
	}

	installManagedFileAuditFailure(t, database)
	if _, err := database.ApproveManagedFileProposal(
		ctx, proposal.ID, "actor-b", proposal.ProposedDigest, proposal.ConfirmationPhrase,
	); err == nil {
		t.Fatal("managed file first approval survived audit failure")
	}
	assertManagedFileState(t, database, proposal.ID, nil, "proposed")
	dropManagedFileAuditFailure(t, database)
	if _, err := database.ApproveManagedFileProposal(
		ctx, proposal.ID, "actor-b", proposal.ProposedDigest, proposal.ConfirmationPhrase,
	); err != nil {
		t.Fatal(err)
	}

	installManagedFileAuditFailure(t, database)
	if _, err := database.ApproveManagedFileProposal(
		ctx, proposal.ID, "actor-c", proposal.ProposedDigest, proposal.ConfirmationPhrase,
	); err == nil {
		t.Fatal("managed file second approval survived audit failure")
	}
	assertManagedFileState(t, database, proposal.ID, nil, "pending_second_approval")
	dropManagedFileAuditFailure(t, database)
	if _, err := database.ApproveManagedFileProposal(
		ctx, proposal.ID, "actor-c", proposal.ProposedDigest, proposal.ConfirmationPhrase,
	); err != nil {
		t.Fatal(err)
	}

	installManagedFileAuditFailure(t, database)
	if _, _, err := database.StartManagedFileApply(ctx, proposal.ID, "actor-d", "apply-key"); err == nil {
		t.Fatal("managed file apply start survived audit failure")
	}
	assertManagedFileState(t, database, proposal.ID, nil, "approved")
	dropManagedFileAuditFailure(t, database)
	if _, started, err := database.StartManagedFileApply(ctx, proposal.ID, "actor-d", "apply-key"); err != nil || !started {
		t.Fatalf("started=%v err=%v", started, err)
	}

	installManagedFileAuditFailure(t, database)
	if err := database.FinishManagedFileApply(ctx, proposal.ID, "applied", "/backup/before", ""); err == nil {
		t.Fatal("managed file apply finish survived audit failure")
	}
	assertManagedFileState(t, database, proposal.ID, nil, "applying")
	dropManagedFileAuditFailure(t, database)
	if err := database.FinishManagedFileApply(ctx, proposal.ID, "applied", "/backup/before", ""); err != nil {
		t.Fatal(err)
	}

	installManagedFileAuditFailure(t, database)
	if _, _, err := database.StartManagedFileRollback(ctx, proposal.ID, "actor-e", "rollback-key"); err == nil {
		t.Fatal("managed file rollback start survived audit failure")
	}
	assertManagedFileState(t, database, proposal.ID, nil, "applied")
	dropManagedFileAuditFailure(t, database)
	if _, started, err := database.StartManagedFileRollback(ctx, proposal.ID, "actor-e", "rollback-key"); err != nil || !started {
		t.Fatalf("rollback started=%v err=%v", started, err)
	}

	installManagedFileAuditFailure(t, database)
	if err := database.FinishManagedFileRollback(ctx, proposal.ID, "rolled_back", ""); err == nil {
		t.Fatal("managed file rollback finish survived audit failure")
	}
	assertManagedFileState(t, database, proposal.ID, nil, "rolling_back")
	dropManagedFileAuditFailure(t, database)
	if err := database.FinishManagedFileRollback(ctx, proposal.ID, "rolled_back", ""); err != nil {
		t.Fatal(err)
	}
	assertManagedFileState(t, database, proposal.ID, nil, "rolled_back")
	assertManagedFileAuditEvents(t, database, proposal.ID)
}

func TestManagedFileInterruptedApplyRecoveryAuditIsAtomic(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	proposal := atomicManagedFileProposal(time.Now().UTC())
	if _, _, err := database.SaveManagedFileProposal(ctx, proposal, "sha256:request"); err != nil {
		t.Fatal(err)
	}
	for _, actor := range []string{"actor-b", "actor-c"} {
		if _, err := database.ApproveManagedFileProposal(
			ctx, proposal.ID, actor, proposal.ProposedDigest, proposal.ConfirmationPhrase,
		); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := database.StartManagedFileApply(ctx, proposal.ID, "actor-d", "apply-key"); err != nil {
		t.Fatal(err)
	}
	installManagedFileAuditFailure(t, database)
	if _, _, err := database.StartManagedFileApply(ctx, proposal.ID, "actor-d", "apply-key"); err == nil {
		t.Fatal("interrupted apply recovery survived audit failure")
	}
	assertManagedFileState(t, database, proposal.ID, nil, "applying")
	dropManagedFileAuditFailure(t, database)
	if recovered, _, err := database.StartManagedFileApply(
		ctx, proposal.ID, "actor-d", "apply-key",
	); err == nil || recovered.State != "needs_attention" {
		t.Fatalf("recovered=%+v err=%v", recovered, err)
	}
}

func atomicManagedFileProposal(now time.Time) model.ManagedFileProposal {
	return model.ManagedFileProposal{
		ID:             "11111111-1111-4111-8111-111111111111",
		IdempotencyKey: "22222222-2222-4222-8222-222222222222",
		ActorHash:      "actor-a", RootID: "managed", Path: "service.conf",
		ExpectedDigest: "sha256:before", ProposedDigest: "sha256:after",
		Content: "enabled=true\n", State: "proposed",
		ConfirmationPhrase: "批准文件变更 managed/service.conf", CreatedAt: now,
	}
}

func assertManagedFileState(t *testing.T, database *Store, id string, wantErr error, state string) {
	t.Helper()
	proposal, err := database.GetManagedFileProposal(context.Background(), id)
	if err != wantErr {
		t.Fatalf("proposal error=%v want=%v", err, wantErr)
	}
	if err == nil && proposal.State != state {
		t.Fatalf("proposal state=%q want=%q", proposal.State, state)
	}
}

func assertManagedFileAuditEvents(t *testing.T, database *Store, proposalID string) {
	t.Helper()
	entries, err := database.ListAudit(context.Background(), 30, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{
		"file.proposal.created": 1, "file.proposal.approved": 2,
		"file.proposal.apply_started": 1, "file.proposal.applied": 1,
		"file.proposal.rollback_started": 1, "file.proposal.rolled_back": 1,
	}
	for _, entry := range entries {
		if entry.Resource == proposalID {
			want[entry.Event]--
		}
	}
	for event, remaining := range want {
		if remaining != 0 {
			t.Fatalf("audit event %s remaining=%d entries=%+v", event, remaining, entries)
		}
	}
}

func installManagedFileAuditFailure(t *testing.T, database *Store) {
	t.Helper()
	if _, err := database.db.Exec(`CREATE TRIGGER fail_managed_file_audit BEFORE INSERT ON audit_entries
		WHEN NEW.event LIKE 'file.%'
		BEGIN SELECT RAISE(ABORT, 'forced managed file audit failure'); END`); err != nil {
		t.Fatal(err)
	}
}

func dropManagedFileAuditFailure(t *testing.T, database *Store) {
	t.Helper()
	if _, err := database.db.Exec(`DROP TRIGGER fail_managed_file_audit`); err != nil {
		t.Fatal(err)
	}
}
