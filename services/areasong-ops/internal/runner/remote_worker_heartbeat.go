package runner

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

func (worker *RemoteWorker) remoteWorkerConfigured() bool {
	return worker.Catalog != nil && worker.Catalog.Fleet != nil &&
		worker.Catalog.Fleet.RemoteWorker != nil && worker.Catalog.Fleet.RemoteWorker.Enabled
}

func (worker *RemoteWorker) identityHeartbeatLoop(ctx context.Context, result chan<- error) {
	delay := worker.HeartbeatInterval
	retryAttempt := 0
	for {
		if stopped, _ := worker.waitForInterval(ctx, nil, delay); stopped {
			return
		}
		err := worker.sendIdentityHeartbeat(ctx)
		if err == nil {
			delay = worker.HeartbeatInterval
			retryAttempt = 0
			continue
		}
		if ctx.Err() != nil {
			return
		}
		if !isRetryableRemoteWorkerError(err) {
			result <- err
			return
		}
		delay = remoteWorkerRetryDelay(retryAttempt, worker.PollInterval)
		retryAttempt++
		logRemoteWorkerRetry("身份心跳", err, delay)
	}
}

func (worker *RemoteWorker) sendIdentityHeartbeat(ctx context.Context) error {
	node, err := worker.localRunnerNode()
	if err != nil {
		return err
	}
	version, revision, digest, err := worker.currentRunnerIdentity()
	if err != nil {
		return fmt.Errorf("读取远程 Runner 运行身份失败: %w", err)
	}
	nonce, err := newHeartbeatNonce()
	if err != nil {
		return err
	}
	input := RunnerHeartbeatRequest{
		RunnerID: worker.RunnerID, Version: version, Revision: revision, BinaryDigest: digest,
		PayloadVersion: RunnerIdentityPayloadVersion,
		Capabilities:   append([]string(nil), node.Capabilities...),
		Labels:         cloneWorkerLabels(node.Labels),
		Timestamp:      worker.now().UTC().Format(time.RFC3339Nano), Nonce: nonce,
	}
	input.Signature, err = SignHeartbeatPayload(worker.HeartbeatPrivateKey, input)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return err
	}
	url := strings.TrimRight(worker.Endpoint, "/") + "/v1/fleet/runners/" + worker.RunnerID + "/heartbeat"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(runnerIDHeader, worker.RunnerID)
	request.Header.Set(runnerNonceHeader, input.Nonce)
	request.Header.Set(runnerTimestampHeader, input.Timestamp)
	request.Header.Set(runnerSignatureHeader, input.Signature)
	response, err := worker.Client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, workerResponseLimit+1))
	if err != nil {
		return err
	}
	if len(body) > workerResponseLimit {
		return errors.New("Runner 心跳响应超过限制")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return newRemoteWorkerHTTPError("Runner 身份心跳", response)
	}
	var accepted model.RunnerNode
	if err := json.Unmarshal(body, &accepted); err != nil {
		return err
	}
	if accepted.ID != worker.RunnerID || accepted.ServerID != node.ServerID ||
		accepted.Version != version || accepted.Revision != revision || accepted.BinaryDigest != digest ||
		accepted.IdentityPayloadVersion != RunnerIdentityPayloadVersion || accepted.LeaseGeneration == 0 ||
		accepted.State != model.NodeOnline {
		return errors.New("控制面返回的 Runner 身份心跳回执不一致")
	}
	return nil
}

func (worker *RemoteWorker) localRunnerNode() (model.RunnerNode, error) {
	for _, node := range worker.Catalog.Fleet.Inventory.Runners {
		if node.ID == worker.RunnerID {
			return node, nil
		}
	}
	return model.RunnerNode{}, errors.New("本地目录未登记远程 Runner 身份")
}

func (worker *RemoteWorker) now() time.Time {
	if worker.Now != nil {
		return worker.Now()
	}
	return time.Now()
}

func newHeartbeatNonce() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func cloneWorkerLabels(input map[string]string) map[string]string {
	result := make(map[string]string, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}
