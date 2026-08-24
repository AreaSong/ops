package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/buildinfo"
	"github.com/AreaSong/ops/services/areasong-ops/internal/config"
	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	"github.com/AreaSong/ops/services/areasong-ops/internal/store"
)

var (
	runnerVersionPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+:-]{0,63}$`)
	runnerRevisionPattern = regexp.MustCompile(`^[a-f0-9]{40}$`)
	runnerDigestPattern   = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
)

func (engine *Engine) RunnerUpdateStatus(
	ctx context.Context,
	actor string,
) (model.RunnerUpdateStatus, error) {
	policy := engine.catalog.RunnerUpdate
	if policy == nil || !policy.Enabled {
		return model.RunnerUpdateStatus{}, errors.New("Runner 自更新尚未启用")
	}
	if err := engine.authorizePlatform(ctx, actor, model.PermissionRead, "runner:"+policy.RunnerID); err != nil {
		return model.RunnerUpdateStatus{}, err
	}
	pending, err := engine.store.ListPendingRunnerUpdates(ctx, policy.RunnerID)
	if err != nil {
		return model.RunnerUpdateStatus{}, err
	}
	recent, err := engine.store.ListRunnerUpdates(ctx, policy.RunnerID, 20)
	if err != nil {
		return model.RunnerUpdateStatus{}, err
	}
	canManage := engine.authorizePlatform(
		ctx, actor, model.PermissionRunnerUpdate, "runner:"+policy.RunnerID,
	) == nil
	manifest := canonicalRunnerUpdateManifest(policy, model.RunnerUpdateRequest{})
	return model.RunnerUpdateStatus{
		RunnerID: policy.RunnerID, CurrentVersion: buildinfo.Version,
		Revision: buildinfo.Revision, Publisher: policy.Publisher,
		ManifestPurpose: manifest.Purpose, ManifestSchema: manifest.Schema,
		ManifestGOOS: manifest.GOOS, ManifestGOARCH: manifest.GOARCH,
		CurrentActorHash: actor, CanManage: canManage, Pending: pending, Recent: recent,
	}, nil
}

func (engine *Engine) PrepareRunnerUpdate(
	ctx context.Context,
	actor string,
	request model.RunnerUpdateRequest,
) (model.RunnerUpdate, bool, error) {
	policy := engine.catalog.RunnerUpdate
	if policy == nil || !policy.Enabled {
		return model.RunnerUpdate{}, false, errors.New("Runner 自更新尚未启用")
	}
	if err := validateRunnerUpdateRequest(request, policy); err != nil {
		return model.RunnerUpdate{}, false, err
	}
	manifestPayload, err := runnerUpdateManifestPayload(policy, request)
	if err != nil {
		return model.RunnerUpdate{}, false, fmt.Errorf("Runner 更新 manifest 序列化失败: %w", err)
	}
	if err := engine.authorizePlatform(ctx, actor, model.PermissionRunnerUpdate, "runner:"+policy.RunnerID); err != nil {
		return model.RunnerUpdate{}, false, err
	}
	requestDigest := digestText(strings.Join([]string{
		actor, policy.RunnerID, request.TargetVersion, request.ArtifactPath,
		request.ArtifactDigest, request.ArtifactRevision, request.Publisher,
		request.ArtifactSignature, request.Confirmation, string(manifestPayload),
	}, "\x00"))
	if existing, preparedBy, found, err := engine.store.GetRunnerUpdateByIdempotency(
		ctx, request.IdempotencyKey,
	); err != nil {
		return model.RunnerUpdate{}, false, err
	} else if found {
		if existing.RequestDigest != requestDigest || preparedBy != actor {
			return model.RunnerUpdate{}, false, store.ErrIdempotency
		}
		return existing, false, nil
	}
	if err := verifyRunnerArtifactSignature(policy, request); err != nil {
		return model.RunnerUpdate{}, false, err
	}
	sourcePath, digest, err := engine.verifyRunnerArtifact(
		policy, request.ArtifactPath, request.ArtifactDigest,
	)
	if err != nil {
		return model.RunnerUpdate{}, false, err
	}
	id, err := newUUID()
	if err != nil {
		return model.RunnerUpdate{}, false, err
	}
	stagedPath, stagedDigest, err := stageRunnerArtifact(
		engine.stateRoot, id, sourcePath, policy.MaxArtifactBytes,
	)
	if err != nil {
		return model.RunnerUpdate{}, false, fmt.Errorf("暂存 Runner 制品失败: %w", err)
	}
	if stagedDigest != digest {
		_ = os.Remove(stagedPath)
		return model.RunnerUpdate{}, false, errors.New("Runner 暂存制品摘要校验失败")
	}
	previousDigest, err := hashFile(policy.BinaryPath, policy.MaxArtifactBytes)
	if err != nil {
		_ = os.Remove(stagedPath)
		return model.RunnerUpdate{}, false, fmt.Errorf("读取当前 Runner 二进制身份失败: %w", err)
	}
	confirmationPhrase := "激活 Runner 更新到 " + request.TargetVersion + " revision " + request.ArtifactRevision
	update := model.RunnerUpdate{
		ID: id, IdempotencyKey: request.IdempotencyKey, RequestDigest: requestDigest,
		RunnerID:      policy.RunnerID,
		TargetVersion: request.TargetVersion, ArtifactPath: request.ArtifactPath,
		ArtifactDigest: digest, ArtifactRevision: request.ArtifactRevision,
		Publisher: request.Publisher, ArtifactSignature: request.ArtifactSignature,
		ManifestPurpose: request.Manifest.Purpose, ManifestSchema: request.Manifest.Schema,
		ManifestGOOS: request.Manifest.GOOS, ManifestGOARCH: request.Manifest.GOARCH,
		StagedPath: stagedPath, BinaryPath: policy.BinaryPath, UnitName: policy.UnitName,
		HealthTimeoutSeconds: policy.HealthTimeoutSeconds,
		State:                "prepared", Phase: "prepared", PreparedByHash: actor,
		PreviousVersion: buildinfo.Version, PreviousRevision: buildinfo.Revision,
		PreviousDigest: previousDigest, ConfirmationPhrase: confirmationPhrase,
		CreatedAt: time.Now().UTC(),
	}
	result, created, err := engine.store.ReserveRunnerUpdate(ctx, update, actor)
	if err != nil || !created {
		_ = os.Remove(stagedPath)
	}
	if err == nil && created {
		_, _ = engine.store.AppendAudit(ctx, model.AuditEntry{
			ActorHash: actor, Event: "runner.update.prepared", Resource: "runner:" + policy.RunnerID,
			Outcome: "accepted", Detail: map[string]any{
				"updateId": result.ID, "targetVersion": result.TargetVersion,
				"artifactDigest":   result.ArtifactDigest,
				"artifactRevision": result.ArtifactRevision,
			},
		})
	}
	return result, created, err
}

func (engine *Engine) ActivateRunnerUpdate(
	ctx context.Context,
	actor, id string,
	request model.RunnerUpdateActivationRequest,
) (model.RunnerUpdate, bool, error) {
	policy := engine.catalog.RunnerUpdate
	if policy == nil || !policy.Enabled {
		return model.RunnerUpdate{}, false, errors.New("Runner 自更新尚未启用")
	}
	if !uuidPattern.MatchString(id) || !uuidPattern.MatchString(request.IdempotencyKey) {
		return model.RunnerUpdate{}, false, errors.New("Runner 激活请求标识无效")
	}
	if err := engine.authorizePlatform(
		ctx, actor, model.PermissionRunnerUpdate, "runner:"+policy.RunnerID,
	); err != nil {
		return model.RunnerUpdate{}, false, err
	}
	update, created, err := engine.store.BeginRunnerUpdateActivation(
		ctx, id, actor, request.IdempotencyKey, request.Confirmation,
	)
	if err != nil || !created {
		return update, created, err
	}
	launcher := engine.runnerUpdater
	if launcher == nil {
		launcher = systemdRunnerUpdateLauncher{}
	}
	if err := launcher.Launch(context.WithoutCancel(ctx), *policy, update); err != nil {
		finishErr := engine.store.FinishRunnerUpdate(
			context.WithoutCancel(ctx), id, "needs_attention", "launch_failed", "", err.Error(), update.FencingToken,
		)
		update.State, update.Phase, update.Error = "needs_attention", "launch_failed", err.Error()
		if finishErr != nil {
			return update, true, fmt.Errorf("%v; Runner 更新状态收口失败: %w", err, finishErr)
		}
		return update, true, err
	}
	_, _ = engine.store.AppendAudit(context.WithoutCancel(ctx), model.AuditEntry{
		ActorHash: actor, Event: "runner.update.activation_requested",
		Resource: "runner:" + policy.RunnerID, Outcome: "accepted",
		Detail: map[string]any{"updateId": id, "artifactDigest": update.ArtifactDigest},
	})
	return update, true, nil
}

func (engine *Engine) ResolveRunnerUpdate(
	ctx context.Context,
	actor, id string,
	request model.RunnerUpdateResolutionRequest,
) (model.RunnerUpdate, bool, error) {
	policy := engine.catalog.RunnerUpdate
	if policy == nil || !policy.Enabled {
		return model.RunnerUpdate{}, false, errors.New("Runner 自更新尚未启用")
	}
	if !uuidPattern.MatchString(id) || !uuidPattern.MatchString(request.IdempotencyKey) {
		return model.RunnerUpdate{}, false, errors.New("Runner 收口请求标识无效")
	}
	if request.Confirmation != "确认 Runner 更新已人工核对" {
		return model.RunnerUpdate{}, false, errors.New("Runner 收口确认短语不匹配")
	}
	// Schema 4 makes the operator observation mandatory. Schema 3 existed
	// before the structured evidence contract and remains readable so an
	// already-deployed legacy control plane can close an old interrupted update;
	// it cannot be used to create a new schema-4 update.
	legacy := engine.catalog.SchemaVersion < 4 && isEmptyResolutionEvidence(request.Evidence)
	if engine.catalog.SchemaVersion >= 4 && isEmptyResolutionEvidence(request.Evidence) {
		return model.RunnerUpdate{}, false, errors.New("Runner 人工收口必须提供现场核对证据")
	}
	if err := engine.authorizePlatform(
		ctx, actor, model.PermissionRunnerUpdate, "runner:"+policy.RunnerID,
	); err != nil {
		return model.RunnerUpdate{}, false, err
	}
	var update model.RunnerUpdate
	var created bool
	var err error
	if legacy {
		update, created, err = engine.store.ResolveRunnerUpdate(ctx, id, actor, request.IdempotencyKey)
	} else {
		update, created, err = engine.store.ResolveRunnerUpdate(ctx, id, actor, request.IdempotencyKey, request.Evidence)
	}
	if err == nil && created {
		_, _ = engine.store.AppendAudit(context.WithoutCancel(ctx), model.AuditEntry{
			ActorHash: actor, Event: "runner.update.manually_resolved",
			Resource: "runner:" + policy.RunnerID, Outcome: "accepted",
			Detail: map[string]any{"updateId": id},
		})
	}
	return update, created, err
}

func isEmptyResolutionEvidence(evidence model.RunnerUpdateResolutionEvidence) bool {
	return evidence.Decision == "" && evidence.ObservedVersion == "" &&
		evidence.ObservedRevision == "" && evidence.ObservedDigest == "" &&
		evidence.ObservedPID == 0 && evidence.Reason == ""
}

func (engine *Engine) CancelRunnerUpdate(
	ctx context.Context,
	actor, id string,
	request model.RunnerUpdateCancellationRequest,
) (model.RunnerUpdate, bool, error) {
	policy := engine.catalog.RunnerUpdate
	if policy == nil || !policy.Enabled {
		return model.RunnerUpdate{}, false, errors.New("Runner 自更新尚未启用")
	}
	if !uuidPattern.MatchString(id) || !uuidPattern.MatchString(request.IdempotencyKey) {
		return model.RunnerUpdate{}, false, errors.New("Runner 取消请求标识无效")
	}
	if request.Confirmation != "取消 Runner 更新 "+id {
		return model.RunnerUpdate{}, false, errors.New("Runner 取消确认短语不匹配")
	}
	if err := engine.authorizePlatform(
		ctx, actor, model.PermissionRunnerUpdate, "runner:"+policy.RunnerID,
	); err != nil {
		return model.RunnerUpdate{}, false, err
	}
	update, created, err := engine.store.CancelRunnerUpdate(
		ctx, id, actor, request.IdempotencyKey,
	)
	if err != nil {
		return update, created, err
	}
	expectedStagedPath := filepath.Join(engine.stateRoot, "runner-updates", "staged", id+".runner")
	if filepath.Clean(update.StagedPath) != filepath.Clean(expectedStagedPath) {
		return update, created, errors.New("Runner 暂存制品路径与更新身份不一致")
	}
	if removeErr := os.Remove(expectedStagedPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
		return update, created, fmt.Errorf("删除已取消 Runner 暂存制品失败: %w", removeErr)
	}
	if created {
		_, _ = engine.store.AppendAudit(context.WithoutCancel(ctx), model.AuditEntry{
			ActorHash: actor, Event: "runner.update.cancelled",
			Resource: "runner:" + policy.RunnerID, Outcome: "accepted",
			Detail: map[string]any{"updateId": id, "artifactDigest": update.ArtifactDigest},
		})
	}
	return update, created, nil
}

func validateRunnerUpdateRequest(
	request model.RunnerUpdateRequest,
	policy *config.RunnerUpdatePolicy,
) error {
	if !runnerVersionPattern.MatchString(request.TargetVersion) {
		return errors.New("Runner 目标版本格式无效")
	}
	if !runnerRevisionPattern.MatchString(request.ArtifactRevision) {
		return errors.New("Runner 制品 revision 必须是完整 Git commit")
	}
	if !uuidPattern.MatchString(request.IdempotencyKey) {
		return errors.New("Runner 更新幂等键无效")
	}
	if request.Confirmation != "准备 Runner 更新到 "+request.TargetVersion {
		return errors.New("Runner 更新需要精确确认短语")
	}
	if request.ArtifactPath == "" || filepath.IsAbs(request.ArtifactPath) {
		return errors.New("Runner 制品路径必须是 artifactRoot 下的相对路径")
	}
	if !runnerDigestPattern.MatchString(request.ArtifactDigest) {
		return errors.New("Runner 制品摘要格式无效")
	}
	if policy.ArtifactRoot == "" || policy.BinaryPath == "" || policy.UnitName == "" {
		return errors.New("Runner 更新策略路径配置不完整")
	}
	if request.Publisher != policy.Publisher {
		return errors.New("Runner 制品发布者与策略不一致")
	}
	if strings.TrimSpace(request.ArtifactSignature) == "" {
		return errors.New("Runner 制品缺少签名")
	}
	return nil
}

func (engine *Engine) verifyRunnerArtifact(
	policy *config.RunnerUpdatePolicy,
	relativePath, expectedDigest string,
) (string, string, error) {
	cleanPath, err := cleanRelativePath(relativePath)
	if err != nil || cleanPath == "" {
		return "", "", errors.New("Runner 制品路径无效")
	}
	root := filepath.Clean(policy.ArtifactRoot)
	target := filepath.Join(root, filepath.FromSlash(cleanPath))
	if err := rejectManagedSymlinks(root, target); err != nil {
		return "", "", err
	}
	info, err := os.Lstat(target)
	if err != nil || !info.Mode().IsRegular() {
		return "", "", errors.New("Runner 制品必须是普通文件")
	}
	if info.Size() > policy.MaxArtifactBytes {
		return "", "", errors.New("Runner 制品超过大小限制")
	}
	digest, err := hashFile(target, policy.MaxArtifactBytes)
	if err != nil {
		return "", "", err
	}
	if digest != expectedDigest {
		return "", "", errors.New("Runner 制品摘要校验失败")
	}
	return target, digest, nil
}

func hashFile(path string, limit int64) (string, error) {
	pathInfo, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !pathInfo.Mode().IsRegular() || pathInfo.Mode()&os.ModeSymlink != 0 || pathInfo.Size() > limit {
		return "", errors.New("文件身份或大小无效")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil || !os.SameFile(pathInfo, openedInfo) {
		return "", errors.New("文件读取期间身份发生变化")
	}
	hasher := sha256.New()
	count, err := io.CopyN(hasher, file, limit+1)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	if count > limit {
		return "", errors.New("文件超过大小限制")
	}
	return "sha256:" + hex.EncodeToString(hasher.Sum(nil)), nil
}
