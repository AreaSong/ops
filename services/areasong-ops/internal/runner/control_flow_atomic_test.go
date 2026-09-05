package runner

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	"github.com/AreaSong/ops/services/areasong-ops/internal/store"
)

func TestRejectedTaskAuditFailureIsReturned(t *testing.T) {
	ctx := context.Background()
	engine, _ := testEngine(t, &fakeExecutor{})
	preview, err := engine.CreatePreview(ctx, actorHash(), model.PreviewRequest{
		Service: "demo", Action: "restart",
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", filepath.Join(engine.stateRoot, "ops.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.ExecContext(ctx, `CREATE TRIGGER reject_task_rejected_audit
		BEFORE INSERT ON audit_entries WHEN NEW.event='task.rejected'
		BEGIN SELECT RAISE(ABORT, 'injected task rejection audit failure'); END`); err != nil {
		t.Fatal(err)
	}
	_, _, err = engine.StartTask(ctx, actorHash(), model.StartTaskRequest{
		PreviewID: preview.ID, Confirmation: "wrong phrase", IdempotencyKey: mustUUID(t),
	})
	if !errors.Is(err, store.ErrConfirmation) ||
		!strings.Contains(err.Error(), "injected task rejection audit failure") {
		t.Fatalf("err=%v, want confirmation and audit failures", err)
	}
	var rejectedAudits int
	if err := raw.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM audit_entries WHERE event='task.rejected'`).Scan(&rejectedAudits); err != nil {
		t.Fatal(err)
	}
	if rejectedAudits != 0 {
		t.Fatalf("rejected audits=%d want=0", rejectedAudits)
	}
}
