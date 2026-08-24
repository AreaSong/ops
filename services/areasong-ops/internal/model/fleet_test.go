package model

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"
)

func testServer(id string) ServerNode {
	return ServerNode{
		ID: "server-" + id, Hostname: id, Environment: "production", Region: "la",
		Labels:       map[string]string{"environment": "production", "role": "web"},
		Capabilities: []string{"restart", "update"}, State: NodeOnline,
	}
}

func testRunner(id, serverID string) RunnerNode {
	return RunnerNode{
		ID: "runner-" + id, ServerID: serverID, Version: "1.0.0",
		Labels:       map[string]string{"environment": "production"},
		Capabilities: []string{"restart", "update"}, State: NodeOnline,
	}
}

func TestFleetNodesValidateAndSelectByLabels(t *testing.T) {
	server := testServer("one")
	runner := testRunner("one", server.ID)
	if err := ValidateServerNode(server); err != nil {
		t.Fatalf("valid server rejected: %v", err)
	}
	if err := ValidateRunnerNode(runner); err != nil {
		t.Fatalf("valid runner rejected: %v", err)
	}
	if err := ValidateFleet(Fleet{Servers: []ServerNode{server}, Runners: []RunnerNode{runner}}); err != nil {
		t.Fatalf("valid fleet rejected: %v", err)
	}
	server.RunnerID = runner.ID
	if err := ValidateFleet(Fleet{Servers: []ServerNode{server}, Runners: []RunnerNode{runner}}); err != nil {
		t.Fatalf("valid explicit runner association rejected: %v", err)
	}

	selected, err := SelectServerNodes([]ServerNode{server, func() ServerNode {
		other := testServer("two")
		other.Labels["role"] = "database"
		return other
	}()}, NodeSelector{
		MatchLabels:       map[string]string{"environment": "production", "role": "web"},
		MatchCapabilities: []string{"restart"},
	})
	if err != nil || len(selected) != 1 || selected[0].ID != server.ID {
		t.Fatalf("selector result = %#v, err=%v", selected, err)
	}

	bad := server
	bad.State = NodeState("bogus")
	if err := bad.Validate(); !errors.Is(err, ErrInvalidNode) {
		t.Fatalf("invalid state error = %v", err)
	}
	bad = server
	bad.Capabilities = []string{"restart", "restart"}
	if err := bad.Validate(); !errors.Is(err, ErrInvalidNode) {
		t.Fatalf("duplicate capability error = %v", err)
	}
	bad = server
	bad.Labels = map[string]string{"bad key": "value"}
	if err := bad.Validate(); !errors.Is(err, ErrInvalidNode) {
		t.Fatalf("invalid label error = %v", err)
	}

	unknownRunner := runner
	unknownRunner.ServerID = "server-missing"
	if err := (Fleet{Servers: []ServerNode{server}, Runners: []RunnerNode{unknownRunner}}).Validate(); !errors.Is(err, ErrInvalidFleet) {
		t.Fatalf("unknown server reference error = %v", err)
	}
	if !CanTransition(NodeOnline, NodeDraining) || CanTransition(NodeDisabled, NodeOnline) {
		t.Fatal("fleet node transition rules are incorrect")
	}
	if !NodeOnline.Available() || NodeOffline.Available() || !CanTransitionFleetNode(NodeUnknown, NodeOnline) {
		t.Fatal("fleet node availability helpers are incorrect")
	}
	now := time.Date(2026, 8, 20, 1, 0, 0, 0, time.UTC)
	runner.LastHeartbeat = pointerTo(now.Add(-time.Minute))
	runner.LeaseExpiresAt = pointerTo(now.Add(time.Minute))
	if !runner.AvailableAt(now, 2*time.Minute) || runner.AvailableAt(now.Add(2*time.Minute), 2*time.Minute) {
		t.Fatal("runner heartbeat or lease boundary is incorrect")
	}
	// The heartbeat deadline itself is expired. Use a separate copy without a
	// lease expiry so this assertion isolates the heartbeat age boundary.
	boundary := runner
	boundary.LeaseExpiresAt = nil
	if boundary.AvailableAt(now.Add(time.Minute), 2*time.Minute) {
		t.Fatal("runner at exact heartbeat timeout must be unavailable")
	}
}

func pointerTo[T any](value T) *T { return &value }

func TestFleetJSONRoundTrip(t *testing.T) {
	server := testServer("one")
	now := time.Date(2026, time.August, 20, 3, 0, 0, 0, time.UTC)
	server.LastHeartbeat = &now
	payload, err := json.Marshal(Fleet{Servers: []ServerNode{server}, Runners: []RunnerNode{testRunner("one", server.ID)}})
	if err != nil {
		t.Fatalf("marshal fleet: %v", err)
	}
	var decoded Fleet
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("unmarshal fleet: %v", err)
	}
	if !reflect.DeepEqual(decoded.Servers[0], server) {
		t.Fatalf("round-trip server mismatch: got %#v want %#v", decoded.Servers[0], server)
	}
	if string(payload) == "" {
		t.Fatal("empty JSON payload")
	}
}

func TestCapabilityDefinitionsAndSelectorBoundaries(t *testing.T) {
	labels := LabelSet{"environment": "production"}
	clone := labels.Clone()
	clone["environment"] = "test"
	if labels["environment"] != "production" || LabelSet(nil).Clone() != nil {
		t.Fatal("label clone aliases the source")
	}
	if err := ValidateCapabilityDefinitions([]Capability{{Name: "restart", Version: "v1", Parameters: map[string]string{"mode": "safe"}}}); err != nil {
		t.Fatalf("valid capability definition rejected: %v", err)
	}
	if err := ValidateCapabilityDefinitions([]Capability{{Name: "restart"}, {Name: "restart"}}); !errors.Is(err, ErrInvalidNode) {
		t.Fatalf("duplicate capability definition accepted: %v", err)
	}
	server := testServer("one")
	runner := testRunner("one", server.ID)
	selector := NodeSelector{IDs: []string{runner.ID}, MatchCapabilities: []string{"update"}}
	selected, err := SelectRunnerNodes([]RunnerNode{runner}, selector)
	if err != nil || len(selected) != 1 {
		t.Fatalf("runner selector result=%#v err=%v", selected, err)
	}
	selector.ExcludeIDs = []string{runner.ID}
	if err := selector.Validate(); !errors.Is(err, ErrInvalidNode) {
		t.Fatalf("overlapping include/exclude selector accepted: %v", err)
	}
	if _, err := SelectServerNodes([]ServerNode{server}, NodeSelector{IDs: []string{"bad id"}}); !errors.Is(err, ErrInvalidNode) {
		t.Fatalf("invalid selector id accepted: %v", err)
	}
}

func TestBatchPolicyPartitionsAndBoundaries(t *testing.T) {
	targets := []string{"server-a", "server-b", "server-c", "server-d", "server-e"}
	original := append([]string(nil), targets...)
	tests := []struct {
		name   string
		policy BatchPolicy
		want   [][]string
	}{
		{name: "serial", policy: BatchPolicy{Strategy: BatchSerial}, want: [][]string{{"server-a"}, {"server-b"}, {"server-c"}, {"server-d"}, {"server-e"}}},
		{name: "fixed", policy: BatchPolicy{Strategy: BatchFixed, BatchSize: 2}, want: [][]string{{"server-a", "server-b"}, {"server-c", "server-d"}, {"server-e"}}},
		{name: "percentage", policy: BatchPolicy{Strategy: BatchPercentage, BatchPercentage: 40}, want: [][]string{{"server-a", "server-b"}, {"server-c", "server-d"}, {"server-e"}}},
		{name: "canary", policy: BatchPolicy{Strategy: BatchCanary, CanarySize: 1, BatchSize: 2}, want: [][]string{{"server-a"}, {"server-b", "server-c"}, {"server-d", "server-e"}}},
	}
	for _, test := range tests {
		got, err := test.policy.Partition(targets)
		if err != nil || !reflect.DeepEqual(got, test.want) {
			t.Errorf("%s: got %#v, err=%v, want %#v", test.name, got, err, test.want)
		}
	}
	if !reflect.DeepEqual(targets, original) {
		t.Fatalf("partition mutated input: got %#v want %#v", targets, original)
	}
	for _, policy := range []BatchPolicy{
		{}, {Strategy: BatchFixed}, {Strategy: BatchPercentage, BatchPercentage: 101},
		{Strategy: BatchCanary, CanarySize: 1}, {Strategy: BatchCanary, CanarySize: 1, CanaryPercentage: 20, BatchSize: 2},
	} {
		if err := policy.Validate(); !errors.Is(err, ErrInvalidBatchPolicy) {
			t.Errorf("policy %#v accepted: %v", policy, err)
		}
	}
	if got, err := (BatchPolicy{Strategy: BatchFixed, BatchSize: 1}).Partition(nil); err != nil || len(got) != 0 {
		t.Fatalf("empty target partition = %#v, err=%v", got, err)
	}
	if err := ValidateBatchPolicy(BatchPolicy{Strategy: BatchSerial}); err != nil {
		t.Fatalf("batch policy wrapper rejected valid policy: %v", err)
	}
	if got, err := PartitionTargets(targets[:2], BatchPolicy{Strategy: BatchFixed, BatchSize: 2}); err != nil || len(got) != 1 {
		t.Fatalf("partition wrapper result=%#v err=%v", got, err)
	}
	if _, err := PartitionTargets([]string{"server-a", "server-a"}, BatchPolicy{Strategy: BatchSerial}); !errors.Is(err, ErrInvalidBatchPolicy) {
		t.Fatalf("duplicate partition target accepted: %v", err)
	}
}

func TestConcurrencyFailureAndChangeWindowValidation(t *testing.T) {
	validConcurrency := ConcurrencyPolicy{Scope: ConcurrencyGlobal, MaxConcurrent: 3, QueueLimit: 5}
	if err := validConcurrency.Validate(); err != nil {
		t.Fatalf("valid concurrency rejected: %v", err)
	}
	if err := ValidateConcurrencyPolicy(ConcurrencyPolicy{Scope: ConcurrencyPerRunner, MaxConcurrent: 3, PerRunner: 1}); err != nil {
		t.Fatalf("per-runner concurrency rejected: %v", err)
	}
	for _, invalid := range []ConcurrencyPolicy{
		{Scope: ConcurrencyGlobal, MaxConcurrent: 0},
		{Scope: ConcurrencyPerRunner, MaxConcurrent: 2},
		{Scope: ConcurrencyPerServer, MaxConcurrent: 2, PerServer: -1},
	} {
		if err := invalid.Validate(); !errors.Is(err, ErrInvalidConcurrency) {
			t.Errorf("invalid concurrency accepted: %#v (%v)", invalid, err)
		}
	}
	if err := (FailurePolicyConfig{Policy: FailureRollback, RollbackOnFailure: true}).Validate(); err != nil {
		t.Fatalf("valid failure policy rejected: %v", err)
	}
	if err := (FailurePolicyConfig{Policy: FailureContinue, RollbackOnFailure: true}).Validate(); !errors.Is(err, ErrInvalidFailurePolicy) {
		t.Fatalf("incompatible rollback policy accepted: %v", err)
	}
	if !(FailurePolicyConfig{Policy: FailureStop}).ShouldStop(1) ||
		(FailurePolicyConfig{Policy: FailureContinue}).ShouldStop(100) ||
		!(FailurePolicyConfig{Policy: FailureContinue, MaxFailures: 2}).ShouldStop(2) {
		t.Fatal("failure threshold semantics are incorrect")
	}

	start := time.Date(2026, 8, 20, 1, 0, 0, 0, time.UTC)
	window := ChangeWindow{ID: "window-1", StartAt: start, EndAt: start.Add(time.Hour), Timezone: "UTC"}
	if err := window.Validate(); err != nil {
		t.Fatalf("valid window rejected: %v", err)
	}
	if err := ValidateChangeWindow(window); err != nil || !window.IsOpen(start) || !IsWithinChangeWindow(window, start) || window.Contains(window.EndAt) {
		t.Fatal("window boundary semantics are incorrect")
	}
	for _, invalid := range []ChangeWindow{
		{StartAt: start, EndAt: start},
		{StartAt: start.Add(time.Hour), EndAt: start, Timezone: "UTC"},
		{StartAt: start, EndAt: start.Add(time.Hour), Timezone: "Not/AZone"},
	} {
		if err := invalid.Validate(); !errors.Is(err, ErrInvalidChangeWindow) {
			t.Errorf("invalid window accepted: %#v (%v)", invalid, err)
		}
	}
}

func TestDAGValidationReadyNodesAndTopologicalOrder(t *testing.T) {
	nodes := []DAGNode{
		{ID: "prepare", State: BatchNodeSucceeded},
		{ID: "canary", Dependencies: []string{"prepare"}, State: BatchNodePending},
		{ID: "observe", Dependencies: []string{"canary"}, State: BatchNodePending},
		{ID: "independent", State: BatchNodePending},
	}
	if err := ValidateDAG(nodes); err != nil {
		t.Fatalf("valid DAG rejected: %v", err)
	}
	dag := DAG{Nodes: nodes}
	if err := dag.Validate(); err != nil {
		t.Fatalf("DAG method rejected graph: %v", err)
	}
	ready, err := SelectReadyNodes(nodes)
	if err != nil {
		t.Fatalf("select ready nodes: %v", err)
	}
	if got := []string{ready[0].ID, ready[1].ID}; !reflect.DeepEqual(got, []string{"canary", "independent"}) {
		t.Fatalf("ready IDs = %#v", got)
	}
	order, err := TopologicalOrder(nodes)
	if err != nil {
		t.Fatalf("topological order: %v", err)
	}
	position := make(map[string]int, len(order))
	for index, node := range order {
		position[node.ID] = index
	}
	if position["prepare"] > position["canary"] || position["canary"] > position["observe"] {
		t.Fatalf("dependency order violated: %#v", position)
	}
	if methodReady, err := dag.ReadyNodes(); err != nil || len(methodReady) != len(ready) {
		t.Fatalf("DAG ready method result=%#v err=%v", methodReady, err)
	}
	if methodOrder, err := dag.TopologicalOrder(); err != nil || len(methodOrder) != len(nodes) {
		t.Fatalf("DAG order method result=%#v err=%v", methodOrder, err)
	}
	if ids, err := ReadyNodeIDs(nodes); err != nil || !reflect.DeepEqual(ids, []string{"canary", "independent"}) {
		t.Fatalf("ready node IDs=%#v err=%v", ids, err)
	}

	invalidDAGs := [][]DAGNode{
		{{ID: "a", State: BatchNodePending}, {ID: "a", State: BatchNodePending}},
		{{ID: "a", Dependencies: []string{"missing"}, State: BatchNodePending}},
		{{ID: "a", Dependencies: []string{"a"}, State: BatchNodePending}},
		{{ID: "a", Dependencies: []string{"b"}, State: BatchNodePending}, {ID: "b", Dependencies: []string{"a"}, State: BatchNodePending}},
	}
	for _, invalid := range invalidDAGs {
		if err := ValidateDAG(invalid); err == nil {
			t.Errorf("invalid DAG accepted: %#v", invalid)
		}
	}
	if err := ValidateDAG([]DAGNode{{ID: "a", State: BatchNodeState("bad")}}); !errors.Is(err, ErrInvalidDAG) {
		t.Fatalf("invalid state error = %v", err)
	}
	if !BatchNodeSucceeded.Terminal() || BatchNodeRunning.Terminal() ||
		ValidateNodeTransition(BatchNodePending, BatchNodeReady) != nil ||
		!errors.Is(ValidateNodeTransition(BatchNodeSucceeded, BatchNodeRunning), ErrInvalidNodeTransition) {
		t.Fatal("DAG node state helpers are incorrect")
	}
}

func TestBatchTaskValidationAndTransitions(t *testing.T) {
	task := BatchTask{
		ID: "batch-1", Action: "restart", TargetIDs: []string{"server-a", "server-b"},
		Nodes:         []DAGNode{{ID: "restart-a", TargetID: "server-a", State: BatchNodePending}},
		BatchPolicy:   BatchPolicy{Strategy: BatchFixed, BatchSize: 1},
		Concurrency:   ConcurrencyPolicy{Scope: ConcurrencyGlobal, MaxConcurrent: 1},
		FailurePolicy: FailureStop, State: BatchTaskPending,
	}
	if err := task.Validate(); err != nil {
		t.Fatalf("valid batch task rejected: %v", err)
	}
	if err := ValidateBatchTask(task); err != nil {
		t.Fatalf("batch task validation wrapper rejected task: %v", err)
	}
	bad := task
	bad.Nodes = []DAGNode{{ID: "restart-a", TargetID: "server-z", State: BatchNodePending}}
	if err := bad.Validate(); !errors.Is(err, ErrInvalidFleet) {
		t.Fatalf("out-of-scope target accepted: %v", err)
	}
	if !CanTransitionBatchTask(BatchTaskPending, BatchTaskPlanning) || CanTransitionBatchTask(BatchTaskSucceeded, BatchTaskRunning) {
		t.Fatal("batch task transition rules are incorrect")
	}
	if !CanTransitionDAGNode(BatchNodeRunning, BatchNodeSucceeded) || CanTransitionDAGNode(BatchNodeSucceeded, BatchNodeRunning) {
		t.Fatal("DAG node transition rules are incorrect")
	}
	if !BatchTaskSucceeded.Terminal() || BatchTaskRunning.Terminal() || !CanTransition(BatchTaskRunning, BatchTaskPaused) || CanTransition("running", "paused") {
		t.Fatal("generic task state helpers are incorrect")
	}
	fleet := Fleet{Servers: []ServerNode{testServer("a")}}
	if selected, err := SelectTargets(fleet, NodeSelector{MatchLabels: map[string]string{"role": "web"}}); err != nil || len(selected) != 1 {
		t.Fatalf("select targets result=%#v err=%v", selected, err)
	}
}
