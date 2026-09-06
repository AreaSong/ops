package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AreaSong/ops/services/areasong-ops/internal/config"
	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

func TestManagedFileRequiresIndependentApprovalAndCreatorExecution(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "managed.conf")
	oldContent, newContent := "enabled=false\n", "enabled=true\n"
	if err := os.WriteFile(target, []byte(oldContent), 0o600); err != nil {
		t.Fatal(err)
	}

	engine, _ := testEngine(t, &fakeExecutor{})
	engine.catalog.Files = &config.FilePolicy{
		Enabled: true, Roots: map[string]string{"managed": root}, MaxFileBytes: 4096,
	}
	actors := []string{actorHash(), strings.Repeat("b", 64), strings.Repeat("c", 64), strings.Repeat("d", 64)}
	ctx := context.Background()
	proposal, created, err := engine.ProposeManagedFile(ctx, actors[0], model.ManagedFileRequest{
		RootID: "managed", Path: "managed.conf", Content: newContent,
		ExpectedDigest: digestText(oldContent), Mode: "propose", IdempotencyKey: mustUUID(t),
	})
	if err != nil || !created {
		t.Fatalf("proposal=%+v created=%v err=%v", proposal, created, err)
	}
	if _, err := engine.ApplyManagedFileProposal(ctx, actors[3], proposal.ID,
		model.ManagedFileApplyRequest{IdempotencyKey: mustUUID(t)}); err == nil {
		t.Fatal("unapproved managed file proposal was applied")
	}
	if content, _ := os.ReadFile(target); string(content) != oldContent {
		t.Fatalf("unapproved apply changed file content=%q", content)
	}

	proposal, err = engine.ApproveManagedFileProposal(ctx, actors[1], proposal.ID,
		model.ManagedFileApprovalRequest{Digest: proposal.ProposedDigest, Confirmation: proposal.ConfirmationPhrase})
	if err != nil {
		t.Fatal(err)
	}
	if proposal.State != "approved" || proposal.ApprovalPolicy != model.ApprovalPolicyTwoParty || proposal.SecondApprovedByHash != "" {
		t.Fatalf("proposal state=%s want approved", proposal.State)
	}
	applied, err := engine.ApplyManagedFileProposal(ctx, actors[0], proposal.ID,
		model.ManagedFileApplyRequest{IdempotencyKey: mustUUID(t)})
	if err != nil || applied.State != "applied" {
		t.Fatalf("applied=%+v err=%v", applied, err)
	}
	if content, _ := os.ReadFile(target); string(content) != newContent {
		t.Fatalf("applied file content=%q", content)
	}
}
