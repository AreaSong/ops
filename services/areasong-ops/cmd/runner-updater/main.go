package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/AreaSong/ops/services/areasong-ops/internal/store"
	"github.com/AreaSong/ops/services/areasong-ops/internal/updater"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		slog.Error("Runner updater 退出", "error", err)
		os.Exit(1)
	}
}

func run(arguments []string) error {
	if os.Geteuid() != 0 {
		return errors.New("Runner updater 必须以 root 身份运行")
	}
	flags := flag.NewFlagSet("areasong-ops-runner-updater", flag.ContinueOnError)
	stateRoot := flags.String("state-root", envOr("OPS_STATE_ROOT", "/var/lib/areasong-ops"), "Runner state root")
	updateID := flags.String("update-id", "", "approved Runner update ID")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if !filepath.IsAbs(*stateRoot) || filepath.Clean(*stateRoot) != *stateRoot || strings.ContainsRune(*stateRoot, '\x00') {
		return errors.New("Runner updater state root 无效")
	}
	database, err := store.OpenExisting(filepath.Join(*stateRoot, "ops.db"))
	if err != nil {
		return err
	}
	defer database.Close()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	executor := updater.Executor{
		Store: database, StateRoot: *stateRoot,
		Controller: updater.SystemdController{SocketPath: filepath.Join(*stateRoot, "run", "runner.sock")},
	}
	outcome, err := executor.Run(ctx, *updateID)
	if err != nil {
		return fmt.Errorf("Runner 更新以 %s 收口: %w", outcome, err)
	}
	slog.Info("Runner 更新执行完成", "state", outcome, "update_id", *updateID)
	return nil
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
