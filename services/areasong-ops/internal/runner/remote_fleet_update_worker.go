package runner

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/buildinfo"
	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	"github.com/AreaSong/ops/services/areasong-ops/internal/store"
)

var errFleetRunnerAssignmentTerminal = errors.New("Runner Fleet 更新 assignment 已由控制面收口")

func (worker *RemoteWorker) fleetRunnerUpdatesConfigured() bool {
	return worker.Catalog != nil && worker.Catalog.Fleet != nil && worker.Catalog.Fleet.Enabled &&
		worker.Catalog.RunnerUpdate != nil && worker.Catalog.RunnerUpdate.Enabled &&
		worker.Catalog.RunnerUpdate.FleetEnabled
}

func (worker *RemoteWorker) claimFleetRunnerUpdate(
	ctx context.Context,
) (*model.FleetRunnerUpdateAssignment, error) {
	var assignment model.FleetRunnerUpdateAssignment
	status, err := worker.request(ctx, http.MethodPost, "fleet-updates/claim", model.FleetRunnerUpdateClaimRequest{
		LeaseSeconds: int(worker.Lease / time.Second),
	}, &assignment)
	if err != nil {
		return nil, err
	}
	if status == http.StatusNoContent {
		return nil, nil
	}
	return &assignment, nil
}

func (worker *RemoteWorker) acceptFleetRunnerUpdate(
	ctx context.Context,
	assignment model.FleetRunnerUpdateAssignment,
) error {
	if err := worker.validateFleetRunnerAssignment(assignment); err != nil {
		return worker.rejectFleetRunnerAssignment(ctx, assignment, err)
	}
	receipt := model.FleetRunnerUpdateReceipt{
		ItemID: assignment.ItemID, AssignmentGeneration: assignment.Fence.Generation,
		PlanID: assignment.PlanID, Fence: assignment.Fence,
		ControlPlaneEndpoint: strings.TrimRight(worker.Endpoint, "/"),
		LocalUpdateID:        deterministicFleetRunnerUUID("local-update", assignment),
		Action:               assignment.Action, Assignment: assignment,
	}
	stored, _, err := worker.Store.SaveFleetRunnerUpdateReceipt(ctx, receipt)
	if err != nil {
		return err
	}
	err = worker.processFleetRunnerUpdateReceipt(ctx, stored)
	if errors.Is(err, errFleetRunnerAssignmentTerminal) {
		return nil
	}
	return err
}

func (worker *RemoteWorker) resumeFleetRunnerUpdateReceipts(ctx context.Context) (bool, error) {
	receipts, err := worker.Store.ListPendingFleetRunnerUpdateReceipts(ctx)
	if err != nil || len(receipts) == 0 {
		return false, err
	}
	receipt := receipts[0]
	if receipt.ControlPlaneEndpoint != strings.TrimRight(worker.Endpoint, "/") {
		return true, errors.New("Runner Fleet 更新回执控制面身份不一致")
	}
	if receipt.State == "needs_attention" {
		return true, worker.reportFleetRunnerNeedsAttention(ctx, receipt, receipt.LastError)
	}
	err = worker.processFleetRunnerUpdateReceipt(ctx, receipt)
	if errors.Is(err, errFleetRunnerAssignmentTerminal) {
		err = nil
	}
	return true, err
}

func (worker *RemoteWorker) processFleetRunnerUpdateReceipt(
	ctx context.Context,
	receipt model.FleetRunnerUpdateReceipt,
) error {
	if err := worker.validateFleetRunnerAssignment(receipt.Assignment); err != nil {
		return worker.persistAndReportFleetRunnerFailure(ctx, receipt, err)
	}
	if !worker.now().UTC().Before(receipt.Assignment.ExecutionDeadlineAt) {
		return worker.persistAndReportFleetRunnerFailure(ctx, receipt, errors.New("Runner Fleet 更新执行截止时间已过"))
	}
	if err := worker.heartbeatFleetRunnerUpdate(ctx, receipt); err != nil {
		return err
	}
	update, err := worker.Store.GetRunnerUpdate(ctx, receipt.LocalUpdateID)
	if errors.Is(err, store.ErrNotFound) {
		update, err = worker.prepareFleetRunnerLocalUpdate(ctx, receipt)
	}
	if err != nil {
		if isRetryableRemoteWorkerError(err) {
			return err
		}
		return worker.persistAndReportFleetRunnerFailure(ctx, receipt, err)
	}
	if update.State == "prepared" {
		update, _, err = worker.activateFleetRunnerLocalUpdate(ctx, receipt, update)
		if err != nil {
			return worker.persistAndReportFleetRunnerFailure(ctx, receipt, err)
		}
	}
	if update.State == "activating" {
		if receipt.State != "launched" {
			if receipt.State != "launching" {
				if err := worker.Store.UpdateFleetRunnerUpdateReceipt(
					ctx, receipt.ItemID, receipt.AssignmentGeneration, "launching", receipt.LocalUpdateID, "",
				); err != nil {
					return err
				}
				receipt.State = "launching"
			}
			launcher := worker.RunnerUpdater
			if launcher == nil {
				launcher = systemdRunnerUpdateLauncher{}
			}
			if err := launcher.Launch(context.WithoutCancel(ctx), *worker.Catalog.RunnerUpdate, update); err != nil {
				_ = worker.Store.FinishRunnerUpdate(context.WithoutCancel(ctx), update.ID,
					"needs_attention", "fleet_launch_failed", update.RollbackPath, err.Error(), update.FencingToken)
				return worker.persistAndReportFleetRunnerFailure(ctx, receipt, err)
			}
			persistCtx, cancel := fleetRunnerDetachedContext(ctx)
			defer cancel()
			return worker.Store.UpdateFleetRunnerUpdateReceipt(
				persistCtx, receipt.ItemID, receipt.AssignmentGeneration, "launched", receipt.LocalUpdateID, "",
			)
		}
		return nil
	}
	return worker.reportFleetRunnerLocalOutcome(ctx, receipt, update)
}

func (worker *RemoteWorker) validateFleetRunnerAssignment(
	assignment model.FleetRunnerUpdateAssignment,
) error {
	policy := worker.Catalog.RunnerUpdate
	if assignment.RunnerID != worker.RunnerID || policy.RunnerID != worker.RunnerID {
		return errors.New("Runner Fleet 更新 assignment 与本地 Runner 身份不一致")
	}
	if !uuidPattern.MatchString(assignment.PlanID) || !uuidPattern.MatchString(assignment.ItemID) ||
		(assignment.Action != "update" && assignment.Action != "rollback") ||
		assignment.Fence.Generation == 0 || assignment.Fence.ClaimToken == "" ||
		!runnerDigestPattern.MatchString(assignment.PlanDigest) {
		return errors.New("Runner Fleet 更新 assignment 合同不完整")
	}
	if !fleetRunnerIDPattern.MatchString(assignment.ServerID) ||
		!runnerVersionPattern.MatchString(assignment.PreviousVersion) ||
		!runnerRevisionPattern.MatchString(assignment.PreviousRevision) ||
		!runnerDigestPattern.MatchString(assignment.PreviousDigest) {
		return errors.New("Runner Fleet 更新 assignment 基线身份无效")
	}
	if assignment.ExecutionDeadlineAt.IsZero() || !worker.now().UTC().Before(assignment.ExecutionDeadlineAt) {
		return errors.New("Runner Fleet 更新 assignment 已过期")
	}
	if err := validateFleetRunnerUpdateManifest(policy, assignment.Manifest); err != nil {
		return err
	}
	if err := verifyFleetRunnerUpdateSignature(policy, assignment.Manifest, assignment.ArtifactSignature); err != nil {
		return err
	}
	if assignment.PolicyDigest != fleetRunnerUpdatePolicyDigest(policy, worker.Catalog.Fleet) {
		return errors.New("Runner Fleet 更新 assignment 策略摘要不匹配")
	}
	for _, node := range worker.Catalog.Fleet.Inventory.Runners {
		if node.ID == worker.RunnerID && node.ServerID == assignment.ServerID {
			return nil
		}
	}
	return errors.New("Runner Fleet 更新 assignment server 未在本地清单登记")
}

func (worker *RemoteWorker) heartbeatFleetRunnerUpdate(
	ctx context.Context,
	receipt model.FleetRunnerUpdateReceipt,
) error {
	status, err := worker.request(ctx, http.MethodPost,
		"fleet-updates/"+receipt.ItemID+"/heartbeat",
		model.FleetRunnerUpdateHeartbeatRequest{FleetRunnerUpdateFence: receipt.Fence}, nil)
	if err == nil {
		return nil
	}
	if status == http.StatusNotFound || status == http.StatusPreconditionFailed {
		_ = worker.Store.UpdateFleetRunnerUpdateReceipt(ctx, receipt.ItemID,
			receipt.AssignmentGeneration, "reported", receipt.LocalUpdateID, err.Error())
		return errFleetRunnerAssignmentTerminal
	}
	return err
}

func (worker *RemoteWorker) prepareFleetRunnerLocalUpdate(
	ctx context.Context,
	receipt model.FleetRunnerUpdateReceipt,
) (model.RunnerUpdate, error) {
	assignment := receipt.Assignment
	version, revision, digest, err := worker.currentRunnerIdentity()
	if err != nil {
		return model.RunnerUpdate{}, err
	}
	wantVersion, wantRevision, wantDigest := assignment.PreviousVersion,
		assignment.PreviousRevision, assignment.PreviousDigest
	if assignment.Action == "rollback" {
		wantVersion, wantRevision, wantDigest = assignment.Manifest.TargetVersion,
			assignment.Manifest.ArtifactRevision, assignment.Manifest.ArtifactDigest
	}
	if version != wantVersion || revision != wantRevision || digest != wantDigest {
		return model.RunnerUpdate{}, errors.New("Runner Fleet 更新前本地运行身份与 assignment 不一致")
	}
	stagedPath, err := worker.stageFleetRunnerAssignment(ctx, receipt)
	if err != nil {
		return model.RunnerUpdate{}, err
	}
	targetVersion, targetRevision, targetDigest := assignment.Manifest.TargetVersion,
		assignment.Manifest.ArtifactRevision, assignment.Manifest.ArtifactDigest
	if assignment.Action == "rollback" {
		targetVersion, targetRevision, targetDigest = assignment.PreviousVersion,
			assignment.PreviousRevision, assignment.PreviousDigest
	}
	payload, _ := json.Marshal(assignment)
	confirmation := "执行 Runner Fleet assignment " + assignment.ItemID
	update := model.RunnerUpdate{
		ID:             receipt.LocalUpdateID,
		IdempotencyKey: deterministicFleetRunnerUUID("reserve", assignment),
		RequestDigest:  digestText(string(payload)), RunnerID: worker.RunnerID,
		TargetVersion: targetVersion, ArtifactPath: "fleet/" + assignment.ItemID,
		ArtifactDigest: targetDigest, ArtifactRevision: targetRevision,
		Publisher: assignment.Manifest.Publisher, ArtifactSignature: assignment.ArtifactSignature,
		ManifestPurpose: assignment.Manifest.Purpose, ManifestSchema: assignment.Manifest.Schema,
		ManifestGOOS: assignment.Manifest.GOOS, ManifestGOARCH: assignment.Manifest.GOARCH,
		StagedPath: stagedPath, BinaryPath: worker.Catalog.RunnerUpdate.BinaryPath,
		UnitName:             worker.Catalog.RunnerUpdate.UnitName,
		HealthTimeoutSeconds: worker.Catalog.RunnerUpdate.HealthTimeoutSeconds,
		State:                "prepared", Phase: "prepared",
		PreviousVersion: version, PreviousRevision: revision, PreviousDigest: digest,
		ConfirmationPhrase: confirmation, CreatedAt: worker.now().UTC(),
	}
	prepared, _, err := worker.Store.ReserveRunnerUpdate(
		ctx, update, fleetRunnerSystemActor("prepare", assignment),
	)
	if err != nil {
		_ = os.Remove(stagedPath)
		return model.RunnerUpdate{}, err
	}
	return prepared, nil
}

func (worker *RemoteWorker) activateFleetRunnerLocalUpdate(
	ctx context.Context,
	receipt model.FleetRunnerUpdateReceipt,
	update model.RunnerUpdate,
) (model.RunnerUpdate, bool, error) {
	return worker.Store.BeginRunnerUpdateActivation(ctx, update.ID,
		fleetRunnerSystemActor("execute", receipt.Assignment),
		deterministicFleetRunnerUUID("activate", receipt.Assignment), update.ConfirmationPhrase)
}

func (worker *RemoteWorker) stageFleetRunnerAssignment(
	ctx context.Context,
	receipt model.FleetRunnerUpdateReceipt,
) (string, error) {
	assignment := receipt.Assignment
	expectedDigest := assignment.Manifest.ArtifactDigest
	var source string
	var cleanup func()
	if assignment.Action == "update" {
		downloaded, err := worker.downloadFleetRunnerArtifact(ctx, receipt)
		if err != nil {
			return "", err
		}
		source = downloaded
		cleanup = func() { _ = os.Remove(downloaded) }
	} else {
		previous, found, err := worker.Store.GetReportedFleetRunnerUpdateReceipt(
			ctx, assignment.PlanID, assignment.ItemID,
		)
		if err != nil || !found {
			return "", errors.New("Runner Fleet 回滚缺少原更新本地回执")
		}
		update, err := worker.Store.GetRunnerUpdate(ctx, previous.LocalUpdateID)
		if err != nil || update.State != "succeeded" || update.RollbackPath == "" {
			return "", errors.New("Runner Fleet 回滚缺少已验证的本地回滚副本")
		}
		source, expectedDigest = update.RollbackPath, assignment.PreviousDigest
		cleanup = func() {}
	}
	defer cleanup()
	expectedPath := filepath.Join(worker.StateRoot, "runner-updates", "staged", receipt.LocalUpdateID+".runner")
	if existingDigest, err := hashFile(expectedPath, worker.Catalog.RunnerUpdate.MaxArtifactBytes); err == nil {
		if existingDigest != expectedDigest {
			return "", errors.New("Runner Fleet 已有本地暂存制品摘要冲突")
		}
		return expectedPath, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	path, digest, err := stageRunnerArtifact(worker.StateRoot, receipt.LocalUpdateID, source,
		worker.Catalog.RunnerUpdate.MaxArtifactBytes)
	if err != nil || digest != expectedDigest {
		if err == nil {
			err = errors.New("Runner Fleet 本地暂存制品摘要不匹配")
		}
		return "", err
	}
	return path, nil
}

func (worker *RemoteWorker) downloadFleetRunnerArtifact(
	ctx context.Context,
	receipt model.FleetRunnerUpdateReceipt,
) (string, error) {
	policy := worker.Catalog.RunnerUpdate
	root := filepath.Join(worker.StateRoot, "runner-updates", "incoming")
	if filepath.Clean(policy.ArtifactRoot) != filepath.Clean(root) {
		return "", errors.New("Runner Fleet 下载目录与本地更新策略不一致")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", err
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return "", err
	}
	if err := requireRootOwnedDirectory(root); err != nil {
		return "", err
	}
	payload, err := json.Marshal(model.FleetRunnerUpdateArtifactRequest{
		FleetRunnerUpdateFence: receipt.Fence,
	})
	if err != nil {
		return "", err
	}
	url := strings.TrimRight(worker.Endpoint, "/") + "/v1/fleet/runners/" +
		worker.RunnerID + "/fleet-updates/" + receipt.ItemID + "/artifact"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(runnerIDHeader, worker.RunnerID)
	response, err := worker.Client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, workerResponseLimit))
		return "", newRemoteWorkerHTTPError("Runner Fleet 制品下载", response)
	}
	if header := response.Header.Get("X-AreaSong-Artifact-Digest"); header != receipt.Assignment.Manifest.ArtifactDigest {
		return "", errors.New("Runner Fleet 制品响应摘要头不匹配")
	}
	if response.ContentLength < 1 || response.ContentLength > policy.MaxArtifactBytes {
		return "", errors.New("Runner Fleet 制品响应大小无效")
	}
	temporary, err := os.CreateTemp(root, ".fleet-runner-*")
	if err != nil {
		return "", err
	}
	path := temporary.Name()
	remove := true
	defer func() {
		_ = temporary.Close()
		if remove {
			_ = os.Remove(path)
		}
	}()
	if err := temporary.Chmod(0o700); err != nil {
		return "", err
	}
	hasher := sha256.New()
	written, err := io.Copy(io.MultiWriter(temporary, hasher),
		io.LimitReader(response.Body, policy.MaxArtifactBytes+1))
	if err != nil {
		return "", fmt.Errorf("Runner Fleet 制品传输中断: %w", err)
	}
	if written != response.ContentLength || written > policy.MaxArtifactBytes {
		return "", errors.New("Runner Fleet 制品下载长度无效")
	}
	if "sha256:"+hex.EncodeToString(hasher.Sum(nil)) != receipt.Assignment.Manifest.ArtifactDigest {
		return "", errors.New("Runner Fleet 下载制品摘要校验失败")
	}
	if err := temporary.Sync(); err != nil {
		return "", err
	}
	if err := temporary.Close(); err != nil {
		return "", err
	}
	remove = false
	return path, nil
}

func (worker *RemoteWorker) reportFleetRunnerLocalOutcome(
	ctx context.Context,
	receipt model.FleetRunnerUpdateReceipt,
	update model.RunnerUpdate,
) error {
	version, revision, digest, identityErr := worker.currentRunnerIdentity()
	completion := model.FleetRunnerUpdateCompletionRequest{
		FleetRunnerUpdateFence: receipt.Fence,
		IdempotencyKey:         fleetRunnerCompletionKey(receipt.Assignment),
		ObservedVersion:        version, ObservedRevision: revision, ObservedDigest: digest,
	}
	assignment := receipt.Assignment
	switch {
	case update.State == "succeeded" && assignment.Action == "update":
		completion.State = "succeeded"
	case update.State == "succeeded" && assignment.Action == "rollback":
		completion.State = "rolled_back"
	case (update.State == "failed" || update.State == "rolled_back") && assignment.Action == "update":
		completion.State = "rolled_back"
		completion.Error = nonEmpty(update.Error, "Runner 更新未激活目标制品，节点保持原版本")
	default:
		completion.State = "needs_attention"
		completion.Error = nonEmpty(update.Error, "Runner Fleet 本地更新需要人工核对")
	}
	if identityErr != nil || !worker.completionIdentityMatches(completion, assignment) {
		completion.State = "needs_attention"
		completion.ObservedVersion, completion.ObservedRevision, completion.ObservedDigest = "", "", ""
		completion.Error = nonEmpty(errorText(identityErr), "Runner Fleet 本地运行身份复验失败")
	}
	return worker.reportFleetRunnerCompletion(ctx, receipt, completion)
}

func (worker *RemoteWorker) completionIdentityMatches(
	completion model.FleetRunnerUpdateCompletionRequest,
	assignment model.FleetRunnerUpdateAssignment,
) bool {
	wantVersion, wantRevision, wantDigest := assignment.Manifest.TargetVersion,
		assignment.Manifest.ArtifactRevision, assignment.Manifest.ArtifactDigest
	if completion.State == "rolled_back" {
		wantVersion, wantRevision, wantDigest = assignment.PreviousVersion,
			assignment.PreviousRevision, assignment.PreviousDigest
	}
	if completion.State == "needs_attention" {
		return true
	}
	return completion.ObservedVersion == wantVersion && completion.ObservedRevision == wantRevision &&
		completion.ObservedDigest == wantDigest
}

func (worker *RemoteWorker) persistAndReportFleetRunnerFailure(
	ctx context.Context,
	receipt model.FleetRunnerUpdateReceipt,
	cause error,
) error {
	message := redactText(cause.Error())
	if len(message) > 4096 {
		message = message[:4096]
	}
	persistCtx, cancel := fleetRunnerDetachedContext(ctx)
	defer cancel()
	if err := worker.Store.UpdateFleetRunnerUpdateReceipt(persistCtx, receipt.ItemID,
		receipt.AssignmentGeneration, "needs_attention", receipt.LocalUpdateID, message); err != nil {
		return errors.Join(cause, err)
	}
	receipt.State, receipt.LastError = "needs_attention", message
	return worker.reportFleetRunnerNeedsAttention(persistCtx, receipt, message)
}

func (worker *RemoteWorker) reportFleetRunnerNeedsAttention(
	ctx context.Context,
	receipt model.FleetRunnerUpdateReceipt,
	message string,
) error {
	completion := model.FleetRunnerUpdateCompletionRequest{
		FleetRunnerUpdateFence: receipt.Fence,
		IdempotencyKey:         fleetRunnerCompletionKey(receipt.Assignment),
		State:                  "needs_attention", Error: nonEmpty(message, "Runner Fleet 更新需要人工核对"),
	}
	return worker.reportFleetRunnerCompletion(ctx, receipt, completion)
}

func (worker *RemoteWorker) reportFleetRunnerCompletion(
	ctx context.Context,
	receipt model.FleetRunnerUpdateReceipt,
	completion model.FleetRunnerUpdateCompletionRequest,
) error {
	reportCtx, cancel := fleetRunnerDetachedContext(ctx)
	defer cancel()
	status, err := worker.request(reportCtx, http.MethodPost,
		"fleet-updates/"+receipt.ItemID+"/complete", completion, nil)
	if err != nil && status != http.StatusNotFound && status != http.StatusPreconditionFailed {
		return err
	}
	lastError := ""
	if err != nil {
		lastError = err.Error()
	}
	return worker.Store.UpdateFleetRunnerUpdateReceipt(reportCtx, receipt.ItemID,
		receipt.AssignmentGeneration, "reported", receipt.LocalUpdateID, lastError)
}

func fleetRunnerDetachedContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(parent), 15*time.Second)
}

func (worker *RemoteWorker) rejectFleetRunnerAssignment(
	ctx context.Context,
	assignment model.FleetRunnerUpdateAssignment,
	cause error,
) error {
	if assignment.ItemID == "" || assignment.Fence.Generation == 0 || assignment.Fence.ClaimToken == "" {
		return cause
	}
	completion := model.FleetRunnerUpdateCompletionRequest{
		FleetRunnerUpdateFence: assignment.Fence,
		IdempotencyKey:         fleetRunnerCompletionKey(assignment),
		State:                  "needs_attention", Error: redactText(cause.Error()),
	}
	status, reportErr := worker.request(ctx, http.MethodPost,
		"fleet-updates/"+assignment.ItemID+"/complete", completion, nil)
	if reportErr == nil || status == http.StatusNotFound || status == http.StatusPreconditionFailed {
		return nil
	}
	return errors.Join(cause, reportErr)
}

func (worker *RemoteWorker) currentRunnerIdentity() (string, string, string, error) {
	if worker.Identity != nil {
		return worker.Identity()
	}
	policy := worker.Catalog.RunnerUpdate
	digest, err := hashFile(policy.BinaryPath, policy.MaxArtifactBytes)
	if err != nil {
		return "", "", "", err
	}
	return buildinfo.Version, buildinfo.Revision, digest, nil
}

func deterministicFleetRunnerUUID(namespace string, assignment model.FleetRunnerUpdateAssignment) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%d", namespace,
		assignment.ItemID, assignment.Fence.Generation)))
	value := sum[:16]
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(value)
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" +
		encoded[16:20] + "-" + encoded[20:]
}

func fleetRunnerSystemActor(namespace string, assignment model.FleetRunnerUpdateAssignment) string {
	sum := sha256.Sum256([]byte(namespace + "\x00" + assignment.PlanID + "\x00" + assignment.ItemID))
	return hex.EncodeToString(sum[:])
}

func fleetRunnerCompletionKey(assignment model.FleetRunnerUpdateAssignment) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("fleet-complete\x00%s\x00%d\x00%s",
		assignment.ItemID, assignment.Fence.Generation, assignment.Action)))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func nonEmpty(value, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return redactText(err.Error())
}
