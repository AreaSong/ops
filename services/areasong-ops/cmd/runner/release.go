package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/buildinfo"
	"github.com/AreaSong/ops/services/areasong-ops/internal/store"
)

const releaseProtocol = 1

type releaseFence struct {
	SchemaVersion int    `json:"schemaVersion"`
	DeploymentID  string `json:"deploymentId"`
	Revision      string `json:"revision"`
	RunnerSHA256  string `json:"runnerSha256"`
	CatalogSHA256 string `json:"catalogSha256"`
	BootID        string `json:"bootId"`
}

type releaseState struct {
	SchemaVersion             int    `json:"schemaVersion"`
	DeploymentID              string `json:"deploymentId"`
	ActivationStarted         bool   `json:"activationStarted"`
	RollbackActivationStarted bool   `json:"rollbackActivationStarted"`
	Status                    string `json:"status"`
}

func runCommand(arguments []string) error {
	if len(arguments) == 1 && arguments[0] == "--release-info" {
		return json.NewEncoder(os.Stdout).Encode(map[string]any{
			"releaseProtocol": releaseProtocol, "component": "runner",
			"version": buildinfo.Version, "revision": buildinfo.Revision,
			"schemaVersion": store.CurrentSchemaVersion(),
		})
	}
	if len(arguments) != 0 {
		return errors.New("Runner 参数无效")
	}
	return run()
}

func loadReleaseFence(stateRoot, catalogPath string) (*releaseFence, error) {
	root := filepath.Join(stateRoot, "release-orchestrator")
	marker := filepath.Join(root, "maintenance.json")
	active := filepath.Join(root, "active.json")
	if _, err := os.Lstat(root); os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	if err := trustedReleaseDirectory("/", root, 0); err != nil {
		return nil, err
	}
	_, markerErr := os.Lstat(marker)
	_, activeErr := os.Lstat(active)
	if os.IsNotExist(markerErr) && os.IsNotExist(activeErr) {
		return nil, nil
	}
	var owner struct {
		SchemaVersion int    `json:"schemaVersion"`
		DeploymentID  string `json:"deploymentId"`
		Revision      string `json:"revision"`
	}
	if err := readReleaseJSON(active, &owner, 0); err != nil {
		return nil, err
	}
	if owner.SchemaVersion != 1 || !regexp.MustCompile(`^[A-Za-z0-9_-]{1,100}$`).MatchString(owner.DeploymentID) || owner.Revision != buildinfo.Revision {
		return nil, errors.New("发布占用记录与当前 Runner 身份不匹配")
	}
	state, err := readReleaseState(root, owner.DeploymentID)
	if err != nil {
		return nil, err
	}
	if os.IsNotExist(markerErr) {
		if !state.ActivationStarted && !state.RollbackActivationStarted {
			return nil, errors.New("维护标记缺失且未持久化激活边界")
		}
		return nil, nil
	}
	if state.ActivationStarted || state.RollbackActivationStarted {
		return nil, errors.New("激活边界与维护标记重叠，需要人工核对")
	}
	var fence releaseFence
	if err := readReleaseJSON(marker, &fence, 0); err != nil {
		return nil, err
	}
	if fence.SchemaVersion != releaseProtocol || fence.DeploymentID != owner.DeploymentID || fence.Revision != owner.Revision {
		return nil, errors.New("发布维护标记与占用记录不匹配")
	}
	if err := verifyReleaseFence(&fence, catalogPath); err != nil {
		return nil, err
	}
	return &fence, nil
}

func readReleaseState(root, deploymentID string) (*releaseState, error) {
	directory := filepath.Join(root, "deployments", deploymentID)
	if err := trustedReleaseDirectory(root, directory, 0); err != nil {
		return nil, err
	}
	var state releaseState
	if err := readReleaseJSON(filepath.Join(directory, "state.json"), &state, 0); err != nil {
		return nil, err
	}
	if state.SchemaVersion != 2 || state.DeploymentID != deploymentID || (state.Status != "running" && state.Status != "succeeded" && state.Status != "rolled_back") {
		return nil, errors.New("发布状态未核准启动，拒绝迁移或执行业务")
	}
	return &state, nil
}

func verifyReleaseFence(fence *releaseFence, catalogPath string) error {
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil || strings.TrimSpace(string(boot)) != fence.BootID {
		return errors.New("发布期间主机启动身份变化，需人工核对")
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	for path, expected := range map[string]string{executable: fence.RunnerSHA256, catalogPath: fence.CatalogSHA256} {
		digest, err := releaseFileDigest(path)
		if err != nil || digest != expected {
			return errors.New("发布维护标记的程序或配置摘要不匹配")
		}
	}
	return nil
}

func releaseFileDigest(path string) (string, error) {
	stream, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer stream.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, stream); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func trustedReleaseDirectory(anchor, path string, owner uint32) error {
	if !filepath.IsAbs(anchor) || filepath.Clean(anchor) != anchor {
		return errors.New("发布状态根目录必须为规范绝对路径")
	}
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o022 != 0 {
			return errors.New("发布状态父目录身份或权限无效")
		}
		identity, ok := info.Sys().(*syscall.Stat_t)
		if !ok || identity.Uid != owner {
			return errors.New("发布状态父目录不属于受信用户")
		}
		if current == anchor {
			return nil
		}
		if current == filepath.Dir(current) {
			return errors.New("发布状态路径超出受信根目录")
		}
	}
}

func readReleaseJSON(path string, target any, owner uint32) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() > 1<<20 {
		return errors.New("发布状态文件身份或权限无效")
	}
	identity, ok := info.Sys().(*syscall.Stat_t)
	if !ok || identity.Uid != owner || identity.Gid != 0 && owner == 0 || identity.Nlink != 1 {
		return errors.New("发布状态文件属主或链接数无效")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(content, target); err != nil {
		return errors.New("发布状态文件不是有效 JSON")
	}
	return nil
}

func releaseMaintenanceHandler(fence *releaseFence) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Cache-Control", "no-store")
		if request.Method == http.MethodGet && request.URL.Path == "/healthz" {
			response.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(response).Encode(map[string]any{
				"ok": true, "component": "runner", "version": buildinfo.Version,
				"revision": buildinfo.Revision, "releaseMaintenance": true,
				"releaseProtocol": releaseProtocol, "deploymentId": fence.DeploymentID,
				"schemaVersion": store.CurrentSchemaVersion(),
			})
			return
		}
		if request.Method == http.MethodGet && request.URL.Path == "/metrics" {
			response.Header().Set("Content-Type", "text/plain; version=0.0.4")
			_, _ = fmt.Fprintf(response, "areasong_ops_build_info{component=%q,version=%q,revision=%q} 1\nareasong_ops_release_maintenance 1\n", "runner", buildinfo.Version, buildinfo.Revision)
			return
		}
		http.Error(response, "控制面处于发布验收模式，请稍后刷新；请求未执行", http.StatusServiceUnavailable)
	})
}

func serveReleaseMaintenance(socketPath string, fence *releaseFence) error {
	listener, err := unixListener(socketPath)
	if err != nil {
		return err
	}
	defer listener.Close()
	server := &http.Server{Handler: releaseMaintenanceHandler(fence), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	result := make(chan error, 1)
	go func() { result <- server.Serve(listener) }()
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return server.Shutdown(shutdown)
	}
}
