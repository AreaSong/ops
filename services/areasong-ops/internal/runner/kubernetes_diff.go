package runner

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	"gopkg.in/yaml.v3"
)

type kubernetesDiffPair struct {
	Before map[string]any
	After  map[string]any
}

// diff 固定输出完整上下文；只有重建完整对象后才能正确区分配置字符串与结构字段。
func parseKubernetesDiff(raw string) ([]kubernetesDiffPair, error) {
	var before, after strings.Builder
	var pairs []kubernetesDiffPair
	active := false
	flush := func() error {
		if !active {
			return nil
		}
		pair := kubernetesDiffPair{}
		if before.Len() > 0 {
			if err := yaml.Unmarshal([]byte(before.String()), &pair.Before); err != nil {
				return errors.New("Kubernetes diff 原对象格式无效")
			}
		}
		if err := yaml.Unmarshal([]byte(after.String()), &pair.After); err != nil || len(pair.After) == 0 {
			return errors.New("Kubernetes diff 新对象格式无效")
		}
		pairs = append(pairs, pair)
		before.Reset()
		after.Reset()
		active = false
		return nil
	}
	for _, line := range strings.Split(raw, "\n") {
		switch {
		case strings.HasPrefix(line, "diff "):
			if err := flush(); err != nil {
				return nil, err
			}
		case strings.HasPrefix(line, "--- "), strings.HasPrefix(line, "+++ "):
			continue
		case strings.HasPrefix(line, "@@ "):
			if active {
				return nil, errors.New("Kubernetes diff 缺少完整上下文")
			}
			var left, right string
			if _, err := fmt.Sscanf(line, "@@ -%s +%s @@", &left, &right); err != nil {
				return nil, errors.New("Kubernetes diff 范围无效")
			}
			leftStart, _, _ := strings.Cut(left, ",")
			rightStart, _, _ := strings.Cut(right, ",")
			if (leftStart != "0" && leftStart != "1") || rightStart != "1" {
				return nil, errors.New("Kubernetes diff 不是完整对象")
			}
			active = true
		case active && strings.HasPrefix(line, "+"):
			after.WriteString(line[1:] + "\n")
		case active && strings.HasPrefix(line, "-"):
			before.WriteString(line[1:] + "\n")
		case active && strings.HasPrefix(line, " "):
			before.WriteString(line[1:] + "\n")
			after.WriteString(line[1:] + "\n")
		case strings.HasPrefix(line, "\\ No newline"), line == "":
			continue
		default:
			return nil, errors.New("Kubernetes diff 包含不可解释的输出")
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return pairs, nil
}

func normalizeKubernetesObject(object map[string]any) {
	delete(object, "status")
	if metadata, ok := object["metadata"].(map[string]any); ok {
		for _, key := range []string{"uid", "resourceVersion", "managedFields", "creationTimestamp", "generation"} {
			delete(metadata, key)
		}
	}
}

var kubernetesVisibleFields = map[string]bool{}

func init() {
	for _, field := range strings.Fields("spec replicas strategy type rollingUpdate maxSurge maxUnavailable template metadata selector matchLabels matchExpressions containers initContainers name image imagePullPolicy ports containerPort hostPort protocol targetPort port readinessProbe livenessProbe startupProbe httpGet path initialDelaySeconds periodSeconds timeoutSeconds failureThreshold successThreshold terminationGracePeriodSeconds resources limits requests cpu memory storage serviceAccountName env envFrom value valueFrom configMapKeyRef secretKeyRef key optional volumes volumeMounts mountPath readOnly emptyDir medium sizeLimit persistentVolumeClaim claimName accessModes storageClassName volumeMode jobTemplate backoffLimit completions parallelism schedule suspend securityContext runAsUser runAsGroup fsGroup runAsNonRoot capabilities add drop allowPrivilegeEscalation privileged readOnlyRootFilesystem data binaryData stringData annotations labels command args") {
		kubernetesVisibleFields[field] = true
	}
}

func opaqueKubernetesValue(value any) string {
	digest, _ := canonicalDigest(value)
	return "<内容摘要 " + digest + ">"
}

func sanitizeKubernetesValue(value any, field string) any {
	switch field {
	case "data", "binaryData", "stringData", "annotations", "labels", "matchLabels", "command", "args", "value":
		return opaqueKubernetesValue(value)
	}
	switch typed := value.(type) {
	case map[string]any:
		result := map[string]any{}
		for key, child := range typed {
			if !kubernetesVisibleFields[key] {
				result["隐藏字段 "+digestText(key)] = opaqueKubernetesValue(child)
				continue
			}
			result[key] = sanitizeKubernetesValue(child, key)
		}
		return result
	case []any:
		result := make([]any, 0, len(typed))
		for _, child := range typed {
			result = append(result, sanitizeKubernetesValue(child, field))
		}
		return result
	case string:
		if field == "image" && strings.Contains(typed, "@sha256:") {
			return redactText(typed)
		}
		return opaqueKubernetesValue(typed)
	default:
		return typed
	}
}

func sanitizedKubernetesObject(object map[string]any) map[string]any {
	if len(object) == 0 {
		return map[string]any{}
	}
	result := map[string]any{"apiVersion": object["apiVersion"], "kind": object["kind"]}
	if meta, ok := object["metadata"].(map[string]any); ok {
		visible := map[string]any{"name": meta["name"], "namespace": meta["namespace"]}
		for key, value := range meta {
			if key == "name" || key == "namespace" {
				continue
			}
			label := "隐藏字段 " + digestText(key)
			if key == "labels" || key == "annotations" || key == "ownerReferences" || key == "finalizers" {
				label = key
			}
			visible[label] = opaqueKubernetesValue(value)
		}
		result["metadata"] = visible
	}
	kind, _ := object["kind"].(string)
	known := strings.Contains("|Deployment|StatefulSet|DaemonSet|Pod|Service|Job|CronJob|ConfigMap|PersistentVolumeClaim|", "|"+kind+"|")
	for key, value := range object {
		if key == "metadata" || key == "kind" || key == "apiVersion" {
			continue
		}
		if known && kubernetesVisibleFields[key] {
			result[key] = sanitizeKubernetesValue(value, key)
		} else {
			result["隐藏字段 "+digestText(key)] = opaqueKubernetesValue(value)
		}
	}
	return result
}

func kubernetesDiffEvidence(raw string, identities []model.KubernetesResourceIdentity) (string, string, error) {
	pairs, err := parseKubernetesDiff(raw)
	if err != nil {
		return "", "", err
	}
	var display strings.Builder
	for _, pair := range pairs {
		matched := false
		meta, _ := pair.After["metadata"].(map[string]any)
		for _, identity := range identities {
			matched = matched || pair.After["apiVersion"] == identity.APIVersion && pair.After["kind"] == identity.Kind &&
				meta["name"] == identity.Name && meta["namespace"] == identity.Namespace
		}
		if !matched {
			return "", "", errors.New("Kubernetes diff 包含预览范围之外的对象")
		}
		normalizeKubernetesObject(pair.Before)
		normalizeKubernetesObject(pair.After)
		left, _ := json.MarshalIndent(sanitizedKubernetesObject(pair.Before), "", "  ")
		right, _ := json.MarshalIndent(sanitizedKubernetesObject(pair.After), "", "  ")
		if bytes.Equal(left, right) {
			continue
		}
		fmt.Fprintf(&display, "--- %s/%s\n+++ %s/%s\n", meta["namespace"], meta["name"], meta["namespace"], meta["name"])
		for _, line := range strings.Split(string(left), "\n") {
			display.WriteString("-" + line + "\n")
		}
		for _, line := range strings.Split(string(right), "\n") {
			display.WriteString("+" + line + "\n")
		}
	}
	if display.Len() > kubernetesPreviewMaxBytes {
		return "", "", errors.New("安全差异展示过大，请拆分计划")
	}
	digest, err := canonicalDigest(pairs)
	return display.String(), digest, err
}
