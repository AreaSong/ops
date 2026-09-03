package main

import (
	"context"
	"errors"
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
)

type demoExecutor struct {
	mu       sync.Mutex
	versions map[string]string
}

type demoAlertmanager struct {
	mode string
}

type demoCredentialRotator struct{}

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
			"gitCommit":           strings.Repeat("c", 40), "appState": "running",
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
	return model.AdapterResult{
		OK: true,
		Summary: map[string]string{
			"preflight": "前置身份与策略核验通过", "backup": "新鲜备份完成",
			"apply": "目标版本已应用", "health": "健康检查通过",
			"smoke": "业务只读抽测通过", "identity": "三方身份一致",
			"drill": "隔离恢复演练完成", "verify": "演练结果已验证",
			"restart": "仅应用容器已重建", "rollback": "更新前应用身份已恢复",
		}[input.Phase],
		Data: map[string]any{"service": input.Service.Name, "phase": input.Phase},
	}, nil
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
	database, err := store.Open(filepath.Join(stateRoot, "ops.db"))
	if err != nil {
		log.Fatal(err)
	}
	defer database.Close()
	executor := &demoExecutor{versions: map[string]string{"areaforge": "1.1.1", "sub2api": "0.1.168"}}
	engine, err := runner.NewEngineChecked(catalog, database, executor, stateRoot,
		runner.WithAlertmanager(demoAlertmanager{mode: os.Getenv("OPS_DEV_ALERTS")}),
		runner.WithCredentialRotator(demoCredentialRotator{}))
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
	socket := envOr("OPS_RUNNER_SOCKET", "/tmp/areasong-ops-dev.sock")
	listenAddr := strings.TrimSpace(os.Getenv("OPS_DEV_LISTEN_ADDR"))
	listenerNetwork, listenerAddress := "unix", socket
	if listenAddr != "" {
		listenerNetwork, listenerAddress = "tcp", listenAddr
	} else {
		_ = os.Remove(socket)
	}
	listener, err := net.Listen(listenerNetwork, listenerAddress)
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

// 开发功能开关仅在 cmd/dev-runner 中显式生效，不改变生产目录或放宽生产校验门禁。
func applyDevelopmentFeatureOverrides(catalog *config.Catalog) {
	requested := strings.TrimSpace(os.Getenv("OPS_DEV_ENABLE_FEATURES"))
	if requested == "" {
		return
	}
	features := map[string]bool{}
	for _, item := range strings.Split(requested, ",") {
		item = strings.TrimSpace(strings.ToLower(item))
		if item != "" {
			features[item] = true
		}
	}
	if features["all"] {
		for _, item := range []string{"terminal", "files", "extensions", "runner-update", "runner-fleet-update"} {
			features[item] = true
		}
	}
	if features["runner-fleet-update"] {
		features["runner-update"] = true
	}
	runtimeRoot := strings.TrimRight(os.Getenv("OPS_DEV_RUNTIME_ROOT"), "/")
	if runtimeRoot == "" {
		runtimeRoot = "/tmp/areasong-runtime"
	}
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
	if features["files"] && catalog.Files != nil {
		catalog.Files.Enabled = true
		catalog.Files.Roots = map[string]string{"ops-config": runtimeRoot}
		if err := os.MkdirAll(runtimeRoot, 0o700); err != nil {
			log.Printf("development file root unavailable: %v", err)
		}
	}
	if features["extensions"] && catalog.Extensions != nil {
		catalog.Extensions.Enabled = true
		catalog.Extensions.Sandbox = "wasm"
		catalog.Extensions.RequireSignature = true
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
	if features["runner-update"] && catalog.RunnerUpdate != nil {
		catalog.RunnerUpdate.Enabled = true
		catalog.RunnerUpdate.ArtifactRoot = filepath.Join(runtimeRoot, "runner-updates", "incoming")
		catalog.RunnerUpdate.BinaryPath = filepath.Join(runtimeRoot, "runner", "areasong-ops-runner")
		if err := os.MkdirAll(catalog.RunnerUpdate.ArtifactRoot, 0o700); err != nil {
			log.Printf("development runner update root unavailable: %v", err)
		}
	}
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

func applyDevelopmentAccessOverride(catalog *config.Catalog) error {
	email := config.NormalizeAccessSubject(os.Getenv("OPS_DEV_ADMIN_EMAIL"))
	if email == "" {
		return nil
	}
	if catalog.Access == nil || catalog.Access.Roles["platform-admin"].ID == "" {
		return errors.New("OPS_DEV_ADMIN_EMAIL 需要包含 platform-admin 的访问策略")
	}
	hash := config.AccessHashForEmail(email)
	principal := catalog.Access.Principals[hash]
	principal.Email, principal.EmailHash, principal.Subject = email, hash, hash
	if principal.TenantID == "" {
		principal.TenantID = catalog.Access.DefaultTenant
	}
	if !containsString(principal.Roles, "platform-admin") {
		principal.Roles = append(principal.Roles, "platform-admin")
	}
	if catalog.Access.Principals == nil {
		catalog.Access.Principals = make(map[string]config.AccessPrincipal)
	}
	catalog.Access.Principals[hash] = principal
	return nil
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
