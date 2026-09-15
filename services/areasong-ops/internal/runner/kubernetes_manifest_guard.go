package runner

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	"gopkg.in/yaml.v3"
)

func guardedKubernetesManifest(manifest string, identities []model.KubernetesResourceIdentity) (string, error) {
	decoder := yaml.NewDecoder(strings.NewReader(manifest))
	var documents []map[string]any
	for {
		var document map[string]any
		err := decoder.Decode(&document)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", err
		}
		if len(document) == 0 {
			continue
		}
		objects, err := flattenKubernetesData(document)
		if err != nil {
			return "", err
		}
		documents = append(documents, objects...)
	}
	if len(documents) != len(identities) {
		return "", errors.New("执行清单与预览对象数量不一致")
	}
	var output bytes.Buffer
	for _, document := range documents {
		if err := bindKubernetesIdentity(document, identities); err != nil {
			return "", err
		}
		data, err := json.Marshal(document)
		if err != nil {
			return "", err
		}
		if output.Len() > 0 {
			output.WriteString("\n---\n")
		}
		output.Write(data)
	}
	return output.String(), nil
}

func flattenKubernetesData(document map[string]any) ([]map[string]any, error) {
	kind, _ := document["kind"].(string)
	if !strings.HasSuffix(strings.ToLower(kind), "list") {
		return []map[string]any{document}, nil
	}
	items, ok := document["items"].([]any)
	if !ok {
		return nil, errors.New("Kubernetes List 内容无效")
	}
	var result []map[string]any
	for _, item := range items {
		value, ok := item.(map[string]any)
		if !ok {
			return nil, errors.New("Kubernetes List 项无效")
		}
		nested, err := flattenKubernetesData(value)
		if err != nil {
			return nil, err
		}
		result = append(result, nested...)
	}
	return result, nil
}

func bindKubernetesIdentity(document map[string]any, identities []model.KubernetesResourceIdentity) error {
	metadata, ok := document["metadata"].(map[string]any)
	if !ok {
		return errors.New("Kubernetes metadata 无效")
	}
	for _, identity := range identities {
		if document["apiVersion"] != identity.APIVersion || document["kind"] != identity.Kind || metadata["name"] != identity.Name {
			continue
		}
		metadata["namespace"] = identity.Namespace
		if identity.Exists {
			metadata["uid"], metadata["resourceVersion"] = identity.UID, identity.ResourceVersion
		}
		return nil
	}
	return errors.New("执行清单包含预览之外的对象")
}
