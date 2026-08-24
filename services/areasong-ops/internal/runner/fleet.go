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
	"sort"
	"strings"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

// HeartbeatPayloadVersion identifies the signed wire representation. It is
// deliberately independent from RunnerHeartbeatRequest.Version, which is the
// software version reported by the Runner.
const HeartbeatPayloadVersion = 1

type RunnerHeartbeatRequest struct {
	RunnerID       string            `json:"runnerId"`
	Version        string            `json:"version"`
	PayloadVersion int               `json:"payloadVersion,omitempty"`
	Capabilities   []string          `json:"capabilities,omitempty"`
	Labels         map[string]string `json:"labels,omitempty"`
	Timestamp      string            `json:"timestamp,omitempty"`
	Nonce          string            `json:"nonce,omitempty"`
	Signature      string            `json:"signature,omitempty"`
}

// canonicalHeartbeatPayload is the only representation covered by the
// Runner's Ed25519 signature. Keep field order explicit and avoid signing the
// signature field itself. Slices are copied and sorted because capabilities
// are a set from the protocol's perspective.
type canonicalHeartbeatPayload struct {
	PayloadVersion int               `json:"payloadVersion"`
	RunnerID       string            `json:"runnerId"`
	Version        string            `json:"version"`
	Timestamp      string            `json:"timestamp"`
	Nonce          string            `json:"nonce"`
	Capabilities   []string          `json:"capabilities"`
	Labels         map[string]string `json:"labels"`
}

func (input RunnerHeartbeatRequest) CanonicalPayload() ([]byte, error) {
	version := input.PayloadVersion
	if version == 0 {
		version = HeartbeatPayloadVersion
	}
	if version != HeartbeatPayloadVersion {
		return nil, fmt.Errorf("不支持的 Runner 心跳 payload 版本: %d", version)
	}
	if strings.TrimSpace(input.RunnerID) == "" || strings.TrimSpace(input.Version) == "" {
		return nil, errors.New("Runner 心跳缺少身份或软件版本")
	}
	if err := model.ValidateCapabilities(input.Capabilities); err != nil {
		return nil, err
	}
	if err := model.ValidateLabels(input.Labels); err != nil {
		return nil, err
	}
	timestamp, err := canonicalHeartbeatTimestamp(input.Timestamp)
	if err != nil {
		return nil, err
	}
	if err := validateHeartbeatNonce(input.Nonce); err != nil {
		return nil, err
	}
	capabilities := append([]string(nil), input.Capabilities...)
	sort.Strings(capabilities)
	if capabilities == nil {
		capabilities = []string{}
	}
	labels := make(map[string]string, len(input.Labels))
	for key, value := range input.Labels {
		labels[key] = value
	}
	if labels == nil {
		labels = map[string]string{}
	}
	return json.Marshal(canonicalHeartbeatPayload{
		PayloadVersion: version,
		RunnerID:       input.RunnerID,
		Version:        input.Version,
		Timestamp:      timestamp,
		Nonce:          input.Nonce,
		Capabilities:   capabilities,
		Labels:         labels,
	})
}

// CanonicalHeartbeatPayload is an exported function for Runner implementations
// outside this package and for protocol conformance tests.
func CanonicalHeartbeatPayload(input RunnerHeartbeatRequest) ([]byte, error) {
	return input.CanonicalPayload()
}

func SignHeartbeatPayload(privateKey ed25519.PrivateKey, input RunnerHeartbeatRequest) (string, error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return "", errors.New("Runner 心跳签名私钥长度无效")
	}
	payload, err := input.CanonicalPayload()
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, payload)), nil
}

func VerifyHeartbeatPayload(publicKey ed25519.PublicKey, input RunnerHeartbeatRequest) (bool, error) {
	if len(publicKey) != ed25519.PublicKeySize {
		return false, errors.New("Runner 心跳公钥长度无效")
	}
	signature, err := decodeHeartbeatSignature(input.Signature)
	if err != nil {
		return false, err
	}
	payload, err := input.CanonicalPayload()
	if err != nil {
		return false, err
	}
	return ed25519.Verify(publicKey, payload, signature), nil
}

// RunnerHeartbeatSigningPayload is kept as a discoverable alias for clients
// that prefer a verb-oriented protocol helper name.
func RunnerHeartbeatSigningPayload(input RunnerHeartbeatRequest) ([]byte, error) {
	return input.CanonicalPayload()
}

func decodeHeartbeatSignature(encoded string) ([]byte, error) {
	encoded = strings.TrimSpace(encoded)
	if encoded == "" {
		return nil, errors.New("Runner 心跳缺少签名")
	}
	for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		value, err := encoding.DecodeString(encoded)
		if err == nil && len(value) == ed25519.SignatureSize {
			return value, nil
		}
	}
	return nil, errors.New("Runner 心跳签名编码或长度无效")
}

func canonicalHeartbeatTimestamp(value string) (string, error) {
	parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if err != nil {
		return "", errors.New("Runner 心跳时间戳格式无效")
	}
	return parsed.UTC().Format(time.RFC3339Nano), nil
}

func validateHeartbeatNonce(value string) error {
	if len(value) == 0 || len(value) > 128 || strings.ContainsAny(value, " \t\r\n") {
		return errors.New("Runner 心跳 nonce 无效")
	}
	return nil
}

func HeartbeatPayloadDigest(input RunnerHeartbeatRequest) (string, error) {
	payload, err := input.CanonicalPayload()
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func (engine *Engine) Fleet(ctx context.Context) (model.Fleet, error) {
	if engine.catalog.Fleet == nil || !engine.catalog.Fleet.Enabled {
		return model.Fleet{}, errors.New("多服务器管理尚未启用")
	}
	return engine.store.ListFleet(ctx)
}

func (engine *Engine) RegisterServer(ctx context.Context, actor string, node model.ServerNode) error {
	if err := engine.authorizePlatform(ctx, actor, model.PermissionManageFleet, "fleet:"+node.ID); err != nil {
		return err
	}
	if engine.catalog.Fleet == nil || !engine.catalog.Fleet.Enabled {
		return errors.New("多服务器管理尚未启用")
	}
	if err := engine.store.UpsertServerNode(ctx, node); err != nil {
		return err
	}
	_, _ = engine.store.AppendAudit(ctx, model.AuditEntry{ActorHash: actor, Event: "fleet.server_registered", Resource: node.ID, Outcome: "accepted", Detail: map[string]any{"hostname": node.Hostname, "state": node.State}})
	return nil
}

func (engine *Engine) RegisterRunner(ctx context.Context, actor string, node model.RunnerNode) error {
	if err := engine.authorizePlatform(ctx, actor, model.PermissionManageFleet, "runner:"+node.ID); err != nil {
		return err
	}
	if engine.catalog.Fleet == nil || !engine.catalog.Fleet.Enabled {
		return errors.New("多服务器管理尚未启用")
	}
	if err := engine.store.UpsertRunnerNode(ctx, node, "default"); err != nil {
		return err
	}
	_, _ = engine.store.AppendAudit(ctx, model.AuditEntry{ActorHash: actor, Event: "fleet.runner_registered", Resource: node.ID, Outcome: "accepted", Detail: map[string]any{"serverId": node.ServerID, "version": node.Version}})
	return nil
}

func (engine *Engine) HeartbeatRunner(ctx context.Context, actor, id string, input RunnerHeartbeatRequest) (model.RunnerNode, error) {
	if err := engine.authorize(ctx, actor, model.PermissionManageFleet, "runner:"+id); err != nil {
		return model.RunnerNode{}, err
	}
	if engine.catalog.Fleet == nil || !engine.catalog.Fleet.Enabled {
		return model.RunnerNode{}, errors.New("多服务器管理尚未启用")
	}
	lease := time.Duration(engine.catalog.Fleet.HeartbeatTimeoutSeconds) * time.Second
	if lease <= 0 {
		lease = 90 * time.Second
	}
	node, found, err := engine.store.GetRunnerNode(ctx, id)
	if err != nil {
		return model.RunnerNode{}, err
	}
	if !found {
		return model.RunnerNode{}, errors.New("Runner 未登记")
	}
	if len(input.Capabilities) > 0 || len(input.Labels) > 0 {
		node.Capabilities, node.Labels, node.Version = input.Capabilities, input.Labels, input.Version
		now, expires := time.Now().UTC(), time.Now().UTC().Add(lease)
		node.LastHeartbeat, node.LeaseExpiresAt, node.State = &now, &expires, model.NodeOnline
		if err := engine.store.UpsertRunnerNode(ctx, node, "default"); err != nil {
			return model.RunnerNode{}, err
		}
	}
	return engine.store.HeartbeatRunner(ctx, id, input.Version, lease)
}

// HeartbeatRunnerRequest is the protocol-facing alias used by callers that
// need to make the authenticated path explicit in code review.
func (engine *Engine) HeartbeatRunnerRequest(ctx context.Context, id string, input RunnerHeartbeatRequest, payloadDigest string) (model.RunnerNode, error) {
	return engine.HeartbeatRunnerAuthenticated(ctx, id, input, payloadDigest)
}

// HeartbeatRunnerAuthenticated is used by the Runner-to-control-plane path.
// It deliberately does not accept a human actor hash; transport identity is
// checked by the HTTP boundary and replay protection is persisted in SQLite.
func (engine *Engine) HeartbeatRunnerAuthenticated(ctx context.Context, id string, input RunnerHeartbeatRequest, payloadDigest string) (model.RunnerNode, error) {
	if id == "" || input.RunnerID != id {
		return model.RunnerNode{}, errors.New("Runner 身份与路径不一致")
	}
	if engine.catalog.Fleet == nil || !engine.catalog.Fleet.Enabled {
		return model.RunnerNode{}, errors.New("多服务器管理尚未启用")
	}
	node, found, err := engine.store.GetRunnerNode(ctx, id)
	if err != nil {
		return model.RunnerNode{}, err
	}
	if !found {
		return model.RunnerNode{}, errors.New("Runner 未登记")
	}
	if node.State == model.NodeDisabled || node.State == model.NodeDraining {
		return model.RunnerNode{}, errors.New("Runner 当前不可接受心跳")
	}
	if input.PayloadVersion == 0 {
		input.PayloadVersion = HeartbeatPayloadVersion
	}
	if _, err := input.CanonicalPayload(); err != nil {
		return model.RunnerNode{}, err
	}
	computedDigest, err := HeartbeatPayloadDigest(input)
	if err != nil {
		return model.RunnerNode{}, err
	}
	if strings.TrimSpace(payloadDigest) == "" || payloadDigest != computedDigest {
		return model.RunnerNode{}, errors.New("Runner 心跳 payload 摘要不匹配")
	}
	lease := time.Duration(engine.catalog.Fleet.HeartbeatTimeoutSeconds) * time.Second
	if lease <= 0 {
		lease = 90 * time.Second
	}
	if len(input.Capabilities) == 0 && len(input.Labels) == 0 {
		return engine.store.HeartbeatRunnerWithReceipt(ctx, id, input.Version, lease, input.Nonce, payloadDigest)
	}
	return engine.store.HeartbeatRunnerAuthenticated(ctx, id, input.Version, input.Capabilities, input.Labels, lease, input.Nonce, payloadDigest)
}
