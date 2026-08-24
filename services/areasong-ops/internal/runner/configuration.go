package runner

import (
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
	"sort"
	"strings"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	"gopkg.in/yaml.v3"
)

const maxComposeBytes = 1 << 20

const maxKubernetesManifestBytes = 2 << 20

func (engine *Engine) ComposeFile(ctx context.Context, serviceName string) (model.ComposeFileView, error) {
	service, ok := engine.catalog.Services[serviceName]
	if !ok || service.Runtime == nil {
		return model.ComposeFileView{}, errors.New("服务没有受管 Compose 配置")
	}
	content, err := readControlledFile(service.Runtime.ControlledCompose, maxComposeBytes)
	if err != nil {
		return model.ComposeFileView{}, err
	}
	digest := digestText(content)
	revisions, err := engine.store.ListComposeRevisions(ctx, serviceName, 20)
	if err != nil {
		return model.ComposeFileView{}, err
	}
	return model.ComposeFileView{Service: serviceName, ControlledPath: service.Runtime.ControlledCompose, RuntimePath: service.Runtime.RuntimeCompose, Digest: digest, Content: content, Revisions: revisions}, nil
}

func (engine *Engine) ProposeCompose(ctx context.Context, actor string, request model.ComposeEditRequest) (model.ComposeRevision, error) {
	if err := engine.authorize(ctx, actor, model.PermissionManageConfig, "service:"+request.Service); err != nil {
		return model.ComposeRevision{}, err
	}
	if request.Mode != "validate" && request.Mode != "propose" {
		return model.ComposeRevision{}, errors.New("Compose 编辑模式必须是 validate 或 propose")
	}
	if len(request.Content) == 0 || len(request.Content) > maxComposeBytes || strings.ContainsRune(request.Content, '\x00') {
		return model.ComposeRevision{}, errors.New("Compose 内容为空、过大或包含非法字符")
	}
	if err := validateComposeContent(request.Content); err != nil {
		return model.ComposeRevision{}, err
	}
	current, err := engine.ComposeFile(ctx, request.Service)
	if err != nil {
		return model.ComposeRevision{}, err
	}
	if request.ExpectedDigest != current.Digest {
		return model.ComposeRevision{}, errors.New("Compose 基线摘要已变化，请重新读取")
	}
	if request.Mode == "validate" {
		return model.ComposeRevision{Service: request.Service, Digest: digestText(request.Content), Source: "validation", Content: request.Content, Validated: true, CreatedAt: time.Now().UTC()}, nil
	}
	if !uuidPattern.MatchString(request.IdempotencyKey) {
		return model.ComposeRevision{}, errors.New("Compose 提案幂等键无效")
	}
	id, err := newUUID()
	if err != nil {
		return model.ComposeRevision{}, err
	}
	revision := model.ComposeRevision{ID: id, IdempotencyKey: request.IdempotencyKey,
		Service: request.Service, Digest: digestText(request.Content), ExpectedDigest: current.Digest,
		Source: "web-proposal", Content: request.Content, Validated: true, State: "proposed",
		ActorHash: actor, CreatedAt: time.Now().UTC()}
	revision.ConfirmationPhrase = fmt.Sprintf("批准 Compose 变更 %s %s", request.Service, shortDigest(revision.Digest))
	requestDigest := digestText(strings.Join([]string{actor, request.Service, current.Digest, revision.Digest}, "\x00"))
	stored, created, err := engine.store.SaveComposeRevisionIdempotent(ctx, revision, requestDigest)
	if err != nil {
		return model.ComposeRevision{}, err
	}
	if created {
		_, _ = engine.store.AppendAudit(ctx, model.AuditEntry{ActorHash: actor, Event: "compose.revision.proposed", Resource: request.Service, Outcome: "accepted", Detail: map[string]any{"revisionId": id, "digest": revision.Digest}})
	}
	return stored, nil
}

// validateComposeContent parses the document structurally before it is stored
// as a proposal. Web input is intentionally limited to the Compose data model;
// it can be reviewed and applied by the deployment workflow, but it cannot
// smuggle host paths, privileged settings, or a second YAML document through a
// string-only "services:" check.
func validateComposeContent(content string) error {
	decoder := yaml.NewDecoder(strings.NewReader(content))
	var root yaml.Node
	if err := decoder.Decode(&root); err != nil {
		return fmt.Errorf("Compose YAML 无效: %w", err)
	}
	if root.Kind == 0 {
		return errors.New("Compose 文档为空")
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("Compose 不允许多文档 YAML")
		}
		return fmt.Errorf("Compose 多文档解析失败: %w", err)
	}
	if err := validateComposeNode(&root, "root"); err != nil {
		return err
	}
	if root.Kind != yaml.DocumentNode || len(root.Content) != 1 || root.Content[0].Kind != yaml.MappingNode {
		return errors.New("Compose 顶层必须是对象")
	}
	top := root.Content[0]
	allowedTop := map[string]bool{"version": true, "name": true, "services": true, "networks": true, "volumes": true, "configs": true, "secrets": true}
	var services *yaml.Node
	for index := 0; index < len(top.Content); index += 2 {
		key, value := top.Content[index].Value, top.Content[index+1]
		if !allowedTop[key] {
			return fmt.Errorf("Compose 顶层字段 %q 不在受控白名单", key)
		}
		if key == "services" {
			services = value
		}
		if key == "volumes" || key == "networks" || key == "configs" || key == "secrets" {
			if err := validateComposeNamedResources(value, key); err != nil {
				return err
			}
		}
	}
	if services == nil || services.Kind != yaml.MappingNode || len(services.Content) == 0 {
		return errors.New("Compose 必须声明非空 services 对象")
	}
	for index := 0; index < len(services.Content); index += 2 {
		name, service := services.Content[index].Value, services.Content[index+1]
		if !composeNamePattern.MatchString(name) || service.Kind != yaml.MappingNode {
			return fmt.Errorf("Compose 服务 %q 声明无效", name)
		}
		if err := validateComposeService(service, name); err != nil {
			return err
		}
	}
	return nil
}

var composeNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,62}$`)

func validateComposeNode(node *yaml.Node, path string) error {
	if node == nil {
		return errors.New("Compose YAML 节点为空")
	}
	if node.Kind == yaml.AliasNode {
		return fmt.Errorf("Compose %s 不允许 YAML alias/anchor", path)
	}
	if node.Kind == yaml.MappingNode {
		seen := make(map[string]struct{}, len(node.Content)/2)
		for index := 0; index < len(node.Content); index += 2 {
			key, value := node.Content[index], node.Content[index+1]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" {
				return fmt.Errorf("Compose %s 的映射键必须是字符串", path)
			}
			if _, exists := seen[key.Value]; exists {
				return fmt.Errorf("Compose %s 存在重复字段 %q", path, key.Value)
			}
			seen[key.Value] = struct{}{}
			if key.Value == "<<" {
				return fmt.Errorf("Compose %s 不允许 YAML merge key", path)
			}
			if err := validateComposeNode(value, path+"."+key.Value); err != nil {
				return err
			}
		}
	} else {
		for index, child := range node.Content {
			if err := validateComposeNode(child, fmt.Sprintf("%s[%d]", path, index)); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateComposeNamedResources(node *yaml.Node, kind string) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("Compose %s 必须是对象", kind)
	}
	for index := 0; index < len(node.Content); index += 2 {
		name, value := node.Content[index].Value, node.Content[index+1]
		if !composeNamePattern.MatchString(name) || value.Kind != yaml.MappingNode {
			return fmt.Errorf("Compose %s %q 声明无效", kind, name)
		}
		for child := 0; child < len(value.Content); child += 2 {
			key := value.Content[child].Value
			if key == "driver_opts" || key == "device" || key == "o" || key == "file" {
				return fmt.Errorf("Compose %s %q 不允许主机路径选项", kind, name)
			}
		}
	}
	return nil
}

func validateComposeService(node *yaml.Node, name string) error {
	keys := make(map[string]*yaml.Node, len(node.Content)/2)
	for index := 0; index < len(node.Content); index += 2 {
		keys[node.Content[index].Value] = node.Content[index+1]
	}
	if keys["image"] == nil && keys["build"] == nil {
		return fmt.Errorf("Compose 服务 %q 必须声明 image 或 build", name)
	}
	if build := keys["build"]; build != nil {
		if build.Kind == yaml.ScalarNode {
			if strings.HasPrefix(build.Value, "/") || strings.HasPrefix(build.Value, "../") || build.Value == ".." {
				return fmt.Errorf("Compose 服务 %q build 路径越界", name)
			}
		} else if build.Kind == yaml.MappingNode {
			for index := 0; index < len(build.Content); index += 2 {
				if build.Content[index].Value == "context" {
					contextPath := build.Content[index+1].Value
					if strings.HasPrefix(contextPath, "/") || strings.HasPrefix(contextPath, "../") || contextPath == ".." {
						return fmt.Errorf("Compose 服务 %q build context 越界", name)
					}
				}
			}
		} else {
			return fmt.Errorf("Compose 服务 %q build 声明无效", name)
		}
	}
	for _, key := range []string{"privileged", "pid", "ipc", "network_mode", "userns_mode", "devices", "cap_add", "capabilities"} {
		if value := keys[key]; value != nil {
			if key == "capabilities" && value.Kind == yaml.SequenceNode && len(value.Content) == 0 {
				continue
			}
			return fmt.Errorf("Compose 服务 %q 使用了禁止字段 %q", name, key)
		}
	}
	if volumes := keys["volumes"]; volumes != nil {
		if volumes.Kind != yaml.SequenceNode {
			return fmt.Errorf("Compose 服务 %q volumes 必须是数组", name)
		}
		for _, volume := range volumes.Content {
			if volume.Kind != yaml.ScalarNode {
				return fmt.Errorf("Compose 服务 %q volume 声明必须是字符串", name)
			}
			value := volume.Value
			source := strings.SplitN(value, ":", 2)[0]
			if strings.HasPrefix(source, "/") || strings.HasPrefix(source, "../") || source == ".." || strings.HasPrefix(source, "./") {
				return fmt.Errorf("Compose 服务 %q 不允许越界 bind volume", name)
			}
		}
	}
	if environment := keys["environment"]; environment != nil && environment.Kind == yaml.MappingNode {
		for index := 0; index < len(environment.Content); index += 2 {
			key, value := environment.Content[index].Value, environment.Content[index+1]
			upper := strings.ToUpper(key)
			if (strings.Contains(upper, "PASSWORD") || strings.Contains(upper, "TOKEN") || strings.Contains(upper, "SECRET")) && value.Kind == yaml.ScalarNode && value.Value != "" && !strings.HasPrefix(value.Value, "${") && !strings.HasPrefix(value.Value, "$$") {
				return fmt.Errorf("Compose 服务 %q 不允许在网页提案中写入明文凭据 %q", name, key)
			}
		}
	}
	return nil
}

func readControlledFile(path string, limit int) (string, error) {
	if !filepath.IsAbs(path) {
		return "", errors.New("受控配置路径必须是绝对路径")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("受控配置必须是普通文件且不能是符号链接")
	}
	if info.Size() > int64(limit) {
		return "", errors.New("受控配置超过大小限制")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if err != nil {
		return "", err
	}
	if len(data) > limit {
		return "", errors.New("受控配置超过大小限制")
	}
	return string(data), nil
}

func digestText(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func (engine *Engine) Kubernetes(ctx context.Context, actor string, request model.KubernetesRequest) (model.KubernetesOperation, string, error) {
	if request.Action != "validate" {
		return model.KubernetesOperation{}, "", errors.New("Kubernetes 普通操作接口只允许 validate；apply 必须通过双人批准计划执行")
	}
	return engine.runKubernetesOperation(ctx, actor, request, false)
}

func (engine *Engine) applyKubernetesPlan(
	ctx context.Context,
	actor string,
	request model.KubernetesRequest,
) (model.KubernetesOperation, string, error) {
	if request.Action != "apply" {
		return model.KubernetesOperation{}, "", errors.New("Kubernetes 计划内部执行只允许 apply")
	}
	return engine.runKubernetesOperation(ctx, actor, request, true)
}

func (engine *Engine) runKubernetesOperation(
	ctx context.Context,
	actor string,
	request model.KubernetesRequest,
	allowApply bool,
) (model.KubernetesOperation, string, error) {
	if err := engine.authorizePlatform(ctx, actor, model.PermissionDeploy, "kubernetes:"+request.Target.Cluster); err != nil {
		return model.KubernetesOperation{}, "", err
	}
	if len(request.Manifest) == 0 || len(request.Manifest) > maxKubernetesManifestBytes || strings.ContainsRune(request.Manifest, '\x00') {
		return model.KubernetesOperation{}, "", errors.New("Kubernetes manifest 为空、过大或包含非法字符")
	}
	if request.Action != "validate" && request.Action != "apply" {
		return model.KubernetesOperation{}, "", errors.New("Kubernetes 只支持 validate 或 apply")
	}
	if request.Action == "apply" && !allowApply {
		return model.KubernetesOperation{}, "", errors.New("Kubernetes apply 必须通过双人批准计划执行")
	}
	if request.Action == "apply" && request.DryRun {
		return model.KubernetesOperation{}, "", errors.New("apply 请求不能同时声明 dryRun")
	}
	if request.Action == "apply" && request.Confirmation != "应用 Kubernetes 清单" {
		return model.KubernetesOperation{}, "", errors.New("Kubernetes apply 需要精确确认短语")
	}
	request.DryRun = request.Action == "validate"
	if strings.TrimSpace(request.IdempotencyKey) == "" || len(request.IdempotencyKey) > 128 {
		return model.KubernetesOperation{}, "", errors.New("Kubernetes 幂等键无效")
	}
	target, err := engine.kubernetesTarget(request.Target)
	if err != nil {
		return model.KubernetesOperation{}, "", err
	}
	// Platform permissions authorize the control-plane operation itself, but
	// they must not turn a tenant-scoped actor into a cross-tenant Kubernetes
	// operator. Only a platform-admin principal may cross this boundary.
	if err := engine.authorizeKubernetesTenant(ctx, actor, target); err != nil {
		return model.KubernetesOperation{}, "", err
	}
	if _, err := validateKubernetesManifest(request.Manifest, target); err != nil {
		return model.KubernetesOperation{}, "", err
	}
	requestDigest := kubernetesRequestDigest(request, target)
	digest := digestText(request.Manifest)
	id, err := newUUID()
	if err != nil {
		return model.KubernetesOperation{}, "", err
	}
	now := time.Now().UTC()
	op := model.KubernetesOperation{
		ID: id, IdempotencyKey: request.IdempotencyKey, RequestDigest: requestDigest,
		Target: target, TenantID: target.TenantID, Action: request.Action, ManifestDigest: digest,
		DryRun: request.Action == "validate", State: "pending", CreatedAt: now,
	}
	stored, previousOutput, created, err := engine.store.BeginKubernetesOperation(ctx, op, actor, requestDigest)
	if err != nil {
		return model.KubernetesOperation{}, "", err
	}
	if !created {
		if request.Action != "apply" {
			return stored, previousOutput, nil
		}
		switch stored.State {
		case "pending":
			op = stored
		case "running":
			failure := errors.New("Runner 在 Kubernetes apply 执行中断，结果未知，禁止自动重试")
			engine.markKubernetesNeedsAttention(context.Background(), stored.ID, previousOutput, failure.Error())
			stored.State, stored.Error = "needs_attention", failure.Error()
			return stored, previousOutput, failure
		case "succeeded":
			return stored, previousOutput, nil
		default:
			failure := fmt.Errorf("Kubernetes apply 已处于 %s: %s", stored.State, stored.Error)
			return stored, previousOutput, failure
		}
	}
	if created {
		op = stored
	}
	if err := engine.store.UpdateKubernetesOperation(ctx, op.ID, "running", "", ""); err != nil {
		failure := fmt.Errorf("Kubernetes 操作状态持久化失败: %w", err)
		engine.markKubernetesNeedsAttention(ctx, op.ID, "", failure.Error())
		op.State = "needs_attention"
		op.Error = failure.Error()
		return op, "", failure
	}
	op.State = "running"
	args := []string{"--context", target.Context, "-n", target.Namespace, "apply", "--field-manager", "areasong-ops"}
	if request.Action == "validate" {
		args = append(args, "--dry-run=server")
	}
	args = append(args, "-f", "-")
	command := exec.CommandContext(ctx, "kubectl", args...)
	command.Stdin = strings.NewReader(request.Manifest)
	output, runErr := command.CombinedOutput()
	cleanOutput := redactText(string(output))
	if runErr != nil {
		state := "failed"
		if request.Action == "apply" {
			state = "needs_attention"
		}
		errorText := redactText(runErr.Error())
		if persistErr := engine.store.UpdateKubernetesOperation(ctx, op.ID, state, cleanOutput, errorText); persistErr != nil {
			failure := fmt.Errorf("kubectl 操作失败: %s；结果持久化失败: %w", errorText, persistErr)
			engine.markKubernetesNeedsAttention(ctx, op.ID, cleanOutput, failure.Error())
			op.State, op.Error = "needs_attention", failure.Error()
			return op, cleanOutput, failure
		}
		op.State, op.Error = state, errorText
		finished := time.Now().UTC()
		op.FinishedAt = &finished
		return op, cleanOutput, fmt.Errorf("kubectl 操作失败: %s", errorText)
	}
	if persistErr := engine.store.UpdateKubernetesOperation(ctx, op.ID, "succeeded", cleanOutput, ""); persistErr != nil {
		failure := fmt.Errorf("Kubernetes 操作已执行但结果持久化失败: %w", persistErr)
		engine.markKubernetesNeedsAttention(ctx, op.ID, cleanOutput, failure.Error())
		op.State, op.Error = "needs_attention", failure.Error()
		return op, cleanOutput, failure
	}
	finished := time.Now().UTC()
	op.State, op.FinishedAt = "succeeded", &finished
	return op, cleanOutput, nil
}

func (engine *Engine) validateKubernetesTarget(target model.KubernetesTarget) error {
	_, err := engine.kubernetesTarget(target)
	return err
}

func (engine *Engine) authorizeKubernetesTenant(
	ctx context.Context,
	actor string,
	target model.KubernetesTarget,
) error {
	policy, _, err := engine.effectiveAccessPolicy(ctx)
	if err != nil {
		return authorizationError{message: "访问策略快照不可用"}
	}
	if policy == nil || !policy.Enforced || accessPolicyPlatformAdmin(policy, actor) {
		return nil
	}
	if target.TenantID == "" || target.TenantID != accessPolicyActorTenant(policy, actor) {
		return authorizationError{message: "Kubernetes 目标不属于操作者租户"}
	}
	return nil
}

func (engine *Engine) kubernetesTarget(target model.KubernetesTarget) (model.KubernetesTarget, error) {
	if target.Cluster == "" || target.Context == "" || target.Namespace == "" {
		return model.KubernetesTarget{}, errors.New("Kubernetes 目标不完整")
	}
	allowed, ok := engine.catalog.Kubernetes[target.Cluster]
	if !ok {
		return model.KubernetesTarget{}, errors.New("Kubernetes 集群不在声明白名单")
	}
	if allowed.Context != target.Context || allowed.Namespace != target.Namespace {
		return model.KubernetesTarget{}, errors.New("Kubernetes context 或 namespace 不匹配声明")
	}
	if target.TenantID != "" && allowed.TenantID != "" && target.TenantID != allowed.TenantID {
		return model.KubernetesTarget{}, errors.New("Kubernetes 目标租户不匹配声明")
	}
	if target.TenantID == "" {
		target.TenantID = allowed.TenantID
		if target.TenantID == "" && engine.catalog.Access != nil {
			target.TenantID = engine.catalog.Access.DefaultTenant
		}
	}
	if len(allowed.Allowlist) == 0 || len(allowed.ResourceKinds) == 0 {
		return model.KubernetesTarget{}, errors.New("Kubernetes 目标必须声明 resourceKinds 和对象 allowlist")
	}
	for _, kind := range allowed.ResourceKinds {
		if strings.TrimSpace(kind) == "" || isDangerousKubernetesKind(kind) {
			return model.KubernetesTarget{}, fmt.Errorf("Kubernetes resource kind 被禁止: %s", kind)
		}
	}
	for _, object := range allowed.Allowlist {
		if !validKubernetesAllowlistEntry(object) {
			return model.KubernetesTarget{}, fmt.Errorf("Kubernetes 对象 allowlist 项无效: %s", object)
		}
	}
	for _, requestedKind := range target.ResourceKinds {
		if !containsFold(allowed.ResourceKinds, requestedKind) {
			return model.KubernetesTarget{}, errors.New("Kubernetes 请求扩大了 resourceKinds 范围")
		}
	}
	for _, requestedObject := range target.Allowlist {
		if !containsKubernetesAllowlist(allowed.Allowlist, requestedObject) {
			return model.KubernetesTarget{}, errors.New("Kubernetes 请求扩大了对象 allowlist 范围")
		}
	}
	cluster := allowed.Cluster
	if cluster == "" {
		cluster = target.Cluster
	}
	return model.KubernetesTarget{
		Cluster: cluster, Context: allowed.Context, Namespace: allowed.Namespace,
		TenantID:      target.TenantID,
		Allowlist:     append([]string(nil), allowed.Allowlist...),
		ResourceKinds: append([]string(nil), allowed.ResourceKinds...),
	}, nil
}

type kubernetesManifestMetadata struct {
	Name      string `json:"name" yaml:"name"`
	Namespace string `json:"namespace" yaml:"namespace"`
}

type kubernetesManifestObject struct {
	APIVersion string                     `json:"apiVersion" yaml:"apiVersion"`
	Kind       string                     `json:"kind" yaml:"kind"`
	Metadata   kubernetesManifestMetadata `json:"metadata" yaml:"metadata"`
	Items      []kubernetesManifestObject `json:"items" yaml:"items"`
}

// parseKubernetesManifest parses JSON and YAML documents without treating the
// manifest as an opaque string. List envelopes are flattened for policy checks;
// the original bytes are still passed to kubectl after every object is vetted.
func parseKubernetesManifest(manifest string) ([]kubernetesManifestObject, error) {
	decoder := yaml.NewDecoder(strings.NewReader(manifest))
	objects := make([]kubernetesManifestObject, 0)
	for document := 1; ; document++ {
		var node yaml.Node
		err := decoder.Decode(&node)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("解析 Kubernetes manifest 第 %d 个文档失败: %w", document, err)
		}
		if len(node.Content) == 0 {
			continue
		}
		root := node.Content[0]
		if root.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("Kubernetes manifest 第 %d 个文档必须是对象", document)
		}
		var object kubernetesManifestObject
		if err := root.Decode(&object); err != nil {
			return nil, fmt.Errorf("解析 Kubernetes manifest 第 %d 个文档失败: %w", document, err)
		}
		flattened, err := flattenKubernetesManifestObject(object)
		if err != nil {
			return nil, fmt.Errorf("Kubernetes manifest 第 %d 个文档无效: %w", document, err)
		}
		objects = append(objects, flattened...)
	}
	if len(objects) == 0 {
		return nil, errors.New("Kubernetes manifest 不包含对象")
	}
	return objects, nil
}

func flattenKubernetesManifestObject(object kubernetesManifestObject) ([]kubernetesManifestObject, error) {
	if strings.TrimSpace(object.Kind) == "" || strings.TrimSpace(object.APIVersion) == "" {
		return nil, errors.New("对象缺少 apiVersion 或 kind")
	}
	if strings.EqualFold(object.Kind, "List") || strings.HasSuffix(strings.ToLower(object.Kind), "list") {
		if len(object.Items) == 0 {
			return nil, errors.New("List 对象缺少 items")
		}
		result := make([]kubernetesManifestObject, 0, len(object.Items))
		for _, item := range object.Items {
			flattened, err := flattenKubernetesManifestObject(item)
			if err != nil {
				return nil, err
			}
			result = append(result, flattened...)
		}
		return result, nil
	}
	if strings.TrimSpace(object.Metadata.Name) == "" {
		return nil, errors.New("对象缺少 metadata.name")
	}
	if strings.Contains(object.Metadata.Name, "/") {
		return nil, errors.New("metadata.name 不能包含斜杠")
	}
	return []kubernetesManifestObject{object}, nil
}

func validateKubernetesManifest(manifest string, target model.KubernetesTarget) ([]kubernetesManifestObject, error) {
	objects, err := parseKubernetesManifest(manifest)
	if err != nil {
		return nil, err
	}
	for _, object := range objects {
		kind := strings.TrimSpace(object.Kind)
		if isDangerousKubernetesKind(kind) {
			return nil, fmt.Errorf("Kubernetes kind 被禁止: %s", kind)
		}
		if !containsFold(target.ResourceKinds, kind) {
			return nil, fmt.Errorf("Kubernetes kind 不在 resourceKinds allowlist: %s", kind)
		}
		if object.Metadata.Namespace != "" && object.Metadata.Namespace != target.Namespace {
			return nil, fmt.Errorf("Kubernetes 对象 namespace 越界: %s", object.Metadata.Name)
		}
		shortKey, qualifiedKey := kubernetesObjectKeys(object, target.Namespace)
		if !containsKubernetesAllowlist(target.Allowlist, shortKey) && !containsKubernetesAllowlist(target.Allowlist, qualifiedKey) {
			return nil, fmt.Errorf("Kubernetes 对象不在 allowlist: %s", shortKey)
		}
	}
	return objects, nil
}

func kubernetesObjectKeys(object kubernetesManifestObject, namespace string) (string, string) {
	// The short kind/name form is the catalog's canonical representation. The
	// namespace-qualified form is accepted for catalogs that need same-name
	// objects in more than one namespace.
	shortKey := strings.ToLower(strings.TrimSpace(object.Kind)) + "/" + object.Metadata.Name
	return shortKey, namespace + "/" + shortKey
}

func kubernetesRequestDigest(request model.KubernetesRequest, target model.KubernetesTarget) string {
	allowlist := append([]string(nil), target.Allowlist...)
	kinds := append([]string(nil), target.ResourceKinds...)
	sort.Strings(allowlist)
	sort.Strings(kinds)
	parts := []string{
		request.Action, fmt.Sprintf("%t", request.DryRun), target.Cluster, target.Context,
		target.Namespace, strings.Join(kinds, ","), strings.Join(allowlist, ","), request.Manifest,
	}
	return digestText(strings.Join(parts, "\x00"))
}

func containsFold(values []string, candidate string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(candidate)) {
			return true
		}
	}
	return false
}

func containsKubernetesAllowlist(values []string, candidate string) bool {
	for _, value := range values {
		if normalizeKubernetesAllowlistEntry(value) == normalizeKubernetesAllowlistEntry(candidate) {
			return true
		}
	}
	return false
}

func normalizeKubernetesAllowlistEntry(value string) string {
	parts := strings.Split(strings.TrimSpace(value), "/")
	if len(parts) == 2 {
		return strings.ToLower(parts[0]) + "/" + parts[1]
	}
	if len(parts) == 3 {
		return parts[0] + "/" + strings.ToLower(parts[1]) + "/" + parts[2]
	}
	return strings.ToLower(strings.TrimSpace(value))
}

func validKubernetesAllowlistEntry(value string) bool {
	if strings.ContainsAny(value, "*\\") {
		return false
	}
	parts := strings.Split(strings.TrimSpace(value), "/")
	if len(parts) != 2 && len(parts) != 3 {
		return false
	}
	for _, part := range parts {
		if strings.TrimSpace(part) == "" {
			return false
		}
	}
	return !isDangerousKubernetesKind(parts[len(parts)-2])
}

func isDangerousKubernetesKind(kind string) bool {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "namespace", "persistentvolume", "persistentvolumeclaim", "storageclass",
		"clusterrole", "clusterrolebinding", "role", "rolebinding", "customresourcedefinition",
		"node", "resourcequota", "limitrange", "apiservice", "mutatingwebhookconfiguration",
		"validatingwebhookconfiguration", "podsecuritypolicy", "priorityclass":
		return true
	default:
		return false
	}
}

func (engine *Engine) markKubernetesNeedsAttention(ctx context.Context, id, output, reason string) {
	if id == "" {
		return
	}
	_ = engine.store.UpdateKubernetesOperation(ctx, id, "needs_attention", output, redactText(reason))
}
