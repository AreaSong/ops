package runner

import (
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	"gopkg.in/yaml.v3"
)

var immutableImagePattern = regexp.MustCompile(`^[^\s@]+@sha256:[a-f0-9]{64}$`)

type composeAnalysis struct {
	BaselineSemanticDigest  string
	CandidateSemanticDigest string
	Diff                    []model.ComposeDiffEntry
	BaselineImage           string
	CandidateImage          string
	CandidateImageDigest    string
}

// analyzeComposeChange is the shared approval boundary. The service set and
// every semantic field are pinned to the trusted baseline; only the declared
// application service image may change.
func analyzeComposeChange(
	runtime *model.ComposeServiceRuntime,
	baselineContent string,
	candidateContent string,
) (composeAnalysis, error) {
	if runtime == nil || runtime.ProjectName == "" || runtime.ApplicationService == "" {
		return composeAnalysis{}, errors.New("Compose 运行策略缺少固定项目或应用服务")
	}
	baseline, err := parseComposeDocument(baselineContent)
	if err != nil {
		return composeAnalysis{}, fmt.Errorf("Compose 基线无效: %w", err)
	}
	candidate, err := parseComposeDocument(candidateContent)
	if err != nil {
		return composeAnalysis{}, err
	}
	return analyzeComposeDocuments(runtime, baseline, candidate)
}

// analyzeRenderedComposeChange applies the same immutable-service boundary to
// docker compose config output. The rendered document may contain expanded
// secret values, so callers must keep it in memory and persist only the
// returned digests.
func analyzeRenderedComposeChange(
	runtime *model.ComposeServiceRuntime,
	baselineContent string,
	candidateContent string,
) (composeAnalysis, error) {
	baseline, err := parseRenderedComposeDocument(baselineContent)
	if err != nil {
		return composeAnalysis{}, fmt.Errorf("Compose 有效基线无效: %w", err)
	}
	candidate, err := parseRenderedComposeDocument(candidateContent)
	if err != nil {
		return composeAnalysis{}, fmt.Errorf("Compose 有效候选无效: %w", err)
	}
	return analyzeComposeDocuments(runtime, baseline, candidate)
}

func analyzeComposeDocuments(
	runtime *model.ComposeServiceRuntime,
	baseline map[string]any,
	candidate map[string]any,
) (composeAnalysis, error) {
	baselineServices, err := fixedComposeServices(runtime, baseline)
	if err != nil {
		return composeAnalysis{}, fmt.Errorf("Compose 基线不满足固定服务边界: %w", err)
	}
	candidateServices, err := fixedComposeServices(runtime, candidate)
	if err != nil {
		return composeAnalysis{}, err
	}
	if err := fixedComposeProject(runtime, baseline, candidate); err != nil {
		return composeAnalysis{}, err
	}
	baselineTop := cloneComposeMap(baseline)
	candidateTop := cloneComposeMap(candidate)
	delete(baselineTop, "services")
	delete(candidateTop, "services")
	if path := firstComposeChange(baselineTop, candidateTop, "compose"); path != "" {
		return composeAnalysis{}, fmt.Errorf("Compose 禁止修改项目级字段: %s", path)
	}

	for _, dependency := range runtime.DependencyServices {
		if path := firstComposeChange(
			baselineServices[dependency], candidateServices[dependency], "services."+dependency,
		); path != "" {
			return composeAnalysis{}, fmt.Errorf("Compose 依赖服务不可修改: %s", path)
		}
	}
	baselineApp := cloneComposeMap(baselineServices[runtime.ApplicationService])
	candidateApp := cloneComposeMap(candidateServices[runtime.ApplicationService])
	baselineImage, _ := baselineApp["image"].(string)
	candidateImage, _ := candidateApp["image"].(string)
	if _, err := immutableImageDigest(baselineImage); err != nil {
		return composeAnalysis{}, fmt.Errorf("Compose 基线应用镜像无效: %w", err)
	}
	candidateImageDigest, err := immutableImageDigest(candidateImage)
	if err != nil {
		return composeAnalysis{}, fmt.Errorf("Compose 候选应用镜像无效: %w", err)
	}
	baselineRepository, err := immutableImageRepository(baselineImage)
	if err != nil {
		return composeAnalysis{}, fmt.Errorf("Compose 基线应用镜像仓库无效: %w", err)
	}
	candidateRepository, err := immutableImageRepository(candidateImage)
	if err != nil {
		return composeAnalysis{}, fmt.Errorf("Compose 候选应用镜像仓库无效: %w", err)
	}
	if baselineRepository != candidateRepository {
		return composeAnalysis{}, errors.New("Compose 应用镜像仓库不可修改")
	}
	delete(baselineApp, "image")
	delete(candidateApp, "image")
	if path := firstComposeChange(baselineApp, candidateApp, "services."+runtime.ApplicationService); path != "" {
		return composeAnalysis{}, fmt.Errorf("Compose 应用服务只允许修改 image: %s", path)
	}

	baselineDigest, err := canonicalDigest(baseline)
	if err != nil {
		return composeAnalysis{}, err
	}
	candidateDigest, err := canonicalDigest(candidate)
	if err != nil {
		return composeAnalysis{}, err
	}
	diff := make([]model.ComposeDiffEntry, 0, 1)
	if baselineImage != candidateImage {
		diff = append(diff, model.ComposeDiffEntry{
			Path:   "services." + runtime.ApplicationService + ".image",
			Change: "replace", Before: baselineImage, After: candidateImage,
		})
	}
	return composeAnalysis{
		BaselineSemanticDigest: baselineDigest, CandidateSemanticDigest: candidateDigest,
		Diff: diff, BaselineImage: baselineImage, CandidateImage: candidateImage,
		CandidateImageDigest: candidateImageDigest,
	}, nil
}

func parseComposeDocument(content string) (map[string]any, error) {
	if err := validateComposeContent(content); err != nil {
		return nil, err
	}
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(content), &root); err != nil {
		return nil, fmt.Errorf("Compose YAML 无效: %w", err)
	}
	var value map[string]any
	if len(root.Content) != 1 || root.Content[0].Decode(&value) != nil || value == nil {
		return nil, errors.New("Compose 无法转换为规范语义对象")
	}
	return normalizeComposeValue(value).(map[string]any), nil
}

func parseRenderedComposeDocument(content string) (map[string]any, error) {
	if content == "" || len(content) > maxComposeBytes || strings.ContainsRune(content, '\x00') {
		return nil, errors.New("Compose 有效配置为空、过大或包含非法字符")
	}
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(content), &root); err != nil {
		return nil, fmt.Errorf("Compose 有效配置 YAML 无效: %w", err)
	}
	var value map[string]any
	if len(root.Content) != 1 || root.Content[0].Decode(&value) != nil || value == nil {
		return nil, errors.New("Compose 有效配置无法转换为规范语义对象")
	}
	return normalizeComposeValue(value).(map[string]any), nil
}

func normalizeComposeValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			result[key] = normalizeComposeValue(item)
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = normalizeComposeValue(item)
		}
		return result
	default:
		return typed
	}
}

func fixedComposeProject(
	runtime *model.ComposeServiceRuntime,
	baseline map[string]any,
	candidate map[string]any,
) error {
	for label, document := range map[string]map[string]any{"基线": baseline, "候选": candidate} {
		if raw, exists := document["name"]; exists {
			name, ok := raw.(string)
			if !ok || name != runtime.ProjectName {
				return fmt.Errorf("Compose %s project name 与受控项目 %q 不一致", label, runtime.ProjectName)
			}
		}
	}
	return nil
}

func fixedComposeServices(
	runtime *model.ComposeServiceRuntime,
	document map[string]any,
) (map[string]map[string]any, error) {
	raw, ok := document["services"].(map[string]any)
	if !ok {
		return nil, errors.New("Compose services 语义无效")
	}
	expected := append([]string{runtime.ApplicationService}, runtime.DependencyServices...)
	sort.Strings(expected)
	actual := make([]string, 0, len(raw))
	services := make(map[string]map[string]any, len(raw))
	for name, item := range raw {
		service, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("Compose 服务 %q 语义无效", name)
		}
		if _, hasBuild := service["build"]; hasBuild {
			return nil, fmt.Errorf("Compose 服务 %q 禁止 build", name)
		}
		image, _ := service["image"].(string)
		if _, err := immutableImageDigest(image); err != nil {
			return nil, fmt.Errorf("Compose 服务 %q: %w", name, err)
		}
		actual = append(actual, name)
		services[name] = service
	}
	sort.Strings(actual)
	if !reflect.DeepEqual(actual, expected) {
		return nil, fmt.Errorf("服务集合必须固定为 %s", strings.Join(expected, ", "))
	}
	return services, nil
}

func immutableImageDigest(image string) (string, error) {
	image = strings.TrimSpace(image)
	if !immutableImagePattern.MatchString(image) {
		return "", errors.New("镜像必须使用不可变 repo@sha256:<64hex> 引用")
	}
	index := strings.LastIndex(image, "@")
	return image[index+1:], nil
}

func immutableImageRepository(image string) (string, error) {
	if _, err := immutableImageDigest(image); err != nil {
		return "", err
	}
	repository := strings.SplitN(strings.TrimSpace(image), "@", 2)[0]
	lastSlash := strings.LastIndex(repository, "/")
	if colon := strings.LastIndex(repository, ":"); colon > lastSlash {
		repository = repository[:colon]
	}
	if repository == "" {
		return "", errors.New("镜像仓库为空")
	}
	return repository, nil
}

func cloneComposeMap(input map[string]any) map[string]any {
	result := make(map[string]any, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}

func firstComposeChange(left, right any, path string) string {
	leftMap, leftIsMap := left.(map[string]any)
	rightMap, rightIsMap := right.(map[string]any)
	if leftIsMap && rightIsMap {
		keys := make([]string, 0, len(leftMap)+len(rightMap))
		seen := make(map[string]struct{}, len(leftMap)+len(rightMap))
		for key := range leftMap {
			seen[key] = struct{}{}
		}
		for key := range rightMap {
			seen[key] = struct{}{}
		}
		for key := range seen {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			child := key
			if path != "" {
				child = path + "." + key
			}
			if changed := firstComposeChange(leftMap[key], rightMap[key], child); changed != "" {
				return changed
			}
		}
		return ""
	}
	if !reflect.DeepEqual(left, right) {
		return path
	}
	return ""
}

func composeTenantID(service model.ServiceDefinition, catalogDefault string) string {
	if service.TenantID != "" {
		return service.TenantID
	}
	if catalogDefault != "" {
		return catalogDefault
	}
	return "default"
}

func composeProposalTTL(runtime *model.ComposeServiceRuntime) time.Duration {
	seconds := 900
	if runtime != nil && runtime.ProposalTTLSeconds >= 300 && runtime.ProposalTTLSeconds <= 3600 {
		seconds = runtime.ProposalTTLSeconds
	}
	return time.Duration(seconds) * time.Second
}

func composePolicyDigest(service model.ServiceDefinition, tenantID string) (string, error) {
	if service.Runtime == nil {
		return "", errors.New("Compose 运行策略缺失")
	}
	return canonicalDigest(struct {
		SchemaVersion int                         `json:"schemaVersion"`
		ObjectID      string                      `json:"objectId"`
		TenantID      string                      `json:"tenantId"`
		ServerID      string                      `json:"serverId"`
		Runtime       model.ComposeServiceRuntime `json:"runtime"`
		Recovery      *model.RecoveryPointPolicy  `json:"recoveryPointPolicy"`
		Alerts        model.AlertPolicyDefinition `json:"alertPolicy"`
	}{2, service.ObjectID, tenantID, service.ServerID, *service.Runtime,
		service.RecoveryPointPolicy, service.AlertPolicy})
}

func composeAlertEvidence(checkedAt time.Time, fingerprints []string) (string, error) {
	values := append([]string(nil), fingerprints...)
	sort.Strings(values)
	return canonicalDigest(struct {
		SchemaVersion int       `json:"schemaVersion"`
		CheckedAt     time.Time `json:"checkedAt"`
		Fingerprints  []string  `json:"fingerprints"`
	}{1, checkedAt.UTC(), values})
}
