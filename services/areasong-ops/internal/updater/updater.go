package updater

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	"github.com/AreaSong/ops/services/areasong-ops/internal/store"
)

const (
	stateActivating     = "activating"
	stateSucceeded      = "succeeded"
	stateFailed         = "failed"
	stateRolledBack     = "rolled_back"
	stateNeedsAttention = "needs_attention"
	maxArtifactBytes    = int64(1 << 30)
)

var (
	updateIDPattern = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-4[a-f0-9]{3}-[89ab][a-f0-9]{3}-[a-f0-9]{12}$`)
	digestPattern   = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
	revisionPattern = regexp.MustCompile(`^[a-f0-9]{40}$`)
	unitPattern     = regexp.MustCompile(`^[A-Za-z0-9_.@-]+\.service$`)
)

type Controller interface {
	Restart(context.Context, string) error
	WaitIdentity(context.Context, string, string, string, string, time.Duration) error
}

// identitySetter is an optional hook used only by isolated development
// controllers. Production systemd controllers prove the identity by reading
// the restarted process; a local controller can persist the simulated runtime
// identity before the same health check without changing the production path.
type identitySetter interface {
	SetIdentity(string, string) error
}

type Executor struct {
	Store      *store.Store
	StateRoot  string
	Controller Controller
}

func (executor *Executor) Run(ctx context.Context, id string) (string, error) {
	update, err := executor.load(ctx, id)
	if err != nil {
		if update.ID != "" && update.State == stateActivating {
			err = executor.finishNeedsAttention(ctx, update, "contract_invalid", err)
			return stateNeedsAttention, err
		}
		return update.State, err
	}
	if update.State != stateActivating {
		return update.State, err
	}
	binaryLock, err := acquireBinaryLock(update.BinaryPath)
	if err != nil {
		return stateNeedsAttention, err
	}
	defer binaryLock.Close()
	if err := executor.validateLease(update); err != nil {
		// A stale executor must never renew or otherwise resurrect an expired
		// activation. The Runner maintenance loop owns recovery of that row.
		return stateNeedsAttention, err
	}
	operationContext, stopHeartbeat := executor.withHeartbeat(ctx, id, update.FencingToken)
	defer stopHeartbeat()
	if update.Phase == "rolling_back" {
		return executor.resumeRollback(operationContext, update)
	}
	if err := executor.Store.UpdateRunnerUpdatePhase(operationContext, id, "validating", "", update.FencingToken); err != nil {
		return stateFailed, executor.finishBeforeMutation(ctx, update, "validation_failed", err)
	}
	return executor.apply(operationContext, update)
}

func (executor *Executor) load(ctx context.Context, id string) (model.RunnerUpdate, error) {
	if executor.Store == nil || executor.Controller == nil || executor.StateRoot == "" {
		return model.RunnerUpdate{}, errors.New("Runner updater 配置不完整")
	}
	if !updateIDPattern.MatchString(id) {
		return model.RunnerUpdate{}, errors.New("Runner 更新 ID 无效")
	}
	update, err := executor.Store.GetRunnerUpdate(ctx, id)
	if err != nil {
		return model.RunnerUpdate{}, err
	}
	if isTerminal(update.State) {
		return update, nil
	}
	if err := executor.validate(update); err != nil {
		return update, err
	}
	return update, nil
}

func (executor *Executor) validate(update model.RunnerUpdate) error {
	expectedStaged := filepath.Join(executor.StateRoot, "runner-updates", "staged", update.ID+".runner")
	if update.State != stateActivating || update.StagedPath != expectedStaged ||
		!digestPattern.MatchString(update.ArtifactDigest) ||
		!digestPattern.MatchString(update.PreviousDigest) ||
		!revisionPattern.MatchString(update.ArtifactRevision) {
		return errors.New("Runner 更新持久化身份无效")
	}
	if err := executor.validateLease(update); err != nil {
		return err
	}
	if !filepath.IsAbs(update.BinaryPath) || filepath.Clean(update.BinaryPath) != update.BinaryPath ||
		!unitPattern.MatchString(update.UnitName) || update.HealthTimeoutSeconds < 1 ||
		update.HealthTimeoutSeconds > 300 || update.TargetVersion == "" || update.PreviousVersion == "" {
		return errors.New("Runner 更新目标配置无效")
	}
	if err := requireRegularFile(update.StagedPath, maxArtifactBytes); err != nil {
		return fmt.Errorf("Runner 暂存制品无效: %w", err)
	}
	if digest, err := hashFile(update.StagedPath, maxArtifactBytes); err != nil || digest != update.ArtifactDigest {
		return errors.New("Runner 暂存制品摘要不匹配")
	}
	return requireRegularFile(update.BinaryPath, maxArtifactBytes)
}

func (executor *Executor) validateLease(update model.RunnerUpdate) error {
	if update.FencingToken == "" || update.LeaseExpiresAt == nil {
		return errors.New("Runner 更新执行租约无效")
	}
	if !update.LeaseExpiresAt.After(time.Now().UTC()) {
		return errors.New("Runner 更新执行租约已过期")
	}
	return nil
}

func (executor *Executor) apply(
	ctx context.Context,
	update model.RunnerUpdate,
) (string, error) {
	currentDigest, err := hashFile(update.BinaryPath, maxArtifactBytes)
	if err != nil {
		return stateFailed, executor.finishBeforeMutation(ctx, update, "identity_failed", err)
	}
	if currentDigest != update.PreviousDigest && currentDigest != update.ArtifactDigest {
		err := errors.New("当前 Runner 二进制既不是准备基线也不是目标制品")
		return stateNeedsAttention, executor.finishNeedsAttention(ctx, update, "identity_drift", err)
	}
	rollbackPath, err := executor.ensureRollback(ctx, update, currentDigest)
	if err != nil {
		return stateNeedsAttention, executor.finishNeedsAttention(ctx, update, "backup_failed", err)
	}
	if currentDigest == update.PreviousDigest {
		if update.Phase == "installed" || update.Phase == "restarting" || update.Phase == "verifying" {
			err := errors.New("Runner 更新阶段与当前二进制身份冲突")
			return stateNeedsAttention, executor.finishNeedsAttention(ctx, update, "phase_identity_conflict", err)
		}
		if err := executor.install(ctx, update, rollbackPath); err != nil {
			return executor.rollback(ctx, update, rollbackPath, err)
		}
	}
	return executor.restartAndVerify(ctx, update, rollbackPath)
}

func (executor *Executor) ensureRollback(
	ctx context.Context,
	update model.RunnerUpdate,
	currentDigest string,
) (string, error) {
	rollbackPath := filepath.Join(executor.StateRoot, "runner-updates", "rollback", update.ID+".runner")
	if update.RollbackPath != "" && update.RollbackPath != rollbackPath {
		return "", errors.New("Runner 回滚路径与持久化记录不一致")
	}
	if exists, err := validDigestFile(rollbackPath, update.PreviousDigest, maxArtifactBytes); err != nil {
		return "", err
	} else if exists {
		if err := executor.Store.UpdateRunnerUpdatePhase(ctx, update.ID, "backup_ready", rollbackPath, update.FencingToken); err != nil {
			return "", err
		}
		return rollbackPath, nil
	}
	if currentDigest != update.PreviousDigest {
		return "", errors.New("目标二进制已改变但缺少有效回滚副本")
	}
	if err := executor.Store.UpdateRunnerUpdatePhase(ctx, update.ID, "backing_up", "", update.FencingToken); err != nil {
		return "", err
	}
	if err := copyExclusive(update.BinaryPath, rollbackPath, 0o700, update.PreviousDigest); err != nil {
		return "", err
	}
	if err := executor.Store.UpdateRunnerUpdatePhase(ctx, update.ID, "backup_ready", rollbackPath, update.FencingToken); err != nil {
		return "", err
	}
	return rollbackPath, nil
}

func (executor *Executor) install(
	ctx context.Context,
	update model.RunnerUpdate,
	rollbackPath string,
) error {
	if err := executor.Store.UpdateRunnerUpdatePhase(ctx, update.ID, "installing", rollbackPath, update.FencingToken); err != nil {
		return err
	}
	if err := atomicReplace(update.StagedPath, update.BinaryPath, update.ArtifactDigest, update.PreviousDigest); err != nil {
		return err
	}
	digest, err := hashFile(update.BinaryPath, maxArtifactBytes)
	if err != nil || digest != update.ArtifactDigest {
		return errors.New("Runner 目标二进制安装后摘要不一致")
	}
	return executor.Store.UpdateRunnerUpdatePhase(ctx, update.ID, "installed", rollbackPath, update.FencingToken)
}

func (executor *Executor) restartAndVerify(
	ctx context.Context,
	update model.RunnerUpdate,
	rollbackPath string,
) (string, error) {
	if err := executor.Store.UpdateRunnerUpdatePhase(ctx, update.ID, "restarting", rollbackPath, update.FencingToken); err != nil {
		return executor.rollback(ctx, update, rollbackPath, err)
	}
	if setter, ok := executor.Controller.(identitySetter); ok {
		if err := setter.SetIdentity(update.TargetVersion, update.ArtifactRevision); err != nil {
			return executor.rollback(ctx, update, rollbackPath, err)
		}
	}
	if err := executor.Controller.Restart(ctx, update.UnitName); err != nil {
		return executor.rollback(ctx, update, rollbackPath, err)
	}
	if err := executor.Store.UpdateRunnerUpdatePhase(ctx, update.ID, "verifying", rollbackPath, update.FencingToken); err != nil {
		return executor.rollback(ctx, update, rollbackPath, err)
	}
	timeout := time.Duration(update.HealthTimeoutSeconds) * time.Second
	if err := executor.Controller.WaitIdentity(ctx, update.UnitName, update.BinaryPath, update.TargetVersion, update.ArtifactRevision, timeout); err != nil {
		return executor.rollback(ctx, update, rollbackPath, err)
	}
	digest, err := hashFile(update.BinaryPath, maxArtifactBytes)
	if err != nil || digest != update.ArtifactDigest {
		return executor.rollback(ctx, update, rollbackPath, errors.New("Runner 健康后制品摘要不一致"))
	}
	if err := executor.finish(ctx, update, stateSucceeded, "verified", rollbackPath, ""); err != nil {
		return update.State, err
	}
	return stateSucceeded, nil
}

func (executor *Executor) rollback(
	ctx context.Context,
	update model.RunnerUpdate,
	rollbackPath string,
	cause error,
) (string, error) {
	update.RollbackPath = rollbackPath
	if err := executor.Store.UpdateRunnerUpdatePhase(ctx, update.ID, "rolling_back", rollbackPath, update.FencingToken); err != nil {
		return stateNeedsAttention, executor.finishNeedsAttention(ctx, update, "rollback_state_failed", errors.Join(cause, err))
	}
	if err := atomicReplace(rollbackPath, update.BinaryPath, update.PreviousDigest, update.ArtifactDigest); err != nil {
		return stateNeedsAttention, executor.finishNeedsAttention(ctx, update, "rollback_install_failed", errors.Join(cause, err))
	}
	if err := executor.Controller.Restart(ctx, update.UnitName); err != nil {
		return stateNeedsAttention, executor.finishNeedsAttention(ctx, update, "rollback_restart_failed", errors.Join(cause, err))
	}
	if err := executor.verifyPrevious(ctx, update); err != nil {
		return stateNeedsAttention, executor.finishNeedsAttention(ctx, update, "rollback_verify_failed", errors.Join(cause, err))
	}
	if err := executor.finish(ctx, update, stateRolledBack, "rollback_verified", rollbackPath, cause.Error()); err != nil {
		return update.State, err
	}
	return stateRolledBack, nil
}

func (executor *Executor) resumeRollback(
	ctx context.Context,
	update model.RunnerUpdate,
) (string, error) {
	rollbackPath := filepath.Join(executor.StateRoot, "runner-updates", "rollback", update.ID+".runner")
	if valid, _ := validDigestFile(rollbackPath, update.PreviousDigest, maxArtifactBytes); !valid {
		err := errors.New("中断的 Runner 回滚缺少有效回滚副本")
		return stateNeedsAttention, executor.finishNeedsAttention(ctx, update, "rollback_resume_failed", err)
	}
	return executor.rollback(ctx, update, rollbackPath, errors.New("Runner 回滚执行曾中断"))
}

func (executor *Executor) verifyPrevious(ctx context.Context, update model.RunnerUpdate) error {
	if setter, ok := executor.Controller.(identitySetter); ok {
		if err := setter.SetIdentity(update.PreviousVersion, update.PreviousRevision); err != nil {
			return err
		}
	}
	timeout := time.Duration(update.HealthTimeoutSeconds) * time.Second
	if err := executor.Controller.WaitIdentity(ctx, update.UnitName, update.BinaryPath, update.PreviousVersion, update.PreviousRevision, timeout); err != nil {
		return err
	}
	digest, err := hashFile(update.BinaryPath, maxArtifactBytes)
	if err != nil || digest != update.PreviousDigest {
		return errors.New("Runner 回滚后二进制摘要不一致")
	}
	return nil
}

func (executor *Executor) finishBeforeMutation(
	ctx context.Context,
	update model.RunnerUpdate,
	phase string,
	cause error,
) error {
	if err := executor.finish(ctx, update, stateFailed, phase, update.RollbackPath, cause.Error()); err != nil {
		return errors.Join(cause, err)
	}
	return cause
}

func (executor *Executor) finishNeedsAttention(
	ctx context.Context,
	update model.RunnerUpdate,
	phase string,
	cause error,
) error {
	if err := executor.finish(ctx, update, stateNeedsAttention, phase, update.RollbackPath, cause.Error()); err != nil {
		return errors.Join(cause, err)
	}
	return cause
}

func (executor *Executor) finish(
	ctx context.Context,
	update model.RunnerUpdate,
	state, phase, rollbackPath, message string,
) error {
	finishContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	if err := executor.Store.FinishRunnerUpdate(
		finishContext, update.ID, state, phase, rollbackPath, message, update.FencingToken,
	); err != nil {
		return err
	}
	_, _ = executor.Store.AppendAudit(finishContext, model.AuditEntry{
		ActorHash: update.ApprovedByHash, Event: "runner.update." + state,
		Resource: "runner:" + update.RunnerID, Outcome: state,
		Detail: map[string]any{"updateId": update.ID, "phase": phase, "error": message},
	})
	return nil
}

func (executor *Executor) withHeartbeat(
	ctx context.Context,
	id, fencingToken string,
) (context.Context, context.CancelFunc) {
	heartbeatContext, cancel := context.WithCancel(ctx)
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatContext.Done():
				return
			case <-ticker.C:
				if err := executor.Store.HeartbeatRunnerUpdate(heartbeatContext, id, fencingToken); err != nil {
					cancel()
					return
				}
			}
		}
	}()
	return heartbeatContext, cancel
}

func isTerminal(state string) bool {
	return state == stateSucceeded || state == stateFailed ||
		state == stateRolledBack || state == stateNeedsAttention
}
