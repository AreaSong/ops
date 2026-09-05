package runner

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

var extensionNamePattern = regexp.MustCompile(`^[a-z][a-z0-9._-]{1,63}$`)
var extensionDigestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

func (engine *Engine) UploadExtension(
	ctx context.Context,
	actor string,
	request model.ExtensionUploadRequest,
) (model.ExtensionUploadResult, bool, error) {
	if err := engine.validateExtensionRequest(ctx, actor, request); err != nil {
		return model.ExtensionUploadResult{}, false, err
	}
	content, err := decodeExtensionContent(request.Content, engine.catalog.Extensions.MaxPackageBytes)
	if err != nil {
		return model.ExtensionUploadResult{}, false, err
	}
	if digestText(string(content)) != request.Manifest.Digest {
		return model.ExtensionUploadResult{}, false, errors.New("扩展包摘要不匹配")
	}
	if err := engine.verifyExtensionSignature(request.Manifest); err != nil {
		return model.ExtensionUploadResult{}, false, err
	}
	requestDigest := extensionRequestDigest(actor, request.Manifest, request.Content)
	result := model.ExtensionUploadResult{
		Manifest: request.Manifest, Stored: false, State: "staging",
		StorageDigest: request.Manifest.Digest, IdempotencyKey: request.IdempotencyKey,
		CreatedAt: time.Now().UTC(),
	}
	storagePath, err := engine.extensionStoragePath(request.Manifest)
	if err != nil {
		return model.ExtensionUploadResult{}, false, err
	}
	reserved, created, err := engine.store.ReserveExtensionPackage(
		ctx, result, actor, requestDigest, storagePath,
	)
	if err != nil || !created {
		if err == nil && !created {
			switch reserved.State {
			case "stored":
				reserved.Stored = true
				return reserved, false, nil
			case "failed":
				return reserved, false, errors.New("扩展包此前暂存失败，已进入人工关注")
			case "staging":
				// A process crash can leave a durable reservation behind. Resume only
				// when the controlled file is complete and its digest still matches.
				if data, readErr := os.ReadFile(storagePath); readErr == nil && digestText(string(data)) == request.Manifest.Digest {
					if markErr := engine.store.MarkExtensionStored(ctx, request.Manifest.ID, request.Manifest.Version); markErr == nil {
						reserved.Stored, reserved.State = true, "stored"
						return reserved, false, nil
					}
				}
				failure := errors.New("扩展包暂存状态不完整，已进入人工关注")
				return reserved, false, errors.Join(failure,
					engine.failExtension(ctx, request.Manifest, storagePath, "暂存状态无法恢复"))
			}
		}
		return reserved, created, err
	}
	if err := writeExtensionPackage(storagePath, content); err != nil {
		return result, true, errors.Join(fmt.Errorf("扩展包暂存失败: %w", err),
			engine.failExtension(ctx, request.Manifest, storagePath, err.Error()))
	}
	if err := engine.store.MarkExtensionStored(ctx, request.Manifest.ID, request.Manifest.Version); err != nil {
		return result, true, errors.Join(err,
			engine.failExtension(ctx, request.Manifest, storagePath, err.Error()))
	}
	result.Stored = true
	result.State = "stored"
	return result, true, nil
}

func (engine *Engine) validateExtensionRequest(
	ctx context.Context,
	actor string,
	request model.ExtensionUploadRequest,
) error {
	policy := engine.catalog.Extensions
	if policy == nil || !policy.Enabled {
		return errors.New("扩展上传尚未启用")
	}
	if !uuidPattern.MatchString(request.IdempotencyKey) {
		return errors.New("扩展上传幂等键无效")
	}
	manifest := request.Manifest
	if manifest.Purpose != model.ExtensionManifestPurpose || manifest.SchemaVersion != model.ExtensionManifestSchema {
		return errors.New("扩展 manifest 用途或 Schema 版本无效")
	}
	if !extensionNamePattern.MatchString(manifest.ID) || !extensionNamePattern.MatchString(manifest.Version) {
		return errors.New("扩展 ID 或版本格式无效")
	}
	if manifest.Type != "script" && manifest.Type != "wasm" && manifest.Type != "plugin" {
		return errors.New("扩展类型必须是 script、wasm 或 plugin")
	}
	if !extensionDigestPattern.MatchString(manifest.Digest) {
		return errors.New("扩展摘要格式无效")
	}
	if err := validateRelativeEntrypoint(manifest.Entrypoint); err != nil {
		return err
	}
	if !contains(policy.TrustedPublishers, manifest.Publisher) {
		return errors.New("扩展发布者不在受信白名单")
	}
	if err := engine.authorizePlatform(ctx, actor, model.PermissionManageConfig, "extensions"); err != nil {
		return err
	}
	return nil
}

func decodeExtensionContent(encoded string, limit int64) ([]byte, error) {
	if encoded == "" || int64(len(encoded)) > ((limit+2)/3)*4+4 {
		return nil, errors.New("扩展包为空或超过大小限制")
	}
	content, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(content) == 0 || int64(len(content)) > limit {
		return nil, errors.New("扩展包 Base64 或大小无效")
	}
	return content, nil
}

func validateRelativeEntrypoint(value string) error {
	if value == "" || filepath.IsAbs(value) || strings.ContainsRune(value, '\x00') {
		return errors.New("扩展入口路径无效")
	}
	cleaned := filepath.Clean(filepath.FromSlash(value))
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return errors.New("扩展入口路径越过包目录")
	}
	return nil
}

func (engine *Engine) verifyExtensionSignature(manifest model.ExtensionManifest) error {
	policy := engine.catalog.Extensions
	if policy == nil || !contains(policy.TrustedPublishers, manifest.Publisher) {
		return errors.New("扩展发布者已被撤销或不在受信白名单")
	}
	if !policy.RequireSignature {
		return nil
	}
	encodedKey := policy.TrustedPublisherKeys[manifest.Publisher]
	publicKey, err := base64.StdEncoding.Strict().DecodeString(encodedKey)
	if err != nil || len(publicKey) != ed25519.PublicKeySize {
		return errors.New("扩展发布者公钥无效")
	}
	signature, err := base64.StdEncoding.Strict().DecodeString(manifest.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return errors.New("扩展签名格式无效")
	}
	payload, err := extensionSigningPayload(manifest)
	if err != nil || !ed25519.Verify(ed25519.PublicKey(publicKey), payload, signature) {
		return errors.New("扩展签名校验失败")
	}
	return nil
}

func extensionSigningPayload(manifest model.ExtensionManifest) ([]byte, error) {
	type signedManifest struct {
		Purpose        string   `json:"purpose"`
		SchemaVersion  int      `json:"schemaVersion"`
		ID             string   `json:"id"`
		Version        string   `json:"version"`
		Type           string   `json:"type"`
		Entrypoint     string   `json:"entrypoint"`
		Digest         string   `json:"digest"`
		Permissions    []string `json:"permissions,omitempty"`
		AllowedObjects []string `json:"allowedObjects,omitempty"`
		Publisher      string   `json:"publisher"`
	}
	return json.Marshal(signedManifest{
		Purpose: manifest.Purpose, SchemaVersion: manifest.SchemaVersion,
		ID: manifest.ID, Version: manifest.Version, Type: manifest.Type,
		Entrypoint: manifest.Entrypoint, Digest: manifest.Digest,
		Permissions: manifest.Permissions, AllowedObjects: manifest.AllowedObjects,
		Publisher: manifest.Publisher,
	})
}

func extensionRequestDigest(
	actor string,
	manifest model.ExtensionManifest,
	encodedContent string,
) string {
	payload, _ := extensionSigningPayload(manifest)
	return digestText(strings.Join([]string{actor, string(payload), encodedContent}, "\x00"))
}

func (engine *Engine) extensionStoragePath(manifest model.ExtensionManifest) (string, error) {
	root := filepath.Join(engine.stateRoot, "extensions")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", err
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return "", err
	}
	directory := filepath.Join(root, manifest.ID, manifest.Version)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", err
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(directory, manifest.Digest[len("sha256:"):]+".package"), nil
}

func writeExtensionPackage(path string, content []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err = file.Write(content); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		_ = os.Remove(path)
		return err
	}
	return closeErr
}

func (engine *Engine) failExtension(
	ctx context.Context,
	manifest model.ExtensionManifest,
	storagePath, reason string,
) error {
	var result error
	if err := engine.store.MarkExtensionFailed(ctx, manifest.ID, manifest.Version, reason); err != nil {
		result = fmt.Errorf("扩展失败状态收口失败: %w", err)
	}
	if info, err := os.Lstat(storagePath); err == nil && info.Mode().IsRegular() {
		if removeErr := os.Remove(storagePath); removeErr != nil {
			result = errors.Join(result, fmt.Errorf("删除不完整扩展包失败: %w", removeErr))
		}
	}
	return result
}
