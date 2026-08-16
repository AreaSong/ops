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
	stateRoot, err := os.MkdirTemp("/tmp", "areasong-ops-dev-state-")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(stateRoot)
	database, err := store.Open(filepath.Join(stateRoot, "ops.db"))
	if err != nil {
		log.Fatal(err)
	}
	defer database.Close()
	executor := &demoExecutor{versions: map[string]string{"areaforge": "1.1.1", "sub2api": "0.1.168"}}
	engine := runner.NewEngine(catalog, database, executor, stateRoot,
		runner.WithAlertmanager(demoAlertmanager{mode: os.Getenv("OPS_DEV_ALERTS")}),
		runner.WithCredentialRotator(demoCredentialRotator{}))
	socket := envOr("OPS_RUNNER_SOCKET", "/tmp/areasong-ops-dev.sock")
	_ = os.Remove(socket)
	listener, err := net.Listen("unix", socket)
	if err != nil {
		log.Fatal(err)
	}
	defer os.Remove(socket)
	server := &http.Server{Handler: runner.NewServer(engine, database), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("serve: %v", err)
		}
	}()
	log.Printf("development runner listening on %s", socket)
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	_ = server.Shutdown(context.Background())
	engine.Wait()
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
