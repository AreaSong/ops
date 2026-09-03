package config

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

const (
	ControlPlaneHostname = "ops.areasong.top"
	legacySchemaVersion  = 3
	schemaVersion        = 4

	RunnerUpdateArtifactRoot    = "/var/lib/areasong-ops/runner-updates/incoming"
	RunnerUpdateBinaryPath      = "/usr/local/libexec/areasong-ops/runner/areasong-ops-runner"
	RunnerUpdateUnitName        = "areasong-ops-runner.service"
	RunnerUpdateUpdaterUnitName = "areasong-ops-runner-update@.service"
	TrafficAdapterPath          = model.TrafficAdapterPath
)

var (
	namePattern            = regexp.MustCompile(`^[a-z][a-z0-9-]{1,39}$`)
	actionPattern          = regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`)
	stepPattern            = regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`)
	objectIDPattern        = regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}:[a-z][a-z0-9-]{1,39}$`)
	labelNamePattern       = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]{0,63}$`)
	labelValuePattern      = regexp.MustCompile(`^[a-zA-Z0-9_.:-]{1,128}$`)
	alertNamePattern       = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{1,79}$`)
	repositoryPattern      = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	artifactRolePattern    = regexp.MustCompile(`^[a-z][a-z0-9-]{1,63}$`)
	composeServicePattern  = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,62}$`)
	trafficHostnamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$`)
)

type Catalog struct {
	SchemaVersion  int                                `json:"schemaVersion"`
	Adapters       map[string]model.AdapterDefinition `json:"adapters,omitempty"`
	Services       map[string]model.ServiceDefinition `json:"services"`
	AutomaticTasks map[string]model.ServiceDefinition `json:"automaticTasks,omitempty"`
	Access         *AccessPolicy                      `json:"access,omitempty"`
	Fleet          *FleetPolicy                       `json:"fleet,omitempty"`
	Extensions     *ExtensionPolicy                   `json:"extensions,omitempty"`
	Terminal       *TerminalPolicy                    `json:"terminal,omitempty"`
	Files          *FilePolicy                        `json:"files,omitempty"`
	RunnerUpdate   *RunnerUpdatePolicy                `json:"runnerUpdate,omitempty"`
	Kubernetes     map[string]model.KubernetesTarget  `json:"kubernetes,omitempty"`
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
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return nil, fmt.Errorf("解析服务声明失败: %w", err)
	}
	var catalog Catalog
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&catalog); err != nil {
		return nil, fmt.Errorf("解析服务声明失败: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return nil, fmt.Errorf("解析服务声明失败: %w", err)
	}
	if err := catalog.Validate(requireRoot); err != nil {
		return nil, err
	}
	return &catalog, nil
}

func (catalog *Catalog) Validate(requireRoot bool) error {
	if catalog.SchemaVersion != legacySchemaVersion && catalog.SchemaVersion != schemaVersion {
		return fmt.Errorf("不支持的服务声明版本: %d", catalog.SchemaVersion)
	}
	if len(catalog.Services) == 0 {
		return errors.New("服务声明不能为空")
	}
	if catalog.SchemaVersion == schemaVersion && (catalog.Access == nil || !catalog.Access.Enforced) {
		return errors.New("schema 4 必须启用访问策略")
	}
	if catalog.Access != nil {
		if err := catalog.Access.Normalize(); err != nil {
			return err
		}
		if catalog.Access.DefaultTenant == "" {
			return errors.New("访问策略缺少默认租户")
		}
	}
	for name, target := range catalog.Kubernetes {
		if strings.TrimSpace(name) == "" || strings.TrimSpace(target.Cluster) == "" ||
			strings.TrimSpace(target.Context) == "" || strings.TrimSpace(target.Namespace) == "" {
			return fmt.Errorf("Kubernetes 目标 %s 声明不完整", name)
		}
		if target.TenantID == "" && catalog.Access != nil {
			target.TenantID = catalog.Access.DefaultTenant
		}
		if catalog.Access != nil {
			if _, ok := catalog.Access.Tenants[target.TenantID]; !ok {
				return fmt.Errorf("Kubernetes 目标 %s 引用了未知租户 %s", name, target.TenantID)
			}
		}
		catalog.Kubernetes[name] = target
	}
	if catalog.Fleet != nil && catalog.Fleet.HeartbeatTimeoutSeconds != 0 &&
		(catalog.Fleet.HeartbeatTimeoutSeconds < 10 || catalog.Fleet.HeartbeatTimeoutSeconds > 3600) {
		return errors.New("Runner 心跳超时必须为 10 到 3600 秒")
	}
	if catalog.Fleet != nil {
		if catalog.Fleet.HeartbeatMaxSkewSeconds == 0 {
			catalog.Fleet.HeartbeatMaxSkewSeconds = 300
		}
		if catalog.Fleet.HeartbeatMaxSkewSeconds < 5 || catalog.Fleet.HeartbeatMaxSkewSeconds > 900 {
			return errors.New("Runner 心跳最大时钟偏差必须为 5 到 900 秒")
		}
		for runnerID, encoded := range catalog.Fleet.RunnerPublicKeys {
			if !catalog.hasRunner(runnerID) {
				return fmt.Errorf("Runner 心跳公钥引用了未登记 Runner: %s", runnerID)
			}
			key, err := base64.StdEncoding.DecodeString(encoded)
			if err != nil || len(key) != ed25519.PublicKeySize {
				return fmt.Errorf("Runner %s 心跳公钥无效", runnerID)
			}
		}
		if err := validateFleetTransport(catalog.Fleet, requireRoot); err != nil {
			return err
		}
	}
	if catalog.Fleet != nil && (catalog.Fleet.Enabled || len(catalog.Fleet.Inventory.Servers) > 0 || len(catalog.Fleet.Inventory.Runners) > 0) {
		if err := catalog.Fleet.Inventory.Validate(); err != nil {
			return fmt.Errorf("Fleet 清单无效: %w", err)
		}
	}
	if catalog.Extensions != nil && catalog.Extensions.Enabled {
		if catalog.Extensions.Sandbox == "" {
			return errors.New("启用扩展必须显式选择沙箱")
		}
		if catalog.Extensions.Sandbox != "wasm" {
			return errors.New("当前扩展执行链路只允许 wasm 沙箱")
		}
		if !catalog.Extensions.RequireSignature {
			return errors.New("启用扩展必须强制校验签名")
		}
		if catalog.Extensions.MaxPackageBytes == 0 {
			catalog.Extensions.MaxPackageBytes = 16 << 20
		}
		if catalog.Extensions.MaxPackageBytes < 1 || catalog.Extensions.MaxPackageBytes > 64<<20 {
			return errors.New("扩展包大小限制无效")
		}
		if catalog.Extensions.MaxInputBytes == 0 {
			catalog.Extensions.MaxInputBytes = 64 << 10
		}
		if catalog.Extensions.MaxInputBytes < 2 || catalog.Extensions.MaxInputBytes > 1<<20 {
			return errors.New("扩展输入大小限制无效")
		}
		if catalog.Extensions.MaxOutputBytes == 0 {
			catalog.Extensions.MaxOutputBytes = 64 << 10
		}
		if catalog.Extensions.MaxOutputBytes < 1 || catalog.Extensions.MaxOutputBytes > 1<<20 {
			return errors.New("扩展输出大小限制无效")
		}
		if catalog.Extensions.MaxExecutionSeconds == 0 {
			catalog.Extensions.MaxExecutionSeconds = 15
		}
		if catalog.Extensions.MaxExecutionSeconds < 1 || catalog.Extensions.MaxExecutionSeconds > 60 {
			return errors.New("扩展执行超时必须为 1 到 60 秒")
		}
		if catalog.Extensions.MaxMemoryPages == 0 {
			catalog.Extensions.MaxMemoryPages = 256
		}
		if catalog.Extensions.MaxMemoryPages < 16 || catalog.Extensions.MaxMemoryPages > 1024 {
			return errors.New("扩展内存上限必须为 16 到 1024 个 WebAssembly page")
		}
		if err := validatePublisherKeys(catalog.Extensions); err != nil {
			return err
		}
	}
	if catalog.Terminal != nil && catalog.Terminal.Enabled {
		if catalog.Terminal.MaxSessionSeconds == 0 {
			catalog.Terminal.MaxSessionSeconds = 900
		}
		if catalog.Terminal.MaxSessionSeconds < 30 || catalog.Terminal.MaxSessionSeconds > 3600 {
			return errors.New("终端会话时长必须为 30 到 3600 秒")
		}
		for name, command := range catalog.Terminal.Commands {
			if !namePattern.MatchString(name) || !filepath.IsAbs(command.Executable) || command.TimeoutSeconds < 1 || command.TimeoutSeconds > 300 {
				return fmt.Errorf("终端命令 %s 声明无效", name)
			}
			if !command.ReadOnly && !catalog.Terminal.BreakGlass {
				return fmt.Errorf("终端命令 %s 需要 break-glass 策略", name)
			}
			command.Name = name
			catalog.Terminal.Commands[name] = command
		}
		if catalog.Terminal.BreakGlass {
			if catalog.Terminal.ShellExecutable == "" {
				catalog.Terminal.ShellExecutable = "/bin/bash"
			}
			if catalog.Terminal.ShellWorkingDir == "" {
				catalog.Terminal.ShellWorkingDir = "/var/lib/areasong-ops/shell"
			}
			if !filepath.IsAbs(catalog.Terminal.ShellExecutable) || !filepath.IsAbs(catalog.Terminal.ShellWorkingDir) {
				return errors.New("紧急终端 shell 或工作目录必须为绝对路径")
			}
			if requireRoot {
				if err := verifySecureExecutable(catalog.Terminal.ShellExecutable); err != nil {
					return fmt.Errorf("紧急终端 shell: %w", err)
				}
				if err := verifySecureDirectoryTree(catalog.Terminal.ShellWorkingDir); err != nil {
					return fmt.Errorf("紧急终端工作目录: %w", err)
				}
			}
		}
	}
	if catalog.Files != nil && catalog.Files.Enabled {
		if catalog.Files.MaxFileBytes == 0 {
			catalog.Files.MaxFileBytes = 1 << 20
		}
		if catalog.Files.MaxFileBytes < 1 || catalog.Files.MaxFileBytes > 16<<20 {
			return errors.New("文件管理大小限制无效")
		}
		for name, root := range catalog.Files.Roots {
			if !namePattern.MatchString(name) || !filepath.IsAbs(root) {
				return fmt.Errorf("文件根目录 %s 声明无效", name)
			}
		}
	}
	if catalog.RunnerUpdate != nil && catalog.RunnerUpdate.Enabled {
		if catalog.SchemaVersion != schemaVersion || catalog.Access == nil || !catalog.Access.Enforced {
			return errors.New("Runner 自更新必须使用启用访问策略的 schema 4")
		}
		if !namePattern.MatchString(catalog.RunnerUpdate.RunnerID) || !filepath.IsAbs(catalog.RunnerUpdate.ArtifactRoot) {
			return errors.New("Runner 更新策略缺少有效的 runnerId 或 artifactRoot")
		}
		if filepath.Clean(catalog.RunnerUpdate.ArtifactRoot) != RunnerUpdateArtifactRoot {
			return errors.New("Runner 更新 artifactRoot 无效")
		}
		if catalog.RunnerUpdate.BinaryPath == "" {
			catalog.RunnerUpdate.BinaryPath = RunnerUpdateBinaryPath
		}
		if catalog.RunnerUpdate.BinaryPath != RunnerUpdateBinaryPath {
			return errors.New("Runner 更新 binaryPath 无效")
		}
		if catalog.RunnerUpdate.UnitName == "" {
			catalog.RunnerUpdate.UnitName = RunnerUpdateUnitName
		}
		if catalog.RunnerUpdate.UnitName != RunnerUpdateUnitName {
			return errors.New("Runner 更新 unitName 无效")
		}
		if catalog.RunnerUpdate.UpdaterUnitName == "" {
			catalog.RunnerUpdate.UpdaterUnitName = RunnerUpdateUpdaterUnitName
		}
		if catalog.RunnerUpdate.UpdaterUnitName != RunnerUpdateUpdaterUnitName {
			return errors.New("Runner 更新 updaterUnitName 无效")
		}
		if !namePattern.MatchString(catalog.RunnerUpdate.Publisher) {
			return errors.New("Runner 更新策略缺少发布者")
		}
		if catalog.RunnerUpdate.ManifestPurpose == "" {
			catalog.RunnerUpdate.ManifestPurpose = RunnerUpdateManifestPurpose
		}
		if catalog.RunnerUpdate.ManifestSchema == 0 {
			catalog.RunnerUpdate.ManifestSchema = RunnerUpdateManifestSchema
		}
		if catalog.RunnerUpdate.ManifestGOOS == "" {
			catalog.RunnerUpdate.ManifestGOOS = RunnerUpdateManifestGOOS
		}
		if catalog.RunnerUpdate.ManifestGOARCH == "" {
			catalog.RunnerUpdate.ManifestGOARCH = RunnerUpdateManifestGOARCH
		}
		if catalog.RunnerUpdate.ManifestPurpose != RunnerUpdateManifestPurpose ||
			catalog.RunnerUpdate.ManifestSchema != RunnerUpdateManifestSchema ||
			catalog.RunnerUpdate.ManifestGOOS != RunnerUpdateManifestGOOS ||
			catalog.RunnerUpdate.ManifestGOARCH != RunnerUpdateManifestGOARCH {
			return errors.New("Runner 更新签名 manifest 绑定无效")
		}
		encodedKey := catalog.RunnerUpdate.TrustedPublisherKeys[catalog.RunnerUpdate.Publisher]
		publicKey, decodeErr := base64.StdEncoding.Strict().DecodeString(encodedKey)
		if decodeErr != nil || len(publicKey) != ed25519.PublicKeySize {
			return errors.New("Runner 更新发布者 Ed25519 公钥无效")
		}
		if catalog.RunnerUpdate.HealthTimeoutSeconds == 0 {
			catalog.RunnerUpdate.HealthTimeoutSeconds = 30
		}
		if catalog.RunnerUpdate.HealthTimeoutSeconds < 1 || catalog.RunnerUpdate.HealthTimeoutSeconds > 300 {
			return errors.New("Runner 更新健康检查超时无效")
		}
		if catalog.RunnerUpdate.MaxArtifactBytes == 0 {
			catalog.RunnerUpdate.MaxArtifactBytes = 256 << 20
		}
		if catalog.RunnerUpdate.MaxArtifactBytes < 1 || catalog.RunnerUpdate.MaxArtifactBytes > 1<<30 {
			return errors.New("Runner 更新制品大小限制无效")
		}
		if catalog.RunnerUpdate.LeaseSeconds == 0 {
			catalog.RunnerUpdate.LeaseSeconds = RunnerUpdateLeaseSeconds
		}
		if catalog.RunnerUpdate.LeaseSeconds < 30 || catalog.RunnerUpdate.LeaseSeconds > 900 {
			return errors.New("Runner 更新执行租约无效")
		}
		if !catalog.hasRunner(catalog.RunnerUpdate.RunnerID) {
			return errors.New("Runner 更新目标未登记在 Fleet 清单")
		}
		if runnerUpdateManagerCount(catalog.Access, catalog.RunnerUpdate.RunnerID, time.Now().UTC()) < 2 {
			return errors.New("Runner 自更新至少需要两名独立授权主体")
		}
		if requireRoot {
			if err := verifySecureDirectoryTree(catalog.RunnerUpdate.ArtifactRoot); err != nil {
				return fmt.Errorf("Runner 更新 artifactRoot: %w", err)
			}
			if err := verifySecureExecutable(catalog.RunnerUpdate.BinaryPath); err != nil {
				return fmt.Errorf("Runner 更新 binaryPath: %w", err)
			}
		}
	}
	if err := catalog.validateRunnerFleetUpdate(); err != nil {
		return err
	}
	if err := catalog.validateRemoteWorker(requireRoot); err != nil {
		return err
	}
	if catalog.SchemaVersion == schemaVersion && len(catalog.Adapters) == 0 {
		return errors.New("受信适配器注册表不能为空")
	}
	if err := catalog.validateAdapters(requireRoot); err != nil {
		return err
	}
	objectIDs := map[string]string{
		"access":       "access policy",
		"audit":        "audit records",
		"auto-updates": "automatic update policy",
		"batch":        "batch operations",
		"credentials":  "credential metadata",
		"events":       "event stream",
		"extensions":   "extension policy",
		"files":        "managed files",
		"fleet":        "fleet inventory",
		"kubernetes":   "Kubernetes policy",
		"terminal":     "restricted terminal",
	}
	if catalog.Fleet != nil {
		for _, server := range catalog.Fleet.Inventory.Servers {
			objectIDs["fleet:"+server.ID] = "fleet server"
			objectIDs["server:"+server.ID] = "server"
		}
		for _, runner := range catalog.Fleet.Inventory.Runners {
			objectIDs["runner:"+runner.ID] = "runner"
		}
	}
	for cluster := range catalog.Kubernetes {
		objectIDs["kubernetes:"+cluster] = "Kubernetes cluster"
	}
	if catalog.RunnerUpdate != nil && catalog.RunnerUpdate.Enabled {
		objectIDs["runner:"+catalog.RunnerUpdate.RunnerID] = "runner update"
	}
	for key, service := range catalog.Services {
		service = applyMetadataDefaults(service, "service")
		if service.TenantID == "" && catalog.Access != nil {
			service.TenantID = catalog.Access.DefaultTenant
		}
		if err := catalog.validateObject(key, &service, "service", objectIDs, requireRoot); err != nil {
			return err
		}
		catalog.Services[key] = service
	}
	for key, task := range catalog.AutomaticTasks {
		task = applyMetadataDefaults(task, "automatic_task")
		if task.TenantID == "" && catalog.Access != nil {
			task.TenantID = catalog.Access.DefaultTenant
		}
		if err := catalog.validateObject(key, &task, "automatic_task", objectIDs, requireRoot); err != nil {
			return err
		}
		if task.AutomaticTask == nil || task.AutomaticTask.Schedule == "" ||
			task.AutomaticTask.ScheduleSource != "cron" || task.AutomaticTask.FreshnessSeconds < 60 ||
			task.AutomaticTask.FreshnessSeconds > 86400 {
			return fmt.Errorf("自动任务 %s 的调度或新鲜度声明无效", key)
		}
		if task.Runtime != nil || task.Template != "automatic-task-v1" {
			return fmt.Errorf("自动任务 %s 的模板或运行配置无效", key)
		}
		catalog.AutomaticTasks[key] = task
	}
	if err := catalog.validateAccessPolicy(objectIDs); err != nil {
		return err
	}
	return nil
}

func (catalog *Catalog) validateRunnerFleetUpdate() error {
	policy := catalog.RunnerUpdate
	if policy == nil || !policy.FleetEnabled {
		return nil
	}
	if !policy.Enabled || catalog.SchemaVersion != schemaVersion || catalog.Access == nil || !catalog.Access.Enforced {
		return errors.New("Runner Fleet 自更新必须启用 schema 4、访问策略和 Runner 自更新")
	}
	if catalog.Fleet == nil || !catalog.Fleet.Enabled || !catalog.Fleet.AllowRemoteRunners ||
		!catalog.Fleet.RequiremTLS || !catalog.Fleet.RequireSignedHeartbeat {
		return errors.New("Runner Fleet 自更新必须启用远程 Fleet、mTLS 和签名心跳")
	}
	now := time.Now().UTC()
	for _, node := range catalog.Fleet.Inventory.Runners {
		if !slices.Contains(node.Capabilities, "runner-update") {
			return fmt.Errorf("Runner Fleet 自更新目标 %s 缺少 runner-update 能力", node.ID)
		}
		if node.CertificateFingerprint == "" {
			return fmt.Errorf("Runner Fleet 自更新目标 %s 缺少固定 mTLS 指纹", node.ID)
		}
		key := catalog.Fleet.RunnerPublicKeys[node.ID]
		if key == "" {
			key = node.HeartbeatPublicKey
		}
		decoded, err := base64.StdEncoding.Strict().DecodeString(strings.TrimSpace(key))
		if err != nil || len(decoded) != ed25519.PublicKeySize {
			return fmt.Errorf("Runner Fleet 自更新目标 %s 缺少有效心跳公钥", node.ID)
		}
		if runnerUpdateManagerCount(catalog.Access, node.ID, now) < 4 {
			return fmt.Errorf("Runner Fleet 自更新目标 %s 至少需要四名独立授权主体", node.ID)
		}
	}
	if len(catalog.Fleet.Inventory.Runners) == 0 {
		return errors.New("Runner Fleet 自更新至少需要一个显式登记的 Runner")
	}
	return nil
}

func validateFleetTransport(policy *FleetPolicy, requireRoot bool) error {
	if policy == nil {
		return nil
	}
	if !policy.AllowRemoteRunners {
		if strings.TrimSpace(policy.MTLSListenAddress) != "" ||
			strings.TrimSpace(policy.MTLSCertificateFile) != "" ||
			strings.TrimSpace(policy.MTLSKeyFile) != "" ||
			strings.TrimSpace(policy.MTLSClientCAFile) != "" {
			return errors.New("禁止远程 Runner 时不得配置 mTLS 远程监听器")
		}
		return nil
	}
	if !policy.RequiremTLS {
		return errors.New("允许远程 Runner 时必须启用 mTLS")
	}
	if strings.TrimSpace(policy.MTLSListenAddress) == "" {
		return errors.New("启用远程 Runner mTLS 时必须声明监听地址")
	}
	if _, _, err := net.SplitHostPort(policy.MTLSListenAddress); err != nil {
		return fmt.Errorf("远程 Runner mTLS 监听地址无效: %w", err)
	}
	for name, path := range map[string]string{
		"证书":     policy.MTLSCertificateFile,
		"私钥":     policy.MTLSKeyFile,
		"客户端 CA": policy.MTLSClientCAFile,
	} {
		if err := validateFleetTLSFile(name, path, requireRoot); err != nil {
			return err
		}
	}
	// Remote heartbeats must be signed even when an older catalog omitted the
	// explicit switch. Mutating the normalized policy keeps runtime behavior
	// and configuration review semantics aligned.
	policy.RequireSignedHeartbeat = true
	return nil
}

func (catalog *Catalog) validateRemoteWorker(requireRoot bool) error {
	if catalog.Fleet == nil || catalog.Fleet.RemoteWorker == nil || !catalog.Fleet.RemoteWorker.Enabled {
		return nil
	}
	policy := catalog.Fleet.RemoteWorker
	if !catalog.Fleet.Enabled {
		return errors.New("远程 Runner worker 必须启用 Fleet")
	}
	if catalog.RunnerUpdate == nil || !catalog.RunnerUpdate.Enabled {
		return errors.New("远程 Runner worker 必须启用 Runner 更新策略")
	}
	if !namePattern.MatchString(policy.RunnerID) || !catalog.hasRunner(policy.RunnerID) {
		return errors.New("远程 Runner worker 的 runnerId 未登记")
	}
	if policy.RunnerID != catalog.RunnerUpdate.RunnerID {
		return errors.New("远程 Runner worker 与本地 Runner 更新身份不一致")
	}
	endpoint, err := url.Parse(strings.TrimSpace(policy.ControlPlaneURL))
	if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.User != nil ||
		(endpoint.Path != "" && endpoint.Path != "/") || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return errors.New("远程 Runner worker 控制面地址必须是无路径、认证或查询参数的 HTTPS origin")
	}
	policy.ControlPlaneURL = strings.TrimRight(endpoint.String(), "/")
	if policy.PollIntervalSeconds == 0 {
		policy.PollIntervalSeconds = 2
	}
	if policy.PollIntervalSeconds < 1 || policy.PollIntervalSeconds > 60 {
		return errors.New("远程 Runner worker 轮询间隔必须为 1 到 60 秒")
	}
	if policy.HeartbeatIntervalSeconds == 0 {
		policy.HeartbeatIntervalSeconds = 30
	}
	if policy.HeartbeatIntervalSeconds < 5 ||
		policy.HeartbeatIntervalSeconds >= catalog.Fleet.HeartbeatTimeoutSeconds {
		return errors.New("远程 Runner worker 心跳间隔必须至少 5 秒且小于心跳超时")
	}
	for name, path := range map[string]string{
		"客户端证书":  policy.MTLSCertificateFile,
		"客户端私钥":  policy.MTLSKeyFile,
		"控制面 CA": policy.ControlPlaneCAFile,
		"心跳签名私钥": policy.HeartbeatPrivateKeyFile,
	} {
		path = strings.TrimSpace(path)
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return fmt.Errorf("远程 Runner worker %s路径必须是规范绝对路径", name)
		}
		if requireRoot {
			if err := verifySecureFile(path); err != nil {
				return fmt.Errorf("远程 Runner worker %s: %w", name, err)
			}
		}
	}
	key := catalog.Fleet.RunnerPublicKeys[policy.RunnerID]
	if key == "" {
		for _, node := range catalog.Fleet.Inventory.Runners {
			if node.ID == policy.RunnerID {
				key = node.HeartbeatPublicKey
				break
			}
		}
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(strings.TrimSpace(key))
	if err != nil || len(decoded) != ed25519.PublicKeySize {
		return errors.New("远程 Runner worker 缺少匹配的心跳公钥")
	}
	catalog.Fleet.RequireSignedHeartbeat = true
	return nil
}

func validateFleetTLSFile(name, path string, requireRoot bool) error {
	path = strings.TrimSpace(path)
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return fmt.Errorf("远程 Runner mTLS %s路径必须是规范绝对路径", name)
	}
	if !requireRoot {
		return nil
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("检查远程 Runner mTLS %s失败: %w", name, err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("远程 Runner mTLS %s必须是普通非符号链接", name)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 || info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("远程 Runner mTLS %s属主或权限不安全", name)
	}
	return nil
}

func (catalog *Catalog) validateAccessPolicy(objectIDs map[string]string) error {
	policy := catalog.Access
	if policy == nil {
		return nil
	}
	defaultTenant, ok := policy.Tenants[policy.DefaultTenant]
	if !ok || (defaultTenant.Status != "" && defaultTenant.Status != "active") {
		return errors.New("访问策略的默认租户不存在或未启用")
	}
	for actor, principal := range policy.Principals {
		if !IsAccessHash(actor) {
			return fmt.Errorf("访问主体 %s 未规范化为 SHA-256", actor)
		}
		if principal.Email != "" && AccessHashForEmail(principal.Email) != principal.EmailHash {
			return fmt.Errorf("访问主体 %s 的邮箱与哈希不一致", actor)
		}
		if _, ok := policy.Tenants[principal.TenantID]; !ok {
			return fmt.Errorf("访问主体 %s 引用了未知租户", actor)
		}
		for _, roleID := range principal.Roles {
			if _, ok := policy.Roles[roleID]; !ok {
				return fmt.Errorf("访问主体 %s 引用了未知角色 %s", actor, roleID)
			}
		}
	}
	for _, binding := range policy.Bindings {
		if _, ok := policy.Roles[binding.RoleID]; !ok {
			return fmt.Errorf("角色绑定 %s 引用了未知角色", binding.ID)
		}
		if binding.TenantID != "*" {
			if _, ok := policy.Tenants[binding.TenantID]; !ok {
				return fmt.Errorf("角色绑定 %s 引用了未知租户", binding.ID)
			}
		}
		for _, objectID := range binding.ObjectIDs {
			if objectID != "*" {
				if _, ok := objectIDs[objectID]; !ok {
					return fmt.Errorf("角色绑定 %s 引用了未知对象 %s", binding.ID, objectID)
				}
			}
		}
	}
	for _, name := range catalog.ObjectNames() {
		object, _ := catalog.Object(name)
		if _, ok := policy.Tenants[object.TenantID]; !ok {
			return fmt.Errorf("受管对象 %s 引用了未知租户", name)
		}
	}
	return nil
}

func validatePublisherKeys(policy *ExtensionPolicy) error {
	if len(policy.TrustedPublishers) == 0 {
		return errors.New("扩展签名策略缺少受信发布者")
	}
	for _, publisher := range policy.TrustedPublishers {
		encoded := policy.TrustedPublisherKeys[publisher]
		key, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil || len(key) != ed25519.PublicKeySize {
			return fmt.Errorf("扩展发布者 %s 的 Ed25519 公钥无效", publisher)
		}
	}
	return nil
}

func (catalog *Catalog) validateObject(
	key string,
	service *model.ServiceDefinition,
	expectedType string,
	objectIDs map[string]string,
	requireRoot bool,
) error {
	if key != service.Name || !namePattern.MatchString(key) {
		return fmt.Errorf("受管对象名称无效: %q", key)
	}
	if service.DisplayName == "" || service.Metadata.Type != expectedType {
		return fmt.Errorf("受管对象 %s 的显示名称或类型无效", key)
	}
	if err := validateMetadata(key, service.Metadata); err != nil {
		return err
	}
	if policy := service.StatePolicy; policy != nil {
		if policy.DefaultDesired != "" && policy.DefaultDesired != model.DesiredRunning &&
			policy.DefaultDesired != model.DesiredStopped && policy.DefaultDesired != model.DesiredMaintenance &&
			policy.DefaultDesired != model.DesiredDrained {
			return fmt.Errorf("服务 %s 的默认目标状态无效", key)
		}
		if policy.MaintenanceTTLSeconds != 0 && (policy.MaintenanceTTLSeconds < 60 || policy.MaintenanceTTLSeconds > 7*24*60*60) {
			return fmt.Errorf("服务 %s 的维护 TTL 无效", key)
		}
		if policy.DrainTimeoutSeconds != 0 && (policy.DrainTimeoutSeconds < 10 || policy.DrainTimeoutSeconds > 3600) {
			return fmt.Errorf("服务 %s 的排空超时无效", key)
		}
	}
	if service.TrafficPolicy != nil {
		if expectedType != "service" {
			return fmt.Errorf("受管对象 %s 不是服务，不能声明流量策略", key)
		}
		if err := validateTrafficPolicy(key, service.TrafficPolicy, requireRoot); err != nil {
			return err
		}
		computedDigest := model.TrafficPolicyDigest(*service.TrafficPolicy)
		if service.TrafficPolicyDigest != "" && service.TrafficPolicyDigest != computedDigest {
			return fmt.Errorf("服务 %s 的流量策略摘要与声明不一致", key)
		}
		service.TrafficPolicyDigest = computedDigest
	} else if service.TrafficPolicyDigest != "" {
		return fmt.Errorf("服务 %s 声明了流量策略摘要但没有流量策略", key)
	}
	if err := validateAutoUpdatePolicy(key, service.AutoUpdate); err != nil {
		return err
	}
	adapterPath, err := catalog.resolveAdapter(*service, expectedType)
	if err != nil {
		return fmt.Errorf("受管对象 %s: %w", key, err)
	}
	service.Adapter = adapterPath
	service.AdapterContractVersion = 1
	if catalog.SchemaVersion == schemaVersion {
		service.AdapterContractVersion = 2
	}
	if !objectIDPattern.MatchString(service.ObjectID) {
		return fmt.Errorf("受管对象 %s 的稳定对象标识无效", key)
	}
	if owner, exists := objectIDs[service.ObjectID]; exists {
		return fmt.Errorf("受管对象 %s 与 %s 使用了重复对象标识", key, owner)
	}
	objectIDs[service.ObjectID] = key
	if expectedType == "service" && service.Template != "custom" && service.Template != "compose-service-v1" {
		return fmt.Errorf("服务 %s 的模板无效", key)
	}
	if service.Template == "compose-service-v1" {
		if err := validateComposeRuntime(key, service.Runtime, requireRoot); err != nil {
			return err
		}
	} else if expectedType == "service" && service.Runtime != nil {
		return fmt.Errorf("自定义服务 %s 不应声明通用 Compose 运行配置", key)
	}
	if requireRoot {
		if err := verifySecureExecutable(adapterPath); err != nil {
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
	if err := validateRecoveryPointPolicy(key, service.RecoveryPointPolicy, service.Actions); err != nil {
		return err
	}
	if err := validateProductionRestoreAction(key, *service); err != nil {
		return err
	}
	if service.Template == "compose-service-v1" && service.RecoveryPointPolicy != nil &&
		service.Runtime.BackupEvidenceExecutable == "" {
		return fmt.Errorf("服务 %s 的通用 Compose 恢复点缺少备份证据 hook", key)
	}
	if err := validateCapabilityState(key, service.Metadata, service.Actions); err != nil {
		return err
	}
	if err := validateAlertPolicy(key, *service); err != nil {
		return err
	}
	return nil
}

func validateTrafficPolicy(service string, policy *model.TrafficPolicy, requireRoot bool) error {
	if policy == nil {
		return nil
	}
	if policy.AdapterPath != "" && policy.AdapterPath != TrafficAdapterPath {
		return fmt.Errorf("服务 %s 的流量适配器路径不受信", service)
	}
	policy.AdapterPath = TrafficAdapterPath
	if policy.SiteFile == "" || policy.IncludeFile == "" || policy.Hostname == "" ||
		policy.MaintenanceFile == "" || policy.Marker == "" {
		return fmt.Errorf("服务 %s 的流量策略字段不完整", service)
	}
	for name, path := range map[string]string{
		"siteFile": policy.SiteFile, "includeFile": policy.IncludeFile, "maintenanceFile": policy.MaintenanceFile,
	} {
		clean := filepath.Clean(path)
		if !filepath.IsAbs(path) || clean != path || strings.Contains(path, "..") {
			return fmt.Errorf("服务 %s 的流量策略 %s 路径无效", service, name)
		}
	}
	if !strings.HasPrefix(policy.SiteFile, "/etc/nginx/sites-enabled/") ||
		!strings.HasPrefix(policy.IncludeFile, "/etc/nginx/snippets/areasong-ops/") ||
		!strings.HasPrefix(policy.MaintenanceFile, "/etc/nginx/snippets/areasong-ops/") {
		return fmt.Errorf("服务 %s 的流量策略路径不在受控 Nginx 目录", service)
	}
	if !trafficHostnamePattern.MatchString(policy.Hostname) || !strings.Contains(policy.Hostname, ".") {
		return fmt.Errorf("服务 %s 的流量 hostname 无效", service)
	}
	if strings.EqualFold(policy.Hostname, ControlPlaneHostname) {
		return fmt.Errorf("服务 %s 禁止绑定控制面自身域名 %s", service, ControlPlaneHostname)
	}
	if !strings.HasSuffix(policy.SiteFile, ".conf") ||
		!strings.HasSuffix(policy.IncludeFile, ".conf") ||
		!strings.HasSuffix(policy.MaintenanceFile, ".conf") {
		return fmt.Errorf("服务 %s 的流量策略文件必须使用 .conf 后缀", service)
	}
	if policy.IncludeFile == policy.MaintenanceFile {
		return fmt.Errorf("服务 %s 的流量 include 与 maintenance 文件必须不同", service)
	}
	if len(policy.Marker) < 8 || len(policy.Marker) > 128 || strings.ContainsAny(policy.Marker, "\r\n") {
		return fmt.Errorf("服务 %s 的流量 marker 无效", service)
	}
	if policy.Marker != "include "+policy.IncludeFile+";" {
		return fmt.Errorf("服务 %s 的流量 marker 必须精确匹配 include 指令", service)
	}
	if policy.DrainTimeoutSecs == 0 {
		policy.DrainTimeoutSecs = 300
	}
	if policy.DrainTimeoutSecs < 10 || policy.DrainTimeoutSecs > 3600 {
		return fmt.Errorf("服务 %s 的排空超时必须为 10 到 3600 秒", service)
	}
	if requireRoot {
		if err := verifySecureExecutable(policy.AdapterPath); err != nil {
			return fmt.Errorf("服务 %s 的流量适配器: %w", service, err)
		}
	}
	return nil
}

func validateRecoveryPointPolicy(
	service string,
	policy *model.RecoveryPointPolicy,
	actions map[string]model.ActionDefinition,
) error {
	usesRecoveryPoint := false
	for actionName, action := range actions {
		// Restore actions consume a recovery point produced by a previous task.
		// Other actions must still produce their point before using it in the
		// same ordered action contract.
		produced := actionName == "restore" || actionName == "restore-drill"
		for _, phase := range action.Steps {
			semantics := model.EffectivePhaseSemantics(action, phase)
			if semantics.ProducesRecoveryPoint {
				usesRecoveryPoint = true
				produced = true
			}
			if semantics.RequiresRecoveryPoint {
				usesRecoveryPoint = true
				if !produced {
					return fmt.Errorf("服务 %s 的动作 %s 在产生恢复点前要求恢复点", service, actionName)
				}
			}
		}
	}
	if !usesRecoveryPoint {
		if policy != nil {
			return fmt.Errorf("服务 %s 未使用恢复点却声明了恢复点策略", service)
		}
		return nil
	}
	if policy == nil {
		return fmt.Errorf("服务 %s 使用恢复点但缺少恢复点策略", service)
	}
	if len(policy.RequiredArtifactRoles) == 0 || len(policy.RequiredArtifactRoles) > 16 {
		return fmt.Errorf("服务 %s 的恢复点必需角色数量无效", service)
	}
	if policy.RecoverableSeconds < 3600 || policy.RecoverableSeconds > 7*24*60*60 {
		return fmt.Errorf("服务 %s 的恢复点有效期必须为 1 小时到 7 天", service)
	}
	seen := make(map[string]struct{}, len(policy.RequiredArtifactRoles))
	for _, role := range policy.RequiredArtifactRoles {
		if !artifactRolePattern.MatchString(role) {
			return fmt.Errorf("服务 %s 的恢复点角色无效: %q", service, role)
		}
		if _, exists := seen[role]; exists {
			return fmt.Errorf("服务 %s 的恢复点角色重复: %s", service, role)
		}
		seen[role] = struct{}{}
	}
	return nil
}

func validateProductionRestoreAction(service string, definition model.ServiceDefinition) error {
	action, exists := definition.Actions["restore"]
	if !exists || !action.Enabled {
		return nil
	}
	if action.Risk != model.RiskHigh || action.TargetMode != "none" {
		return fmt.Errorf("服务 %s 的生产恢复必须是 high 风险且只接受恢复点目标", service)
	}
	expectedSteps := []string{"preflight", "quiesce", "restore", "verify", "resume"}
	if !slices.Equal(action.Steps, expectedSteps) {
		return fmt.Errorf("服务 %s 的生产恢复阶段必须为 preflight/quiesce/restore/verify/resume", service)
	}
	restoreSemantics := model.EffectivePhaseSemantics(action, "restore")
	if restoreSemantics.Effect != "data_mutation" || !restoreSemantics.RequiresRecoveryPoint ||
		restoreSemantics.FailurePolicy != "needs_attention" || restoreSemantics.RecoveryPhase != "" {
		return fmt.Errorf("服务 %s 的生产恢复数据阶段必须要求恢复点并在失败时进入人工核对", service)
	}
	for _, phase := range []string{"preflight", "verify"} {
		semantics := model.EffectivePhaseSemantics(action, phase)
		expectedFailure := "needs_attention"
		if phase == "preflight" {
			expectedFailure = "fail"
		}
		if semantics.Effect != "observe" || !semantics.RequiresRecoveryPoint ||
			semantics.FailurePolicy != expectedFailure || semantics.RecoveryPhase != "" {
			return fmt.Errorf("服务 %s 的生产恢复 %s 阶段必须是绑定恢复点的只读校验", service, phase)
		}
	}
	for _, phase := range []string{"quiesce", "resume"} {
		semantics := model.EffectivePhaseSemantics(action, phase)
		if semantics.Effect != "runtime_mutation" || !semantics.RequiresRecoveryPoint ||
			semantics.FailurePolicy != "needs_attention" || semantics.RecoveryPhase != "" {
			return fmt.Errorf("服务 %s 的生产恢复 %s 阶段必须是不可自动回滚的运行态变更", service, phase)
		}
	}
	if definition.Template == "compose-service-v1" &&
		(definition.Runtime == nil || definition.Runtime.RestoreExecutable == "") {
		return fmt.Errorf("服务 %s 的生产恢复缺少独立受信 restore hook", service)
	}
	return nil
}

func (catalog *Catalog) validateAdapters(requireRoot bool) error {
	for name, adapter := range catalog.Adapters {
		if !namePattern.MatchString(name) || !filepath.IsAbs(adapter.Path) || len(adapter.AllowedTypes) == 0 {
			return fmt.Errorf("受信适配器 %s 声明无效", name)
		}
		seen := map[string]struct{}{}
		for _, objectType := range adapter.AllowedTypes {
			if objectType != "service" && objectType != "automatic_task" {
				return fmt.Errorf("受信适配器 %s 的对象类型无效", name)
			}
			if _, exists := seen[objectType]; exists {
				return fmt.Errorf("受信适配器 %s 的对象类型重复", name)
			}
			seen[objectType] = struct{}{}
		}
		if requireRoot {
			if err := verifySecureExecutable(adapter.Path); err != nil {
				return fmt.Errorf("受信适配器 %s: %w", name, err)
			}
		}
	}
	return nil
}

func (catalog *Catalog) resolveAdapter(service model.ServiceDefinition, objectType string) (string, error) {
	if service.AdapterRef == "" {
		if catalog.SchemaVersion != legacySchemaVersion || service.Adapter == "" || !filepath.IsAbs(service.Adapter) {
			return "", errors.New("必须引用受信适配器")
		}
		return service.Adapter, nil
	}
	if service.Adapter != "" {
		return "", errors.New("不能同时声明 adapter 和 adapterRef")
	}
	adapter, exists := catalog.Adapters[service.AdapterRef]
	if !exists {
		return "", errors.New("引用了未登记适配器")
	}
	for _, allowed := range adapter.AllowedTypes {
		if allowed == objectType {
			return adapter.Path, nil
		}
	}
	return "", errors.New("适配器未授权该对象类型")
}

func applyMetadataDefaults(service model.ServiceDefinition, objectType string) model.ServiceDefinition {
	if service.Metadata.Type == "" {
		service.Metadata = model.ObjectMetadata{
			Type: objectType, Environment: "production", Owner: "operations",
			Criticality: "important", Lifecycle: "active", Maturity: "manual_approval",
		}
	}
	return service
}

func validateMetadata(name string, metadata model.ObjectMetadata) error {
	if metadata.Environment != "production" || metadata.Owner == "" {
		return fmt.Errorf("受管对象 %s 的环境或责任域无效", name)
	}
	if metadata.Criticality != "standard" && metadata.Criticality != "important" && metadata.Criticality != "critical" {
		return fmt.Errorf("受管对象 %s 的重要级别无效", name)
	}
	switch metadata.Lifecycle {
	case "proposed", "onboarding", "active", "maintenance", "retiring", "retired":
	default:
		return fmt.Errorf("受管对象 %s 的生命周期无效", name)
	}
	switch metadata.Maturity {
	case "disabled", "inspect_only", "shadow", "manual_approval", "automated":
	default:
		return fmt.Errorf("受管对象 %s 的能力成熟度无效", name)
	}
	return nil
}

func validateCapabilityState(name string, metadata model.ObjectMetadata, actions map[string]model.ActionDefinition) error {
	for _, action := range actions {
		if !action.Enabled {
			continue
		}
		if metadata.Lifecycle == "retired" || metadata.Lifecycle == "retiring" || metadata.Maturity == "disabled" {
			return fmt.Errorf("受管对象 %s 当前生命周期或成熟度不允许开放动作", name)
		}
		if (metadata.Lifecycle == "proposed" || metadata.Lifecycle == "onboarding" || metadata.Maturity == "inspect_only") &&
			action.Risk != model.RiskReadOnly {
			return fmt.Errorf("受管对象 %s 当前只能开放只读动作", name)
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
	if runtime.RestoreExecutable != "" {
		paths = append(paths, runtime.RestoreExecutable)
	}
	if runtime.BackupEvidenceExecutable != "" {
		paths = append(paths, runtime.BackupEvidenceExecutable)
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
	if filepath.Clean(runtime.ControlledCompose) == filepath.Clean(runtime.RuntimeCompose) {
		return fmt.Errorf("服务 %s 的 controlled 与 runtime Compose 不得是同一路径", service)
	}
	if runtime.ApplicationService == "" || runtime.ApplicationContainer == "" ||
		!repositoryPattern.MatchString(runtime.ReleaseRepository) {
		return fmt.Errorf("服务 %s 的 Compose 服务、容器或发布仓库无效", service)
	}
	if !composeServicePattern.MatchString(runtime.ApplicationService) ||
		!composeServicePattern.MatchString(runtime.ApplicationContainer) {
		return fmt.Errorf("服务 %s 的 Compose 应用服务或容器名称无效", service)
	}
	seenDependencies := make(map[string]struct{}, len(runtime.DependencyContainers))
	for _, dependency := range runtime.DependencyContainers {
		if !composeServicePattern.MatchString(dependency) || dependency == runtime.ApplicationService {
			return fmt.Errorf("服务 %s 的 Compose 依赖服务名称无效", service)
		}
		if _, exists := seenDependencies[dependency]; exists {
			return fmt.Errorf("服务 %s 的 Compose 依赖服务重复", service)
		}
		seenDependencies[dependency] = struct{}{}
	}
	parsed, err := url.Parse(runtime.HealthURL)
	if err != nil || parsed.Scheme != "http" || (parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "localhost") || parsed.User != nil {
		return fmt.Errorf("服务 %s 的健康地址必须是本机 HTTP 地址", service)
	}
	if requireRoot {
		executables := []string{runtime.InspectExecutable, runtime.BackupEvidenceExecutable}
		executables = append(executables, runtime.BackupExecutables...)
		executables = append(executables, runtime.RestoreDrillExecutable, runtime.RestoreExecutable,
			runtime.PrepareExecutable, runtime.UpdateExecutable)
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
		if semantics.RecoveryPhase != "" && !stepPattern.MatchString(semantics.RecoveryPhase) {
			return fmt.Errorf("服务 %s 的动作 %s 阶段 %s 恢复阶段无效", service, name, phase)
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

func (catalog *Catalog) AutomaticTaskNames() []string {
	names := make([]string, 0, len(catalog.AutomaticTasks))
	for name := range catalog.AutomaticTasks {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (catalog *Catalog) ObjectNames() []string {
	names := append(catalog.ServiceNames(), catalog.AutomaticTaskNames()...)
	sort.Strings(names)
	return names
}

func (catalog *Catalog) Object(name string) (model.ServiceDefinition, bool) {
	if service, exists := catalog.Services[name]; exists {
		return service, true
	}
	task, exists := catalog.AutomaticTasks[name]
	return task, exists
}

func (catalog *Catalog) hasRunner(id string) bool {
	if catalog.Fleet == nil || !catalog.Fleet.Enabled {
		return false
	}
	for _, runner := range catalog.Fleet.Inventory.Runners {
		if runner.ID == id {
			return true
		}
	}
	return false
}

func runnerUpdateManagerCount(policy *AccessPolicy, runnerID string, now time.Time) int {
	if policy == nil {
		return 0
	}
	resource := "runner:" + runnerID
	count := 0
	for subject, principal := range policy.Principals {
		if !accessTenantActive(policy, principal.TenantID) {
			continue
		}
		allowed := accessRolesAllow(policy, principal.Roles, model.Permission("*"))
		if !allowed {
			for _, binding := range policy.Bindings {
				if binding.Subject != subject || (binding.TenantID != principal.TenantID && binding.TenantID != "*") ||
					(binding.ExpiresAt != nil && !now.Before(*binding.ExpiresAt)) ||
					(len(binding.ObjectIDs) > 0 && !containsString(binding.ObjectIDs, resource) && !containsString(binding.ObjectIDs, "*")) {
					continue
				}
				if role, ok := policy.Roles[binding.RoleID]; ok && role.Allows(model.PermissionRunnerUpdate) {
					allowed = true
					break
				}
			}
		}
		if allowed {
			count++
		}
	}
	return count
}

func accessTenantActive(policy *AccessPolicy, tenantID string) bool {
	tenant, ok := policy.Tenants[tenantID]
	return ok && (tenant.Status == "" || tenant.Status == "active")
}

func accessRolesAllow(policy *AccessPolicy, roles []string, permission model.Permission) bool {
	for _, roleID := range roles {
		if role, ok := policy.Roles[roleID]; ok && role.Allows(permission) {
			return true
		}
	}
	return false
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func requireJSONEOF(decoder *json.Decoder) error {
	var trailing any
	err := decoder.Decode(&trailing)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err != nil {
		return err
	}
	return errors.New("服务声明包含多个 JSON 文档")
}

func rejectDuplicateJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := walkJSONValue(decoder); err != nil {
		return err
	}
	return requireJSONEOF(decoder)
}

func walkJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("JSON 对象键格式无效")
			}
			if _, exists := seen[key]; exists {
				return fmt.Errorf("JSON 对象包含重复键 %q", key)
			}
			seen[key] = struct{}{}
			if err := walkJSONValue(decoder); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := walkJSONValue(decoder); err != nil {
				return err
			}
		}
	default:
		return errors.New("JSON 结构无效")
	}
	closing, err := decoder.Token()
	if err != nil {
		return err
	}
	if closing != matchingJSONDelimiter(delimiter) {
		return errors.New("JSON 结束符无效")
	}
	return nil
}

func matchingJSONDelimiter(open json.Delim) json.Delim {
	if open == '{' {
		return '}'
	}
	return ']'
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

func verifySecureDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("检查目录失败: %w", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !ok || stat.Uid != 0 {
		return fmt.Errorf("目录必须由 root 拥有且不是符号链接: %s", path)
	}
	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("目录不能由 group/other 写入: %s", path)
	}
	return nil
}

func verifySecureDirectoryTree(path string) error {
	cleaned := filepath.Clean(path)
	if !filepath.IsAbs(cleaned) || cleaned != path {
		return errors.New("目录路径必须是规范绝对路径")
	}
	current := string(filepath.Separator)
	for _, part := range strings.Split(strings.TrimPrefix(cleaned, string(filepath.Separator)), string(filepath.Separator)) {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		if err := verifySecureDirectory(current); err != nil {
			return err
		}
	}
	info, err := os.Lstat(cleaned)
	if err != nil {
		return err
	}
	if info.Mode().Perm() != 0o700 {
		return errors.New("Runner 更新 artifactRoot 权限必须为 0700")
	}
	return nil
}
