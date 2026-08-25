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

type fakeCredentialRotator struct {
	current     CurrentCredential
	rotateHook  func()
	verifyCalls int
	removeCalls int
}

func (rotator *fakeCredentialRotator) Current(context.Context) (CurrentCredential, error) {
	return rotator.current, nil
}

func (rotator *fakeCredentialRotator) Rotate(
	_ context.Context,
	_ string,
	secret string,
	expiresAt string,
) (model.CredentialRotationResult, error) {
	if rotator.rotateHook != nil {
		rotator.rotateHook()
	}
	rotator.current = CurrentCredential{
		Configured: true, Fingerprint: credentialFingerprint(secret), ExpiresAt: expiresAt,
	}
	return model.CredentialRotationResult{
		State:            model.CredentialRotationSwitchedPendingRevocation,
		ValidationResult: "验证通过", Outcome: "已切换", RollbackResult: "已保留",
	}, nil
}

func (rotator *fakeCredentialRotator) VerifyRevoked(context.Context, model.CredentialRotation) error {
	rotator.verifyCalls++
	return nil
}
func (rotator *fakeCredentialRotator) RemoveRollback(context.Context, model.CredentialRotation) error {
	rotator.removeCalls++
	return nil
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
	}, AutomaticTasks: map[string]model.ServiceDefinition{
		"collector": {
			Name: "collector", ObjectID: "automatic-task:collector", DisplayName: "Collector",
			Description: "test collector", Template: "automatic-task-v1", Adapter: "/tmp/collector",
			Metadata: model.ObjectMetadata{Type: "automatic_task", Environment: "production", Owner: "operations",
				Criticality: "important", Lifecycle: "active", Maturity: "manual_approval"},
			AutomaticTask: &model.AutomaticTaskRuntime{Schedule: "每分钟", ScheduleSource: "cron", FreshnessSeconds: 180},
			Actions: map[string]model.ActionDefinition{
				"inspect": {Name: "inspect", DisplayName: "检查", Enabled: true, Risk: model.RiskReadOnly,
					TargetMode: "none", Steps: []string{"inspect"}, TimeoutSeconds: 30,
					Impact: "none", Rollback: "none", Scope: "collector"},
				"rerun": {Name: "rerun", DisplayName: "补跑", Enabled: true, Risk: model.RiskLow,
					TargetMode: "none", Steps: []string{"preflight", "run", "verify"}, TimeoutSeconds: 60,
					ConfirmationTemplate: "补跑 {service}", Impact: "refresh metrics", Rollback: "keep old", Scope: "collector"},
			},
		},
	}}
	engine := NewEngine(catalog, database, executor, stateRoot, WithAlertmanager(&fakeAlertmanager{}))
	return engine, database
}

func TestAutomaticTaskUsesManagedObjectPlanAndExecution(t *testing.T) {
	ctx := context.Background()
	engine, database := testEngine(t, &fakeExecutor{})
	views := engine.AutomaticTasks(ctx)
	if len(views) != 1 || views[0].ObjectID != "automatic-task:collector" || views[0].Schedule != "每分钟" {
		t.Fatalf("views=%+v", views)
	}
	plan, err := engine.CreateReleasePlan(ctx, actorHash(), model.PreviewRequest{Service: "collector", Action: "rerun"})
	if err != nil || plan.ConfirmationPhrase != "补跑 collector" {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
	approved, err := engine.ApproveReleasePlan(ctx, actorHash(), plan.ID, model.ApprovePlanRequest{
		Confirmation: plan.ConfirmationPhrase, Digest: plan.Digest,
	})
	if err != nil {
		t.Fatal(err)
	}
	task, _, err := engine.ExecuteReleasePlan(ctx, actorHash(), approved.ID, model.ExecutePlanRequest{IdempotencyKey: mustUUID(t)})
	if err != nil {
		t.Fatal(err)
	}
	engine.Wait()
	finished, err := database.GetTask(ctx, task.ID)
	if err != nil || finished.State != model.TaskSucceeded || finished.ProductionChanged || len(finished.Stages) != 3 {
		t.Fatalf("task=%+v err=%v", finished, err)
	}
}

func TestManagedObjectEndpointsRequireActor(t *testing.T) {
	engine, database := testEngine(t, &fakeExecutor{})
	handler := NewServer(engine, database)
	for _, test := range []struct {
		path     string
		expected []string
	}{
		{path: "/v1/automatic-tasks", expected: []string{"automatic-task:collector"}},
		{path: "/v1/objects", expected: []string{"service:demo", "automatic-task:collector"}},
	} {
		request := httptest.NewRequest(http.MethodGet, test.path, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("path=%s status=%d", test.path, response.Code)
		}
		request = httptest.NewRequest(http.MethodGet, test.path, nil)
		request.Header.Set(actorHeader, actorHash())
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("path=%s status=%d body=%s", test.path, response.Code, response.Body.String())
		}
		for _, expected := range test.expected {
			if !strings.Contains(response.Body.String(), expected) {
				t.Fatalf("path=%s missing=%s body=%s", test.path, expected, response.Body.String())
			}
		}
	}
}

func TestCredentialRotationAPILeaksNoSecretToResponseSQLiteOrAudit(t *testing.T) {
	ctx := context.Background()
	engine, database := testEngine(t, &fakeExecutor{})
	engine.credentials = &fakeCredentialRotator{current: CurrentCredential{
		Configured: true, Fingerprint: "sha256:oldcredential", ExpiresAt: "2026-12-31",
	}}
	token := "github_pat_endpoint_test_secret_12345678901234567890"
	payload, err := json.Marshal(model.CredentialRotationRequest{
		CredentialType: model.GitHubAlertmanagerCredential, Secret: token,
		ExpiresAt: "2027-08-12", Confirmation: credentialConfirmation,
		IdempotencyKey: mustUUID(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost,
		"/v1/credentials/github-alertmanager/rotate", bytes.NewReader(payload))
	request.Header.Set(actorHeader, actorHash())
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	NewServer(engine, database).ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), token) {
		t.Fatal("credential API response exposed the token")
	}
	data, err := os.ReadFile(filepath.Join(engine.stateRoot, "ops.db"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), token) {
		t.Fatal("SQLite contained the token")
	}
	audit, err := database.ListAudit(ctx, 20, 0)
	if err != nil {
		t.Fatal(err)
	}
	encodedAudit, err := json.Marshal(audit)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encodedAudit), token) {
		t.Fatal("audit contained the token")
	}
}

func TestCredentialRotationPersistsTerminalStateAfterRequestCancellation(t *testing.T) {
	engine, database := testEngine(t, &fakeExecutor{})
	requestContext, cancel := context.WithCancel(context.Background())
	engine.credentials = &fakeCredentialRotator{
		current:    CurrentCredential{Configured: true, Fingerprint: "sha256:oldcredential", ExpiresAt: "2026-12-31"},
		rotateHook: cancel,
	}
	rotation, created, err := engine.RotateCredential(requestContext, actorHash(), model.CredentialRotationRequest{
		CredentialType: model.GitHubAlertmanagerCredential,
		Secret:         "github_pat_cancel_test_secret_12345678901234567890",
		ExpiresAt:      "2027-08-12",
		Confirmation:   credentialConfirmation,
		IdempotencyKey: mustUUID(t),
	})
	if err != nil || !created || rotation.State != model.CredentialRotationSwitchedPendingRevocation {
		t.Fatalf("rotation=%+v created=%v err=%v", rotation, created, err)
	}
	persisted, err := database.GetCredentialRotation(context.Background(), rotation.ID)
	if err != nil || persisted.State != model.CredentialRotationSwitchedPendingRevocation {
		t.Fatalf("persisted=%+v err=%v", persisted, err)
	}
}

func TestCredentialClosureResumesAfterPersistedRevocationEvidence(t *testing.T) {
	ctx := context.Background()
	engine, database := testEngine(t, &fakeExecutor{})
	rotator := &fakeCredentialRotator{
		current: CurrentCredential{Configured: true, Fingerprint: "sha256:oldcredential", ExpiresAt: "2026-12-31"},
	}
	engine.credentials = rotator
	rotation, _, err := engine.RotateCredential(ctx, actorHash(), model.CredentialRotationRequest{
		CredentialType: model.GitHubAlertmanagerCredential,
		Secret:         "github_pat_resume_test_secret_12345678901234567890",
		ExpiresAt:      "2027-08-12",
		Confirmation:   credentialConfirmation,
		IdempotencyKey: mustUUID(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	rotation, err = database.MarkCredentialRevocationVerified(ctx, rotation.ID, actorHash(), mustUUID(t))
	if err != nil {
		t.Fatal(err)
	}
	closed, fresh, err := engine.CloseCredentialRotation(ctx, actorHash(), rotation.ID,
		model.CredentialRotationCloseRequest{
			Confirmation:   credentialClosureConfirmation,
			IdempotencyKey: mustUUID(t),
		})
	if err != nil || !fresh || closed.State != model.CredentialRotationCompleted {
		t.Fatalf("closed=%+v fresh=%v err=%v", closed, fresh, err)
	}
	if rotator.verifyCalls != 0 || rotator.removeCalls != 1 {
		t.Fatalf("verifyCalls=%d removeCalls=%d", rotator.verifyCalls, rotator.removeCalls)
	}
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
	creator, approver := actorHash(), strings.Repeat("b", 64)
	discoverRelease(t, engine)
	plan, err := engine.CreateReleasePlan(ctx, creator, model.PreviewRequest{
		Service: "demo", Action: "update", Target: "v1.1.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.State != model.PlanPendingApproval || plan.Digest == "" {
		t.Fatalf("plan=%+v", plan)
	}
	if _, _, err := engine.ExecuteReleasePlan(ctx, creator, plan.ID, model.ExecutePlanRequest{
		IdempotencyKey: mustUUID(t),
	}); err == nil {
		t.Fatal("unapproved plan executed")
	}
	approved, err := engine.ApproveReleasePlan(ctx, approver, plan.ID, model.ApprovePlanRequest{
		Confirmation: plan.ConfirmationPhrase, Digest: plan.Digest,
	})
	if err != nil || approved.State != model.PlanApproved {
		t.Fatalf("approved=%+v err=%v", approved, err)
	}
	executionKey := mustUUID(t)
	task, created, err := engine.ExecuteReleasePlan(ctx, creator, plan.ID, model.ExecutePlanRequest{
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
	replayed, created, err := engine.ExecuteReleasePlan(ctx, creator, plan.ID, model.ExecutePlanRequest{
		IdempotencyKey: executionKey,
	})
	if err != nil || created || replayed.ID != task.ID {
		t.Fatalf("replayed=%+v created=%v err=%v", replayed, created, err)
	}
}

func TestPlanClosureRejectsChangedRuntimeIdentity(t *testing.T) {
	ctx := context.Background()
	engine, database := testEngine(t, &fakeExecutor{})
	creator, approver := actorHash(), strings.Repeat("b", 64)
	service := engine.catalog.Services["demo"]
	action := service.Actions["update"]
	action.ObservationSeconds = 1
	service.Actions["update"] = action
	engine.catalog.Services["demo"] = service
	discoverRelease(t, engine)
	plan, err := engine.CreateReleasePlan(ctx, creator, model.PreviewRequest{
		Service: "demo", Action: "update", Target: "v1.1.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	approved, err := engine.ApproveReleasePlan(ctx, approver, plan.ID, model.ApprovePlanRequest{
		Confirmation: plan.ConfirmationPhrase, Digest: plan.Digest,
	})
	if err != nil {
		t.Fatal(err)
	}
	task, _, err := engine.ExecuteReleasePlan(ctx, creator, approved.ID, model.ExecutePlanRequest{
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
	request.Header.Set(actorHeader, creator)
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

func TestHighRiskPlanRejectsCreatorApproval(t *testing.T) {
	ctx := context.Background()
	engine, _ := testEngine(t, &fakeExecutor{})
	discoverRelease(t, engine)
	plan, err := engine.CreateReleasePlan(ctx, actorHash(), model.PreviewRequest{
		Service: "demo", Action: "update", Target: "v1.1.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.ApproveReleasePlan(ctx, actorHash(), plan.ID, model.ApprovePlanRequest{
		Confirmation: plan.ConfirmationPhrase, Digest: plan.Digest,
	}); !errors.Is(err, store.ErrActorMismatch) {
		t.Fatalf("creator approval err=%v, want actor mismatch", err)
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
	service := engine.catalog.Services["demo"]
	service.RecoveryPointPolicy = &model.RecoveryPointPolicy{
		RequiredArtifactRoles: []string{"postgres-demo"}, RecoverableSeconds: 604800,
	}
	point, err := engine.persistRecoveryPoint(ctx, task, service, &model.RecoveryPointEvidence{
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

	missingRoleService := service
	missingRoleService.RecoveryPointPolicy = &model.RecoveryPointPolicy{
		RequiredArtifactRoles: []string{"postgres-demo", "volume-demo"}, RecoverableSeconds: 604800,
	}
	if _, err := engine.persistRecoveryPoint(ctx, task, missingRoleService, &point.Evidence); err == nil ||
		!strings.Contains(err.Error(), "缺少必需产物角色") {
		t.Fatalf("missing role err=%v", err)
	}

	driftedTask := task
	driftedTask.Snapshot = map[string]any{"currentVersion": "changed"}
	if err := engine.verifyRecoveryPoint(ctx, driftedTask, service, point.ID); err == nil ||
		!strings.Contains(err.Error(), "未绑定当前变更前身份") {
		t.Fatalf("identity drift err=%v", err)
	}

	if err := os.WriteFile(artifactPath, []byte("tampered-backup"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := engine.verifyRecoveryPoint(ctx, task, service, point.ID); err == nil ||
		!strings.Contains(err.Error(), "恢复点产物") {
		t.Fatalf("tampered artifact err=%v", err)
	}
	if err := os.WriteFile(artifactPath, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExpireRecoveryPoints(ctx, now.Add(8*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := engine.verifyRecoveryPoint(ctx, task, service, point.ID); err == nil ||
		!strings.Contains(err.Error(), "状态或身份") {
		t.Fatalf("expired point err=%v", err)
	}
}

func TestControlledRollbackPlanRevalidatesCurrentSource(t *testing.T) {
	ctx := context.Background()
	engine, database := testEngine(t, &fakeExecutor{})
	creator, approver := actorHash(), strings.Repeat("b", 64)
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
	plan, err := engine.CreateReleasePlan(ctx, creator, model.PreviewRequest{
		Service: "demo", Action: "rollback", Target: source.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	approved, err := engine.ApproveReleasePlan(ctx, approver, plan.ID, model.ApprovePlanRequest{
		Confirmation: plan.ConfirmationPhrase, Digest: plan.Digest,
	})
	if err != nil {
		t.Fatal(err)
	}
	task, _, err := engine.ExecuteReleasePlan(ctx, creator, approved.ID, model.ExecutePlanRequest{
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
