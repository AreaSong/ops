package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

type composeEffectiveEvidence struct {
	BaselineDigest  string
	CandidateDigest string
	EnvFileDigest   string
}

func verifyComposeEffectiveEvidence(
	evidence composeEffectiveEvidence,
	revision model.ComposeRevision,
) error {
	if evidence.BaselineDigest == "" || evidence.CandidateDigest == "" || evidence.EnvFileDigest == "" ||
		evidence.BaselineDigest != revision.BaselineEffectiveDigest ||
		evidence.CandidateDigest != revision.CandidateEffectiveDigest ||
		evidence.EnvFileDigest != revision.EnvFileDigest {
		return errors.New("Compose env 或有效配置已经漂移，请重新创建提案")
	}
	return nil
}

func (engine *Engine) verifyComposeEffectiveContract(
	ctx context.Context,
	runtime *model.ComposeServiceRuntime,
	baselineContent string,
	candidateContent string,
	revision model.ComposeRevision,
) error {
	evidence, err := inspectComposeEffectiveEvidence(
		ctx, runtime, baselineContent, candidateContent, engine.composeRunner,
	)
	if err != nil {
		return err
	}
	return verifyComposeEffectiveEvidence(evidence, revision)
}

// inspectComposeEffectiveEvidence binds the operator-owned env file and the
// fully rendered Compose model. Rendered output may contain secrets and is
// therefore never returned from this function or persisted.
func inspectComposeEffectiveEvidence(
	ctx context.Context,
	runtime *model.ComposeServiceRuntime,
	baselineContent string,
	candidateContent string,
	runner ComposeCommandRunner,
) (composeEffectiveEvidence, error) {
	if runtime == nil || runtime.EnvFile == "" || runtime.RuntimeCompose == "" || runtime.ProjectName == "" {
		return composeEffectiveEvidence{}, errors.New("Compose 有效配置策略不完整")
	}
	envBefore, err := readComposeFile(runtime.EnvFile)
	if err != nil {
		return composeEffectiveEvidence{}, fmt.Errorf("读取 Compose env 文件失败: %w", err)
	}
	if strings.ContainsRune(envBefore.Content, '\x00') {
		return composeEffectiveEvidence{}, errors.New("Compose env 文件包含非法字符")
	}
	envSnapshot, err := writeComposeTemporary(filepath.Dir(runtime.RuntimeCompose), ".areasong-ops-env-*", envBefore.Content)
	if err != nil {
		return composeEffectiveEvidence{}, fmt.Errorf("创建 Compose env 快照失败: %w", err)
	}
	defer os.Remove(envSnapshot)

	first, err := renderComposePair(ctx, runtime, baselineContent, candidateContent, envSnapshot, runner)
	if err != nil {
		return composeEffectiveEvidence{}, err
	}
	second, err := renderComposePair(ctx, runtime, baselineContent, candidateContent, envSnapshot, runner)
	if err != nil {
		return composeEffectiveEvidence{}, err
	}
	envAfter, err := readComposeFile(runtime.EnvFile)
	if err != nil || !sameComposeIdentity(envBefore.Info, envAfter.Info) || envBefore.Digest != envAfter.Digest {
		return composeEffectiveEvidence{}, errors.New("Compose env 文件在有效配置校验期间发生变化")
	}
	if first.BaselineSemanticDigest != second.BaselineSemanticDigest ||
		first.CandidateSemanticDigest != second.CandidateSemanticDigest ||
		first.BaselineImage != second.BaselineImage || first.CandidateImage != second.CandidateImage ||
		!reflect.DeepEqual(first.Diff, second.Diff) {
		return composeEffectiveEvidence{}, errors.New("Compose 引用配置在有效配置校验期间发生变化")
	}
	return composeEffectiveEvidence{
		BaselineDigest: first.BaselineSemanticDigest, CandidateDigest: first.CandidateSemanticDigest,
		EnvFileDigest: envBefore.Digest,
	}, nil
}

func renderComposePair(
	ctx context.Context,
	runtime *model.ComposeServiceRuntime,
	baselineContent string,
	candidateContent string,
	envSnapshot string,
	runner ComposeCommandRunner,
) (composeAnalysis, error) {
	baseline, err := renderComposeConfig(ctx, runtime, baselineContent, envSnapshot, runner)
	if err != nil {
		return composeAnalysis{}, fmt.Errorf("展开 Compose 基线失败: %w", err)
	}
	candidate, err := renderComposeConfig(ctx, runtime, candidateContent, envSnapshot, runner)
	if err != nil {
		return composeAnalysis{}, fmt.Errorf("展开 Compose 候选失败: %w", err)
	}
	return analyzeRenderedComposeChange(runtime, baseline, candidate)
}

func renderComposeConfig(
	ctx context.Context,
	runtime *model.ComposeServiceRuntime,
	content string,
	envSnapshot string,
	runner ComposeCommandRunner,
) (string, error) {
	if runner == nil {
		runner = systemComposeCommandRunner{executable: "/usr/bin/docker"}
	}
	directory := filepath.Dir(runtime.RuntimeCompose)
	candidatePath, err := writeComposeTemporary(directory, ".areasong-ops-effective-*", content)
	if err != nil {
		return "", err
	}
	defer os.Remove(candidatePath)
	args := []string{"compose", "--project-name", runtime.ProjectName,
		"--project-directory", directory, "--env-file", envSnapshot,
		"-f", candidatePath, "config"}
	output, err := runner.Run(ctx, directory, args...)
	if err != nil {
		return "", fmt.Errorf("docker compose config 失败: %w (%s)", err, redactText(output))
	}
	if output == "" || len(output) > maxComposeBytes {
		return "", errors.New("docker compose config 输出为空或过大")
	}
	return output, nil
}

func writeComposeTemporary(directory, pattern, content string) (string, error) {
	file, err := os.CreateTemp(directory, pattern)
	if err != nil {
		return "", err
	}
	path := file.Name()
	ok := false
	defer func() {
		_ = file.Close()
		if !ok {
			_ = os.Remove(path)
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		return "", err
	}
	if _, err := file.WriteString(content); err != nil {
		return "", err
	}
	if err := file.Sync(); err != nil {
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	ok = true
	return path, nil
}
