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

const schemaVersion = 3

var (
	namePattern       = regexp.MustCompile(`^[a-z][a-z0-9-]{1,39}$`)
	actionPattern     = regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`)
	stepPattern       = regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`)
	objectIDPattern   = regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}:[a-z][a-z0-9-]{1,39}$`)
	labelNamePattern  = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]{0,63}$`)
	labelValuePattern = regexp.MustCompile(`^[a-zA-Z0-9_.:-]{1,128}$`)
	alertNamePattern  = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{1,79}$`)
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
	objectIDs := make(map[string]string, len(catalog.Services))
	for key, service := range catalog.Services {
		if key != service.Name || !namePattern.MatchString(key) {
			return fmt.Errorf("服务名称无效: %q", key)
		}
		if service.DisplayName == "" || service.Adapter == "" || !filepath.IsAbs(service.Adapter) {
			return fmt.Errorf("服务 %s 的名称或适配器无效", key)
		}
		if !objectIDPattern.MatchString(service.ObjectID) {
			return fmt.Errorf("服务 %s 的稳定对象标识无效", key)
		}
		if owner, exists := objectIDs[service.ObjectID]; exists {
			return fmt.Errorf("服务 %s 与 %s 使用了重复对象标识", key, owner)
		}
		objectIDs[service.ObjectID] = key
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
		if err := validateAlertPolicy(key, service); err != nil {
			return err
		}
	}
	return nil
}

func validateAlertPolicy(name string, service model.ServiceDefinition) error {
	requiresPolicy := false
	for _, action := range service.Actions {
		if action.Enabled && model.ActionRequiresObservation(action) {
			requiresPolicy = true
			break
		}
	}
	policy := service.AlertPolicy
	if !requiresPolicy && len(policy.Matchers) == 0 && len(policy.BlockingAlerts) == 0 &&
		len(policy.MaintenanceAlerts) == 0 {
		return nil
	}
	if len(policy.Matchers) == 0 || len(policy.Matchers) > 4 || policy.Matchers["service"] != name {
		return fmt.Errorf("服务 %s 的告警策略必须使用精确 service matcher", name)
	}
	for key, value := range policy.Matchers {
		if !labelNamePattern.MatchString(key) || !labelValuePattern.MatchString(value) {
			return fmt.Errorf("服务 %s 的告警 matcher 无效", name)
		}
	}
	blocking, err := validateAlertNames(name, "阻断", policy.BlockingAlerts)
	if err != nil {
		return err
	}
	if requiresPolicy && len(blocking) == 0 {
		return fmt.Errorf("服务 %s 的生产变更缺少阻断告警映射", name)
	}
	maintenance, err := validateAlertNames(name, "维护静默", policy.MaintenanceAlerts)
	if err != nil {
		return err
	}
	for alertName := range maintenance {
		if _, exists := blocking[alertName]; !exists {
			return fmt.Errorf("服务 %s 的维护静默告警 %s 必须同时是阻断告警", name, alertName)
		}
	}
	return nil
}

func validateAlertNames(service, purpose string, names []string) (map[string]struct{}, error) {
	if len(names) > 24 {
		return nil, fmt.Errorf("服务 %s 的%s告警数量过多", service, purpose)
	}
	result := make(map[string]struct{}, len(names))
	for _, name := range names {
		if !alertNamePattern.MatchString(name) {
			return nil, fmt.Errorf("服务 %s 的%s告警名称无效", service, purpose)
		}
		if _, exists := result[name]; exists {
			return nil, fmt.Errorf("服务 %s 的%s告警名称重复", service, purpose)
		}
		result[name] = struct{}{}
	}
	return result, nil
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
	for phase, semantics := range action.PhaseSemantics {
		if _, exists := seen[phase]; !exists {
			return fmt.Errorf("服务 %s 的动作 %s 为未知阶段 %s 声明语义", service, name, phase)
		}
		switch semantics.Effect {
		case "observe", "artifact_write", "runtime_mutation", "data_mutation":
		default:
			return fmt.Errorf("服务 %s 的动作 %s 阶段 %s 副作用无效", service, name, phase)
		}
		switch semantics.FailurePolicy {
		case "fail", "rollback", "needs_attention":
		default:
			return fmt.Errorf("服务 %s 的动作 %s 阶段 %s 失败策略无效", service, name, phase)
		}
		if semantics.FailurePolicy == "rollback" && semantics.RecoveryPhase == "" {
			return fmt.Errorf("服务 %s 的动作 %s 阶段 %s 缺少恢复阶段", service, name, phase)
		}
	}
	requiresObservation := model.ActionRequiresObservation(action)
	if requiresObservation && (action.ObservationSeconds < 60 || action.ObservationSeconds > 86400) {
		return fmt.Errorf("生产变更动作 %s/%s 的观察时间必须为 60 到 86400 秒", service, name)
	}
	if !requiresObservation && action.ObservationSeconds != 0 {
		return fmt.Errorf("非生产变更动作 %s/%s 不应声明观察时间", service, name)
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
