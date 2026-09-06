package model

import "time"

const (
	FleetRunnerUpdateManifestPurpose = "areasong-ops.runner-fleet-update"
	FleetRunnerUpdateManifestSchema  = 1
)

type FleetRunnerUpdatePlanState string

const (
	FleetRunnerUpdatePendingApproval       FleetRunnerUpdatePlanState = "pending_approval"
	FleetRunnerUpdatePendingSecondApproval FleetRunnerUpdatePlanState = "pending_second_approval"
	FleetRunnerUpdateApproved              FleetRunnerUpdatePlanState = "approved"
	FleetRunnerUpdateRunning               FleetRunnerUpdatePlanState = "running"
	FleetRunnerUpdateObserving             FleetRunnerUpdatePlanState = "observing"
	FleetRunnerUpdateRollingBack           FleetRunnerUpdatePlanState = "rolling_back"
	FleetRunnerUpdateSucceeded             FleetRunnerUpdatePlanState = "succeeded"
	FleetRunnerUpdateRolledBack            FleetRunnerUpdatePlanState = "rolled_back"
	FleetRunnerUpdateNeedsAttention        FleetRunnerUpdatePlanState = "needs_attention"
	FleetRunnerUpdateCancelled             FleetRunnerUpdatePlanState = "cancelled"
	FleetRunnerUpdateExpired               FleetRunnerUpdatePlanState = "expired"
)

func (state FleetRunnerUpdatePlanState) Terminal() bool {
	switch state {
	case FleetRunnerUpdateSucceeded, FleetRunnerUpdateRolledBack,
		FleetRunnerUpdateNeedsAttention, FleetRunnerUpdateCancelled, FleetRunnerUpdateExpired:
		return true
	default:
		return false
	}
}

type FleetRunnerUpdateItemState string

const (
	FleetRunnerUpdateItemPending        FleetRunnerUpdateItemState = "pending"
	FleetRunnerUpdateItemReady          FleetRunnerUpdateItemState = "ready"
	FleetRunnerUpdateItemRunning        FleetRunnerUpdateItemState = "running"
	FleetRunnerUpdateItemSucceeded      FleetRunnerUpdateItemState = "succeeded"
	FleetRunnerUpdateItemFailed         FleetRunnerUpdateItemState = "failed"
	FleetRunnerUpdateItemRollbackReady  FleetRunnerUpdateItemState = "rollback_ready"
	FleetRunnerUpdateItemRollingBack    FleetRunnerUpdateItemState = "rolling_back"
	FleetRunnerUpdateItemRolledBack     FleetRunnerUpdateItemState = "rolled_back"
	FleetRunnerUpdateItemNeedsAttention FleetRunnerUpdateItemState = "needs_attention"
	FleetRunnerUpdateItemSkipped        FleetRunnerUpdateItemState = "skipped"
)

func (state FleetRunnerUpdateItemState) Terminal() bool {
	switch state {
	case FleetRunnerUpdateItemSucceeded, FleetRunnerUpdateItemFailed,
		FleetRunnerUpdateItemRolledBack, FleetRunnerUpdateItemNeedsAttention,
		FleetRunnerUpdateItemSkipped:
		return true
	default:
		return false
	}
}

// FleetRunnerUpdateManifest is signed once for a platform-specific artifact.
// Exact Runner targets and their before-identities are bound by PlanDigest.
type FleetRunnerUpdateManifest struct {
	Purpose          string `json:"purpose"`
	Schema           int    `json:"schema"`
	GOOS             string `json:"goos"`
	GOARCH           string `json:"goarch"`
	TargetVersion    string `json:"targetVersion"`
	ArtifactDigest   string `json:"artifactDigest"`
	ArtifactRevision string `json:"artifactRevision"`
	Publisher        string `json:"publisher"`
}

type FleetRunnerUpdateItem struct {
	ID                       string                     `json:"id"`
	PlanID                   string                     `json:"planId"`
	RunnerID                 string                     `json:"runnerId"`
	ServerID                 string                     `json:"serverId"`
	BatchIndex               int                        `json:"batchIndex"`
	State                    FleetRunnerUpdateItemState `json:"state"`
	PreviousVersion          string                     `json:"previousVersion"`
	PreviousRevision         string                     `json:"previousRevision"`
	PreviousDigest           string                     `json:"previousDigest"`
	ExpectedLeaseGeneration  uint64                     `json:"expectedLeaseGeneration"`
	CertificateFingerprint   string                     `json:"certificateFingerprint,omitempty"`
	AssignmentAction         string                     `json:"assignmentAction,omitempty"`
	AssignmentGeneration     uint64                     `json:"assignmentGeneration,omitempty"`
	AssignmentToken          string                     `json:"-"`
	AssignmentIdempotencyKey string                     `json:"-"`
	CompletionIdempotencyKey string                     `json:"-"`
	ObservedVersion          string                     `json:"observedVersion,omitempty"`
	ObservedRevision         string                     `json:"observedRevision,omitempty"`
	ObservedDigest           string                     `json:"observedDigest,omitempty"`
	Error                    string                     `json:"error,omitempty"`
	RollbackError            string                     `json:"rollbackError,omitempty"`
	ClaimedAt                *time.Time                 `json:"claimedAt,omitempty"`
	LastHeartbeatAt          *time.Time                 `json:"lastHeartbeatAt,omitempty"`
	LeaseExpiresAt           *time.Time                 `json:"leaseExpiresAt,omitempty"`
	ExecutionDeadlineAt      *time.Time                 `json:"executionDeadlineAt,omitempty"`
	StartedAt                *time.Time                 `json:"startedAt,omitempty"`
	FinishedAt               *time.Time                 `json:"finishedAt,omitempty"`
	UpdatedAt                time.Time                  `json:"updatedAt"`
}

type FleetRunnerUpdatePlan struct {
	ID                         string                     `json:"id"`
	IdempotencyKey             string                     `json:"-"`
	ExecutionIdempotencyKey    string                     `json:"-"`
	CancellationIdempotencyKey string                     `json:"-"`
	RequestDigest              string                     `json:"requestDigest"`
	PlanDigest                 string                     `json:"planDigest"`
	PolicyDigest               string                     `json:"policyDigest"`
	ActorHash                  string                     `json:"actorHash"`
	TenantID                   string                     `json:"tenantId"`
	Manifest                   FleetRunnerUpdateManifest  `json:"manifest"`
	ArtifactPath               string                     `json:"artifactPath,omitempty"`
	ArtifactSignature          string                     `json:"artifactSignature,omitempty"`
	StagedPath                 string                     `json:"-"`
	TargetRunnerIDs            []string                   `json:"targetRunnerIds"`
	BatchPolicy                BatchPolicy                `json:"batchPolicy"`
	MaxConcurrent              int                        `json:"maxConcurrent"`
	ChangeWindow               *ChangeWindow              `json:"changeWindow"`
	RollbackOnFailure          bool                       `json:"rollbackOnFailure"`
	State                      FleetRunnerUpdatePlanState `json:"state"`
	CurrentBatch               int                        `json:"currentBatch"`
	ConfirmationPhrase         string                     `json:"confirmationPhrase,omitempty"`
	ApprovedByHash             string                     `json:"approvedByHash,omitempty"`
	SecondApprovedByHash       string                     `json:"secondApprovedByHash,omitempty"`
	ApprovalPolicy             string                     `json:"approvalPolicy,omitempty"`
	ExecutedByHash             string                     `json:"executedByHash,omitempty"`
	CancelledByHash            string                     `json:"cancelledByHash,omitempty"`
	Summary                    string                     `json:"summary,omitempty"`
	Error                      string                     `json:"error,omitempty"`
	Items                      []FleetRunnerUpdateItem    `json:"items"`
	CreatedAt                  time.Time                  `json:"createdAt"`
	ExpiresAt                  time.Time                  `json:"expiresAt"`
	ApprovedAt                 *time.Time                 `json:"approvedAt,omitempty"`
	SecondApprovedAt           *time.Time                 `json:"secondApprovedAt,omitempty"`
	StartedAt                  *time.Time                 `json:"startedAt,omitempty"`
	ObservationStartedAt       *time.Time                 `json:"observationStartedAt,omitempty"`
	ObservationEndsAt          *time.Time                 `json:"observationEndsAt,omitempty"`
	FinishedAt                 *time.Time                 `json:"finishedAt,omitempty"`
	UpdatedAt                  time.Time                  `json:"updatedAt"`
}

type FleetRunnerUpdatePlanRequest struct {
	Manifest          FleetRunnerUpdateManifest `json:"manifest"`
	ArtifactPath      string                    `json:"artifactPath"`
	ArtifactSignature string                    `json:"artifactSignature"`
	TargetRunnerIDs   []string                  `json:"targetRunnerIds"`
	BatchPolicy       BatchPolicy               `json:"batchPolicy"`
	MaxConcurrent     int                       `json:"maxConcurrent"`
	ChangeWindow      ChangeWindow              `json:"changeWindow"`
	RollbackOnFailure bool                      `json:"rollbackOnFailure"`
	Confirmation      string                    `json:"confirmation"`
	IdempotencyKey    string                    `json:"idempotencyKey"`
}

type FleetRunnerUpdateApprovalRequest struct {
	Digest       string `json:"digest"`
	Confirmation string `json:"confirmation"`
}

type FleetRunnerUpdateExecuteRequest struct {
	IdempotencyKey string `json:"idempotencyKey"`
}

type FleetRunnerUpdateCancelRequest struct {
	IdempotencyKey string `json:"idempotencyKey"`
	Confirmation   string `json:"confirmation"`
}

type FleetRunnerUpdateStatus struct {
	Available        bool                    `json:"available"`
	CanManage        bool                    `json:"canManage"`
	CurrentActorHash string                  `json:"currentActorHash"`
	Publisher        string                  `json:"publisher"`
	ManifestPurpose  string                  `json:"manifestPurpose"`
	ManifestSchema   int                     `json:"manifestSchema"`
	ManifestGOOS     string                  `json:"manifestGoos"`
	ManifestGOARCH   string                  `json:"manifestGoarch"`
	Runners          []RunnerNode            `json:"runners"`
	Plans            []FleetRunnerUpdatePlan `json:"plans"`
}

type FleetRunnerUpdateFence struct {
	Generation uint64 `json:"generation"`
	ClaimToken string `json:"claimToken"`
}

type FleetRunnerUpdateClaimRequest struct {
	LeaseSeconds int `json:"leaseSeconds,omitempty"`
}

type FleetRunnerUpdateAssignment struct {
	PlanID              string                    `json:"planId"`
	ItemID              string                    `json:"itemId"`
	RunnerID            string                    `json:"runnerId"`
	ServerID            string                    `json:"serverId"`
	Action              string                    `json:"action"`
	Manifest            FleetRunnerUpdateManifest `json:"manifest"`
	ArtifactSignature   string                    `json:"artifactSignature"`
	PreviousVersion     string                    `json:"previousVersion"`
	PreviousRevision    string                    `json:"previousRevision"`
	PreviousDigest      string                    `json:"previousDigest"`
	PolicyDigest        string                    `json:"policyDigest"`
	PlanDigest          string                    `json:"planDigest"`
	ExecutionDeadlineAt time.Time                 `json:"executionDeadlineAt"`
	Fence               FleetRunnerUpdateFence    `json:"fence"`
}

type FleetRunnerUpdateHeartbeatRequest struct {
	FleetRunnerUpdateFence
}

type FleetRunnerUpdateArtifactRequest struct {
	FleetRunnerUpdateFence
}

type FleetRunnerUpdateCompletionRequest struct {
	FleetRunnerUpdateFence
	IdempotencyKey   string `json:"idempotencyKey"`
	State            string `json:"state"`
	ObservedVersion  string `json:"observedVersion"`
	ObservedRevision string `json:"observedRevision"`
	ObservedDigest   string `json:"observedDigest"`
	Error            string `json:"error,omitempty"`
}

type FleetRunnerUpdateReceipt struct {
	ItemID               string                      `json:"itemId"`
	AssignmentGeneration uint64                      `json:"assignmentGeneration"`
	PlanID               string                      `json:"planId"`
	Fence                FleetRunnerUpdateFence      `json:"-"`
	ControlPlaneEndpoint string                      `json:"controlPlaneEndpoint"`
	LocalUpdateID        string                      `json:"localUpdateId,omitempty"`
	Action               string                      `json:"action"`
	State                string                      `json:"state"`
	LastError            string                      `json:"lastError,omitempty"`
	CreatedAt            time.Time                   `json:"createdAt"`
	UpdatedAt            time.Time                   `json:"updatedAt"`
	Assignment           FleetRunnerUpdateAssignment `json:"-"`
}
