package runner

import (
	"context"
	"embed"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	opsweb "github.com/AreaSong/ops/services/areasong-ops/internal/web"
)

func TestBindingReviewBrowser(t *testing.T) {
	if os.Getenv("S33A2_BROWSER") != "1" {
		t.Skip("按需运行真实 Chromium：S33A2_BROWSER=1；需先构建 web/dist 和提供已有 Playwright 路径")
	}
	f := newTenantReviewFixture(t)
	dir, err := os.MkdirTemp("/tmp", "ops-binding-review-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "runner.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	runner := &http.Server{Handler: f.handler}
	go runner.Serve(listener)
	defer runner.Close()
	web, err := opsweb.NewServer(tenantReviewAuth{f.actors}, opsweb.NewRunnerClient(socket), opsweb.ServerOptions{Development: true}, embed.FS{})
	if err != nil {
		t.Fatal(err)
	}
	dist, err := filepath.Abs("../../web/dist")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("/api/", web)
	mux.Handle("/", http.FileServer(http.Dir(dist)))
	server := httptest.NewServer(mux)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "binding-review-browser.mjs")
	cmd.Dir = "../../web/tests"
	cmd.Env = append(os.Environ(), "S33A2_URL="+server.URL)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("browser: %v\n%s", err, output)
	} else {
		t.Log(string(output))
	}
	rows, err := f.db.ListRoleBindings(context.Background())
	if err != nil || len(rows) != 0 {
		t.Fatal("browser revoke did not reach SQLite", rows, err)
	}
}
