package model

import "time"

type BatchOperationState string

const (
	BatchPendingApproval BatchOperationState = "pending_approval"
	BatchApproved        BatchOperationState = "approved"
	BatchRunning         BatchOperationState = "running"
	BatchPaused          BatchOperationState = "paused"
	BatchObserving       BatchOperationState = "observing"
	BatchSucceeded       BatchOperationState = "succeeded"
	BatchFailed          BatchOperationState = "failed"
	BatchRollingBack     BatchOperationState = "rolling_back"
	BatchRolledBack      BatchOperationState = "rolled_back"
	BatchNeedsAttention  BatchOperationState = "needs_attention"
	BatchCancelled       BatchOperationState = "cancelled"
)

func (state BatchOperationState) Terminal() bool {
	switch state {
	case BatchSucceeded, BatchFailed, BatchRolledBack, BatchNeedsAttention, BatchCancelled:
		return true
	default:
		return false
	}
}

type BatchItem struct {
	ID         string         `json:"id"`
	ObjectID   string         `json:"objectId"`
	Service    string         `json:"service"`
	ServerID   string         `json:"serverId,omitempty"`
	RunnerID   string         `json:"runnerId,omitempty"`
	BatchIndex int            `json:"batchIndex"`
	DependsOn  []string       `json:"dependsOn,omitempty"`
	State      BatchNodeState `json:"state"`
	PlanID     string         `json:"planId,omitempty"`
	TaskID     string         `json:"taskId,omitempty"`
	Error      string         `json:"error,omitempty"`
	UpdatedAt  time.Time      `json:"updatedAt"`
}

type BatchOperation struct {
	ID                         string              `json:"id"`
	IdempotencyKey             string              `json:"-"`
	RunIdempotencyKey          string              `json:"-"`
	ActorHash                  string              `json:"actorHash"`
	TenantID                   string              `json:"tenantId"`
	Action                     string              `json:"action"`
	Target                     string              `json:"target,omitempty"`
	Task                       BatchTask           `json:"task"`
	Digest                     string              `json:"digest"`
	ConfirmationPhrase         string              `json:"confirmationPhrase,omitempty"`
	State                      BatchOperationState `json:"state"`
	Items                      []BatchItem         `json:"items"`
	ApprovedByHash             string              `json:"approvedByHash,omitempty"`
	ApprovedAt                 *time.Time          `json:"approvedAt,omitempty"`
	RequiresDualApproval       bool                `json:"requiresDualApproval,omitempty"`
	SecondApprovedByHash       string              `json:"secondApprovedByHash,omitempty"`
	SecondApprovedAt           *time.Time          `json:"secondApprovedAt,omitempty"`
	ExecutedByHash             string              `json:"executedByHash,omitempty"`
	ExecutedAt                 *time.Time          `json:"executedAt,omitempty"`
	StartedAt                  *time.Time          `json:"startedAt,omitempty"`
	FinishedAt                 *time.Time          `json:"finishedAt,omitempty"`
	CanaryObservationStartedAt *time.Time          `json:"canaryObservationStartedAt,omitempty"`
	CanaryObservedAt           *time.Time          `json:"canaryObservedAt,omitempty"`
	Summary                    string              `json:"summary,omitempty"`
	Error                      string              `json:"error,omitempty"`
	CreatedAt                  time.Time           `json:"createdAt"`
	UpdatedAt                  time.Time           `json:"updatedAt"`
}

// BatchTaskStateForOperation keeps the nested task representation aligned with
// the durable operation envelope exposed by the batch API.
func BatchTaskStateForOperation(state BatchOperationState) BatchTaskState {
	switch state {
	case BatchPendingApproval:
		return BatchTaskPending
	case BatchApproved:
		return BatchTaskPlanning
	case BatchRunning, BatchObserving:
		return BatchTaskRunning
	case BatchPaused:
		return BatchTaskPaused
	case BatchSucceeded:
		return BatchTaskSucceeded
	case BatchFailed:
		return BatchTaskFailed
	case BatchRollingBack:
		return BatchTaskRollingBack
	case BatchRolledBack:
		return BatchTaskRolledBack
	case BatchNeedsAttention:
		return BatchTaskNeedsAttention
	case BatchCancelled:
		return BatchTaskCancelled
	default:
		return BatchTaskUnknown
	}
}

// SyncTask projects the operation's durable state into the nested task. The
// task is retained for compatibility with callers that consume BatchTask
// directly, while the operation remains the source of truth for approvals.
func (operation *BatchOperation) SyncTask() {
	if operation == nil {
		return
	}
	task := &operation.Task
	if task.ID == "" {
		task.ID = operation.ID
	}
	if task.CreatedAt.IsZero() {
		task.CreatedAt = operation.CreatedAt
	}
	task.State = BatchTaskStateForOperation(operation.State)
	task.Summary = operation.Summary
	task.Error = operation.Error
	task.StartedAt = operation.StartedAt
	task.FinishedAt = operation.FinishedAt
	byID := make(map[string]int, len(task.Nodes))
	for index := range task.Nodes {
		byID[task.Nodes[index].ID] = index
	}
	for _, item := range operation.Items {
		index, exists := byID[item.ID]
		if !exists {
			task.Nodes = append(task.Nodes, DAGNode{ID: item.ID, Action: operation.Action, TargetID: item.Service})
			index = len(task.Nodes) - 1
		}
		node := &task.Nodes[index]
		node.State = item.State
		node.TargetID = item.Service
		node.Error = item.Error
		if len(node.Dependencies) == 0 && len(item.DependsOn) > 0 {
			node.Dependencies = append([]string(nil), item.DependsOn...)
		}
	}
}

type BatchCreateRequest struct {
	Action         string            `json:"action"`
	Target         string            `json:"target,omitempty"`
	TargetIDs      []string          `json:"targetIds"`
	TargetSelector NodeSelector      `json:"targetSelector,omitempty"`
	BatchPolicy    BatchPolicy       `json:"batchPolicy"`
	Concurrency    ConcurrencyPolicy `json:"concurrency"`
	FailurePolicy  FailurePolicy     `json:"failurePolicy"`
	ChangeWindow   *ChangeWindow     `json:"changeWindow,omitempty"`
	IdempotencyKey string            `json:"idempotencyKey"`
}

type BatchApproveRequest struct {
	Digest       string `json:"digest"`
	Confirmation string `json:"confirmation"`
}

type BatchExecuteRequest struct {
	IdempotencyKey string `json:"idempotencyKey"`
}
