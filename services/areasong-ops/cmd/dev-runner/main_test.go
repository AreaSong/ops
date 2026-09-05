package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/buildinfo"
	"github.com/AreaSong/ops/services/areasong-ops/internal/config"
	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	"github.com/AreaSong/ops/services/areasong-ops/internal/runner"
	"github.com/AreaSong/ops/services/areasong-ops/internal/store"
)

func TestDevelopmentUnixListenerSetsWebSocketMode(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "ops-sock-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	socket := filepath.Join(root, "runner.sock")
	listener, err := developmentListener("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	info, err := os.Stat(socket)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o660 {
		t.Fatalf("socket mode=%#o want 0660", got)
	}
}

func TestDevelopmentRunnerControllerVerifiesIdentityAndHealth(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "ops-dev-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	socket := filepath.Join(root, "runner.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/healthz" {
			http.NotFound(response, request)
			return
		}
		_ = json.NewEncoder(response).Encode(map[string]any{"ok": true, "component": "runner"})
	}))
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)

	identityPath := filepath.Join(root, "runner.identity")
	controller := developmentRunnerController{socketPath: socket, identityPath: identityPath}
	revision := strings.Repeat("a", 40)
	if err := controller.SetIdentity("v2", revision); err != nil {
		t.Fatal(err)
	}
	if err := controller.WaitIdentity(context.Background(), "", "", "v2", revision, time.Second); err != nil {
		t.Fatalf("health verification failed: %v", err)
	}
	if err := controller.WaitIdentity(context.Background(), "", "", "v3", revision, 30*time.Millisecond); err == nil {
		t.Fatal("identity mismatch was accepted")
	}
}

func TestDevelopmentRunnerUpdateAPIExecutesAndRollsBack(t *testing.T) {
	for _, test := range []struct {
		name         string
		failTarget   bool
		wantState    string
		wantBody     string
		wantIdentity string
	}{
		{name: "success", wantState: "succeeded", wantBody: "runner-v2", wantIdentity: "v2"},
		{name: "rollback", failTarget: true, wantState: "rolled_back", wantBody: "runner-v1", wantIdentity: "v1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newDevelopmentRunnerUpdateAPIFixture(t, test.failTarget)
			prepared := postDevelopmentRunnerUpdate[model.RunnerUpdate](
				t, fixture.handler, fixture.prepareActor, "/v1/runner/update/prepare",
				fixture.request, http.StatusAccepted,
			)
			activation := model.RunnerUpdateActivationRequest{
				Confirmation:   prepared.ConfirmationPhrase,
				IdempotencyKey: "22222222-2222-4222-8222-222222222222",
			}
			postDevelopmentRunnerUpdate[model.RunnerUpdate](
				t, fixture.handler, fixture.activateActor,
				"/v1/runner/update/"+prepared.ID+"/activate", activation, http.StatusAccepted,
			)
			stored, err := fixture.database.GetRunnerUpdate(context.Background(), prepared.ID)
			if err != nil || stored.State != test.wantState {
				t.Fatalf("stored update=%+v err=%v", stored, err)
			}
			body, err := os.ReadFile(fixture.binaryPath)
			if err != nil || string(body) != test.wantBody {
				t.Fatalf("binary=%q err=%v", body, err)
			}
			identityBody, err := os.ReadFile(fixture.binaryPath + ".identity")
			if err != nil {
				t.Fatal(err)
			}
			var identity developmentRunnerIdentity
			if err := json.Unmarshal(identityBody, &identity); err != nil || identity.Version != test.wantIdentity {
				t.Fatalf("identity=%+v err=%v", identity, err)
			}
			replayed := postDevelopmentRunnerUpdate[model.RunnerUpdate](
				t, fixture.handler, fixture.activateActor,
				"/v1/runner/update/"+prepared.ID+"/activate", activation, http.StatusOK,
			)
			if replayed.State != test.wantState {
				t.Fatalf("replayed=%+v", replayed)
			}
			status := getDevelopmentRunnerUpdate[model.RunnerUpdateStatus](
				t, fixture.handler, fixture.prepareActor, "/v1/runner/update", http.StatusOK,
			)
			if len(status.Recent) != 1 || status.Recent[0].State != test.wantState || len(status.Pending) != 0 {
				t.Fatalf("status=%+v", status)
			}
		})
	}
}

type developmentRunnerUpdateAPIFixture struct {
	handler       http.Handler
	database      *store.Store
	request       model.RunnerUpdateRequest
	prepareActor  string
	activateActor string
	binaryPath    string
}

func newDevelopmentRunnerUpdateAPIFixture(t *testing.T, failTarget bool) developmentRunnerUpdateAPIFixture {
	t.Helper()
	temporaryRoot, err := os.MkdirTemp("/tmp", "ops-runner-update-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(temporaryRoot) })
	root, err := filepath.EvalSymlinks(temporaryRoot)
	if err != nil {
		t.Fatal(err)
	}
	database, err := store.Open(filepath.Join(root, "ops.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	incoming := filepath.Join(root, "incoming")
	if err := os.MkdirAll(incoming, 0o700); err != nil {
		t.Fatal(err)
	}
	binaryPath := filepath.Join(root, "runner", "areasong-ops-runner")
	if err := os.MkdirAll(filepath.Dir(binaryPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binaryPath, []byte("runner-v1"), 0o700); err != nil {
		t.Fatal(err)
	}
	artifactPath := filepath.Join(incoming, "runner-v2")
	if err := os.WriteFile(artifactPath, []byte("runner-v2"), 0o700); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(root, "runner.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	health := httptest.NewUnstartedServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		if failTarget {
			payload, readErr := os.ReadFile(binaryPath + ".identity")
			var identity developmentRunnerIdentity
			if readErr == nil && json.Unmarshal(payload, &identity) == nil && identity.Version == "v2" {
				http.Error(response, "target unavailable", http.StatusServiceUnavailable)
				return
			}
		}
		_ = json.NewEncoder(response).Encode(map[string]any{"ok": true, "component": "runner"})
	}))
	health.Listener = listener
	health.Start()
	t.Cleanup(health.Close)

	seed, err := hex.DecodeString("9d61b19deffd5a60ba844af492ec2cc44449c5697b326919703bac031cae7f60")
	if err != nil {
		t.Fatal(err)
	}
	privateKey := ed25519.NewKeyFromSeed(seed)
	policy := &config.RunnerUpdatePolicy{
		Enabled: true, RunnerID: "development-runner", ArtifactRoot: incoming,
		BinaryPath: binaryPath, UnitName: "areasong-ops-runner.service",
		UpdaterUnitName: "areasong-ops-runner-update@.service",
		Publisher:       developmentPublisher,
		ManifestPurpose: config.RunnerUpdateManifestPurpose,
		ManifestSchema:  config.RunnerUpdateManifestSchema,
		ManifestGOOS:    config.RunnerUpdateManifestGOOS,
		ManifestGOARCH:  config.RunnerUpdateManifestGOARCH,
		TrustedPublisherKeys: map[string]string{
			developmentPublisher: base64.StdEncoding.EncodeToString(privateKey.Public().(ed25519.PublicKey)),
		},
		HealthTimeoutSeconds: 1, MaxArtifactBytes: 1 << 20,
	}
	catalog := &config.Catalog{
		SchemaVersion: 3, Services: map[string]model.ServiceDefinition{}, RunnerUpdate: policy,
	}
	engine, err := runner.NewEngineChecked(
		catalog, database, &demoExecutor{versions: map[string]string{}}, root,
		runner.WithRunnerUpdateLauncher(developmentRunnerUpdateLauncher{
			database: database, stateRoot: root, socketPath: socket,
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(engine.Stop)
	originalVersion, originalRevision := buildinfo.Version, buildinfo.Revision
	buildinfo.Version, buildinfo.Revision = "v1", strings.Repeat("b", 40)
	t.Cleanup(func() { buildinfo.Version, buildinfo.Revision = originalVersion, originalRevision })
	digest := sha256.Sum256([]byte("runner-v2"))
	request := model.RunnerUpdateRequest{
		Manifest: model.RunnerUpdateManifest{
			Purpose: config.RunnerUpdateManifestPurpose, Schema: config.RunnerUpdateManifestSchema,
			GOOS: config.RunnerUpdateManifestGOOS, GOARCH: config.RunnerUpdateManifestGOARCH,
			RunnerID: "development-runner", TargetVersion: "v2",
			ArtifactDigest:   "sha256:" + hex.EncodeToString(digest[:]),
			ArtifactRevision: strings.Repeat("a", 40), Publisher: developmentPublisher,
		},
		ManifestPurpose: config.RunnerUpdateManifestPurpose, ManifestSchema: config.RunnerUpdateManifestSchema,
		ManifestGOOS: config.RunnerUpdateManifestGOOS, ManifestGOARCH: config.RunnerUpdateManifestGOARCH,
		RunnerID: "development-runner", TargetVersion: "v2", ArtifactPath: "runner-v2",
		ArtifactDigest:   "sha256:" + hex.EncodeToString(digest[:]),
		ArtifactRevision: strings.Repeat("a", 40), Publisher: developmentPublisher,
		Confirmation: "准备 Runner 更新到 v2", IdempotencyKey: "11111111-1111-4111-8111-111111111111",
	}
	manifestPayload, err := json.Marshal(request.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	request.ArtifactSignature = base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, manifestPayload))
	return developmentRunnerUpdateAPIFixture{
		handler: runner.NewServer(engine, database), database: database, request: request,
		prepareActor: strings.Repeat("1", 64), activateActor: strings.Repeat("2", 64), binaryPath: binaryPath,
	}
}

func postDevelopmentRunnerUpdate[T any](
	t *testing.T, handler http.Handler, actor, path string, body any, wantStatus int,
) T {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(payload))
	request.Header.Set("X-AreaSong-Ops-Actor-Hash", actor)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != wantStatus {
		t.Fatalf("POST %s status=%d want=%d body=%s", path, response.Code, wantStatus, response.Body.String())
	}
	var result T
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode POST %s: %v body=%s", path, err, response.Body.String())
	}
	return result
}

func getDevelopmentRunnerUpdate[T any](
	t *testing.T, handler http.Handler, actor, path string, wantStatus int,
) T {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.Header.Set("X-AreaSong-Ops-Actor-Hash", actor)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != wantStatus {
		t.Fatalf("GET %s status=%d want=%d body=%s", path, response.Code, wantStatus, response.Body.String())
	}
	var result T
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode GET %s: %v body=%s", path, err, response.Body.String())
	}
	return result
}

func TestDemoExecutorTracksLifecycleState(t *testing.T) {
	executor := &demoExecutor{
		versions:      map[string]string{"areaforge": "1.1.1"},
		appStates:     map[string]string{"areaforge": "running"},
		trafficStates: map[string]string{"areaforge": "running"},
	}
	service := model.ServiceDefinition{Name: "areaforge"}
	execute := func(action, phase, kind string) model.AdapterResult {
		t.Helper()
		result, err := executor.Execute(context.Background(), runner.ExecuteInput{
			Service: service, Action: action, Phase: phase, AdapterKind: kind,
		})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}

	execute("drain", "drain", "traffic")
	execute("enter-maintenance", "enter-maintenance", "traffic")
	execute("stop", "stop", "service")
	if got := execute("inspect", "inspect", "service").Data["appState"]; got != "stopped" {
		t.Fatalf("stopped app state=%v", got)
	}
	if got := execute("inspect", "inspect", "traffic").Data["trafficState"]; got != "maintenance" {
		t.Fatalf("maintenance traffic state=%v", got)
	}

	execute("start", "start", "service")
	execute("resume-traffic", "resume-traffic", "traffic")
	if got := execute("inspect", "inspect", "service").Data["appState"]; got != "running" {
		t.Fatalf("running app state=%v", got)
	}
	if got := execute("inspect", "inspect", "traffic").Data["trafficState"]; got != "running" {
		t.Fatalf("running traffic state=%v", got)
	}
	if got := execute("resume-traffic", "verify", "traffic").Data["trafficState"]; got != "running" {
		t.Fatalf("verified traffic state=%v", got)
	}
}

func TestDemoExecutorCreatesVerifiableRecoveryArtifacts(t *testing.T) {
	backupRoot := t.TempDir()
	taskID := "11111111-1111-4111-8111-111111111111"
	executor := &demoExecutor{
		versions:   map[string]string{"demo": "1.0.0"},
		backupRoot: backupRoot,
	}
	result, err := executor.Execute(context.Background(), runner.ExecuteInput{
		Service: model.ServiceDefinition{
			Name: "demo",
			RecoveryPointPolicy: &model.RecoveryPointPolicy{
				RequiredArtifactRoles: []string{"postgres-demo", "volume-demo"},
			},
		},
		Action: "backup", Phase: "backup", OperationDir: filepath.Join(t.TempDir(), taskID),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.RecoveryPoint == nil || result.RecoveryPoint.TaskID != taskID ||
		len(result.RecoveryPoint.Artifacts) != 2 {
		t.Fatalf("recovery point=%+v", result.RecoveryPoint)
	}
	for _, artifact := range result.RecoveryPoint.Artifacts {
		if !pathWithin(backupRoot, artifact.Path) || !strings.HasPrefix(artifact.SHA256, "sha256:") {
			t.Fatalf("artifact=%+v", artifact)
		}
		info, statErr := os.Lstat(artifact.Path)
		if statErr != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() != artifact.SizeBytes {
			t.Fatalf("artifact info=%+v err=%v", info, statErr)
		}
	}
}

func TestPrepareDevelopmentComposeIsIsolatedAndAllowlisted(t *testing.T) {
	seedRoot := t.TempDir()
	runtimeRoot := t.TempDir()
	seedPath := filepath.Join(seedRoot, "compose.yml")
	seed := []byte("services:\n  app:\n    image: example.test/app@sha256:" + strings.Repeat("a", 64) +
		"\n  db:\n    image: example.test/db@sha256:" + strings.Repeat("b", 64) + "\n")
	if err := os.WriteFile(seedPath, seed, 0o600); err != nil {
		t.Fatal(err)
	}
	catalog := &config.Catalog{Services: map[string]model.ServiceDefinition{
		"demo": {
			Name: "demo",
			Runtime: &model.ComposeServiceRuntime{
				ControlledCompose:    seedPath,
				RuntimeCompose:       "/opt/services/demo/compose.yml",
				EnvFile:              "/opt/services/demo/.env",
				ProjectName:          "demo",
				ApplicationService:   "app",
				ApplicationContainer: "demo-app",
				DependencyServices:   []string{"db"},
				DependencyContainers: []string{"demo-db"},
			},
		},
	}}

	if compose, err := prepareDevelopmentCompose(catalog); err != nil || compose != nil {
		t.Fatalf("Compose 未开启时 runner=%v err=%v", compose, err)
	}
	t.Setenv("OPS_DEV_ENABLE_FEATURES", "compose")
	t.Setenv("OPS_DEV_RUNTIME_ROOT", runtimeRoot)
	compose, err := prepareDevelopmentCompose(catalog)
	if err != nil {
		t.Fatal(err)
	}
	runtime := catalog.Services["demo"].Runtime
	wantDirectory := filepath.Join(runtimeRoot, "compose", "demo")
	if filepath.Dir(runtime.ControlledCompose) != wantDirectory || filepath.Dir(runtime.RuntimeCompose) != wantDirectory ||
		runtime.ControlledCompose == runtime.RuntimeCompose {
		t.Fatalf("Compose 隔离路径不正确: %+v", runtime)
	}
	for _, path := range []string{runtime.ControlledCompose, runtime.RuntimeCompose} {
		content, readErr := os.ReadFile(path)
		if readErr != nil || string(content) != string(seed) {
			t.Fatalf("Compose 副本 %s content=%q err=%v", path, content, readErr)
		}
	}
	if runtime.HealthURL != "http://127.0.0.1:8080/healthz" {
		t.Fatalf("Compose 健康地址=%s", runtime.HealthURL)
	}

	prefix := []string{
		"compose", "--project-name", "demo", "--project-directory", wantDirectory,
		"--env-file", runtime.EnvFile, "-f", runtime.RuntimeCompose,
	}
	if _, err := compose.Run(context.Background(), wantDirectory, append(prefix, "config", "--quiet")...); err != nil {
		t.Fatalf("config 命令被拒绝: %v", err)
	}
	rendered, err := compose.Run(context.Background(), wantDirectory, append(prefix, "config")...)
	if err != nil || !strings.Contains(rendered, "example.test/app@sha256:") {
		t.Fatalf("config output=%q err=%v", rendered, err)
	}
	output, err := compose.Run(context.Background(), wantDirectory, append(prefix, "ps", "-q", "db")...)
	if err != nil || output != "development-db\n" {
		t.Fatalf("ps output=%q err=%v", output, err)
	}
	if _, err := compose.Run(context.Background(), wantDirectory,
		append(prefix, "up", "-d", "--no-deps", "--force-recreate", "app")...); err != nil {
		t.Fatalf("up 命令被拒绝: %v", err)
	}
	container, err := compose.Run(context.Background(), wantDirectory,
		"inspect", "--format", `{{.Id}}\t{{.Name}}\t{{.Config.Image}}\t{{.Image}}`, "development-app")
	if err != nil || !strings.Contains(container, "\t/demo-app\texample.test/app@sha256:") {
		t.Fatalf("container identity=%q err=%v", container, err)
	}
	if _, err := compose.Run(context.Background(), wantDirectory, append(prefix, "down")...); err == nil {
		t.Fatal("任意 Compose 命令未被拒绝")
	}
	outside := filepath.Join(runtimeRoot, "outside.yml")
	if err := os.WriteFile(outside, seed, 0o600); err != nil {
		t.Fatal(err)
	}
	escaped := append([]string(nil), prefix...)
	escaped[8] = outside
	if _, err := compose.Run(context.Background(), wantDirectory, append(escaped, "config", "--quiet")...); err == nil {
		t.Fatal("隔离根外 Compose 文件未被拒绝")
	}
	if err := os.Remove(runtime.RuntimeCompose); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, runtime.RuntimeCompose); err != nil {
		t.Fatal(err)
	}
	if _, err := compose.Run(context.Background(), wantDirectory, append(prefix, "config", "--quiet")...); err == nil {
		t.Fatal("Compose 软链接未被拒绝")
	}
}

func TestApplyDevelopmentFeatureOverridesIsOptInAndScoped(t *testing.T) {
	runtimeRoot := t.TempDir()
	catalog := &config.Catalog{
		Terminal:     &config.TerminalPolicy{},
		Files:        &config.FilePolicy{},
		Extensions:   &config.ExtensionPolicy{},
		RunnerUpdate: &config.RunnerUpdatePolicy{RunnerID: "runner-a"},
		Services: map[string]model.ServiceDefinition{
			"demo": {Name: "demo", Actions: map[string]model.ActionDefinition{
				"restart": {Name: "restart", ObservationSeconds: 300},
			}},
		},
		Fleet: &config.FleetPolicy{Inventory: model.Fleet{Runners: []model.RunnerNode{
			{ID: "runner-a", Capabilities: []string{"inspect"}},
		}}},
	}
	applyDevelopmentFeatureOverrides(catalog)
	if catalog.Terminal.Enabled || catalog.Files.Enabled || catalog.Extensions.Enabled || catalog.RunnerUpdate.Enabled {
		t.Fatal("development features changed without explicit opt-in")
	}
	t.Setenv("OPS_DEV_ENABLE_FEATURES", "all")
	t.Setenv("OPS_DEV_RUNTIME_ROOT", runtimeRoot)
	applyDevelopmentFeatureOverrides(catalog)
	if !catalog.Terminal.Enabled || catalog.Terminal.Commands["service-status"].Executable != "/bin/echo" {
		t.Fatalf("terminal override=%+v", catalog.Terminal)
	}
	managedRoot := filepath.Join(runtimeRoot, "managed-files")
	if !catalog.Files.Enabled || catalog.Files.ReadOnly || catalog.Files.Roots["ops-config"] != managedRoot {
		t.Fatalf("files override=%+v", catalog.Files)
	}
	if _, err := os.Stat(filepath.Join(managedRoot, "managed-file.txt")); err != nil {
		t.Fatalf("development managed file missing: %v", err)
	}
	if !catalog.Extensions.Enabled || !catalog.RunnerUpdate.Enabled || !catalog.RunnerUpdate.FleetEnabled {
		t.Fatalf("policy overrides extensions=%+v runner=%+v", catalog.Extensions, catalog.RunnerUpdate)
	}
	if catalog.Extensions.Sandbox != "wasm" || !catalog.Extensions.RequireSignature ||
		catalog.Extensions.MaxPackageBytes == 0 || catalog.Extensions.MaxInputBytes == 0 ||
		catalog.Extensions.MaxOutputBytes == 0 || catalog.Extensions.MaxExecutionSeconds == 0 ||
		catalog.Extensions.MaxMemoryPages == 0 {
		t.Fatalf("extension execution limits were not initialized: %+v", catalog.Extensions)
	}
	if !containsString(catalog.Extensions.TrustedPublishers, developmentPublisher) ||
		catalog.Extensions.TrustedPublisherKeys[developmentPublisher] != developmentPublicKey {
		t.Fatalf("development extension publisher was not configured: %+v", catalog.Extensions)
	}
	if catalog.RunnerUpdate.ArtifactRoot != filepath.Join(runtimeRoot, "runner-updates", "incoming") {
		t.Fatalf("runner artifact root=%s", catalog.RunnerUpdate.ArtifactRoot)
	}
	if catalog.RunnerUpdate.Publisher != developmentPublisher ||
		catalog.RunnerUpdate.TrustedPublisherKeys[developmentPublisher] != developmentPublicKey {
		t.Fatalf("development runner publisher was not configured: %+v", catalog.RunnerUpdate)
	}
	if info, err := os.Lstat(catalog.RunnerUpdate.BinaryPath); err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o700 {
		t.Fatalf("development runner binary info=%+v err=%v", info, err)
	}
	if info, err := os.Lstat(filepath.Join(runtimeRoot, "bin", "kubectl")); err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o700 {
		t.Fatalf("development kubectl info=%+v err=%v", info, err)
	}
	node := catalog.Fleet.Inventory.Runners[0]
	if !containsString(node.Capabilities, "runner-update") ||
		node.IdentityPayloadVersion != runner.RunnerIdentityPayloadVersion ||
		node.Revision == "" || node.BinaryDigest == "" || node.CertificateFingerprint == "" {
		t.Fatalf("fleet runner development identity=%+v", node)
	}
	if catalog.Terminal.BreakGlass {
		t.Fatal("break-glass must remain opt-in")
	}
	if catalog.Terminal.MaxSessionSeconds != 10 {
		t.Fatalf("development terminal expiry=%d", catalog.Terminal.MaxSessionSeconds)
	}
	if catalog.Services["demo"].Actions["restart"].ObservationSeconds != 1 {
		t.Fatalf("development observation window=%d", catalog.Services["demo"].Actions["restart"].ObservationSeconds)
	}
	t.Setenv("OPS_DEV_ENABLE_BREAK_GLASS", "1")
	applyDevelopmentFeatureOverrides(catalog)
	if !catalog.Terminal.BreakGlass || catalog.Terminal.ShellWorkingDir != filepath.Join(runtimeRoot, "shell") {
		t.Fatalf("break-glass override=%+v", catalog.Terminal)
	}
}

func TestApplyDevelopmentAccessOverrideIsExplicit(t *testing.T) {
	catalog := &config.Catalog{Access: &config.AccessPolicy{
		DefaultTenant: "production",
		Principals:    map[string]config.AccessPrincipal{},
		Roles: map[string]model.Role{
			"platform-admin": {ID: "platform-admin", Permissions: []model.Permission{"*"}},
		},
	}}
	if err := applyDevelopmentAccessOverride(catalog); err != nil {
		t.Fatal(err)
	}
	if len(catalog.Access.Principals) != 0 {
		t.Fatal("development admin changed without explicit opt-in")
	}
	t.Setenv("OPS_DEV_ADMIN_EMAIL", "Admin@Example.Test")
	if err := applyDevelopmentAccessOverride(catalog); err != nil {
		t.Fatal(err)
	}
	hash := config.AccessHashForEmail("admin@example.test")
	principal := catalog.Access.Principals[hash]
	if principal.Email != "admin@example.test" || principal.TenantID != "production" ||
		len(principal.Roles) != 1 || principal.Roles[0] != "platform-admin" {
		t.Fatalf("principal=%+v", principal)
	}
	if err := applyDevelopmentAccessOverride(catalog); err != nil {
		t.Fatal(err)
	}
	principal = catalog.Access.Principals[hash]
	if len(principal.Roles) != 1 {
		t.Fatalf("repeat principal=%+v", principal)
	}

	t.Setenv("OPS_DEV_ADMIN_EMAILS", "Approver.One@Example.Test, approver.two@example.test,admin@example.test")
	if err := applyDevelopmentAccessOverride(catalog); err != nil {
		t.Fatal(err)
	}
	if len(catalog.Access.Principals) != 3 {
		t.Fatalf("development principals=%d want 3", len(catalog.Access.Principals))
	}
	for _, email := range []string{"approver.one@example.test", "approver.two@example.test"} {
		principal := catalog.Access.Principals[config.AccessHashForEmail(email)]
		if principal.Email != email || !containsString(principal.Roles, "platform-admin") {
			t.Fatalf("multi-admin principal=%+v", principal)
		}
	}
}
