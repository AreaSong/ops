package updater

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type SystemdController struct {
	SocketPath string
}

func (controller SystemdController) Restart(ctx context.Context, unit string) error {
	if !unitPattern.MatchString(unit) {
		return errors.New("Runner systemd unit 名称无效")
	}
	command := exec.CommandContext(ctx, "/usr/bin/systemctl", "restart", unit)
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("重启 Runner 失败: %s", boundedOutput(output))
	}
	return nil
}

func (controller SystemdController) WaitIdentity(
	ctx context.Context,
	unit, binaryPath, version, revision string,
	timeout time.Duration,
) error {
	if controller.SocketPath == "" || !unitPattern.MatchString(unit) ||
		!filepath.IsAbs(binaryPath) || filepath.Clean(binaryPath) != binaryPath ||
		version == "" || revision == "" || timeout <= 0 {
		return errors.New("Runner 健康核验参数不完整")
	}
	waitContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "unix", controller.SocketPath)
		},
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	var lastError error
	for {
		if err := controller.checkRuntimeIdentity(waitContext, client, unit, binaryPath, version, revision); err == nil {
			return nil
		} else {
			lastError = err
		}
		select {
		case <-waitContext.Done():
			return fmt.Errorf("Runner 健康与构建身份核验超时: %w", lastError)
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func (controller SystemdController) checkRuntimeIdentity(
	ctx context.Context,
	client *http.Client,
	unit, binaryPath, version, revision string,
) error {
	pid, err := systemdMainPID(ctx, unit)
	if err != nil {
		return err
	}
	if err := processExecutableMatches(pid, binaryPath); err != nil {
		return err
	}
	return checkIdentity(ctx, client, version, revision)
}

func systemdMainPID(ctx context.Context, unit string) (int, error) {
	command := exec.CommandContext(ctx, "/usr/bin/systemctl", "show", "--property=MainPID", "--value", unit)
	output, err := command.CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("读取 Runner MainPID 失败: %s", boundedOutput(output))
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(output)))
	if err != nil || pid <= 0 {
		return 0, errors.New("Runner systemd MainPID 无效")
	}
	return pid, nil
}

func processExecutableMatches(pid int, binaryPath string) error {
	executable, err := os.Readlink(filepath.Join("/proc", strconv.Itoa(pid), "exe"))
	if err != nil {
		return fmt.Errorf("读取 Runner 进程可执行文件失败: %w", err)
	}
	executable = strings.TrimSuffix(executable, " (deleted)")
	if filepath.Clean(executable) != binaryPath {
		return fmt.Errorf("Runner MainPID 可执行文件不匹配: %s", executable)
	}
	return nil
}

func checkIdentity(
	ctx context.Context,
	client *http.Client,
	version, revision string,
) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://runner/healthz", nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("Runner health 返回 %d", response.StatusCode)
	}
	var health struct {
		OK        bool   `json:"ok"`
		Component string `json:"component"`
		Version   string `json:"version"`
		Revision  string `json:"revision"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 64<<10))
	if err := decoder.Decode(&health); err != nil {
		return err
	}
	if !health.OK || health.Component != "runner" ||
		health.Version != version || health.Revision != revision {
		return fmt.Errorf("Runner 构建身份不匹配: %s@%s", health.Version, health.Revision)
	}
	return nil
}

func boundedOutput(output []byte) string {
	value := strings.TrimSpace(string(output))
	if len(value) > 512 {
		value = value[len(value)-512:]
	}
	if value == "" {
		return "systemctl 未返回详情"
	}
	return value
}
