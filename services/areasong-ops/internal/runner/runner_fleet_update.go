package runner

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/config"
	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	"github.com/AreaSong/ops/services/areasong-ops/internal/store"
)

const (
	maxFleetRunnerUpdateTargets = 100
	maxFleetUpdateWindow        = 4 * time.Hour
	maxFleetUpdateSchedule      = 7 * 24 * time.Hour
	fleetRunnerUpdateCapability = "runner-update"
)

var fleetRunnerIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{1,39}$`)

type fleetRunnerUpdateTargetSnapshot struct {
	RunnerID               string `json:"runnerId"`
	ServerID               string `json:"serverId"`
	Version                string `json:"version"`
	Revision               string `json:"revision"`
	BinaryDigest           string `json:"binaryDigest"`
	LeaseGeneration        uint64 `json:"leaseGeneration"`
	CertificateFingerprint string `json:"certificateFingerprint,omitempty"`
}

type fleetRunnerUpdatePlanDigestPayload struct {
	RequestDigest     string                            `json:"requestDigest"`
	PolicyDigest      string                            `json:"policyDigest"`
	Manifest          model.FleetRunnerUpdateManifest   `json:"manifest"`
	Targets           []fleetRunnerUpdateTargetSnapshot `json:"targets"`
	BatchPolicy       model.BatchPolicy                 `json:"batchPolicy"`
	MaxConcurrent     int                               `json:"maxConcurrent"`
	ChangeWindow      model.ChangeWindow                `json:"changeWindow"`
	RollbackOnFailure bool                              `json:"rollbackOnFailure"`
}

func (engine *Engine) FleetRunnerUpdateStatus(
	ctx context.Context,
	actor string,
) (model.FleetRunnerUpdateStatus, error) {
	if err := engine.expireFleetRunnerUpdatePlans(ctx, time.Now().UTC()); err != nil {
		return model.FleetRunnerUpdateStatus{}, err
	}
	policy, err := engine.fleetRunnerUpdatePolicy()
	if err != nil {
		return model.FleetRunnerUpdateStatus{}, err
	}
	tenantID, err := engine.actorTenantID(ctx, actor)
	if err != nil {
		return model.FleetRunnerUpdateStatus{}, err
	}
	fleet, err := engine.store.ListFleet(ctx)
	if err != nil {
		return model.FleetRunnerUpdateStatus{}, err
	}
	runners := make([]model.RunnerNode, 0, len(fleet.Runners))
	canManage := false
	for _, node := range fleet.Runners {
		if node.TenantID != tenantID || engine.authorizePlatform(ctx, actor, model.PermissionRead, "runner:"+node.ID) != nil {
			continue
		}
		runners = append(runners, node)
		if engine.authorizePlatform(ctx, actor, model.PermissionRunnerUpdate, "runner:"+node.ID) == nil {
			canManage = true
		}
	}
	plans, err := engine.store.ListFleetRunnerUpdatePlans(ctx, tenantID, 50)
	if err != nil {
		return model.FleetRunnerUpdateStatus{}, err
	}
	return model.FleetRunnerUpdateStatus{
		Available: true, CanManage: canManage, CurrentActorHash: actor,
		Publisher: policy.Publisher, ManifestPurpose: model.FleetRunnerUpdateManifestPurpose,
		ManifestSchema: model.FleetRunnerUpdateManifestSchema,
		ManifestGOOS:   policy.ManifestGOOS, ManifestGOARCH: policy.ManifestGOARCH,
		Runners: runners, Plans: plans,
	}, nil
}

func (engine *Engine) FleetRunnerUpdatePlan(
	ctx context.Context,
	actor, id string,
) (model.FleetRunnerUpdatePlan, error) {
	plan, err := engine.store.GetFleetRunnerUpdatePlan(ctx, id)
	if err != nil {
		return model.FleetRunnerUpdatePlan{}, err
	}
	if err := engine.authorizeFleetRunnerUpdatePlan(ctx, actor, plan, model.PermissionRead); err != nil {
		return model.FleetRunnerUpdatePlan{}, err
	}
	return plan, nil
}

func (engine *Engine) CreateFleetRunnerUpdatePlan(
	ctx context.Context,
	actor string,
	request model.FleetRunnerUpdatePlanRequest,
) (model.FleetRunnerUpdatePlan, bool, error) {
	if err := engine.expireFleetRunnerUpdatePlans(ctx, time.Now().UTC()); err != nil {
		return model.FleetRunnerUpdatePlan{}, false, err
	}
	policy, err := engine.fleetRunnerUpdatePolicy()
	if err != nil {
		return model.FleetRunnerUpdatePlan{}, false, err
	}
	if err := validateFleetRunnerUpdateRequest(request); err != nil {
		return model.FleetRunnerUpdatePlan{}, false, err
	}
	tenantID, err := engine.actorTenantID(ctx, actor)
	if err != nil {
		return model.FleetRunnerUpdatePlan{}, false, err
	}
	requestDigest, err := fleetRunnerUpdateRequestDigest(actor, request)
	if err != nil {
		return model.FleetRunnerUpdatePlan{}, false, err
	}
	if existing, found, err := engine.store.GetFleetRunnerUpdatePlanByIdempotency(ctx, request.IdempotencyKey); err != nil {
		return model.FleetRunnerUpdatePlan{}, false, err
	} else if found {
		if existing.ActorHash != actor || existing.RequestDigest != requestDigest {
			return model.FleetRunnerUpdatePlan{}, false, store.ErrIdempotency
		}
		return existing, false, nil
	}
	if err := validateFleetRunnerUpdateManifest(policy, request.Manifest); err != nil {
		return model.FleetRunnerUpdatePlan{}, false, err
	}
	if err := verifyFleetRunnerUpdateSignature(policy, request.Manifest, request.ArtifactSignature); err != nil {
		return model.FleetRunnerUpdatePlan{}, false, err
	}
	source, digest, err := engine.verifyRunnerArtifact(policy, request.ArtifactPath, request.Manifest.ArtifactDigest)
	if err != nil {
		return model.FleetRunnerUpdatePlan{}, false, err
	}
	nodes, err := engine.validateFleetRunnerUpdateTargets(ctx, actor, tenantID, request.TargetRunnerIDs)
	if err != nil {
		return model.FleetRunnerUpdatePlan{}, false, err
	}
	batches, err := request.BatchPolicy.Partition(request.TargetRunnerIDs)
	if err != nil {
		return model.FleetRunnerUpdatePlan{}, false, err
	}
	id, err := newUUID()
	if err != nil {
		return model.FleetRunnerUpdatePlan{}, false, err
	}
	stagedPath, stagedDigest, err := stageRunnerArtifact(engine.stateRoot, id, source, policy.MaxArtifactBytes)
	if err != nil {
		return model.FleetRunnerUpdatePlan{}, false, fmt.Errorf("暂存 Runner Fleet 制品失败: %w", err)
	}
	if stagedDigest != digest {
		_ = os.Remove(stagedPath)
		return model.FleetRunnerUpdatePlan{}, false, errors.New("Runner Fleet 暂存制品摘要校验失败")
	}
	policyDigest := fleetRunnerUpdatePolicyDigest(policy, engine.catalog.Fleet)
	targets := fleetRunnerUpdateTargetSnapshots(nodes)
	planDigest, err := fleetRunnerUpdatePlanDigest(requestDigest, policyDigest, request, targets)
	if err != nil {
		_ = os.Remove(stagedPath)
		return model.FleetRunnerUpdatePlan{}, false, err
	}
	now := time.Now().UTC()
	phrase := fleetRunnerUpdateConfirmation(request.Manifest.TargetVersion, len(nodes))
	items := make([]model.FleetRunnerUpdateItem, 0, len(nodes))
	for _, node := range nodes {
		itemID, idErr := newUUID()
		if idErr != nil {
			_ = os.Remove(stagedPath)
			return model.FleetRunnerUpdatePlan{}, false, idErr
		}
		items = append(items, model.FleetRunnerUpdateItem{
			ID: itemID, PlanID: id, RunnerID: node.ID, ServerID: node.ServerID,
			BatchIndex: batchIndexOf(batches, node.ID), State: model.FleetRunnerUpdateItemPending,
			PreviousVersion: node.Version, PreviousRevision: node.Revision,
			PreviousDigest: node.BinaryDigest, ExpectedLeaseGeneration: node.LeaseGeneration,
			CertificateFingerprint: node.CertificateFingerprint, UpdatedAt: now,
		})
	}
	window := request.ChangeWindow
	plan := model.FleetRunnerUpdatePlan{
		ID: id, IdempotencyKey: request.IdempotencyKey, RequestDigest: requestDigest,
		PlanDigest: planDigest, PolicyDigest: policyDigest, ActorHash: actor, TenantID: tenantID,
		Manifest: request.Manifest, ArtifactPath: request.ArtifactPath,
		ArtifactSignature: request.ArtifactSignature, StagedPath: stagedPath,
		TargetRunnerIDs: append([]string(nil), request.TargetRunnerIDs...),
		BatchPolicy:     request.BatchPolicy, MaxConcurrent: request.MaxConcurrent,
		ChangeWindow: &window, RollbackOnFailure: true,
		State: model.FleetRunnerUpdatePendingApproval, CurrentBatch: -1,
		ConfirmationPhrase: phrase, Items: items, CreatedAt: now,
		ExpiresAt: request.ChangeWindow.EndAt.UTC(), UpdatedAt: now,
	}
	created, fresh, err := engine.store.CreateFleetRunnerUpdatePlan(ctx, plan)
	if err != nil || !fresh {
		_ = os.Remove(stagedPath)
	}
	return created, fresh, err
}

func (engine *Engine) expireFleetRunnerUpdatePlans(ctx context.Context, now time.Time) error {
	paths, err := engine.store.ExpireFleetRunnerUpdatePlans(ctx, now)
	if err != nil {
		return err
	}
	for _, path := range paths {
		if path != "" {
			_ = os.Remove(path)
		}
	}
	return nil
}

func (engine *Engine) ApproveFleetRunnerUpdatePlan(
	ctx context.Context,
	actor, id string,
	request model.FleetRunnerUpdateApprovalRequest,
) (model.FleetRunnerUpdatePlan, error) {
	plan, err := engine.store.GetFleetRunnerUpdatePlan(ctx, id)
	if err != nil {
		return model.FleetRunnerUpdatePlan{}, err
	}
	if err := engine.authorizeFleetRunnerUpdatePlan(ctx, actor, plan, model.PermissionRunnerUpdate); err != nil {
		return model.FleetRunnerUpdatePlan{}, err
	}
	if err := engine.revalidateFleetRunnerUpdatePlan(ctx, plan); err != nil {
		return model.FleetRunnerUpdatePlan{}, err
	}
	return engine.store.ApproveFleetRunnerUpdatePlan(ctx, id, actor, request.Digest, request.Confirmation)
}

func (engine *Engine) ExecuteFleetRunnerUpdatePlan(
	ctx context.Context,
	actor, id string,
	request model.FleetRunnerUpdateExecuteRequest,
) (model.FleetRunnerUpdatePlan, bool, error) {
	if !uuidPattern.MatchString(request.IdempotencyKey) {
		return model.FleetRunnerUpdatePlan{}, false, errors.New("Runner Fleet 更新执行幂等键无效")
	}
	plan, err := engine.store.GetFleetRunnerUpdatePlan(ctx, id)
	if err != nil {
		return model.FleetRunnerUpdatePlan{}, false, err
	}
	if err := engine.authorizeFleetRunnerUpdatePlan(ctx, actor, plan, model.PermissionRunnerUpdate); err != nil {
		return model.FleetRunnerUpdatePlan{}, false, err
	}
	if plan.State == model.FleetRunnerUpdateApproved {
		if err := engine.revalidateFleetRunnerUpdatePlan(ctx, plan); err != nil {
			return model.FleetRunnerUpdatePlan{}, false, err
		}
	}
	started, fresh, err := engine.store.StartFleetRunnerUpdatePlan(ctx, id, actor, request.IdempotencyKey)
	if err != nil || !fresh {
		return started, fresh, err
	}
	engine.startFleetRunnerUpdateCoordinator(started.ID)
	return started, true, nil
}

func (engine *Engine) CancelFleetRunnerUpdatePlan(
	ctx context.Context,
	actor, id string,
	request model.FleetRunnerUpdateCancelRequest,
) (model.FleetRunnerUpdatePlan, bool, error) {
	if !uuidPattern.MatchString(request.IdempotencyKey) {
		return model.FleetRunnerUpdatePlan{}, false, errors.New("Runner Fleet 更新取消幂等键无效")
	}
	plan, err := engine.store.GetFleetRunnerUpdatePlan(ctx, id)
	if err != nil {
		return model.FleetRunnerUpdatePlan{}, false, err
	}
	if err := engine.authorizeFleetRunnerUpdatePlan(ctx, actor, plan, model.PermissionRunnerUpdate); err != nil {
		return model.FleetRunnerUpdatePlan{}, false, err
	}
	cancelled, fresh, err := engine.store.CancelFleetRunnerUpdatePlan(
		ctx, id, actor, request.IdempotencyKey, request.Confirmation,
	)
	if err == nil && fresh {
		_ = os.Remove(plan.StagedPath)
	}
	return cancelled, fresh, err
}

func (engine *Engine) authorizeFleetRunnerUpdatePlan(
	ctx context.Context,
	actor string,
	plan model.FleetRunnerUpdatePlan,
	permission model.Permission,
) error {
	tenantID, err := engine.actorTenantID(ctx, actor)
	if err != nil {
		return err
	}
	if tenantID != plan.TenantID || len(plan.TargetRunnerIDs) == 0 {
		return authorizationError{message: "Runner Fleet 更新计划不属于操作者租户"}
	}
	for _, runnerID := range plan.TargetRunnerIDs {
		if err := engine.authorizePlatform(ctx, actor, permission, "runner:"+runnerID); err != nil {
			return err
		}
	}
	return nil
}

func (engine *Engine) fleetRunnerUpdatePolicy() (*config.RunnerUpdatePolicy, error) {
	if engine.catalog.RunnerUpdate == nil || !engine.catalog.RunnerUpdate.Enabled {
		return nil, errors.New("Runner 自更新尚未启用")
	}
	if !engine.catalog.RunnerUpdate.FleetEnabled {
		return nil, errors.New("Runner Fleet 自更新尚未启用")
	}
	if engine.catalog.Fleet == nil || !engine.catalog.Fleet.Enabled {
		return nil, errors.New("Runner Fleet 尚未启用")
	}
	return engine.catalog.RunnerUpdate, nil
}

func validateFleetRunnerUpdateRequest(request model.FleetRunnerUpdatePlanRequest) error {
	if !uuidPattern.MatchString(request.IdempotencyKey) {
		return errors.New("Runner Fleet 更新幂等键无效")
	}
	if len(request.TargetRunnerIDs) == 0 || len(request.TargetRunnerIDs) > maxFleetRunnerUpdateTargets {
		return errors.New("Runner Fleet 更新必须显式提供 1 到 100 个目标")
	}
	seen := make(map[string]struct{}, len(request.TargetRunnerIDs))
	for _, id := range request.TargetRunnerIDs {
		if !fleetRunnerIDPattern.MatchString(id) || strings.Contains(id, "*") {
			return fmt.Errorf("Runner Fleet 更新目标无效: %s", id)
		}
		if _, duplicate := seen[id]; duplicate {
			return fmt.Errorf("Runner Fleet 更新目标重复: %s", id)
		}
		seen[id] = struct{}{}
	}
	if err := request.BatchPolicy.Validate(); err != nil {
		return err
	}
	if len(request.TargetRunnerIDs) > 1 && request.BatchPolicy.Strategy != model.BatchCanary {
		return errors.New("多 Runner 更新必须先执行 Canary")
	}
	if request.BatchPolicy.Strategy == model.BatchCanary &&
		(request.BatchPolicy.ObservationSeconds < 30 || request.BatchPolicy.ObservationSeconds > 3600) {
		return errors.New("Runner Canary 观察窗口必须为 30 到 3600 秒")
	}
	if request.BatchPolicy.PauseSeconds > 600 {
		return errors.New("Runner Fleet 批次暂停不能超过 600 秒")
	}
	if request.MaxConcurrent < 1 || request.MaxConcurrent > 32 || request.MaxConcurrent > len(request.TargetRunnerIDs) {
		return errors.New("Runner Fleet 并发限制无效")
	}
	if !request.RollbackOnFailure {
		return errors.New("Runner Fleet 更新必须启用失败回滚")
	}
	if err := request.ChangeWindow.Validate(); err != nil {
		return err
	}
	now := time.Now().UTC()
	if !request.ChangeWindow.EndAt.After(now) || request.ChangeWindow.EndAt.Sub(request.ChangeWindow.StartAt) > maxFleetUpdateWindow ||
		request.ChangeWindow.StartAt.After(now.Add(maxFleetUpdateSchedule)) {
		return errors.New("Runner Fleet 变更窗口无效或超出允许范围")
	}
	if request.Confirmation != fleetRunnerUpdateConfirmation(request.Manifest.TargetVersion, len(request.TargetRunnerIDs)) {
		return errors.New("Runner Fleet 更新确认短语不匹配")
	}
	if request.ArtifactPath == "" || filepath.IsAbs(request.ArtifactPath) {
		return errors.New("Runner Fleet 制品路径必须是 artifactRoot 下的相对路径")
	}
	return nil
}

func fleetRunnerUpdateConfirmation(version string, targets int) string {
	return fmt.Sprintf("创建 Runner Fleet 更新到 %s，目标 %d 个", version, targets)
}

func validateFleetRunnerUpdateManifest(policy *config.RunnerUpdatePolicy, manifest model.FleetRunnerUpdateManifest) error {
	if manifest.Purpose != model.FleetRunnerUpdateManifestPurpose || manifest.Schema != model.FleetRunnerUpdateManifestSchema ||
		manifest.GOOS != policy.ManifestGOOS || manifest.GOARCH != policy.ManifestGOARCH ||
		manifest.Publisher != policy.Publisher || !runnerVersionPattern.MatchString(manifest.TargetVersion) ||
		!runnerRevisionPattern.MatchString(manifest.ArtifactRevision) || !runnerDigestPattern.MatchString(manifest.ArtifactDigest) {
		return errors.New("Runner Fleet 更新签名 manifest 无效")
	}
	return nil
}

func verifyFleetRunnerUpdateSignature(
	policy *config.RunnerUpdatePolicy,
	manifest model.FleetRunnerUpdateManifest,
	encodedSignature string,
) error {
	encodedKey := policy.TrustedPublisherKeys[manifest.Publisher]
	publicKey, err := base64.StdEncoding.Strict().DecodeString(encodedKey)
	if err != nil || len(publicKey) != ed25519.PublicKeySize {
		return errors.New("Runner Fleet 更新发布者公钥无效")
	}
	signature, err := base64.StdEncoding.Strict().DecodeString(encodedSignature)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return errors.New("Runner Fleet 更新签名格式无效")
	}
	payload, err := json.Marshal(manifest)
	if err != nil || !ed25519.Verify(ed25519.PublicKey(publicKey), payload, signature) {
		return errors.New("Runner Fleet 更新签名校验失败")
	}
	return nil
}

func fleetRunnerUpdatePolicyDigest(policy *config.RunnerUpdatePolicy, fleet *config.FleetPolicy) string {
	payload, _ := json.Marshal(struct {
		Purpose          string `json:"purpose"`
		Schema           int    `json:"schema"`
		GOOS             string `json:"goos"`
		GOARCH           string `json:"goarch"`
		Publisher        string `json:"publisher"`
		PublisherKey     string `json:"publisherKey"`
		MaxArtifactBytes int64  `json:"maxArtifactBytes"`
		AllowRemote      bool   `json:"allowRemote"`
		RequireMTLS      bool   `json:"requireMtls"`
		HeartbeatTimeout int    `json:"heartbeatTimeout"`
	}{
		model.FleetRunnerUpdateManifestPurpose, model.FleetRunnerUpdateManifestSchema,
		policy.ManifestGOOS, policy.ManifestGOARCH, policy.Publisher,
		policy.TrustedPublisherKeys[policy.Publisher], policy.MaxArtifactBytes,
		fleet.AllowRemoteRunners, fleet.RequiremTLS, fleet.HeartbeatTimeoutSeconds,
	})
	return digestText(string(payload))
}

func fleetRunnerUpdateRequestDigest(actor string, request model.FleetRunnerUpdatePlanRequest) (string, error) {
	copy := request
	copy.IdempotencyKey = ""
	payload, err := json.Marshal(struct {
		Actor   string                             `json:"actor"`
		Request model.FleetRunnerUpdatePlanRequest `json:"request"`
	}{actor, copy})
	if err != nil {
		return "", err
	}
	return digestText(string(payload)), nil
}

func fleetRunnerUpdatePlanDigest(
	requestDigest, policyDigest string,
	request model.FleetRunnerUpdatePlanRequest,
	targets []fleetRunnerUpdateTargetSnapshot,
) (string, error) {
	payload, err := json.Marshal(fleetRunnerUpdatePlanDigestPayload{
		RequestDigest: requestDigest, PolicyDigest: policyDigest, Manifest: request.Manifest,
		Targets: targets, BatchPolicy: request.BatchPolicy, MaxConcurrent: request.MaxConcurrent,
		ChangeWindow: request.ChangeWindow, RollbackOnFailure: request.RollbackOnFailure,
	})
	if err != nil {
		return "", err
	}
	return digestText(string(payload)), nil
}

func (engine *Engine) validateFleetRunnerUpdateTargets(
	ctx context.Context,
	actor, tenantID string,
	targetIDs []string,
) ([]model.RunnerNode, error) {
	timeout := time.Duration(engine.catalog.Fleet.HeartbeatTimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 90 * time.Second
	}
	fleet, err := engine.store.ListFleet(ctx)
	if err != nil {
		return nil, err
	}
	servers := make(map[string]model.ServerNode, len(fleet.Servers))
	for _, server := range fleet.Servers {
		servers[server.ID] = server
	}
	runners := make(map[string]model.RunnerNode, len(fleet.Runners))
	for _, node := range fleet.Runners {
		runners[node.ID] = node
	}
	now := time.Now().UTC()
	result := make([]model.RunnerNode, 0, len(targetIDs))
	for _, id := range targetIDs {
		node, ok := runners[id]
		if !ok {
			return nil, fmt.Errorf("Runner Fleet 更新目标未登记: %s", id)
		}
		if node.TenantID != tenantID {
			return nil, errors.New("Runner Fleet 更新禁止跨租户目标")
		}
		if err := engine.authorizePlatform(ctx, actor, model.PermissionRunnerUpdate, "runner:"+id); err != nil {
			return nil, err
		}
		server, ok := servers[node.ServerID]
		if !ok || server.State != model.NodeOnline || !node.AvailableAt(now, timeout) {
			return nil, fmt.Errorf("Runner Fleet 更新目标不在线: %s", id)
		}
		if node.IdentityPayloadVersion < RunnerIdentityPayloadVersion ||
			!runnerVersionPattern.MatchString(node.Version) || !runnerRevisionPattern.MatchString(node.Revision) ||
			!runnerDigestPattern.MatchString(node.BinaryDigest) || node.LeaseGeneration == 0 {
			return nil, fmt.Errorf("Runner Fleet 更新目标缺少完整 v2 身份: %s", id)
		}
		if !slices.Contains(node.Capabilities, fleetRunnerUpdateCapability) {
			return nil, fmt.Errorf("Runner Fleet 更新目标缺少 runner-update 能力: %s", id)
		}
		if engine.catalog.Fleet.RequiremTLS && node.CertificateFingerprint == "" {
			return nil, fmt.Errorf("Runner Fleet 更新目标缺少固定 mTLS 指纹: %s", id)
		}
		result = append(result, node)
	}
	return result, nil
}

func fleetRunnerUpdateTargetSnapshots(nodes []model.RunnerNode) []fleetRunnerUpdateTargetSnapshot {
	result := make([]fleetRunnerUpdateTargetSnapshot, 0, len(nodes))
	for _, node := range nodes {
		result = append(result, fleetRunnerUpdateTargetSnapshot{
			RunnerID: node.ID, ServerID: node.ServerID, Version: node.Version,
			Revision: node.Revision, BinaryDigest: node.BinaryDigest,
			LeaseGeneration: node.LeaseGeneration, CertificateFingerprint: node.CertificateFingerprint,
		})
	}
	return result
}

func fleetRunnerUpdateTargetSnapshotsFromItems(items []model.FleetRunnerUpdateItem) []fleetRunnerUpdateTargetSnapshot {
	result := make([]fleetRunnerUpdateTargetSnapshot, 0, len(items))
	for _, item := range items {
		result = append(result, fleetRunnerUpdateTargetSnapshot{
			RunnerID: item.RunnerID, ServerID: item.ServerID, Version: item.PreviousVersion,
			Revision: item.PreviousRevision, BinaryDigest: item.PreviousDigest,
			LeaseGeneration:        item.ExpectedLeaseGeneration,
			CertificateFingerprint: item.CertificateFingerprint,
		})
	}
	return result
}

func (engine *Engine) revalidateFleetRunnerUpdatePlan(ctx context.Context, plan model.FleetRunnerUpdatePlan) error {
	policy, err := engine.fleetRunnerUpdatePolicy()
	if err != nil {
		return err
	}
	if plan.PolicyDigest != fleetRunnerUpdatePolicyDigest(policy, engine.catalog.Fleet) {
		return errors.New("Runner Fleet 更新策略摘要已变化")
	}
	if err := validateFleetRunnerUpdateManifest(policy, plan.Manifest); err != nil {
		return err
	}
	if err := verifyFleetRunnerUpdateSignature(policy, plan.Manifest, plan.ArtifactSignature); err != nil {
		return err
	}
	if plan.ChangeWindow == nil {
		return errors.New("Runner Fleet 更新计划缺少变更窗口")
	}
	expectedStaged := filepath.Join(engine.stateRoot, "runner-updates", "staged", plan.ID+".runner")
	if filepath.Clean(plan.StagedPath) != filepath.Clean(expectedStaged) {
		return errors.New("Runner Fleet 暂存制品路径与计划不一致")
	}
	digest, err := hashFile(plan.StagedPath, policy.MaxArtifactBytes)
	if err != nil || digest != plan.Manifest.ArtifactDigest {
		return errors.New("Runner Fleet 暂存制品摘要已变化")
	}
	request := model.FleetRunnerUpdatePlanRequest{
		Manifest: plan.Manifest, ArtifactPath: plan.ArtifactPath,
		ArtifactSignature: plan.ArtifactSignature, TargetRunnerIDs: plan.TargetRunnerIDs,
		BatchPolicy: plan.BatchPolicy, MaxConcurrent: plan.MaxConcurrent,
		ChangeWindow: *plan.ChangeWindow, RollbackOnFailure: plan.RollbackOnFailure,
		Confirmation: fleetRunnerUpdateConfirmation(plan.Manifest.TargetVersion, len(plan.TargetRunnerIDs)),
	}
	want, err := fleetRunnerUpdatePlanDigest(plan.RequestDigest, plan.PolicyDigest, request,
		fleetRunnerUpdateTargetSnapshotsFromItems(plan.Items))
	if err != nil || want != plan.PlanDigest {
		return errors.New("Runner Fleet 更新计划摘要复验失败")
	}
	return engine.revalidateFleetRunnerBeforeIdentities(ctx, plan)
}

func (engine *Engine) ClaimFleetRunnerUpdate(
	ctx context.Context,
	runnerID string,
	lease time.Duration,
) (model.FleetRunnerUpdateAssignment, bool, error) {
	if _, err := engine.fleetRunnerUpdatePolicy(); err != nil {
		return model.FleetRunnerUpdateAssignment{}, false, err
	}
	return engine.store.ClaimFleetRunnerUpdate(ctx, runnerID, lease)
}

func (engine *Engine) HeartbeatFleetRunnerUpdate(
	ctx context.Context,
	runnerID, itemID string,
	input model.FleetRunnerUpdateHeartbeatRequest,
	lease time.Duration,
) (model.FleetRunnerUpdateItem, error) {
	if _, err := engine.fleetRunnerUpdatePolicy(); err != nil {
		return model.FleetRunnerUpdateItem{}, err
	}
	return engine.store.HeartbeatFleetRunnerUpdate(
		ctx, runnerID, itemID, input.FleetRunnerUpdateFence, lease,
	)
}

func (engine *Engine) CompleteFleetRunnerUpdate(
	ctx context.Context,
	runnerID, itemID string,
	input model.FleetRunnerUpdateCompletionRequest,
) (model.FleetRunnerUpdateItem, bool, error) {
	if _, err := engine.fleetRunnerUpdatePolicy(); err != nil {
		return model.FleetRunnerUpdateItem{}, false, err
	}
	if item, replayed, err := engine.store.FleetRunnerUpdateCompletionReplay(
		ctx, runnerID, itemID, input,
	); err != nil || replayed {
		return item, false, err
	}
	assignment, err := engine.store.FleetRunnerUpdateAssignmentForArtifact(
		ctx, runnerID, itemID, input.FleetRunnerUpdateFence,
	)
	if err != nil {
		return model.FleetRunnerUpdateItem{}, false, err
	}
	if err := engine.ensureFleetRunnerCompletionIdentity(assignment, input); err != nil {
		return model.FleetRunnerUpdateItem{}, false, err
	}
	return engine.store.CompleteFleetRunnerUpdate(ctx, runnerID, itemID, input)
}

func (engine *Engine) OpenFleetRunnerUpdateArtifact(
	ctx context.Context,
	runnerID, itemID string,
	input model.FleetRunnerUpdateArtifactRequest,
) (*os.File, int64, string, error) {
	policy, err := engine.fleetRunnerUpdatePolicy()
	if err != nil {
		return nil, 0, "", err
	}
	assignment, err := engine.store.FleetRunnerUpdateAssignmentForArtifact(
		ctx, runnerID, itemID, input.FleetRunnerUpdateFence,
	)
	if err != nil {
		return nil, 0, "", err
	}
	if assignment.Action != "update" {
		return nil, 0, "", errors.New("Runner Fleet 回滚必须使用节点本地受控副本")
	}
	plan, err := engine.store.GetFleetRunnerUpdatePlan(ctx, assignment.PlanID)
	if err != nil {
		return nil, 0, "", err
	}
	if err := engine.revalidateFleetRunnerUpdateArtifact(plan, policy); err != nil {
		return nil, 0, "", err
	}
	file, size, err := openVerifiedRunnerArtifact(
		plan.StagedPath, plan.Manifest.ArtifactDigest, policy.MaxArtifactBytes,
	)
	return file, size, plan.Manifest.ArtifactDigest, err
}

func (engine *Engine) revalidateFleetRunnerUpdateArtifact(
	plan model.FleetRunnerUpdatePlan,
	policy *config.RunnerUpdatePolicy,
) error {
	if plan.PolicyDigest != fleetRunnerUpdatePolicyDigest(policy, engine.catalog.Fleet) {
		return errors.New("Runner Fleet 更新策略摘要已变化")
	}
	if err := validateFleetRunnerUpdateManifest(policy, plan.Manifest); err != nil {
		return err
	}
	if err := verifyFleetRunnerUpdateSignature(policy, plan.Manifest, plan.ArtifactSignature); err != nil {
		return err
	}
	if plan.ChangeWindow == nil {
		return errors.New("Runner Fleet 更新计划缺少变更窗口")
	}
	expected := filepath.Join(engine.stateRoot, "runner-updates", "staged", plan.ID+".runner")
	if filepath.Clean(plan.StagedPath) != filepath.Clean(expected) {
		return errors.New("Runner Fleet 暂存制品路径与计划不一致")
	}
	request := model.FleetRunnerUpdatePlanRequest{
		Manifest: plan.Manifest, ArtifactPath: plan.ArtifactPath,
		ArtifactSignature: plan.ArtifactSignature, TargetRunnerIDs: plan.TargetRunnerIDs,
		BatchPolicy: plan.BatchPolicy, MaxConcurrent: plan.MaxConcurrent,
		ChangeWindow: *plan.ChangeWindow, RollbackOnFailure: plan.RollbackOnFailure,
		Confirmation: fleetRunnerUpdateConfirmation(plan.Manifest.TargetVersion, len(plan.TargetRunnerIDs)),
	}
	want, err := fleetRunnerUpdatePlanDigest(plan.RequestDigest, plan.PolicyDigest, request,
		fleetRunnerUpdateTargetSnapshotsFromItems(plan.Items))
	if err != nil || want != plan.PlanDigest {
		return errors.New("Runner Fleet 更新计划摘要复验失败")
	}
	return nil
}

func openVerifiedRunnerArtifact(path, expectedDigest string, limit int64) (*os.File, int64, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		info.Size() < 1 || info.Size() > limit {
		return nil, 0, errors.New("Runner Fleet 暂存制品身份或大小无效")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		file.Close()
		return nil, 0, errors.New("Runner Fleet 暂存制品读取期间身份变化")
	}
	hasher := sha256.New()
	written, err := io.Copy(hasher, io.LimitReader(file, limit+1))
	actualDigest := "sha256:" + hex.EncodeToString(hasher.Sum(nil))
	if err != nil || written != opened.Size() || written > limit || actualDigest != expectedDigest {
		file.Close()
		return nil, 0, errors.New("Runner Fleet 暂存制品摘要校验失败")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		file.Close()
		return nil, 0, err
	}
	return file, opened.Size(), nil
}

func (engine *Engine) revalidateFleetRunnerBeforeIdentities(
	ctx context.Context,
	plan model.FleetRunnerUpdatePlan,
) error {
	timeout := time.Duration(engine.catalog.Fleet.HeartbeatTimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 90 * time.Second
	}
	now := time.Now().UTC()
	for _, item := range plan.Items {
		node, found, err := engine.store.GetRunnerNode(ctx, item.RunnerID)
		if err != nil || !found {
			return fmt.Errorf("Runner Fleet 更新目标身份不可读: %s", item.RunnerID)
		}
		if node.TenantID != plan.TenantID || node.ServerID != item.ServerID ||
			node.Version != item.PreviousVersion || node.Revision != item.PreviousRevision ||
			node.BinaryDigest != item.PreviousDigest || node.IdentityPayloadVersion < RunnerIdentityPayloadVersion ||
			node.LeaseGeneration < item.ExpectedLeaseGeneration || !node.AvailableAt(now, timeout) ||
			node.CertificateFingerprint != item.CertificateFingerprint {
			return fmt.Errorf("Runner Fleet 更新目标身份或租约已变化: %s", item.RunnerID)
		}
	}
	return nil
}
