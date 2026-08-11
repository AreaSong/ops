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
	TaskWaitingConfirmation TaskState = "waiting_confirmation"
	TaskQueued              TaskState = "queued"
	TaskRunning             TaskState = "running"
	TaskRollingBack         TaskState = "rolling_back"
	TaskSucceeded           TaskState = "succeeded"
	TaskFailed              TaskState = "failed"
	TaskFailedRecoverable   TaskState = "failed_recoverable"
	TaskNeedsAttention      TaskState = "needs_attention"
	TaskRolledBack          TaskState = "rolled_back"
	TaskRecoveryUncertain   TaskState = "recovery_uncertain"
)

func (state TaskState) Active() bool {
	switch state {
	case TaskWaitingConfirmation, TaskQueued, TaskRunning, TaskRollingBack:
		return true
	default:
		return false
	}
}

func (state TaskState) Terminal() bool {
	switch state {
	case TaskSucceeded, TaskFailed, TaskFailedRecoverable, TaskNeedsAttention,
		TaskRolledBack, TaskRecoveryUncertain:
		return true
	default:
		return false
	}
}

func (state TaskState) CanTransition(next TaskState) bool {
	switch state {
	case TaskWaitingConfirmation:
		return next == TaskQueued || next == TaskFailedRecoverable
	case TaskQueued:
		return next == TaskRunning || next == TaskFailedRecoverable || next == TaskNeedsAttention
	case TaskRunning:
		return next == TaskRollingBack || next == TaskSucceeded || next == TaskFailed ||
			next == TaskFailedRecoverable || next == TaskNeedsAttention || next == TaskRecoveryUncertain
	case TaskRollingBack:
		return next == TaskRolledBack || next == TaskNeedsAttention || next == TaskRecoveryUncertain
	default:
		return false
	}
}

type PlanState string

const (
	PlanPendingApproval PlanState = "pending_approval"
	PlanApproved        PlanState = "approved"
	PlanExecuting       PlanState = "executing"
	PlanObserving       PlanState = "observing"
	PlanNeedsAttention  PlanState = "needs_attention"
	PlanCompleted       PlanState = "completed"
	PlanInvalidated     PlanState = "invalidated"
)

type StageState string

const (
	StagePending    StageState = "pending"
	StageRunning    StageState = "running"
	StageSucceeded  StageState = "succeeded"
	StageFailed     StageState = "failed"
	StageSkipped    StageState = "skipped"
	StageRolledBack StageState = "rolled_back"
)

type TaskStage struct {
	Name       string     `json:"name"`
	State      StageState `json:"state"`
	Summary    string     `json:"summary,omitempty"`
	StartedAt  *time.Time `json:"startedAt,omitempty"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
}

type RecoveryAction struct {
	Name    string `json:"name"`
	Label   string `json:"label"`
	Enabled bool   `json:"enabled"`
	Reason  string `json:"reason,omitempty"`
}

type ApprovalSummary struct {
	SchemaVersion      int                       `json:"schemaVersion"`
	Service            string                    `json:"service"`
	Action             string                    `json:"action"`
	Target             string                    `json:"target,omitempty"`
	Risk               Risk                      `json:"risk"`
	Impact             string                    `json:"impact"`
	Rollback           string                    `json:"rollback"`
	Scope              string                    `json:"scope"`
	Steps              []string                  `json:"steps"`
	PhaseSemantics     map[string]PhaseSemantics `json:"phaseSemantics,omitempty"`
	ObservationSeconds int                       `json:"observationSeconds,omitempty"`
	AlertPolicy        AlertPolicyDefinition     `json:"alertPolicy,omitempty"`
	ConfirmationPhrase string                    `json:"confirmationPhrase,omitempty"`
	ExpectedBefore     map[string]any            `json:"expectedBefore"`
	TargetEvidence     map[string]any            `json:"targetEvidence,omitempty"`
}

type ReleasePlan struct {
	ID                           string          `json:"id"`
	ActorHash                    string          `json:"actorHash"`
	Service                      string          `json:"service"`
	Action                       string          `json:"action"`
	Target                       string          `json:"target,omitempty"`
	Risk                         Risk            `json:"risk"`
	State                        PlanState       `json:"state"`
	Digest                       string          `json:"digest"`
	ApprovalSummary              ApprovalSummary `json:"approvalSummary"`
	ConfirmationPhrase           string          `json:"confirmationPhrase,omitempty"`
	RequiresConfirmation         bool            `json:"requiresConfirmation"`
	ApprovedByHash               string          `json:"approvedByHash,omitempty"`
	ApprovedAt                   *time.Time      `json:"approvedAt,omitempty"`
	InvalidatedReason            string          `json:"invalidatedReason,omitempty"`
	TaskID                       string          `json:"taskId,omitempty"`
	ObservationSeconds           int             `json:"observationSeconds,omitempty"`
	ObservationStartedAt         *time.Time      `json:"observationStartedAt,omitempty"`
	ObservationEndsAt            *time.Time      `json:"observationEndsAt,omitempty"`
	ClosureReason                string          `json:"closureReason,omitempty"`
	MaintenanceSilenceID         string          `json:"maintenanceSilenceId,omitempty"`
	MaintenanceSilenceEndsAt     *time.Time      `json:"maintenanceSilenceEndsAt,omitempty"`
	MaintenanceSilenceReleasedAt *time.Time      `json:"maintenanceSilenceReleasedAt,omitempty"`
	BlockingAlertFingerprints    []string        `json:"blockingAlertFingerprints,omitempty"`
	ClosedAt                     *time.Time      `json:"closedAt,omitempty"`
	CreatedAt                    time.Time       `json:"createdAt"`
	UpdatedAt                    time.Time       `json:"updatedAt"`
}

type ActionDefinition struct {
	Name                 string                    `json:"name"`
	DisplayName          string                    `json:"displayName"`
	Enabled              bool                      `json:"enabled"`
	Risk                 Risk                      `json:"risk"`
	TargetMode           string                    `json:"targetMode"`
	AllowedTargets       []string                  `json:"allowedTargets,omitempty"`
	Steps                []string                  `json:"steps"`
	TimeoutSeconds       int                       `json:"timeoutSeconds"`
	ConfirmationTemplate string                    `json:"confirmationTemplate,omitempty"`
	DisabledReason       string                    `json:"disabledReason,omitempty"`
	ReadinessGate        string                    `json:"readinessGate,omitempty"`
	Impact               string                    `json:"impact"`
	Rollback             string                    `json:"rollback"`
	Scope                string                    `json:"scope"`
	PhaseSemantics       map[string]PhaseSemantics `json:"phaseSemantics,omitempty"`
	ObservationSeconds   int                       `json:"observationSeconds,omitempty"`
}

type AlertPolicyDefinition struct {
	Matchers          map[string]string `json:"matchers,omitempty"`
	BlockingAlerts    []string          `json:"blockingAlerts,omitempty"`
	MaintenanceAlerts []string          `json:"maintenanceAlerts,omitempty"`
}

type ActiveAlert struct {
	Fingerprint string            `json:"fingerprint"`
	ObjectID    string            `json:"objectId"`
	Service     string            `json:"service"`
	AlertName   string            `json:"alertName"`
	Severity    string            `json:"severity"`
	Summary     string            `json:"summary"`
	RunbookURL  string            `json:"runbookUrl,omitempty"`
	GrafanaURL  string            `json:"grafanaUrl,omitempty"`
	Labels      map[string]string `json:"-"`
	Silenced    bool              `json:"silenced"`
	StartsAt    time.Time         `json:"startsAt"`
}

type MaintenanceSilence struct {
	ID     string    `json:"id"`
	EndsAt time.Time `json:"endsAt"`
}

type PhaseSemantics struct {
	Effect                string `json:"effect"`
	ProducesRecoveryPoint bool   `json:"producesRecoveryPoint,omitempty"`
	RequiresRecoveryPoint bool   `json:"requiresRecoveryPoint,omitempty"`
	FailurePolicy         string `json:"failurePolicy"`
	RecoveryPhase         string `json:"recoveryPhase,omitempty"`
}

func EffectivePhaseSemantics(action ActionDefinition, phase string) PhaseSemantics {
	if semantics, ok := action.PhaseSemantics[phase]; ok {
		return semantics
	}
	semantics := PhaseSemantics{Effect: "observe", FailurePolicy: "fail"}
	switch phase {
	case "backup":
		semantics.Effect = "artifact_write"
	case "migration":
		semantics.Effect = "data_mutation"
		semantics.FailurePolicy = "needs_attention"
	case "apply", "restart":
		semantics.Effect = "runtime_mutation"
		if action.Name == "update" {
			semantics.FailurePolicy = "rollback"
			semantics.RecoveryPhase = "rollback"
		}
	}
	return semantics
}

func ActionRequiresObservation(action ActionDefinition) bool {
	for _, phase := range action.Steps {
		effect := EffectivePhaseSemantics(action, phase).Effect
		if effect == "runtime_mutation" || effect == "data_mutation" {
			return true
		}
	}
	return false
}

type RecoveryArtifact struct {
	Role      string `json:"role"`
	Path      string `json:"path"`
	SizeBytes int64  `json:"sizeBytes"`
	SHA256    string `json:"sha256"`
}

type RecoveryPointEvidence struct {
	SchemaVersion int                `json:"schemaVersion"`
	Service       string             `json:"service"`
	TaskID        string             `json:"taskId"`
	CreatedAt     time.Time          `json:"createdAt"`
	Artifacts     []RecoveryArtifact `json:"artifacts"`
}

type RecoveryPoint struct {
	ID               string                `json:"id"`
	TaskID           string                `json:"taskId"`
	Service          string                `json:"service"`
	Status           string                `json:"status"`
	Evidence         RecoveryPointEvidence `json:"evidence"`
	EvidenceDigest   string                `json:"evidenceDigest"`
	CreatedAt        time.Time             `json:"createdAt"`
	VerifiedAt       *time.Time            `json:"verifiedAt,omitempty"`
	RecoverableUntil *time.Time            `json:"recoverableUntil,omitempty"`
}

type ComposeServiceRuntime struct {
	ControlledCompose      string   `json:"controlledCompose"`
	RuntimeCompose         string   `json:"runtimeCompose"`
	EnvFile                string   `json:"envFile"`
	ApplicationService     string   `json:"applicationService"`
	ApplicationContainer   string   `json:"applicationContainer"`
	DependencyContainers   []string `json:"dependencyContainers,omitempty"`
	HealthURL              string   `json:"healthUrl"`
	ReleaseRepository      string   `json:"releaseRepository"`
	ReleaseCatalog         string   `json:"releaseCatalog"`
	PreparedReleaseDir     string   `json:"preparedReleaseDir"`
	InspectExecutable      string   `json:"inspectExecutable"`
	BackupExecutables      []string `json:"backupExecutables,omitempty"`
	RestoreDrillExecutable string   `json:"restoreDrillExecutable,omitempty"`
	PrepareExecutable      string   `json:"prepareExecutable,omitempty"`
	UpdateExecutable       string   `json:"updateExecutable,omitempty"`
}

type ServiceDefinition struct {
	Name        string                      `json:"name"`
	ObjectID    string                      `json:"objectId"`
	DisplayName string                      `json:"displayName"`
	Description string                      `json:"description"`
	Template    string                      `json:"template"`
	Adapter     string                      `json:"adapter"`
	Runtime     *ComposeServiceRuntime      `json:"runtime,omitempty"`
	AlertPolicy AlertPolicyDefinition       `json:"alertPolicy"`
	Actions     map[string]ActionDefinition `json:"actions"`
}

type ServiceView struct {
	Name                 string                      `json:"name"`
	ObjectID             string                      `json:"objectId"`
	DisplayName          string                      `json:"displayName"`
	Description          string                      `json:"description"`
	Actions              map[string]ActionDefinition `json:"actions"`
	Status               map[string]any              `json:"status,omitempty"`
	ReleaseDiscovery     map[string]any              `json:"releaseDiscovery,omitempty"`
	StatusError          string                      `json:"statusError,omitempty"`
	ActiveTaskID         string                      `json:"activeTaskId,omitempty"`
	RollbackSourceTaskID string                      `json:"rollbackSourceTaskId,omitempty"`
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
	ID                string           `json:"id"`
	IdempotencyKey    string           `json:"-"`
	RequestHash       string           `json:"-"`
	ActorHash         string           `json:"actorHash"`
	Service           string           `json:"service"`
	Action            string           `json:"action"`
	Target            string           `json:"target,omitempty"`
	Risk              Risk             `json:"risk"`
	State             TaskState        `json:"state"`
	CurrentPhase      string           `json:"currentPhase,omitempty"`
	Summary           string           `json:"summary,omitempty"`
	Error             string           `json:"error,omitempty"`
	PreviewID         string           `json:"previewId"`
	PlanID            string           `json:"planId,omitempty"`
	PlanDigest        string           `json:"planDigest,omitempty"`
	ParentTaskID      string           `json:"parentTaskId,omitempty"`
	Snapshot          map[string]any   `json:"snapshot,omitempty"`
	Stages            []TaskStage      `json:"stages,omitempty"`
	RunnerOwner       string           `json:"-"`
	HeartbeatAt       *time.Time       `json:"heartbeatAt,omitempty"`
	ProductionChanged bool             `json:"productionChanged"`
	Retryable         bool             `json:"retryable"`
	FailureCode       string           `json:"failureCode,omitempty"`
	RollbackAvailable bool             `json:"rollbackAvailable"`
	RollbackReason    string           `json:"rollbackReason,omitempty"`
	RecoveryPointID   string           `json:"recoveryPointId,omitempty"`
	RecoveryActions   []RecoveryAction `json:"recoveryActions,omitempty"`
	CreatedAt         time.Time        `json:"createdAt"`
	StartedAt         *time.Time       `json:"startedAt,omitempty"`
	FinishedAt        *time.Time       `json:"finishedAt,omitempty"`
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

type ApprovePlanRequest struct {
	Confirmation string `json:"confirmation,omitempty"`
	Digest       string `json:"digest"`
}

type ExecutePlanRequest struct {
	IdempotencyKey string `json:"idempotencyKey"`
}

type ClosePlanRequest struct {
	IdempotencyKey string `json:"idempotencyKey"`
}

type RecoveryRequest struct {
	Action string `json:"action"`
}

type AdapterResult struct {
	SchemaVersion int                    `json:"schemaVersion,omitempty"`
	Action        string                 `json:"action,omitempty"`
	Phase         string                 `json:"phase,omitempty"`
	OK            bool                   `json:"ok"`
	Summary       string                 `json:"summary"`
	Data          map[string]any         `json:"data,omitempty"`
	RecoveryPoint *RecoveryPointEvidence `json:"recoveryPoint,omitempty"`
}
