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

func TestWithBackupRootOnlyAcceptsScopedAbsolutePath(t *testing.T) {
	engine := &Engine{backupRoot: "/var/backups/ops"}
	WithBackupRoot("relative/backups")(engine)
	WithBackupRoot("/")(engine)
	if engine.backupRoot != "/var/backups/ops" {
		t.Fatalf("unsafe backup root accepted: %s", engine.backupRoot)
	}
	want := filepath.Join(t.TempDir(), "backups")
	WithBackupRoot(want)(engine)
	if engine.backupRoot != want {
		t.Fatalf("backup root=%s want=%s", engine.backupRoot, want)
	}
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
			"currentVersion": "1.0.0", "currentImage": "demo@sha256:1111111111111111111111111111111111111111111111111111111111111111",
			"currentImageId": "sha256:image", "runtimeIdentityHash": "sha256:runtime",
		}}, nil
	}
	if input.Action == "check" && input.Phase == "discover" {
		return model.AdapterResult{OK: true, Summary: "discovered", Data: map[string]any{
			"currentVersion": "1.0.0", "latestTag": "v1.1.0", "prepared": true,
		}}, nil
	}
	if input.Action == "restore-drill" && input.Phase == "verify" {
		var contract map[string]any
		if raw, err := os.ReadFile(filepath.Join(input.OperationDir, "recovery-point.json")); err == nil {
			if err := json.Unmarshal(raw, &contract); err != nil {
				return model.AdapterResult{}, err
			}
			return model.AdapterResult{OK: true, Summary: "isolated restore verified", Data: map[string]any{
				"recoveryPointId": contract["recoveryPointId"], "bindingDigest": contract["bindingDigest"], "evidenceDigest": contract["evidenceDigest"],
			}}, nil
		}
	}
	return model.AdapterResult{OK: true, Summary: input.Phase + " ok", Data: map[string]any{"phase": input.Phase}}, nil
}

func testEngine(t *testing.T, executor Executor) (*Engine, *store.Store) {
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

func TestBPAutomaticTaskCannotUseOrdinaryPlan(t *testing.T) {
	engine, _ := testEngine(t, &fakeExecutor{})
	ctx := context.Background()
	if _, err := engine.CreateReleasePlan(ctx, actorHash(), model.PreviewRequest{Service: "collector", Action: "rerun"}); !errors.Is(err, model.ErrReleaseNotIntegrated) {
		t.Fatal("自动任务误作普通人工来源", err)
	}
	tasks, err := engine.store.ListTasks(ctx, 200, 0)
	if err != nil || len(tasks) != 0 {
		t.Fatal("产生自动执行任务", err)
	}
	if len(engine.executor.(*fakeExecutor).calls) != 0 {
		t.Fatal("调用自动任务适配器")
	}
}

func TestServiceViewsExposeManagedComposeCapability(t *testing.T) {
	ctx := context.Background()
	engine, _ := testEngine(t, &fakeExecutor{})

	views := engine.Services(ctx)
	if len(views) != 1 || views[0].ManagedCompose {
		t.Fatalf("custom service unexpectedly exposes managed Compose: %+v", views)
	}

	service := engine.catalog.Services["demo"]
	service.Runtime = &model.ComposeServiceRuntime{}
	engine.catalog.Services["demo"] = service
	views = engine.Services(ctx)
	if len(views) != 1 || !views[0].ManagedCompose {
		t.Fatalf("managed Compose capability missing: %+v", views)
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

func TestBPPreviewRequiresExactPhraseAndRejectsNewTask(t *testing.T) {
	engine, _ := testEngine(t, &fakeExecutor{})
	ctx := context.Background()
	preview := historicalRunnerPreview(t, engine, actorHash(), model.PreviewRequest{Service: "demo", Action: "update", Target: "v1.1.0"})
	if _, _, err := engine.StartTask(ctx, actorHash(), model.StartTaskRequest{PreviewID: preview.ID, Confirmation: "wrong", IdempotencyKey: mustUUID(t)}); !errors.Is(err, store.ErrConfirmation) {
		t.Fatal("确认短语边界退化", err)
	}
	if _, _, err := engine.StartTask(ctx, actorHash(), model.StartTaskRequest{PreviewID: preview.ID, Confirmation: preview.ConfirmationPhrase, IdempotencyKey: mustUUID(t)}); !errors.Is(err, model.ErrReleaseNotIntegrated) {
		t.Fatal("旧预览启动新任务", err)
	}
	if calls := len(engine.executor.(*fakeExecutor).calls); calls != 0 {
		t.Fatal("拒绝后调用适配器")
	}
}

func TestReleasePlanApprovalAndExecutionAreSeparate(t *testing.T) {
	ctx := context.Background()
	engine, profile := preparationEngine(t)
	plan, err := engine.createManualReleasePlan(ctx, actorHash(), bpRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	if plan.State != model.PlanPendingApproval || !plan.RequiresDualApproval {
		t.Fatal("批准边界丢失")
	}
	if _, _, err = engine.ExecuteReleasePlan(ctx, actorHash(), plan.ID, model.ExecutePlanRequest{IdempotencyKey: mustUUID(t)}); err == nil {
		t.Fatal("未批准执行")
	}
	approved := approveReleasePlanForTest(t, engine, plan, strings.Repeat("b", 64))
	if !approved.AllowsExecutor(actorHash()) || approved.AllowsExecutor(strings.Repeat("b", 64)) {
		t.Fatal("执行身份规则退化")
	}
	assertBPPlanFrozen(t, engine, approved)
	if profile.calls != 1 {
		t.Fatal("执行重新检查")
	}
}

func TestBPClosureDoesNotInspectRuntimeIdentity(t *testing.T) {
	engine, _ := testEngine(t, &fakeExecutor{failPhase: "health"})
	plan, _ := historicalObservedPlan(t, engine, "update")
	engine.alertmanager.(*fakeAlertmanager).alerts = []model.ActiveAlert{{Fingerprint: "fedcba0987654321", AlertName: "AppHttpProbeFailed", Labels: map[string]string{"service": "demo"}}}
	assertBPPlanFrozen(t, engine, plan)
	stored, err := engine.store.GetReleasePlan(context.Background(), plan.ID)
	if err != nil || stored.ClosureReason != "" {
		t.Fatal("B-P产生新的收口/回滚结果", err)
	}
}

func TestBPClosurePreservesObservationWithoutStickyReason(t *testing.T) {
	engine, _ := testEngine(t, &fakeExecutor{failPhase: "health"})
	plan, _ := historicalObservedPlan(t, engine, "restart")
	engine.alertmanager.(*fakeAlertmanager).alerts = []model.ActiveAlert{{Fingerprint: "fedcba0987654321", AlertName: "AppHttpProbeFailed", Labels: map[string]string{"service": "demo"}}}
	assertBPPlanFrozen(t, engine, plan)
	stored, err := engine.store.GetReleasePlan(context.Background(), plan.ID)
	if err != nil || stored.ClosureReason != "" {
		t.Fatal("B-P产生新的收口/回滚结果", err)
	}
}

func TestHighRiskPlanRejectsCreatorApproval(t *testing.T) {
	engine, _ := preparationEngine(t)
	ctx := context.Background()
	plan, err := engine.createManualReleasePlan(ctx, actorHash(), bpRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	if !plan.RequiresDualApproval {
		t.Fatal("高风险丢失独立批准")
	}
	if _, err = engine.ApproveReleasePlan(ctx, actorHash(), plan.ID, model.ApprovePlanRequest{Digest: plan.Digest, Confirmation: plan.ConfirmationPhrase}); !errors.Is(err, store.ErrActorMismatch) {
		t.Fatal(err)
	}
}

func TestBPLegacyWeakPlanIsRejectedWithoutHistoryRewrite(t *testing.T) {
	ctx := context.Background()
	engine, database := testEngine(t, &fakeExecutor{})
	now := time.Now().UTC()
	plan := model.ReleasePlan{
		ID: mustUUID(t), ActorHash: actorHash(), Service: "demo", Action: "update", Target: "v1.1.0",
		TenantID: "default", Risk: model.RiskHigh, State: model.PlanApproved, Digest: "sha256:legacy",
		ApprovalSummary: model.ApprovalSummary{
			SchemaVersion: 1, Service: "demo", Action: "update", TenantID: "default",
			Risk: model.RiskHigh, ExpectedBefore: map[string]any{"currentVersion": "1.0.0"},
		},
		ConfirmationPhrase: "更新 demo 到 v1.1.0", ApprovedByHash: strings.Repeat("b", 64),
		CreatedAt: now, UpdatedAt: now,
	}
	if err := database.CreateReleasePlan(ctx, store.ReleasePlanInput{
		Plan: plan, ConfirmationHash: store.HashConfirmation(plan.ConfirmationPhrase),
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := engine.ExecuteReleasePlan(ctx, actorHash(), plan.ID, model.ExecutePlanRequest{
		IdempotencyKey: mustUUID(t),
	}); err == nil {
		t.Fatalf("legacy weak plan execution err=%v", err)
	}
	stored, err := database.GetReleasePlan(ctx, plan.ID)
	if err != nil || stored.State != model.PlanApproved {
		t.Fatalf("legacy weak plan history was rewritten: plan=%+v err=%v", stored, err)
	}
}

func TestBPC2HistoricalExceptionCannotStartNewApproval(t *testing.T) {
	engine, _ := testEngine(t, &fakeExecutor{})
	service := engine.catalog.Services["demo"]
	service.Name = "areaforge"
	service.ObjectID = "service:areaforge"
	service.TenantID = "production"
	action := service.Actions["restart"]
	action.Name = "stop"
	action.Risk = model.RiskHigh
	service.Actions["stop"] = action
	engine.catalog.Services["areaforge"] = service
	plan := historicalRunnerPlan(t, engine, actorHash(), model.PreviewRequest{Service: "areaforge", Action: "stop"})
	if !plan.AllowsC2LifecycleSingleActorApproval() || !plan.HasRequiredApprovalPolicy() {
		t.Fatal("C2历史身份例外丢失")
	}
	if _, err := engine.ApproveReleasePlan(context.Background(), actorHash(), plan.ID, model.ApprovePlanRequest{Digest: plan.Digest, Confirmation: plan.ConfirmationPhrase}); err == nil {
		t.Fatal("C2例外绕过B-P")
	}
	assertBPPlanFrozen(t, engine, plan)
}

func TestPlanExecutionRejectsActiveBlockingAlert(t *testing.T) {
	engine, _ := testEngine(t, &fakeExecutor{failPhase: "health"})
	plan, _ := historicalObservedPlan(t, engine, "update")
	engine.alertmanager.(*fakeAlertmanager).alerts = []model.ActiveAlert{{Fingerprint: "fedcba0987654321", AlertName: "AppHttpProbeFailed", Labels: map[string]string{"service": "demo"}}}
	assertBPPlanFrozen(t, engine, plan)
	stored, err := engine.store.GetReleasePlan(context.Background(), plan.ID)
	if err != nil || stored.ClosureReason != "" {
		t.Fatal("B-P产生新的收口/回滚结果", err)
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

func TestBPClosurePreservesSilenceAndHistory(t *testing.T) {
	engine, _ := testEngine(t, &fakeExecutor{failPhase: "health"})
	plan, _ := historicalObservedPlan(t, engine, "restart")
	engine.alertmanager.(*fakeAlertmanager).alerts = []model.ActiveAlert{{Fingerprint: "fedcba0987654321", AlertName: "AppHttpProbeFailed", Labels: map[string]string{"service": "demo"}}}
	assertBPPlanFrozen(t, engine, plan)
	stored, err := engine.store.GetReleasePlan(context.Background(), plan.ID)
	if err != nil || stored.ClosureReason != "" {
		t.Fatal("B-P产生新的收口/回滚结果", err)
	}
}

func discoverRelease(t *testing.T, engine *Engine) {
	t.Helper()
	seedHistoricalDiscovery(t, engine)
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
	task, _, err := seedRunnerPreviewTask(t, engine, ctx, actorHash(), model.StartTaskRequest{
		PreviewID: preview.ID, Confirmation: "confirm", IdempotencyKey: "recovery-idem",
	}, "task-recovery")
	if err != nil {
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

func TestBPControlledRollbackCannotConsumeHistoricalSource(t *testing.T) {
	engine, _ := testEngine(t, &fakeExecutor{failPhase: "health"})
	plan, _ := historicalObservedPlan(t, engine, "update")
	engine.alertmanager.(*fakeAlertmanager).alerts = []model.ActiveAlert{{Fingerprint: "fedcba0987654321", AlertName: "AppHttpProbeFailed", Labels: map[string]string{"service": "demo"}}}
	assertBPPlanFrozen(t, engine, plan)
	stored, err := engine.store.GetReleasePlan(context.Background(), plan.ID)
	if err != nil || stored.ClosureReason != "" {
		t.Fatal("B-P产生新的收口/回滚结果", err)
	}
}

func TestBPFailureHistoryDoesNotStartRollback(t *testing.T) {
	engine, _ := testEngine(t, &fakeExecutor{failPhase: "health"})
	plan, _ := historicalObservedPlan(t, engine, "update")
	engine.alertmanager.(*fakeAlertmanager).alerts = []model.ActiveAlert{{Fingerprint: "fedcba0987654321", AlertName: "AppHttpProbeFailed", Labels: map[string]string{"service": "demo"}}}
	assertBPPlanFrozen(t, engine, plan)
	stored, err := engine.store.GetReleasePlan(context.Background(), plan.ID)
	if err != nil || stored.ClosureReason != "" {
		t.Fatal("B-P产生新的收口/回滚结果", err)
	}
}

func TestServicesRestoresDiscoveryAndSafeRollbackSource(t *testing.T) {
	engine, _ := testEngine(t, &fakeExecutor{})
	ctx := context.Background()
	seedHistoricalDiscovery(t, engine)
	updated := seedHistoricalRunnerTask(t, engine, model.Task{ActorHash: actorHash(), Service: "demo", Action: "update", Target: "v1.0.0", State: model.TaskSucceeded})
	if err := os.MkdirAll(filepath.Join(engine.stateRoot, "operations", updated.ID), 0700); err != nil {
		t.Fatal(err)
	}
	views := engine.Services(ctx)
	if len(views) != 1 || views[0].ReleaseDiscovery["latestTag"] != "v1.1.0" {
		t.Fatalf("发现历史丢失: %+v", views)
	}
	if views[0].RollbackSourceTaskID != updated.ID {
		t.Fatalf("回滚来源=%q，期望=%q", views[0].RollbackSourceTaskID, updated.ID)
	}
}

func actorHash() string {
	return strings.Repeat("a", 64)
}

func approveReleasePlanForTest(
	t *testing.T,
	engine *Engine,
	plan model.ReleasePlan,
	firstApprover string,
) model.ReleasePlan {
	t.Helper()
	approved, err := engine.ApproveReleasePlan(context.Background(), firstApprover, plan.ID, model.ApprovePlanRequest{
		Confirmation: plan.ConfirmationPhrase, Digest: plan.Digest,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.RequiresDualApproval {
		return approved
	}
	if model.UsesTwoPartyApproval(plan.ApprovalPolicy) {
		if approved.State != model.PlanApproved && approved.State != model.PlanScheduled {
			t.Fatalf("two-party approval did not enter approved state: %+v", approved)
		}
		return approved
	}
	if approved.State != model.PlanPendingApproval || approved.ApprovedByHash != firstApprover {
		t.Fatalf("first approval did not preserve pending state: %+v", approved)
	}
	secondApprover := strings.Repeat("c", 64)
	if secondApprover == plan.ActorHash || secondApprover == firstApprover {
		secondApprover = strings.Repeat("d", 64)
	}
	approved, err = engine.ApproveReleasePlan(context.Background(), secondApprover, plan.ID, model.ApprovePlanRequest{
		Confirmation: plan.ConfirmationPhrase, Digest: plan.Digest,
	})
	if err != nil || (approved.State != model.PlanApproved && approved.State != model.PlanScheduled) {
		t.Fatalf("second approval failed: plan=%+v err=%v", approved, err)
	}
	return approved
}

func mustUUID(t *testing.T) string {
	t.Helper()
	id, err := newUUID()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func releasePlanExecutor(plan model.ReleasePlan) string {
	if plan.Risk != model.RiskHigh || plan.AllowsC2LifecycleSingleActorApproval() {
		return plan.ActorHash
	}
	if model.UsesTwoPartyApproval(plan.ApprovalPolicy) {
		return plan.ActorHash
	}
	for _, candidate := range []string{strings.Repeat("d", 64), strings.Repeat("e", 64), strings.Repeat("f", 64)} {
		if model.IndependentExecutor(candidate, plan.ActorHash, plan.ApprovedByHash, plan.SecondApprovedByHash) {
			return candidate
		}
	}
	return ""
}
