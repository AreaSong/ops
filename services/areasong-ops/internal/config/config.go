package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

const schemaVersion = 2

var (
	namePattern       = regexp.MustCompile(`^[a-z][a-z0-9-]{1,39}$`)
	actionPattern     = regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`)
	stepPattern       = regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`)
	repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
)

type Catalog struct {
	SchemaVersion int                                `json:"schemaVersion"`
	Services      map[string]model.ServiceDefinition `json:"services"`
}

func Load(path string, requireRoot bool) (*Catalog, error) {
	if requireRoot {
		if err := verifySecureFile(path); err != nil {
			return nil, err
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取服务声明失败: %w", err)
	}
	var catalog Catalog
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&catalog); err != nil {
		return nil, fmt.Errorf("解析服务声明失败: %w", err)
	}
	if err := catalog.Validate(requireRoot); err != nil {
		return nil, err
	}
	return &catalog, nil
}

func (catalog *Catalog) Validate(requireRoot bool) error {
	if catalog.SchemaVersion != schemaVersion {
		return fmt.Errorf("不支持的服务声明版本: %d", catalog.SchemaVersion)
	}
	if len(catalog.Services) == 0 {
		return errors.New("服务声明不能为空")
	}
	for key, service := range catalog.Services {
		if key != service.Name || !namePattern.MatchString(key) {
			return fmt.Errorf("服务名称无效: %q", key)
		}
		if service.DisplayName == "" || service.Adapter == "" || !filepath.IsAbs(service.Adapter) {
			return fmt.Errorf("服务 %s 的名称或适配器无效", key)
		}
		if service.Template != "custom" && service.Template != "compose-service-v1" {
			return fmt.Errorf("服务 %s 的模板无效", key)
		}
		if service.Template == "compose-service-v1" {
			if err := validateComposeRuntime(key, service.Runtime, requireRoot); err != nil {
				return err
			}
		} else if service.Runtime != nil {
			return fmt.Errorf("自定义服务 %s 不应声明通用 Compose 运行配置", key)
		}
		if requireRoot {
			if err := verifySecureExecutable(service.Adapter); err != nil {
				return fmt.Errorf("服务 %s: %w", key, err)
			}
		}
		if len(service.Actions) == 0 {
			return fmt.Errorf("服务 %s 未声明任何能力", key)
		}
		for actionName, action := range service.Actions {
			if err := validateAction(key, actionName, action); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateComposeRuntime(service string, runtime *model.ComposeServiceRuntime, requireRoot bool) error {
	if runtime == nil {
		return fmt.Errorf("服务 %s 缺少通用 Compose 运行配置", service)
	}
	paths := []string{runtime.ControlledCompose, runtime.RuntimeCompose, runtime.EnvFile,
		runtime.ReleaseCatalog, runtime.PreparedReleaseDir, runtime.InspectExecutable}
	if runtime.RestoreDrillExecutable != "" {
		paths = append(paths, runtime.RestoreDrillExecutable)
	}
	if runtime.PrepareExecutable != "" {
		paths = append(paths, runtime.PrepareExecutable)
	}
	if runtime.UpdateExecutable != "" {
		paths = append(paths, runtime.UpdateExecutable)
	}
	paths = append(paths, runtime.BackupExecutables...)
	for _, path := range paths {
		if !filepath.IsAbs(path) {
			return fmt.Errorf("服务 %s 的受控路径必须是绝对路径", service)
		}
	}
	if runtime.ApplicationService == "" || runtime.ApplicationContainer == "" ||
		!repositoryPattern.MatchString(runtime.ReleaseRepository) {
		return fmt.Errorf("服务 %s 的 Compose 服务、容器或发布仓库无效", service)
	}
	parsed, err := url.Parse(runtime.HealthURL)
	if err != nil || parsed.Scheme != "http" || (parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "localhost") || parsed.User != nil {
		return fmt.Errorf("服务 %s 的健康地址必须是本机 HTTP 地址", service)
	}
	if requireRoot {
		executables := []string{runtime.InspectExecutable}
		executables = append(executables, runtime.BackupExecutables...)
		executables = append(executables, runtime.RestoreDrillExecutable, runtime.PrepareExecutable, runtime.UpdateExecutable)
		for _, executable := range executables {
			if executable != "" {
				if err := verifySecureExecutable(executable); err != nil {
					return fmt.Errorf("服务 %s: %w", service, err)
				}
			}
		}
	}
	return nil
}

func validateAction(service, name string, action model.ActionDefinition) error {
	if name != action.Name || !actionPattern.MatchString(name) {
		return fmt.Errorf("服务 %s 的动作名称无效: %q", service, name)
	}
	if action.DisplayName == "" || action.Impact == "" || action.Rollback == "" || action.Scope == "" {
		return fmt.Errorf("服务 %s 的动作 %s 缺少治理说明", service, name)
	}
	if !action.Enabled && strings.TrimSpace(action.DisabledReason) == "" {
		return fmt.Errorf("服务 %s 的未开放动作 %s 缺少禁用原因", service, name)
	}
	if action.ReadinessGate != "" && action.ReadinessGate != "prepared_release" {
		return fmt.Errorf("服务 %s 的动作 %s 准备门禁无效", service, name)
	}
	if action.TimeoutSeconds < 5 || action.TimeoutSeconds > 7200 {
		return fmt.Errorf("服务 %s 的动作 %s 超时范围无效", service, name)
	}
	if len(action.Steps) == 0 || len(action.Steps) > 12 {
		return fmt.Errorf("服务 %s 的动作 %s 阶段数量无效", service, name)
	}
	seen := make(map[string]struct{}, len(action.Steps))
	for _, step := range action.Steps {
		if !stepPattern.MatchString(step) {
			return fmt.Errorf("服务 %s 的动作 %s 包含无效阶段", service, name)
		}
		if _, exists := seen[step]; exists {
			return fmt.Errorf("服务 %s 的动作 %s 包含重复阶段", service, name)
		}
		seen[step] = struct{}{}
	}
	switch action.Risk {
	case model.RiskReadOnly:
		if action.ConfirmationTemplate != "" {
			return fmt.Errorf("只读动作 %s/%s 不应要求确认短语", service, name)
		}
	case model.RiskLow, model.RiskMedium, model.RiskHigh:
		if action.ConfirmationTemplate == "" {
			return fmt.Errorf("变更动作 %s/%s 必须要求确认短语", service, name)
		}
	default:
		return fmt.Errorf("服务 %s 的动作 %s 风险等级无效", service, name)
	}
	if action.TargetMode == "allowlist" && len(action.AllowedTargets) == 0 && action.Enabled {
		return fmt.Errorf("服务 %s 的动作 %s 缺少允许目标", service, name)
	}
	return nil
}

func (catalog *Catalog) ServiceNames() []string {
	names := make([]string, 0, len(catalog.Services))
	for name := range catalog.Services {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func verifySecureFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("检查文件失败: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("文件必须是普通非符号链接: %s", path)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 {
		return fmt.Errorf("文件必须由 root 拥有: %s", path)
	}
	if info.Mode().Perm() != 0o600 {
		return fmt.Errorf("文件权限必须为 0600: %s", path)
	}
	return nil
}

func verifySecureExecutable(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("检查适配器失败: %w", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || !ok || stat.Uid != 0 {
		return fmt.Errorf("适配器必须是 root 拥有的普通非符号链接: %s", path)
	}
	if info.Mode().Perm()&0o022 != 0 || info.Mode().Perm()&0o100 == 0 {
		return fmt.Errorf("适配器权限不安全: %s", path)
	}
	return nil
}
