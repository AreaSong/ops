package main

import (
	"context"
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
	development := os.Getenv("OPS_ENV") == "development"
	if os.Geteuid() != 0 && !development {
		return errors.New("生产 Runner 必须以 root 身份运行")
	}
	stateRoot := envOr("OPS_STATE_ROOT", "/var/lib/areasong-ops")
	catalogPath := envOr("OPS_SERVICE_CATALOG", "/etc/areasong-ops/services.json")
	socketPath := envOr("OPS_RUNNER_SOCKET", "/run/areasong-ops/runner.sock")
	catalog, err := config.Load(catalogPath, !development)
	if err != nil {
		return err
	}
	database, err := store.Open(filepath.Join(stateRoot, "ops.db"))
	if err != nil {
		return err
	}
	defer database.Close()
	if count, err := database.RecoverInterrupted(context.Background()); err != nil {
		return err
	} else if count > 0 {
		slog.Warn("检测到未完成任务，已转为人工核对", "count", count)
	}
	engine := runner.NewEngine(catalog, database, runner.CommandExecutor{}, stateRoot)
	listener, err := unixListener(socketPath)
	if err != nil {
		return err
	}
	defer listener.Close()
	server := &http.Server{
		Handler:           runner.NewServer(engine, database),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       90 * time.Second,
		MaxHeaderBytes:    32 << 10,
	}
	maintenanceContext, stopMaintenance := context.WithCancel(context.Background())
	defer stopMaintenance()
	legacyStateRoot := envOr("OPS_LEGACY_STATE_ROOT", "/var/lib/ops/update-control")
	go maintain(maintenanceContext, database, stateRoot, legacyStateRoot)
	serveErrors := make(chan error, 1)
	go func() { serveErrors <- server.Serve(listener) }()
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
	engine.Wait()
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
		if err := database.Prune(ctx, 30*24*time.Hour, 365*24*time.Hour); err != nil {
			slog.Error("清理审计留存失败", "error", err)
		} else {
			slog.Info("数据库留存清理完成")
		}
		pruned, err := maintenance.PruneArtifacts(
			stateRoot, legacyStateRoot, 30*24*time.Hour, time.Now().UTC(),
		)
		if err != nil {
			slog.Error("清理任务产物失败", "error", err)
		} else {
			slog.Info("任务产物清理完成", "operations", pruned.OperationDirectories,
				"snapshots", pruned.Snapshots, "sensitive_files", pruned.SensitiveFiles)
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
