package runner

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

type kubernetesMutationInput struct {
	action   string
	manifest string
}

func kubernetesMutationInputs(request model.KubernetesRequest) ([]kubernetesMutationInput, error) {
	if len(request.ExpectedResources) == 0 {
		return []kubernetesMutationInput{{"apply", request.Manifest}}, nil
	}
	guarded, err := guardedKubernetesManifest(request.Manifest, request.ExpectedResources)
	if err != nil {
		return nil, err
	}
	var creates, updates []string
	for _, document := range strings.Split(guarded, "\n---\n") {
		var object struct {
			Metadata struct {
				UID string `json:"uid"`
			} `json:"metadata"`
		}
		if err := json.Unmarshal([]byte(document), &object); err != nil {
			return nil, err
		}
		if object.Metadata.UID == "" {
			creates = append(creates, document)
		} else {
			updates = append(updates, document)
		}
	}
	var inputs []kubernetesMutationInput
	// 缺席前置条件必须用 create-only 落实；不能用 apply 覆盖并发出现的同名对象。
	if len(creates) > 0 {
		inputs = append(inputs, kubernetesMutationInput{"create", strings.Join(creates, "\n---\n")})
	}
	if len(updates) > 0 {
		inputs = append(inputs, kubernetesMutationInput{"apply", strings.Join(updates, "\n---\n")})
	}
	return inputs, nil
}

func runKubernetesMutation(ctx context.Context, target model.KubernetesTarget, request model.KubernetesRequest) (string, error) {
	inputs, err := kubernetesMutationInputs(request)
	if err != nil {
		return "", err
	}
	output := newCappedBuffer(kubernetesPreviewMaxBytes)
	for _, input := range inputs {
		args := []string{"--context", target.Context, "-n", target.Namespace, input.action, "--field-manager", "areasong-ops"}
		if input.action == "apply" {
			args = append(args, "--server-side")
		}
		if request.Action == "validate" {
			args = append(args, "--dry-run=server")
		}
		args = append(args, "-f", "-")
		command := exec.CommandContext(ctx, "kubectl", args...)
		command.Stdin = strings.NewReader(input.manifest)
		command.Stdout, command.Stderr = output, output
		command.WaitDelay = time.Second
		if err := command.Run(); err != nil {
			return output.buffer.String(), err
		}
		if output.truncated {
			return output.buffer.String(), errors.New("Kubernetes 执行输出超限，需要核对结果")
		}
	}
	return output.buffer.String(), nil
}
