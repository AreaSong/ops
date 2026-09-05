package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/config"
	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	"github.com/AreaSong/ops/services/areasong-ops/internal/runner"
	"github.com/AreaSong/ops/services/areasong-ops/internal/store"
	"github.com/AreaSong/ops/services/areasong-ops/internal/updater"
	"gopkg.in/yaml.v3"
)

type demoExecutor struct {
	mu            sync.Mutex
	versions      map[string]string
	appStates     map[string]string
	trafficStates map[string]string
	backupRoot    string
}

type demoAlertmanager struct {
	mode string
}

type demoCredentialRotator struct{}

type demoComposeCommandRunner struct {
	root     string
	services map[string]developmentComposeService
}

type developmentComposeService struct {
	applicationService   string
	applicationContainer string
	dependencyContainers map[string]string
}

// developmentRunnerUpdateLauncher executes the real updater state machine
// against disposable local state. It is wired only by cmd/dev-runner and is
// never reachable from the production entrypoint.
type developmentRunnerUpdateLauncher struct {
	database   *store.Store
	stateRoot  string
	socketPath string
}

type developmentRunnerController struct {
	socketPath   string
	identityPath string
}

type developmentRunnerIdentity struct {
	Version  string `json:"version"`
	Revision string `json:"revision"`
}

func (launcher developmentRunnerUpdateLauncher) Launch(
	ctx context.Context,
	policy config.RunnerUpdatePolicy,
	update model.RunnerUpdate,
) error {
	controller := developmentRunnerController{
		socketPath:   launcher.socketPath,
		identityPath: update.BinaryPath + ".identity",
	}
	executor := updater.Executor{
		Store: launcher.database, StateRoot: launcher.stateRoot, Controller: controller,
	}
	_, err := executor.Run(ctx, update.ID)
	return err
}

func (controller developmentRunnerController) SetIdentity(version, revision string) error {
	if version == "" || revision == "" || controller.identityPath == "" {
		return errors.New("development Runner identity 不完整")
	}
	payload, err := json.Marshal(developmentRunnerIdentity{Version: version, Revision: revision})
	if err != nil {
		return err
	}
	temporary := controller.identityPath + ".tmp"
	if err := os.WriteFile(temporary, payload, 0o600); err != nil {
		return err
	}
	if err := os.Rename(temporary, controller.identityPath); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}

func (controller developmentRunnerController) Restart(context.Context, string) error {
	// The disposable dev Runner stays alive. The updater has already replaced
	// the staged binary and SetIdentity models the new process identity.
	return nil
}

func (controller developmentRunnerController) WaitIdentity(
	ctx context.Context,
	_, _, version, revision string,
	timeout time.Duration,
) error {
	if controller.socketPath == "" || timeout <= 0 {
		return errors.New("development Runner health 参数不完整")
	}
	waitContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "unix", controller.socketPath)
		},
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	var lastErr error
	for {
		if err := controller.checkIdentity(waitContext, client, version, revision); err == nil {
			return nil
		} else {
			lastErr = err
		}
		select {
		case <-waitContext.Done():
			return fmt.Errorf("development Runner 健康与身份核验超时: %w", lastErr)
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func (controller developmentRunnerController) checkIdentity(
	ctx context.Context, client *http.Client, version, revision string,
) error {
	identityFile, err := os.Open(controller.identityPath)
	if err != nil {
		return err
	}
	defer identityFile.Close()
	var identity developmentRunnerIdentity
	if err := json.NewDecoder(io.LimitReader(identityFile, 4<<10)).Decode(&identity); err != nil {
		return err
	}
	if identity.Version != version || identity.Revision != revision {
		return fmt.Errorf("development Runner identity 不匹配: %s@%s", identity.Version, identity.Revision)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://runner/healthz", nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("development Runner health 返回 %d", response.StatusCode)
	}
	var health struct {
		OK        bool   `json:"ok"`
		Component string `json:"component"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 4<<10)).Decode(&health); err != nil {
		return err
	}
	if !health.OK || health.Component != "runner" {
		return errors.New("development Runner health 未通过")
	}
	return nil
}

const (
	developmentPublisher = "AreaSong Development"
	// RFC 8032 test-vector public key. It is public test material and is only
	// injected by the opt-in development entrypoint.
	developmentPublicKey = "11qYAYKxCrfVS/7TyWQHOg7hcvPapiMlrwIaaPcHURo="
)

func (demoCredentialRotator) Current(context.Context) (runner.CurrentCredential, error) {
	return runner.CurrentCredential{
		Configured: true, Fingerprint: "sha256:development", ExpiresAt: "2027-08-12",
	}, nil
}

func (demoCredentialRotator) Rotate(
	context.Context, string, string, string,
) (model.CredentialRotationResult, error) {
	return model.CredentialRotationResult{
		State:            model.CredentialRotationSwitchedPendingRevocation,
		ValidationResult: "开发验证通过", Outcome: "开发新凭据已切换；等待撤销旧凭据",
		RollbackResult: "开发回滚副本已保留",
	}, nil
}

func (demoCredentialRotator) VerifyRevoked(context.Context, model.CredentialRotation) error {
	return nil
}
func (demoCredentialRotator) RemoveRollback(context.Context, model.CredentialRotation) error {
	return nil
}

func (manager demoAlertmanager) ListAlerts(context.Context, bool) ([]model.ActiveAlert, error) {
	switch manager.mode {
	case "sample":
		now := time.Now().UTC()
		return []model.ActiveAlert{
			{
				Fingerprint: "abcdef1234567890", AlertName: "BusinessHttp5xxHigh", Severity: "critical",
				Summary:    "AreaForge 五分钟错误率超过阈值，需要先诊断再执行变更",
				GrafanaURL: "https://grafana.areasong.top/alerting/list",
				Labels:     map[string]string{"alertname": "BusinessHttp5xxHigh", "service": "areaforge"},
				StartsAt:   now.Add(-20 * time.Minute),
			},
			{
				Fingerprint: "1234567890abcdef", AlertName: "AppHttpProbeFailed", Severity: "warning",
				Summary:  "Sub2API 应用健康探针失败",
				Labels:   map[string]string{"alertname": "AppHttpProbeFailed", "service": "sub2api"},
				StartsAt: now.Add(-10 * time.Minute),
			},
		}, nil
	case "error":
		return nil, errors.New("development Alertmanager unavailable")
	default:
		return []model.ActiveAlert{}, nil
	}
}

func (demoAlertmanager) CreateSilence(
	_ context.Context,
	_ map[string]string,
	_ []string,
	_, endsAt time.Time,
	_ string,
) (model.MaintenanceSilence, error) {
	return model.MaintenanceSilence{ID: "development-silence", EndsAt: endsAt}, nil
}

func (demoAlertmanager) ExpireSilence(context.Context, string) error { return nil }

func (executor *demoExecutor) Execute(ctx context.Context, input runner.ExecuteInput) (model.AdapterResult, error) {
	select {
	case <-ctx.Done():
		return model.AdapterResult{}, ctx.Err()
	case <-time.After(180 * time.Millisecond):
	}
	executor.mu.Lock()
	defer executor.mu.Unlock()
	version := executor.versions[input.Service.Name]
	if input.Action == "inspect" {
		if input.AdapterKind == "traffic" {
			return model.AdapterResult{OK: true, Summary: "开发流量状态检查完成", Data: map[string]any{
				"trafficState": executor.trafficState(input.Service.Name),
			}}, nil
		}
		if input.Service.Metadata.Type == "automatic_task" {
			now := time.Now().UTC()
			return model.AdapterResult{OK: true, Summary: "开发自动任务状态检查完成", Data: map[string]any{
				"objectId": input.Service.ObjectID, "taskName": input.Service.Name,
				"scheduleSource": "cron", "enabled": true, "health": "healthy",
				"lastSuccessAt": now.Add(-15 * time.Second).Format(time.RFC3339), "ageSeconds": 15,
			}}, nil
		}
		return model.AdapterResult{OK: true, Summary: "开发状态检查完成", Data: map[string]any{
			"currentVersion":      version,
			"currentImage":        input.Service.Name + ":v" + version + "@sha256:demo",
			"currentImageId":      "sha256:" + strings.Repeat("a", 64),
			"runtimeIdentityHash": "sha256:" + strings.Repeat("b", 64),
			"gitCommit":           strings.Repeat("c", 40), "appState": executor.appState(input.Service.Name),
			"postgresState": "healthy", "redisState": "healthy", "migrations": 237,
		}}, nil
	}
	if input.Action == "check" && input.Phase == "discover" {
		latest := "v1.2.0"
		data := map[string]any{
			"currentVersion": version, "latestTag": latest,
			"updateAvailable": version != "1.2.0", "prepared": true,
		}
		if input.Service.Name == "sub2api" {
			data = map[string]any{"currentVersion": version, "latestTag": "v0.1.170", "updateAvailable": true, "prepared": false}
		}
		return model.AdapterResult{OK: true, Summary: "最新发布检查完成", Data: data}, nil
	}
	if input.Action == "update" && input.Phase == "apply" {
		executor.versions[input.Service.Name] = strings.TrimPrefix(input.Target, "v")
	}
	var recoveryPoint *model.RecoveryPointEvidence
	if input.Phase == "backup" && input.Service.RecoveryPointPolicy != nil {
		point, pointErr := executor.createRecoveryPoint(input)
		if pointErr != nil {
			return model.AdapterResult{}, pointErr
		}
		recoveryPoint = point
	}
	executor.applyLifecycleState(input)
	data := map[string]any{"service": input.Service.Name, "phase": input.Phase}
	if input.AdapterKind == "traffic" {
		data["trafficState"] = executor.trafficState(input.Service.Name)
	}
	return model.AdapterResult{
		OK: true,
		Summary: map[string]string{
			"preflight": "前置身份与策略核验通过", "backup": "新鲜备份完成",
			"apply": "目标版本已应用", "health": "健康检查通过",
			"smoke": "业务只读抽测通过", "identity": "三方身份一致",
			"drill": "隔离恢复演练完成", "verify": "结果验证通过",
			"drain": "现有连接已排空", "enter-maintenance": "维护屏障已启用",
			"start": "应用已启动", "stop": "应用已停止", "resume-traffic": "公网流量已恢复",
			"restart": "仅应用容器已重建", "rollback": "更新前应用身份已恢复",
		}[input.Phase],
		Data: data, RecoveryPoint: recoveryPoint,
	}, nil
}

func (executor *demoExecutor) createRecoveryPoint(input runner.ExecuteInput) (*model.RecoveryPointEvidence, error) {
	if executor.backupRoot == "" || input.OperationDir == "" || input.Service.RecoveryPointPolicy == nil {
		return nil, errors.New("开发恢复点上下文不完整")
	}
	taskID := filepath.Base(filepath.Clean(input.OperationDir))
	if !validDevelopmentComposeName(taskID) || !validDevelopmentComposeName(input.Service.Name) {
		return nil, errors.New("开发恢复点任务标识无效")
	}
	directory := filepath.Join(executor.backupRoot, input.Service.Name, taskID)
	if err := ensureDevelopmentDirectory(directory); err != nil {
		return nil, err
	}
	artifacts := make([]model.RecoveryArtifact, 0, len(input.Service.RecoveryPointPolicy.RequiredArtifactRoles))
	for _, role := range input.Service.RecoveryPointPolicy.RequiredArtifactRoles {
		if !validDevelopmentComposeName(role) {
			return nil, fmt.Errorf("开发恢复点角色无效: %s", role)
		}
		path := filepath.Join(directory, role+".artifact")
		content := []byte(strings.Join([]string{"AreaSong Ops development recovery artifact", taskID, role, ""}, "\n"))
		if err := writeDevelopmentFile(path, content, 0o600); err != nil {
			return nil, err
		}
		digest := sha256.Sum256(content)
		artifacts = append(artifacts, model.RecoveryArtifact{
			Role: role, Path: path, SizeBytes: int64(len(content)), SHA256: "sha256:" + hex.EncodeToString(digest[:]),
		})
	}
	return &model.RecoveryPointEvidence{
		SchemaVersion: 1, Service: input.Service.Name, TaskID: taskID,
		CreatedAt: time.Now().UTC(), Artifacts: artifacts,
	}, nil
}

func (executor *demoExecutor) appState(service string) string {
	if state := executor.appStates[service]; state != "" {
		return state
	}
	return "running"
}

func (executor *demoExecutor) trafficState(service string) string {
	if state := executor.trafficStates[service]; state != "" {
		return state
	}
	return "running"
}

func (executor *demoExecutor) applyLifecycleState(input runner.ExecuteInput) {
	service := input.Service.Name
	if input.AdapterKind == "traffic" {
		if executor.trafficStates == nil {
			executor.trafficStates = make(map[string]string)
		}
		switch {
		case input.Action == "drain" && input.Phase == "drain":
			executor.trafficStates[service] = "drained"
		case input.Action == "enter-maintenance" && input.Phase == "enter-maintenance":
			executor.trafficStates[service] = "maintenance"
		case input.Action == "resume-traffic" && input.Phase == "resume-traffic":
			executor.trafficStates[service] = "running"
		}
		return
	}
	if executor.appStates == nil {
		executor.appStates = make(map[string]string)
	}
	switch {
	case input.Action == "stop" && input.Phase == "stop":
		executor.appStates[service] = "stopped"
	case input.Action == "start" && input.Phase == "start":
		executor.appStates[service] = "running"
	}
}

func main() {
	catalogPath := envOr("OPS_SERVICE_CATALOG", "config/services.example.json")
	catalog, err := config.Load(catalogPath, false)
	if err != nil {
		log.Fatal(err)
	}
	applyDevelopmentPathOverrides(catalog)
	stateRoot, err := os.MkdirTemp("/tmp", "areasong-ops-dev-state-")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(stateRoot)
	applyDevelopmentFeatureOverrides(catalog)
	if err := applyDevelopmentAccessOverride(catalog); err != nil {
		log.Fatal(err)
	}
	composeRunner, err := prepareDevelopmentCompose(catalog)
	if err != nil {
		log.Fatal(err)
	}
	database, err := store.Open(filepath.Join(stateRoot, "ops.db"))
	if err != nil {
		log.Fatal(err)
	}
	defer database.Close()
	executor := &demoExecutor{
		versions:      map[string]string{"areaforge": "1.1.1", "sub2api": "0.1.168"},
		appStates:     map[string]string{"areaforge": "running", "sub2api": "running"},
		trafficStates: map[string]string{"areaforge": "running", "sub2api": "running"},
		backupRoot:    filepath.Join(stateRoot, "backups"),
	}
	if err := ensureDevelopmentDirectory(executor.backupRoot); err != nil {
		log.Fatal(err)
	}
	socket := envOr("OPS_RUNNER_SOCKET", "/tmp/areasong-ops-dev.sock")
	engineOptions := []runner.EngineOption{
		runner.WithAlertmanager(demoAlertmanager{mode: os.Getenv("OPS_DEV_ALERTS")}),
		runner.WithCredentialRotator(demoCredentialRotator{}),
		runner.WithBackupRoot(executor.backupRoot),
	}
	if developmentFeatures(os.Getenv("OPS_DEV_ENABLE_FEATURES"))["acceptance-timers"] {
		engineOptions = append(engineOptions, runner.WithLifecycleObservationSeconds(1))
	}
	if catalog.RunnerUpdate != nil && catalog.RunnerUpdate.Enabled {
		engineOptions = append(engineOptions, runner.WithRunnerUpdateLauncher(
			developmentRunnerUpdateLauncher{database: database, stateRoot: stateRoot, socketPath: socket},
		))
	}
	if composeRunner != nil {
		engineOptions = append(engineOptions, runner.WithComposeRunner(composeRunner))
	}
	engine, err := runner.NewEngineChecked(catalog, database, executor, stateRoot, engineOptions...)
	if err != nil {
		log.Fatal(err)
	}
	// 开发 Runner 没有独立心跳进程；持续续租用于本地验收 Fleet 门禁，生产 API 门禁不变。
	if catalog.Fleet != nil && catalog.Fleet.Enabled {
		lease := time.Duration(catalog.Fleet.HeartbeatTimeoutSeconds) * time.Second
		if lease <= 0 {
			lease = 90 * time.Second
		}
		for _, node := range catalog.Fleet.Inventory.Runners {
			if _, err := database.HeartbeatRunner(context.Background(), node.ID, "development", lease); err != nil {
				log.Fatal(err)
			}
		}
		heartbeatStop := make(chan struct{})
		heartbeatDone := make(chan struct{})
		go func() {
			defer close(heartbeatDone)
			ticker := time.NewTicker(30 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					for _, node := range catalog.Fleet.Inventory.Runners {
						if _, err := database.HeartbeatRunner(context.Background(), node.ID, "development", lease); err != nil {
							log.Printf("development Fleet heartbeat failed: %v", err)
						}
					}
				case <-heartbeatStop:
					return
				}
			}
		}()
		defer func() {
			close(heartbeatStop)
			<-heartbeatDone
		}()
	}
	listenAddr := strings.TrimSpace(os.Getenv("OPS_DEV_LISTEN_ADDR"))
	listenerNetwork, listenerAddress := "unix", socket
	if listenAddr != "" {
		listenerNetwork, listenerAddress = "tcp", listenAddr
	}
	listener, err := developmentListener(listenerNetwork, listenerAddress)
	if err != nil {
		log.Fatal(err)
	}
	if listenerNetwork == "unix" {
		defer os.Remove(socket)
	}
	server := &http.Server{Handler: runner.NewServer(engine, database), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("serve: %v", err)
		}
	}()
	log.Printf("development runner listening on %s://%s", listenerNetwork, listenerAddress)
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	_ = server.Shutdown(context.Background())
	engine.Wait()
}

func developmentListener(network, address string) (net.Listener, error) {
	if network == "unix" {
		_ = os.Remove(address)
	}
	listener, err := net.Listen(network, address)
	if err != nil {
		return nil, err
	}
	if network != "unix" {
		return listener, nil
	}
	// net.Listen 使用进程 umask 创建 Socket，通常会得到 0755。开发 Web
	// 以非 root 身份运行，因此必须在每次替换旧 Socket 后重新收紧并开放组写。
	if err := os.Chmod(address, 0o660); err != nil {
		_ = listener.Close()
		_ = os.Remove(address)
		return nil, fmt.Errorf("设置开发 Runner Socket 权限失败: %w", err)
	}
	return listener, nil
}

// Development catalogs intentionally keep production-shaped absolute paths.
// Remapping is opt-in and local to this demo entrypoint so production config
// loading never silently changes a declared target.
func applyDevelopmentPathOverrides(catalog *config.Catalog) {
	repoRoot := strings.TrimRight(os.Getenv("OPS_DEV_REPO_ROOT"), "/")
	runtimeRoot := strings.TrimRight(os.Getenv("OPS_DEV_RUNTIME_ROOT"), "/")
	if repoRoot == "" && runtimeRoot == "" {
		return
	}
	rewrite := func(value string) string {
		if repoRoot != "" && strings.HasPrefix(value, "/opt/ops/") {
			return repoRoot + strings.TrimPrefix(value, "/opt/ops")
		}
		if runtimeRoot != "" && strings.HasPrefix(value, "/opt/services/") {
			return runtimeRoot + strings.TrimPrefix(value, "/opt/services")
		}
		return value
	}
	for name, service := range catalog.Services {
		if service.Runtime != nil {
			service.Runtime.ControlledCompose = rewrite(service.Runtime.ControlledCompose)
			service.Runtime.RuntimeCompose = rewrite(service.Runtime.RuntimeCompose)
			service.Runtime.EnvFile = rewrite(service.Runtime.EnvFile)
			service.Runtime.ReleaseCatalog = rewrite(service.Runtime.ReleaseCatalog)
			service.Runtime.PreparedReleaseDir = rewrite(service.Runtime.PreparedReleaseDir)
			service.Runtime.InspectExecutable = rewrite(service.Runtime.InspectExecutable)
			service.Runtime.BackupEvidenceExecutable = rewrite(service.Runtime.BackupEvidenceExecutable)
			service.Runtime.RestoreDrillExecutable = rewrite(service.Runtime.RestoreDrillExecutable)
			service.Runtime.RestoreExecutable = rewrite(service.Runtime.RestoreExecutable)
			service.Runtime.PrepareExecutable = rewrite(service.Runtime.PrepareExecutable)
			service.Runtime.UpdateExecutable = rewrite(service.Runtime.UpdateExecutable)
			for index, path := range service.Runtime.BackupExecutables {
				service.Runtime.BackupExecutables[index] = rewrite(path)
			}
		}
		catalog.Services[name] = service
	}
	if catalog.Files != nil {
		for name, root := range catalog.Files.Roots {
			catalog.Files.Roots[name] = rewrite(root)
		}
	}
}

// prepareDevelopmentCompose creates two independent files under the disposable
// runtime root. The repository copy remains an immutable seed for local tests.
func prepareDevelopmentCompose(catalog *config.Catalog) (*demoComposeCommandRunner, error) {
	features := developmentFeatures(os.Getenv("OPS_DEV_ENABLE_FEATURES"))
	if !features["compose"] {
		return nil, nil
	}
	runtimeRoot := strings.TrimSpace(os.Getenv("OPS_DEV_RUNTIME_ROOT"))
	if runtimeRoot == "" || !filepath.IsAbs(runtimeRoot) {
		return nil, errors.New("开发 Compose 验收需要绝对 OPS_DEV_RUNTIME_ROOT")
	}
	composeRoot := filepath.Join(filepath.Clean(runtimeRoot), "compose")
	if err := ensureDevelopmentDirectory(composeRoot); err != nil {
		return nil, err
	}
	configured := 0
	services := make(map[string]developmentComposeService)
	for name, service := range catalog.Services {
		if service.Runtime == nil || service.Runtime.ControlledCompose == "" ||
			service.Runtime.RuntimeCompose == "" {
			continue
		}
		if !validDevelopmentComposeName(name) {
			return nil, fmt.Errorf("开发 Compose 服务名称无效: %s", name)
		}
		baseline, err := readDevelopmentComposeSeed(service.Runtime.ControlledCompose)
		if err != nil {
			return nil, fmt.Errorf("读取 %s Compose 基线失败: %w", name, err)
		}
		directory := filepath.Join(composeRoot, name)
		if err := ensureDevelopmentDirectory(directory); err != nil {
			return nil, err
		}
		controlledPath := filepath.Join(directory, "controlled.yml")
		runtimePath := filepath.Join(directory, "runtime.yml")
		envPath := filepath.Join(directory, ".env")
		for _, path := range []string{controlledPath, runtimePath} {
			if err := writeDevelopmentFile(path, baseline, 0o600); err != nil {
				return nil, err
			}
		}
		env := []byte("POSTGRES_PASSWORD=development-only\n" +
			"SECURITY_URL_ALLOWLIST_UPSTREAM_HOSTS=example.test\n")
		if err := writeDevelopmentFile(envPath, env, 0o600); err != nil {
			return nil, err
		}
		service.Runtime.ControlledCompose = controlledPath
		service.Runtime.RuntimeCompose = runtimePath
		service.Runtime.EnvFile = envPath
		service.Runtime.HealthURL = "http://127.0.0.1:8080/healthz"
		catalog.Services[name] = service
		if len(service.Runtime.DependencyServices) != len(service.Runtime.DependencyContainers) {
			return nil, fmt.Errorf("开发 Compose 服务 %s 的依赖身份映射不完整", name)
		}
		dependencies := make(map[string]string, len(service.Runtime.DependencyServices))
		for index, dependency := range service.Runtime.DependencyServices {
			dependencies[dependency] = service.Runtime.DependencyContainers[index]
		}
		services[directory] = developmentComposeService{
			applicationService: service.Runtime.ApplicationService, applicationContainer: service.Runtime.ApplicationContainer,
			dependencyContainers: dependencies,
		}
		configured++
	}
	if configured == 0 {
		return nil, errors.New("开发 Compose 验收未发现受管服务")
	}
	return &demoComposeCommandRunner{root: composeRoot, services: services}, nil
}

func (compose demoComposeCommandRunner) Run(
	ctx context.Context, projectDirectory string, args ...string,
) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	directory := filepath.Clean(projectDirectory)
	if !pathWithin(compose.root, directory) || directory == filepath.Clean(compose.root) {
		return "", errors.New("开发 Compose 项目目录超出隔离根")
	}
	policy, ok := compose.services[directory]
	if !ok {
		return "", errors.New("开发 Compose 服务未登记")
	}
	if len(args) > 0 && args[0] != "compose" {
		return compose.runDevelopmentInspect(directory, policy, args)
	}
	if len(args) < 10 || args[0] != "compose" || args[1] != "--project-name" ||
		args[2] == "" || args[3] != "--project-directory" || filepath.Clean(args[4]) != directory ||
		args[5] != "--env-file" || args[7] != "-f" {
		return "", errors.New("开发 Compose 参数结构不受支持")
	}
	if err := validateDevelopmentComposeFile(directory, args[6]); err != nil {
		return "", fmt.Errorf("开发 Compose env 无效: %w", err)
	}
	if err := validateDevelopmentComposeFile(directory, args[8]); err != nil {
		return "", fmt.Errorf("开发 Compose 文件无效: %w", err)
	}
	command := args[9:]
	switch {
	case len(command) == 1 && command[0] == "config":
		return renderDevelopmentCompose(args[8], args[6])
	case len(command) == 2 && command[0] == "config" && command[1] == "--quiet":
		return "", nil
	case len(command) == 3 && command[0] == "ps" && command[1] == "-q" &&
		policy.allowsService(command[2]):
		return "development-" + command[2] + "\n", nil
	case len(command) == 5 && command[0] == "up" && command[1] == "-d" &&
		command[2] == "--no-deps" && command[3] == "--force-recreate" &&
		command[4] == policy.applicationService:
		return "", nil
	default:
		return "", errors.New("开发 Compose 命令不在白名单")
	}
}

func (policy developmentComposeService) allowsService(service string) bool {
	if service == policy.applicationService {
		return true
	}
	_, ok := policy.dependencyContainers[service]
	return ok
}

func (compose demoComposeCommandRunner) runDevelopmentInspect(
	directory string, policy developmentComposeService, args []string,
) (string, error) {
	if len(args) == 4 && args[0] == "inspect" && args[1] == "--format" {
		service := strings.TrimPrefix(args[3], "development-")
		if !policy.allowsService(service) || args[3] != "development-"+service {
			return "", errors.New("开发 Compose 容器身份不受信")
		}
		container := policy.dependencyContainers[service]
		if service == policy.applicationService {
			container = policy.applicationContainer
		}
		image, err := developmentComposeImage(filepath.Join(directory, "runtime.yml"), service)
		if err != nil {
			return "", err
		}
		imageID, _ := developmentImageIdentity(image)
		if strings.Contains(args[2], ".Config.Image") {
			return strings.Join([]string{args[3], "/" + container, image, imageID}, "\t") + "\n", nil
		}
		return strings.Join([]string{args[3], "/" + container, imageID}, "\t") + "\n", nil
	}
	if len(args) == 5 && args[0] == "image" && args[1] == "inspect" && args[2] == "--format" {
		return compose.developmentImageInspect(directory, policy, args[3], args[4])
	}
	return "", errors.New("开发 Docker 身份命令不在白名单")
}

func (compose demoComposeCommandRunner) developmentImageInspect(
	directory string, policy developmentComposeService, format, target string,
) (string, error) {
	image := target
	if strings.HasPrefix(target, "sha256:") {
		var err error
		image, err = developmentComposeImage(filepath.Join(directory, "runtime.yml"), policy.applicationService)
		if err != nil {
			return "", err
		}
	}
	imageID, repoDigest := developmentImageIdentity(image)
	if imageID == "" || repoDigest == "" || strings.HasPrefix(target, "sha256:") && target != imageID {
		return "", errors.New("开发 Compose 镜像身份不匹配")
	}
	repoDigests, _ := json.Marshal([]string{repoDigest})
	if strings.Contains(format, "{{.Id}}") {
		return imageID + "\t" + string(repoDigests) + "\n", nil
	}
	return string(repoDigests) + "\n", nil
}

func developmentComposeImage(path, service string) (string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var document struct {
		Services map[string]struct {
			Image string `yaml:"image"`
		} `yaml:"services"`
	}
	if err := yaml.Unmarshal(content, &document); err != nil {
		return "", err
	}
	image := strings.TrimSpace(document.Services[service].Image)
	if image == "" {
		return "", errors.New("开发 Compose 服务镜像缺失")
	}
	return image, nil
}

func developmentImageIdentity(image string) (string, string) {
	parts := strings.SplitN(strings.TrimSpace(image), "@sha256:", 2)
	if len(parts) != 2 || len(parts[1]) != 64 {
		return "", ""
	}
	repository := parts[0]
	lastSlash := strings.LastIndex(repository, "/")
	if colon := strings.LastIndex(repository, ":"); colon > lastSlash {
		repository = repository[:colon]
	}
	sum := sha256.Sum256([]byte(image))
	return "sha256:" + hex.EncodeToString(sum[:]), repository + "@sha256:" + parts[1]
}

func renderDevelopmentCompose(composePath, envPath string) (string, error) {
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

func ensureDevelopmentDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("开发目录身份无效: %s", path)
	}
	return os.Chmod(path, 0o700)
}

func readDevelopmentComposeSeed(path string) ([]byte, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("Compose 基线路径必须是绝对路径")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("Compose 基线必须是普通文件")
	}
	return os.ReadFile(path)
}

func writeDevelopmentFile(path string, content []byte, mode os.FileMode) error {
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("开发文件身份无效: %s", path)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.WriteFile(path, content, mode); err != nil {
		return err
	}
	return os.Chmod(path, mode)
}

func validateDevelopmentComposeFile(directory, path string) error {
	if !filepath.IsAbs(path) || !pathWithin(directory, path) {
		return errors.New("路径超出项目目录")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("路径不是普通文件")
	}
	return nil
}

func pathWithin(root, path string) bool {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator))
}

func validDevelopmentComposeName(value string) bool {
	if value == "" || len(value) > 63 {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || strings.ContainsRune("_.-", character) {
			continue
		}
		return false
	}
	return value[0] != '.' && value[0] != '-'
}

// 开发功能开关仅在 cmd/dev-runner 中显式生效，不改变生产目录或放宽生产校验门禁。
func applyDevelopmentFeatureOverrides(catalog *config.Catalog) {
	features := developmentFeatures(os.Getenv("OPS_DEV_ENABLE_FEATURES"))
	if len(features) == 0 {
		return
	}
	runtimeRoot := strings.TrimRight(os.Getenv("OPS_DEV_RUNTIME_ROOT"), "/")
	if runtimeRoot == "" {
		runtimeRoot = "/tmp/areasong-runtime"
	}
	configureDevelopmentTerminal(catalog, features, runtimeRoot)
	configureDevelopmentFiles(catalog, features, runtimeRoot)
	configureDevelopmentExtensions(catalog, features)
	configureDevelopmentRunnerUpdate(catalog, features, runtimeRoot)
	configureDevelopmentFleetRunnerUpdate(catalog, features)
	configureDevelopmentKubernetes(features, runtimeRoot)
	configureDevelopmentTimings(catalog, features)
}

func developmentFeatures(requested string) map[string]bool {
	features := map[string]bool{}
	for _, item := range strings.Split(requested, ",") {
		item = strings.TrimSpace(strings.ToLower(item))
		if item != "" {
			features[item] = true
		}
	}
	if features["all"] {
		for _, item := range []string{
			"compose", "terminal", "files", "extensions", "runner-update", "runner-fleet-update", "kubernetes",
			"acceptance-timers",
		} {
			features[item] = true
		}
	}
	if features["runner-fleet-update"] {
		features["runner-update"] = true
	}
	return features
}

func configureDevelopmentTimings(catalog *config.Catalog, features map[string]bool) {
	if !features["acceptance-timers"] {
		return
	}
	if catalog.Terminal != nil {
		catalog.Terminal.MaxSessionSeconds = 10
	}
	for name, service := range catalog.Services {
		for actionName, action := range service.Actions {
			if action.ObservationSeconds > 0 {
				action.ObservationSeconds = 1
				service.Actions[actionName] = action
			}
		}
		catalog.Services[name] = service
	}
}

func configureDevelopmentTerminal(catalog *config.Catalog, features map[string]bool, runtimeRoot string) {
	if features["terminal"] && catalog.Terminal != nil {
		catalog.Terminal.Enabled = true
		// 默认开发命令是确定性的只读命令；Break-glass Shell 仍需单独显式开启。
		catalog.Terminal.Commands = map[string]model.TerminalCommand{
			"service-status": {
				Name: "service-status", Executable: "/bin/echo",
				Arguments: []string{"development terminal ready"}, ReadOnly: true, TimeoutSeconds: 30,
			},
		}
		if features["break-glass"] || os.Getenv("OPS_DEV_ENABLE_BREAK_GLASS") == "1" {
			catalog.Terminal.BreakGlass = true
			catalog.Terminal.ShellExecutable = "/bin/bash"
			catalog.Terminal.ShellWorkingDir = filepath.Join(runtimeRoot, "shell")
			if err := os.MkdirAll(catalog.Terminal.ShellWorkingDir, 0o700); err != nil {
				log.Printf("development shell root unavailable: %v", err)
			}
		}
	}
}

func configureDevelopmentFiles(catalog *config.Catalog, features map[string]bool, runtimeRoot string) {
	if features["files"] && catalog.Files != nil {
		managedRoot := filepath.Join(runtimeRoot, "managed-files")
		catalog.Files.Enabled = true
		catalog.Files.ReadOnly = false
		catalog.Files.Roots = map[string]string{"ops-config": managedRoot}
		if err := os.MkdirAll(managedRoot, 0o700); err != nil {
			log.Printf("development file root unavailable: %v", err)
		} else {
			seedPath := filepath.Join(managedRoot, "managed-file.txt")
			if _, err := os.Lstat(seedPath); errors.Is(err, os.ErrNotExist) {
				if err := os.WriteFile(seedPath, []byte("AreaSong Ops local managed file\n"), 0o600); err != nil {
					log.Printf("development managed file unavailable: %v", err)
				}
			}
		}
	}
}

func configureDevelopmentExtensions(catalog *config.Catalog, features map[string]bool) {
	if features["extensions"] && catalog.Extensions != nil {
		catalog.Extensions.Enabled = true
		catalog.Extensions.Sandbox = "wasm"
		catalog.Extensions.RequireSignature = true
		if !containsString(catalog.Extensions.TrustedPublishers, developmentPublisher) {
			catalog.Extensions.TrustedPublishers = append(
				catalog.Extensions.TrustedPublishers, developmentPublisher,
			)
		}
		if catalog.Extensions.TrustedPublisherKeys == nil {
			catalog.Extensions.TrustedPublisherKeys = make(map[string]string)
		}
		catalog.Extensions.TrustedPublisherKeys[developmentPublisher] = developmentPublicKey
		// The example catalog is intentionally disabled and therefore has no
		// runtime defaults applied by config validation. Keep local all-features
		// mode usable while retaining conservative limits.
		if catalog.Extensions.MaxPackageBytes == 0 {
			catalog.Extensions.MaxPackageBytes = 1 << 20
		}
		if catalog.Extensions.MaxInputBytes == 0 {
			catalog.Extensions.MaxInputBytes = 64 << 10
		}
		if catalog.Extensions.MaxOutputBytes == 0 {
			catalog.Extensions.MaxOutputBytes = 64 << 10
		}
		if catalog.Extensions.MaxExecutionSeconds == 0 {
			catalog.Extensions.MaxExecutionSeconds = 15
		}
		if catalog.Extensions.MaxMemoryPages == 0 {
			catalog.Extensions.MaxMemoryPages = 256
		}
	}
}

func configureDevelopmentRunnerUpdate(catalog *config.Catalog, features map[string]bool, runtimeRoot string) {
	if features["runner-update"] && catalog.RunnerUpdate != nil {
		catalog.RunnerUpdate.Enabled = true
		catalog.RunnerUpdate.ArtifactRoot = filepath.Join(runtimeRoot, "runner-updates", "incoming")
		catalog.RunnerUpdate.BinaryPath = filepath.Join(runtimeRoot, "runner", "areasong-ops-runner")
		catalog.RunnerUpdate.Publisher = developmentPublisher
		if catalog.RunnerUpdate.TrustedPublisherKeys == nil {
			catalog.RunnerUpdate.TrustedPublisherKeys = make(map[string]string)
		}
		catalog.RunnerUpdate.TrustedPublisherKeys[developmentPublisher] = developmentPublicKey
		if err := os.MkdirAll(catalog.RunnerUpdate.ArtifactRoot, 0o700); err != nil {
			log.Printf("development runner update root unavailable: %v", err)
		}
		if err := ensureDevelopmentFile(
			catalog.RunnerUpdate.BinaryPath, []byte("#!/bin/sh\nprintf 'areasong-ops development runner\\n'\n"), 0o700,
		); err != nil {
			log.Printf("development runner binary unavailable: %v", err)
		}
	}
}

func configureDevelopmentFleetRunnerUpdate(catalog *config.Catalog, features map[string]bool) {
	if features["runner-fleet-update"] && catalog.RunnerUpdate != nil && catalog.Fleet != nil {
		catalog.RunnerUpdate.FleetEnabled = true
		catalog.RunnerUpdate.ManifestGOOS = config.RunnerUpdateManifestGOOS
		catalog.RunnerUpdate.ManifestGOARCH = config.RunnerUpdateManifestGOARCH
		for index := range catalog.Fleet.Inventory.Runners {
			node := &catalog.Fleet.Inventory.Runners[index]
			if node.ID != catalog.RunnerUpdate.RunnerID {
				continue
			}
			if !containsString(node.Capabilities, "runner-update") {
				node.Capabilities = append(node.Capabilities, "runner-update")
			}
			node.Version = "v1.0.3"
			node.Revision = strings.Repeat("a", 40)
			node.BinaryDigest = "sha256:" + strings.Repeat("b", 64)
			node.IdentityPayloadVersion = runner.RunnerIdentityPayloadVersion
			node.CertificateFingerprint = "sha256:" + strings.Repeat("c", 64)
		}
	}
}

func configureDevelopmentKubernetes(features map[string]bool, runtimeRoot string) {
	if !features["kubernetes"] {
		return
	}
	binDir := filepath.Join(runtimeRoot, "bin")
	kubectl := filepath.Join(binDir, "kubectl")
	content := []byte("#!/bin/sh\ncat >/dev/null\nprintf '{\"kind\":\"Status\",\"status\":\"Success\"}\\n'\n")
	if err := ensureDevelopmentFile(kubectl, content, 0o700); err != nil {
		log.Printf("development kubectl unavailable: %v", err)
		return
	}
	_ = os.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func ensureDevelopmentFile(path string, content []byte, mode os.FileMode) error {
	info, err := os.Lstat(path)
	if err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("development path is not a regular file: %s", path)
		}
		return os.Chmod(path, mode)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, content, mode)
}

func applyDevelopmentAccessOverride(catalog *config.Catalog) error {
	emails := developmentAdminEmails()
	if len(emails) == 0 {
		return nil
	}
	if catalog.Access == nil || catalog.Access.Roles["platform-admin"].ID == "" {
		return errors.New("开发管理员覆盖需要包含 platform-admin 的访问策略")
	}
	if catalog.Access.Principals == nil {
		catalog.Access.Principals = make(map[string]config.AccessPrincipal)
	}
	for _, email := range emails {
		hash := config.AccessHashForEmail(email)
		principal := catalog.Access.Principals[hash]
		principal.Email, principal.EmailHash, principal.Subject = email, hash, hash
		if principal.TenantID == "" {
			principal.TenantID = catalog.Access.DefaultTenant
		}
		if !containsString(principal.Roles, "platform-admin") {
			principal.Roles = append(principal.Roles, "platform-admin")
		}
		catalog.Access.Principals[hash] = principal
	}
	return nil
}

func developmentAdminEmails() []string {
	raw := append([]string{os.Getenv("OPS_DEV_ADMIN_EMAIL")}, strings.Split(os.Getenv("OPS_DEV_ADMIN_EMAILS"), ",")...)
	result := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, candidate := range raw {
		email := config.NormalizeAccessSubject(candidate)
		if email == "" {
			continue
		}
		if _, exists := seen[email]; exists {
			continue
		}
		seen[email] = struct{}{}
		result = append(result, email)
	}
	return result
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
