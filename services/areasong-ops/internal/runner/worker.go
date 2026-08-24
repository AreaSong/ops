package runner

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/config"
	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

const (
	defaultWorkerPollInterval = 2 * time.Second
	defaultAssignmentLease    = 90 * time.Second
	workerResponseLimit       = 1 << 20
)

// RemoteWorker claims durable assignments from the control plane and executes
// only the immutable dispatch contract returned by a successful claim.
type RemoteWorker struct {
	RunnerID     string
	Endpoint     string
	Client       *http.Client
	Catalog      *config.Catalog
	Executor     Executor
	StateRoot    string
	Lease        time.Duration
	PollInterval time.Duration
}

func (worker *RemoteWorker) Run(ctx context.Context) error {
	if worker.RunnerID == "" || worker.Endpoint == "" || worker.Client == nil || worker.Catalog == nil || worker.Executor == nil {
		return errors.New("远程 Runner worker 配置不完整")
	}
	if worker.Lease <= 0 {
		worker.Lease = defaultAssignmentLease
	}
	if worker.PollInterval <= 0 {
		worker.PollInterval = defaultWorkerPollInterval
	}
	for {
		claimed, err := worker.claim(ctx)
		if err != nil {
			return err
		}
		if claimed != nil {
			worker.execute(ctx, *claimed)
			continue
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(worker.PollInterval):
		}
	}
}

func (worker *RemoteWorker) claim(ctx context.Context) (*model.AssignmentClaimResponse, error) {
	var result model.AssignmentClaimResponse
	status, err := worker.request(ctx, http.MethodPost, "assignments/claim", model.AssignmentClaimRequest{
		LeaseSeconds: int(worker.Lease / time.Second),
	}, &result)
	if err != nil {
		return nil, err
	}
	if status == http.StatusNoContent {
		return nil, nil
	}
	return &result, nil
}

func (worker *RemoteWorker) execute(parent context.Context, claim model.AssignmentClaimResponse) {
	task := claim.Task
	assignment := claim.Assignment
	fence := model.AssignmentFence{RunnerID: worker.RunnerID, Generation: assignment.Generation, ClaimToken: assignment.ClaimToken}
	service, ok := worker.Catalog.Object(task.Service)
	if !ok || service.ServerID != assignment.ServerID {
		worker.complete(parent, task.ID, model.AssignmentCompletionRequest{AssignmentFence: fence,
			IdempotencyKey: workerCompletionKey(task.ID, assignment.Generation), State: model.TaskFailedRecoverable,
			Summary: "远程任务合同无法解析", Error: "任务服务或 server 绑定与本地声明不一致", FailureCode: "dispatch_contract_invalid", Retryable: true})
		return
	}
	localPolicyDigest := service.PolicyDigest()
	snapshotPolicyDigest, _ := task.Snapshot["trafficPolicyDigest"].(string)
	if task.TrafficPolicyDigest != localPolicyDigest || snapshotPolicyDigest != task.TrafficPolicyDigest {
		worker.complete(parent, task.ID, model.AssignmentCompletionRequest{AssignmentFence: fence,
			IdempotencyKey: workerCompletionKey(task.ID, assignment.Generation), State: model.TaskFailedRecoverable,
			Summary: "远程任务合同无法验证", Error: "流量策略摘要与本地声明不一致", FailureCode: "dispatch_contract_invalid", Retryable: true})
		return
	}
	action, ok := service.Actions[task.Action]
	if !ok {
		if generated, generatedOK := lifecycleAction(service, task.Action); generatedOK {
			action, ok = generated, true
		}
	}
	if !ok || !action.Enabled || !sameSteps(action.Steps, task.Stages) {
		worker.complete(parent, task.ID, model.AssignmentCompletionRequest{AssignmentFence: fence,
			IdempotencyKey: workerCompletionKey(task.ID, assignment.Generation), State: model.TaskFailedRecoverable,
			Summary: "远程任务合同无法验证", Error: "动作或阶段与本地声明不一致", FailureCode: "dispatch_contract_invalid", Retryable: true})
		return
	}
	operationDir := filepath.Join(worker.StateRoot, "operations", task.ID)
	if err := os.MkdirAll(operationDir, 0o700); err != nil {
		worker.complete(parent, task.ID, model.AssignmentCompletionRequest{AssignmentFence: fence,
			IdempotencyKey: workerCompletionKey(task.ID, assignment.Generation), State: model.TaskFailedRecoverable,
			Summary: "远程任务未开始", Error: redactText(err.Error()), FailureCode: "preflight_failed", Retryable: true})
		return
	}
	ctx, cancel := context.WithDeadline(parent, assignment.ExecutionDeadlineAt)
	defer cancel()
	leaseLost := make(chan struct{})
	var once sync.Once
	go worker.heartbeat(ctx, task.ID, fence, func() { once.Do(func() { close(leaseLost); cancel() }) })
	sequence := uint64(1)
	_ = worker.event(ctx, task.ID, model.AssignmentEventRequest{AssignmentFence: fence, RunnerSequence: sequence,
		Level: "info", Phase: action.Steps[0], Message: "任务开始执行"})
	lastSummary := ""
	productionChanged := false
	for _, phase := range action.Steps {
		if err := worker.progress(ctx, task.ID, model.AssignmentProgressRequest{AssignmentFence: fence,
			Phase: phase, Summary: lastSummary, State: model.TaskRunning, ProductionChanged: productionChanged}); err != nil {
			once.Do(func() { close(leaseLost); cancel() })
			return
		}
		result, err := executeAdapterPhase(ctx, worker.Executor, service, action.Name,
			phase, operationDir, task.Target, "")
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			worker.complete(parent, task.ID, model.AssignmentCompletionRequest{AssignmentFence: fence,
				IdempotencyKey: workerCompletionKey(task.ID, assignment.Generation), State: model.TaskFailedRecoverable,
				Summary: "远程任务执行失败", Error: redactText(err.Error()), FailureCode: "adapter_phase_failed",
				Retryable: !productionChanged, ProductionChanged: productionChanged})
			return
		}
		if changed, _ := result.Data["productionChanged"].(bool); changed || mutationSemantics(phaseSemantics(action, phase)) {
			productionChanged = true
		}
		lastSummary = result.Summary
		sequence++
		if err := worker.event(ctx, task.ID, model.AssignmentEventRequest{AssignmentFence: fence,
			RunnerSequence: sequence, Level: "info", Phase: phase, Message: result.Summary, Data: result.Data}); err != nil {
			return
		}
	}
	worker.complete(parent, task.ID, model.AssignmentCompletionRequest{AssignmentFence: fence,
		IdempotencyKey: workerCompletionKey(task.ID, assignment.Generation), State: model.TaskSucceeded,
		Summary: lastSummary, ProductionChanged: productionChanged})
}

func (worker *RemoteWorker) heartbeat(ctx context.Context, taskID string, fence model.AssignmentFence, lost func()) {
	interval := worker.Lease / 3
	if interval < time.Second {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := worker.request(ctx, http.MethodPost, "assignments/"+taskID+"/heartbeat",
				model.AssignmentHeartbeatRequest{AssignmentFence: fence}, nil); err != nil {
				lost()
				return
			}
		}
	}
}

func (worker *RemoteWorker) progress(ctx context.Context, taskID string, input model.AssignmentProgressRequest) error {
	_, err := worker.request(ctx, http.MethodPost, "assignments/"+taskID+"/progress", input, nil)
	return err
}

func (worker *RemoteWorker) event(ctx context.Context, taskID string, input model.AssignmentEventRequest) error {
	_, err := worker.request(ctx, http.MethodPost, "assignments/"+taskID+"/events", input, nil)
	return err
}

func (worker *RemoteWorker) complete(ctx context.Context, taskID string, input model.AssignmentCompletionRequest) {
	completeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	_, _ = worker.request(completeCtx, http.MethodPost, "assignments/"+taskID+"/complete", input, nil)
}

func (worker *RemoteWorker) request(ctx context.Context, method, suffix string, input, output any) (int, error) {
	payload, err := json.Marshal(input)
	if err != nil {
		return 0, err
	}
	url := worker.Endpoint + "/v1/fleet/runners/" + worker.RunnerID + "/" + suffix
	request, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(payload))
	if err != nil {
		return 0, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(runnerIDHeader, worker.RunnerID)
	response, err := worker.Client.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNoContent {
		return response.StatusCode, nil
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, workerResponseLimit+1))
	if err != nil {
		return response.StatusCode, err
	}
	if len(body) > workerResponseLimit {
		return response.StatusCode, errors.New("控制面响应超过限制")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return response.StatusCode, fmt.Errorf("控制面请求失败: %s", response.Status)
	}
	if output != nil && len(body) > 0 {
		if err := json.Unmarshal(body, output); err != nil {
			return response.StatusCode, err
		}
	}
	return response.StatusCode, nil
}

func workerCompletionKey(taskID string, generation uint64) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d", taskID, generation)))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func sameSteps(steps []string, stages []model.TaskStage) bool {
	if len(steps) != len(stages) {
		return false
	}
	for index := range steps {
		if steps[index] != stages[index].Name {
			return false
		}
	}
	return true
}
