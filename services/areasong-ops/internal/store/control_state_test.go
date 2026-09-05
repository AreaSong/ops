package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

func TestSetDesiredStateIdempotencyBindsActorAndDigest(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	input := DesiredStateInput{
		Service:   "demo",
		ObjectID:  "service:demo",
		TenantID:  "default",
		Desired:   model.DesiredMaintenance,
		Reason:    "planned maintenance",
		ActorHash: "actor-a",
	}

	first, replayed, err := database.SetDesiredStateIdempotent(ctx, input, "request-1", "digest-1")
	if err != nil || replayed {
		t.Fatalf("first state=%+v replayed=%v err=%v", first, replayed, err)
	}
	second, replayed, err := database.SetDesiredStateIdempotent(ctx, input, "request-1", "digest-1")
	if err != nil || !replayed {
		t.Fatalf("replay state=%+v replayed=%v err=%v", second, replayed, err)
	}
	if second.Generation != first.Generation || second.Desired != first.Desired {
		t.Fatalf("replay changed state: first=%+v second=%+v", first, second)
	}

	var events, receipts int
	if err := database.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM control_plane_events WHERE event_type='desired_state.changed'`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := database.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM desired_state_requests`).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if events != 1 || receipts != 1 {
		t.Fatalf("events=%d receipts=%d want one each", events, receipts)
	}
	var eventSequence, receiptSequence int64
	if err := database.db.QueryRowContext(ctx,
		`SELECT sequence FROM control_plane_events WHERE event_type='desired_state.changed' ORDER BY sequence LIMIT 1`).Scan(&eventSequence); err != nil {
		t.Fatal(err)
	}
	if err := database.db.QueryRowContext(ctx,
		`SELECT event_sequence FROM desired_state_requests WHERE idempotency_key=?`, "request-1").Scan(&receiptSequence); err != nil {
		t.Fatal(err)
	}
	if receiptSequence != eventSequence {
		t.Fatalf("receipt event_sequence=%d event sequence=%d", receiptSequence, eventSequence)
	}

	if _, _, err := database.SetDesiredStateIdempotent(ctx, input, "request-1", "digest-other"); !errors.Is(err, ErrIdempotency) {
		t.Fatalf("digest mismatch err=%v, want ErrIdempotency", err)
	}
	otherActor := input
	otherActor.ActorHash = "actor-b"
	if _, _, err := database.SetDesiredStateIdempotent(ctx, otherActor, "request-1", "digest-1"); !errors.Is(err, ErrActorMismatch) {
		t.Fatalf("actor mismatch err=%v, want ErrActorMismatch", err)
	}

	third, replayed, err := database.SetDesiredStateIdempotent(ctx, input, "request-2", "digest-2")
	if err != nil || replayed {
		t.Fatalf("new request state=%+v replayed=%v err=%v", third, replayed, err)
	}
	if third.Generation != first.Generation+1 {
		t.Fatalf("generation=%d want=%d", third.Generation, first.Generation+1)
	}
	if err := database.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM control_plane_events WHERE event_type='desired_state.changed'`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 2 {
		t.Fatalf("events=%d want 2 after distinct request", events)
	}
}

func TestLegacySetDesiredStateDoesNotRequireReceiptTableColumns(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "ops.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	state, err := database.SetDesiredState(context.Background(), DesiredStateInput{
		Service: "legacy", ObjectID: "service:legacy", Desired: model.DesiredRunning,
	})
	if err != nil {
		t.Fatalf("legacy state=%+v err=%v", state, err)
	}
	if state.Generation != 1 {
		t.Fatalf("generation=%d want 1", state.Generation)
	}
}

func TestSetDesiredStateAndAuditAreAtomic(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	if _, err := database.db.Exec(`CREATE TRIGGER fail_desired_state_audit BEFORE INSERT ON audit_entries
		WHEN NEW.event='desired_state.changed' BEGIN SELECT RAISE(ABORT, 'forced desired-state audit failure'); END`); err != nil {
		t.Fatal(err)
	}
	input := DesiredStateInput{
		Service: "demo", ObjectID: "service:demo", TenantID: "tenant-a",
		Desired: model.DesiredMaintenance, Reason: "planned", ActorHash: "actor-a",
	}
	if _, _, err := database.SetDesiredStateIdempotent(ctx, input, "request-a", "digest-a"); err == nil {
		t.Fatal("desired-state write survived audit failure")
	}
	for table, query := range map[string]string{
		"desired state": `SELECT COUNT(*) FROM desired_states`,
		"control event": `SELECT COUNT(*) FROM control_plane_events`,
		"receipt":       `SELECT COUNT(*) FROM desired_state_requests`,
	} {
		var count int
		if err := database.db.QueryRowContext(ctx, query).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%s count=%d after audit failure", table, count)
		}
	}
}
