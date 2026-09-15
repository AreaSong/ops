package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

const kubernetesPreviewLifetime = 15 * time.Minute
const kubernetesPreviewMaxBytes = 256 * 1024

func runKubernetesPreviewCommand(ctx context.Context, target model.KubernetesTarget, input string, args ...string) (string, int, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	arguments := append([]string{"--context", target.Context, "-n", target.Namespace}, args...)
	command := exec.CommandContext(ctx, "kubectl", arguments...)
	command.Stdin = strings.NewReader(input)
	command.WaitDelay = time.Second
	command.Env = append(os.Environ(), "KUBECTL_EXTERNAL_DIFF=diff -u -N -U 1000000")
	output, stderr := newCappedBuffer(kubernetesPreviewMaxBytes), newCappedBuffer(kubernetesPreviewMaxBytes)
	command.Stdout, command.Stderr = output, stderr
	err := command.Run()
	if ctx.Err() != nil {
		return "", -1, ctx.Err()
	}
	if output.truncated || stderr.truncated {
		return "", -1, errors.New("Kubernetes 预览输出过大，请拆分清单")
	}
	if err == nil {
		return output.buffer.String(), 0, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return output.buffer.String(), exit.ExitCode(), fmt.Errorf("kubectl 预览失败，退出码 %d", exit.ExitCode())
	}
	return "", -1, err
}

func kubernetesClusterFingerprint(ctx context.Context, target model.KubernetesTarget) (string, error) {
	output, _, err := runKubernetesPreviewCommand(ctx, target, "", "config", "view", "--minify", "-o", "json")
	if err != nil {
		return "", err
	}
	var config struct {
		Clusters []struct {
			Name    string `json:"name"`
			Cluster struct {
				Server   string `json:"server"`
				Insecure bool   `json:"insecure-skip-tls-verify"`
			} `json:"cluster"`
		} `json:"clusters"`
	}
	if err := json.Unmarshal([]byte(output), &config); err != nil || len(config.Clusters) != 1 || config.Clusters[0].Cluster.Server == "" {
		return "", errors.New("无法确认 Kubernetes context 的集群身份")
	}
	return canonicalDigest(config.Clusters[0].Cluster)
}

func readKubernetesIdentities(ctx context.Context, target model.KubernetesTarget, objects []kubernetesManifestObject) ([]model.KubernetesResourceIdentity, error) {
	identities := make([]model.KubernetesResourceIdentity, 0, len(objects))
	seen := make(map[string]bool)
	for _, object := range objects {
		key, _ := kubernetesObjectKeys(object, target.Namespace)
		if seen[key] {
			return nil, errors.New("Kubernetes 清单包含重复对象")
		}
		seen[key] = true
		output, _, err := runKubernetesPreviewCommand(ctx, target, "", "get", key, "--ignore-not-found", "-o", "json")
		if err != nil {
			return nil, err
		}
		identity := model.KubernetesResourceIdentity{APIVersion: object.APIVersion, Kind: object.Kind, Name: object.Metadata.Name, Namespace: target.Namespace}
		if strings.TrimSpace(output) != "" {
			if err := decodeKubernetesIdentity(output, &identity); err != nil {
				return nil, err
			}
		}
		identities = append(identities, identity)
	}
	sort.Slice(identities, func(i, j int) bool {
		return identities[i].Kind+"/"+identities[i].Name < identities[j].Kind+"/"+identities[j].Name
	})
	return identities, nil
}

func decodeKubernetesIdentity(output string, identity *model.KubernetesResourceIdentity) error {
	var resource struct {
		APIVersion string `json:"apiVersion"`
		Kind       string `json:"kind"`
		Metadata   struct {
			Name              string     `json:"name"`
			Namespace         string     `json:"namespace"`
			UID               string     `json:"uid"`
			ResourceVersion   string     `json:"resourceVersion"`
			DeletionTimestamp *time.Time `json:"deletionTimestamp"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal([]byte(output), &resource); err != nil {
		return errors.New("Kubernetes 对象身份响应无效")
	}
	meta := resource.Metadata
	if resource.APIVersion != identity.APIVersion || resource.Kind != identity.Kind || meta.Name != identity.Name ||
		meta.Namespace != identity.Namespace || meta.UID == "" || meta.ResourceVersion == "" || meta.DeletionTimestamp != nil {
		return errors.New("Kubernetes 对象身份不一致或正在删除")
	}
	identity.Exists, identity.UID, identity.ResourceVersion = true, meta.UID, meta.ResourceVersion
	return nil
}

var kubernetesDiffTemporaryPath = regexp.MustCompile(`\S*/(?:LIVE|MERGED)-[^/]+/`)

func normalizeKubernetesDiff(raw string) string {
	return strings.TrimSpace(kubernetesDiffTemporaryPath.ReplaceAllString(strings.ReplaceAll(raw, "\r\n", "\n"), ""))
}

// 字符串可能包含环境变量或 ConfigMap 私密值；公开预览显示摘要而非原值。
func collectKubernetesPreview(ctx context.Context, target model.KubernetesTarget, manifest string) (*model.KubernetesPreview, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	objects, err := validateKubernetesManifest(manifest, target)
	if err != nil {
		return nil, err
	}
	if len(objects) > 100 {
		return nil, errors.New("Kubernetes 预览最多包含 100 个显式对象，请拆分计划")
	}
	cluster, err := kubernetesClusterFingerprint(ctx, target)
	if err != nil {
		return nil, err
	}
	before, err := readKubernetesIdentities(ctx, target, objects)
	if err != nil {
		return nil, err
	}
	output, code, err := runKubernetesPreviewCommand(ctx, target, manifest, "diff", "--server-side", "--field-manager", "areasong-ops", "-f", "-")
	if code != 0 && code != 1 {
		return nil, err
	}
	if code == 1 && strings.TrimSpace(output) == "" {
		return nil, errors.New("Kubernetes diff 缺少差异输出")
	}
	if code == 1 && !strings.Contains(output, "@@") {
		return nil, errors.New("Kubernetes diff 返回的不是统一差异格式")
	}
	after, err := readKubernetesIdentities(ctx, target, objects)
	if err != nil {
		return nil, err
	}
	if !slices.Equal(before, after) {
		return nil, errors.New("Kubernetes 预览期间对象发生变化，请重试")
	}
	policyDigest, err := canonicalDigest(target)
	if err != nil {
		return nil, err
	}
	raw := normalizeKubernetesDiff(output)
	display, diffDigest, err := kubernetesDiffEvidence(raw, before)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	return &model.KubernetesPreview{Version: 1, PolicyDigest: policyDigest, ClusterFingerprint: cluster,
		Diff: display, DiffDigest: diffDigest, HasChanges: code == 1,
		Resources: before, ObservedAt: now, ExpiresAt: now.Add(kubernetesPreviewLifetime)}, nil
}

func (engine *Engine) verifyKubernetesPreview(ctx context.Context, plan model.KubernetesPlan, manifest string) error {
	digest, err := plan.ApprovalDigest()
	if err != nil || digest != plan.PlanDigest || plan.Preview == nil || !time.Now().UTC().Before(plan.Preview.ExpiresAt) {
		return errors.New("Kubernetes 预览缺失、过期或摘要不符，请重新创建计划")
	}
	target, err := engine.kubernetesTarget(plan.Target)
	if err != nil {
		return err
	}
	current, err := collectKubernetesPreview(ctx, target, manifest)
	if err != nil {
		return err
	}
	if current.PolicyDigest != plan.Preview.PolicyDigest || current.ClusterFingerprint != plan.Preview.ClusterFingerprint ||
		current.DiffDigest != plan.Preview.DiffDigest || !slices.Equal(current.Resources, plan.Preview.Resources) {
		return errors.New("Kubernetes 差异、策略或运行身份已变化，请重新创建并批准计划")
	}
	return nil
}
