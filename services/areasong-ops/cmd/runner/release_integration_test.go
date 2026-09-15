package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

func TestReleaseProcessHelper(t *testing.T) {
	if os.Getenv("OPS_RELEASE_TEST_PROCESS") != "1" {
		return
	}
	if err := run(); err != nil {
		t.Fatal(err)
	}
}

// 直接使用仓库真实 schema 的前 45 次迁移，避免用空表夹具冒充旧库兼容性。
func createLegacyReleaseDatabase(t *testing.T, path string) {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "../../internal/store/schema.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var base string
	var migrations []string
	ast.Inspect(file, func(node ast.Node) bool {
		value, ok := node.(*ast.ValueSpec)
		if !ok || len(value.Names) != 1 || len(value.Values) != 1 {
			return true
		}
		if value.Names[0].Name == "schema" {
			base, err = strconv.Unquote(value.Values[0].(*ast.BasicLit).Value)
		}
		if value.Names[0].Name == "migrations" {
			for _, element := range value.Values[0].(*ast.CompositeLit).Elts {
				text, decodeErr := strconv.Unquote(element.(*ast.BasicLit).Value)
				if decodeErr != nil {
					t.Fatal(decodeErr)
				}
				migrations = append(migrations, text)
			}
		}
		return true
	})
	if err != nil || base == "" || len(migrations) < 47 {
		t.Fatal("真实 schema 夹具不完整")
	}
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.Exec(base); err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations[:45] {
		if _, err := database.Exec(migration); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := database.Exec(`PRAGMA user_version=45;
		INSERT INTO tasks(id,idempotency_key,request_hash,actor_hash,service,action,target,risk,state,preview_id,snapshot_json,created_at)
		VALUES('pending','pending-key','request','actor','demo','inspect','','read_only','queued','','{}','2026-09-15T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
}

func releaseTestCatalog(t *testing.T, root, path string) {
	t.Helper()
	adapter := filepath.Join(root, "adapter")
	if err := os.WriteFile(adapter, []byte("#!/bin/sh\nexit 91\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	content := fmt.Sprintf(`{"schemaVersion":3,"services":{"demo":{
		"name":"demo","objectId":"service:demo","displayName":"Demo","template":"custom","adapter":%q,
		"actions":{"inspect":{"name":"inspect","displayName":"检查","enabled":true,"risk":"read_only",
		"targetMode":"none","steps":["inspect"],"timeoutSeconds":30,"impact":"无变更","rollback":"无需回滚","scope":"单服务"}}
	}}}`, adapter)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func releaseUnixClient(socket string) *http.Client {
	return &http.Client{Timeout: time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		},
	}}
}

func TestActualRunnerMaintenanceMigratesButNeverRecoversTasks(t *testing.T) {
	root, catalog, fence := releaseFenceFixture(t)
	releaseTestCatalog(t, root, catalog)
	fence.CatalogSHA256, _ = releaseFileDigest(catalog)
	writeReleaseTestJSON(t, filepath.Join(root, "release-orchestrator/maintenance.json"), fence)
	databasePath := filepath.Join(root, "ops.db")
	createLegacyReleaseDatabase(t, databasePath)
	socket := filepath.Join(root, "run/runner.sock")
	command := exec.Command(os.Args[0], "-test.run=^TestReleaseProcessHelper$")
	command.Env = append(os.Environ(), "OPS_RELEASE_TEST_PROCESS=1", "OPS_STATE_ROOT="+root,
		"OPS_SERVICE_CATALOG="+catalog, "OPS_RUNNER_SOCKET="+socket, "OPS_ALERTMANAGER_URL=http://127.0.0.1:9093")
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = command.Process.Signal(syscall.SIGTERM)
		if err := command.Wait(); err != nil {
			t.Errorf("维护 Runner 退出失败: %v\n%s", err, output.String())
		}
	})
	client := releaseUnixClient(socket)
	defer client.CloseIdleConnections()
	var health map[string]any
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		response, err := client.Get("http://runner/healthz")
		if err == nil {
			_ = json.NewDecoder(response.Body).Decode(&health)
			response.Body.Close()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if health["releaseMaintenance"] != true || health["schemaVersion"] != float64(47) {
		t.Fatalf("维护 Runner 未就绪: %v", health)
	}
	request, _ := http.NewRequest(http.MethodPost, "http://runner/v1/auto-updates/evaluate", nil)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("维护 Runner 接受了写请求: %d", response.StatusCode)
	}
	database, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var state string
	if err := database.QueryRow("SELECT state FROM tasks WHERE id='pending'").Scan(&state); err != nil || state != "queued" {
		t.Fatalf("维护模式恢复了任务: %q %v", state, err)
	}
	if _, err := os.Stat(filepath.Join(root, "snapshots")); !os.IsNotExist(err) {
		t.Fatal("维护模式运行了后台快照/留存循环")
	}
}
