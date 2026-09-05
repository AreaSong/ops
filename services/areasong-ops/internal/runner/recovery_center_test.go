package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	"github.com/AreaSong/ops/services/areasong-ops/internal/store"
)

func enableRecoveryActions(engine *Engine) model.ServiceDefinition {
	service := engine.catalog.Services["demo"]
	service.TenantID, service.ServerID = "production", "losangeles"
	service.RecoveryPointPolicy = &model.RecoveryPointPolicy{
		RequiredArtifactRoles: []string{"postgres-demo"}, RecoverableSeconds: 86400,
	}
	service.Actions["restore-drill"] = model.ActionDefinition{
		Name: "restore-drill", Enabled: true, Risk: model.RiskMedium, TargetMode: "none",
		Steps: []string{"preflight", "drill", "verify"}, TimeoutSeconds: 60,
		ConfirmationTemplate: "演练恢复 {service}", Impact: "isolated", Rollback: "cleanup", Scope: "demo drill",
	}
	service.Actions["restore"] = model.ActionDefinition{
		Name: "restore", Enabled: true, Risk: model.RiskHigh, TargetMode: "none",
		Steps: []string{"preflight", "quiesce", "restore", "verify", "resume"}, TimeoutSeconds: 60,
		ConfirmationTemplate: "恢复 {service} 使用恢复点 {target}", Impact: "production", Rollback: "manual", Scope: "demo data",
	}
	engine.catalog.Services["demo"] = service
	return service
}

func createVerifiedRecoveryPoint(t *testing.T, engine *Engine, database *store.Store, service model.ServiceDefinition) model.RecoveryPoint {
	t.Helper()
	ctx, now := context.Background(), time.Now().UTC()
	artifactPath := filepath.Join(engine.backupRoot, "postgres", "demo.sql.gz")
	if err := os.MkdirAll(filepath.Dir(artifactPath), 0o700); err != nil {
		t.Fatal(err)
	}
	content := []byte("verified-backup")
	if err := os.WriteFile(artifactPath, content, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	preview := model.Preview{
		ID: "preview-recovery-center", ActorHash: actorHash(), Service: service.Name, Action: "update",
		Risk: model.RiskHigh, Steps: []string{"backup"}, Snapshot: map[string]any{"currentVersion": "1.0.0"},
		CreatedAt: now, ExpiresAt: now.Add(time.Minute),
	}
	if err := database.CreatePreview(ctx, store.PreviewInput{Preview: preview, ConfirmationHash: store.HashConfirmation("backup")}); err != nil {
		t.Fatal(err)
	}
	task, _, err := database.StartTask(ctx, actorHash(), model.StartTaskRequest{
		PreviewID: preview.ID, Confirmation: "backup", IdempotencyKey: mustUUID(t),
	}, "task-recovery-center")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.MarkRunningOwned(ctx, task.ID, "backup", engine.owner); err != nil {
		t.Fatal(err)
	}
	point, err := engine.persistRecoveryPoint(ctx, task, service, &model.RecoveryPointEvidence{
		SchemaVersion: 1, Service: service.Name, TaskID: task.ID, CreatedAt: now,
		Artifacts: []model.RecoveryArtifact{{Role: "postgres-demo", Path: artifactPath,
			SizeBytes: int64(len(content)), SHA256: "sha256:" + hex.EncodeToString(digest[:])}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.FinishTask(ctx, task.ID, model.TaskSucceeded, "backup complete", ""); err != nil {
		t.Fatal(err)
	}
	return point
}

func TestProductionRestoreRequiresFreshDrillAndIndependentDualApproval(t *testing.T) {
	ctx := context.Background()
	engine, database := testEngine(t, &fakeExecutor{})
	engine.backupRoot = t.TempDir()
	service := enableRecoveryActions(engine)
	point := createVerifiedRecoveryPoint(t, engine, database, service)
	creator, firstApprover, secondApprover := actorHash(), strings.Repeat("b", 64), strings.Repeat("c", 64)

	productionRequest := model.RestoreRequest{
		Service: service.Name, RecoveryPointID: point.ID, Mode: "production",
		Confirmation: "创建生产恢复计划 demo", IdempotencyKey: mustUUID(t),
	}
	if _, err := engine.CreateRestorePlan(ctx, creator, productionRequest); err == nil || !strings.Contains(err.Error(), "隔离恢复演练") {
		t.Fatalf("production restore without drill err=%v", err)
	}

	drill, err := engine.CreateRestorePlan(ctx, creator, model.RestoreRequest{
		Service: service.Name, RecoveryPointID: point.ID, Mode: "isolated",
		Confirmation: "创建隔离恢复演练计划 demo", IdempotencyKey: mustUUID(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	drill, err = engine.ApproveReleasePlan(ctx, creator, drill.ID, model.ApprovePlanRequest{
		Digest: drill.Digest, Confirmation: drill.ConfirmationPhrase,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := engine.ExecuteReleasePlan(ctx, creator, drill.ID, model.ExecutePlanRequest{IdempotencyKey: mustUUID(t)}); err != nil {
		t.Fatal(err)
	}
	engine.Wait()

	plan, err := engine.CreateRestorePlan(ctx, creator, productionRequest)
	if err != nil || !plan.RequiresDualApproval {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
	if _, _, err := engine.ExecuteReleasePlan(ctx, creator, plan.ID, model.ExecutePlanRequest{IdempotencyKey: mustUUID(t)}); err == nil {
		t.Fatal("unapproved production restore executed")
	}
	if _, err := engine.ApproveReleasePlan(ctx, creator, plan.ID, model.ApprovePlanRequest{
		Digest: plan.Digest, Confirmation: plan.ConfirmationPhrase,
	}); !errors.Is(err, store.ErrActorMismatch) {
		t.Fatalf("creator approval err=%v, want actor mismatch", err)
	}
	plan, err = engine.ApproveReleasePlan(ctx, firstApprover, plan.ID, model.ApprovePlanRequest{
		Digest: plan.Digest, Confirmation: plan.ConfirmationPhrase,
	})
	if err != nil || plan.State != model.PlanPendingApproval {
		t.Fatalf("first approval plan=%+v err=%v", plan, err)
	}
	if _, _, err := engine.ExecuteReleasePlan(ctx, creator, plan.ID, model.ExecutePlanRequest{IdempotencyKey: mustUUID(t)}); err == nil {
		t.Fatal("production restore with one approval executed")
	}
	plan, err = engine.ApproveReleasePlan(ctx, secondApprover, plan.ID, model.ApprovePlanRequest{
		Digest: plan.Digest, Confirmation: plan.ConfirmationPhrase,
	})
	if err != nil || plan.State != model.PlanApproved || plan.ApprovedByHash == plan.SecondApprovedByHash {
		t.Fatalf("second approval plan=%+v err=%v", plan, err)
	}
	if _, _, err := engine.ExecuteReleasePlan(ctx, releasePlanExecutor(plan), plan.ID, model.ExecutePlanRequest{IdempotencyKey: mustUUID(t)}); err != nil {
		t.Fatal(err)
	}
	engine.Wait()
}
