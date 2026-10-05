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
	"reflect"
	"testing"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	opsweb "github.com/AreaSong/ops/services/areasong-ops/internal/web"
)

// 只运行合成 Cookie 身份、回环 Web 和临时 SQLite；不接触目录或生产资源。
func TestBindingEditBrowser(t *testing.T) {
	if os.Getenv("S33B_BROWSER") != "1" {
		t.Skip("S33B_BROWSER=1 按需运行真实浏览器")
	}
	f := newTenantReviewFixture(t)
	expires, _ := time.Parse(time.RFC3339Nano, "2030-01-01T00:00:00.123456789Z")
	// viewer 移除直接角色，唯一授权来自目标绑定；正反 HTTP 对照才有意义。
	seed := submitReviewRequest(t, f, model.AccessControlUpdateRequest{
		Principals: []model.AccessPrincipal{{Subject: f.actors["viewer"], TenantID: "default", Roles: []string{}, Status: "active"}},
		Bindings: []model.RoleBinding{
			{ID: "existing-binding", Subject: f.actors["viewer"], TenantID: "default", RoleID: "platform-admin", ObjectIDs: []string{"access", "fleet", "comma, space object", "fleet"}, ExpiresAt: &expires},
			{ID: "unsupported-jit", Subject: f.actors["other"], TenantID: "default", RoleID: "viewer", JIT: true},
		},
	})
	f.apply(t, seed)
	before, _, err := f.engine.effectiveAccessPolicy(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	beforeRows, _ := f.db.ListRoleBindings(context.Background())
	dir, err := os.MkdirTemp("/tmp", "ops-binding-edit-")
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
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "node", "binding-edit-browser.mjs")
	cmd.Dir = "../../web/tests"
	cmd.Env = append(os.Environ(), "S33B_URL="+server.URL)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("browser: %v\n%s", err, output)
	}
	t.Log(string(output))
	after, _, err := f.engine.effectiveAccessPolicy(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before.Principals, after.Principals) || !reflect.DeepEqual(before.Roles, after.Roles) || !reflect.DeepEqual(before.Tenants, after.Tenants) {
		t.Fatal("unrelated policy rewritten")
	}
	afterRows, _ := f.db.ListRoleBindings(context.Background())
	if len(afterRows) != len(beforeRows) || !reflect.DeepEqual(beforeRows[1], afterRows[1]) {
		t.Fatal("other binding changed")
	}
}
