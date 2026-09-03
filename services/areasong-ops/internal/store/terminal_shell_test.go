package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

func TestTerminalShellApprovalExpiresAndRequiresDistinctActors(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, time.September, 2, 8, 0, 0, 0, time.UTC)
	database.now = func() time.Time { return now }
	creator, first, second := strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)
	plan := model.TerminalShellPlan{
		ID: "11111111-1111-4111-8111-111111111111", ObjectID: "service:demo",
		State: "pending_approval", ActorHash: creator, InputDigest: "sha256:input",
		ConfirmationPhrase: "批准紧急终端 test", CreatedAt: now, ExpiresAt: now.Add(time.Minute),
	}
	if _, created, err := database.CreateTerminalShellPlan(ctx, plan, "create-key", "sha256:request"); err != nil || !created {
		t.Fatalf("created=%v err=%v", created, err)
	}
	approved, err := database.ApproveTerminalShellPlan(ctx, plan.ID, first, plan.ConfirmationPhrase)
	if err != nil || approved.State != "pending_second_approval" {
		t.Fatalf("first approval=%+v err=%v", approved, err)
	}
	if _, err := database.ApproveTerminalShellPlan(ctx, plan.ID, first, plan.ConfirmationPhrase); err == nil {
		t.Fatal("same actor completed both approvals")
	}
	now = now.Add(2 * time.Minute)
	expired, err := database.ExpireTerminalShellPlans(ctx, now)
	if err != nil || len(expired) != 1 || expired[0].State != "expired" {
		t.Fatalf("expired=%+v err=%v", expired, err)
	}
	if _, err := database.ApproveTerminalShellPlan(ctx, plan.ID, second, plan.ConfirmationPhrase); err == nil {
		t.Fatal("expired terminal plan was approved")
	}
	if _, _, err := database.StartTerminalShellPlan(ctx, plan.ID, creator, "execute-key", plan.InputDigest); err == nil {
		t.Fatal("expired terminal plan was executed")
	}
}

func TestTerminalShellMigrationDowngradesLegacySingleApproval(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-terminal.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(schema); err != nil {
		t.Fatal(err)
	}
	// Locate the exact dual-approval migration so later additive migrations do
	// not silently change which legacy schema this fixture represents.
	legacyVersion := -1
	for index, migration := range migrations {
		if strings.Contains(migration, "ALTER TABLE terminal_shell_plans ADD COLUMN second_approved_by_hash") {
			legacyVersion = index
			break
		}
	}
	if legacyVersion < 0 {
		t.Fatal("terminal dual-approval migration not found")
	}
	for index, migration := range migrations[:legacyVersion] {
		if _, err := raw.Exec(migration); err != nil {
			t.Fatalf("migration %d: %v", index+1, err)
		}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = raw.Exec(`INSERT INTO terminal_shell_plans(
		id,idempotency_key,request_digest,object_id,state,actor_hash,input_digest,
		confirmation_hash,confirmation_phrase,approved_by_hash,created_at,expires_at,approved_at
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		"22222222-2222-4222-8222-222222222222", "legacy-key", "sha256:request", "service:demo",
		"approved", strings.Repeat("a", 64), "sha256:input", HashConfirmation("approve"), "approve",
		strings.Repeat("b", 64), now, time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano), now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec("PRAGMA user_version = " + strconv.Itoa(legacyVersion)); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	migrated, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	plan, err := migrated.GetTerminalShellPlan(context.Background(), "22222222-2222-4222-8222-222222222222")
	if err != nil {
		t.Fatal(err)
	}
	if plan.State != "pending_second_approval" || plan.ApprovedByHash == "" || plan.SecondApprovedByHash != "" {
		t.Fatalf("migrated plan=%+v", plan)
	}
	if _, _, err := migrated.StartTerminalShellPlan(context.Background(), plan.ID, plan.ActorHash, "new-execution-key", plan.InputDigest); err == nil {
		t.Fatal("legacy single-approval plan remained executable")
	}
}
