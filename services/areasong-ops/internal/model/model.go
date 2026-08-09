package model

import "time"

type Risk string

const (
	RiskReadOnly Risk = "read_only"
	RiskLow      Risk = "low"
	RiskMedium   Risk = "medium"
	RiskHigh     Risk = "high"
)

type TaskState string

const (
	TaskQueued            TaskState = "queued"
	TaskRunning           TaskState = "running"
	TaskSucceeded         TaskState = "succeeded"
	TaskFailed            TaskState = "failed"
	TaskRolledBack        TaskState = "rolled_back"
	TaskRecoveryUncertain TaskState = "recovery_uncertain"
)

func (state TaskState) Terminal() bool {
	switch state {
	case TaskSucceeded, TaskFailed, TaskRolledBack, TaskRecoveryUncertain:
		return true
	default:
		return false
	}
}

type ActionDefinition struct {
	Name                 string   `json:"name"`
	DisplayName          string   `json:"displayName"`
	Enabled              bool     `json:"enabled"`
	Risk                 Risk     `json:"risk"`
	TargetMode           string   `json:"targetMode"`
	AllowedTargets       []string `json:"allowedTargets,omitempty"`
	Steps                []string `json:"steps"`
	TimeoutSeconds       int      `json:"timeoutSeconds"`
	ConfirmationTemplate string   `json:"confirmationTemplate,omitempty"`
	Impact               string   `json:"impact"`
	Rollback             string   `json:"rollback"`
	Scope                string   `json:"scope"`
}

type ServiceDefinition struct {
	Name        string                      `json:"name"`
	DisplayName string                      `json:"displayName"`
	Description string                      `json:"description"`
	Adapter     string                      `json:"adapter"`
	Actions     map[string]ActionDefinition `json:"actions"`
}

type ServiceView struct {
	Name         string                      `json:"name"`
	DisplayName  string                      `json:"displayName"`
	Description  string                      `json:"description"`
	Actions      map[string]ActionDefinition `json:"actions"`
	Status       map[string]any              `json:"status,omitempty"`
	StatusError  string                      `json:"statusError,omitempty"`
	ActiveTaskID string                      `json:"activeTaskId,omitempty"`
}

type Preview struct {
	ID                   string         `json:"id"`
	ActorHash            string         `json:"-"`
	Service              string         `json:"service"`
	Action               string         `json:"action"`
	Target               string         `json:"target,omitempty"`
	Risk                 Risk           `json:"risk"`
	Impact               string         `json:"impact"`
	Rollback             string         `json:"rollback"`
	Scope                string         `json:"scope"`
	Steps                []string       `json:"steps"`
	RequiresConfirmation bool           `json:"requiresConfirmation"`
	ConfirmationPhrase   string         `json:"confirmationPhrase,omitempty"`
	Snapshot             map[string]any `json:"snapshot,omitempty"`
	ExpiresAt            time.Time      `json:"expiresAt"`
	CreatedAt            time.Time      `json:"createdAt"`
}

type Task struct {
	ID             string         `json:"id"`
	IdempotencyKey string         `json:"-"`
	RequestHash    string         `json:"-"`
	ActorHash      string         `json:"actorHash"`
	Service        string         `json:"service"`
	Action         string         `json:"action"`
	Target         string         `json:"target,omitempty"`
	Risk           Risk           `json:"risk"`
	State          TaskState      `json:"state"`
	CurrentPhase   string         `json:"currentPhase,omitempty"`
	Summary        string         `json:"summary,omitempty"`
	Error          string         `json:"error,omitempty"`
	PreviewID      string         `json:"previewId"`
	Snapshot       map[string]any `json:"snapshot,omitempty"`
	CreatedAt      time.Time      `json:"createdAt"`
	StartedAt      *time.Time     `json:"startedAt,omitempty"`
	FinishedAt     *time.Time     `json:"finishedAt,omitempty"`
}

type Event struct {
	Sequence   int64          `json:"sequence"`
	TaskID     string         `json:"taskId,omitempty"`
	OccurredAt time.Time      `json:"occurredAt"`
	Level      string         `json:"level"`
	Phase      string         `json:"phase,omitempty"`
	Message    string         `json:"message"`
	Data       map[string]any `json:"data,omitempty"`
}

type AuditEntry struct {
	Sequence   int64          `json:"sequence"`
	OccurredAt time.Time      `json:"occurredAt"`
	ActorHash  string         `json:"actorHash"`
	Event      string         `json:"event"`
	Resource   string         `json:"resource"`
	Outcome    string         `json:"outcome"`
	Detail     map[string]any `json:"detail,omitempty"`
}

type PreviewRequest struct {
	Service string `json:"service"`
	Action  string `json:"action"`
	Target  string `json:"target,omitempty"`
}

type StartTaskRequest struct {
	PreviewID      string `json:"previewId"`
	Confirmation   string `json:"confirmation,omitempty"`
	IdempotencyKey string `json:"idempotencyKey"`
}

type AdapterResult struct {
	OK      bool           `json:"ok"`
	Summary string         `json:"summary"`
	Data    map[string]any `json:"data,omitempty"`
}
