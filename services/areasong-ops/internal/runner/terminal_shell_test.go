package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AreaSong/ops/services/areasong-ops/internal/config"
	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	"github.com/AreaSong/ops/services/areasong-ops/internal/store"
)

func TestTerminalShellRequiresIndependentApprovalAndCreatorExecution(t *testing.T) {
	ctx := context.Background()
	engine, database := testEngine(t, &fakeExecutor{})
	engine.catalog.Terminal = &config.TerminalPolicy{
		Enabled: true, BreakGlass: true, MaxSessionSeconds: 60,
		ShellExecutable: "/bin/bash", ShellWorkingDir: t.TempDir(),
	}
	creator := actorHash()
	firstApprover := strings.Repeat("b", 64)
	outputPath := filepath.Join(t.TempDir(), "executed")
	input := "printf 'executed' > " + outputPath + " && printf 'break-glass-ok'"
	plan, created, err := engine.CreateTerminalShellPlan(ctx, creator, model.TerminalShellPlanRequest{
		ObjectID: "service:demo", Input: input, Confirmation: "申请紧急终端 service:demo",
		IdempotencyKey: mustUUID(t),
	})
	if err != nil || !created {
		t.Fatalf("plan=%+v created=%v err=%v", plan, created, err)
	}
	if _, err := engine.ApproveTerminalShellPlan(ctx, creator, plan.ID, model.TerminalShellApprovalRequest{
		Confirmation: plan.ConfirmationPhrase,
	}); err == nil {
		t.Fatal("creator approved own terminal plan")
	}
	if _, err := engine.ExecuteTerminalShellPlan(ctx, creator, plan.ID, model.TerminalShellExecuteRequest{
		Input: input, IdempotencyKey: mustUUID(t),
	}); err == nil {
		t.Fatal("unapproved terminal plan executed")
	}

	plan, err = engine.ApproveTerminalShellPlan(ctx, firstApprover, plan.ID, model.TerminalShellApprovalRequest{
		Confirmation: plan.ConfirmationPhrase,
	})
	if err != nil || plan.State != "approved" || plan.ApprovedByHash != firstApprover || plan.SecondApprovedByHash != "" {
		t.Fatalf("approval plan=%+v err=%v", plan, err)
	}
	if _, err := engine.ExecuteTerminalShellPlan(ctx, firstApprover, plan.ID, model.TerminalShellExecuteRequest{
		Input: input, IdempotencyKey: mustUUID(t),
	}); !errors.Is(err, store.ErrActorMismatch) {
		t.Fatalf("approver execution err=%v, want actor mismatch", err)
	}
	if _, err := engine.ExecuteTerminalShellPlan(ctx, creator, plan.ID, model.TerminalShellExecuteRequest{
		Input: input + " ", IdempotencyKey: mustUUID(t),
	}); err == nil {
		t.Fatal("terminal plan accepted input outside approved digest")
	}
	executionKey := mustUUID(t)
	plan, err = engine.ExecuteTerminalShellPlan(ctx, creator, plan.ID, model.TerminalShellExecuteRequest{
		Input: input, IdempotencyKey: executionKey,
	})
	if err != nil || plan.State != "succeeded" || plan.Output != "break-glass-ok" {
		t.Fatalf("executed plan=%+v err=%v", plan, err)
	}
	if content, readErr := os.ReadFile(outputPath); readErr != nil || string(content) != "executed" {
		t.Fatalf("execution marker=%q err=%v", content, readErr)
	}
	replayed, err := engine.ExecuteTerminalShellPlan(ctx, creator, plan.ID, model.TerminalShellExecuteRequest{
		Input: input, IdempotencyKey: executionKey,
	})
	if err != nil || replayed.State != "succeeded" {
		t.Fatalf("replayed plan=%+v err=%v", replayed, err)
	}

	audit, err := database.ListAudit(ctx, 20, 0)
	if err != nil {
		t.Fatal(err)
	}
	steps, executed := map[string]bool{}, 0
	for _, entry := range audit {
		if entry.Resource != plan.ID {
			continue
		}
		if entry.Event == "terminal.shell.approved" {
			step, _ := entry.Detail["approvalStep"].(string)
			steps[step] = true
		}
		if entry.Event == "terminal.shell.executed" {
			executed++
		}
	}
	if !steps["first"] || steps["second"] || executed != 1 {
		t.Fatalf("approval steps=%v executed audits=%d audit=%+v", steps, executed, audit)
	}
}
