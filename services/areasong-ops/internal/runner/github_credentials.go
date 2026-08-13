package runner

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

var envKeyPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

var credentialConfigKeys = map[string]struct{}{
	"ALERTMANAGER_GITHUB_ISSUES_ENABLED": {},
	"GITHUB_TOKEN":                       {},
	"GITHUB_REPOSITORY":                  {},
	"GITHUB_TOKEN_EXPIRES_AT":            {},
	"ALERTMANAGER_URL":                   {},
	"GITHUB_API_BASE":                    {},
	"ALERTMANAGER_GITHUB_METRIC_OUT":     {},
	"ALERTMANAGER_HTTP_TIMEOUT_SECONDS":  {},
}

type GitHubCredentialRotatorOptions struct {
	ConfigPath     string
	RollbackRoot   string
	MetricPath     string
	RequireRoot    bool
	Validator      GitHubCredentialValidator
	Smoke          CredentialSmoke
	Now            func() time.Time
	AllowTestToken bool
}

type GitHubCredentialRotator struct {
	configPath     string
	rollbackRoot   string
	metricPath     string
	requireRoot    bool
	validator      GitHubCredentialValidator
	smoke          CredentialSmoke
	now            func() time.Time
	allowTestToken bool
	mu             sync.Mutex
}

func NewProductionCredentialRotator() *GitHubCredentialRotator {
	return NewGitHubCredentialRotator(GitHubCredentialRotatorOptions{
		ConfigPath:   "/var/lib/areasong-ops/credentials/alertmanager-github.env",
		RollbackRoot: "/var/lib/areasong-ops/credential-rollbacks",
		MetricPath:   "/var/lib/node_exporter/textfile_collector/alertmanager-github-issues.prom",
		RequireRoot:  true,
		Validator:    NewGitHubAPIValidator("https://api.github.com"),
		Smoke:        commandCredentialSmoke{},
	})
}

func NewGitHubCredentialRotator(options GitHubCredentialRotatorOptions) *GitHubCredentialRotator {
	if options.Now == nil {
		options.Now = func() time.Time { return time.Now().UTC() }
	}
	return &GitHubCredentialRotator{
		configPath: options.ConfigPath, rollbackRoot: options.RollbackRoot,
		metricPath: options.MetricPath, requireRoot: options.RequireRoot,
		validator: options.Validator, smoke: options.Smoke, now: options.Now,
		allowTestToken: options.AllowTestToken,
	}
}

func (rotator *GitHubCredentialRotator) Current(context.Context) (CurrentCredential, error) {
	values, err := rotator.readConfig(rotator.configPath)
	if os.IsNotExist(err) {
		return CurrentCredential{}, nil
	}
	if err != nil {
		return CurrentCredential{}, err
	}
	token := values["GITHUB_TOKEN"]
	if token == "" {
		return CurrentCredential{}, errors.New("当前 GitHub Token 配置为空")
	}
	return CurrentCredential{
		Configured: true, Fingerprint: credentialFingerprint(token),
		ExpiresAt: values["GITHUB_TOKEN_EXPIRES_AT"],
	}, nil
}

func (rotator *GitHubCredentialRotator) Rotate(
	ctx context.Context,
	rotationID, token, expiresAt string,
) (model.CredentialRotationResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	rotator.mu.Lock()
	defer rotator.mu.Unlock()
	if err := rotator.validateInput(token, expiresAt); err != nil {
		return failedCredentialResult("输入验证未通过"), err
	}
	validation, err := rotator.validator.Validate(ctx, token)
	if err != nil {
		return failedCredentialResult("GitHub 身份或权限验证未通过"), err
	}
	if validation.Expiration.UTC().Format("2006-01-02") != expiresAt {
		return failedCredentialResult("GitHub 签发方到期日与输入不一致"), errors.New("GitHub Token 到期日验证失败")
	}
	if _, err := rotator.readConfig(rotator.configPath); err != nil {
		return failedCredentialResult("当前配置安全检查未通过"), err
	}
	oldConfig, err := os.ReadFile(rotator.configPath)
	if err != nil {
		return failedCredentialResult("当前配置读取失败"), err
	}
	rollbackPath := rotator.rollbackPath(rotationID)
	if err := rotator.atomicWrite(rollbackPath, oldConfig); err != nil {
		return failedCredentialResult("回滚副本创建失败"), err
	}
	newConfig := renderCredentialConfig(token, expiresAt, rotator.metricPath)
	if err := rotator.atomicWrite(rotator.configPath, newConfig); err != nil {
		_ = os.Remove(rollbackPath)
		return failedCredentialResult("生产配置原子切换失败"), err
	}
	if err := rotator.smoke.Run(ctx, rotator.configPath); err != nil {
		return rotator.rollbackAfterSmokeFailure(ctx, rollbackPath, oldConfig, err)
	}
	if err := verifyCredentialMetric(rotator.metricPath, expiresAt); err != nil {
		return rotator.rollbackAfterSmokeFailure(ctx, rollbackPath, oldConfig, err)
	}
	return model.CredentialRotationResult{
		State:            model.CredentialRotationSwitchedPendingRevocation,
		ValidationResult: "GitHub 身份、固定仓库访问与 Issues 读写能力验证通过",
		Outcome:          "新凭据已切换并通过真实同步；等待撤销旧凭据",
		RollbackResult:   "旧凭据回滚副本已隔离保留",
	}, nil
}

func (rotator *GitHubCredentialRotator) VerifyRevoked(
	ctx context.Context,
	rotation model.CredentialRotation,
) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	rotator.mu.Lock()
	defer rotator.mu.Unlock()
	current, err := rotator.Current(ctx)
	if err != nil {
		return err
	}
	if current.Fingerprint != rotation.Fingerprint {
		return errors.New("当前生产凭据与待收口轮换不一致")
	}
	rollbackPath := rotator.rollbackPath(rotation.ID)
	values, err := rotator.readConfig(rollbackPath)
	if os.IsNotExist(err) {
		return errors.New("旧凭据回滚副本缺失，拒绝收口")
	}
	if err != nil {
		return err
	}
	revoked, err := rotator.validator.Revoked(ctx, values["GITHUB_TOKEN"])
	if err != nil {
		return err
	}
	if !revoked {
		return errors.New("旧 GitHub Token 仍然有效，拒绝收口")
	}
	return nil
}

func (rotator *GitHubCredentialRotator) RemoveRollback(
	_ context.Context,
	rotation model.CredentialRotation,
) error {
	rotator.mu.Lock()
	defer rotator.mu.Unlock()
	rollbackPath := rotator.rollbackPath(rotation.ID)
	if err := os.Remove(rollbackPath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("清除旧凭据回滚副本失败: %w", err)
	}
	return syncDirectory(filepath.Dir(rollbackPath))
}

func (rotator *GitHubCredentialRotator) rollbackAfterSmokeFailure(
	ctx context.Context,
	rollbackPath string,
	oldConfig []byte,
	smokeErr error,
) (model.CredentialRotationResult, error) {
	if err := rotator.atomicWrite(rotator.configPath, oldConfig); err != nil {
		return model.CredentialRotationResult{
			State:            model.CredentialRotationNeedsAttention,
			ValidationResult: "新凭据验证通过，但切换后验证失败",
			Outcome:          "生产凭据状态需要人工核对", RollbackResult: "自动恢复旧配置失败",
		}, fmt.Errorf("切换后验证失败且回滚失败: %w", err)
	}
	rollbackSmokeErr := rotator.smoke.Run(ctx, rotator.configPath)
	if rollbackSmokeErr != nil {
		return model.CredentialRotationResult{
			State:            model.CredentialRotationNeedsAttention,
			ValidationResult: "新凭据验证通过，但切换后验证失败",
			Outcome:          "旧配置已恢复但真实同步验证失败", RollbackResult: "配置已恢复，运行结果待核对",
		}, fmt.Errorf("切换后验证失败，旧配置恢复验证失败: %w", rollbackSmokeErr)
	}
	if err := os.Remove(rollbackPath); err != nil {
		return model.CredentialRotationResult{
			State:            model.CredentialRotationNeedsAttention,
			ValidationResult: "新凭据验证通过，但切换后验证失败",
			Outcome:          "旧配置与真实同步已恢复", RollbackResult: "旧凭据回滚副本清理失败",
		}, fmt.Errorf("切换失败后已恢复旧凭据，但清理回滚副本失败: %w", err)
	}
	return model.CredentialRotationResult{
		State:            model.CredentialRotationRolledBack,
		ValidationResult: "新凭据验证通过，但切换后验证失败",
		Outcome:          "切换失败，旧凭据已恢复", RollbackResult: "旧配置与真实同步均恢复",
	}, fmt.Errorf("切换后验证失败，已恢复旧凭据: %w", smokeErr)
}

func (rotator *GitHubCredentialRotator) validateInput(token, expiresAt string) error {
	if strings.ContainsAny(token, "\r\n\x00") || len(token) < 32 || len(token) > 512 {
		return errors.New("GitHub Token 格式无效")
	}
	if !rotator.allowTestToken && !strings.HasPrefix(token, "github_pat_") {
		return errors.New("只接受 GitHub fine-grained personal access token")
	}
	expiry, err := time.Parse("2006-01-02", expiresAt)
	if err != nil || expiry.Format("2006-01-02") != expiresAt {
		return errors.New("到期日必须使用 YYYY-MM-DD")
	}
	days := int(expiry.Sub(rotator.now().Truncate(24*time.Hour)).Hours() / 24)
	if days < 7 || days > 366 {
		return errors.New("到期日必须在 7 至 366 天内")
	}
	return nil
}

func (rotator *GitHubCredentialRotator) readConfig(path string) (map[string]string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return nil, fmt.Errorf("凭据文件必须是 0600 普通文件: %s", path)
	}
	if info.Size() <= 0 || info.Size() > 16<<10 {
		return nil, errors.New("凭据文件大小无效")
	}
	if rotator.requireRoot {
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != 0 || stat.Gid != 0 {
			return nil, fmt.Errorf("凭据文件必须由 root:root 拥有: %s", path)
		}
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	values := make(map[string]string)
	scanner := bufio.NewScanner(io.LimitReader(file, 64<<10))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found || !envKeyPattern.MatchString(key) {
			return nil, errors.New("凭据文件包含无效配置行")
		}
		if _, allowed := credentialConfigKeys[key]; !allowed {
			return nil, errors.New("凭据文件包含未授权配置项")
		}
		if _, duplicate := values[key]; duplicate {
			return nil, errors.New("凭据文件包含重复配置项")
		}
		values[key] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if err := rotator.validateConfigValues(values); err != nil {
		return nil, err
	}
	return values, nil
}

func (rotator *GitHubCredentialRotator) validateConfigValues(values map[string]string) error {
	expected := map[string]string{
		"ALERTMANAGER_GITHUB_ISSUES_ENABLED": "true",
		"GITHUB_REPOSITORY":                  githubRepository,
		"ALERTMANAGER_URL":                   "http://127.0.0.1:9093/api/v2/alerts",
		"GITHUB_API_BASE":                    "https://api.github.com",
		"ALERTMANAGER_GITHUB_METRIC_OUT":     rotator.metricPath,
		"ALERTMANAGER_HTTP_TIMEOUT_SECONDS":  "15",
	}
	for key, value := range expected {
		if values[key] != value {
			return fmt.Errorf("凭据文件固定配置不匹配: %s", key)
		}
	}
	if values["GITHUB_TOKEN"] == "" {
		return errors.New("凭据文件缺少 GitHub Token")
	}
	if _, err := time.Parse("2006-01-02", values["GITHUB_TOKEN_EXPIRES_AT"]); err != nil {
		return errors.New("凭据文件到期日无效")
	}
	return nil
}

func (rotator *GitHubCredentialRotator) atomicWrite(path string, content []byte) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".credential-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if rotator.requireRoot {
		if err := temporary.Chown(0, 0); err != nil {
			temporary.Close()
			return err
		}
	}
	if _, err := temporary.Write(content); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	return syncDirectory(directory)
}

func (rotator *GitHubCredentialRotator) rollbackPath(rotationID string) string {
	return filepath.Join(rotator.rollbackRoot, rotationID+".env")
}

func renderCredentialConfig(token, expiresAt, metricPath string) []byte {
	return []byte(strings.Join([]string{
		"ALERTMANAGER_GITHUB_ISSUES_ENABLED=true",
		"GITHUB_TOKEN=" + token,
		"GITHUB_REPOSITORY=" + githubRepository,
		"GITHUB_TOKEN_EXPIRES_AT=" + expiresAt,
		"ALERTMANAGER_URL=http://127.0.0.1:9093/api/v2/alerts",
		"GITHUB_API_BASE=https://api.github.com",
		"ALERTMANAGER_GITHUB_METRIC_OUT=" + metricPath,
		"ALERTMANAGER_HTTP_TIMEOUT_SECONDS=15",
		"",
	}, "\n"))
}

func credentialFingerprint(secret string) string {
	digest := sha256.Sum256([]byte(secret))
	return "sha256:" + hex.EncodeToString(digest[:6])
}

func failedCredentialResult(outcome string) model.CredentialRotationResult {
	return model.CredentialRotationResult{
		State: model.CredentialRotationFailed, ValidationResult: "未通过",
		Outcome: outcome, RollbackResult: "未修改生产配置",
	}
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func verifyCredentialMetric(path, expiresAt string) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("读取凭据到期指标失败: %w", err)
	}
	expiry, _ := time.Parse("2006-01-02", expiresAt)
	expected := strconv.FormatInt(expiry.UTC().Unix(), 10)
	metrics := make(map[string]string)
	for _, line := range strings.Split(string(content), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && !strings.HasPrefix(fields[0], "#") {
			metrics[fields[0]] = fields[1]
		}
	}
	if metrics["alertmanager_github_token_expiry_configured"] != "1" ||
		metrics["alertmanager_github_token_expiry_timestamp"] != expected ||
		metrics["alertmanager_github_issue_sync_success"] != "1" {
		return errors.New("凭据到期或同步指标与目标不一致")
	}
	return nil
}

type commandCredentialSmoke struct{}

func (commandCredentialSmoke) Run(ctx context.Context, configPath string) error {
	command := exec.CommandContext(ctx, "/usr/bin/flock", "-w", "30",
		"/var/lib/areasong-ops/run/alertmanager-github-issues.lock", "/usr/bin/python3",
		"/var/lib/ops/observability-host-jobs/current/observability/scripts/alertmanager_github_issues.py",
		"--config", configPath, "--require-enabled")
	command.Env = []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "PYTHONDONTWRITEBYTECODE=1"}
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("GitHub Issue 同步 smoke 失败: %w (%s)", err, redactText(string(output)))
	}
	return nil
}
