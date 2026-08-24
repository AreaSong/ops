package model

import "time"

// AssignmentState is the durable lifecycle of a task dispatched to a Fleet
// Runner. A claimed assignment is never silently re-assigned after its lease
// expires; the control plane must classify the interruption explicitly.
type AssignmentState string

const (
	AssignmentAssigned  AssignmentState = "assigned"
	AssignmentClaimed   AssignmentState = "claimed"
	AssignmentCompleted AssignmentState = "completed"
	AssignmentExpired   AssignmentState = "expired"
)

func (state AssignmentState) Active() bool {
	return state == AssignmentAssigned || state == AssignmentClaimed
}

// TaskAssignment is the durable dispatch fence. ClaimToken is only populated
// in the one-time claim response and is never persisted or exposed by reads.
type TaskAssignment struct {
	TaskID              string          `json:"taskId"`
	ServerID            string          `json:"serverId"`
	RunnerID            string          `json:"runnerId"`
	Generation          uint64          `json:"generation"`
	State               AssignmentState `json:"state"`
	ClaimToken          string          `json:"claimToken,omitempty"`
	ContractDigest      string          `json:"contractDigest"`
	ClaimedAt           *time.Time      `json:"claimedAt,omitempty"`
	LastHeartbeatAt     *time.Time      `json:"lastHeartbeatAt,omitempty"`
	LeaseExpiresAt      *time.Time      `json:"leaseExpiresAt,omitempty"`
	ExecutionDeadlineAt time.Time       `json:"executionDeadlineAt"`
	CompletionDigest    string          `json:"completionDigest,omitempty"`
	CompletionKey       string          `json:"-"`
	CompletionSequence  int64           `json:"-"`
	LastRunnerSequence  uint64          `json:"lastRunnerSequence,omitempty"`
	CreatedAt           time.Time       `json:"createdAt"`
	UpdatedAt           time.Time       `json:"updatedAt"`
}

// TaskDispatch is the immutable task envelope sent to a remote Runner. It is
// deliberately separate from Task so future additions to control-plane view
// fields cannot accidentally become part of the execution contract.
type TaskDispatch struct {
	ID                          string         `json:"id"`
	Service                     string         `json:"service"`
	Action                      string         `json:"action"`
	Target                      string         `json:"target,omitempty"`
	Risk                        Risk           `json:"risk"`
	PlanID                      string         `json:"planId,omitempty"`
	PlanDigest                  string         `json:"planDigest,omitempty"`
	TrafficPolicyDigest         string         `json:"trafficPolicyDigest,omitempty"`
	Snapshot                    map[string]any `json:"snapshot,omitempty"`
	Stages                      []TaskStage    `json:"stages,omitempty"`
	RestoreMode                 string         `json:"restoreMode,omitempty"`
	RecoveryPointID             string         `json:"recoveryPointId,omitempty"`
	RestoreTenantID             string         `json:"restoreTenantId,omitempty"`
	RestoreServerID             string         `json:"restoreServerId,omitempty"`
	RestoreExpectedBeforeDigest string         `json:"restoreExpectedBeforeDigest,omitempty"`
	RestoreContractDigest       string         `json:"restoreContractDigest,omitempty"`
	RestoreEvidenceDigest       string         `json:"restoreEvidenceDigest,omitempty"`
}

func NewTaskDispatch(task Task) TaskDispatch {
	trafficPolicyDigest := task.TrafficPolicyDigest
	if trafficPolicyDigest == "" && task.Snapshot != nil {
		trafficPolicyDigest, _ = task.Snapshot["trafficPolicyDigest"].(string)
	}
	return TaskDispatch{
		ID: task.ID, Service: task.Service, Action: task.Action, Target: task.Target,
		Risk: task.Risk, PlanID: task.PlanID, PlanDigest: task.PlanDigest,
		TrafficPolicyDigest: trafficPolicyDigest,
		Snapshot:            task.Snapshot, Stages: task.Stages, RestoreMode: task.RestoreMode,
		RecoveryPointID: task.RecoveryPointID, RestoreTenantID: task.RestoreTenantID,
		RestoreServerID:             task.RestoreServerID,
		RestoreExpectedBeforeDigest: task.RestoreExpectedBeforeDigest,
		RestoreContractDigest:       task.RestoreContractDigest,
		RestoreEvidenceDigest:       task.RestoreEvidenceDigest,
	}
}

// TrafficPolicyDigestFromDispatch returns the immutable traffic contract
// digest that a remote worker must compare with its local ServiceDefinition.
func TrafficPolicyDigestFromDispatch(dispatch TaskDispatch) string {
	return dispatch.TrafficPolicyDigest
}

type AssignmentClaimRequest struct {
	TaskID       string `json:"taskId,omitempty"`
	LeaseSeconds int    `json:"leaseSeconds,omitempty"`
}

type AssignmentClaimResponse struct {
	Task       TaskDispatch   `json:"task"`
	Assignment TaskAssignment `json:"assignment"`
}

// AssignmentFence is carried by every post-claim mutation. ClaimToken is
// plaintext only in transit; the store compares its SHA-256 hash.
type AssignmentFence struct {
	RunnerID   string `json:"runnerId,omitempty"`
	Generation uint64 `json:"generation"`
	ClaimToken string `json:"claimToken"`
}

type AssignmentHeartbeatRequest struct {
	AssignmentFence
}

type AssignmentProgressRequest struct {
	AssignmentFence
	Phase             string    `json:"phase"`
	Summary           string    `json:"summary,omitempty"`
	State             TaskState `json:"state,omitempty"`
	ProductionChanged bool      `json:"productionChanged,omitempty"`
	RollbackAvailable bool      `json:"rollbackAvailable,omitempty"`
	RollbackReason    string    `json:"rollbackReason,omitempty"`
}

type AssignmentEventRequest struct {
	AssignmentFence
	RunnerSequence uint64         `json:"runnerSequence"`
	Level          string         `json:"level"`
	Phase          string         `json:"phase,omitempty"`
	Message        string         `json:"message"`
	Data           map[string]any `json:"data,omitempty"`
}

type AssignmentCompletionRequest struct {
	AssignmentFence
	IdempotencyKey    string    `json:"idempotencyKey"`
	State             TaskState `json:"state"`
	Summary           string    `json:"summary,omitempty"`
	Error             string    `json:"error,omitempty"`
	FailureCode       string    `json:"failureCode,omitempty"`
	Retryable         bool      `json:"retryable,omitempty"`
	RollbackAvailable bool      `json:"rollbackAvailable,omitempty"`
	RollbackReason    string    `json:"rollbackReason,omitempty"`
	ProductionChanged bool      `json:"productionChanged,omitempty"`
	ResultDigest      string    `json:"resultDigest,omitempty"`
}

type AssignmentCompletionResponse struct {
	Task          Task           `json:"task"`
	Assignment    TaskAssignment `json:"assignment"`
	EventSequence int64          `json:"eventSequence"`
}
