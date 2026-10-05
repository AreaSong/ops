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
func TestBindingRevokeBrowser(t *testing.T) {
	if os.Getenv("S33C_BROWSER") != "1" {
		t.Skip("S33C_BROWSER=1 按需运行真实浏览器")
	}
	f := newTenantReviewFixture(t)
	expires, _ := time.Parse(time.RFC3339Nano, "2030-01-01T00:00:00.123456789Z")
	expired, _ := time.Parse(time.RFC3339Nano, "2000-01-01T00:00:00.123456789Z")
	seed := submitReviewRequest(t, f, model.AccessControlUpdateRequest{
		Principals: []model.AccessPrincipal{
			{Subject: f.actors["viewer"], TenantID: "default", Roles: []string{}, Status: "active"},
			{Subject: f.actors["other"], TenantID: "default", Roles: []string{}, Status: "active"},
			{Subject: f.actors["creator"], TenantID: "default", Roles: []string{}, Status: "active"},
		},
		Roles: []model.Role{{ID: "revoke-role", DisplayName: "完整权限核对角色", Permissions: []model.Permission{"ops.read", "ops.inspect", "ops.lifecycle", "ops.deploy", "ops.batch", "ops.recover", "fleet.manage", "access.manage", "config.manage", "break_glass", "runner.update"}}},
		Bindings: []model.RoleBinding{
			{ID: "sole-binding", Subject: f.actors["viewer"], TenantID: "default", RoleID: "revoke-role", ObjectIDs: []string{"access", "long, object with internal spaces and precise identity", "access"}, ExpiresAt: &expires},
			{ID: "other-target", Subject: f.actors["other"], TenantID: "default", RoleID: "manager", ObjectIDs: []string{"access"}},
			{ID: "other-source", Subject: f.actors["other"], TenantID: "default", RoleID: "manager", ObjectIDs: []string{"access"}},
			{ID: "expired-target", Subject: f.actors["viewer"], TenantID: "default", RoleID: "viewer", ExpiresAt: &expired},
			{ID: "expired-keep", Subject: f.actors["viewer"], TenantID: "default", RoleID: "viewer", ExpiresAt: &expired},
			{ID: "self-binding", Subject: f.actors["creator"], TenantID: "default", RoleID: "platform-admin"},
			{ID: "unsupported-jit", Subject: f.actors["other"], TenantID: "default", RoleID: "viewer", JIT: true},
		},
	})
	f.apply(t, seed)
	before, _, err := f.engine.effectiveAccessPolicy(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	beforeRows, _ := f.db.ListRoleBindings(context.Background())
	dir, err := os.MkdirTemp("/tmp", "ops-binding-revoke-")
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
	cmd := exec.CommandContext(ctx, "node", "binding-revoke-browser.mjs")
	cmd.Dir = "../../web/tests"
	cmd.Env = append(os.Environ(), "S33C_URL="+server.URL)
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
	removed := map[string]bool{"sole-binding": true, "other-target": true, "expired-target": true, "self-binding": true}
	if len(afterRows) != len(beforeRows)-len(removed) {
		t.Fatal("unexpected binding count")
	}
	for _, row := range beforeRows {
		if removed[row.ID] {
			continue
		}
		found := false
		for _, current := range afterRows {
			if current.ID == row.ID {
				found = reflect.DeepEqual(row, current)
			}
		}
		if !found {
			t.Fatalf("unrelated binding changed: %s", row.ID)
		}
	}
}
