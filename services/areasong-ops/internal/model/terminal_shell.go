package model

import "time"

type TerminalShellPlan struct {
	ID                      string     `json:"id"`
	ObjectID                string     `json:"objectId"`
	State                   string     `json:"state"`
	ActorHash               string     `json:"actorHash"`
	InputDigest             string     `json:"inputDigest"`
	ConfirmationPhrase      string     `json:"confirmationPhrase,omitempty"`
	ApprovedByHash          string     `json:"approvedByHash,omitempty"`
	SecondApprovedByHash    string     `json:"secondApprovedByHash,omitempty"`
	ExecutionIdempotencyKey string     `json:"-"`
	ExitCode                int        `json:"exitCode,omitempty"`
	Output                  string     `json:"output,omitempty"`
	Error                   string     `json:"error,omitempty"`
	CreatedAt               time.Time  `json:"createdAt"`
	ExpiresAt               time.Time  `json:"expiresAt"`
	ApprovedAt              *time.Time `json:"approvedAt,omitempty"`
	SecondApprovedAt        *time.Time `json:"secondApprovedAt,omitempty"`
	StartedAt               *time.Time `json:"startedAt,omitempty"`
	FinishedAt              *time.Time `json:"finishedAt,omitempty"`
}

type TerminalShellPlanRequest struct {
	ObjectID       string `json:"objectId"`
	Input          string `json:"input"`
	Confirmation   string `json:"confirmation"`
	IdempotencyKey string `json:"idempotencyKey"`
}

type TerminalShellApprovalRequest struct {
	Confirmation string `json:"confirmation"`
}

type TerminalShellExecuteRequest struct {
	Input          string `json:"input"`
	IdempotencyKey string `json:"idempotencyKey"`
}
