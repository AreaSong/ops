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
	task, _, err := seedRunnerPreviewTask(t, engine, ctx, actorHash(), model.StartTaskRequest{
		PreviewID: preview.ID, Confirmation: "backup", IdempotencyKey: mustUUID(t),
	}, "task-recovery-center")
	if err != nil {
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

func TestBPRestoreRetainsDrillAndIdentityGates(t *testing.T) {
	ctx := context.Background()
	engine, database := testEngine(t, &fakeExecutor{})
	engine.backupRoot = t.TempDir()
	service := enableRecoveryActions(engine)
	point := createVerifiedRecoveryPoint(t, engine, database, service)
	creator, approver := actorHash(), strings.Repeat("b", 64)

	productionRequest := model.RestoreRequest{
		Service: service.Name, RecoveryPointID: point.ID, Mode: "production",
		Confirmation: "创建生产恢复计划 demo", IdempotencyKey: mustUUID(t),
	}
	if _, err := engine.CreateRestorePlan(ctx, creator, productionRequest); err == nil || !strings.Contains(err.Error(), "隔离恢复演练") {
		t.Fatalf("production restore without drill err=%v", err)
	}

	if _, err := engine.CreateRestorePlan(ctx, creator, model.RestoreRequest{Service: service.Name, RecoveryPointID: point.ID, Mode: "isolated", Confirmation: "创建隔离恢复演练计划 demo", IdempotencyKey: mustUUID(t)}); !errors.Is(err, model.ErrReleaseNotIntegrated) {
		t.Fatal("隔离恢复绕过B-P", err)
	}
	// 原恢复审批身份语义独立保留，关闭普通链不降低双人边界。
	historical := model.ReleasePlan{Risk: model.RiskHigh, ActorHash: creator, ApprovedByHash: approver, RequiresDualApproval: true, ApprovalPolicy: model.ApprovalPolicyTwoParty}
	if !historical.AllowsExecutor(creator) || historical.AllowsExecutor(approver) {
		t.Fatal("恢复执行身份规则退化")
	}
	saved, err := database.GetRecoveryPoint(ctx, point.ID)
	if err != nil || saved.EvidenceDigest != point.EvidenceDigest {
		t.Fatal("拒绝改写恢复点", err)
	}
	if len(engine.executor.(*fakeExecutor).calls) != 0 {
		t.Fatal("拒绝后调用恢复适配器")
	}
}
