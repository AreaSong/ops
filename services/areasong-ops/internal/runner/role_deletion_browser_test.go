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

func TestRoleDeletionBrowser(t *testing.T) {
	if os.Getenv("S32D2_BROWSER") != "1" {
		t.Skip("按需运行真实 Chromium：S32D2_BROWSER=1；需先构建 web/dist 和提供已有 Playwright 路径")
	}
	f := newTenantReviewFixture(t)
	f.apply(t, submitReviewRequest(t, f, roleRequest("bound-role", "已有绑定角色", model.PermissionRead, model.PermissionInspect, model.PermissionRead)))
	expired := time.Now().Add(-time.Hour).UTC()
	f.apply(t, submitReviewRequest(t, f, model.AccessControlUpdateRequest{Bindings: []model.RoleBinding{
		{ID: "active-binding", Subject: f.actors["viewer"], TenantID: "default", RoleID: "bound-role"},
		{ID: "expired-binding", Subject: f.actors["viewer"], TenantID: "isolated", RoleID: "bound-role", ExpiresAt: &expired},
	}}))
	bindingsBefore, _ := f.db.ListRoleBindings(context.Background())
	f.apply(t, submitReviewRequest(t, f, roleRequest("direct-role", "直接引用角色", model.PermissionRead)))
	f.apply(t, submitReviewRequest(t, f, model.AccessControlUpdateRequest{Principals: []model.AccessPrincipal{{Subject: f.actors["viewer"], TenantID: "default", Roles: []string{"direct-role"}}}}))
	policyBefore, _, _ := f.engine.effectiveAccessPolicy(context.Background())
	dir, err := os.MkdirTemp("/tmp", "ops-role-deletion-")
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
	cmd := exec.CommandContext(ctx, "node", "role-deletion-browser.mjs")
	cmd.Dir = "../../web/tests"
	cmd.Env = append(os.Environ(), "S32D2_URL="+server.URL)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("browser: %v\n%s", err, output)
	} else {
		t.Log(string(output))
	}
	policy, _, err := f.engine.effectiveAccessPolicy(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := policy.Roles["browser-delete"]; exists {
		t.Fatal("deleted role remains in SQLite")
	}
	if _, exists := policy.Roles["stale-delete"]; !exists {
		t.Fatal("stale proposal removed role")
	}
	for id, role := range policyBefore.Roles {
		if !reflect.DeepEqual(role, policy.Roles[id]) {
			t.Fatalf("unrelated role changed: %s", id)
		}
	}
	bindingsAfter, _ := f.db.ListRoleBindings(context.Background())
	if !reflect.DeepEqual(bindingsBefore, bindingsAfter) || !reflect.DeepEqual(policyBefore.Tenants, policy.Tenants) || !reflect.DeepEqual(policyBefore.Principals, policy.Principals) {
		t.Fatal("unrelated policy changed")
	}
}
