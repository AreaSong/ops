package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	"github.com/AreaSong/ops/services/areasong-ops/internal/store"
)

type fakeComposeCommandRunner struct {
	mu             sync.Mutex
	calls          [][]string
	failUpCount    int
	dependency     string
	currentImage   string
	candidateImage string
}

func createComposeRecoveryPoint(
	t *testing.T,
	engine *Engine,
	database *store.Store,
	service model.ServiceDefinition,
) model.RecoveryPoint {
	t.Helper()
	backupRoot := t.TempDir()
	engine.backupRoot = backupRoot
	artifactPath := filepath.Join(backupRoot, "compose", "recovery.tar")
	if err := os.MkdirAll(filepath.Dir(artifactPath), 0o700); err != nil {
		t.Fatal(err)
	}
	content := []byte("compose-recovery")
	if err := os.WriteFile(artifactPath, content, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	now := time.Now().UTC()
	snapshot := map[string]any{
		"currentVersion": "1.0.0",
		"currentImage":   "demo@sha256:1111111111111111111111111111111111111111111111111111111111111111",
		"currentImageId": "sha256:image", "runtimeIdentityHash": "sha256:runtime",
	}
	preview := model.Preview{
		ID: mustUUID(t), ActorHash: actorHash(), Service: service.Name, Action: "update",
		Risk: model.RiskHigh, Steps: []string{"backup"}, Snapshot: snapshot,
		CreatedAt: now, ExpiresAt: now.Add(5 * time.Minute), ConfirmationPhrase: "confirm",
	}
	if err := database.CreatePreview(context.Background(), store.PreviewInput{
		Preview: preview, ConfirmationHash: store.HashConfirmation("confirm"),
	}); err != nil {
		t.Fatal(err)
	}
	task, _, err := database.StartTask(context.Background(), preview.ActorHash, model.StartTaskRequest{
		PreviewID: preview.ID, Confirmation: "confirm", IdempotencyKey: mustUUID(t),
	}, mustUUID(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := database.MarkRunningOwned(context.Background(), task.ID, "backup", engine.owner); err != nil {
		t.Fatal(err)
	}
	point, err := engine.persistRecoveryPoint(context.Background(), task, service, &model.RecoveryPointEvidence{
		SchemaVersion: 1, Service: service.Name, TaskID: task.ID, CreatedAt: now,
		Artifacts: []model.RecoveryArtifact{{Role: "compose", Path: artifactPath,
			SizeBytes: int64(len(content)), SHA256: "sha256:" + hex.EncodeToString(digest[:])}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return point
}

func TestComposeFileExposesValidatedRuntimeMetadata(t *testing.T) {
	root := t.TempDir()
	controlledPath := filepath.Join(root, "controlled.yml")
	runtimePath := filepath.Join(root, "runtime.yml")
	content := "services:\n  web:\n    image: example/web@sha256:1111111111111111111111111111111111111111111111111111111111111111\n"
	if err := os.WriteFile(controlledPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	engine, _ := testEngine(t, &fakeExecutor{})
	engine.catalog.Services["demo"] = model.ServiceDefinition{
		Name: "demo", ObjectID: "service:demo",
		Runtime: &model.ComposeServiceRuntime{
			ControlledCompose: controlledPath, RuntimeCompose: runtimePath, EnvFile: filepath.Join(root, ".env"), ProjectName: "demo",
			ApplicationService: "web", ApplicationContainer: "demo-web", DependencyServices: []string{}, DependencyContainers: []string{},
			HealthURL: "https://demo.example.test/health",
		},
	}
	view, err := engine.ComposeFile(context.Background(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	if !view.Validated || view.Source != "controlled-file" || view.ApplicationService != "web" || view.ApplicationContainer != "demo-web" {
		t.Fatalf("unexpected compose metadata: %+v", view)
	}
	if len(view.DependencyContainers) != 0 || view.HealthURL == "" {
		t.Fatalf("runtime metadata missing: %+v", view)
	}
	if err := os.WriteFile(controlledPath, []byte("services:\n  web:\n    image: example/web@sha256:1111111111111111111111111111111111111111111111111111111111111111\n    privileged: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	view, err = engine.ComposeFile(context.Background(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	if view.Validated || view.ValidationError == "" {
		t.Fatalf("invalid compose was reported as valid: %+v", view)
	}
}

func TestComposeFileRejectsSymlinkInAncestorPath(t *testing.T) {
	root := t.TempDir()
	realDirectory := filepath.Join(root, "real")
	if err := os.Mkdir(realDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(realDirectory, "compose.yml")
	if err := os.WriteFile(path, []byte("services:\n  web:\n    image: example/web:v1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(realDirectory, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := readComposeFile(filepath.Join(alias, "compose.yml")); err == nil || !strings.Contains(err.Error(), "符号链接") {
		t.Fatalf("symlink ancestor error=%v", err)
	}
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
	joined := strings.Join(args, " ")
	if strings.Contains(joined, " up -d ") && !failUp {
		for index, arg := range args {
			if arg != "-f" || index+1 >= len(args) {
				continue
			}
			content, _ := os.ReadFile(args[index+1])
			for _, line := range strings.Split(string(content), "\n") {
				trimmed := strings.TrimSpace(line)
				if strings.HasPrefix(trimmed, "image:") && strings.Contains(trimmed, "example/web@") {
					runner.currentImage = strings.TrimSpace(strings.TrimPrefix(trimmed, "image:"))
					break
				}
			}
		}
	}
	runner.mu.Unlock()
	if strings.HasSuffix(joined, " config") {
		return fakeRenderedCompose(args)
	}
	if strings.Contains(joined, " ps -q db") {
		return runner.dependency + "\n", nil
	}
	if strings.Contains(joined, " ps -q web") {
		return "app-container-id\n", nil
	}
	if strings.Contains(joined, "inspect --format") && strings.HasSuffix(joined, " db-container-id") {
		return "db-container-id\t/demo-db\tsha256:db-image\n", nil
	}
	if strings.Contains(joined, "inspect --format") && strings.HasSuffix(joined, " app-container-id") {
		return "app-container-id\t/demo-web\t" + runner.currentImage + "\tsha256:app-image\n", nil
	}
	if strings.Contains(joined, "image inspect --format") {
		if strings.Contains(joined, "{{.Id}}") {
			image := args[len(args)-1]
			return "sha256:app-image\t[\"example/web@" + strings.Split(image, "@")[1] + "\"]\n", nil
		}
		return `["example/web@` + strings.Split(runner.currentImage, "@")[1] + `"]` + "\n", nil
	}
	if failUp {
		return "fake up failed", os.ErrPermission
	}
	return "", nil
}

func fakeRenderedCompose(args []string) (string, error) {
	composePath, envPath := "", ""
	for index, arg := range args {
		if index+1 >= len(args) {
			continue
		}
		switch arg {
		case "-f":
			composePath = args[index+1]
		case "--env-file":
			envPath = args[index+1]
		}
	}
	content, err := os.ReadFile(composePath)
	if err != nil {
		return "", err
	}
	envContent, err := os.ReadFile(envPath)
	if err != nil {
		return "", err
	}
	rendered := string(content)
	for _, line := range strings.Split(string(envContent), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok && key != "" {
			rendered = strings.ReplaceAll(rendered, "${"+key+"}", value)
		}
	}
	return rendered, nil
}

func TestComposeApplyAndRollbackAreBoundedAndIdempotent(t *testing.T) {
	root := t.TempDir()
	controlledPath := filepath.Join(root, "controlled.yml")
	runtimePath := filepath.Join(root, "runtime.yml")
	envPath := filepath.Join(root, ".env")
	oldImage := "example/web@sha256:1111111111111111111111111111111111111111111111111111111111111111"
	newImage := "example/web@sha256:2222222222222222222222222222222222222222222222222222222222222222"
	oldContent := "services:\n  web:\n    image: " + oldImage + "\n  db:\n    image: example/db@sha256:3333333333333333333333333333333333333333333333333333333333333333\n"
	newContent := "services:\n  web:\n    image: " + newImage + "\n  db:\n    image: example/db@sha256:3333333333333333333333333333333333333333333333333333333333333333\n"
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
		Name: "demo", ObjectID: "service:demo", Template: "compose-service-v1", ServerID: "server-demo",
		RecoveryPointPolicy: &model.RecoveryPointPolicy{RequiredArtifactRoles: []string{"compose"}, RecoverableSeconds: 3600},
		AlertPolicy:         model.AlertPolicyDefinition{Matchers: map[string]string{"service": "demo"}, BlockingAlerts: []string{"AppHttpProbeFailed"}},
		Runtime: &model.ComposeServiceRuntime{
			ControlledCompose: controlledPath, RuntimeCompose: runtimePath, EnvFile: envPath, ProjectName: "demo",
			ApplicationService: "web", ApplicationContainer: "demo-web", DependencyServices: []string{"db"}, DependencyContainers: []string{"demo-db"},
			HealthURL: strings.Replace(health.URL, "localhost", "127.0.0.1", 1),
		},
	}
	point := createComposeRecoveryPoint(t, engine, database, engine.catalog.Services["demo"])
	runner := &fakeComposeCommandRunner{dependency: "db-container-id", currentImage: oldImage, candidateImage: newImage}
	engine.composeRunner = runner
	actorA, actorB, actorD := actorHash(), strings.Repeat("b", 64), strings.Repeat("d", 64)
	ctx := context.Background()
	expectedDigest := digestText(oldContent)
	proposalKey := mustUUID(t)
	proposalRequest := model.ComposeEditRequest{
		Service: "demo", Content: newContent, ExpectedDigest: expectedDigest,
		Mode: "propose", IdempotencyKey: proposalKey, RecoveryPointID: point.ID,
	}
	revision, err := engine.ProposeCompose(ctx, actorA, proposalRequest)
	if err != nil {
		t.Fatal(err)
	}
	if revision.BaselineEffectiveDigest == "" || revision.CandidateEffectiveDigest == "" || revision.EnvFileDigest == "" {
		t.Fatalf("effective approval evidence missing: %+v", revision)
	}
	replayedProposal, err := engine.ProposeCompose(ctx, actorA, proposalRequest)
	if err != nil || replayedProposal.ID != revision.ID {
		t.Fatalf("proposal replay=%+v err=%v", replayedProposal, err)
	}
	if _, err := engine.ApplyComposeRevision(ctx, actorD, "demo", revision.ID,
		model.ComposeApplyRequest{IdempotencyKey: mustUUID(t)}); err == nil {
		t.Fatal("unapproved Compose revision was applied")
	}
	if content, _ := os.ReadFile(runtimePath); string(content) != oldContent {
		t.Fatalf("unapproved apply changed runtime content=%q", content)
	}
	if err := os.WriteFile(envPath, []byte("COMPOSE_PROJECT_NAME=escaped\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.ApproveComposeRevision(ctx, actorB, "demo", revision.ID,
		model.ComposeApprovalRequest{Digest: revision.Digest, Confirmation: revision.ConfirmationPhrase}); err == nil || !strings.Contains(err.Error(), "env") {
		t.Fatalf("env drift did not stop approval: %v", err)
	}
	if err := os.WriteFile(envPath, []byte("COMPOSE_PROJECT_NAME=test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.ApproveComposeRevision(ctx, actorB, "demo", revision.ID,
		model.ComposeApprovalRequest{Digest: revision.Digest, Confirmation: revision.ConfirmationPhrase}); err != nil {
		t.Fatal(err)
	}
	approved, err := engine.ApproveComposeRevision(ctx, actorB, "demo", revision.ID,
		model.ComposeApprovalRequest{Digest: revision.Digest, Confirmation: revision.ConfirmationPhrase})
	if err != nil || approved.State != "approved" || approved.ApprovedBy != actorB || approved.SecondApprovedByHash != "" {
		t.Fatalf("approved=%+v err=%v", approved, err)
	}
	manager := engine.alertmanager.(*fakeAlertmanager)
	manager.alerts = []model.ActiveAlert{{AlertName: "AppHttpProbeFailed", Fingerprint: "blocker", Labels: map[string]string{"service": "demo"}}}
	if _, err := engine.ApplyComposeRevision(ctx, actorA, "demo", revision.ID,
		model.ComposeApplyRequest{IdempotencyKey: mustUUID(t)}); err == nil || !strings.Contains(err.Error(), "阻断告警") {
		t.Fatalf("blocking alert did not stop apply: %v", err)
	}
	manager.alerts = nil
	runner.currentImage = "example/web@sha256:9999999999999999999999999999999999999999999999999999999999999999"
	if _, err := engine.ApplyComposeRevision(ctx, actorA, "demo", revision.ID,
		model.ComposeApplyRequest{IdempotencyKey: mustUUID(t)}); err == nil || !strings.Contains(err.Error(), "运行") {
		t.Fatalf("runtime drift did not stop apply: %v", err)
	}
	runner.currentImage = oldImage
	applyKey := mustUUID(t)
	if _, err := engine.ApplyComposeRevision(ctx, actorD, "demo", revision.ID,
		model.ComposeApplyRequest{IdempotencyKey: mustUUID(t)}); err == nil {
		t.Fatal("non-creator applied two-party Compose revision")
	}
	applied, err := engine.ApplyComposeRevision(ctx, actorA, "demo", revision.ID,
		model.ComposeApplyRequest{IdempotencyKey: applyKey})
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
	runner.mu.Lock()
	callCount := len(runner.calls)
	runner.mu.Unlock()
	replayed, err := engine.ApplyComposeRevision(ctx, actorA, "demo", revision.ID,
		model.ComposeApplyRequest{IdempotencyKey: applyKey})
	if err != nil || replayed.State != "applied" {
		t.Fatalf("apply replay=%+v err=%v", replayed, err)
	}
	runner.mu.Lock()
	if len(runner.calls) != callCount {
		t.Fatalf("idempotent replay executed Compose again: before=%d after=%d", callCount, len(runner.calls))
	}
	runner.mu.Unlock()

	rolledBack, err := engine.RollbackComposeRevision(ctx, actorA, "demo", revision.ID,
		model.ComposeRollbackRequest{Confirmation: "回滚 Compose 变更 " + revision.ID, IdempotencyKey: mustUUID(t)})
	if err != nil || rolledBack.State != "rolled_back" {
		t.Fatalf("rolledBack=%+v err=%v", rolledBack, err)
	}
	if content, _ := os.ReadFile(runtimePath); string(content) != oldContent {
		t.Fatalf("runtime rollback content=%q", content)
	}

	runner.mu.Lock()
	for _, call := range runner.calls {
		joined := strings.Join(call, " ")
		if strings.Contains(joined, " up ") && !strings.Contains(joined, "--no-deps") {
			t.Fatalf("Compose up omitted --no-deps: %v", call)
		}
	}
	runner.mu.Unlock()
	audits, err := database.ListAudit(ctx, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	counts := make(map[string]int)
	for _, audit := range audits {
		if audit.Resource == revision.ID {
			counts[audit.Event]++
		}
	}
	for _, event := range []string{"compose.revision.proposed", "compose.revision.apply_started", "compose.revision.apply_finished", "compose.revision.rollback_started", "compose.revision.rollback_finished"} {
		if counts[event] != 1 {
			t.Fatalf("audit %s count=%d", event, counts[event])
		}
	}
}

func TestComposeApplyFailureRestoresBothFiles(t *testing.T) {
	root := t.TempDir()
	controlledPath := filepath.Join(root, "controlled.yml")
	runtimePath := filepath.Join(root, "runtime.yml")
	envPath := filepath.Join(root, ".env")
	oldContent := "services:\n  web:\n    image: example/web@sha256:1111111111111111111111111111111111111111111111111111111111111111\n"
	newContent := "services:\n  web:\n    image: example/web@sha256:2222222222222222222222222222222222222222222222222222222222222222\n"
	for path, content := range map[string]string{controlledPath: oldContent, runtimePath: oldContent, envPath: "x=y\n"} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	health := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusOK)
	}))
	defer health.Close()
	engine, database := testEngine(t, &fakeExecutor{})
	service := engine.catalog.Services["demo"]
	service.ServerID = "server-demo"
	service.RecoveryPointPolicy = &model.RecoveryPointPolicy{RequiredArtifactRoles: []string{"compose"}, RecoverableSeconds: 3600}
	service.Runtime = &model.ComposeServiceRuntime{
		ControlledCompose: controlledPath, RuntimeCompose: runtimePath, EnvFile: envPath, ProjectName: "demo",
		ApplicationService: "web", ApplicationContainer: "demo-web", HealthURL: health.URL,
	}
	engine.catalog.Services["demo"] = service
	point := createComposeRecoveryPoint(t, engine, database, service)
	runner := &fakeComposeCommandRunner{
		failUpCount:  1,
		currentImage: "example/web@sha256:1111111111111111111111111111111111111111111111111111111111111111",
	}
	engine.composeRunner = runner
	ctx := context.Background()
	revision, err := engine.ProposeCompose(ctx, actorHash(), model.ComposeEditRequest{
		Service: "demo", Content: newContent, ExpectedDigest: digestText(oldContent),
		Mode: "propose", IdempotencyKey: mustUUID(t), RecoveryPointID: point.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.ApproveComposeRevision(ctx, strings.Repeat("b", 64), "demo", revision.ID,
		model.ComposeApprovalRequest{Digest: revision.Digest, Confirmation: revision.ConfirmationPhrase}); err != nil {
		t.Fatal(err)
	}
	failed, err := engine.ApplyComposeRevision(ctx, actorHash(), "demo", revision.ID,
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
