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
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/config"
	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

type RunnerUpdateLauncher interface {
	Launch(context.Context, config.RunnerUpdatePolicy, model.RunnerUpdate) error
}

type systemdRunnerUpdateLauncher struct{}

func (systemdRunnerUpdateLauncher) Launch(
	ctx context.Context,
	policy config.RunnerUpdatePolicy,
	update model.RunnerUpdate,
) error {
	template := strings.TrimSuffix(policy.UpdaterUnitName, ".service")
	if !strings.HasSuffix(template, "@") {
		return errors.New("Runner updater unit 不是模板 unit")
	}
	unit := template + update.ID + ".service"
	launchContext, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	command := exec.CommandContext(launchContext, "/usr/bin/systemctl", "--no-block", "start", unit)
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("启动 Runner updater 失败: %s", redactText(strings.TrimSpace(string(output))))
	}
	return nil
}

func canonicalRunnerUpdateManifest(
	policy *config.RunnerUpdatePolicy,
	request model.RunnerUpdateRequest,
) model.RunnerUpdateManifest {
	purpose := policy.ManifestPurpose
	if purpose == "" {
		purpose = config.RunnerUpdateManifestPurpose
	}
	schema := policy.ManifestSchema
	if schema == 0 {
		schema = config.RunnerUpdateManifestSchema
	}
	goos := policy.ManifestGOOS
	if goos == "" {
		goos = config.RunnerUpdateManifestGOOS
	}
	goarch := policy.ManifestGOARCH
	if goarch == "" {
		goarch = config.RunnerUpdateManifestGOARCH
	}
	return model.RunnerUpdateManifest{
		Purpose:          purpose,
		Schema:           schema,
		GOOS:             goos,
		GOARCH:           goarch,
		RunnerID:         policy.RunnerID,
		TargetVersion:    request.TargetVersion,
		ArtifactDigest:   request.ArtifactDigest,
		ArtifactRevision: request.ArtifactRevision,
		Publisher:        request.Publisher,
	}
}

func runnerUpdateManifestPayload(
	policy *config.RunnerUpdatePolicy,
	request model.RunnerUpdateRequest,
) ([]byte, error) {
	return json.Marshal(canonicalRunnerUpdateManifest(policy, request))
}

func validateRunnerUpdateManifest(
	policy *config.RunnerUpdatePolicy,
	request model.RunnerUpdateRequest,
) error {
	expected := canonicalRunnerUpdateManifest(policy, request)
	if request.Manifest != expected {
		return errors.New("Runner 更新 manifest 与策略或制品字段不一致")
	}
	if request.ManifestPurpose != expected.Purpose ||
		request.ManifestSchema != expected.Schema ||
		request.ManifestGOOS != expected.GOOS ||
		request.ManifestGOARCH != expected.GOARCH ||
		request.RunnerID != expected.RunnerID {
		return errors.New("Runner 更新扁平 manifest 字段与嵌套 manifest 不一致")
	}
	if policy.ManifestPurpose != "" && policy.ManifestPurpose != config.RunnerUpdateManifestPurpose {
		return errors.New("Runner 更新 manifest purpose 与策略绑定无效")
	}
	if policy.ManifestSchema != 0 && policy.ManifestSchema != config.RunnerUpdateManifestSchema {
		return errors.New("Runner 更新 manifest schema 与策略绑定无效")
	}
	if policy.ManifestGOOS != "" && policy.ManifestGOOS != config.RunnerUpdateManifestGOOS {
		return errors.New("Runner 更新 manifest GOOS 与策略绑定无效")
	}
	if policy.ManifestGOARCH != "" && policy.ManifestGOARCH != config.RunnerUpdateManifestGOARCH {
		return errors.New("Runner 更新 manifest GOARCH 与策略绑定无效")
	}
	return nil
}

func verifyRunnerArtifactSignature(
	policy *config.RunnerUpdatePolicy,
	request model.RunnerUpdateRequest,
) error {
	encodedKey := policy.TrustedPublisherKeys[request.Publisher]
	publicKey, err := base64.StdEncoding.Strict().DecodeString(encodedKey)
	if err != nil || len(publicKey) != ed25519.PublicKeySize {
		return errors.New("Runner 更新发布者公钥无效")
	}
	signature, err := base64.StdEncoding.Strict().DecodeString(request.ArtifactSignature)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return errors.New("Runner 更新签名格式无效")
	}
	payload, err := runnerUpdateManifestPayload(policy, request)
	if err != nil || !ed25519.Verify(ed25519.PublicKey(publicKey), payload, signature) {
		return errors.New("Runner 更新签名校验失败")
	}
	return validateRunnerUpdateManifest(policy, request)
}

func stageRunnerArtifact(
	stateRoot, id, source string,
	limit int64,
) (string, string, error) {
	stagingRoot := filepath.Join(stateRoot, "runner-updates", "staged")
	if err := os.MkdirAll(stagingRoot, 0o700); err != nil {
		return "", "", err
	}
	if err := os.Chmod(stagingRoot, 0o700); err != nil {
		return "", "", err
	}
	if err := requireRootOwnedDirectory(stagingRoot); err != nil {
		return "", "", err
	}
	if err := rejectManagedSymlinks(filepath.Clean(stateRoot), stagingRoot); err != nil {
		return "", "", err
	}
	sourceInfo, err := os.Lstat(source)
	if err != nil || !sourceInfo.Mode().IsRegular() || sourceInfo.Mode()&os.ModeSymlink != 0 {
		return "", "", errors.New("Runner 制品源文件身份无效")
	}
	input, err := os.Open(source)
	if err != nil {
		return "", "", err
	}
	defer input.Close()
	openedInfo, err := input.Stat()
	if err != nil || !os.SameFile(sourceInfo, openedInfo) {
		return "", "", errors.New("Runner 制品源文件发生变化")
	}
	target := filepath.Join(stagingRoot, id+".runner")
	output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o700)
	if err != nil {
		return "", "", err
	}
	hasher := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(output, hasher), io.LimitReader(input, limit+1))
	if copyErr == nil && written > limit {
		copyErr = errors.New("Runner 制品超过大小限制")
	}
	if copyErr == nil {
		copyErr = output.Sync()
	}
	if closeErr := output.Close(); copyErr == nil {
		copyErr = closeErr
	}
	if copyErr != nil {
		_ = os.Remove(target)
		return "", "", copyErr
	}
	if err := syncRunnerUpdateDirectory(stagingRoot); err != nil {
		_ = os.Remove(target)
		return "", "", err
	}
	return target, "sha256:" + hex.EncodeToString(hasher.Sum(nil)), nil
}

func requireRootOwnedDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("Runner 更新目录身份无效")
	}
	if info.Mode().Perm() != 0o700 {
		return errors.New("Runner 更新目录权限无效")
	}
	if os.Geteuid() == 0 {
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != 0 || stat.Gid != 0 {
			return errors.New("Runner 更新目录必须由 root:root 所有")
		}
	}
	return nil
}

func syncRunnerUpdateDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
