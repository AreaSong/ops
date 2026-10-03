package runner

import (
	"context"
	"embed"
	"errors"
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

// 测试专用合成身份由两个隔离浏览器 Cookie 选择；不接触真实身份目录。
type tenantReviewAuth struct{ actors map[string]string }

func (a tenantReviewAuth) Authenticate(r *http.Request) (opsweb.Session, error) {
	cookie, err := r.Cookie("review_actor")
	if err != nil {
		return opsweb.Session{}, errors.New("测试会话未登录")
	}
	actor, ok := a.actors[cookie.Value]
	if !ok {
		return opsweb.Session{}, errors.New("测试身份无效")
	}
	return opsweb.Session{Subject: actor, Email: cookie.Value + "@example.test", TenantID: "default"}, nil
}
func TestTenantReviewBrowser(t *testing.T) {
	if os.Getenv("S31A_BROWSER") != "1" {
		t.Skip("按需运行真实 Chromium：S31A_BROWSER=1；需先构建 web/dist 和提供已有 Playwright 路径")
	}
	f := newTenantReviewFixture(t)
	dir, err := os.MkdirTemp("/tmp", "ops-tenant-review-")
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
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "tenant-review-browser.mjs")
	cmd.Dir = "../../web/tests"
	cmd.Env = append(os.Environ(), "S31A_URL="+server.URL)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("browser: %v\n%s", err, output)
	} else {
		t.Log(string(output))
	}
	policy, _, err := f.engine.effectiveAccessPolicy(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if policy.Tenants["browser-tenant"].DisplayName != "浏览器审阅后名称" {
		t.Fatal("browser apply did not reach SQLite policy")
	}
}
