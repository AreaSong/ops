package runner

import (
	"context"
	"embed"
	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	opsweb "github.com/AreaSong/ops/services/areasong-ops/internal/web"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRegisteredReleaseBrowser(t *testing.T) {
	if os.Getenv("BR_BROWSER") != "1" {
		t.Skip("BR_BROWSER=1 使用已有Chromium运行完整网页HTTP链")
	}
	e, p, m := brEngine(t, 1)
	dir, err := os.MkdirTemp("/tmp", "ops-br-http-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "runner.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	accepted := make(chan struct{})
	var acceptOnce sync.Once
	product := NewServer(e, e.store)
	runner := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		product.ServeHTTP(w, r)
		if strings.HasSuffix(r.URL.Path, "/execute") {
			acceptOnce.Do(func() { close(accepted) })
		}
	})}
	go runner.Serve(listener)
	defer runner.Close()
	web, err := opsweb.NewServer(tenantReviewAuth{map[string]string{"creator": actorHash(), "approver": strings.Repeat("b", 64)}}, opsweb.NewRunnerClient(socket), opsweb.ServerOptions{Development: true}, embed.FS{})
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
	mux.HandleFunc("POST /__br/control", func(w http.ResponseWriter, r *http.Request) {
		<-accepted
		e.Wait()
		m.mu.Lock()
		m.blocked = r.URL.Query().Get("blocked") == "true"
		m.mu.Unlock()
		w.Write([]byte(`{}`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "release-execution-browser.mjs")
	cmd.Dir = "../../web/tests"
	cmd.Env = append(os.Environ(), "BR_URL="+server.URL)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("browser: %v\n%s", err, output)
	}
	t.Log(string(output))
	plans, _ := e.store.ListReleasePlans(context.Background(), 10, 0)
	if len(plans) != 1 || plans[0].State != model.PlanCompleted {
		t.Fatal("browser final plan")
	}
	task, _ := e.store.GetTask(context.Background(), plans[0].TaskID)
	brState(t, e, task, model.PlanCompleted, model.WorkClosed)
	if p.calls != 3 || len(p.phases) != 3 || m.creates != 1 || m.deletes != 1 || m.gets != 1 {
		t.Fatalf("unexpected calls inspect=%d run=%v silence=%d/%d/%d", p.calls, p.phases, m.creates, m.deletes, m.gets)
	}
}
