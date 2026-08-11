package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/config"
	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	"github.com/AreaSong/ops/services/areasong-ops/internal/store"
)

type fakeExecutor struct {
	mu        sync.Mutex
	calls     []ExecuteInput
	failPhase string
}

func (executor *fakeExecutor) Execute(_ context.Context, input ExecuteInput) (model.AdapterResult, error) {
	executor.mu.Lock()
	executor.calls = append(executor.calls, input)
	executor.mu.Unlock()
	if input.Phase == executor.failPhase {
		return model.AdapterResult{}, errors.New("适配器阶段 health 失败: password=secret failure")
	}
	if input.Action == "inspect" {
		return model.AdapterResult{OK: true, Summary: "checked", Data: map[string]any{"currentVersion": "1.0.0"}}, nil
	}
	if input.Action == "check" && input.Phase == "discover" {
		return model.AdapterResult{OK: true, Summary: "discovered", Data: map[string]any{
			"currentVersion": "1.0.0", "latestTag": "v1.1.0", "prepared": true,
		}}, nil
	}
	return model.AdapterResult{OK: true, Summary: input.Phase + " ok", Data: map[string]any{"phase": input.Phase}}, nil
}

func testEngine(t *testing.T, executor *fakeExecutor) (*Engine, *store.Store) {
	t.Helper()
	stateRoot := t.TempDir()
	database, err := store.Open(filepath.Join(stateRoot, "ops.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	catalog := &config.Catalog{SchemaVersion: 2, Services: map[string]model.ServiceDefinition{
		"demo": {
			Name: "demo", DisplayName: "Demo", Description: "test", Template: "custom", Adapter: "/tmp/demo",
			Actions: map[string]model.ActionDefinition{
				"inspect": {
					Name: "inspect", DisplayName: "检查", Enabled: true, Risk: model.RiskReadOnly,
					TargetMode: "none", Steps: []string{"inspect"}, TimeoutSeconds: 30,
					Impact: "none", Rollback: "none", Scope: "demo",
				},
				"check": {
					Name: "check", DisplayName: "检查更新", Enabled: true, Risk: model.RiskReadOnly,
					TargetMode: "none", Steps: []string{"discover"}, TimeoutSeconds: 30,
					Impact: "none", Rollback: "none", Scope: "demo",
				},
				"update": {
					Name: "update", DisplayName: "更新", Enabled: true, Risk: model.RiskHigh,
					TargetMode: "signed_release_tag", Steps: []string{"preflight", "backup", "apply", "health", "identity"},
					TimeoutSeconds: 60, ConfirmationTemplate: "更新 {service} 到 {target}",
					Impact: "update", Rollback: "rollback", Scope: "demo",
				},
				"rollback": {
					Name: "rollback", DisplayName: "回滚", Enabled: true, Risk: model.RiskHigh,
					TargetMode: "controlled_rollback", Steps: []string{"preflight", "apply", "health", "identity"},
					TimeoutSeconds: 60, ConfirmationTemplate: "回滚 {service} 使用任务 {target}",
					Impact: "rollback", Rollback: "manual", Scope: "demo",
				},
			},
		},
	}}
	return NewEngine(catalog, database, executor, stateRoot), database
}

func TestHighRiskTaskRequiresExactPhraseAndCompletes(t *testing.T) {
	ctx := context.Background()
	engine, database := testEngine(t, &fakeExecutor{})
	preview, err := engine.CreatePreview(ctx, actorHash(), model.PreviewRequest{
		Service: "demo", Action: "update", Target: "v1.1.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	if preview.ConfirmationPhrase != "更新 demo 到 v1.1.0" {
		t.Fatalf("phrase=%q", preview.ConfirmationPhrase)
	}
	idempotency, _ := newUUID()
	_, _, err = engine.StartTask(ctx, actorHash(), model.StartTaskRequest{
		PreviewID: preview.ID, IdempotencyKey: idempotency, Confirmation: "wrong",
	})
	if !errors.Is(err, store.ErrConfirmation) {
		t.Fatalf("expected confirmation error, got %v", err)
	}
	idempotency, _ = newUUID()
	task, created, err := engine.StartTask(ctx, actorHash(), model.StartTaskRequest{
		PreviewID: preview.ID, IdempotencyKey: idempotency, Confirmation: preview.ConfirmationPhrase,
	})
	if err != nil || !created {
		t.Fatalf("created=%v err=%v", created, err)
	}
	engine.Wait()
	finished, err := database.GetTask(ctx, task.ID)
	if err != nil || finished.State != model.TaskSucceeded {
		t.Fatalf("task=%+v err=%v", finished, err)
	}
}

func TestReleasePlanApprovalAndExecutionAreSeparate(t *testing.T) {
	ctx := context.Background()
	engine, database := testEngine(t, &fakeExecutor{})
	discoverRelease(t, engine)
	plan, err := engine.CreateReleasePlan(ctx, actorHash(), model.PreviewRequest{
		Service: "demo", Action: "update", Target: "v1.1.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.State != model.PlanPendingApproval || plan.Digest == "" {
		t.Fatalf("plan=%+v", plan)
	}
	if _, _, err := engine.ExecuteReleasePlan(ctx, actorHash(), plan.ID, model.ExecutePlanRequest{
		IdempotencyKey: mustUUID(t),
	}); err == nil {
		t.Fatal("unapproved plan executed")
	}
	approved, err := engine.ApproveReleasePlan(ctx, actorHash(), plan.ID, model.ApprovePlanRequest{
		Confirmation: plan.ConfirmationPhrase, Digest: plan.Digest,
	})
	if err != nil || approved.State != model.PlanApproved {
		t.Fatalf("approved=%+v err=%v", approved, err)
	}
	executionKey := mustUUID(t)
	task, created, err := engine.ExecuteReleasePlan(ctx, actorHash(), plan.ID, model.ExecutePlanRequest{
		IdempotencyKey: executionKey,
	})
	if err != nil || !created || task.PlanID != plan.ID {
		t.Fatalf("task=%+v created=%v err=%v", task, created, err)
	}
	engine.Wait()
	finished, err := database.GetTask(ctx, task.ID)
	if err != nil || finished.State != model.TaskSucceeded || len(finished.Stages) != 5 {
		t.Fatalf("task=%+v err=%v", finished, err)
	}
	replayed, created, err := engine.ExecuteReleasePlan(ctx, actorHash(), plan.ID, model.ExecutePlanRequest{
		IdempotencyKey: executionKey,
	})
	if err != nil || created || replayed.ID != task.ID {
		t.Fatalf("replayed=%+v created=%v err=%v", replayed, created, err)
	}
}

func discoverRelease(t *testing.T, engine *Engine) {
	t.Helper()
	preview, err := engine.CreatePreview(context.Background(), actorHash(), model.PreviewRequest{
		Service: "demo", Action: "check",
	})
	if err != nil {
		t.Fatal(err)
	}
	task, _, err := engine.StartTask(context.Background(), actorHash(), model.StartTaskRequest{
		PreviewID: preview.ID, IdempotencyKey: mustUUID(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	engine.Wait()
	if task.ID == "" {
		t.Fatal("discovery task was not created")
	}
}

func TestRecoveryPointEvidenceIsVerifiedAndBound(t *testing.T) {
	ctx := context.Background()
	engine, database := testEngine(t, &fakeExecutor{})
	backupRoot := t.TempDir()
	engine.backupRoot = backupRoot
	artifactPath := filepath.Join(backupRoot, "postgres", "demo.sql.gz")
	if err := os.MkdirAll(filepath.Dir(artifactPath), 0o700); err != nil {
		t.Fatal(err)
	}
	content := []byte("verified-backup")
	if err := os.WriteFile(artifactPath, content, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)

	now := time.Now().UTC()
	preview := model.Preview{
		ID: "preview-recovery", ActorHash: actorHash(), Service: "demo", Action: "update",
		Target: "v1.1.0", Risk: model.RiskHigh, Steps: []string{"backup", "apply"},
		Snapshot: map[string]any{"currentVersion": "1.0.0"}, CreatedAt: now,
		ExpiresAt: now.Add(5 * time.Minute),
	}
	if err := database.CreatePreview(ctx, store.PreviewInput{
		Preview: preview, ConfirmationHash: store.HashConfirmation("confirm"),
	}); err != nil {
		t.Fatal(err)
	}
	task, _, err := database.StartTask(ctx, actorHash(), model.StartTaskRequest{
		PreviewID: preview.ID, Confirmation: "confirm", IdempotencyKey: "recovery-idem",
	}, "task-recovery")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.MarkRunningOwned(ctx, task.ID, "backup", engine.owner); err != nil {
		t.Fatal(err)
	}
	point, err := engine.persistRecoveryPoint(ctx, task, &model.RecoveryPointEvidence{
		SchemaVersion: 1, Service: task.Service, TaskID: task.ID, CreatedAt: now,
		Artifacts: []model.RecoveryArtifact{{
			Role: "postgres-demo", Path: artifactPath, SizeBytes: int64(len(content)),
			SHA256: "sha256:" + hex.EncodeToString(digest[:]),
		}},
	})
	if err != nil || point.Status != "verified" {
		t.Fatalf("point=%+v err=%v", point, err)
	}
	stored, err := database.GetTask(ctx, task.ID)
	if err != nil || stored.RecoveryPointID != point.ID {
		t.Fatalf("task=%+v err=%v", stored, err)
	}
}

func TestControlledRollbackPlanRevalidatesCurrentSource(t *testing.T) {
	ctx := context.Background()
	engine, database := testEngine(t, &fakeExecutor{})
	preview, err := engine.CreatePreview(ctx, actorHash(), model.PreviewRequest{
		Service: "demo", Action: "update", Target: "v1.0.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	source, _, err := engine.StartTask(ctx, actorHash(), model.StartTaskRequest{
		PreviewID: preview.ID, Confirmation: preview.ConfirmationPhrase, IdempotencyKey: mustUUID(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	engine.Wait()
	plan, err := engine.CreateReleasePlan(ctx, actorHash(), model.PreviewRequest{
		Service: "demo", Action: "rollback", Target: source.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	approved, err := engine.ApproveReleasePlan(ctx, actorHash(), plan.ID, model.ApprovePlanRequest{
		Confirmation: plan.ConfirmationPhrase, Digest: plan.Digest,
	})
	if err != nil {
		t.Fatal(err)
	}
	task, _, err := engine.ExecuteReleasePlan(ctx, actorHash(), approved.ID, model.ExecutePlanRequest{
		IdempotencyKey: mustUUID(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	engine.Wait()
	finished, err := database.GetTask(ctx, task.ID)
	if err != nil || finished.State != model.TaskSucceeded {
		t.Fatalf("task=%+v err=%v", finished, err)
	}
}

func TestPostApplyFailureRunsRollbackAndRedactsError(t *testing.T) {
	ctx := context.Background()
	executor := &fakeExecutor{failPhase: "health"}
	engine, database := testEngine(t, executor)
	preview, err := engine.CreatePreview(ctx, actorHash(), model.PreviewRequest{
		Service: "demo", Action: "update", Target: "v1.1.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	idempotency, _ := newUUID()
	task, _, err := engine.StartTask(ctx, actorHash(), model.StartTaskRequest{
		PreviewID: preview.ID, IdempotencyKey: idempotency, Confirmation: preview.ConfirmationPhrase,
	})
	if err != nil {
		t.Fatal(err)
	}
	engine.Wait()
	finished, err := database.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if finished.State != model.TaskRolledBack {
		t.Fatalf("state=%s error=%s", finished.State, finished.Error)
	}
	if finished.Error != "适配器阶段 health 失败: password=[REDACTED] failure" {
		t.Fatalf("error was not redacted: %q", finished.Error)
	}
	if len(finished.Stages) != 6 || finished.Stages[3].State != model.StageFailed ||
		finished.Stages[5].Name != "rollback" || finished.Stages[5].State != model.StageRolledBack {
		t.Fatalf("rollback stages=%+v", finished.Stages)
	}
	executor.mu.Lock()
	defer executor.mu.Unlock()
	if executor.calls[len(executor.calls)-1].Phase != "rollback" {
		t.Fatalf("last phase=%s", executor.calls[len(executor.calls)-1].Phase)
	}
}

func TestServicesRestoresDiscoveryAndSafeRollbackSource(t *testing.T) {
	ctx := context.Background()
	engine, database := testEngine(t, &fakeExecutor{})
	now := time.Now().UTC()
	preview := model.Preview{
		ID: "preview-check", ActorHash: actorHash(), Service: "demo", Action: "check",
		Risk: model.RiskReadOnly, Impact: "none", Rollback: "none", Scope: "demo",
		Steps: []string{"discover"}, Snapshot: map[string]any{}, CreatedAt: now,
		ExpiresAt: now.Add(5 * time.Minute),
	}
	if err := database.CreatePreview(ctx, store.PreviewInput{
		Preview: preview, ConfirmationHash: store.HashConfirmation(""),
	}); err != nil {
		t.Fatal(err)
	}
	check, _, err := database.StartTask(ctx, actorHash(), model.StartTaskRequest{
		PreviewID: preview.ID, IdempotencyKey: "check-idempotency",
	}, "task-check")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.MarkRunning(ctx, check.ID, "discover"); err != nil {
		t.Fatal(err)
	}
	if err := database.FinishTask(ctx, check.ID, model.TaskSucceeded, "完成", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := database.AppendEvent(ctx, model.Event{
		TaskID: check.ID, Level: "info", Phase: "discover", Message: "完成",
		Data: map[string]any{"latestTag": "v1.1.0", "prepared": true},
	}); err != nil {
		t.Fatal(err)
	}

	updatePreview, err := engine.CreatePreview(ctx, actorHash(), model.PreviewRequest{
		Service: "demo", Action: "update", Target: "v1.0.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	idempotency, _ := newUUID()
	updated, _, err := engine.StartTask(ctx, actorHash(), model.StartTaskRequest{
		PreviewID: updatePreview.ID, IdempotencyKey: idempotency,
		Confirmation: updatePreview.ConfirmationPhrase,
	})
	if err != nil {
		t.Fatal(err)
	}
	engine.Wait()

	views := engine.Services(ctx)
	if len(views) != 1 || views[0].ReleaseDiscovery["latestTag"] != "v1.1.0" {
		t.Fatalf("views=%+v", views)
	}
	if views[0].RollbackSourceTaskID != updated.ID {
		t.Fatalf("rollback source=%q want=%q", views[0].RollbackSourceTaskID, updated.ID)
	}
}

func actorHash() string {
	return strings.Repeat("a", 64)
}

func mustUUID(t *testing.T) string {
	t.Helper()
	id, err := newUUID()
	if err != nil {
		t.Fatal(err)
	}
	return id
}
