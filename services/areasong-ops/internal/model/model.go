package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

// TrafficAdapterPath is the only executable allowed for the Nginx traffic
// contract. It is kept in model so control-plane and remote-worker digest
// calculation do not depend on a config package import.
const TrafficAdapterPath = "/usr/local/libexec/areasong-ops/adapters/nginx-traffic.sh"

type Risk string

const (
	RiskReadOnly Risk = "read_only"
	RiskLow      Risk = "low"
	RiskMedium   Risk = "medium"
	RiskHigh     Risk = "high"
)

const GitHubAlertmanagerCredential = "github_alertmanager"

type CredentialRotationState string

const (
	CredentialRotationRunning                   CredentialRotationState = "running"
	CredentialRotationFailed                    CredentialRotationState = "failed"
	CredentialRotationRolledBack                CredentialRotationState = "rolled_back"
	CredentialRotationNeedsAttention            CredentialRotationState = "needs_attention"
	CredentialRotationSwitchedPendingRevocation CredentialRotationState = "switched_pending_revocation"
	CredentialRotationRevocationVerified        CredentialRotationState = "revocation_verified"
	CredentialRotationCompleted                 CredentialRotationState = "completed"
)

type CredentialProfileView struct {
	Type               string              `json:"type"`
	DisplayName        string              `json:"displayName"`
	Target             string              `json:"target"`
	Repository         string              `json:"repository"`
	Risk               Risk                `json:"risk"`
	ConfirmationPhrase string              `json:"confirmationPhrase"`
	Configured         bool                `json:"configured"`
	CanManage          bool                `json:"canManage"`
	Fingerprint        string              `json:"fingerprint,omitempty"`
	ExpiresAt          string              `json:"expiresAt,omitempty"`
	LastRotation       *CredentialRotation `json:"lastRotation,omitempty"`
}

type CredentialRotation struct {
	ID                    string                  `json:"id"`
	IdempotencyKey        string                  `json:"-"`
	ClosureIdempotencyKey string                  `json:"-"`
	ActorHash             string                  `json:"actorHash"`
	CredentialType        string                  `json:"credentialType"`
	Target                string                  `json:"target"`
	State                 CredentialRotationState `json:"state"`
	Fingerprint           string                  `json:"fingerprint"`
	ExpiresAt             string                  `json:"expiresAt"`
	ValidationResult      string                  `json:"validationResult,omitempty"`
	Outcome               string                  `json:"outcome,omitempty"`
	RollbackResult        string                  `json:"rollbackResult,omitempty"`
	CreatedAt             time.Time               `json:"createdAt"`
	FinishedAt            *time.Time              `json:"finishedAt,omitempty"`
	ClosedAt              *time.Time              `json:"closedAt,omitempty"`
}

type CredentialRotationRequest struct {
	CredentialType string `json:"credentialType"`
	Secret         string `json:"secret"`
	ExpiresAt      string `json:"expiresAt"`
	Confirmation   string `json:"confirmation"`
	IdempotencyKey string `json:"idempotencyKey"`
}

type CredentialRotationCloseRequest struct {
	Confirmation   string `json:"confirmation"`
	IdempotencyKey string `json:"idempotencyKey"`
}

type CredentialRotationResult struct {
	State            CredentialRotationState
	ValidationResult string
	Outcome          string
	RollbackResult   string
}

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
	PlanScheduled       PlanState = "scheduled"
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
	SchemaVersion int `json:"schemaVersion"`
	// ApprovalPolicy is signed into the summary so an approved plan cannot be
	// reinterpreted under a different identity workflow.
	ApprovalPolicy string `json:"approvalPolicy,omitempty"`
	Service        string `json:"service"`
	Action         string `json:"action"`
	// ApprovalException records a narrowly-scoped, policy-approved deviation
	// from the default high-risk approval gate. It is part of the signed
	// summary so the exception cannot be added after approval.
	ApprovalException   string `json:"approvalException,omitempty"`
	Target              string `json:"target,omitempty"`
	TrafficPolicyDigest string `json:"trafficPolicyDigest,omitempty"`
	// Restore fields are part of the signed approval summary.  Keeping them in
	// the summary (rather than only on the plan envelope) prevents an approval
	// from being replayed against a different tenant, runner, or before-state.
	RestoreMode                 string                    `json:"restoreMode,omitempty"`
	RecoveryPointID             string                    `json:"recoveryPointId,omitempty"`
	TenantID                    string                    `json:"tenantId,omitempty"`
	ServerID                    string                    `json:"serverId,omitempty"`
	ScheduleAt                  *time.Time                `json:"scheduleAt,omitempty"`
	ExpectedBeforeDigest        string                    `json:"expectedBeforeDigest,omitempty"`
	RecoveryPointBindingDigest  string                    `json:"recoveryPointBindingDigest,omitempty"`
	RecoveryPointEvidenceDigest string                    `json:"recoveryPointEvidenceDigest,omitempty"`
	Risk                        Risk                      `json:"risk"`
	Impact                      string                    `json:"impact"`
	Rollback                    string                    `json:"rollback"`
	Scope                       string                    `json:"scope"`
	Steps                       []string                  `json:"steps"`
	PhaseSemantics              map[string]PhaseSemantics `json:"phaseSemantics,omitempty"`
	ObservationSeconds          int                       `json:"observationSeconds,omitempty"`
	TimeoutSeconds              int                       `json:"timeoutSeconds,omitempty"`
	AlertPolicy                 AlertPolicyDefinition     `json:"alertPolicy,omitempty"`
	ConfirmationPhrase          string                    `json:"confirmationPhrase,omitempty"`
	ExpectedBefore              map[string]any            `json:"expectedBefore"`
	TargetEvidence              map[string]any            `json:"targetEvidence,omitempty"`
}

const ApprovalExceptionC2LifecycleSingleActor = "c2_lifecycle_single_actor"

// ApprovalPolicy identifies the identity workflow used by a plan. Empty is
// deliberately treated as the legacy four-identity workflow when reading
// persisted records created before this field existed.
const (
	ApprovalPolicyLegacyFourParty = "legacy_four_party"
	ApprovalPolicyTwoParty        = "two_party_v1"
)

func EffectiveApprovalPolicy(policy string) string {
	if policy == ApprovalPolicyTwoParty {
		return ApprovalPolicyTwoParty
	}
	return ApprovalPolicyLegacyFourParty
}

func UsesTwoPartyApproval(policy string) bool {
	return EffectiveApprovalPolicy(policy) == ApprovalPolicyTwoParty
}

// AllowsC2LifecycleSingleActorApproval is intentionally strict. It applies
// only to the production AreaForge lifecycle plan and never to an explicit
// dual-approval plan or to any other action/service.
func (plan ReleasePlan) AllowsC2LifecycleSingleActorApproval() bool {
	if plan.RequiresDualApproval || plan.TenantID != "production" || plan.Service != "areaforge" {
		return false
	}
	if plan.Action != "start" && plan.Action != "stop" {
		return false
	}
	if plan.ApprovalSummary.Service != plan.Service || plan.ApprovalSummary.Action != plan.Action ||
		plan.ApprovalSummary.TenantID != plan.TenantID || plan.ApprovalSummary.Risk != plan.Risk {
		return false
	}
	return plan.ApprovalSummary.ApprovalException == ApprovalExceptionC2LifecycleSingleActor
}

// HasRequiredApprovalPolicy rejects legacy or malformed high-risk plans that
// do not carry either the global dual-approval gate or the signed C2 exception.
func (plan ReleasePlan) HasRequiredApprovalPolicy() bool {
	return plan.Risk != RiskHigh || plan.RequiresDualApproval ||
		plan.AllowsC2LifecycleSingleActorApproval()
}

// IndependentExecutor enforces the four-identity boundary shared by high-risk
// plans: creator, two approvers, and executor must all be distinct.
func IndependentExecutor(actor, creator, firstApprover, secondApprover string) bool {
	return actor != "" && creator != "" && firstApprover != "" && secondApprover != "" &&
		creator != firstApprover && creator != secondApprover && firstApprover != secondApprover &&
		actor != creator && actor != firstApprover && actor != secondApprover
}

func (plan ReleasePlan) AllowsExecutor(actor string) bool {
	if plan.Risk == RiskHigh && !plan.AllowsC2LifecycleSingleActorApproval() {
		if UsesTwoPartyApproval(plan.ApprovalPolicy) {
			// A two-party plan is executable only after an independent
			// approver has been durably recorded.  Treat a malformed or
			// partially migrated row as unexecutable rather than allowing the
			// creator through on the actor check alone.
			return actor != "" && actor == plan.ActorHash &&
				plan.ApprovedByHash != "" && plan.ApprovedByHash != plan.ActorHash
		}
		return IndependentExecutor(actor, plan.ActorHash, plan.ApprovedByHash, plan.SecondApprovedByHash)
	}
	return actor != "" && actor == plan.ActorHash
}

type ReleasePlan struct {
	ID                           string          `json:"id"`
	ActorHash                    string          `json:"actorHash"`
	Service                      string          `json:"service"`
	Action                       string          `json:"action"`
	Target                       string          `json:"target,omitempty"`
	TenantID                     string          `json:"tenantId"`
	ServerID                     string          `json:"serverId"`
	ScheduleAt                   *time.Time      `json:"scheduleAt,omitempty"`
	Risk                         Risk            `json:"risk"`
	State                        PlanState       `json:"state"`
	Digest                       string          `json:"digest"`
	ApprovalSummary              ApprovalSummary `json:"approvalSummary"`
	ApprovalPolicy               string          `json:"approvalPolicy,omitempty"`
	ConfirmationPhrase           string          `json:"confirmationPhrase,omitempty"`
	RequiresConfirmation         bool            `json:"requiresConfirmation"`
	ApprovedByHash               string          `json:"approvedByHash,omitempty"`
	SecondApprovedByHash         string          `json:"secondApprovedByHash,omitempty"`
	RequiresDualApproval         bool            `json:"requiresDualApproval,omitempty"`
	RequestIdempotencyKey        string          `json:"-"`
	RequestDigest                string          `json:"-"`
	RestoreMode                  string          `json:"restoreMode,omitempty"`
	RecoveryPointID              string          `json:"recoveryPointId,omitempty"`
	RestoreTenantID              string          `json:"restoreTenantId,omitempty"`
	RestoreServerID              string          `json:"restoreServerId,omitempty"`
	RestoreExpectedBeforeDigest  string          `json:"restoreExpectedBeforeDigest,omitempty"`
	RestoreContractDigest        string          `json:"restoreContractDigest,omitempty"`
	RestoreRevalidationDigest    string          `json:"restoreRevalidationDigest,omitempty"`
	RestoreRevalidatedAt         *time.Time      `json:"restoreRevalidatedAt,omitempty"`
	ExecutedByHash               string          `json:"executedByHash,omitempty"`
	RestoreOutcome               string          `json:"restoreOutcome,omitempty"`
	RestoreEvidenceDigest        string          `json:"restoreEvidenceDigest,omitempty"`
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

func EffectiveFailureSemantics(action ActionDefinition, phase string, productionChanged bool) PhaseSemantics {
	semantics := EffectivePhaseSemantics(action, phase)
	if _, explicit := action.PhaseSemantics[phase]; explicit || !productionChanged {
		return semantics
	}
	for _, candidate := range action.Steps {
		if candidate == phase {
			break
		}
		candidateSemantics := EffectivePhaseSemantics(action, candidate)
		if candidateSemantics.Effect == "runtime_mutation" || candidateSemantics.Effect == "data_mutation" {
			semantics = candidateSemantics
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
	SchemaVersion        int                `json:"schemaVersion"`
	Service              string             `json:"service"`
	TaskID               string             `json:"taskId"`
	TenantID             string             `json:"tenantId,omitempty"`
	ServerID             string             `json:"serverId,omitempty"`
	ExpectedBeforeDigest string             `json:"expectedBeforeDigest,omitempty"`
	BindingDigest        string             `json:"bindingDigest,omitempty"`
	CreatedAt            time.Time          `json:"createdAt"`
	Artifacts            []RecoveryArtifact `json:"artifacts"`
}

type RecoveryPointPolicy struct {
	RequiredArtifactRoles []string `json:"requiredArtifactRoles"`
	RecoverableSeconds    int      `json:"recoverableSeconds"`
}

type RecoveryPoint struct {
	ID                    string                `json:"id"`
	TaskID                string                `json:"taskId"`
	Service               string                `json:"service"`
	TenantID              string                `json:"tenantId,omitempty"`
	ServerID              string                `json:"serverId,omitempty"`
	Status                string                `json:"status"`
	Evidence              RecoveryPointEvidence `json:"evidence"`
	EvidenceDigest        string                `json:"evidenceDigest"`
	ExpectedBeforeDigest  string                `json:"expectedBeforeDigest"`
	ExpectedBefore        map[string]any        `json:"expectedBefore,omitempty"`
	BindingDigest         string                `json:"bindingDigest,omitempty"`
	RequiredArtifactRoles []string              `json:"requiredArtifactRoles"`
	CreatedAt             time.Time             `json:"createdAt"`
	VerifiedAt            *time.Time            `json:"verifiedAt,omitempty"`
	RecoverableUntil      *time.Time            `json:"recoverableUntil,omitempty"`
	RestoreOutcome        string                `json:"restoreOutcome,omitempty"`
	RestoreEvidenceDigest string                `json:"restoreEvidenceDigest,omitempty"`
	OutcomeAt             *time.Time            `json:"outcomeAt,omitempty"`
}

type ComposeServiceRuntime struct {
	ControlledCompose        string   `json:"controlledCompose"`
	RuntimeCompose           string   `json:"runtimeCompose"`
	EnvFile                  string   `json:"envFile"`
	ProjectName              string   `json:"projectName"`
	ApplicationService       string   `json:"applicationService"`
	ApplicationContainer     string   `json:"applicationContainer"`
	DependencyServices       []string `json:"dependencyServices,omitempty"`
	DependencyContainers     []string `json:"dependencyContainers,omitempty"`
	ProposalTTLSeconds       int      `json:"proposalTtlSeconds,omitempty"`
	HealthURL                string   `json:"healthUrl"`
	ReleaseRepository        string   `json:"releaseRepository"`
	ReleaseCatalog           string   `json:"releaseCatalog"`
	PreparedReleaseDir       string   `json:"preparedReleaseDir"`
	InspectExecutable        string   `json:"inspectExecutable"`
	BackupExecutables        []string `json:"backupExecutables,omitempty"`
	BackupEvidenceExecutable string   `json:"backupEvidenceExecutable,omitempty"`
	RestoreDrillExecutable   string   `json:"restoreDrillExecutable,omitempty"`
	RestoreExecutable        string   `json:"restoreExecutable,omitempty"`
	PrepareExecutable        string   `json:"prepareExecutable,omitempty"`
	UpdateExecutable         string   `json:"updateExecutable,omitempty"`
}

// TrafficPolicy defines the only Nginx surface a service lifecycle action may
// change. All paths and hostnames are operator-owned declarations; request
// payloads never supply them.
type TrafficPolicy struct {
	AdapterPath      string `json:"adapterPath"`
	SiteFile         string `json:"siteFile"`
	IncludeFile      string `json:"includeFile"`
	Hostname         string `json:"hostname"`
	MaintenanceFile  string `json:"maintenanceFile"`
	Marker           string `json:"marker"`
	DrainTimeoutSecs int    `json:"drainTimeoutSeconds"`
}

// trafficPolicyDigestPayload intentionally enumerates the adapter wire
// contract. This keeps unrelated future fields out of the remote execution
// identity and makes the digest stable across JSON map ordering.
type trafficPolicyDigestPayload struct {
	AdapterPath      string `json:"adapterPath"`
	SiteFile         string `json:"siteFile"`
	IncludeFile      string `json:"includeFile"`
	Hostname         string `json:"hostname"`
	MaintenanceFile  string `json:"maintenanceFile"`
	Marker           string `json:"marker"`
	DrainTimeoutSecs int    `json:"drainTimeoutSeconds"`
}

// TrafficPolicyDigest returns a stable SHA-256 digest for the seven-field
// traffic adapter contract. Call it after config validation, which fills the
// fixed adapter path and default drain timeout.
func TrafficPolicyDigest(policy TrafficPolicy) string {
	payload := trafficPolicyDigestPayload{
		AdapterPath:      policy.AdapterPath,
		SiteFile:         policy.SiteFile,
		IncludeFile:      policy.IncludeFile,
		Hostname:         policy.Hostname,
		MaintenanceFile:  policy.MaintenanceFile,
		Marker:           policy.Marker,
		DrainTimeoutSecs: policy.DrainTimeoutSecs,
	}
	data, _ := json.Marshal(payload)
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

type ObjectMetadata struct {
	Type        string `json:"type"`
	Environment string `json:"environment"`
	Owner       string `json:"owner"`
	Criticality string `json:"criticality"`
	Lifecycle   string `json:"lifecycle"`
	Maturity    string `json:"maturity"`
}

type AdapterDefinition struct {
	Path         string   `json:"path"`
	AllowedTypes []string `json:"allowedTypes"`
}

type AutomaticTaskRuntime struct {
	Schedule         string `json:"schedule"`
	ScheduleSource   string `json:"scheduleSource"`
	FreshnessSeconds int    `json:"freshnessSeconds"`
}

type ServiceDefinition struct {
	Name                   string                      `json:"name"`
	ObjectID               string                      `json:"objectId"`
	Metadata               ObjectMetadata              `json:"metadata"`
	DisplayName            string                      `json:"displayName"`
	Description            string                      `json:"description"`
	Template               string                      `json:"template"`
	Adapter                string                      `json:"adapter,omitempty"`
	AdapterRef             string                      `json:"adapterRef,omitempty"`
	AdapterContractVersion int                         `json:"-"`
	Runtime                *ComposeServiceRuntime      `json:"runtime,omitempty"`
	AutomaticTask          *AutomaticTaskRuntime       `json:"automaticTask,omitempty"`
	RecoveryPointPolicy    *RecoveryPointPolicy        `json:"recoveryPointPolicy,omitempty"`
	AlertPolicy            AlertPolicyDefinition       `json:"alertPolicy"`
	Actions                map[string]ActionDefinition `json:"actions"`
	// TenantID/ServerID are optional in legacy catalogs and become required
	// when the multi-tenant/fleet policy is enabled.
	TenantID            string                 `json:"tenantId,omitempty"`
	ServerID            string                 `json:"serverId,omitempty"`
	Capabilities        []string               `json:"capabilities,omitempty"`
	StatePolicy         *StatePolicyDefinition `json:"statePolicy,omitempty"`
	TrafficPolicy       *TrafficPolicy         `json:"trafficPolicy,omitempty"`
	TrafficPolicyDigest string                 `json:"trafficPolicyDigest,omitempty"`
	// AutoUpdate is deliberately separate from the service action map. An
	// enabled policy may create a release plan, but it never bypasses the
	// normal preview, approval, backup, and observation gates.
	AutoUpdate *AutoUpdatePolicy `json:"autoUpdate,omitempty"`
}

// PolicyDigest returns the normalized traffic policy digest carried by a
// service declaration. The fallback keeps manually constructed legacy test
// catalogs useful while validated catalogs retain the explicit field.
func (service ServiceDefinition) PolicyDigest() string {
	if service.TrafficPolicy == nil {
		return ""
	}
	return TrafficPolicyDigest(*service.TrafficPolicy)
}

type ServiceView struct {
	Name                 string                      `json:"name"`
	ObjectID             string                      `json:"objectId"`
	Metadata             ObjectMetadata              `json:"metadata"`
	DisplayName          string                      `json:"displayName"`
	Description          string                      `json:"description"`
	ManagedCompose       bool                        `json:"managedCompose"`
	Actions              map[string]ActionDefinition `json:"actions"`
	Status               map[string]any              `json:"status,omitempty"`
	ReleaseDiscovery     map[string]any              `json:"releaseDiscovery,omitempty"`
	StatusError          string                      `json:"statusError,omitempty"`
	ActiveTaskID         string                      `json:"activeTaskId,omitempty"`
	RollbackSourceTaskID string                      `json:"rollbackSourceTaskId,omitempty"`
	TenantID             string                      `json:"tenantId,omitempty"`
	ServerID             string                      `json:"serverId,omitempty"`
	State                *ServiceState               `json:"state,omitempty"`
	Drift                *StateDrift                 `json:"drift,omitempty"`
}

type ManagedObjectView struct {
	Name         string                      `json:"name"`
	ObjectID     string                      `json:"objectId"`
	Metadata     ObjectMetadata              `json:"metadata"`
	DisplayName  string                      `json:"displayName"`
	Description  string                      `json:"description"`
	Actions      map[string]ActionDefinition `json:"actions"`
	Status       map[string]any              `json:"status,omitempty"`
	StatusError  string                      `json:"statusError,omitempty"`
	ActiveTaskID string                      `json:"activeTaskId,omitempty"`
	TenantID     string                      `json:"tenantId,omitempty"`
	ServerID     string                      `json:"serverId,omitempty"`
	State        *ServiceState               `json:"state,omitempty"`
	Drift        *StateDrift                 `json:"drift,omitempty"`
}

type AutomaticTaskView struct {
	ManagedObjectView
	Schedule         string `json:"schedule"`
	ScheduleSource   string `json:"scheduleSource"`
	FreshnessSeconds int    `json:"freshnessSeconds"`
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
	ID                          string           `json:"id"`
	IdempotencyKey              string           `json:"-"`
	RequestHash                 string           `json:"-"`
	ActorHash                   string           `json:"actorHash"`
	Service                     string           `json:"service"`
	Action                      string           `json:"action"`
	Target                      string           `json:"target,omitempty"`
	Risk                        Risk             `json:"risk"`
	State                       TaskState        `json:"state"`
	CurrentPhase                string           `json:"currentPhase,omitempty"`
	Summary                     string           `json:"summary,omitempty"`
	Error                       string           `json:"error,omitempty"`
	PreviewID                   string           `json:"previewId"`
	PlanID                      string           `json:"planId,omitempty"`
	PlanDigest                  string           `json:"planDigest,omitempty"`
	TrafficPolicyDigest         string           `json:"trafficPolicyDigest,omitempty"`
	ParentTaskID                string           `json:"parentTaskId,omitempty"`
	Snapshot                    map[string]any   `json:"snapshot,omitempty"`
	Stages                      []TaskStage      `json:"stages,omitempty"`
	RunnerOwner                 string           `json:"-"`
	HeartbeatAt                 *time.Time       `json:"heartbeatAt,omitempty"`
	ProductionChanged           bool             `json:"productionChanged"`
	Retryable                   bool             `json:"retryable"`
	FailureCode                 string           `json:"failureCode,omitempty"`
	RollbackAvailable           bool             `json:"rollbackAvailable"`
	RollbackReason              string           `json:"rollbackReason,omitempty"`
	RecoveryPointID             string           `json:"recoveryPointId,omitempty"`
	RestoreMode                 string           `json:"restoreMode,omitempty"`
	RestoreTenantID             string           `json:"restoreTenantId,omitempty"`
	RestoreServerID             string           `json:"restoreServerId,omitempty"`
	RestoreExpectedBeforeDigest string           `json:"restoreExpectedBeforeDigest,omitempty"`
	RestoreContractDigest       string           `json:"restoreContractDigest,omitempty"`
	RestoreRevalidatedAt        *time.Time       `json:"restoreRevalidatedAt,omitempty"`
	RestoreOutcome              string           `json:"restoreOutcome,omitempty"`
	RestoreEvidenceDigest       string           `json:"restoreEvidenceDigest,omitempty"`
	RecoveryActions             []RecoveryAction `json:"recoveryActions,omitempty"`
	CreatedAt                   time.Time        `json:"createdAt"`
	StartedAt                   *time.Time       `json:"startedAt,omitempty"`
	FinishedAt                  *time.Time       `json:"finishedAt,omitempty"`
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
	Service                     string     `json:"service"`
	Action                      string     `json:"action"`
	Target                      string     `json:"target,omitempty"`
	IdempotencyKey              string     `json:"idempotencyKey,omitempty"`
	RequestDigest               string     `json:"-"`
	ScheduleAt                  *time.Time `json:"scheduleAt,omitempty"`
	RestoreMode                 string     `json:"-"`
	RecoveryPointID             string     `json:"-"`
	RequiresDualApproval        bool       `json:"-"`
	RestoreTenantID             string     `json:"-"`
	RestoreServerID             string     `json:"-"`
	RestoreExpectedBeforeDigest string     `json:"-"`
	RestoreContractDigest       string     `json:"-"`
	RestoreEvidenceDigest       string     `json:"-"`
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
	Action         string `json:"action"`
	IdempotencyKey string `json:"idempotencyKey,omitempty"`
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
