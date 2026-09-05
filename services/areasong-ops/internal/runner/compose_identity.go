package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

type composeApplicationIdentity struct {
	ContainerID   string   `json:"containerId"`
	ContainerName string   `json:"containerName"`
	DeclaredImage string   `json:"declaredImage"`
	ImageID       string   `json:"imageId"`
	RepoDigests   []string `json:"repoDigests"`
}

func (engine *Engine) inspectComposeApplication(
	ctx context.Context,
	runtime *model.ComposeServiceRuntime,
	composePath string,
) (composeApplicationIdentity, error) {
	if runtime == nil || runtime.ApplicationService == "" || runtime.ApplicationContainer == "" {
		return composeApplicationIdentity{}, errors.New("Compose 应用身份策略不完整")
	}
	containerOutput, err := engine.runCompose(ctx, *runtime, composePath, "ps", "-q", runtime.ApplicationService)
	if err != nil {
		return composeApplicationIdentity{}, fmt.Errorf("读取 Compose 应用容器失败: %w", err)
	}
	containerIDs := strings.Fields(containerOutput)
	if len(containerIDs) != 1 {
		return composeApplicationIdentity{}, errors.New("Compose 应用服务必须且只能有一个运行容器")
	}
	runner := engine.composeRunner
	if runner == nil {
		runner = systemComposeCommandRunner{executable: "/usr/bin/docker"}
	}
	directory := filepath.Dir(runtime.RuntimeCompose)
	format := `{{.Id}}\t{{.Name}}\t{{.Config.Image}}\t{{.Image}}`
	output, err := runner.Run(ctx, directory, "inspect", "--format", format, containerIDs[0])
	if err != nil {
		return composeApplicationIdentity{}, fmt.Errorf("读取 Compose 容器运行身份失败: %w", err)
	}
	fields := strings.Split(strings.TrimSpace(output), "\t")
	if len(fields) != 4 {
		return composeApplicationIdentity{}, errors.New("Compose 容器运行身份输出无效")
	}
	identity := composeApplicationIdentity{
		ContainerID: strings.TrimSpace(fields[0]), ContainerName: strings.TrimPrefix(strings.TrimSpace(fields[1]), "/"),
		DeclaredImage: strings.TrimSpace(fields[2]), ImageID: strings.TrimSpace(fields[3]),
	}
	if identity.ContainerID != containerIDs[0] || identity.ImageID == "" {
		return composeApplicationIdentity{}, errors.New("Compose 容器 ID 或镜像 ID 不匹配")
	}
	repoOutput, err := runner.Run(ctx, directory, "image", "inspect", "--format", `{{json .RepoDigests}}`, identity.ImageID)
	if err != nil {
		return composeApplicationIdentity{}, fmt.Errorf("读取 Compose 镜像 RepoDigests 失败: %w", err)
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(repoOutput)), &identity.RepoDigests); err != nil || len(identity.RepoDigests) == 0 {
		return composeApplicationIdentity{}, errors.New("Compose 镜像 RepoDigests 无效")
	}
	sort.Strings(identity.RepoDigests)
	return identity, nil
}

func verifyComposeApplicationIdentity(
	identity composeApplicationIdentity,
	runtime *model.ComposeServiceRuntime,
	expectedImage string,
) (string, error) {
	if runtime == nil || identity.ContainerName != runtime.ApplicationContainer {
		return "", errors.New("Compose 应用容器名称与受控身份不一致")
	}
	expectedDigest, err := immutableImageDigest(expectedImage)
	if err != nil {
		return "", err
	}
	declaredDigest, err := immutableImageDigest(identity.DeclaredImage)
	if err != nil || declaredDigest != expectedDigest {
		return "", errors.New("Compose 运行容器声明镜像与批准摘要不一致")
	}
	expectedRepoDigest, err := composeRepoDigestReference(expectedImage)
	if err != nil {
		return "", err
	}
	matched := false
	for _, repoDigest := range identity.RepoDigests {
		if repoDigest == expectedRepoDigest {
			matched = true
			break
		}
	}
	if !matched {
		return "", errors.New("Compose 运行镜像 RepoDigests 与批准摘要不一致")
	}
	return canonicalDigest(identity)
}

func composeRepoDigestReference(image string) (string, error) {
	digest, err := immutableImageDigest(image)
	if err != nil {
		return "", err
	}
	repository, err := immutableImageRepository(image)
	if err != nil {
		return "", err
	}
	return repository + "@" + digest, nil
}

func (engine *Engine) inspectComposeImageReference(
	ctx context.Context,
	runtime *model.ComposeServiceRuntime,
	image string,
) (string, error) {
	if runtime == nil {
		return "", errors.New("Compose 运行策略缺失")
	}
	expectedRepoDigest, err := composeRepoDigestReference(image)
	if err != nil {
		return "", err
	}
	runner := engine.composeRunner
	if runner == nil {
		runner = systemComposeCommandRunner{executable: "/usr/bin/docker"}
	}
	output, err := runner.Run(ctx, filepath.Dir(runtime.RuntimeCompose), "image", "inspect", "--format", `{{.Id}}\t{{json .RepoDigests}}`, image)
	if err != nil {
		return "", fmt.Errorf("候选 Compose 镜像尚未准备或不可验证: %w", err)
	}
	fields := strings.Split(strings.TrimSpace(output), "\t")
	if len(fields) != 2 || fields[0] == "" {
		return "", errors.New("候选 Compose 镜像身份输出无效")
	}
	var repoDigests []string
	if err := json.Unmarshal([]byte(fields[1]), &repoDigests); err != nil {
		return "", errors.New("候选 Compose 镜像 RepoDigests 无效")
	}
	for _, repoDigest := range repoDigests {
		if repoDigest == expectedRepoDigest {
			return fields[0], nil
		}
	}
	return "", errors.New("候选 Compose 镜像 RepoDigests 与批准摘要不一致")
}
