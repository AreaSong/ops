package model

import "time"

// DesiredState is the lifecycle state requested by the control plane.
type DesiredState string

const (
	DesiredRunning     DesiredState = "running"
	DesiredStopped     DesiredState = "stopped"
	DesiredMaintenance DesiredState = "maintenance"
	DesiredDrained     DesiredState = "drained"
)

// ActualState is the lifecycle state reported by an observer.
type ActualState string

const (
	ActualUnknown     ActualState = "unknown"
	ActualRunning     ActualState = "running"
	ActualStopped     ActualState = "stopped"
	ActualMaintenance ActualState = "maintenance"
	ActualDrained     ActualState = "drained"
)

// HealthState is the health assessment associated with an observation.
type HealthState string

const (
	HealthUnknown   HealthState = "unknown"
	HealthHealthy   HealthState = "healthy"
	HealthDegraded  HealthState = "degraded"
	HealthUnhealthy HealthState = "unhealthy"
)

// StatePolicyDefinition controls the default and automatic lifecycle policy
// for a service. Zero values retain the legacy/manual behaviour.
type StatePolicyDefinition struct {
	DefaultDesired        DesiredState `json:"defaultDesired"`
	MaintenanceTTLSeconds int          `json:"maintenanceTtlSeconds"`
	DrainTimeoutSeconds   int          `json:"drainTimeoutSeconds"`
	AutoReconcile         bool         `json:"autoReconcile"`
}

// ServiceState is the merged desired/observed state presented by the control
// plane.
type ServiceState struct {
	Service          string         `json:"service"`
	ObjectID         string         `json:"objectId"`
	TenantID         string         `json:"tenantId"`
	Desired          DesiredState   `json:"desired"`
	Actual           ActualState    `json:"actual"`
	Health           HealthState    `json:"health"`
	ObservedAt       time.Time      `json:"observedAt"`
	DesiredUpdatedAt time.Time      `json:"desiredUpdatedAt"`
	MaintenanceUntil *time.Time     `json:"maintenanceUntil,omitempty"`
	Reason           string         `json:"reason"`
	Generation       int64          `json:"generation"`
	Data             map[string]any `json:"data,omitempty"`
	Drift            *StateDrift    `json:"drift,omitempty"`
}

// StateDrift describes a mismatch between desired and observed state.
type StateDrift struct {
	Detected   bool      `json:"detected"`
	Expected   string    `json:"expected"`
	Observed   string    `json:"observed"`
	Reason     string    `json:"reason"`
	DetectedAt time.Time `json:"detectedAt"`
}

// StateObservation is the durable, observer-owned portion of ServiceState.
// Drift is kept as a scalar here because it is stored in the state table as a
// compact boolean; the richer explanation is reconstructed on read.
type StateObservation struct {
	Service    string         `json:"service"`
	ObjectID   string         `json:"objectId"`
	TenantID   string         `json:"tenantId"`
	Actual     ActualState    `json:"actual"`
	Health     HealthState    `json:"health"`
	ObservedAt time.Time      `json:"observedAt"`
	Reason     string         `json:"reason"`
	Data       map[string]any `json:"data,omitempty"`
	Drift      bool           `json:"drift"`
}

// ControlPlaneEvent is an append-only event emitted by state and lifecycle
// changes.
type ControlPlaneEvent struct {
	OccurredAt time.Time      `json:"occurredAt"`
	Type       string         `json:"type"`
	Resource   string         `json:"resource"`
	TenantID   string         `json:"tenantId"`
	Data       map[string]any `json:"data,omitempty"`
}
