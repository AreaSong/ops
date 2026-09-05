package model

import "time"

// AutoUpdatePolicyView is the durable, actor-filtered representation exposed
// to the control plane. It intentionally omits adapter paths and credentials.
type AutoUpdatePolicyView struct {
	Service             string     `json:"service"`
	ObjectID            string     `json:"objectId"`
	TenantID            string     `json:"tenantId"`
	Enabled             bool       `json:"enabled"`
	Channel             string     `json:"channel"`
	MaintenanceWindow   string     `json:"maintenanceWindow,omitempty"`
	MaintenanceTimezone string     `json:"maintenanceTimezone"`
	CanaryPercent       int        `json:"canaryPercent,omitempty"`
	MaxUnavailable      int        `json:"maxUnavailable,omitempty"`
	RequireBackup       bool       `json:"requireBackup"`
	RequireApproval     bool       `json:"requireApproval"`
	RollbackOnAlert     bool       `json:"rollbackOnAlert"`
	ObservationSeconds  int        `json:"observationSeconds"`
	NextEvaluationAt    *time.Time `json:"nextEvaluationAt,omitempty"`
	LastEvaluationAt    *time.Time `json:"lastEvaluationAt,omitempty"`
	LastPlanID          string     `json:"lastPlanId,omitempty"`
	LastError           string     `json:"lastError,omitempty"`
}

type AutoUpdatePolicyRequest struct {
	Service             string `json:"service"`
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
	IdempotencyKey      string `json:"idempotencyKey"`
}

type AutoUpdateEvaluation struct {
	Service       string    `json:"service"`
	EvaluatedAt   time.Time `json:"evaluatedAt"`
	Eligible      bool      `json:"eligible"`
	Reason        string    `json:"reason,omitempty"`
	PlanID        string    `json:"planId,omitempty"`
	Target        string    `json:"target,omitempty"`
	UpdateCreated bool      `json:"updateCreated"`
}
