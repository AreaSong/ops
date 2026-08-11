package main

import (
	"context"
	"embed"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	webapi "github.com/AreaSong/ops/services/areasong-ops/internal/web"
)

//go:embed all:static
var assets embed.FS

func main() {
	if len(os.Args) == 2 && os.Args[1] == "healthcheck" {
		healthcheck()
		return
	}
	if err := run(); err != nil {
		slog.Error("Web 退出", "error", err)
		os.Exit(1)
	}
}

func run() error {
	development := os.Getenv("OPS_ENV") == "development"
	allowedEmail := os.Getenv("OPS_ALLOWED_EMAIL")
	var authenticator webapi.Authenticator
	if development {
		authenticator = webapi.DevelopmentAuthenticator{Email: allowedEmail}
	} else {
		verifier, err := webapi.NewAccessVerifier(
			os.Getenv("OPS_ACCESS_ISSUER"), os.Getenv("OPS_ACCESS_AUDIENCE"), allowedEmail)
		if err != nil {
			return err
		}
		authenticator = verifier
	}
	handler, err := webapi.NewServer(
		authenticator,
		webapi.NewRunnerClient(envOr("OPS_RUNNER_SOCKET", "/run/areasong-ops/runner.sock")),
		webapi.ServerOptions{
			PublicOrigin: envOr("OPS_PUBLIC_ORIGIN", "https://ops.areasong.top"),
			GrafanaURL:   os.Getenv("OPS_GRAFANA_URL"),
			Development:  development,
		},
		assets,
	)
	if err != nil {
		return err
	}
	server := &http.Server{
		Addr:              envOr("OPS_WEB_LISTEN", ":8080"),
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       90 * time.Second,
		MaxHeaderBytes:    32 << 10,
	}
	slog.Info("Web 启动", "address", server.Addr)
	serveErrors := make(chan error, 1)
	go func() { serveErrors <- server.ListenAndServe() }()
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	select {
	case <-stop:
	case err := <-serveErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return server.Shutdown(ctx)
}

func healthcheck() {
	client := http.Client{Timeout: 3 * time.Second}
	response, err := client.Get("http://127.0.0.1:8080/healthz")
	if err != nil || response.StatusCode != http.StatusOK {
		os.Exit(1)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	response.Body.Close()
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
