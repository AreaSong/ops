package runner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

const fleetRunnerUpdatePollInterval = 250 * time.Millisecond

func (engine *Engine) startFleetRunnerUpdateCoordinator(id string) {
	engine.wait.Add(1)
	go func() {
		defer engine.wait.Done()
		engine.runFleetRunnerUpdateCoordinator(engine.fleetUpdateCtx, id)
	}()
}

func (engine *Engine) resumeFleetRunnerUpdates() error {
	ctx := engine.fleetUpdateCtx
	if err := engine.expireFleetRunnerUpdatePlans(ctx, time.Now().UTC()); err != nil {
		return err
	}
	if _, err := engine.store.RecoverFleetRunnerUpdateLeases(ctx, time.Now().UTC()); err != nil {
		return err
	}
	ids, err := engine.store.ListActiveFleetRunnerUpdatePlanIDs(ctx)
	if err != nil {
		return err
	}
	for _, id := range ids {
		engine.startFleetRunnerUpdateCoordinator(id)
	}
	return nil
}

func (engine *Engine) runFleetRunnerUpdateCoordinator(ctx context.Context, id string) {
	consecutiveErrors := 0
	for {
		done, err := engine.reconcileFleetRunnerUpdate(ctx, id, time.Now().UTC())
		if done || errors.Is(err, context.Canceled) {
			return
		}
		delay := fleetRunnerUpdatePollInterval
		if err != nil {
			consecutiveErrors++
			delay = fleetRunnerUpdateRetryDelay(consecutiveErrors)
			slog.Error("Runner Fleet 更新协调失败，将重试", "plan_id", id, "error", err, "retry_in", delay)
		} else {
			consecutiveErrors = 0
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func fleetRunnerUpdateRetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		return fleetRunnerUpdatePollInterval
	}
	if attempt > 5 {
		attempt = 5
	}
	delay := fleetRunnerUpdatePollInterval * time.Duration(1<<(attempt-1))
	if delay > 5*time.Second {
		return 5 * time.Second
	}
	return delay
}

func (engine *Engine) reconcileFleetRunnerUpdate(
	ctx context.Context,
	id string,
	now time.Time,
) (bool, error) {
	if _, err := engine.store.RecoverFleetRunnerUpdateLeases(ctx, now); err != nil {
		return false, err
	}
	plan, err := engine.store.GetFleetRunnerUpdatePlan(ctx, id)
	if err != nil {
		return false, err
	}
	if plan.State.Terminal() {
		return true, nil
	}
	if plan.ChangeWindow == nil {
		return engine.finishFleetRunnerUpdateNeedsAttention(ctx, plan, "计划缺少变更窗口")
	}
	if plan.State != model.FleetRunnerUpdateRollingBack && !now.Before(plan.ChangeWindow.EndAt) {
		return false, engine.store.BeginFleetRunnerUpdateRollback(ctx, plan.ID, "变更窗口已结束")
	}
	if plan.State == model.FleetRunnerUpdateRunning || plan.State == model.FleetRunnerUpdateObserving {
		if reason := fleetRunnerUpdateFailure(plan.Items); reason != "" {
			return false, engine.store.BeginFleetRunnerUpdateRollback(ctx, plan.ID, reason)
		}
	}
	switch plan.State {
	case model.FleetRunnerUpdateRunning:
		return engine.reconcileFleetRunnerUpdateRunning(ctx, plan, now)
	case model.FleetRunnerUpdateObserving:
		return engine.reconcileFleetRunnerUpdateObserving(ctx, plan, now)
	case model.FleetRunnerUpdateRollingBack:
		return engine.reconcileFleetRunnerUpdateRollback(ctx, plan, now)
	default:
		return false, nil
	}
}

func (engine *Engine) reconcileFleetRunnerUpdateRunning(
	ctx context.Context,
	plan model.FleetRunnerUpdatePlan,
	now time.Time,
) (bool, error) {
	wave := fleetRunnerUpdateBatchItems(plan.Items, plan.CurrentBatch)
	if len(wave) == 0 {
		return engine.finishFleetRunnerUpdateNeedsAttention(ctx, plan, "当前批次没有目标")
	}
	if !fleetRunnerUpdateItemsSucceeded(wave) {
		return false, nil
	}
	if err := engine.verifyFleetRunnerObservedIdentities(ctx, plan, wave, false, now); err != nil {
		return false, engine.store.BeginFleetRunnerUpdateRollback(ctx, plan.ID, err.Error())
	}
	if plan.BatchPolicy.Strategy == model.BatchCanary && plan.CurrentBatch == 0 &&
		plan.ObservationStartedAt == nil {
		ends := now.Add(time.Duration(plan.BatchPolicy.ObservationSeconds) * time.Second)
		if plan.ChangeWindow.EndAt.Before(ends) {
			return false, engine.store.BeginFleetRunnerUpdateRollback(ctx, plan.ID, "Canary 观察窗口超出变更窗口")
		}
		return false, engine.store.BeginFleetRunnerUpdateObservation(ctx, plan.ID, now, ends)
	}
	if !fleetRunnerUpdatePauseElapsed(wave, plan.BatchPolicy.PauseSeconds, now) {
		return false, nil
	}
	return engine.releaseNextFleetRunnerUpdateBatch(ctx, plan, now)
}

func (engine *Engine) reconcileFleetRunnerUpdateObserving(
	ctx context.Context,
	plan model.FleetRunnerUpdatePlan,
	now time.Time,
) (bool, error) {
	if plan.ObservationStartedAt == nil || plan.ObservationEndsAt == nil {
		return engine.finishFleetRunnerUpdateNeedsAttention(ctx, plan, "Canary 观察状态不完整")
	}
	if now.Before(*plan.ObservationEndsAt) {
		return false, nil
	}
	wave := fleetRunnerUpdateBatchItems(plan.Items, plan.CurrentBatch)
	if err := engine.verifyFleetRunnerObservedIdentities(ctx, plan, wave, false, now); err != nil {
		return false, engine.store.BeginFleetRunnerUpdateRollback(ctx, plan.ID, err.Error())
	}
	return engine.releaseNextFleetRunnerUpdateBatch(ctx, plan, now)
}

func (engine *Engine) releaseNextFleetRunnerUpdateBatch(
	ctx context.Context,
	plan model.FleetRunnerUpdatePlan,
	now time.Time,
) (bool, error) {
	next := plan.CurrentBatch + 1
	if next <= fleetRunnerUpdateMaxBatch(plan.Items) {
		return false, engine.store.ReleaseFleetRunnerUpdateBatch(ctx, plan.ID, next, now)
	}
	if err := engine.verifyFleetRunnerObservedIdentities(ctx, plan, plan.Items, false, now); err != nil {
		return false, engine.store.BeginFleetRunnerUpdateRollback(ctx, plan.ID, err.Error())
	}
	if err := engine.store.FinishFleetRunnerUpdatePlan(ctx, plan.ID, model.FleetRunnerUpdateSucceeded,
		"所有 Runner 已完成分批更新和身份复验", ""); err != nil {
		return false, err
	}
	_ = os.Remove(plan.StagedPath)
	return true, nil
}

func (engine *Engine) reconcileFleetRunnerUpdateRollback(
	ctx context.Context,
	plan model.FleetRunnerUpdatePlan,
	now time.Time,
) (bool, error) {
	for _, item := range plan.Items {
		switch item.State {
		case model.FleetRunnerUpdateItemRunning, model.FleetRunnerUpdateItemSucceeded,
			model.FleetRunnerUpdateItemRollbackReady, model.FleetRunnerUpdateItemRollingBack:
			return false, nil
		}
	}
	if err := engine.verifyFleetRunnerObservedIdentities(
		ctx, plan, plan.Items, true, now,
	); err != nil {
		return engine.finishFleetRunnerUpdateNeedsAttention(ctx, plan, err.Error())
	}
	for _, item := range plan.Items {
		if item.State == model.FleetRunnerUpdateItemNeedsAttention {
			return engine.finishFleetRunnerUpdateNeedsAttention(ctx, plan, "至少一个 Runner 无法确认回滚身份")
		}
	}
	if err := engine.store.FinishFleetRunnerUpdatePlan(ctx, plan.ID, model.FleetRunnerUpdateRolledBack,
		"已停止后续批次并逐节点完成回滚", plan.Error); err != nil {
		return false, err
	}
	_ = os.Remove(plan.StagedPath)
	return true, nil
}

func (engine *Engine) finishFleetRunnerUpdateNeedsAttention(
	ctx context.Context,
	plan model.FleetRunnerUpdatePlan,
	reason string,
) (bool, error) {
	err := engine.store.FinishFleetRunnerUpdatePlan(ctx, plan.ID, model.FleetRunnerUpdateNeedsAttention,
		"Runner Fleet 更新需要人工核对", reason)
	return err == nil, err
}

func fleetRunnerUpdateFailure(items []model.FleetRunnerUpdateItem) string {
	for _, item := range items {
		if item.State == model.FleetRunnerUpdateItemFailed ||
			item.State == model.FleetRunnerUpdateItemNeedsAttention ||
			item.State == model.FleetRunnerUpdateItemRolledBack {
			if item.Error != "" {
				return fmt.Sprintf("Runner %s 更新失败: %s", item.RunnerID, item.Error)
			}
			return fmt.Sprintf("Runner %s 更新失败", item.RunnerID)
		}
	}
	return ""
}

func fleetRunnerUpdateBatchItems(items []model.FleetRunnerUpdateItem, batch int) []model.FleetRunnerUpdateItem {
	result := make([]model.FleetRunnerUpdateItem, 0)
	for _, item := range items {
		if item.BatchIndex == batch {
			result = append(result, item)
		}
	}
	return result
}

func fleetRunnerUpdateItemsSucceeded(items []model.FleetRunnerUpdateItem) bool {
	if len(items) == 0 {
		return false
	}
	for _, item := range items {
		if item.State != model.FleetRunnerUpdateItemSucceeded {
			return false
		}
	}
	return true
}

func fleetRunnerUpdatePauseElapsed(items []model.FleetRunnerUpdateItem, seconds int, now time.Time) bool {
	if seconds <= 0 {
		return true
	}
	latest := time.Time{}
	for _, item := range items {
		if item.FinishedAt == nil {
			return false
		}
		if item.FinishedAt.After(latest) {
			latest = *item.FinishedAt
		}
	}
	return !now.Before(latest.Add(time.Duration(seconds) * time.Second))
}

func fleetRunnerUpdateMaxBatch(items []model.FleetRunnerUpdateItem) int {
	result := -1
	for _, item := range items {
		if item.BatchIndex > result {
			result = item.BatchIndex
		}
	}
	return result
}

func (engine *Engine) verifyFleetRunnerObservedIdentities(
	ctx context.Context,
	plan model.FleetRunnerUpdatePlan,
	items []model.FleetRunnerUpdateItem,
	rollback bool,
	now time.Time,
) error {
	timeout := time.Duration(engine.catalog.Fleet.HeartbeatTimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 90 * time.Second
	}
	for _, item := range items {
		if rollback && item.State != model.FleetRunnerUpdateItemRolledBack &&
			item.State != model.FleetRunnerUpdateItemFailed {
			continue
		}
		if !rollback && item.State != model.FleetRunnerUpdateItemSucceeded {
			continue
		}
		node, found, err := engine.store.GetRunnerNode(ctx, item.RunnerID)
		if err != nil || !found {
			return fmt.Errorf("Runner %s 身份不可读", item.RunnerID)
		}
		version, revision, digest := plan.Manifest.TargetVersion, plan.Manifest.ArtifactRevision, plan.Manifest.ArtifactDigest
		if rollback {
			version, revision, digest = item.PreviousVersion, item.PreviousRevision, item.PreviousDigest
		}
		if node.TenantID != plan.TenantID || node.ServerID != item.ServerID ||
			node.Version != version || node.Revision != revision || node.BinaryDigest != digest ||
			node.IdentityPayloadVersion < RunnerIdentityPayloadVersion ||
			node.LeaseGeneration <= item.ExpectedLeaseGeneration || !node.AvailableAt(now, timeout) ||
			node.CertificateFingerprint != item.CertificateFingerprint {
			return fmt.Errorf("Runner %s 运行身份、租约或证书复验失败", item.RunnerID)
		}
	}
	return nil
}

func (engine *Engine) ensureFleetRunnerCompletionIdentity(
	assignment model.FleetRunnerUpdateAssignment,
	input model.FleetRunnerUpdateCompletionRequest,
) error {
	if !uuidPattern.MatchString(input.IdempotencyKey) && !runnerDigestPattern.MatchString(input.IdempotencyKey) {
		return errors.New("Runner Fleet 更新完成幂等键无效")
	}
	if len(input.Error) > 4096 {
		return errors.New("Runner Fleet 更新错误信息超过限制")
	}
	if input.State == "succeeded" {
		if input.ObservedVersion != assignment.Manifest.TargetVersion ||
			input.ObservedRevision != assignment.Manifest.ArtifactRevision ||
			input.ObservedDigest != assignment.Manifest.ArtifactDigest {
			return errors.New("Runner Fleet 更新成功回执身份不匹配")
		}
	}
	if input.State == "rolled_back" {
		if input.ObservedVersion != assignment.PreviousVersion ||
			input.ObservedRevision != assignment.PreviousRevision ||
			input.ObservedDigest != assignment.PreviousDigest {
			return errors.New("Runner Fleet 更新回滚回执身份不匹配")
		}
	}
	if input.State == "failed" || input.State == "needs_attention" {
		if input.Error == "" {
			return errors.New("Runner Fleet 更新失败回执缺少错误信息")
		}
		if input.ObservedVersion != "" && !runnerVersionPattern.MatchString(input.ObservedVersion) {
			return errors.New("Runner Fleet 更新失败回执版本无效")
		}
		if input.ObservedRevision != "" && !runnerRevisionPattern.MatchString(input.ObservedRevision) {
			return errors.New("Runner Fleet 更新失败回执 revision 无效")
		}
		if input.ObservedDigest != "" && !runnerDigestPattern.MatchString(input.ObservedDigest) {
			return errors.New("Runner Fleet 更新失败回执摘要无效")
		}
	}
	return nil
}
