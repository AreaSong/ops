package model

import (
	"encoding/json"
	"time"
)

type ExtensionPlan struct {
	ID                      string     `json:"id"`
	IdempotencyKey          string     `json:"idempotencyKey,omitempty"`
	RequestDigest           string     `json:"requestDigest"`
	PlanDigest              string     `json:"planDigest"`
	ActorHash               string     `json:"actorHash"`
	TenantID                string     `json:"tenantId"`
	ObjectID                string     `json:"objectId"`
	ExtensionID             string     `json:"extensionId"`
	ExtensionVersion        string     `json:"extensionVersion"`
	ExtensionDigest         string     `json:"extensionDigest"`
	Publisher               string     `json:"publisher"`
	ManifestDigest          string     `json:"manifestDigest"`
	PolicyDigest            string     `json:"policyDigest"`
	Sandbox                 string     `json:"sandbox"`
	InputDigest             string     `json:"inputDigest"`
	TimeoutSeconds          int        `json:"timeoutSeconds"`
	MaxPackageBytes         int64      `json:"maxPackageBytes"`
	MaxInputBytes           int64      `json:"maxInputBytes"`
	MaxOutputBytes          int64      `json:"maxOutputBytes"`
	MaxMemoryPages          uint32     `json:"maxMemoryPages"`
	State                   string     `json:"state"`
	ConfirmationPhrase      string     `json:"confirmationPhrase,omitempty"`
	ApprovedByHash          string     `json:"approvedByHash,omitempty"`
	SecondApprovedByHash    string     `json:"secondApprovedByHash,omitempty"`
	ApprovalPolicy          string     `json:"approvalPolicy,omitempty"`
	ExecutedByHash          string     `json:"executedByHash,omitempty"`
	ExecutionIdempotencyKey string     `json:"-"`
	Output                  string     `json:"output,omitempty"`
	ExitCode                int        `json:"exitCode,omitempty"`
	Error                   string     `json:"error,omitempty"`
	CreatedAt               time.Time  `json:"createdAt"`
	ExpiresAt               time.Time  `json:"expiresAt"`
	ApprovedAt              *time.Time `json:"approvedAt,omitempty"`
	SecondApprovedAt        *time.Time `json:"secondApprovedAt,omitempty"`
	StartedAt               *time.Time `json:"startedAt,omitempty"`
	FinishedAt              *time.Time `json:"finishedAt,omitempty"`
}

type ExtensionPlanRequest struct {
	ExtensionID      string          `json:"extensionId"`
	ExtensionVersion string          `json:"extensionVersion"`
	ObjectID         string          `json:"objectId"`
	Input            json.RawMessage `json:"input"`
	TimeoutSeconds   int             `json:"timeoutSeconds,omitempty"`
	IdempotencyKey   string          `json:"idempotencyKey"`
}

type ExtensionPlanApprovalRequest struct {
	Digest       string `json:"digest"`
	Confirmation string `json:"confirmation"`
}

type ExtensionPlanExecuteRequest struct {
	IdempotencyKey string `json:"idempotencyKey"`
}
