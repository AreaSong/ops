package main

import (
	"os"
	"testing"

	"github.com/AreaSong/ops/services/areasong-ops/internal/config"
	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

func TestMTLSListenerRejectsIncompletePolicy(t *testing.T) {
	if _, err := mtlsListener(&config.FleetPolicy{AllowRemoteRunners: true, RequiremTLS: true}); err == nil {
		t.Fatal("incomplete mTLS policy was accepted")
	}
}

func TestEnforceProductionStateRootMode(t *testing.T) {
	stateRoot := t.TempDir()
	if err := os.Chmod(stateRoot, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := enforceProductionStateRootMode(stateRoot); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o710 {
		t.Fatalf("state root mode = %04o, want 0710", got)
	}
}

func TestInterruptionClassifierUsesPhaseSemantics(t *testing.T) {
	action := model.ActionDefinition{
		Name: "deploy", Steps: []string{"preflight", "apply", "health"},
		PhaseSemantics: map[string]model.PhaseSemantics{
			"apply": {
				Effect: "runtime_mutation", FailurePolicy: "rollback", RecoveryPhase: "undo",
			},
		},
	}
	catalog := &config.Catalog{Services: map[string]model.ServiceDefinition{
		"demo": {Name: "demo", Actions: map[string]model.ActionDefinition{"deploy": action}},
	}}
	classify := interruptionClassifier(catalog)
	mutation, rollback := classify("demo", "deploy", "apply", false)
	if !mutation || !rollback {
		t.Fatalf("apply mutation=%v rollback=%v", mutation, rollback)
	}
	mutation, rollback = classify("demo", "deploy", "health", true)
	if mutation || !rollback {
		t.Fatalf("legacy health mutation=%v rollback=%v", mutation, rollback)
	}
	action.PhaseSemantics["health"] = model.PhaseSemantics{Effect: "observe", FailurePolicy: "fail"}
	service := catalog.Services["demo"]
	service.Actions["deploy"] = action
	catalog.Services["demo"] = service
	mutation, rollback = classify("demo", "deploy", "health", true)
	if mutation || rollback {
		t.Fatalf("explicit health mutation=%v rollback=%v", mutation, rollback)
	}
	mutation, rollback = classify("unknown", "deploy", "health", false)
	if !mutation || rollback {
		t.Fatalf("unknown mutation=%v rollback=%v", mutation, rollback)
	}
}
