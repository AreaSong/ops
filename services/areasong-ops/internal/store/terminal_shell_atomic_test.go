package store

import (
	"context"
	"testing"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

func TestTerminalShellStateAndAuditAreAtomic(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Round(0)
	database.now = func() time.Time { return now }
	plan := atomicTerminalShellPlan(now)

	installTerminalAuditFailure(t, database)
	if _, _, err := database.CreateTerminalShellPlan(ctx, plan, "create-key", "sha256:request"); err == nil {
		t.Fatal("terminal plan creation survived audit failure")
	}
	assertTerminalShellState(t, database, plan.ID, ErrNotFound, "")
	dropTerminalAuditFailure(t, database)
	if _, created, err := database.CreateTerminalShellPlan(ctx, plan, "create-key", "sha256:request"); err != nil || !created {
		t.Fatalf("created=%v err=%v", created, err)
	}

	installTerminalAuditFailure(t, database)
	if _, err := database.ApproveTerminalShellPlan(ctx, plan.ID, "actor-b", plan.ConfirmationPhrase); err == nil {
		t.Fatal("terminal first approval survived audit failure")
	}
	assertTerminalShellState(t, database, plan.ID, nil, "pending_approval")
	dropTerminalAuditFailure(t, database)
	if _, err := database.ApproveTerminalShellPlan(ctx, plan.ID, "actor-b", plan.ConfirmationPhrase); err != nil {
		t.Fatal(err)
	}

	installTerminalAuditFailure(t, database)
	if _, err := database.ApproveTerminalShellPlan(ctx, plan.ID, "actor-c", plan.ConfirmationPhrase); err == nil {
		t.Fatal("terminal second approval survived audit failure")
	}
	assertTerminalShellState(t, database, plan.ID, nil, "pending_second_approval")
	dropTerminalAuditFailure(t, database)
	if _, err := database.ApproveTerminalShellPlan(ctx, plan.ID, "actor-c", plan.ConfirmationPhrase); err != nil {
		t.Fatal(err)
	}

	installTerminalAuditFailure(t, database)
	if _, _, err := database.StartTerminalShellPlan(ctx, plan.ID, plan.ActorHash, "execute-key", plan.InputDigest); err == nil {
		t.Fatal("terminal start survived audit failure")
	}
	assertTerminalShellState(t, database, plan.ID, nil, "approved")
	dropTerminalAuditFailure(t, database)
	if _, started, err := database.StartTerminalShellPlan(ctx, plan.ID, plan.ActorHash, "execute-key", plan.InputDigest); err != nil || !started {
		t.Fatalf("started=%v err=%v", started, err)
	}

	installTerminalAuditFailure(t, database)
	if err := database.FinishTerminalShellPlan(ctx, plan.ID, "succeeded", 0, "ok", ""); err == nil {
		t.Fatal("terminal finish survived audit failure")
	}
	assertTerminalShellState(t, database, plan.ID, nil, "running")
	dropTerminalAuditFailure(t, database)
	if err := database.FinishTerminalShellPlan(ctx, plan.ID, "succeeded", 0, "ok", ""); err != nil {
		t.Fatal(err)
	}
	assertTerminalShellState(t, database, plan.ID, nil, "succeeded")
	assertTerminalAuditEvents(t, database, plan.ID)
}

func TestTerminalShellExpiryAndInterruptedRecoveryAuditsAreAtomic(t *testing.T) {
	t.Run("expiry", func(t *testing.T) {
		database := openTestStore(t)
		ctx := context.Background()
		now := time.Now().UTC().Round(0)
		database.now = func() time.Time { return now }
		plan := atomicTerminalShellPlan(now)
		if _, _, err := database.CreateTerminalShellPlan(ctx, plan, "create-key", "sha256:request"); err != nil {
			t.Fatal(err)
		}
		now = plan.ExpiresAt.Add(time.Second)
		installTerminalAuditFailure(t, database)
		if _, err := database.ExpireTerminalShellPlans(ctx, now); err == nil {
			t.Fatal("terminal expiry survived audit failure")
		}
		assertTerminalShellState(t, database, plan.ID, nil, "pending_approval")
		dropTerminalAuditFailure(t, database)
		if expired, err := database.ExpireTerminalShellPlans(ctx, now); err != nil || len(expired) != 1 {
			t.Fatalf("expired=%+v err=%v", expired, err)
		}
		assertTerminalShellState(t, database, plan.ID, nil, "expired")
	})

	t.Run("interrupted execution", func(t *testing.T) {
		database := openTestStore(t)
		ctx := context.Background()
		plan := atomicTerminalShellPlan(time.Now().UTC())
		if _, _, err := database.CreateTerminalShellPlan(ctx, plan, "create-key", "sha256:request"); err != nil {
			t.Fatal(err)
		}
		for _, actor := range []string{"actor-b", "actor-c"} {
			if _, err := database.ApproveTerminalShellPlan(ctx, plan.ID, actor, plan.ConfirmationPhrase); err != nil {
				t.Fatal(err)
			}
		}
		if _, _, err := database.StartTerminalShellPlan(ctx, plan.ID, plan.ActorHash, "execute-key", plan.InputDigest); err != nil {
			t.Fatal(err)
		}
		installTerminalAuditFailure(t, database)
		if _, _, err := database.StartTerminalShellPlan(ctx, plan.ID, plan.ActorHash, "execute-key", plan.InputDigest); err == nil {
			t.Fatal("terminal interrupted recovery survived audit failure")
		}
		assertTerminalShellState(t, database, plan.ID, nil, "running")
		dropTerminalAuditFailure(t, database)
		if recovered, _, err := database.StartTerminalShellPlan(
			ctx, plan.ID, plan.ActorHash, "execute-key", plan.InputDigest,
		); err == nil || recovered.State != "needs_attention" {
			t.Fatalf("recovered=%+v err=%v", recovered, err)
		}
	})
}

func atomicTerminalShellPlan(now time.Time) model.TerminalShellPlan {
	return model.TerminalShellPlan{
		ID: "11111111-1111-4111-8111-111111111111", ObjectID: "service:demo",
		State: "pending_approval", ActorHash: "actor-a", InputDigest: "sha256:input",
		ConfirmationPhrase: "批准紧急终端 test", CreatedAt: now, ExpiresAt: now.Add(time.Minute),
	}
}

func assertTerminalShellState(t *testing.T, database *Store, id string, wantErr error, state string) {
	t.Helper()
	plan, err := database.GetTerminalShellPlan(context.Background(), id)
	if err != wantErr {
		t.Fatalf("plan error=%v want=%v", err, wantErr)
	}
	if err == nil && plan.State != state {
		t.Fatalf("plan state=%q want=%q", plan.State, state)
	}
}

func assertTerminalAuditEvents(t *testing.T, database *Store, planID string) {
	t.Helper()
	entries, err := database.ListAudit(context.Background(), 20, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{
		"terminal.shell.requested": 1, "terminal.shell.approved": 2,
		"terminal.shell.started": 1, "terminal.shell.executed": 1,
	}
	for _, entry := range entries {
		if entry.Resource == planID {
			want[entry.Event]--
		}
	}
	for event, remaining := range want {
		if remaining != 0 {
			t.Fatalf("audit event %s remaining=%d entries=%+v", event, remaining, entries)
		}
	}
}

func installTerminalAuditFailure(t *testing.T, database *Store) {
	t.Helper()
	if _, err := database.db.Exec(`CREATE TRIGGER fail_terminal_audit BEFORE INSERT ON audit_entries
		WHEN NEW.event LIKE 'terminal.shell.%'
		BEGIN SELECT RAISE(ABORT, 'forced terminal audit failure'); END`); err != nil {
		t.Fatal(err)
	}
}

func dropTerminalAuditFailure(t *testing.T, database *Store) {
	t.Helper()
	if _, err := database.db.Exec(`DROP TRIGGER fail_terminal_audit`); err != nil {
		t.Fatal(err)
	}
}
