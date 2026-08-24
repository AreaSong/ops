package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

const (
	maxComposeCommandOutput = 128 << 10
	composeOperationTimeout = 15 * time.Minute
	composeHealthTimeout    = 90 * time.Second
)

// ComposeCommandRunner is the narrow host boundary used by the Compose
// approval state machine. It receives only argv and a validated project
// directory; no shell is involved.
type ComposeCommandRunner interface {
	Run(ctx context.Context, projectDirectory string, args ...string) (string, error)
}

type systemComposeCommandRunner struct {
	executable string
}

func (runner systemComposeCommandRunner) Run(
	ctx context.Context, projectDirectory string, args ...string,
) (string, error) {
	command := exec.CommandContext(ctx, runner.executable, args...)
	command.Dir = projectDirectory
	command.Env = []string{
		"PATH=/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME=/root",
		"OPS_ENV=production",
	}
	output, err := command.CombinedOutput()
	if len(output) > maxComposeCommandOutput {
		output = output[:maxComposeCommandOutput]
	}
	return redactText(string(output)), err
}

type composeFileState struct {
	Path    string
	Content string
	Digest  string
	Info    os.FileInfo
}

type composeApplyResult struct {
	State            string
	ControlledBackup string
	RuntimeBackup    string
	Err              error
}

func (engine *Engine) ApproveComposeRevision(
	ctx context.Context,
	actor, serviceName, revisionID string,
	request model.ComposeApprovalRequest,
) (model.ComposeRevision, error) {
	if !actorPattern.MatchString(actor) || !uuidPattern.MatchString(revisionID) {
		return model.ComposeRevision{}, errors.New("Compose 批准请求标识无效")
	}
	revision, err := engine.store.GetComposeRevision(ctx, revisionID)
	if err != nil {
		return model.ComposeRevision{}, err
	}
	if revision.Service != serviceName {
		return model.ComposeRevision{}, errors.New("Compose 修订与服务路径不匹配")
	}
	service, ok := engine.catalog.Services[serviceName]
	if !ok {
		return model.ComposeRevision{}, errors.New("服务未纳入控制面")
	}
	if err := engine.authorize(ctx, actor, model.PermissionManageConfig, service.ObjectID); err != nil {
		return model.ComposeRevision{}, err
	}
	approved, err := engine.store.ApproveComposeRevision(
		ctx, revisionID, actor, request.Digest, request.Confirmation,
	)
	if err != nil {
		return model.ComposeRevision{}, err
	}
	_, _ = engine.store.AppendAudit(ctx, model.AuditEntry{
		ActorHash: actor, Event: "compose.revision.approved", Resource: revisionID,
		Outcome: approved.State, Detail: map[string]any{
			"service": serviceName, "digest": approved.Digest,
		},
	})
	return approved, nil
}

func (engine *Engine) ApplyComposeRevision(
	ctx context.Context,
	actor, serviceName, revisionID string,
	request model.ComposeApplyRequest,
) (model.ComposeRevision, error) {
	if !actorPattern.MatchString(actor) || !uuidPattern.MatchString(revisionID) ||
		!uuidPattern.MatchString(request.IdempotencyKey) {
		return model.ComposeRevision{}, errors.New("Compose 应用请求标识无效")
	}
	revision, err := engine.store.GetComposeRevision(ctx, revisionID)
	if err != nil {
		return model.ComposeRevision{}, err
	}
	if revision.Service != serviceName {
		return model.ComposeRevision{}, errors.New("Compose 修订与服务路径不匹配")
	}
	service, ok := engine.catalog.Services[serviceName]
	if !ok || service.Runtime == nil {
		return model.ComposeRevision{}, errors.New("服务没有受管 Compose 配置")
	}
	if err := engine.authorize(ctx, actor, model.PermissionManageConfig, service.ObjectID); err != nil {
		return model.ComposeRevision{}, err
	}
	lockID := "compose:" + revisionID
	if !engine.acquire([]string{"service:" + serviceName, "compose:" + serviceName}, lockID) {
		return model.ComposeRevision{}, errors.New("该服务的 Compose 操作正被其他操作占用")
	}
	defer engine.release([]string{"service:" + serviceName, "compose:" + serviceName}, lockID)

	started, fresh, err := engine.store.StartComposeApply(
		ctx, revisionID, actor, request.IdempotencyKey,
	)
	if err != nil {
		return started, err
	}
	if !fresh {
		return started, nil
	}

	operationContext, cancel := context.WithTimeout(context.Background(), composeOperationTimeout)
	defer cancel()
	result := engine.applyCompose(operationContext, service, started)
	finishErr := engine.store.FinishComposeApply(
		context.WithoutCancel(ctx), revisionID, result.State,
		result.ControlledBackup, result.RuntimeBackup, redactComposeError(result.Err),
	)
	if finishErr != nil {
		return started, fmt.Errorf("Compose 应用状态收口失败: %w", finishErr)
	}
	final, getErr := engine.store.GetComposeRevision(context.WithoutCancel(ctx), revisionID)
	if getErr != nil {
		return started, getErr
	}
	_, _ = engine.store.AppendAudit(context.Background(), model.AuditEntry{
		ActorHash: actor, Event: "compose.revision.applied", Resource: revisionID,
		Outcome: result.State, Detail: map[string]any{
			"service": serviceName, "digest": started.Digest,
			"controlledBackup": result.ControlledBackup != "",
			"runtimeBackup":    result.RuntimeBackup != "",
		},
	})
	if result.Err != nil {
		return final, result.Err
	}
	return final, nil
}

func (engine *Engine) RollbackComposeRevision(
	ctx context.Context,
	actor, serviceName, revisionID string,
	request model.ComposeRollbackRequest,
) (model.ComposeRevision, error) {
	if !actorPattern.MatchString(actor) || !uuidPattern.MatchString(revisionID) ||
		!uuidPattern.MatchString(request.IdempotencyKey) {
		return model.ComposeRevision{}, errors.New("Compose 回滚请求标识无效")
	}
	revision, err := engine.store.GetComposeRevision(ctx, revisionID)
	if err != nil {
		return model.ComposeRevision{}, err
	}
	if revision.Service != serviceName {
		return model.ComposeRevision{}, errors.New("Compose 修订与服务路径不匹配")
	}
	service, ok := engine.catalog.Services[serviceName]
	if !ok || service.Runtime == nil {
		return model.ComposeRevision{}, errors.New("服务没有受管 Compose 配置")
	}
	if err := engine.authorize(ctx, actor, model.PermissionManageConfig, service.ObjectID); err != nil {
		return model.ComposeRevision{}, err
	}
	if request.Confirmation != "回滚 Compose 变更 "+revisionID {
		return model.ComposeRevision{}, errors.New("Compose 回滚确认短语不匹配")
	}
	lockID := "compose:" + revisionID
	if !engine.acquire([]string{"service:" + serviceName, "compose:" + serviceName}, lockID) {
		return model.ComposeRevision{}, errors.New("该服务的 Compose 操作正被其他操作占用")
	}
	defer engine.release([]string{"service:" + serviceName, "compose:" + serviceName}, lockID)
	started, fresh, err := engine.store.StartComposeRollback(
		ctx, revisionID, actor, request.IdempotencyKey,
	)
	if err != nil {
		return started, err
	}
	if !fresh {
		return started, nil
	}
	operationContext, cancel := context.WithTimeout(context.Background(), composeOperationTimeout)
	defer cancel()
	result := engine.rollbackCompose(operationContext, service, started)
	finishErr := engine.store.FinishComposeRollback(
		context.WithoutCancel(ctx), revisionID, result.State, redactComposeError(result.Err),
	)
	if finishErr != nil {
		return started, fmt.Errorf("Compose 回滚状态收口失败: %w", finishErr)
	}
	final, getErr := engine.store.GetComposeRevision(context.WithoutCancel(ctx), revisionID)
	if getErr != nil {
		return started, getErr
	}
	_, _ = engine.store.AppendAudit(context.Background(), model.AuditEntry{
		ActorHash: actor, Event: "compose.revision.rolled_back", Resource: revisionID,
		Outcome: result.State, Detail: map[string]any{"service": serviceName},
	})
	if result.Err != nil {
		return final, result.Err
	}
	return final, nil
}

func (engine *Engine) applyCompose(
	ctx context.Context, service model.ServiceDefinition, revision model.ComposeRevision,
) composeApplyResult {
	runtime := service.Runtime
	if runtime == nil {
		return composeApplyResult{State: "failed", Err: errors.New("Compose 运行配置缺失")}
	}
	controlled, runtimeFile, err := readComposePair(runtime)
	if err != nil {
		return composeApplyResult{State: "failed", Err: err}
	}
	if controlled.Digest != revision.ExpectedDigest || runtimeFile.Digest != revision.ExpectedDigest ||
		controlled.Digest != runtimeFile.Digest {
		return composeApplyResult{State: "failed", Err: errors.New("Compose 执行前基线摘要已变化")}
	}
	if err := validateComposeCandidate(ctx, runtime, revision.Content, engine.composeRunner); err != nil {
		return composeApplyResult{State: "failed", Err: err}
	}
	dependenciesBefore, err := engine.composeDependencySnapshot(ctx, runtime)
	if err != nil {
		return composeApplyResult{State: "failed", Err: err}
	}
	controlledBackup, err := engine.backupComposeFile(revision, "controlled", controlled)
	if err != nil {
		return composeApplyResult{State: "failed", Err: err}
	}
	runtimeBackup, err := engine.backupComposeFile(revision, "runtime", runtimeFile)
	if err != nil {
		return composeApplyResult{State: "failed", ControlledBackup: controlledBackup, Err: err}
	}

	changed, err := replaceComposePair(controlled, runtimeFile, revision.Content, revision.Content)
	if err != nil {
		if changed {
			currentControlled, currentRuntime, readErr := readComposePair(runtime)
			if readErr != nil {
				return composeApplyResult{State: "needs_attention", ControlledBackup: controlledBackup,
					RuntimeBackup: runtimeBackup, Err: fmt.Errorf("Compose 替换失败且无法读取当前文件身份: %w", readErr)}
			}
			if safeComposeRestoreState(currentControlled, currentRuntime, controlled.Digest, revision.Digest) == false {
				return composeApplyResult{State: "needs_attention", ControlledBackup: controlledBackup,
					RuntimeBackup: runtimeBackup, Err: fmt.Errorf("Compose 替换失败且当前文件身份已漂移: %w", err)}
			}
			if _, restoreErr := restoreComposePairFromBackups(
				currentControlled, currentRuntime, controlledBackup, runtimeBackup,
				revision.ExpectedDigest,
			); restoreErr != nil {
				return composeApplyResult{State: "needs_attention", ControlledBackup: controlledBackup,
					RuntimeBackup: runtimeBackup, Err: fmt.Errorf("Compose 替换失败且自动恢复失败: %w", restoreErr)}
			}
			return composeApplyResult{State: "rolled_back", ControlledBackup: controlledBackup,
				RuntimeBackup: runtimeBackup, Err: fmt.Errorf("Compose 替换失败，已恢复上一版本: %w", err)}
		}
		return composeApplyResult{State: "failed", ControlledBackup: controlledBackup,
			RuntimeBackup: runtimeBackup, Err: err}
	}

	if _, err := engine.runCompose(ctx, *runtime, runtime.RuntimeCompose, "config", "--quiet"); err != nil {
		return engine.recoverComposeAfterFailure(ctx, runtime, controlled, runtimeFile,
			controlledBackup, runtimeBackup, dependenciesBefore,
			revision.Digest,
			fmt.Errorf("运行 Compose 配置校验失败: %w", err))
	}
	if _, err := engine.runCompose(ctx, *runtime, runtime.RuntimeCompose, "up", "-d", "--no-deps", "--force-recreate", runtime.ApplicationService); err != nil {
		return engine.recoverComposeAfterFailure(ctx, runtime, controlled, runtimeFile,
			controlledBackup, runtimeBackup, dependenciesBefore,
			revision.Digest,
			fmt.Errorf("仅重建应用服务失败: %w", err))
	}
	if err := waitComposeHealth(ctx, runtime.HealthURL); err != nil {
		return engine.recoverComposeAfterFailure(ctx, runtime, controlled, runtimeFile,
			controlledBackup, runtimeBackup, dependenciesBefore,
			revision.Digest,
			fmt.Errorf("应用健康检查失败: %w", err))
	}
	dependenciesAfter, err := engine.composeDependencySnapshot(ctx, runtime)
	if err != nil {
		return engine.recoverComposeAfterFailure(ctx, runtime, controlled, runtimeFile,
			controlledBackup, runtimeBackup, dependenciesBefore, revision.Digest, err)
	}
	if !sameStringMap(dependenciesBefore, dependenciesAfter) {
		return engine.recoverComposeAfterFailure(ctx, runtime, controlled, runtimeFile,
			controlledBackup, runtimeBackup, dependenciesBefore,
			revision.Digest,
			errors.New("Compose 应用服务操作改变了依赖容器身份"))
	}
	if err := verifyComposeDigest(runtime, revision.Digest); err != nil {
		return engine.recoverComposeAfterFailure(ctx, runtime, controlled, runtimeFile,
			controlledBackup, runtimeBackup, dependenciesBefore, revision.Digest, err)
	}
	return composeApplyResult{State: "applied", ControlledBackup: controlledBackup, RuntimeBackup: runtimeBackup}
}

func (engine *Engine) rollbackCompose(
	ctx context.Context, service model.ServiceDefinition, revision model.ComposeRevision,
) composeApplyResult {
	runtime := service.Runtime
	if runtime == nil {
		return composeApplyResult{State: "needs_attention", Err: errors.New("Compose 运行配置缺失")}
	}
	controlled, runtimeFile, err := readComposePair(runtime)
	if err != nil {
		return composeApplyResult{State: "needs_attention", Err: err}
	}
	if controlled.Digest != revision.Digest || runtimeFile.Digest != revision.Digest {
		return composeApplyResult{State: "needs_attention", Err: errors.New("当前 Compose 已偏离目标修订，拒绝回滚覆盖")}
	}
	if err := verifyComposeBackup(revision.BackupControlledPath, revision.ExpectedDigest); err != nil {
		return composeApplyResult{State: "needs_attention", Err: err}
	}
	if err := verifyComposeBackup(revision.BackupRuntimePath, revision.ExpectedDigest); err != nil {
		return composeApplyResult{State: "needs_attention", Err: err}
	}
	dependenciesBefore, err := engine.composeDependencySnapshot(ctx, runtime)
	if err != nil {
		return composeApplyResult{State: "needs_attention", Err: err}
	}
	if changed, err := restoreComposePairFromBackups(
		controlled, runtimeFile, revision.BackupControlledPath, revision.BackupRuntimePath,
		revision.ExpectedDigest,
	); err != nil {
		if changed {
			return composeApplyResult{State: "needs_attention", Err: err}
		}
		return composeApplyResult{State: "needs_attention", Err: err}
	}
	if _, err := engine.runCompose(ctx, *runtime, runtime.RuntimeCompose, "config", "--quiet"); err != nil {
		return composeApplyResult{State: "needs_attention", Err: fmt.Errorf("回滚后 Compose 校验失败: %w", err)}
	}
	if _, err := engine.runCompose(ctx, *runtime, runtime.RuntimeCompose, "up", "-d", "--no-deps", "--force-recreate", runtime.ApplicationService); err != nil {
		return composeApplyResult{State: "needs_attention", Err: fmt.Errorf("回滚后应用服务重建失败: %w", err)}
	}
	if err := waitComposeHealth(ctx, runtime.HealthURL); err != nil {
		return composeApplyResult{State: "needs_attention", Err: fmt.Errorf("回滚后健康检查失败: %w", err)}
	}
	dependenciesAfter, err := engine.composeDependencySnapshot(ctx, runtime)
	if err != nil || !sameStringMap(dependenciesBefore, dependenciesAfter) {
		if err == nil {
			err = errors.New("回滚改变了依赖容器身份")
		}
		return composeApplyResult{State: "needs_attention", Err: err}
	}
	if err := verifyComposeDigest(runtime, revision.ExpectedDigest); err != nil {
		return composeApplyResult{State: "needs_attention", Err: err}
	}
	return composeApplyResult{State: "rolled_back"}
}

func (engine *Engine) recoverComposeAfterFailure(
	ctx context.Context,
	runtime *model.ComposeServiceRuntime,
	controlled, runtimeFile composeFileState,
	controlledBackup, runtimeBackup string,
	dependenciesBefore map[string]string,
	candidateDigest string,
	failure error,
) composeApplyResult {
	currentControlled, currentRuntime, readErr := readComposePair(runtime)
	if readErr != nil {
		return composeApplyResult{State: "needs_attention", ControlledBackup: controlledBackup,
			RuntimeBackup: runtimeBackup, Err: fmt.Errorf("%w；无法读取当前 Compose 身份: %v", failure, readErr)}
	}
	if !safeComposeRestoreState(currentControlled, currentRuntime, controlled.Digest, candidateDigest) {
		return composeApplyResult{State: "needs_attention", ControlledBackup: controlledBackup,
			RuntimeBackup: runtimeBackup, Err: fmt.Errorf("%w；当前 Compose 文件身份已漂移", failure)}
	}
	changed, restoreErr := restoreComposePairFromBackups(
		currentControlled, currentRuntime, controlledBackup, runtimeBackup, controlled.Digest,
	)
	if restoreErr != nil {
		return composeApplyResult{State: "needs_attention", ControlledBackup: controlledBackup,
			RuntimeBackup: runtimeBackup, Err: fmt.Errorf("%w；自动恢复失败: %v", failure, restoreErr)}
	}
	if changed {
		if _, err := engine.runCompose(ctx, *runtime, runtime.RuntimeCompose, "config", "--quiet"); err != nil {
			return composeApplyResult{State: "needs_attention", ControlledBackup: controlledBackup,
				RuntimeBackup: runtimeBackup, Err: fmt.Errorf("%w；恢复后的 Compose 校验失败: %v", failure, err)}
		}
		if _, err := engine.runCompose(ctx, *runtime, runtime.RuntimeCompose, "up", "-d", "--no-deps", "--force-recreate", runtime.ApplicationService); err != nil {
			return composeApplyResult{State: "needs_attention", ControlledBackup: controlledBackup,
				RuntimeBackup: runtimeBackup, Err: fmt.Errorf("%w；恢复应用服务失败: %v", failure, err)}
		}
		if healthErr := waitComposeHealth(ctx, runtime.HealthURL); healthErr != nil {
			return composeApplyResult{State: "needs_attention", ControlledBackup: controlledBackup,
				RuntimeBackup: runtimeBackup, Err: fmt.Errorf("%w；恢复后健康检查失败: %v", failure, healthErr)}
		}
		dependenciesAfter, dependencyErr := engine.composeDependencySnapshot(ctx, runtime)
		if dependencyErr != nil || !sameStringMap(dependenciesBefore, dependenciesAfter) {
			if dependencyErr == nil {
				dependencyErr = errors.New("恢复过程改变了依赖容器身份")
			}
			return composeApplyResult{State: "needs_attention", ControlledBackup: controlledBackup,
				RuntimeBackup: runtimeBackup, Err: fmt.Errorf("%w；%v", failure, dependencyErr)}
		}
	}
	return composeApplyResult{State: "rolled_back", ControlledBackup: controlledBackup,
		RuntimeBackup: runtimeBackup, Err: failure}
}

func safeComposeRestoreState(
	controlled, runtimeFile composeFileState, originalDigest, candidateDigest string,
) bool {
	allowed := map[string]struct{}{originalDigest: {}}
	if candidateDigest != "" {
		allowed[candidateDigest] = struct{}{}
	}
	_, controlledOK := allowed[controlled.Digest]
	_, runtimeOK := allowed[runtimeFile.Digest]
	return controlledOK && runtimeOK
}

func (engine *Engine) runCompose(
	ctx context.Context, runtime model.ComposeServiceRuntime, composePath string, args ...string,
) (string, error) {
	runner := engine.composeRunner
	if runner == nil {
		runner = systemComposeCommandRunner{executable: "/usr/bin/docker"}
	}
	base := []string{"compose", "--project-directory", filepath.Dir(runtime.RuntimeCompose),
		"--env-file", runtime.EnvFile, "-f", composePath}
	base = append(base, args...)
	output, err := runner.Run(ctx, filepath.Dir(runtime.RuntimeCompose), base...)
	if err != nil {
		if output != "" {
			return output, fmt.Errorf("docker compose 执行失败: %w (%s)", err, redactText(output))
		}
		return output, fmt.Errorf("docker compose 执行失败: %w", err)
	}
	return output, nil
}

func validateComposeCandidate(
	ctx context.Context, runtime *model.ComposeServiceRuntime, content string, runner ComposeCommandRunner,
) error {
	if runtime == nil || runtime.RuntimeCompose == "" || runtime.EnvFile == "" {
		return errors.New("Compose 运行配置不完整")
	}
	if runner == nil {
		runner = systemComposeCommandRunner{executable: "/usr/bin/docker"}
	}
	if err := validateComposeContent(content); err != nil {
		return err
	}
	directory := filepath.Dir(runtime.RuntimeCompose)
	// CreateTemp is used after the structural validation so an invalid request
	// never leaves a candidate on disk.
	temporary, err := os.CreateTemp(directory, ".areasong-ops-compose-*")
	if err != nil {
		return fmt.Errorf("创建 Compose 校验文件失败: %w", err)
	}
	path := temporary.Name()
	defer os.Remove(path)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.WriteString(content); err != nil {
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
	base := []string{"compose", "--project-directory", directory, "--env-file", runtime.EnvFile,
		"-f", path, "config", "--quiet"}
	if output, err := runner.Run(ctx, directory, base...); err != nil {
		return fmt.Errorf("候选 Compose config 校验失败: %w (%s)", err, redactText(output))
	}
	return nil
}

func readComposePair(runtime *model.ComposeServiceRuntime) (composeFileState, composeFileState, error) {
	if runtime == nil || runtime.ControlledCompose == "" || runtime.RuntimeCompose == "" {
		return composeFileState{}, composeFileState{}, errors.New("Compose 文件路径配置不完整")
	}
	controlled, err := readComposeFile(runtime.ControlledCompose)
	if err != nil {
		return composeFileState{}, composeFileState{}, err
	}
	runtimeFile, err := readComposeFile(runtime.RuntimeCompose)
	if err != nil {
		return composeFileState{}, composeFileState{}, err
	}
	if os.SameFile(controlled.Info, runtimeFile.Info) {
		return composeFileState{}, composeFileState{}, errors.New("controlled 与 runtime Compose 不得指向同一文件")
	}
	return controlled, runtimeFile, nil
}

func readComposeFile(path string) (composeFileState, error) {
	if !filepath.IsAbs(path) || path == "/" {
		return composeFileState{}, errors.New("Compose 文件路径必须是绝对路径")
	}
	if err := rejectComposeSymlinkPath(path); err != nil {
		return composeFileState{}, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return composeFileState{}, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > maxComposeBytes {
		return composeFileState{}, errors.New("Compose 文件身份或大小无效")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return composeFileState{}, err
	}
	after, err := os.Lstat(path)
	if err != nil || !sameComposeIdentity(info, after) {
		return composeFileState{}, errors.New("读取 Compose 文件期间身份发生变化")
	}
	return composeFileState{Path: path, Content: string(data), Digest: digestText(string(data)), Info: info}, nil
}

func rejectComposeSymlinkPath(path string) error {
	// macOS exposes /var and /tmp through stable system aliases. Reject the
	// target and its immediate project directory (the attacker-controlled
	// portion), while allowing those OS-level aliases above the project root.
	for _, candidate := range []string{filepath.Clean(path), filepath.Dir(filepath.Clean(path))} {
		info, err := os.Lstat(candidate)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("Compose 路径包含符号链接")
		}
	}
	return nil
}

func sameComposeIdentity(before, after os.FileInfo) bool {
	if before == nil || after == nil || before.Mode() != after.Mode() || before.Size() != after.Size() {
		return false
	}
	left, leftOK := before.Sys().(*syscall.Stat_t)
	right, rightOK := after.Sys().(*syscall.Stat_t)
	return !leftOK || !rightOK || (left.Dev == right.Dev && left.Ino == right.Ino)
}

func (engine *Engine) backupComposeFile(
	revision model.ComposeRevision, label string, state composeFileState,
) (string, error) {
	if !uuidPattern.MatchString(revision.ID) || label != "controlled" && label != "runtime" {
		return "", errors.New("Compose 备份标识无效")
	}
	directory := filepath.Join(engine.stateRoot, "compose-backups", revision.Service, revision.ID)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", err
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(directory, label)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	if _, err := io.WriteString(file, state.Content); err != nil {
		file.Close()
		os.Remove(path)
		return "", err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		os.Remove(path)
		return "", err
	}
	if err := file.Close(); err != nil {
		os.Remove(path)
		return "", err
	}
	if err := verifyComposeBackup(path, state.Digest); err != nil {
		os.Remove(path)
		return "", err
	}
	if err := syncComposeDirectory(directory); err != nil {
		return "", err
	}
	return path, nil
}

func verifyComposeBackup(path, expectedDigest string) error {
	if path == "" || !filepath.IsAbs(path) {
		return errors.New("Compose 备份路径无效")
	}
	if err := rejectComposeSymlinkPath(path); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > maxComposeBytes {
		return errors.New("Compose 备份身份无效")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if digestText(string(content)) != expectedDigest {
		return errors.New("Compose 备份摘要不匹配")
	}
	return nil
}

func replaceComposePair(
	controlled, runtimeFile composeFileState, controlledContent, runtimeContent string,
) (bool, error) {
	controlledTemp, err := writeComposeTemp(controlled.Path, controlledContent, controlled.Info)
	if err != nil {
		return false, err
	}
	defer os.Remove(controlledTemp)
	runtimeTemp, err := writeComposeTemp(runtimeFile.Path, runtimeContent, runtimeFile.Info)
	if err != nil {
		return false, err
	}
	defer os.Remove(runtimeTemp)
	if err := verifyComposeState(controlled); err != nil {
		return false, err
	}
	if err := verifyComposeState(runtimeFile); err != nil {
		return false, err
	}
	changed := false
	if err := os.Rename(controlledTemp, controlled.Path); err != nil {
		return false, err
	}
	changed = true
	if err := syncComposeDirectory(filepath.Dir(controlled.Path)); err != nil {
		return changed, err
	}
	if err := verifyComposeState(runtimeFile); err != nil {
		return changed, err
	}
	if err := os.Rename(runtimeTemp, runtimeFile.Path); err != nil {
		return changed, err
	}
	if err := syncComposeDirectory(filepath.Dir(runtimeFile.Path)); err != nil {
		return true, err
	}
	return true, nil
}

func writeComposeTemp(path, content string, original os.FileInfo) (string, error) {
	file, err := os.CreateTemp(filepath.Dir(path), ".areasong-ops-compose-*")
	if err != nil {
		return "", err
	}
	temporary := file.Name()
	cleanup := func(err error) (string, error) {
		_ = file.Close()
		_ = os.Remove(temporary)
		return "", err
	}
	if err := file.Chmod(original.Mode().Perm()); err != nil {
		return cleanup(err)
	}
	if stat, ok := original.Sys().(*syscall.Stat_t); ok {
		if err := file.Chown(int(stat.Uid), int(stat.Gid)); err != nil {
			return cleanup(err)
		}
	}
	if _, err := io.WriteString(file, content); err != nil {
		return cleanup(err)
	}
	if err := file.Sync(); err != nil {
		return cleanup(err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(temporary)
		return "", err
	}
	return temporary, nil
}

func verifyComposeState(expected composeFileState) error {
	actual, err := readComposeFile(expected.Path)
	if err != nil {
		return err
	}
	if actual.Digest != expected.Digest || !sameComposeIdentity(expected.Info, actual.Info) {
		return errors.New("Compose 文件基线在替换前已变化")
	}
	return nil
}

func restoreComposePairFromBackups(
	controlled, runtimeFile composeFileState,
	controlledBackup, runtimeBackup, expectedDigest string,
) (bool, error) {
	if err := verifyComposeBackup(controlledBackup, expectedDigest); err != nil {
		return false, err
	}
	if err := verifyComposeBackup(runtimeBackup, expectedDigest); err != nil {
		return false, err
	}
	controlledContent, err := os.ReadFile(controlledBackup)
	if err != nil {
		return false, err
	}
	runtimeContent, err := os.ReadFile(runtimeBackup)
	if err != nil {
		return false, err
	}
	return replaceComposePair(controlled, runtimeFile, string(controlledContent), string(runtimeContent))
}

func syncComposeDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func verifyComposeDigest(runtime *model.ComposeServiceRuntime, expected string) error {
	controlled, runtimeFile, err := readComposePair(runtime)
	if err != nil {
		return err
	}
	if controlled.Digest != expected || runtimeFile.Digest != expected || controlled.Digest != runtimeFile.Digest {
		return errors.New("Compose 最终摘要核验失败")
	}
	return nil
}

func (engine *Engine) composeDependencySnapshot(
	ctx context.Context, runtime *model.ComposeServiceRuntime,
) (map[string]string, error) {
	result := make(map[string]string, len(runtime.DependencyContainers))
	for _, dependency := range runtime.DependencyContainers {
		dependency = strings.TrimSpace(dependency)
		if dependency == "" {
			return nil, errors.New("Compose 依赖服务名称不能为空")
		}
		output, err := engine.runCompose(ctx, *runtime, runtime.RuntimeCompose, "ps", "-q", dependency)
		if err != nil {
			return nil, fmt.Errorf("读取依赖容器身份失败: %w", err)
		}
		ids := strings.Fields(output)
		sort.Strings(ids)
		result[dependency] = strings.Join(ids, "\n")
	}
	return result, nil
}

func sameStringMap(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}

func waitComposeHealth(ctx context.Context, rawURL string) error {
	if rawURL == "" {
		return errors.New("Compose 健康地址未配置")
	}
	requestURL := strings.TrimSpace(rawURL)
	parsedURL, err := http.NewRequest(http.MethodGet, requestURL, nil)
	if err != nil || parsedURL.URL.Scheme != "http" ||
		(parsedURL.URL.Hostname() != "127.0.0.1" && parsedURL.URL.Hostname() != "localhost") ||
		parsedURL.URL.User != nil {
		return errors.New("Compose 健康地址无效")
	}
	client := &http.Client{Timeout: 5 * time.Second}
	deadline := time.Now().Add(composeHealthTimeout)
	var lastErr error
	for {
		retryRequest, requestErr := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
		if requestErr != nil {
			return errors.New("Compose 健康地址无效")
		}
		response, requestErr := client.Do(retryRequest)
		if requestErr == nil {
			_, _ = io.CopyN(io.Discard, response.Body, 64<<10)
			response.Body.Close()
			if response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices {
				return nil
			}
			lastErr = fmt.Errorf("HTTP 状态 %d", response.StatusCode)
		} else {
			lastErr = requestErr
		}
		if time.Now().After(deadline) {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	return fmt.Errorf("健康检查超时: %w", lastErr)
}

func redactComposeError(err error) string {
	if err == nil {
		return ""
	}
	message := redactText(err.Error())
	if len(message) > 4096 {
		message = message[:4096]
	}
	return message
}
