package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/config"
	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

const extensionPlanTTL = 30 * time.Minute

func (engine *Engine) CreateExtensionPlan(
	ctx context.Context,
	actor string,
	request model.ExtensionPlanRequest,
) (model.ExtensionPlan, bool, error) {
	policy, err := engine.extensionPolicy()
	if err != nil {
		return model.ExtensionPlan{}, false, err
	}
	if !actorPattern.MatchString(actor) || !uuidPattern.MatchString(request.IdempotencyKey) {
		return model.ExtensionPlan{}, false, errors.New("扩展计划操作者或幂等键无效")
	}
	manifest, _, err := engine.store.GetStoredExtensionPackage(ctx, request.ExtensionID, request.ExtensionVersion)
	if err != nil {
		return model.ExtensionPlan{}, false, err
	}
	if err := engine.validateExtensionManifestForExecution(manifest); err != nil {
		return model.ExtensionPlan{}, false, err
	}
	if err := engine.verifyExtensionSignature(manifest); err != nil {
		return model.ExtensionPlan{}, false, err
	}
	manifestDigest, err := extensionManifestDigest(manifest)
	if err != nil {
		return model.ExtensionPlan{}, false, err
	}
	policyDigest, err := extensionPolicyDigest(*policy, manifest.Publisher)
	if err != nil {
		return model.ExtensionPlan{}, false, err
	}
	object, exists := engine.catalogObjectByID(request.ObjectID)
	if !exists || !slices.Contains(manifest.AllowedObjects, request.ObjectID) {
		return model.ExtensionPlan{}, false, errors.New("扩展目标不在签名对象白名单")
	}
	if err := engine.authorizeExtensionActor(ctx, actor, manifest, request.ObjectID); err != nil {
		return model.ExtensionPlan{}, false, err
	}
	input, err := canonicalExtensionInput(request.Input, policy.MaxInputBytes)
	if err != nil {
		return model.ExtensionPlan{}, false, err
	}
	timeout := request.TimeoutSeconds
	if timeout == 0 {
		timeout = policy.MaxExecutionSeconds
	}
	if timeout < 1 || timeout > policy.MaxExecutionSeconds {
		return model.ExtensionPlan{}, false, errors.New("扩展计划超时超过策略上限")
	}
	now := time.Now().UTC()
	tenantID := object.TenantID
	if tenantID == "" {
		tenantID = "default"
	}
	inputDigest := digestText(input)
	planDigest := digestText(strings.Join([]string{
		actor, tenantID, request.ObjectID, manifest.ID, manifest.Version,
		manifest.Digest, manifestDigest, policyDigest, inputDigest, fmt.Sprint(timeout),
	}, "\x00"))
	phrase := fmt.Sprintf("执行扩展 %s@%s 目标 %s %s", manifest.ID, manifest.Version,
		request.ObjectID, planDigest[:22])
	requestDigest := digestText(strings.Join([]string{planDigest, request.IdempotencyKey}, "\x00"))
	plan := model.ExtensionPlan{
		ID: newPlanID(), IdempotencyKey: request.IdempotencyKey,
		RequestDigest: requestDigest, PlanDigest: planDigest, ActorHash: actor,
		TenantID: tenantID, ObjectID: request.ObjectID, ExtensionID: manifest.ID,
		ExtensionVersion: manifest.Version, ExtensionDigest: manifest.Digest,
		Publisher: manifest.Publisher, ManifestDigest: manifestDigest, PolicyDigest: policyDigest,
		Sandbox: policy.Sandbox, InputDigest: inputDigest, TimeoutSeconds: timeout,
		MaxPackageBytes: policy.MaxPackageBytes, MaxInputBytes: policy.MaxInputBytes,
		MaxOutputBytes: policy.MaxOutputBytes, MaxMemoryPages: policy.MaxMemoryPages,
		State: "pending_approval", ConfirmationPhrase: phrase,
		ApprovalPolicy: model.ApprovalPolicyTwoParty,
		CreatedAt:      now, ExpiresAt: now.Add(extensionPlanTTL),
	}
	stored, created, err := engine.store.CreateExtensionPlan(ctx, plan, input)
	if err != nil {
		return model.ExtensionPlan{}, false, err
	}
	return stored, created, nil
}

func (engine *Engine) ExtensionPlans(
	ctx context.Context,
	actor string,
	limit int,
) ([]model.ExtensionPlan, error) {
	if _, err := engine.extensionPolicy(); err != nil {
		return nil, err
	}
	if err := engine.authorizePlatform(ctx, actor, model.PermissionRead, "extensions"); err != nil {
		return nil, err
	}
	if err := engine.store.ExpireExtensionPlans(ctx, time.Now().UTC()); err != nil {
		return nil, err
	}
	policy, _, err := engine.effectiveAccessPolicy(ctx)
	if err != nil {
		return nil, err
	}
	tenantID := accessPolicyActorTenant(policy, actor)
	if tenantID == "" {
		tenantID = "default"
	}
	return engine.store.ListExtensionPlans(ctx, tenantID, limit)
}

func (engine *Engine) ExtensionPlan(
	ctx context.Context,
	actor, id string,
) (model.ExtensionPlan, error) {
	if !uuidPattern.MatchString(id) {
		return model.ExtensionPlan{}, errors.New("扩展计划标识无效")
	}
	if err := engine.store.ExpireExtensionPlans(ctx, time.Now().UTC()); err != nil {
		return model.ExtensionPlan{}, err
	}
	plan, _, err := engine.store.GetExtensionPlanWithInput(ctx, id)
	if err != nil {
		return model.ExtensionPlan{}, err
	}
	if err := engine.authorizeExtensionPlan(ctx, actor, plan); err != nil {
		return model.ExtensionPlan{}, err
	}
	return plan, nil
}

func (engine *Engine) ApproveExtensionPlan(
	ctx context.Context,
	actor, id string,
	request model.ExtensionPlanApprovalRequest,
) (model.ExtensionPlan, error) {
	if _, err := engine.ExtensionPlan(ctx, actor, id); err != nil {
		return model.ExtensionPlan{}, err
	}
	if request.Digest == "" || request.Confirmation == "" {
		return model.ExtensionPlan{}, errors.New("扩展批准请求不完整")
	}
	approved, err := engine.store.ApproveExtensionPlan(ctx, id, actor, request.Digest, request.Confirmation)
	if err != nil {
		return model.ExtensionPlan{}, err
	}
	return approved, nil
}

func (engine *Engine) ExecuteExtensionPlan(
	ctx context.Context,
	actor, id string,
	request model.ExtensionPlanExecuteRequest,
) (model.ExtensionPlan, error) {
	if !uuidPattern.MatchString(request.IdempotencyKey) {
		return model.ExtensionPlan{}, errors.New("扩展执行幂等键无效")
	}
	if _, err := engine.ExtensionPlan(ctx, actor, id); err != nil {
		return model.ExtensionPlan{}, err
	}
	started, input, fresh, err := engine.store.StartExtensionPlan(ctx, id, actor, request.IdempotencyKey)
	if err != nil || !fresh {
		return started, err
	}
	output, exitCode, runErr := engine.runApprovedExtension(ctx, started, input)
	state, errorText := "succeeded", ""
	if runErr != nil {
		state, errorText = "failed", redactText(runErr.Error())
	}
	output = redactText(output)
	finishContext := context.WithoutCancel(ctx)
	if finishErr := engine.store.FinishExtensionPlan(finishContext, id, state, exitCode, output, errorText); finishErr != nil {
		_ = engine.store.FinishExtensionPlan(context.Background(), id, "needs_attention", exitCode, output,
			"扩展已运行但终态持久化失败")
		return started, fmt.Errorf("扩展已运行但终态持久化失败: %w", finishErr)
	}
	finished, _, getErr := engine.store.GetExtensionPlanWithInput(finishContext, id)
	if getErr != nil {
		return started, getErr
	}
	return finished, runErr
}

func (engine *Engine) runApprovedExtension(
	ctx context.Context,
	plan model.ExtensionPlan,
	input string,
) (string, int, error) {
	policy, err := engine.extensionPolicy()
	if err != nil {
		return "", -1, err
	}
	manifest, path, err := engine.store.GetStoredExtensionPackage(ctx, plan.ExtensionID, plan.ExtensionVersion)
	if err != nil {
		return "", -1, err
	}
	manifestDigest, err := extensionManifestDigest(manifest)
	if err != nil {
		return "", -1, err
	}
	policyDigest, err := extensionPolicyDigest(*policy, manifest.Publisher)
	if err != nil {
		return "", -1, err
	}
	if manifest.ID != plan.ExtensionID || manifest.Version != plan.ExtensionVersion ||
		manifest.Digest != plan.ExtensionDigest || manifest.Publisher != plan.Publisher ||
		manifestDigest != plan.ManifestDigest || policyDigest != plan.PolicyDigest ||
		policy.Sandbox != plan.Sandbox || policy.MaxPackageBytes != plan.MaxPackageBytes ||
		policy.MaxInputBytes != plan.MaxInputBytes || policy.MaxOutputBytes != plan.MaxOutputBytes ||
		policy.MaxMemoryPages != plan.MaxMemoryPages || digestText(input) != plan.InputDigest ||
		!slices.Contains(manifest.AllowedObjects, plan.ObjectID) {
		return "", -1, errors.New("扩展执行材料与批准摘要不一致")
	}
	if err := engine.validateExtensionManifestForExecution(manifest); err != nil {
		return "", -1, err
	}
	if err := engine.verifyExtensionSignature(manifest); err != nil {
		return "", -1, err
	}
	artifact, err := readExtensionArtifact(path, manifest.Digest, policy.MaxPackageBytes)
	if err != nil {
		return "", -1, err
	}
	runContext, cancel := context.WithTimeout(ctx, time.Duration(plan.TimeoutSeconds)*time.Second)
	defer cancel()
	return engine.extensionRunner.Execute(runContext, *policy, manifest, artifact, []byte(input))
}

func extensionManifestDigest(manifest model.ExtensionManifest) (string, error) {
	payload, err := extensionSigningPayload(manifest)
	if err != nil {
		return "", err
	}
	return digestText(strings.Join([]string{string(payload), manifest.Signature}, "\x00")), nil
}

func extensionPolicyDigest(policy config.ExtensionPolicy, publisher string) (string, error) {
	if !contains(policy.TrustedPublishers, publisher) || policy.TrustedPublisherKeys[publisher] == "" {
		return "", errors.New("扩展发布者已被撤销或不在受信白名单")
	}
	snapshot := struct {
		RuntimeContract     string `json:"runtimeContract"`
		Sandbox             string `json:"sandbox"`
		RequireSignature    bool   `json:"requireSignature"`
		Publisher           string `json:"publisher"`
		PublisherKey        string `json:"publisherKey"`
		MaxPackageBytes     int64  `json:"maxPackageBytes"`
		MaxInputBytes       int64  `json:"maxInputBytes"`
		MaxOutputBytes      int64  `json:"maxOutputBytes"`
		MaxExecutionSeconds int    `json:"maxExecutionSeconds"`
		MaxMemoryPages      uint32 `json:"maxMemoryPages"`
	}{
		RuntimeContract: extensionRuntimeContractVersion, Sandbox: policy.Sandbox,
		RequireSignature: policy.RequireSignature, Publisher: publisher,
		PublisherKey: policy.TrustedPublisherKeys[publisher], MaxPackageBytes: policy.MaxPackageBytes,
		MaxInputBytes: policy.MaxInputBytes, MaxOutputBytes: policy.MaxOutputBytes,
		MaxExecutionSeconds: policy.MaxExecutionSeconds, MaxMemoryPages: policy.MaxMemoryPages,
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return "", err
	}
	return digestText(string(encoded)), nil
}

func (engine *Engine) extensionPolicy() (*config.ExtensionPolicy, error) {
	policy := engine.catalog.Extensions
	if policy == nil || !policy.Enabled {
		return nil, errors.New("扩展能力尚未启用")
	}
	if policy.Sandbox != "wasm" || !policy.RequireSignature {
		return nil, errors.New("扩展安全策略不满足执行要求")
	}
	return policy, nil
}

func (engine *Engine) validateExtensionManifestForExecution(manifest model.ExtensionManifest) error {
	if manifest.Purpose != model.ExtensionManifestPurpose || manifest.SchemaVersion != model.ExtensionManifestSchema {
		return errors.New("扩展 manifest 用途或 Schema 版本无效")
	}
	if manifest.Type != "wasm" && manifest.Type != "plugin" {
		return errors.New("WASM 沙箱只接受 wasm 或 plugin 扩展")
	}
	if manifest.Entrypoint != "_start" {
		return errors.New("WASM 扩展入口必须是 _start")
	}
	if len(manifest.AllowedObjects) == 0 {
		return errors.New("扩展必须声明显式对象白名单")
	}
	seenObjects := make(map[string]struct{}, len(manifest.AllowedObjects))
	for _, objectID := range manifest.AllowedObjects {
		if objectID == "*" {
			return errors.New("扩展对象白名单禁止 wildcard")
		}
		if _, exists := seenObjects[objectID]; exists {
			return errors.New("扩展对象白名单存在重复项")
		}
		if _, exists := engine.catalogObjectByID(objectID); !exists {
			return fmt.Errorf("扩展引用了未知受管对象: %s", objectID)
		}
		seenObjects[objectID] = struct{}{}
	}
	seenPermissions := make(map[string]struct{}, len(manifest.Permissions))
	for _, permission := range manifest.Permissions {
		if permission != string(model.PermissionRead) && permission != string(model.PermissionInspect) {
			return fmt.Errorf("WASM 扩展权限不在只读白名单: %s", permission)
		}
		if _, exists := seenPermissions[permission]; exists {
			return errors.New("扩展权限存在重复项")
		}
		seenPermissions[permission] = struct{}{}
	}
	return nil
}

func (engine *Engine) authorizeExtensionPlan(
	ctx context.Context,
	actor string,
	plan model.ExtensionPlan,
) error {
	manifest, _, err := engine.store.GetStoredExtensionPackage(ctx, plan.ExtensionID, plan.ExtensionVersion)
	if err != nil {
		return err
	}
	return engine.authorizeExtensionActor(ctx, actor, manifest, plan.ObjectID)
}

func (engine *Engine) authorizeExtensionActor(
	ctx context.Context,
	actor string,
	manifest model.ExtensionManifest,
	objectID string,
) error {
	if err := engine.authorizePlatform(ctx, actor, model.PermissionManageConfig, "extensions"); err != nil {
		return err
	}
	if err := engine.authorize(ctx, actor, model.PermissionDeploy, objectID); err != nil {
		return err
	}
	for _, permission := range manifest.Permissions {
		if err := engine.authorize(ctx, actor, model.Permission(permission), objectID); err != nil {
			return err
		}
	}
	return nil
}

func canonicalExtensionInput(input json.RawMessage, limit int64) (string, error) {
	if len(input) == 0 || int64(len(input)) > limit {
		return "", errors.New("扩展输入为空或超过策略上限")
	}
	var value map[string]any
	if err := json.Unmarshal(input, &value); err != nil || value == nil {
		return "", errors.New("扩展输入必须是 JSON 对象")
	}
	canonical, err := json.Marshal(value)
	if err != nil || int64(len(canonical)) > limit {
		return "", errors.New("扩展输入无法规范化或超过策略上限")
	}
	return string(canonical), nil
}

func readExtensionArtifact(path, expectedDigest string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > limit {
		return nil, errors.New("扩展制品身份或大小无效")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if digestText(string(content)) != expectedDigest {
		return nil, errors.New("扩展制品摘要与登记记录不一致")
	}
	return content, nil
}
