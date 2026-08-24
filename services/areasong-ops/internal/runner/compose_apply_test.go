package runner

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

type fakeComposeCommandRunner struct {
	mu          sync.Mutex
	calls       [][]string
	failUpCount int
	dependency  string
}

func (runner *fakeComposeCommandRunner) Run(
	_ context.Context, _ string, args ...string,
) (string, error) {
	runner.mu.Lock()
	runner.calls = append(runner.calls, append([]string(nil), args...))
	failUp := strings.Contains(strings.Join(args, " "), " up -d ") && runner.failUpCount > 0
	if failUp {
		runner.failUpCount--
	}
	runner.mu.Unlock()
	joined := strings.Join(args, " ")
	if strings.Contains(joined, " ps -q ") {
		return runner.dependency + "\n", nil
	}
	if failUp {
		return "fake up failed", os.ErrPermission
	}
	return "", nil
}

func TestComposeApplyAndRollbackAreBoundedAndIdempotent(t *testing.T) {
	root := t.TempDir()
	controlledPath := filepath.Join(root, "controlled.yml")
	runtimePath := filepath.Join(root, "runtime.yml")
	envPath := filepath.Join(root, ".env")
	oldContent := "services:\n  web:\n    image: example/web:v1\n"
	newContent := "services:\n  web:\n    image: example/web:v2\n"
	for path, content := range map[string]string{
		controlledPath: oldContent, runtimePath: oldContent, envPath: "COMPOSE_PROJECT_NAME=test\n",
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
	engine.catalog.Services["demo"] = model.ServiceDefinition{
		Name: "demo", ObjectID: "service:demo", Template: "compose-service-v1",
		Runtime: &model.ComposeServiceRuntime{
			ControlledCompose: controlledPath, RuntimeCompose: runtimePath, EnvFile: envPath,
			ApplicationService: "web", ApplicationContainer: "demo-web", DependencyContainers: []string{"db"},
			HealthURL: strings.Replace(health.URL, "localhost", "127.0.0.1", 1),
		},
	}
	runner := &fakeComposeCommandRunner{dependency: "db-container-id"}
	engine.composeRunner = runner
	actorA, actorB, actorC, actorD := actorHash(), strings.Repeat("b", 64), strings.Repeat("c", 64), strings.Repeat("d", 64)
	ctx := context.Background()
	expectedDigest := digestText(oldContent)
	revision, err := engine.ProposeCompose(ctx, actorA, model.ComposeEditRequest{
		Service: "demo", Content: newContent, ExpectedDigest: expectedDigest,
		Mode: "propose", IdempotencyKey: mustUUID(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.ApproveComposeRevision(ctx, actorB, "demo", revision.ID,
		model.ComposeApprovalRequest{Digest: revision.Digest, Confirmation: revision.ConfirmationPhrase}); err != nil {
		t.Fatal(err)
	}
	approved, err := engine.ApproveComposeRevision(ctx, actorC, "demo", revision.ID,
		model.ComposeApprovalRequest{Digest: revision.Digest, Confirmation: revision.ConfirmationPhrase})
	if err != nil || approved.State != "approved" {
		t.Fatalf("approved=%+v err=%v", approved, err)
	}
	applied, err := engine.ApplyComposeRevision(ctx, actorD, "demo", revision.ID,
		model.ComposeApplyRequest{IdempotencyKey: mustUUID(t)})
	if err != nil || applied.State != "applied" {
		t.Fatalf("applied=%+v err=%v", applied, err)
	}
	if content, _ := os.ReadFile(controlledPath); string(content) != newContent {
		t.Fatalf("controlled content=%q", content)
	}
	if content, _ := os.ReadFile(runtimePath); string(content) != newContent {
		t.Fatalf("runtime content=%q", content)
	}
	if applied.BackupControlledPath == "" || applied.BackupRuntimePath == "" {
		t.Fatalf("backups missing: %+v", applied)
	}

	rolledBack, err := engine.RollbackComposeRevision(ctx, actorD, "demo", revision.ID,
		model.ComposeRollbackRequest{Confirmation: "回滚 Compose 变更 " + revision.ID, IdempotencyKey: mustUUID(t)})
	if err != nil || rolledBack.State != "rolled_back" {
		t.Fatalf("rolledBack=%+v err=%v", rolledBack, err)
	}
	if content, _ := os.ReadFile(runtimePath); string(content) != oldContent {
		t.Fatalf("runtime rollback content=%q", content)
	}

	runner.mu.Lock()
	defer runner.mu.Unlock()
	for _, call := range runner.calls {
		joined := strings.Join(call, " ")
		if strings.Contains(joined, " up ") && !strings.Contains(joined, "--no-deps") {
			t.Fatalf("Compose up omitted --no-deps: %v", call)
		}
	}
	_ = database
}

func TestComposeApplyFailureRestoresBothFiles(t *testing.T) {
	root := t.TempDir()
	controlledPath := filepath.Join(root, "controlled.yml")
	runtimePath := filepath.Join(root, "runtime.yml")
	envPath := filepath.Join(root, ".env")
	oldContent := "services:\n  web:\n    image: example/web:v1\n"
	newContent := "services:\n  web:\n    image: example/web:v2\n"
	for path, content := range map[string]string{controlledPath: oldContent, runtimePath: oldContent, envPath: "x=y\n"} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	health := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusOK)
	}))
	defer health.Close()
	engine, _ := testEngine(t, &fakeExecutor{})
	service := engine.catalog.Services["demo"]
	service.Runtime = &model.ComposeServiceRuntime{
		ControlledCompose: controlledPath, RuntimeCompose: runtimePath, EnvFile: envPath,
		ApplicationService: "web", ApplicationContainer: "demo-web", HealthURL: health.URL,
	}
	engine.catalog.Services["demo"] = service
	runner := &fakeComposeCommandRunner{failUpCount: 1}
	engine.composeRunner = runner
	ctx := context.Background()
	revision, err := engine.ProposeCompose(ctx, actorHash(), model.ComposeEditRequest{
		Service: "demo", Content: newContent, ExpectedDigest: digestText(oldContent),
		Mode: "propose", IdempotencyKey: mustUUID(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.ApproveComposeRevision(ctx, strings.Repeat("b", 64), "demo", revision.ID,
		model.ComposeApprovalRequest{Digest: revision.Digest, Confirmation: revision.ConfirmationPhrase}); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.ApproveComposeRevision(ctx, strings.Repeat("c", 64), "demo", revision.ID,
		model.ComposeApprovalRequest{Digest: revision.Digest, Confirmation: revision.ConfirmationPhrase}); err != nil {
		t.Fatal(err)
	}
	failed, err := engine.ApplyComposeRevision(ctx, strings.Repeat("d", 64), "demo", revision.ID,
		model.ComposeApplyRequest{IdempotencyKey: mustUUID(t)})
	if err == nil || failed.State != "rolled_back" {
		t.Fatalf("failed=%+v err=%v", failed, err)
	}
	for _, path := range []string{controlledPath, runtimePath} {
		content, readErr := os.ReadFile(path)
		if readErr != nil || string(content) != oldContent {
			t.Fatalf("%s content=%q err=%v", path, content, readErr)
		}
	}
}
