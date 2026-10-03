package runner

import (
	"context"
	"embed"
	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	opsweb "github.com/AreaSong/ops/services/areasong-ops/internal/web"
)

func TestRoleReviewBrowser(t *testing.T) {
	if os.Getenv("S32B_BROWSER") != "1" {
		t.Skip("按需运行真实 Chromium：S32B_BROWSER=1；需先构建 web/dist 和提供已有 Playwright 路径")
	}
	f := newTenantReviewFixture(t)
	f.apply(t, submitReviewRequest(t, f, roleRequest("bound-role", "已有绑定角色", model.PermissionRead, model.PermissionInspect, model.PermissionRead)))
	expired := time.Now().Add(-time.Hour).UTC()
	f.apply(t, submitReviewRequest(t, f, model.AccessControlUpdateRequest{Bindings: []model.RoleBinding{
		{ID: "active-binding", Subject: f.actors["viewer"], TenantID: "default", RoleID: "bound-role"},
		{ID: "expired-binding", Subject: f.actors["viewer"], TenantID: "isolated", RoleID: "bound-role", ExpiresAt: &expired},
	}}))
	bindingsBefore, _ := f.db.ListRoleBindings(context.Background())
	policyBefore, _, _ := f.engine.effectiveAccessPolicy(context.Background())
	dir, err := os.MkdirTemp("/tmp", "ops-role-review-")
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
	cmd := exec.CommandContext(ctx, "node", "role-review-browser.mjs")
	cmd.Dir = "../../web/tests"
	cmd.Env = append(os.Environ(), "S32B_URL="+server.URL)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("browser: %v\n%s", err, output)
	} else {
		t.Log(string(output))
	}
	policy, _, err := f.engine.effectiveAccessPolicy(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if policy.Roles["browser-role"].DisplayName != "浏览器新增角色" || policy.Roles["bound-role"].DisplayName != "浏览器编辑角色" {
		t.Fatal("browser did not reach SQLite policy")
	}
	if !reflect.DeepEqual(policy.Roles["bound-role"].Permissions, []model.Permission{model.PermissionInspect, model.PermissionDeploy}) {
		t.Fatal("permissions not replaced")
	}
	bindingsAfter, _ := f.db.ListRoleBindings(context.Background())
	if !reflect.DeepEqual(bindingsBefore, bindingsAfter) || !reflect.DeepEqual(policyBefore.Tenants, policy.Tenants) || !reflect.DeepEqual(policyBefore.Principals, policy.Principals) {
		t.Fatal("unrelated policy changed")
	}
	if !policy.Roles["bound-role"].Allows(model.PermissionDeploy) || policy.Roles["bound-role"].Allows(model.PermissionRead) {
		t.Fatal("bound role permissions unchanged")
	}
}
