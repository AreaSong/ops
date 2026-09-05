package store

import (
	"context"
	"testing"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

func TestRestrictedTerminalSessionAndAuditAreAtomic(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, time.September, 5, 8, 0, 0, 0, time.UTC)
	session := model.TerminalSession{
		ID: "session-a", IdempotencyKey: "request-a", RequestDigest: "digest-a",
		ObjectID: "service:a", Command: "status", State: "running", ActorHash: "actor-a",
		CreatedAt: now, ExpiresAt: now.Add(time.Minute),
	}
	installTerminalSessionAuditFailure(t, database)
	if _, _, err := database.ReserveTerminalSession(ctx, session); err == nil {
		t.Fatal("terminal session reservation survived audit failure")
	}
	if _, found, err := terminalSessionByID(ctx, database.db, session.ID); err != nil || found {
		t.Fatalf("failed reservation found=%v err=%v", found, err)
	}
	dropTerminalSessionAuditFailure(t, database)
	if _, fresh, err := database.ReserveTerminalSession(ctx, session); err != nil || !fresh {
		t.Fatalf("reserve fresh=%v err=%v", fresh, err)
	}
	installTerminalSessionAuditFailure(t, database)
	if err := database.CompleteTerminalSession(ctx, session.ID, "succeeded", 0, "ok"); err == nil {
		t.Fatal("terminal session completion survived audit failure")
	}
	stored, found, err := terminalSessionByID(ctx, database.db, session.ID)
	if err != nil || !found {
		t.Fatalf("session found=%v err=%v", found, err)
	}
	if stored.State != "running" || stored.Output != "" || stored.ExitCode != 0 {
		t.Fatalf("failed completion changed session: %+v", stored)
	}
}

func installTerminalSessionAuditFailure(t *testing.T, database *Store) {
	t.Helper()
	if _, err := database.db.Exec(`CREATE TRIGGER fail_terminal_session_audit BEFORE INSERT ON audit_entries
		WHEN NEW.event LIKE 'terminal.command%' BEGIN SELECT RAISE(ABORT, 'forced terminal session audit failure'); END`); err != nil {
		t.Fatal(err)
	}
}

func dropTerminalSessionAuditFailure(t *testing.T, database *Store) {
	t.Helper()
	if _, err := database.db.Exec(`DROP TRIGGER fail_terminal_session_audit`); err != nil {
		t.Fatal(err)
	}
}
