package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/AreaSong/ops/services/areasong-ops/internal/buildinfo"
	"github.com/AreaSong/ops/services/areasong-ops/internal/store"
)

func TestReleaseMaintenanceOnlyServesReadinessAndMetrics(t *testing.T) {
	handler := releaseMaintenanceHandler(&releaseFence{DeploymentID: "ops-test"})
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		for _, path := range []string{"/v1/services", "/v1/auto-updates/evaluate", "/v1/runner/update/prepare", "/v1/kubernetes/plans", "/v1/terminal/sessions", "/unknown"} {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(method, path, strings.NewReader(`{}`)))
			if response.Code != http.StatusServiceUnavailable {
				t.Fatalf("%s %s status=%d", method, path, response.Code)
			}
		}
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	var health map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &health); err != nil {
		t.Fatal(err)
	}
	if health["releaseMaintenance"] != true || health["deploymentId"] != "ops-test" || health["schemaVersion"] != float64(store.CurrentSchemaVersion()) {
		t.Fatalf("health=%v", health)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/healthz", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatal("POST health unexpectedly allowed")
	}
}

func TestReleaseJSONRejectsLinksAndUnsafePermissions(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "state.json")
	if err := os.WriteFile(file, []byte(`{"schemaVersion":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := readReleaseJSON(file, &result, uint32(os.Geteuid())); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link.json")
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	if err := readReleaseJSON(link, &result, uint32(os.Geteuid())); err == nil {
		t.Fatal("symlink accepted")
	}
	if err := os.Chmod(file, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := readReleaseJSON(file, &result, uint32(os.Geteuid())); err == nil {
		t.Fatal("public state file accepted")
	}
}

func releaseFenceFixture(t *testing.T) (string, string, releaseFence) {
	t.Helper()
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		t.Skip("生产信任链在隔离 Linux root 容器验证")
	}
	root, err := os.MkdirTemp("/root", "ops-release-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	releaseRoot := filepath.Join(root, "release-orchestrator")
	if err := os.Mkdir(releaseRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	catalog := filepath.Join(root, "services.json")
	if err := os.WriteFile(catalog, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	executable, _ := os.Executable()
	binaryDigest, _ := releaseFileDigest(executable)
	catalogDigest, _ := releaseFileDigest(catalog)
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		t.Fatal(err)
	}
	fence := releaseFence{SchemaVersion: 1, DeploymentID: "ops-test", Revision: buildinfo.Revision,
		RunnerSHA256: binaryDigest, CatalogSHA256: catalogDigest, BootID: strings.TrimSpace(string(boot))}
	writeReleaseTestJSON(t, filepath.Join(releaseRoot, "maintenance.json"), fence)
	writeReleaseTestJSON(t, filepath.Join(releaseRoot, "active.json"), map[string]any{
		"schemaVersion": 1, "deploymentId": "ops-test", "revision": buildinfo.Revision,
	})
	directory := filepath.Join(releaseRoot, "deployments/ops-test")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	writeReleaseTestJSON(t, filepath.Join(directory, "state.json"), map[string]any{
		"schemaVersion": 2, "deploymentId": "ops-test", "status": "running", "activationStarted": false,
	})
	return root, catalog, fence
}

func writeReleaseTestJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestReleaseFenceBindsBinaryCatalogBootAndOwner(t *testing.T) {
	root, catalog, fence := releaseFenceFixture(t)
	loaded, err := loadReleaseFence(root, catalog)
	if err != nil || loaded == nil {
		t.Fatalf("fence=%v err=%v", loaded, err)
	}
	fence.BootID = "different-boot"
	writeReleaseTestJSON(t, filepath.Join(root, "release-orchestrator/maintenance.json"), fence)
	if _, err := loadReleaseFence(root, catalog); err == nil {
		t.Fatal("reboot was accepted")
	}
}

func TestMissingFenceRequiresDurableActivation(t *testing.T) {
	root, catalog, _ := releaseFenceFixture(t)
	releaseRoot := filepath.Join(root, "release-orchestrator")
	if err := os.Remove(filepath.Join(releaseRoot, "maintenance.json")); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(releaseRoot, "deployments/ops-test")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	state := map[string]any{"schemaVersion": 2, "deploymentId": "ops-test", "activationStarted": false, "status": "running"}
	writeReleaseTestJSON(t, filepath.Join(directory, "state.json"), state)
	if _, err := loadReleaseFence(root, catalog); err == nil {
		t.Fatal("missing fence before activation was accepted")
	}
	state["activationStarted"] = true
	writeReleaseTestJSON(t, filepath.Join(directory, "state.json"), state)
	if fence, err := loadReleaseFence(root, catalog); err != nil || fence != nil {
		t.Fatalf("activation fence=%v err=%v", fence, err)
	}
	state["status"] = "needs_attention"
	writeReleaseTestJSON(t, filepath.Join(directory, "state.json"), state)
	if _, err := loadReleaseFence(root, catalog); err == nil {
		t.Fatal("needs_attention startup was accepted")
	}
}

func TestEmptyUntrustedReleaseDirectoryNeverEntersNormalMode(t *testing.T) {
	root, catalog, _ := releaseFenceFixture(t)
	directory := filepath.Join(root, "release-orchestrator")
	if err := os.Remove(filepath.Join(directory, "active.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(directory, "maintenance.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o777); err != nil {
		t.Fatal(err)
	}
	if _, err := loadReleaseFence(root, catalog); err == nil {
		t.Fatal("writable empty release directory allowed normal mode")
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	moved := directory + "-moved"
	if err := os.Rename(directory, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(moved, directory); err != nil {
		t.Fatal(err)
	}
	if _, err := loadReleaseFence(root, catalog); err == nil {
		t.Fatal("release directory symlink allowed normal mode")
	}
}
