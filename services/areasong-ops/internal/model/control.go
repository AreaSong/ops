package model

import (
	"strings"
	"time"
)

// Permission is intentionally action-oriented.  A policy can grant a narrow
// capability without granting the broader administrative role that contains
// it in the UI.
type Permission string

const (
	PermissionRead         Permission = "ops.read"
	PermissionInspect      Permission = "ops.inspect"
	PermissionLifecycle    Permission = "ops.lifecycle"
	PermissionDeploy       Permission = "ops.deploy"
	PermissionBatch        Permission = "ops.batch"
	PermissionRecover      Permission = "ops.recover"
	PermissionManageFleet  Permission = "fleet.manage"
	PermissionManageAccess Permission = "access.manage"
	PermissionManageConfig Permission = "config.manage"
	PermissionBreakGlass   Permission = "break_glass"
	PermissionRunnerUpdate Permission = "runner.update"
)

type Tenant struct {
	ID          string    `json:"id"`
	DisplayName string    `json:"displayName"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
	// CreatedBy is persisted so bootstrap-owned records cannot be replaced by
	// a control-plane actor with the same identifier.
	CreatedBy string `json:"createdBy,omitempty"`
}

type Role struct {
	ID          string       `json:"id"`
	DisplayName string       `json:"displayName"`
	Permissions []Permission `json:"permissions"`
	BuiltIn     bool         `json:"builtIn"`
	CreatedAt   time.Time    `json:"createdAt,omitempty"`
	UpdatedAt   time.Time    `json:"updatedAt,omitempty"`
	CreatedBy   string       `json:"createdBy,omitempty"`
}

// AccessPrincipal is the durable subject record used by Runner authorization.
// Email is optional and is never used as the authorization key; Subject is the
// normalized SHA-256 identity sent by the Web tier.
type AccessPrincipal struct {
	Subject   string     `json:"subject"`
	Email     string     `json:"email,omitempty"`
	EmailHash string     `json:"emailHash,omitempty"`
	TenantID  string     `json:"tenantId"`
	Roles     []string   `json:"roles"`
	Status    string     `json:"status,omitempty"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
	JIT       bool       `json:"jit,omitempty"`
	CreatedAt time.Time  `json:"createdAt,omitempty"`
	UpdatedAt time.Time  `json:"updatedAt,omitempty"`
	CreatedBy string     `json:"createdBy,omitempty"`
}

// Principal is kept as a readable alias for callers that use the shorter
// RBAC vocabulary.
type Principal = AccessPrincipal

type RoleBinding struct {
	ID                   string     `json:"id"`
	Subject              string     `json:"subject"`
	TenantID             string     `json:"tenantId"`
	RoleID               string     `json:"roleId"`
	ObjectIDs            []string   `json:"objectIds,omitempty"`
	ExpiresAt            *time.Time `json:"expiresAt,omitempty"`
	JIT                  bool       `json:"jit,omitempty"`
	RequiresDualApproval bool       `json:"requiresDualApproval,omitempty"`
	ApprovalState        string     `json:"approvalState,omitempty"`
	ApprovedByHash       string     `json:"approvedByHash,omitempty"`
	SecondApprovedByHash string     `json:"secondApprovedByHash,omitempty"`
	ApprovedAt           *time.Time `json:"approvedAt,omitempty"`
	SecondApprovedAt     *time.Time `json:"secondApprovedAt,omitempty"`
	CreatedAt            time.Time  `json:"createdAt"`
	UpdatedAt            time.Time  `json:"updatedAt,omitempty"`
	CreatedBy            string     `json:"createdBy"`
}

type AccessSubject struct {
	Email    string `json:"email"`
	Subject  string `json:"subject"`
	TenantID string `json:"tenantId"`
}

type AuthorizationDecision struct {
	Allowed    bool       `json:"allowed"`
	Permission Permission `json:"permission"`
	TenantID   string     `json:"tenantId"`
	ObjectID   string     `json:"objectId,omitempty"`
	Reason     string     `json:"reason,omitempty"`
}

type AccessControlView struct {
	Enforced       bool              `json:"enforced"`
	CanManage      bool              `json:"canManage"`
	DefaultTenant  string            `json:"defaultTenant,omitempty"`
	Principals     map[string]any    `json:"principals,omitempty"`
	PrincipalList  []AccessPrincipal `json:"principalList,omitempty"`
	Tenants        []Tenant          `json:"tenants,omitempty"`
	Roles          []Role            `json:"roles,omitempty"`
	Bindings       []RoleBinding     `json:"bindings,omitempty"`
	CurrentSubject AccessSubject     `json:"currentSubject,omitempty"`
	Version        int64             `json:"version,omitempty"`
	Digest         string            `json:"digest,omitempty"`
	PendingChanges []AccessChange    `json:"pendingChanges,omitempty"`
}

type AccessControlUpdateRequest struct {
	Enforced                *bool             `json:"enforced,omitempty"`
	Tenants                 []Tenant          `json:"tenants,omitempty"`
	Roles                   []Role            `json:"roles,omitempty"`
	Principals              []AccessPrincipal `json:"principals,omitempty"`
	Bindings                []RoleBinding     `json:"bindings,omitempty"`
	RemoveTenantIDs         []string          `json:"removeTenantIds,omitempty"`
	RemoveRoleIDs           []string          `json:"removeRoleIds,omitempty"`
	RemovePrincipalSubjects []string          `json:"removePrincipalSubjects,omitempty"`
	RemoveBindingIDs        []string          `json:"removeBindingIds,omitempty"`
	RequiresDualApproval    bool              `json:"requiresDualApproval,omitempty"`
	Confirmation            string            `json:"confirmation,omitempty"`
	ExpectedVersion         int64             `json:"expectedVersion,omitempty"`
	IdempotencyKey          string            `json:"idempotencyKey"`
}

type AccessChangeState string

const (
	AccessChangePendingApproval AccessChangeState = "pending_approval"
	AccessChangeApproved        AccessChangeState = "approved"
	AccessChangeApplied         AccessChangeState = "applied"
	AccessChangeRejected        AccessChangeState = "rejected"
)

// AccessChange is an immutable request envelope for high-risk/JIT RBAC
// mutations. The payload is retained by the Runner store and applied only
// after an independent approver confirms its digest and phrase.
type AccessChange struct {
	ID                   string            `json:"id"`
	IdempotencyKey       string            `json:"-"`
	RequestDigest        string            `json:"requestDigest"`
	ActorHash            string            `json:"actorHash"`
	State                AccessChangeState `json:"state"`
	ConfirmationPhrase   string            `json:"confirmationPhrase"`
	ApprovedByHash       string            `json:"approvedByHash,omitempty"`
	SecondApprovedByHash string            `json:"secondApprovedByHash,omitempty"`
	RequiresDualApproval bool              `json:"requiresDualApproval"`
	ApprovalPolicy       string            `json:"approvalPolicy,omitempty"`
	Version              int64             `json:"version,omitempty"`
	Error                string            `json:"error,omitempty"`
	CreatedAt            time.Time         `json:"createdAt"`
	ApprovedAt           *time.Time        `json:"approvedAt,omitempty"`
	SecondApprovedAt     *time.Time        `json:"secondApprovedAt,omitempty"`
	AppliedByHash        string            `json:"appliedByHash,omitempty"`
	AppliedPolicyDigest  string            `json:"appliedPolicyDigest,omitempty"`
	AppliedPolicyVersion int64             `json:"appliedPolicyVersion,omitempty"`
	AppliedAt            *time.Time        `json:"appliedAt,omitempty"`
}

type AccessChangeApprovalRequest struct {
	Digest         string `json:"digest"`
	Confirmation   string `json:"confirmation"`
	IdempotencyKey string `json:"idempotencyKey,omitempty"`
}

type AccessPolicySnapshot struct {
	Version    int64     `json:"version"`
	Digest     string    `json:"digest"`
	PolicyJSON string    `json:"-"`
	ActorHash  string    `json:"actorHash,omitempty"`
	CreatedAt  time.Time `json:"createdAt"`
}

type ExtensionPolicyUpdateRequest struct {
	Enabled          *bool  `json:"enabled,omitempty"`
	RequireSignature *bool  `json:"requireSignature,omitempty"`
	Sandbox          string `json:"sandbox,omitempty"`
	IdempotencyKey   string `json:"idempotencyKey"`
}

func (role Role) Allows(permission Permission) bool {
	for _, candidate := range role.Permissions {
		if candidate == permission || candidate == Permission("*") {
			return true
		}
	}
	return false
}

type AutoUpdatePolicy struct {
	Enabled             bool   `json:"enabled"`
	Channel             string `json:"channel"`
	MaintenanceWindow   string `json:"maintenanceWindow,omitempty"`
	MaintenanceTimezone string `json:"maintenanceTimezone,omitempty"`
	CanaryPercent       int    `json:"canaryPercent,omitempty"`
	MaxUnavailable      int    `json:"maxUnavailable,omitempty"`
	RequireBackup       bool   `json:"requireBackup"`
	RequireApproval     bool   `json:"requireApproval"`
	RollbackOnAlert     bool   `json:"rollbackOnAlert"`
	ObservationSeconds  int    `json:"observationSeconds"`
}

type RecoveryCenterView struct {
	Service            string           `json:"service"`
	Latest             *RecoveryPoint   `json:"latest,omitempty"`
	DrillLastSuccessAt *time.Time       `json:"drillLastSuccessAt,omitempty"`
	DrillFresh         bool             `json:"drillFresh"`
	DrillReason        string           `json:"drillReason,omitempty"`
	AvailableActions   []RecoveryAction `json:"availableActions,omitempty"`
}

type RestoreRequest struct {
	Service              string `json:"service"`
	RecoveryPointID      string `json:"recoveryPointId"`
	TenantID             string `json:"tenantId,omitempty"`
	ServerID             string `json:"serverId,omitempty"`
	ExpectedBeforeDigest string `json:"expectedBeforeDigest,omitempty"`
	Mode                 string `json:"mode"` // isolated or production
	Confirmation         string `json:"confirmation"`
	IdempotencyKey       string `json:"idempotencyKey"`
}

type DesiredStateRequest struct {
	Desired        DesiredState `json:"desiredState"`
	Reason         string       `json:"reason,omitempty"`
	MaintenanceTTL int          `json:"maintenanceTtlSeconds,omitempty"`
	Confirmation   string       `json:"confirmation,omitempty"`
	IdempotencyKey string       `json:"idempotencyKey"`
}

// DesiredStateRequestReceipt 记录目标状态幂等请求的持久化结果。
// receipt 与 ServiceState 分离，重放请求时不会改动当前状态。
type DesiredStateRequestReceipt struct {
	IdempotencyKey string       `json:"idempotencyKey"`
	ActorHash      string       `json:"actorHash"`
	RequestDigest  string       `json:"requestDigest"`
	Service        string       `json:"service"`
	State          ServiceState `json:"state"`
	Generation     int64        `json:"generation"`
	EventSequence  int64        `json:"eventSequence"`
	CreatedAt      time.Time    `json:"createdAt"`
}

type ReconcileRequest struct {
	Apply bool `json:"apply"`
}

type ComposeRevision struct {
	ID                            string             `json:"id"`
	IdempotencyKey                string             `json:"-"`
	Service                       string             `json:"service"`
	Digest                        string             `json:"digest"`
	ExpectedDigest                string             `json:"expectedDigest,omitempty"`
	Source                        string             `json:"source"`
	Content                       string             `json:"content,omitempty"`
	Validated                     bool               `json:"validated"`
	State                         string             `json:"state,omitempty"`
	ActorHash                     string             `json:"actorHash,omitempty"`
	ConfirmationPhrase            string             `json:"confirmationPhrase,omitempty"`
	ApprovedBy                    string             `json:"approvedBy,omitempty"`
	SecondApprovedByHash          string             `json:"secondApprovedByHash,omitempty"`
	ApprovalPolicy                string             `json:"approvalPolicy,omitempty"`
	AppliedByHash                 string             `json:"appliedByHash,omitempty"`
	ApplyIdempotencyKey           string             `json:"-"`
	RollbackIdempotencyKey        string             `json:"-"`
	BackupControlledPath          string             `json:"backupControlledPath,omitempty"`
	BackupRuntimePath             string             `json:"backupRuntimePath,omitempty"`
	Error                         string             `json:"error,omitempty"`
	CreatedAt                     time.Time          `json:"createdAt"`
	ApprovedAt                    *time.Time         `json:"approvedAt,omitempty"`
	SecondApprovedAt              *time.Time         `json:"secondApprovedAt,omitempty"`
	AppliedAt                     *time.Time         `json:"appliedAt,omitempty"`
	FinishedAt                    *time.Time         `json:"finishedAt,omitempty"`
	TenantID                      string             `json:"tenantId,omitempty"`
	ServerID                      string             `json:"serverId,omitempty"`
	ProjectName                   string             `json:"projectName,omitempty"`
	BaselineSemanticDigest        string             `json:"baselineSemanticDigest,omitempty"`
	CandidateSemanticDigest       string             `json:"candidateSemanticDigest,omitempty"`
	BaselineEffectiveDigest       string             `json:"baselineEffectiveDigest,omitempty"`
	CandidateEffectiveDigest      string             `json:"candidateEffectiveDigest,omitempty"`
	EnvFileDigest                 string             `json:"envFileDigest,omitempty"`
	SemanticDiff                  []ComposeDiffEntry `json:"semanticDiff,omitempty"`
	PolicyDigest                  string             `json:"policyDigest,omitempty"`
	RecoveryPointID               string             `json:"recoveryPointId,omitempty"`
	RecoveryPointExpectedDigest   string             `json:"recoveryPointExpectedBeforeDigest,omitempty"`
	RecoveryPointBindingDigest    string             `json:"recoveryPointBindingDigest,omitempty"`
	RecoveryPointEvidenceDigest   string             `json:"recoveryPointEvidenceDigest,omitempty"`
	RecoveryPointVerifiedAt       *time.Time         `json:"recoveryPointVerifiedAt,omitempty"`
	RecoveryPointRecoverableUntil *time.Time         `json:"recoveryPointRecoverableUntil,omitempty"`
	AlertEvidenceDigest           string             `json:"alertEvidenceDigest,omitempty"`
	BlockingAlertFingerprints     []string           `json:"blockingAlertFingerprints,omitempty"`
	AlertCheckedAt                *time.Time         `json:"alertCheckedAt,omitempty"`
	ExpiresAt                     time.Time          `json:"expiresAt,omitempty"`
	ExpectedRuntimeIdentityDigest string             `json:"expectedRuntimeIdentityDigest,omitempty"`
	ExpectedRuntimeImage          string             `json:"expectedRuntimeImage,omitempty"`
	ExpectedRuntimeImageID        string             `json:"expectedRuntimeImageId,omitempty"`
	CandidateImage                string             `json:"candidateImage,omitempty"`
	CandidateImageDigest          string             `json:"candidateImageDigest,omitempty"`
	CandidateImageID              string             `json:"candidateImageId,omitempty"`
	AppliedRuntimeIdentityDigest  string             `json:"appliedRuntimeIdentityDigest,omitempty"`
	RolledBackByHash              string             `json:"rolledBackByHash,omitempty"`
	RollbackStartedAt             *time.Time         `json:"rollbackStartedAt,omitempty"`
	RollbackFinishedAt            *time.Time         `json:"rollbackFinishedAt,omitempty"`
}

// ComposeDiffEntry is deliberately restricted to non-secret approval facts.
// The semantic projection may hash sensitive values, but they are never copied
// into the browser-visible diff.
type ComposeDiffEntry struct {
	Path   string `json:"path"`
	Change string `json:"change"`
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
}

type ComposeEditRequest struct {
	Service         string `json:"service"`
	Content         string `json:"content"`
	ExpectedDigest  string `json:"expectedDigest"`
	Mode            string `json:"mode"` // validate or propose
	Confirmation    string `json:"confirmation,omitempty"`
	IdempotencyKey  string `json:"idempotencyKey"`
	RecoveryPointID string `json:"recoveryPointId,omitempty"`
}

type ComposeExecutionGate struct {
	PolicyDigest                  string    `json:"policyDigest"`
	RecoveryPointID               string    `json:"recoveryPointId"`
	RecoveryPointExpectedDigest   string    `json:"recoveryPointExpectedBeforeDigest"`
	RecoveryPointBindingDigest    string    `json:"recoveryPointBindingDigest"`
	RecoveryPointEvidenceDigest   string    `json:"recoveryPointEvidenceDigest"`
	AlertEvidenceDigest           string    `json:"alertEvidenceDigest"`
	BlockingAlertFingerprints     []string  `json:"blockingAlertFingerprints,omitempty"`
	CheckedAt                     time.Time `json:"checkedAt"`
	ExpectedRuntimeIdentityDigest string    `json:"expectedRuntimeIdentityDigest"`
}

type ComposeApprovalRequest struct {
	Digest       string `json:"digest"`
	Confirmation string `json:"confirmation"`
}

type ComposeApplyRequest struct {
	IdempotencyKey string `json:"idempotencyKey"`
}

type ComposeRollbackRequest struct {
	Confirmation   string `json:"confirmation"`
	IdempotencyKey string `json:"idempotencyKey"`
}

type ComposeFileView struct {
	Service              string            `json:"service"`
	ControlledPath       string            `json:"controlledPath"`
	RuntimePath          string            `json:"runtimePath"`
	Digest               string            `json:"digest"`
	Content              string            `json:"content"`
	Source               string            `json:"source"`
	Validated            bool              `json:"validated"`
	ValidationError      string            `json:"validationError,omitempty"`
	ControlledCompose    string            `json:"controlledCompose,omitempty"`
	RuntimeCompose       string            `json:"runtimeCompose,omitempty"`
	EnvFile              string            `json:"envFile,omitempty"`
	ApplicationService   string            `json:"applicationService,omitempty"`
	ApplicationContainer string            `json:"applicationContainer,omitempty"`
	ProjectName          string            `json:"projectName,omitempty"`
	TenantID             string            `json:"tenantId,omitempty"`
	ServerID             string            `json:"serverId,omitempty"`
	DependencyServices   []string          `json:"dependencyServices,omitempty"`
	DependencyContainers []string          `json:"dependencyContainers,omitempty"`
	HealthURL            string            `json:"healthUrl,omitempty"`
	ProposalTTLSeconds   int               `json:"proposalTtlSeconds,omitempty"`
	RecoveryPoints       []RecoveryPoint   `json:"recoveryPoints,omitempty"`
	Revisions            []ComposeRevision `json:"revisions,omitempty"`
}

type KubernetesTarget struct {
	Cluster       string   `json:"cluster"`
	Context       string   `json:"context"`
	Namespace     string   `json:"namespace"`
	TenantID      string   `json:"tenantId,omitempty"`
	Allowlist     []string `json:"allowlist,omitempty"`
	ResourceKinds []string `json:"resourceKinds,omitempty"`
}

type KubernetesOperation struct {
	ID               string           `json:"id"`
	IdempotencyKey   string           `json:"idempotencyKey,omitempty"`
	RequestDigest    string           `json:"requestDigest,omitempty"`
	Target           KubernetesTarget `json:"target"`
	TenantID         string           `json:"tenantId,omitempty"`
	Action           string           `json:"action"`
	ManifestDigest   string           `json:"manifestDigest,omitempty"`
	DryRun           bool             `json:"dryRun"`
	State            string           `json:"state"`
	Phase            string           `json:"phase,omitempty"`
	RolloutState     string           `json:"rolloutState,omitempty"`
	RolloutResources []string         `json:"rolloutResources,omitempty"`
	RollbackOfPlanID string           `json:"rollbackOfPlanId,omitempty"`
	Error            string           `json:"error,omitempty"`
	CreatedAt        time.Time        `json:"createdAt"`
	FinishedAt       *time.Time       `json:"finishedAt,omitempty"`
}

// KubernetesPlan is the durable approval boundary for a production apply.
// The manifest is retained only by the root-owned Runner store and is never
// returned by API views; all approval and execution decisions bind to its
// immutable digest.
type KubernetesPlan struct {
	ID                    string           `json:"id"`
	IdempotencyKey        string           `json:"idempotencyKey,omitempty"`
	RequestDigest         string           `json:"requestDigest,omitempty"`
	ActorHash             string           `json:"actorHash"`
	TenantID              string           `json:"tenantId,omitempty"`
	Target                KubernetesTarget `json:"target"`
	ManifestDigest        string           `json:"manifestDigest"`
	Action                string           `json:"action"`
	State                 string           `json:"state"`
	RollbackOfPlanID      string           `json:"rollbackOfPlanId,omitempty"`
	RollbackTargetPlanID  string           `json:"rollbackTargetPlanId,omitempty"`
	SourceManifestDigest  string           `json:"sourceManifestDigest,omitempty"`
	ConfirmationPhrase    string           `json:"confirmationPhrase,omitempty"`
	ApprovedByHash        string           `json:"approvedByHash,omitempty"`
	SecondApprovedByHash  string           `json:"secondApprovedByHash,omitempty"`
	RequiresDualApproval  bool             `json:"requiresDualApproval"`
	ApprovalPolicy        string           `json:"approvalPolicy,omitempty"`
	OperationID           string           `json:"operationId,omitempty"`
	ExecuteIdempotencyKey string           `json:"executeIdempotencyKey,omitempty"`
	ExecutedByHash        string           `json:"executedByHash,omitempty"`
	Error                 string           `json:"error,omitempty"`
	CreatedAt             time.Time        `json:"createdAt"`
	ApprovedAt            *time.Time       `json:"approvedAt,omitempty"`
	SecondApprovedAt      *time.Time       `json:"secondApprovedAt,omitempty"`
	StartedAt             *time.Time       `json:"startedAt,omitempty"`
	FinishedAt            *time.Time       `json:"finishedAt,omitempty"`
}

type KubernetesPlanRequest struct {
	Target         KubernetesTarget `json:"target"`
	Manifest       string           `json:"manifest"`
	IdempotencyKey string           `json:"idempotencyKey"`
}

// KubernetesRollbackPlanRequest names an immutable, previously successful
// plan. The manifest itself is always loaded from the root-owned Runner store.
type KubernetesRollbackPlanRequest struct {
	RollbackToPlanID string `json:"rollbackToPlanId"`
	IdempotencyKey   string `json:"idempotencyKey"`
}

type KubernetesPlanApprovalRequest struct {
	Digest       string `json:"digest"`
	Confirmation string `json:"confirmation"`
}

type KubernetesPlanExecuteRequest struct {
	IdempotencyKey string `json:"idempotencyKey"`
}

type KubernetesRequest struct {
	Target           KubernetesTarget `json:"target"`
	Action           string           `json:"action"`
	Manifest         string           `json:"manifest"`
	DryRun           bool             `json:"dryRun"`
	Confirmation     string           `json:"confirmation,omitempty"`
	IdempotencyKey   string           `json:"idempotencyKey"`
	RollbackOfPlanID string           `json:"rollbackOfPlanId,omitempty"`
}

type TerminalCommand struct {
	Name           string   `json:"name"`
	Executable     string   `json:"executable"`
	Arguments      []string `json:"arguments,omitempty"`
	ReadOnly       bool     `json:"readOnly"`
	TimeoutSeconds int      `json:"timeoutSeconds"`
}

type TerminalSession struct {
	ID             string    `json:"id"`
	IdempotencyKey string    `json:"idempotencyKey,omitempty"`
	RequestDigest  string    `json:"requestDigest,omitempty"`
	ObjectID       string    `json:"objectId"`
	Command        string    `json:"command"`
	State          string    `json:"state"`
	ActorHash      string    `json:"actorHash"`
	ExitCode       int       `json:"exitCode"`
	Output         string    `json:"output,omitempty"`
	ExpiresAt      time.Time `json:"expiresAt"`
	CreatedAt      time.Time `json:"createdAt"`
}

type TerminalStartRequest struct {
	ObjectID       string `json:"objectId"`
	Command        string `json:"command"`
	Confirmation   string `json:"confirmation,omitempty"`
	IdempotencyKey string `json:"idempotencyKey"`
}

type TerminalOutput struct {
	SessionID string `json:"sessionId"`
	ExitCode  int    `json:"exitCode"`
	Output    string `json:"output"`
	State     string `json:"state"`
}

type ManagedFileView struct {
	RootID      string             `json:"rootId"`
	Path        string             `json:"path"`
	Digest      string             `json:"digest,omitempty"`
	Size        int64              `json:"size"`
	Content     string             `json:"content,omitempty"`
	ReadOnly    bool               `json:"readOnly"`
	IsDirectory bool               `json:"isDirectory"`
	Entries     []ManagedFileEntry `json:"entries,omitempty"`
}

type ManagedFileEntry struct {
	Name        string    `json:"name"`
	Path        string    `json:"path"`
	Size        int64     `json:"size"`
	IsDirectory bool      `json:"isDirectory"`
	ModifiedAt  time.Time `json:"modifiedAt"`
}

type ManagedFileRequest struct {
	RootID         string `json:"rootId"`
	Path           string `json:"path"`
	Content        string `json:"content,omitempty"`
	ExpectedDigest string `json:"expectedDigest,omitempty"`
	Mode           string `json:"mode"` // read or propose
	IdempotencyKey string `json:"idempotencyKey"`
}

type ManagedFileProposal struct {
	ID                     string     `json:"id"`
	IdempotencyKey         string     `json:"idempotencyKey"`
	ActorHash              string     `json:"actorHash"`
	RootID                 string     `json:"rootId"`
	Path                   string     `json:"path"`
	ExpectedDigest         string     `json:"expectedDigest"`
	ProposedDigest         string     `json:"proposedDigest"`
	Content                string     `json:"content,omitempty"`
	State                  string     `json:"state"`
	ConfirmationPhrase     string     `json:"confirmationPhrase,omitempty"`
	ApprovedByHash         string     `json:"approvedByHash,omitempty"`
	SecondApprovedByHash   string     `json:"secondApprovedByHash,omitempty"`
	ApprovalPolicy         string     `json:"approvalPolicy,omitempty"`
	AppliedByHash          string     `json:"appliedByHash,omitempty"`
	ApplyIdempotencyKey    string     `json:"-"`
	BackupPath             string     `json:"backupPath,omitempty"`
	RolledBackByHash       string     `json:"rolledBackByHash,omitempty"`
	RollbackIdempotencyKey string     `json:"-"`
	Error                  string     `json:"error,omitempty"`
	CreatedAt              time.Time  `json:"createdAt"`
	ApprovedAt             *time.Time `json:"approvedAt,omitempty"`
	SecondApprovedAt       *time.Time `json:"secondApprovedAt,omitempty"`
	AppliedAt              *time.Time `json:"appliedAt,omitempty"`
	FinishedAt             *time.Time `json:"finishedAt,omitempty"`
	RolledBackAt           *time.Time `json:"rolledBackAt,omitempty"`
}

type ManagedFileApprovalRequest struct {
	Digest       string `json:"digest"`
	Confirmation string `json:"confirmation"`
}

type ManagedFileApplyRequest struct {
	IdempotencyKey string `json:"idempotencyKey"`
}

type ManagedFileRollbackRequest struct {
	Confirmation   string `json:"confirmation"`
	IdempotencyKey string `json:"idempotencyKey"`
}

type ExtensionUploadRequest struct {
	Manifest       ExtensionManifest `json:"manifest"`
	Content        string            `json:"content"` // base64
	IdempotencyKey string            `json:"idempotencyKey"`
}

type ExtensionUploadResult struct {
	Manifest       ExtensionManifest `json:"manifest"`
	Stored         bool              `json:"stored"`
	State          string            `json:"state"`
	StorageDigest  string            `json:"storageDigest,omitempty"`
	IdempotencyKey string            `json:"idempotencyKey,omitempty"`
	CreatedAt      time.Time         `json:"createdAt,omitempty"`
	Reason         string            `json:"reason,omitempty"`
}

type ExtensionManifest struct {
	Purpose        string   `json:"purpose"`
	SchemaVersion  int      `json:"schemaVersion"`
	ID             string   `json:"id"`
	Version        string   `json:"version"`
	Type           string   `json:"type"` // script, wasm or plugin
	Entrypoint     string   `json:"entrypoint"`
	Digest         string   `json:"digest"`
	Signature      string   `json:"signature"`
	Permissions    []string `json:"permissions,omitempty"`
	AllowedObjects []string `json:"allowedObjects,omitempty"`
	Publisher      string   `json:"publisher"`
}

const (
	ExtensionManifestPurpose = "areasong-ops.extension"
	ExtensionManifestSchema  = 1
)

type RunnerUpdate struct {
	ID                         string                         `json:"id"`
	IdempotencyKey             string                         `json:"idempotencyKey,omitempty"`
	RequestDigest              string                         `json:"requestDigest,omitempty"`
	RunnerID                   string                         `json:"runnerId"`
	TargetVersion              string                         `json:"targetVersion"`
	ArtifactPath               string                         `json:"artifactPath,omitempty"`
	ArtifactDigest             string                         `json:"artifactDigest"`
	ArtifactRevision           string                         `json:"artifactRevision"`
	Publisher                  string                         `json:"publisher"`
	ArtifactSignature          string                         `json:"artifactSignature,omitempty"`
	ManifestPurpose            string                         `json:"manifestPurpose,omitempty"`
	ManifestSchema             int                            `json:"manifestSchema,omitempty"`
	ManifestGOOS               string                         `json:"manifestGoos,omitempty"`
	ManifestGOARCH             string                         `json:"manifestGoarch,omitempty"`
	StagedPath                 string                         `json:"-"`
	BinaryPath                 string                         `json:"-"`
	UnitName                   string                         `json:"-"`
	HealthTimeoutSeconds       int                            `json:"-"`
	State                      string                         `json:"state"`
	Phase                      string                         `json:"phase,omitempty"`
	PreparedByHash             string                         `json:"preparedByHash,omitempty"`
	PreviousVersion            string                         `json:"previousVersion,omitempty"`
	PreviousRevision           string                         `json:"previousRevision,omitempty"`
	PreviousDigest             string                         `json:"previousDigest,omitempty"`
	ConfirmationPhrase         string                         `json:"confirmationPhrase,omitempty"`
	ApprovedByHash             string                         `json:"approvedByHash,omitempty"`
	ActivationIdempotencyKey   string                         `json:"-"`
	ResolvedByHash             string                         `json:"resolvedByHash,omitempty"`
	ResolutionIdempotencyKey   string                         `json:"-"`
	CancelledByHash            string                         `json:"cancelledByHash,omitempty"`
	CancellationIdempotencyKey string                         `json:"-"`
	RollbackPath               string                         `json:"rollbackPath,omitempty"`
	Error                      string                         `json:"error,omitempty"`
	CreatedAt                  time.Time                      `json:"createdAt"`
	ActivatedAt                *time.Time                     `json:"activatedAt,omitempty"`
	FinishedAt                 *time.Time                     `json:"finishedAt,omitempty"`
	ExecutorHeartbeatAt        *time.Time                     `json:"-"`
	FencingToken               string                         `json:"-"`
	LeaseExpiresAt             *time.Time                     `json:"-"`
	ResolutionDecision         string                         `json:"resolutionDecision,omitempty"`
	ResolutionEvidence         RunnerUpdateResolutionEvidence `json:"resolutionEvidence,omitempty"`
	ResolutionEvidenceJSON     string                         `json:"-"`
}

// RunnerUpdateManifest is the signed, platform-bound identity of a Runner
// artifact. The purpose and schema prevent a valid signature for another
// artifact class from being replayed here.
type RunnerUpdateManifest struct {
	Purpose          string `json:"purpose"`
	Schema           int    `json:"schema"`
	GOOS             string `json:"goos"`
	GOARCH           string `json:"goarch"`
	RunnerID         string `json:"runnerId"`
	TargetVersion    string `json:"targetVersion"`
	ArtifactDigest   string `json:"artifactDigest"`
	ArtifactRevision string `json:"artifactRevision"`
	Publisher        string `json:"publisher"`
}

// RunnerUpdateResolutionEvidence is the immutable operator observation used
// to close an interrupted update. It deliberately carries facts, not a free
// form command, so the closure remains reviewable and replay-safe.
type RunnerUpdateResolutionEvidence struct {
	Decision         string `json:"decision"`
	ObservedVersion  string `json:"observedVersion"`
	ObservedRevision string `json:"observedRevision"`
	ObservedDigest   string `json:"observedDigest"`
	ObservedPID      int64  `json:"observedPid,omitempty"`
	Reason           string `json:"reason"`
}

type RunnerUpdateRequest struct {
	Manifest          RunnerUpdateManifest `json:"manifest,omitempty"`
	ManifestPurpose   string               `json:"manifestPurpose,omitempty"`
	ManifestSchema    int                  `json:"manifestSchema,omitempty"`
	ManifestGOOS      string               `json:"manifestGoos,omitempty"`
	ManifestGOARCH    string               `json:"manifestGoarch,omitempty"`
	RunnerID          string               `json:"runnerId,omitempty"`
	TargetVersion     string               `json:"targetVersion"`
	ArtifactPath      string               `json:"artifactPath"`
	ArtifactDigest    string               `json:"artifactDigest"`
	ArtifactRevision  string               `json:"artifactRevision"`
	Publisher         string               `json:"publisher"`
	ArtifactSignature string               `json:"artifactSignature"`
	Confirmation      string               `json:"confirmation,omitempty"`
	IdempotencyKey    string               `json:"idempotencyKey"`
}

type RunnerUpdateActivationRequest struct {
	Confirmation   string `json:"confirmation"`
	IdempotencyKey string `json:"idempotencyKey"`
}

type RunnerUpdateResolutionRequest struct {
	Confirmation   string                         `json:"confirmation"`
	IdempotencyKey string                         `json:"idempotencyKey"`
	Evidence       RunnerUpdateResolutionEvidence `json:"evidence"`
}

type RunnerUpdateCancellationRequest struct {
	Confirmation   string `json:"confirmation"`
	IdempotencyKey string `json:"idempotencyKey"`
}

type RunnerUpdateStatus struct {
	RunnerID         string         `json:"runnerId"`
	CurrentVersion   string         `json:"currentVersion"`
	Revision         string         `json:"revision"`
	Publisher        string         `json:"publisher"`
	ManifestPurpose  string         `json:"manifestPurpose"`
	ManifestSchema   int            `json:"manifestSchema"`
	ManifestGOOS     string         `json:"manifestGoos"`
	ManifestGOARCH   string         `json:"manifestGoarch"`
	CurrentActorHash string         `json:"currentActorHash"`
	CanManage        bool           `json:"canManage"`
	Pending          []RunnerUpdate `json:"pending,omitempty"`
	Recent           []RunnerUpdate `json:"recent,omitempty"`
}

func NormalizeSubject(value string) string { return strings.ToLower(strings.TrimSpace(value)) }
