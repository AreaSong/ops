package runner

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/config"
	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	"github.com/AreaSong/ops/services/areasong-ops/internal/store"
)

func TestExecuteBatchIsIdempotentAcrossMultipleWaves(t *testing.T) {
	ctx := context.Background()
	executor := &fakeExecutor{}
	engine, database := testEngine(t, executor)
	addSecondBatchService(engine)
	setBatchObservationSeconds(engine, 0)

	op := createApprovedBatch(t, engine, []string{"demo", "demo-two"}, model.FailureStop)
	runKey := mustUUID(t)
	started, err := engine.ExecuteBatch(ctx, actorHash(), op.ID, model.BatchExecuteRequest{IdempotencyKey: runKey})
	if err != nil || started.State != model.BatchRunning {
		t.Fatalf("started=%+v err=%v", started, err)
	}
	replayed, err := engine.ExecuteBatch(ctx, actorHash(), op.ID, model.BatchExecuteRequest{IdempotencyKey: runKey})
	if err != nil || replayed.ID != op.ID {
		t.Fatalf("replayed=%+v err=%v", replayed, err)
	}

	engine.Wait()
	finished, err := database.GetBatchOperation(ctx, op.ID)
	if err != nil || finished.State != model.BatchSucceeded {
		t.Fatalf("finished=%+v err=%v", finished, err)
	}
	if len(finished.Items) != 2 || finished.Items[0].State != model.BatchNodeSucceeded ||
		finished.Items[1].State != model.BatchNodeSucceeded {
		t.Fatalf("items=%+v", finished.Items)
	}
	if got := countBatchPhaseCalls(executor, "restart"); got != 2 {
		t.Fatalf("restart phase calls=%d, want 2", got)
	}
	if _, err := engine.ExecuteBatch(ctx, actorHash(), op.ID, model.BatchExecuteRequest{IdempotencyKey: mustUUID(t)}); !errors.Is(err, store.ErrIdempotency) {
		t.Fatalf("different execution key error=%v, want ErrIdempotency", err)
	}
}

func TestCreateBatchReplayIsIdempotentAndHydrated(t *testing.T) {
	ctx := context.Background()
	engine, database := testEngine(t, &fakeExecutor{})
	request := model.BatchCreateRequest{
		Action:         "restart",
		TargetIDs:      []string{"demo"},
		BatchPolicy:    model.BatchPolicy{Strategy: model.BatchFixed, BatchSize: 1},
		Concurrency:    model.ConcurrencyPolicy{Scope: model.ConcurrencyGlobal, MaxConcurrent: 1},
		FailurePolicy:  model.FailureStop,
		IdempotencyKey: mustUUID(t),
	}
	first, created, err := engine.CreateBatch(ctx, actorHash(), request)
	if err != nil || !created {
		t.Fatalf("first batch=%+v created=%v err=%v", first, created, err)
	}
	second, created, err := engine.CreateBatch(ctx, actorHash(), request)
	if err != nil || created {
		t.Fatalf("replayed batch=%+v created=%v err=%v", second, created, err)
	}
	if second.ID != first.ID || second.Digest != first.Digest ||
		second.ConfirmationPhrase != first.ConfirmationPhrase || len(second.Items) != len(first.Items) {
		t.Fatalf("replayed batch lost stable fields: first=%+v second=%+v", first, second)
	}
	stored, err := database.GetBatchOperation(ctx, first.ID)
	if err != nil || len(stored.Items) != 1 {
		t.Fatalf("stored replay items=%+v err=%v", stored.Items, err)
	}
	approved, err := engine.ApproveBatch(ctx, actorHash(), second.ID, model.BatchApproveRequest{
		Digest: second.Digest, Confirmation: second.ConfirmationPhrase,
	})
	if err != nil || approved.State != model.BatchApproved {
		t.Fatalf("replayed batch approval=%+v err=%v", approved, err)
	}
}

func TestBatchFailureStopsLaterWaves(t *testing.T) {
	ctx := context.Background()
	executor := &fakeExecutor{failPhase: "health"}
	engine, database := testEngine(t, executor)
	addSecondBatchService(engine)
	setBatchObservationSeconds(engine, 0)

	op := createApprovedBatch(t, engine, []string{"demo", "demo-two"}, model.FailureStop)
	if _, err := engine.ExecuteBatch(ctx, actorHash(), op.ID, model.BatchExecuteRequest{IdempotencyKey: mustUUID(t)}); err != nil {
		t.Fatal(err)
	}
	engine.Wait()

	finished, err := database.GetBatchOperation(ctx, op.ID)
	if err != nil || finished.State != model.BatchNeedsAttention {
		t.Fatalf("finished=%+v err=%v", finished, err)
	}
	if len(finished.Items) != 2 || finished.Items[0].State != model.BatchNodeFailed ||
		finished.Items[1].State != model.BatchNodePending {
		t.Fatalf("items=%+v", finished.Items)
	}
	if got := countServicePhaseCalls(executor, "demo-two", "restart"); got != 0 {
		t.Fatalf("later wave restart calls=%d, want 0", got)
	}
}

func TestBatchFailureContinueRunsLaterWavesAndEndsNeedsAttention(t *testing.T) {
	ctx := context.Background()
	executor := &fakeExecutor{failPhase: "health"}
	engine, database := testEngine(t, executor)
	addSecondBatchService(engine)
	setBatchObservationSeconds(engine, 0)

	op := createApprovedBatch(t, engine, []string{"demo", "demo-two"}, model.FailureContinue)
	if _, err := engine.ExecuteBatch(ctx, actorHash(), op.ID, model.BatchExecuteRequest{IdempotencyKey: mustUUID(t)}); err != nil {
		t.Fatal(err)
	}
	engine.Wait()

	finished, err := database.GetBatchOperation(ctx, op.ID)
	if err != nil || finished.State != model.BatchNeedsAttention {
		t.Fatalf("finished=%+v err=%v", finished, err)
	}
	if len(finished.Items) != 2 || finished.Items[0].State != model.BatchNodeFailed ||
		finished.Items[1].State != model.BatchNodeFailed {
		t.Fatalf("items=%+v", finished.Items)
	}
	if got := countServicePhaseCalls(executor, "demo-two", "restart"); got != 1 {
		t.Fatalf("continued wave restart calls=%d, want 1", got)
	}
}

func TestProductionBatchRequiresExplicitCanaryAndFailStop(t *testing.T) {
	ctx := context.Background()
	engine, _ := testEngine(t, &fakeExecutor{})
	engine.catalog.SchemaVersion = 4
	addSecondBatchService(engine)

	base := model.BatchCreateRequest{
		Action:         "restart",
		TargetIDs:      []string{"demo", "demo-two"},
		BatchPolicy:    model.BatchPolicy{Strategy: model.BatchFixed, BatchSize: 1},
		Concurrency:    model.ConcurrencyPolicy{Scope: model.ConcurrencyGlobal, MaxConcurrent: 1},
		FailurePolicy:  model.FailureStop,
		IdempotencyKey: mustUUID(t),
	}
	tests := []struct {
		name    string
		request model.BatchCreateRequest
		want    string
	}{
		{name: "fixed strategy", request: base, want: "必须先执行 Canary"},
		{name: "zero observation", request: func() model.BatchCreateRequest {
			r := base
			r.BatchPolicy = model.BatchPolicy{Strategy: model.BatchCanary, CanarySize: 1, BatchSize: 1}
			r.IdempotencyKey = mustUUID(t)
			return r
		}(), want: "正数观察窗口"},
		{name: "continue policy", request: func() model.BatchCreateRequest {
			r := base
			r.BatchPolicy = model.BatchPolicy{Strategy: model.BatchCanary, CanarySize: 1, BatchSize: 1, ObservationSeconds: 1}
			r.FailurePolicy = model.FailureContinue
			r.IdempotencyKey = mustUUID(t)
			return r
		}(), want: "必须停止后续批次"},
		{name: "selector wildcard", request: func() model.BatchCreateRequest {
			r := base
			r.TargetIDs = nil
			r.TargetSelector = model.NodeSelector{MatchLabels: map[string]string{"environment": "production"}}
			r.IdempotencyKey = mustUUID(t)
			return r
		}(), want: "必须显式列出目标 ID"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := engine.CreateBatch(ctx, actorHash(), test.request)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("err=%v, want message containing %q", err, test.want)
			}
		})
	}
}

func TestBatchChildPlanPreservesParentFourActorChain(t *testing.T) {
	ctx := context.Background()
	engine, database := testEngine(t, &fakeExecutor{})
	service := engine.catalog.Services["demo"]
	action := service.Actions["restart"]
	action.Risk = model.RiskHigh
	action.ObservationSeconds = 0
	service.Actions["restart"] = action
	engine.catalog.Services["demo"] = service
	op := model.BatchOperation{ID: "batch-child-identity", ActorHash: strings.Repeat("1", 64), ApprovedByHash: strings.Repeat("2", 64), SecondApprovedByHash: strings.Repeat("4", 64), ExecutedByHash: strings.Repeat("3", 64), Action: "restart", Target: "", RequiresDualApproval: true, ApprovalPolicyVersion: model.CurrentBatchApprovalPolicyVersion}
	item := model.BatchItem{ID: "item-child-identity", Service: "demo", State: model.BatchNodeReady}
	engine.startBatchItem(ctx, op, item)

	plan, found, err := database.GetReleasePlanByRequest(ctx, batchItemIdempotencyKey(op.ID, item.ID, "plan"))
	if err != nil || !found {
		t.Fatalf("child plan lookup: plan=%+v found=%v err=%v", plan, found, err)
	}
	if plan.ActorHash != op.ActorHash || plan.ApprovedByHash != op.ApprovedByHash || plan.SecondApprovedByHash != op.SecondApprovedByHash {
		t.Fatalf("child plan identities: actor=%q approver=%q second=%q, want actor=%q approver=%q second=%q", plan.ActorHash, plan.ApprovedByHash, plan.SecondApprovedByHash, op.ActorHash, op.ApprovedByHash, op.SecondApprovedByHash)
	}
	// startBatchItem enqueues the child task asynchronously.  Wait before the
	// fixture closes SQLite so terminal-state writes cannot race database.Close.
	engine.Wait()
	plan, err = database.GetReleasePlan(ctx, plan.ID)
	if err != nil || plan.State != model.PlanCompleted || plan.ExecutedByHash != op.ExecutedByHash {
		t.Fatalf("child plan execution: plan=%+v err=%v", plan, err)
	}
}

func TestBatchChildPlanResumesAfterFirstApproval(t *testing.T) {
	ctx := context.Background()
	engine, database := testEngine(t, &fakeExecutor{})
	service := engine.catalog.Services["demo"]
	action := service.Actions["restart"]
	action.Risk = model.RiskHigh
	action.ObservationSeconds = 0
	service.Actions["restart"] = action
	engine.catalog.Services["demo"] = service
	op := model.BatchOperation{ID: "batch-child-resume", ActorHash: strings.Repeat("1", 64), ApprovedByHash: strings.Repeat("2", 64), SecondApprovedByHash: strings.Repeat("3", 64), ExecutedByHash: strings.Repeat("4", 64), Action: "restart", RequiresDualApproval: true, ApprovalPolicyVersion: model.CurrentBatchApprovalPolicyVersion}
	item := model.BatchItem{ID: "item-child-resume", Service: "demo", State: model.BatchNodeReady}
	plan, err := engine.CreateReleasePlan(ctx, op.ActorHash, model.PreviewRequest{
		Service: item.Service, Action: op.Action,
		IdempotencyKey: batchItemIdempotencyKey(op.ID, item.ID, "plan"), RequiresDualApproval: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.ApproveReleasePlan(ctx, op.ApprovedByHash, plan.ID, model.ApprovePlanRequest{Digest: plan.Digest, Confirmation: plan.ConfirmationPhrase}); err != nil {
		t.Fatal(err)
	}

	engine.startBatchItem(ctx, op, item)
	engine.Wait()
	stored, err := database.GetReleasePlan(ctx, plan.ID)
	if err != nil || stored.State != model.PlanCompleted || stored.ApprovedByHash != op.ApprovedByHash ||
		stored.SecondApprovedByHash != op.SecondApprovedByHash || stored.ExecutedByHash != op.ExecutedByHash {
		t.Fatalf("resumed child plan=%+v err=%v", stored, err)
	}
}

func TestProductionHighRiskBatchPreservesFourActorApprovalChain(t *testing.T) {
	ctx := context.Background()
	engine, database := testEngine(t, &fakeExecutor{})
	engine.catalog.SchemaVersion = 4
	addSecondBatchService(engine)
	for _, name := range []string{"demo", "demo-two"} {
		service := engine.catalog.Services[name]
		action := service.Actions["restart"]
		action.Risk = model.RiskHigh
		// Force child plans through the observing/close path so the test covers
		// the persisted plan actor and deterministic close idempotency key.
		action.ObservationSeconds = 1
		service.Actions["restart"] = action
		engine.catalog.Services[name] = service
	}
	creator, first, second, executor := strings.Repeat("1", 64), strings.Repeat("2", 64), strings.Repeat("3", 64), strings.Repeat("4", 64)
	admin := model.Role{ID: "admin", Permissions: []model.Permission{model.Permission("*")}}
	engine.catalog.Access = &config.AccessPolicy{
		Enforced: true, DefaultTenant: "default",
		Tenants: map[string]model.Tenant{"default": {ID: "default", Status: "active"}},
		Roles:   map[string]model.Role{"admin": admin},
		Principals: map[string]config.AccessPrincipal{
			creator:  {Subject: creator, TenantID: "default", Roles: []string{"admin"}},
			first:    {Subject: first, TenantID: "default", Roles: []string{"admin"}},
			second:   {Subject: second, TenantID: "default", Roles: []string{"admin"}},
			executor: {Subject: executor, TenantID: "default", Roles: []string{"admin"}},
		},
	}
	op, created, err := engine.CreateBatch(ctx, creator, model.BatchCreateRequest{
		Action: "restart", TargetIDs: []string{"demo", "demo-two"},
		BatchPolicy:   model.BatchPolicy{Strategy: model.BatchCanary, CanarySize: 1, BatchSize: 1, ObservationSeconds: 1},
		Concurrency:   model.ConcurrencyPolicy{Scope: model.ConcurrencyGlobal, MaxConcurrent: 1},
		FailurePolicy: model.FailureStop, IdempotencyKey: mustUUID(t),
	})
	if err != nil || !created || !op.RequiresDualApproval {
		t.Fatalf("create=%+v created=%v err=%v", op, created, err)
	}
	op, err = engine.ApproveBatch(ctx, first, op.ID, model.BatchApproveRequest{Digest: op.Digest, Confirmation: op.ConfirmationPhrase})
	if err != nil || op.State != model.BatchPendingApproval || op.ApprovedByHash != first {
		t.Fatalf("first approval=%+v err=%v", op, err)
	}
	if _, err := engine.ExecuteBatch(ctx, executor, op.ID, model.BatchExecuteRequest{IdempotencyKey: mustUUID(t)}); err == nil {
		t.Fatal("batch executed before second approval")
	}
	op, err = engine.ApproveBatch(ctx, second, op.ID, model.BatchApproveRequest{Digest: op.Digest, Confirmation: op.ConfirmationPhrase})
	if err != nil || op.State != model.BatchApproved || op.SecondApprovedByHash != second {
		t.Fatalf("second approval=%+v err=%v", op, err)
	}
	for _, forbidden := range []string{creator, first, second} {
		if _, err := engine.ExecuteBatch(ctx, forbidden, op.ID, model.BatchExecuteRequest{IdempotencyKey: mustUUID(t)}); !errors.Is(err, store.ErrActorMismatch) {
			t.Fatalf("forbidden executor %q err=%v", forbidden[:4], err)
		}
	}
	if _, err := engine.ExecuteBatch(ctx, executor, op.ID, model.BatchExecuteRequest{IdempotencyKey: mustUUID(t)}); err != nil {
		t.Fatal(err)
	}
	engine.Wait()
	finished, err := database.GetBatchOperation(ctx, op.ID)
	if err != nil || finished.State != model.BatchSucceeded {
		t.Fatalf("finished=%+v err=%v", finished, err)
	}
	for _, item := range finished.Items {
		plan, err := database.GetReleasePlan(ctx, item.PlanID)
		if err != nil || plan.ActorHash != creator || plan.ApprovedByHash != first || plan.SecondApprovedByHash != second || plan.ExecutedByHash != executor {
			t.Fatalf("child plan=%+v err=%v", plan, err)
		}
	}
}

func TestBatchSelectorResolvesRunnerByServerIDAndGatesFleetHealth(t *testing.T) {
	now := time.Now().UTC()
	cases := []struct {
		name        string
		serverState model.NodeState
		runnerState model.NodeState
		heartbeat   time.Time
		lease       *time.Time
		wantMatch   bool
	}{
		{name: "inverse association", serverState: model.NodeOnline, runnerState: model.NodeOnline, heartbeat: now, lease: timePtr(now.Add(time.Minute)), wantMatch: true},
		{name: "expired heartbeat", serverState: model.NodeOnline, runnerState: model.NodeOnline, heartbeat: now.Add(-2 * time.Minute), lease: timePtr(now.Add(time.Minute))},
		{name: "expired lease", serverState: model.NodeOnline, runnerState: model.NodeOnline, heartbeat: now, lease: timePtr(now.Add(-time.Second))},
		{name: "draining runner", serverState: model.NodeOnline, runnerState: model.NodeDraining, heartbeat: now, lease: timePtr(now.Add(time.Minute))},
		{name: "disabled runner", serverState: model.NodeOnline, runnerState: model.NodeDisabled, heartbeat: now, lease: timePtr(now.Add(time.Minute))},
		{name: "unknown server", serverState: model.NodeUnknown, runnerState: model.NodeOnline, heartbeat: now, lease: timePtr(now.Add(time.Minute))},
		{name: "draining server", serverState: model.NodeDraining, runnerState: model.NodeOnline, heartbeat: now, lease: timePtr(now.Add(time.Minute))},
		{name: "disabled server", serverState: model.NodeDisabled, runnerState: model.NodeOnline, heartbeat: now, lease: timePtr(now.Add(time.Minute))},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			engine, _ := testEngine(t, &fakeExecutor{})
			service := engine.catalog.Services["demo"]
			service.ServerID = "server-demo"
			engine.catalog.Services["demo"] = service
			engine.catalog.Fleet = &config.FleetPolicy{Enabled: true, HeartbeatTimeoutSeconds: 90, Inventory: model.Fleet{
				Servers: []model.ServerNode{{ID: "server-demo", Hostname: "demo", Environment: "production", State: test.serverState}},
				Runners: []model.RunnerNode{{ID: "runner-demo", ServerID: "server-demo", Version: "test", State: test.runnerState, LastHeartbeat: &test.heartbeat, LeaseExpiresAt: test.lease}},
			}}
			request := model.BatchCreateRequest{
				Action:         "restart",
				TargetSelector: model.NodeSelector{IDs: []string{"runner-demo"}},
				BatchPolicy:    model.BatchPolicy{Strategy: model.BatchFixed, BatchSize: 1},
				Concurrency:    model.ConcurrencyPolicy{Scope: model.ConcurrencyGlobal, MaxConcurrent: 1},
				FailurePolicy:  model.FailureStop,
				IdempotencyKey: mustUUID(t),
			}
			op, created, err := engine.CreateBatch(context.Background(), actorHash(), request)
			if test.wantMatch {
				if err != nil || !created || len(op.Items) != 1 || op.Items[0].Service != "demo" || op.Items[0].RunnerID != "runner-demo" {
					t.Fatalf("batch=%+v created=%v err=%v", op, created, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("unavailable Fleet accepted batch=%+v", op)
			}
		})
	}
}

func TestReadyBatchItemWithoutTaskFailsInsteadOfWaitingForever(t *testing.T) {
	ctx := context.Background()
	engine, database := testEngine(t, &fakeExecutor{})
	op := createApprovedBatch(t, engine, []string{"demo"}, model.FailureStop)
	item := op.Items[0]
	if err := database.UpdateBatchItemCAS(ctx, op.ID, item.ID, model.BatchNodePending, model.BatchNodeReady, "", "", ""); err != nil {
		t.Fatal(err)
	}
	item.State = model.BatchNodeReady
	if !engine.waitBatchTasks(ctx, op, []model.BatchItem{item}) {
		t.Fatal("waitBatchTasks unexpectedly stopped")
	}

	updated, err := database.GetBatchOperation(ctx, op.ID)
	if err != nil || len(updated.Items) != 1 || updated.Items[0].State != model.BatchNodeFailed {
		t.Fatalf("updated=%+v err=%v", updated, err)
	}
}

func TestResumeBatchOperationsRecoversDurableStates(t *testing.T) {
	ctx := context.Background()
	for _, state := range []model.BatchOperationState{
		model.BatchRunning, model.BatchObserving, model.BatchPaused, model.BatchRollingBack,
	} {
		t.Run(string(state), func(t *testing.T) {
			engine, database := testEngine(t, &fakeExecutor{})
			op := durableBatchForRecovery(state)
			if _, created, err := database.CreateBatchOperation(ctx, store.BatchOperationInput{
				Operation: op, ConfirmationHash: store.HashConfirmation(op.ConfirmationPhrase),
			}); err != nil || !created {
				t.Fatalf("persist recovery batch created=%v err=%v", created, err)
			}
			if err := engine.resumeBatchOperations(); err != nil {
				t.Fatal(err)
			}
			if state == model.BatchRunning || state == model.BatchObserving {
				engine.Wait()
			}
			resumed, err := database.GetBatchOperation(ctx, op.ID)
			if err != nil {
				t.Fatal(err)
			}
			switch state {
			case model.BatchRunning, model.BatchObserving:
				if resumed.State != model.BatchSucceeded {
					t.Fatalf("resumed=%+v, want succeeded", resumed)
				}
			case model.BatchPaused:
				if resumed.State != model.BatchPaused {
					t.Fatalf("paused batch unexpectedly changed: %+v", resumed)
				}
			case model.BatchRollingBack:
				if resumed.State != model.BatchNeedsAttention || resumed.Error == "" {
					t.Fatalf("rollback recovery=%+v, want needs_attention with reason", resumed)
				}
			}
		})
	}
}

func TestRunBatchClosesCompletedOperationAfterWindowEnds(t *testing.T) {
	ctx := context.Background()
	engine, database := testEngine(t, &fakeExecutor{})
	now := time.Now().UTC()
	operation := durableBatchForRecovery(model.BatchRunning)
	operation.ID = "batch-window-completed"
	operation.IdempotencyKey = "window-completed"
	operation.Task.ChangeWindow = &model.ChangeWindow{
		ID: "closed-window", StartAt: now.Add(-2 * time.Hour), EndAt: now.Add(-time.Hour), Timezone: "UTC",
	}
	if _, created, err := database.CreateBatchOperation(ctx, store.BatchOperationInput{
		Operation: operation, ConfirmationHash: store.HashConfirmation(operation.ConfirmationPhrase),
	}); err != nil || !created {
		t.Fatalf("persist completed batch created=%v err=%v", created, err)
	}

	// There are no pending or running items. A closed window must not turn this
	// already-completed operation into a pause while the coordinator is resumed.
	engine.runBatch(operation)
	finished, err := database.GetBatchOperation(ctx, operation.ID)
	if err != nil || finished.State != model.BatchSucceeded {
		t.Fatalf("finished=%+v err=%v, want succeeded", finished, err)
	}
}

func TestSelectBatchItemsHonorsPerRunnerPerServerAndQueueLimits(t *testing.T) {
	engine, _ := testEngine(t, &fakeExecutor{})
	items := []model.BatchItem{
		{ID: "a", RunnerID: "runner-a", ServerID: "server-a", State: model.BatchNodePending},
		{ID: "b", RunnerID: "runner-a", ServerID: "server-a", State: model.BatchNodePending},
		{ID: "c", RunnerID: "runner-b", ServerID: "server-a", State: model.BatchNodePending},
		{ID: "d", RunnerID: "runner-c", ServerID: "server-b", State: model.BatchNodePending},
	}
	cases := []struct {
		name   string
		policy model.ConcurrencyPolicy
		want   []string
	}{
		{name: "per runner", policy: model.ConcurrencyPolicy{Scope: model.ConcurrencyPerRunner, MaxConcurrent: 4, PerRunner: 1}, want: []string{"a", "c", "d"}},
		{name: "per server", policy: model.ConcurrencyPolicy{Scope: model.ConcurrencyPerServer, MaxConcurrent: 4, PerServer: 1}, want: []string{"a", "d"}},
		{name: "queue", policy: model.ConcurrencyPolicy{Scope: model.ConcurrencyGlobal, MaxConcurrent: 4, QueueLimit: 2}, want: []string{"a", "b"}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			selected := engine.selectBatchItems(model.BatchOperation{Task: model.BatchTask{Concurrency: test.policy}, Items: items}, items)
			got := make([]string, 0, len(selected))
			for _, item := range selected {
				got = append(got, item.ID)
			}
			if len(got) != len(test.want) {
				t.Fatalf("selected=%v want=%v", got, test.want)
			}
			for index := range got {
				if got[index] != test.want[index] {
					t.Fatalf("selected=%v want=%v", got, test.want)
				}
			}
		})
	}
}

func TestCanaryFailurePausesBeforeLaterWave(t *testing.T) {
	ctx := context.Background()
	executor := &fakeExecutor{failPhase: "health"}
	engine, database := testEngine(t, executor)
	addSecondBatchService(engine)
	setBatchObservationSeconds(engine, 0)
	op, created, err := engine.CreateBatch(ctx, actorHash(), model.BatchCreateRequest{
		Action: "restart", TargetIDs: []string{"demo", "demo-two"},
		BatchPolicy:   model.BatchPolicy{Strategy: model.BatchCanary, CanarySize: 1, BatchSize: 1, ObservationSeconds: 1},
		Concurrency:   model.ConcurrencyPolicy{Scope: model.ConcurrencyGlobal, MaxConcurrent: 1},
		FailurePolicy: model.FailureContinue, IdempotencyKey: mustUUID(t),
	})
	if err != nil || !created {
		t.Fatalf("create=%+v created=%v err=%v", op, created, err)
	}
	op, err = engine.ApproveBatch(ctx, actorHash(), op.ID, model.BatchApproveRequest{Digest: op.Digest, Confirmation: op.ConfirmationPhrase})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := engine.ExecuteBatch(ctx, actorHash(), op.ID, model.BatchExecuteRequest{IdempotencyKey: mustUUID(t)}); err != nil {
		t.Fatal(err)
	}
	engine.Wait()
	finished, err := database.GetBatchOperation(ctx, op.ID)
	if err != nil || finished.State != model.BatchPaused {
		t.Fatalf("finished=%+v err=%v", finished, err)
	}
	if got := countServicePhaseCalls(executor, "demo-two", "restart"); got != 0 {
		t.Fatalf("later wave calls=%d, want 0", got)
	}
}

func durableBatchForRecovery(state model.BatchOperationState) model.BatchOperation {
	now := time.Now().UTC()
	item := model.BatchItem{ID: "item-recovery", ObjectID: "service:demo", Service: "demo",
		BatchIndex: 0, State: model.BatchNodeSucceeded, UpdatedAt: now}
	task := model.BatchTask{ID: "task-recovery", Action: "restart", TargetIDs: []string{"demo"},
		Nodes:         []model.DAGNode{{ID: item.ID, Action: "restart", TargetID: "demo", State: model.BatchNodeSucceeded}},
		BatchPolicy:   model.BatchPolicy{Strategy: model.BatchFixed, BatchSize: 1},
		Concurrency:   model.ConcurrencyPolicy{Scope: model.ConcurrencyGlobal, MaxConcurrent: 1},
		FailurePolicy: model.FailureStop, State: model.BatchTaskRunning, CreatedAt: now}
	return model.BatchOperation{ID: "batch-recovery-" + string(state), IdempotencyKey: "recovery-" + string(state),
		ActorHash: actorHash(), TenantID: "default", Action: "restart", Task: task, Digest: "recovery-digest",
		ConfirmationPhrase: "恢复批次", State: state, ApprovalPolicyVersion: model.CurrentBatchApprovalPolicyVersion,
		Items: []model.BatchItem{item}, CreatedAt: now, UpdatedAt: now}
}

func createApprovedBatch(
	t *testing.T,
	engine *Engine,
	targets []string,
	failurePolicy model.FailurePolicy,
) model.BatchOperation {
	t.Helper()
	op, created, err := engine.CreateBatch(context.Background(), actorHash(), model.BatchCreateRequest{
		Action:         "restart",
		TargetIDs:      targets,
		BatchPolicy:    model.BatchPolicy{Strategy: model.BatchFixed, BatchSize: 1},
		Concurrency:    model.ConcurrencyPolicy{Scope: model.ConcurrencyGlobal, MaxConcurrent: 1},
		FailurePolicy:  failurePolicy,
		IdempotencyKey: mustUUID(t),
	})
	if err != nil || !created {
		t.Fatalf("op=%+v created=%v err=%v", op, created, err)
	}
	approved, err := engine.ApproveBatch(context.Background(), actorHash(), op.ID, model.BatchApproveRequest{
		Digest: op.Digest, Confirmation: op.ConfirmationPhrase,
	})
	if err != nil {
		t.Fatal(err)
	}
	return approved
}

func addSecondBatchService(engine *Engine) {
	service := engine.catalog.Services["demo"]
	service.Name = "demo-two"
	service.ObjectID = "service:demo-two"
	service.DisplayName = "Demo Two"
	service.Actions = cloneActions(service.Actions)
	engine.catalog.Services[service.Name] = service
}

func setBatchObservationSeconds(engine *Engine, seconds int) {
	for _, name := range []string{"demo", "demo-two"} {
		service := engine.catalog.Services[name]
		service.Actions = cloneActions(service.Actions)
		action := service.Actions["restart"]
		action.ObservationSeconds = seconds
		service.Actions["restart"] = action
		engine.catalog.Services[name] = service
	}
}

func cloneActions(source map[string]model.ActionDefinition) map[string]model.ActionDefinition {
	result := make(map[string]model.ActionDefinition, len(source))
	for name, action := range source {
		result[name] = action
	}
	return result
}

func countBatchPhaseCalls(executor *fakeExecutor, phase string) int {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	count := 0
	for _, call := range executor.calls {
		if call.Phase == phase {
			count++
		}
	}
	return count
}

func countServicePhaseCalls(executor *fakeExecutor, service, phase string) int {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	count := 0
	for _, call := range executor.calls {
		if call.Service.Name == service && call.Phase == phase {
			count++
		}
	}
	return count
}

func timePtr(value time.Time) *time.Time { return &value }
