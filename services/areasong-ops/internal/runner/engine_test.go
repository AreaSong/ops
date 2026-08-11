package runner

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
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

type fakeAlertmanager struct {
	mu       sync.Mutex
	alerts   []model.ActiveAlert
	created  []model.MaintenanceSilence
	expired  []string
	listErr  error
	writeErr error
}

func (manager *fakeAlertmanager) ListAlerts(_ context.Context, _ bool) ([]model.ActiveAlert, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	return append([]model.ActiveAlert(nil), manager.alerts...), manager.listErr
}

func (manager *fakeAlertmanager) CreateSilence(
	_ context.Context,
	_ map[string]string,
	_ []string,
	_, endsAt time.Time,
	_ string,
) (model.MaintenanceSilence, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.writeErr != nil {
		return model.MaintenanceSilence{}, manager.writeErr
	}
	silence := model.MaintenanceSilence{ID: "test-silence-" + mustTestID(len(manager.created)+1), EndsAt: endsAt}
	manager.created = append(manager.created, silence)
	return silence, nil
}

func (manager *fakeAlertmanager) ExpireSilence(_ context.Context, id string) error {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.writeErr != nil {
		return manager.writeErr
	}
	manager.expired = append(manager.expired, id)
	return nil
}

func mustTestID(value int) string { return fmt.Sprintf("%d", value) }

func (executor *fakeExecutor) Execute(_ context.Context, input ExecuteInput) (model.AdapterResult, error) {
	executor.mu.Lock()
	executor.calls = append(executor.calls, input)
	executor.mu.Unlock()
	if input.Phase == executor.failPhase {
		return model.AdapterResult{}, errors.New("适配器阶段 health 失败: password=secret failure")
	}
	if input.Action == "inspect" {
		return model.AdapterResult{OK: true, Summary: "checked", Data: map[string]any{
			"currentVersion": "1.0.0", "currentImage": "demo:v1.0.0@sha256:test",
			"currentImageId": "sha256:image", "runtimeIdentityHash": "sha256:runtime",
		}}, nil
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
	catalog := &config.Catalog{SchemaVersion: 3, Services: map[string]model.ServiceDefinition{
		"demo": {
			Name: "demo", ObjectID: "service:demo", DisplayName: "Demo", Description: "test", Template: "custom", Adapter: "/tmp/demo",
			AlertPolicy: model.AlertPolicyDefinition{
				Matchers:          map[string]string{"service": "demo"},
				BlockingAlerts:    []string{"AppHttpProbeFailed"},
				MaintenanceAlerts: []string{"AppHttpProbeFailed"},
			},
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
					ObservationSeconds: 1, TimeoutSeconds: 60, ConfirmationTemplate: "更新 {service} 到 {target}",
					Impact: "update", Rollback: "rollback", Scope: "demo",
				},
				"rollback": {
					Name: "rollback", DisplayName: "回滚", Enabled: true, Risk: model.RiskHigh,
					TargetMode: "controlled_rollback", Steps: []string{"preflight", "apply", "health", "identity"},
					ObservationSeconds: 1, TimeoutSeconds: 60, ConfirmationTemplate: "回滚 {service} 使用任务 {target}",
					Impact: "rollback", Rollback: "manual", Scope: "demo",
				},
				"restart": {
					Name: "restart", DisplayName: "重启", Enabled: true, Risk: model.RiskMedium,
					TargetMode: "none", Steps: []string{"preflight", "restart", "health"},
					ObservationSeconds: 1, TimeoutSeconds: 60, ConfirmationTemplate: "重启 {service}",
					Impact: "restart", Rollback: "restart", Scope: "demo",
				},
			},
		},
	}}
	return NewEngine(catalog, database, executor, stateRoot,
		WithAlertmanager(&fakeAlertmanager{})), database
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

func TestPlanClosureRejectsChangedRuntimeIdentity(t *testing.T) {
	ctx := context.Background()
	engine, database := testEngine(t, &fakeExecutor{})
	service := engine.catalog.Services["demo"]
	action := service.Actions["update"]
	action.ObservationSeconds = 1
	service.Actions["update"] = action
	engine.catalog.Services["demo"] = service
	discoverRelease(t, engine)
	plan, err := engine.CreateReleasePlan(ctx, actorHash(), model.PreviewRequest{
		Service: "demo", Action: "update", Target: "v1.1.0",
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
	observing, err := database.GetReleasePlan(ctx, plan.ID)
	if err != nil || observing.State != model.PlanObserving || task.ID != observing.TaskID {
		t.Fatalf("plan=%+v err=%v", observing, err)
	}
	time.Sleep(time.Until(*observing.ObservationEndsAt) + 20*time.Millisecond)
	payload, err := json.Marshal(model.ClosePlanRequest{IdempotencyKey: mustUUID(t)})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/plans/"+plan.ID+"/close", bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(actorHeader, actorHash())
	response := httptest.NewRecorder()
	NewServer(engine, database).ServeHTTP(response, request)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "当前版本与计划目标不一致") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	blocked, err := database.GetReleasePlan(ctx, plan.ID)
	if err != nil || blocked.State != model.PlanObserving || blocked.ClosureReason == "" {
		t.Fatalf("blocked=%+v err=%v", blocked, err)
	}
}

func TestPlanExecutionRejectsActiveBlockingAlert(t *testing.T) {
	ctx := context.Background()
	engine, _ := testEngine(t, &fakeExecutor{})
	manager := engine.alertmanager.(*fakeAlertmanager)
	manager.alerts = []model.ActiveAlert{{
		Fingerprint: "abcdef1234567890", AlertName: "AppHttpProbeFailed",
		Severity: "critical", Labels: map[string]string{
			"alertname": "AppHttpProbeFailed", "service": "demo", "severity": "critical",
		},
	}}
	plan, err := engine.CreateReleasePlan(ctx, actorHash(), model.PreviewRequest{
		Service: "demo", Action: "restart",
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
	_, _, err = engine.ExecuteReleasePlan(ctx, actorHash(), approved.ID, model.ExecutePlanRequest{
		IdempotencyKey: mustUUID(t),
	})
	if err == nil || !strings.Contains(err.Error(), "存在阻断告警") || len(manager.created) != 0 {
		t.Fatalf("err=%v silences=%+v", err, manager.created)
	}
}

func TestActiveAlertsOnlyProjectsGitMappedObjects(t *testing.T) {
	engine, _ := testEngine(t, &fakeExecutor{})
	manager := engine.alertmanager.(*fakeAlertmanager)
	manager.alerts = []model.ActiveAlert{
		{Fingerprint: "abcdef1234567890", AlertName: "AppHttpProbeFailed", Labels: map[string]string{
			"alertname": "AppHttpProbeFailed", "service": "demo", "severity": "critical",
		}},
		{Fingerprint: "1234567890abcdef", AlertName: "BackupJobFailed", Labels: map[string]string{
			"alertname": "BackupJobFailed", "service": "demo", "severity": "warning",
		}},
	}
	alerts, err := engine.ActiveAlerts(context.Background())
	if err != nil || len(alerts) != 1 || alerts[0].ObjectID != "service:demo" || alerts[0].Service != "demo" {
		t.Fatalf("alerts=%+v err=%v", alerts, err)
	}
}

func TestAlertsEndpointReportsAlertmanagerUnavailable(t *testing.T) {
	engine, database := testEngine(t, &fakeExecutor{})
	manager := engine.alertmanager.(*fakeAlertmanager)
	manager.listErr = errors.New("connection refused")
	request := httptest.NewRequest(http.MethodGet, "/v1/alerts", nil)
	request.Header.Set(actorHeader, actorHash())
	response := httptest.NewRecorder()
	NewServer(engine, database).ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "活动告警当前不可用") {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestPlanClosureReleasesSilenceAndChecksAlerts(t *testing.T) {
	ctx := context.Background()
	engine, database := testEngine(t, &fakeExecutor{})
	manager := engine.alertmanager.(*fakeAlertmanager)
	plan, err := engine.CreateReleasePlan(ctx, actorHash(), model.PreviewRequest{
		Service: "demo", Action: "restart",
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
	_, _, err = engine.ExecuteReleasePlan(ctx, actorHash(), approved.ID, model.ExecutePlanRequest{
		IdempotencyKey: mustUUID(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	engine.Wait()
	observing, err := database.GetReleasePlan(ctx, plan.ID)
	if err != nil || observing.MaintenanceSilenceID == "" || observing.MaintenanceSilenceEndsAt == nil {
		t.Fatalf("plan=%+v err=%v", observing, err)
	}
	manager.alerts = []model.ActiveAlert{{
		Fingerprint: "fedcba0987654321", AlertName: "AppHttpProbeFailed",
		Labels: map[string]string{"alertname": "AppHttpProbeFailed", "service": "demo"},
	}}
	time.Sleep(time.Until(*observing.ObservationEndsAt) + 20*time.Millisecond)
	_, err = engine.CloseReleasePlan(ctx, actorHash(), plan.ID, model.ClosePlanRequest{
		IdempotencyKey: mustUUID(t),
	})
	if err == nil || !strings.Contains(err.Error(), "关联阻断告警仍在触发") {
		t.Fatalf("err=%v", err)
	}
	blocked, err := database.GetReleasePlan(ctx, plan.ID)
	if err != nil || blocked.MaintenanceSilenceReleasedAt == nil ||
		len(blocked.BlockingAlertFingerprints) != 1 || len(manager.expired) != 1 {
		t.Fatalf("plan=%+v expired=%v err=%v", blocked, manager.expired, err)
	}
	manager.alerts = nil
	closed, err := engine.CloseReleasePlan(ctx, actorHash(), plan.ID, model.ClosePlanRequest{
		IdempotencyKey: mustUUID(t),
	})
	if err != nil || closed.State != model.PlanCompleted {
		t.Fatalf("plan=%+v err=%v", closed, err)
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
