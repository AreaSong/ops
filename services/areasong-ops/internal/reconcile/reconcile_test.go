package reconcile_test

import (
	"testing"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	"github.com/AreaSong/ops/services/areasong-ops/internal/reconcile"
)

func TestReconcileRunningRequiresHealthyObservation(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	state, drift := reconcile.Reconcile(map[string]any{
		"service":    "demo",
		"objectId":   "service:demo",
		"tenantId":   "tenant-a",
		"appState":   "running",
		"health":     map[string]any{"ok": true},
		"observedAt": now.Format(time.RFC3339Nano),
	}, model.DesiredRunning, now)
	if drift != nil || state.Drift != nil {
		t.Fatalf("healthy running state drifted: state=%+v drift=%+v", state, drift)
	}
	if state.Actual != model.ActualRunning || state.Health != model.HealthHealthy {
		t.Fatalf("state = %+v", state)
	}
	if state.Service != "demo" || state.ObjectID != "service:demo" || state.TenantID != "tenant-a" {
		t.Fatalf("identity = %+v", state)
	}

	state, drift = reconcile.Reconcile(map[string]any{
		"actual": "running",
		"health": "degraded",
	}, model.DesiredRunning, now)
	if drift == nil || !drift.Detected || drift.Expected != string(model.DesiredRunning) {
		t.Fatalf("expected running drift: state=%+v drift=%+v", state, drift)
	}
}

func TestReconcileMatchesNonRunningLifecycleStates(t *testing.T) {
	cases := []struct {
		desired model.DesiredState
		actual  model.ActualState
	}{
		{model.DesiredStopped, model.ActualStopped},
		{model.DesiredMaintenance, model.ActualMaintenance},
		{model.DesiredDrained, model.ActualDrained},
	}
	for _, testCase := range cases {
		state, drift := reconcile.Reconcile(map[string]any{
			"actualState": string(testCase.actual),
			"healthState": "unknown",
		}, testCase.desired, time.Time{})
		if drift != nil || state.Drift != nil {
			t.Errorf("desired=%s actual=%s drift=%+v state=%+v", testCase.desired, testCase.actual, drift, state)
		}
	}
}

func TestReconcileTrafficStateOverridesRunningApplication(t *testing.T) {
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	for _, desired := range []model.DesiredState{model.DesiredMaintenance, model.DesiredDrained} {
		state, drift := reconcile.Reconcile(map[string]any{
			"appState":     "running",
			"health":       map[string]any{"ok": true},
			"trafficState": string(desired),
		}, desired, now)
		if drift != nil || state.Actual != model.ActualState(desired) {
			t.Fatalf("desired=%s state=%+v drift=%+v", desired, state, drift)
		}
	}

	state, drift := reconcile.Reconcile(map[string]any{
		"appState": "running", "health": "healthy", "trafficState": "running",
	}, model.DesiredRunning, now)
	if drift != nil || state.Actual != model.ActualRunning || state.Health != model.HealthHealthy {
		t.Fatalf("running traffic still requires healthy app: state=%+v drift=%+v", state, drift)
	}
}

func TestReconcileStoppedApplicationWinsOverMaintenanceBarrier(t *testing.T) {
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	state, drift := reconcile.Reconcile(map[string]any{
		"appState": "stopped", "trafficState": "maintenance", "health": "unknown",
	}, model.DesiredStopped, now)
	if drift != nil || state.Actual != model.ActualStopped {
		t.Fatalf("stopped website state=%+v drift=%+v", state, drift)
	}
}

func TestReconcileUsesNowForMissingTimestampAndUnknownValues(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	observation := map[string]any{"reason": "probe failed", "unexpected": "value"}
	state, drift := reconcile.Reconcile(observation, model.DesiredRunning, now)
	if !state.ObservedAt.Equal(now) || state.Actual != model.ActualUnknown || state.Health != model.HealthUnknown {
		t.Fatalf("state = %+v", state)
	}
	if drift == nil || drift.Reason != "probe failed" || !drift.DetectedAt.Equal(now) {
		t.Fatalf("drift = %+v", drift)
	}
	if _, ok := state.Data["unexpected"]; !ok {
		t.Fatalf("state data lost observation: %#v", state.Data)
	}
	if _, ok := observation["unexpected"]; !ok {
		t.Fatal("reconcile mutated input")
	}
}
