package model

import (
	"encoding/json"
	"time"
)

const PlanPreparationKind = "release_plan_preparation_v1"
const PlanInspectionDone = "inspection_done"

type ReleaseAuthority struct {
	ObjectID      string     `json:"objectId"`
	ActorHash     string     `json:"actorHash"`
	TenantID      string     `json:"tenantId"`
	Permission    Permission `json:"permission"`
	PolicyVersion int64      `json:"policyVersion"`
	PolicyDigest  string     `json:"policyDigest"`
}

type PlanPreparationRequest struct {
	Version        int                     `json:"version"`
	Kind           string                  `json:"kind"`
	PlanID         string                  `json:"planId"`
	IdempotencyKey string                  `json:"idempotencyKey"`
	InputDigest    string                  `json:"inputDigest"`
	Authority      ReleaseAuthority        `json:"authority"`
	Service        string                  `json:"service"`
	Action         string                  `json:"action"`
	Target         string                  `json:"target"`
	ScheduleAt     *time.Time              `json:"scheduleAt,omitempty"`
	Binding        ReleaseLifecycleBinding `json:"binding"`
}

type PlanInspectionResult struct {
	Kind          string         `json:"kind"`
	ScopeDigest   string         `json:"scopeDigest"`
	Succeeded     bool           `json:"succeeded"`
	Snapshot      map[string]any `json:"snapshot"`
	CallDigests   []string       `json:"callDigests"`
	CleanupDigest string         `json:"cleanupDigest"`
	Failure       string         `json:"failure,omitempty"`
}

type PlanPreparation struct {
	ID                string
	Request           PlanPreparationRequest
	RequestDigest     string
	AuthorityDigest   string
	State             string
	Revision          int64
	ResultJSON        string
	ResultDigest      string
	ProducedPlanID    string
	CreatedAt         time.Time
	UpdatedAt         time.Time
	ClosedAt          *time.Time
	CloseKind         string
	CloseEvidenceJSON string
	CloseDigest       string
}

func CanonicalPlanPreparation(r PlanPreparationRequest) (string, string, string, error) {
	if r.Version != 1 || r.Kind != PlanPreparationKind || !workIdentifier(r.PlanID) ||
		!workIdentifier(r.IdempotencyKey) || !ValidWorkDigest(r.InputDigest) ||
		!workIdentifier(r.Authority.ObjectID) || !ValidWorkDigest(r.Authority.ActorHash) || r.Authority.TenantID != r.Binding.CreatorTenantID ||
		r.Authority.PolicyVersion <= 0 || r.Authority.PolicyDigest == "" || r.Authority.Permission != PermissionDeploy ||
		!workIdentifier(r.Service) || r.Action != "update" || !workIdentifier(r.Target) {
		return "", "", "", ErrReleaseLifecycle
	}
	if _, err := r.Binding.WorkTargets(); err != nil {
		return "", "", "", err
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return "", "", "", err
	}
	authority, _ := json.Marshal(r.Authority)
	return string(raw), WorkDigest(string(raw)), WorkDigest(string(authority)), nil
}

func (result PlanInspectionResult) Canonical() (string, string, error) {
	if result.Kind != "inspection_settled_v1" || !ValidWorkDigest(result.ScopeDigest) ||
		!ValidWorkDigest(result.CleanupDigest) || (result.Succeeded && (result.Snapshot == nil || len(result.CallDigests) == 0 || result.Failure != "")) ||
		(!result.Succeeded && result.Failure == "") {
		return "", "", ErrReleaseLifecycle
	}
	for _, digest := range result.CallDigests {
		if !ValidWorkDigest(digest) {
			return "", "", ErrReleaseLifecycle
		}
	}
	raw, err := json.Marshal(result)
	return string(raw), WorkDigest(string(raw)), err
}
