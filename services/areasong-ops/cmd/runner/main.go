package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/config"
	"github.com/AreaSong/ops/services/areasong-ops/internal/maintenance"
	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	"github.com/AreaSong/ops/services/areasong-ops/internal/runner"
	"github.com/AreaSong/ops/services/areasong-ops/internal/store"
)

func main() {
	if err := run(); err != nil {
		slog.Error("Runner 退出", "error", err)
		os.Exit(1)
	}
}

func run() error {
	if os.Geteuid() != 0 {
		return errors.New("生产 Runner 必须以 root 身份运行")
	}
	stateRoot := envOr("OPS_STATE_ROOT", "/var/lib/areasong-ops")
	catalogPath := envOr("OPS_SERVICE_CATALOG", "/etc/areasong-ops/services.json")
	socketPath := envOr("OPS_RUNNER_SOCKET", "/var/lib/areasong-ops/run/runner.sock")
	catalog, err := config.Load(catalogPath, true)
	if err != nil {
		return err
	}
	alertmanager, err := runner.NewAlertmanagerClient(envOr("OPS_ALERTMANAGER_URL", "http://127.0.0.1:9093"))
	if err != nil {
		return err
	}
	database, err := store.Open(filepath.Join(stateRoot, "ops.db"))
	if err != nil {
		return err
	}
	defer database.Close()
	if err := enforceProductionStateRootMode(stateRoot); err != nil {
		return err
	}
	if count, err := database.RecoverInterrupted(context.Background(), interruptionClassifier(catalog)); err != nil {
		return err
	} else if count > 0 {
		slog.Warn("检测到未完成任务，已转为人工核对", "count", count)
	}
	if count, err := database.RecoverInterruptedCredentialRotations(context.Background()); err != nil {
		return err
	} else if count > 0 {
		slog.Warn("检测到未完成凭据轮换，已转为人工核对", "count", count)
	}
	if count, err := database.RecoverInterruptedRunnerUpdates(context.Background()); err != nil {
		return err
	} else if count > 0 {
		slog.Warn("检测到失联的 Runner 更新执行器，已转为人工核对", "count", count)
	}
	engine, err := runner.NewEngineChecked(catalog, database, runner.CommandExecutor{}, stateRoot,
		runner.WithAlertmanager(alertmanager),
		runner.WithCredentialRotator(runner.NewProductionCredentialRotator()))
	if err != nil {
		return err
	}
	listener, err := unixListener(socketPath)
	if err != nil {
		return err
	}
	defer listener.Close()
	rawHandler := runner.NewServer(engine, database)
	handler := runner.WithListenerKind(rawHandler, "unix")
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       90 * time.Second,
		MaxHeaderBytes:    32 << 10,
	}
	var remoteServer *http.Server
	var remoteListener net.Listener
	if catalog.Fleet != nil && catalog.Fleet.AllowRemoteRunners {
		remoteListener, err = mtlsListener(catalog.Fleet)
		if err != nil {
			return err
		}
		defer remoteListener.Close()
		remoteServer = &http.Server{
			Handler:           runner.RemoteRunnerHandler(rawHandler),
			ReadHeaderTimeout: 5 * time.Second,
			IdleTimeout:       90 * time.Second,
			MaxHeaderBytes:    32 << 10,
		}
	}
	maintenanceContext, stopMaintenance := context.WithCancel(context.Background())
	defer stopMaintenance()
	legacyStateRoot := envOr("OPS_LEGACY_STATE_ROOT", "/var/lib/ops/update-control")
	go maintain(maintenanceContext, database, stateRoot, legacyStateRoot)
	go monitorRunnerUpdates(maintenanceContext, database)
	serveBuffer := 1
	if remoteServer != nil {
		serveBuffer++
	}
	serveErrors := make(chan error, serveBuffer)
	go func() { serveErrors <- server.Serve(listener) }()
	if remoteServer != nil {
		go func() { serveErrors <- remoteServer.Serve(remoteListener) }()
	}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	select {
	case signalValue := <-stop:
		slog.Info("收到停止信号", "signal", signalValue.String())
	case err := <-serveErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	shutdownContext, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = server.Shutdown(shutdownContext)
	if remoteServer != nil {
		_ = remoteServer.Shutdown(shutdownContext)
	}
	engine.Wait()
	return nil
}

func mtlsListener(policy *config.FleetPolicy) (net.Listener, error) {
	if policy == nil || !policy.AllowRemoteRunners || !policy.RequiremTLS {
		return nil, errors.New("远程 Runner mTLS 策略未启用")
	}
	certificate, err := tls.LoadX509KeyPair(policy.MTLSCertificateFile, policy.MTLSKeyFile)
	if err != nil {
		return nil, fmt.Errorf("加载 Runner mTLS 证书失败: %w", err)
	}
	caPEM, err := os.ReadFile(policy.MTLSClientCAFile)
	if err != nil {
		return nil, fmt.Errorf("读取 Runner mTLS 客户端 CA 失败: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("Runner mTLS 客户端 CA 无有效证书")
	}
	tlsConfig := &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{certificate},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    pool,
	}
	listener, err := tls.Listen("tcp", policy.MTLSListenAddress, tlsConfig)
	if err != nil {
		return nil, fmt.Errorf("监听 Runner mTLS 地址失败: %w", err)
	}
	return listener, nil
}

func monitorRunnerUpdates(ctx context.Context, database *store.Store) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			count, err := database.RecoverInterruptedRunnerUpdates(ctx)
			if err != nil {
				slog.Error("Runner 更新执行心跳检查失败", "error", err)
			} else if count > 0 {
				slog.Warn("Runner 更新执行器心跳超时，已转为人工核对", "count", count)
			}
		}
	}
}

func interruptionClassifier(catalog *config.Catalog) store.InterruptionClassifier {
	return func(serviceName, actionName, phase string, productionChanged bool) (bool, bool) {
		service, exists := catalog.Services[serviceName]
		if !exists {
			service, exists = catalog.AutomaticTasks[serviceName]
		}
		action, actionExists := service.Actions[actionName]
		if !exists || !actionExists {
			return true, false
		}
		phaseExists := false
		for _, step := range action.Steps {
			if step == phase {
				phaseExists = true
				break
			}
		}
		if !phaseExists {
			return true, false
		}
		semantics := model.EffectivePhaseSemantics(action, phase)
		failureSemantics := model.EffectiveFailureSemantics(
			action, phase, productionChanged || semantics.Effect == "runtime_mutation" || semantics.Effect == "data_mutation",
		)
		mutationUncertain := semantics.Effect == "runtime_mutation" || semantics.Effect == "data_mutation"
		rollbackAvailable := failureSemantics.FailurePolicy == "rollback" && failureSemantics.RecoveryPhase != ""
		return mutationUncertain, rollbackAvailable
	}
}

func enforceProductionStateRootMode(path string) error {
	if err := os.Chmod(path, 0o710); err != nil {
		return fmt.Errorf("设置 Runner 状态根目录权限: %w", err)
	}
	return nil
}

func unixListener(path string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, err
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("拒绝覆盖非 Socket 路径: %s", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o660); err != nil {
		listener.Close()
		return nil, err
	}
	if err := os.Chown(path, 0, os.Getgid()); err != nil && os.Geteuid() == 0 {
		listener.Close()
		return nil, err
	}
	return listener, nil
}

func maintain(ctx context.Context, database *store.Store, stateRoot, legacyStateRoot string) {
	run := func() {
		now := time.Now().UTC()
		if err := database.Prune(ctx, 30*24*time.Hour, 365*24*time.Hour); err != nil {
			slog.Error("清理审计留存失败", "error", err)
		} else {
			slog.Info("数据库留存清理完成")
		}
		if expired, err := database.ExpireRecoveryPoints(ctx, now); err != nil {
			slog.Error("标记过期恢复点失败", "error", err)
		} else {
			slog.Info("恢复点过期检查完成", "expired", expired)
		}
		protected, err := database.ProtectedOperationIDs(ctx, now)
		if err != nil {
			slog.Error("读取受保护任务产物失败，跳过清理", "error", err)
		} else {
			pruned, pruneErr := maintenance.PruneArtifacts(
				stateRoot, legacyStateRoot, 30*24*time.Hour, now, protected,
			)
			if pruneErr != nil {
				slog.Error("清理任务产物失败", "error", pruneErr)
			} else {
				slog.Info("任务产物清理完成", "operations", pruned.OperationDirectories,
					"protected", len(protected), "snapshots", pruned.Snapshots,
					"sensitive_files", pruned.SensitiveFiles)
			}
		}
		path, err := database.Snapshot(ctx, filepath.Join(stateRoot, "snapshots"))
		if err != nil {
			slog.Error("创建 SQLite 快照失败", "error", err)
			return
		}
		slog.Info("SQLite 快照完成", "path", path)
	}
	run()
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
