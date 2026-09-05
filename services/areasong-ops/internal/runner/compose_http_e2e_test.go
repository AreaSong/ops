package runner

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	"github.com/AreaSong/ops/services/areasong-ops/internal/store"
)

func TestComposeHTTPFourActorApplyAndRollbackEndToEnd(t *testing.T) {
	root := t.TempDir()
	controlledPath := filepath.Join(root, "controlled.yml")
	runtimePath := filepath.Join(root, "runtime.yml")
	envPath := filepath.Join(root, ".env")
	oldImage := "example/web@sha256:" + strings.Repeat("1", 64)
	newImage := "example/web@sha256:" + strings.Repeat("2", 64)
	dependencyImage := "example/db@sha256:" + strings.Repeat("3", 64)
	oldContent := composeHTTPContent(oldImage, dependencyImage)
	newContent := composeHTTPContent(newImage, dependencyImage)
	for path, content := range map[string]string{
		controlledPath: oldContent,
		runtimePath:    oldContent,
		envPath:        "COMPOSE_PROJECT_NAME=demo\n",
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	health := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusOK)
	}))
	defer health.Close()

	engine, database := testEngine(t, &fakeExecutor{})
	engine.catalog.SchemaVersion = 4
	engine.catalog.Services["demo"] = model.ServiceDefinition{
		Name: "demo", ObjectID: "service:demo", TenantID: "default",
		Template: "compose-service-v1", ServerID: "server-demo",
		RecoveryPointPolicy: &model.RecoveryPointPolicy{
			RequiredArtifactRoles: []string{"compose"}, RecoverableSeconds: 3600,
		},
		AlertPolicy: model.AlertPolicyDefinition{
			Matchers: map[string]string{"service": "demo"}, BlockingAlerts: []string{"AppHttpProbeFailed"},
		},
		Runtime: &model.ComposeServiceRuntime{
			ControlledCompose: controlledPath, RuntimeCompose: runtimePath,
			EnvFile: envPath, ProjectName: "demo", ApplicationService: "web",
			ApplicationContainer: "demo-web", DependencyServices: []string{"db"},
			DependencyContainers: []string{"demo-db"}, HealthURL: health.URL,
		},
	}
	point := createComposeRecoveryPoint(t, engine, database, engine.catalog.Services["demo"])
	commandRunner := &fakeComposeCommandRunner{
		dependency: "db-container-id", currentImage: oldImage, candidateImage: newImage,
	}
	engine.composeRunner = commandRunner
	actors := installBatchSecurityActors(engine, 5)
	client := runnerAPITestClient{t: t, handler: NewServer(engine, database)}

	var view model.ComposeFileView
	client.as(actors[0]).request(http.MethodGet, "/v1/compose/demo", nil, http.StatusOK, &view)
	if !view.Validated || view.Digest != digestText(oldContent) || view.TenantID != "default" {
		t.Fatalf("compose view=%+v", view)
	}

	proposal := model.ComposeEditRequest{
		Content: newContent, ExpectedDigest: view.Digest, Mode: "propose",
		IdempotencyKey: mustUUID(t), RecoveryPointID: point.ID,
	}
	var revision model.ComposeRevision
	client.as(actors[0]).request(http.MethodPost, "/v1/compose/demo/revisions", proposal, http.StatusCreated, &revision)
	if revision.State != "proposed" || revision.ActorHash != actors[0] ||
		revision.RecoveryPointID != point.ID || revision.ExpectedRuntimeIdentityDigest == "" ||
		revision.BaselineEffectiveDigest == "" || revision.CandidateEffectiveDigest == "" ||
		revision.EnvFileDigest == "" || len(revision.SemanticDiff) != 1 {
		t.Fatalf("compose proposal=%+v", revision)
	}
	var replay model.ComposeRevision
	client.as(actors[0]).request(http.MethodPost, "/v1/compose/demo/revisions", proposal, http.StatusCreated, &replay)
	if replay.ID != revision.ID || replay.Digest != revision.Digest {
		t.Fatalf("proposal replay=%+v original=%+v", replay, revision)
	}

	approval := model.ComposeApprovalRequest{
		Digest: revision.Digest, Confirmation: revision.ConfirmationPhrase,
	}
	client.as(actors[0]).request(http.MethodPost, composeRevisionPath(revision.ID, "approve"), approval, http.StatusConflict, nil)
	client.as(actors[1]).request(http.MethodPost, composeRevisionPath(revision.ID, "approve"), approval, http.StatusOK, &revision)
	if revision.State != "pending_second_approval" || revision.ApprovedBy != actors[1] {
		t.Fatalf("first approval=%+v", revision)
	}
	client.as(actors[2]).request(http.MethodPost, composeRevisionPath(revision.ID, "approve"), approval, http.StatusOK, &revision)
	if revision.State != "approved" || revision.SecondApprovedByHash != actors[2] {
		t.Fatalf("second approval=%+v", revision)
	}

	apply := model.ComposeApplyRequest{IdempotencyKey: mustUUID(t)}
	client.as(actors[2]).request(http.MethodPost, composeRevisionPath(revision.ID, "apply"), apply, http.StatusConflict, nil)
	client.as(actors[3]).request(http.MethodPost, composeRevisionPath(revision.ID, "apply"), apply, http.StatusOK, &revision)
	if revision.State != "applied" || revision.AppliedByHash != actors[3] {
		t.Fatalf("applied revision=%+v", revision)
	}
	assertFileContent(t, controlledPath, newContent)
	assertFileContent(t, runtimePath, newContent)
	commandCount := composeCommandCount(commandRunner)
	client.as(actors[3]).request(http.MethodPost, composeRevisionPath(revision.ID, "apply"), apply, http.StatusOK, &replay)
	if composeCommandCount(commandRunner) != commandCount {
		t.Fatal("idempotent HTTP apply executed Compose again")
	}
	client.as(actors[4]).request(http.MethodPost, composeRevisionPath(revision.ID, "apply"), apply, http.StatusConflict, nil)

	rollback := model.ComposeRollbackRequest{
		Confirmation: "回滚 Compose 变更 " + revision.ID, IdempotencyKey: mustUUID(t),
	}
	client.as(actors[3]).request(http.MethodPost, composeRevisionPath(revision.ID, "rollback"), rollback, http.StatusOK, &revision)
	if revision.State != "rolled_back" || revision.RolledBackByHash != actors[3] {
		t.Fatalf("rolled back revision=%+v", revision)
	}
	assertFileContent(t, controlledPath, oldContent)
	assertFileContent(t, runtimePath, oldContent)
	commandCount = composeCommandCount(commandRunner)
	client.as(actors[3]).request(http.MethodPost, composeRevisionPath(revision.ID, "rollback"), rollback, http.StatusOK, &replay)
	if composeCommandCount(commandRunner) != commandCount {
		t.Fatal("idempotent HTTP rollback executed Compose again")
	}
	client.as(actors[4]).request(http.MethodPost, composeRevisionPath(revision.ID, "rollback"), rollback, http.StatusConflict, nil)

	audits, err := database.ListAudit(context.Background(), 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	events := map[string]int{}
	for _, audit := range audits {
		if audit.Resource == revision.ID {
			events[audit.Event]++
		}
	}
	wantEvents := map[string]int{
		"compose.revision.proposed":          1,
		"compose.revision.approved":          2,
		"compose.revision.apply_started":     1,
		"compose.revision.apply_finished":    1,
		"compose.revision.rollback_started":  1,
		"compose.revision.rollback_finished": 1,
	}
	for event, count := range wantEvents {
		if events[event] != count {
			t.Fatalf("audit %s count=%d want=%d events=%v", event, events[event], count, events)
		}
	}
}

func TestComposeHTTPExecutionGatesFailClosed(t *testing.T) {
	tests := []struct {
		name      string
		wantError string
		mutate    func(*composeHTTPGateFixture)
	}{
		{
			name: "recovery artifact tamper", wantError: "恢复点",
			mutate: func(fixture *composeHTTPGateFixture) {
				artifact := fixture.point.Evidence.Artifacts[0].Path
				if err := os.WriteFile(artifact, []byte("tampered"), 0o600); err != nil {
					fixture.t.Fatal(err)
				}
			},
		},
		{
			name: "environment drift", wantError: "env",
			mutate: func(fixture *composeHTTPGateFixture) {
				if err := os.WriteFile(fixture.envPath, []byte("COMPOSE_PROJECT_NAME=changed\n"), 0o600); err != nil {
					fixture.t.Fatal(err)
				}
			},
		},
		{
			name: "runtime identity drift", wantError: "运行",
			mutate: func(fixture *composeHTTPGateFixture) {
				fixture.commandRunner.currentImage = "example/web@sha256:" + strings.Repeat("9", 64)
			},
		},
		{
			name: "blocking alert", wantError: "阻断告警",
			mutate: func(fixture *composeHTTPGateFixture) {
				fixture.alertmanager.alerts = []model.ActiveAlert{{
					AlertName: "AppHttpProbeFailed", Fingerprint: "blocking-alert",
					Labels: map[string]string{"service": "demo"},
				}}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newComposeHTTPGateFixture(t)
			test.mutate(fixture)
			response := fixture.client.as(fixture.actors[3]).request(
				http.MethodPost, composeRevisionPath(fixture.revision.ID, "apply"),
				model.ComposeApplyRequest{IdempotencyKey: mustUUID(t)}, http.StatusConflict, nil,
			)
			if !strings.Contains(response.Body.String(), test.wantError) {
				t.Fatalf("response=%s want error containing %q", response.Body.String(), test.wantError)
			}
			stored, err := fixture.database.GetComposeRevision(context.Background(), fixture.revision.ID)
			if err != nil || stored.State != "approved" || stored.ApplyIdempotencyKey != "" {
				t.Fatalf("failed gate changed revision=%+v err=%v", stored, err)
			}
			assertFileContent(t, fixture.controlledPath, fixture.oldContent)
			assertFileContent(t, fixture.runtimePath, fixture.oldContent)
			if composeMutationCommandCount(fixture.commandRunner) != 0 {
				t.Fatalf("failed gate executed Compose mutation: %+v", fixture.commandRunner.calls)
			}
		})
	}
}

type composeHTTPGateFixture struct {
	t              *testing.T
	database       *store.Store
	client         runnerAPITestClient
	actors         []string
	revision       model.ComposeRevision
	point          model.RecoveryPoint
	commandRunner  *fakeComposeCommandRunner
	alertmanager   *fakeAlertmanager
	controlledPath string
	runtimePath    string
	envPath        string
	oldContent     string
}

func newComposeHTTPGateFixture(t *testing.T) *composeHTTPGateFixture {
	t.Helper()
	root := t.TempDir()
	controlledPath := filepath.Join(root, "controlled.yml")
	runtimePath := filepath.Join(root, "runtime.yml")
	envPath := filepath.Join(root, ".env")
	oldImage := "example/web@sha256:" + strings.Repeat("1", 64)
	newImage := "example/web@sha256:" + strings.Repeat("2", 64)
	dependencyImage := "example/db@sha256:" + strings.Repeat("3", 64)
	oldContent := composeHTTPContent(oldImage, dependencyImage)
	newContent := composeHTTPContent(newImage, dependencyImage)
	for path, content := range map[string]string{
		controlledPath: oldContent, runtimePath: oldContent, envPath: "COMPOSE_PROJECT_NAME=demo\n",
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	health := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(health.Close)
	engine, database := testEngine(t, &fakeExecutor{})
	engine.catalog.SchemaVersion = 4
	service := model.ServiceDefinition{
		Name: "demo", ObjectID: "service:demo", TenantID: "default",
		Template: "compose-service-v1", ServerID: "server-demo",
		RecoveryPointPolicy: &model.RecoveryPointPolicy{
			RequiredArtifactRoles: []string{"compose"}, RecoverableSeconds: 3600,
		},
		AlertPolicy: model.AlertPolicyDefinition{
			Matchers: map[string]string{"service": "demo"}, BlockingAlerts: []string{"AppHttpProbeFailed"},
		},
		Runtime: &model.ComposeServiceRuntime{
			ControlledCompose: controlledPath, RuntimeCompose: runtimePath, EnvFile: envPath,
			ProjectName: "demo", ApplicationService: "web", ApplicationContainer: "demo-web",
			DependencyServices: []string{"db"}, DependencyContainers: []string{"demo-db"},
			HealthURL: health.URL,
		},
	}
	engine.catalog.Services["demo"] = service
	point := createComposeRecoveryPoint(t, engine, database, service)
	commandRunner := &fakeComposeCommandRunner{
		dependency: "db-container-id", currentImage: oldImage, candidateImage: newImage,
	}
	engine.composeRunner = commandRunner
	actors := installBatchSecurityActors(engine, 5)
	client := runnerAPITestClient{t: t, handler: NewServer(engine, database)}
	var view model.ComposeFileView
	client.as(actors[0]).request(http.MethodGet, "/v1/compose/demo", nil, http.StatusOK, &view)
	var revision model.ComposeRevision
	client.as(actors[0]).request(http.MethodPost, "/v1/compose/demo/revisions", model.ComposeEditRequest{
		Content: newContent, ExpectedDigest: view.Digest, Mode: "propose",
		IdempotencyKey: mustUUID(t), RecoveryPointID: point.ID,
	}, http.StatusCreated, &revision)
	approval := model.ComposeApprovalRequest{Digest: revision.Digest, Confirmation: revision.ConfirmationPhrase}
	for _, actor := range actors[1:3] {
		client.as(actor).request(http.MethodPost, composeRevisionPath(revision.ID, "approve"), approval, http.StatusOK, &revision)
	}
	return &composeHTTPGateFixture{
		t: t, database: database, client: client, actors: actors, revision: revision, point: point,
		commandRunner: commandRunner, alertmanager: engine.alertmanager.(*fakeAlertmanager),
		controlledPath: controlledPath, runtimePath: runtimePath, envPath: envPath, oldContent: oldContent,
	}
}

func composeMutationCommandCount(runner *fakeComposeCommandRunner) int {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	count := 0
	for _, call := range runner.calls {
		joined := " " + strings.Join(call, " ") + " "
		if strings.Contains(joined, " up ") || strings.Contains(joined, " down ") ||
			strings.Contains(joined, " restart ") {
			count++
		}
	}
	return count
}

func composeHTTPContent(applicationImage, dependencyImage string) string {
	return "services:\n  web:\n    image: " + applicationImage +
		"\n  db:\n    image: " + dependencyImage + "\n"
}

func composeRevisionPath(id, operation string) string {
	return "/v1/compose/demo/revisions/" + id + "/" + operation
}

func composeCommandCount(runner *fakeComposeCommandRunner) int {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	return len(runner.calls)
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != want {
		t.Fatalf("file %s content=%q want=%q", path, content, want)
	}
}
