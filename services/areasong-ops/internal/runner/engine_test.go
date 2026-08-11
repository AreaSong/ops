package runner

import (
	"context"
	"errors"
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
				"update": {
					Name: "update", DisplayName: "更新", Enabled: true, Risk: model.RiskHigh,
					TargetMode: "signed_release_tag", Steps: []string{"preflight", "backup", "apply", "health", "identity"},
					TimeoutSeconds: 60, ConfirmationTemplate: "更新 {service} 到 {target}",
					Impact: "update", Rollback: "rollback", Scope: "demo",
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
