package model

import (
	"encoding/json"
	"testing"
)

func TestStateEnumsAndPolicyJSON(t *testing.T) {
	if DesiredRunning != "running" || DesiredStopped != "stopped" ||
		DesiredMaintenance != "maintenance" || DesiredDrained != "drained" {
		t.Fatalf("unexpected desired state constants")
	}
	if ActualUnknown != "unknown" || ActualRunning != "running" ||
		ActualStopped != "stopped" || ActualMaintenance != "maintenance" || ActualDrained != "drained" {
		t.Fatalf("unexpected actual state constants")
	}
	if HealthUnknown != "unknown" || HealthHealthy != "healthy" ||
		HealthDegraded != "degraded" || HealthUnhealthy != "unhealthy" {
		t.Fatalf("unexpected health state constants")
	}

	policy := StatePolicyDefinition{
		DefaultDesired:        DesiredMaintenance,
		MaintenanceTTLSeconds: 300,
		DrainTimeoutSeconds:   60,
		AutoReconcile:         true,
	}
	payload, err := json.Marshal(policy)
	if err != nil {
		t.Fatalf("marshal policy: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("decode policy: %v", err)
	}
	for key, want := range map[string]any{
		"defaultDesired":        "maintenance",
		"maintenanceTtlSeconds": float64(300),
		"drainTimeoutSeconds":   float64(60),
		"autoReconcile":         true,
	} {
		if got := decoded[key]; got != want {
			t.Errorf("%s = %#v, want %#v", key, got, want)
		}
	}
}

func TestStateObservationUsesScalarDrift(t *testing.T) {
	observation := StateObservation{Actual: ActualStopped, Health: HealthUnknown, Drift: true}
	payload, err := json.Marshal(observation)
	if err != nil {
		t.Fatalf("marshal observation: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("decode observation: %v", err)
	}
	if got, ok := decoded["drift"].(bool); !ok || !got {
		t.Fatalf("drift JSON value = %#v, want true", decoded["drift"])
	}
}
