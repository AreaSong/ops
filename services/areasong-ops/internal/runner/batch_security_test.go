package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AreaSong/ops/services/areasong-ops/internal/config"
	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	"github.com/AreaSong/ops/services/areasong-ops/internal/store"
)

func TestBatchHTTPFourActorTwoTargetCanaryEndToEnd(t *testing.T) {
	ctx := context.Background()
	executor := &fakeExecutor{}
	engine, database := testEngine(t, executor)
	engine.catalog.SchemaVersion = 4
	addSecondBatchService(engine)
	setBatchActionRisk(engine, "demo", model.RiskHigh, 0)
	setBatchActionRisk(engine, "demo-two", model.RiskHigh, 0)
	actors := installBatchSecurityActors(engine, 5)
	handler := NewServer(engine, database)
	client := runnerAPITestClient{t: t, handler: handler}

	create := model.BatchCreateRequest{
		Action:         "restart",
		TargetIDs:      []string{"demo-two", "demo"},
		BatchPolicy:    model.BatchPolicy{Strategy: model.BatchCanary, CanarySize: 1, BatchSize: 1, ObservationSeconds: 1},
		Concurrency:    model.ConcurrencyPolicy{Scope: model.ConcurrencyGlobal, MaxConcurrent: 1},
		FailurePolicy:  model.FailureStop,
		IdempotencyKey: mustUUID(t),
	}
	var operation model.BatchOperation
	client.as(actors[0]).request(http.MethodPost, "/v1/batches", create, http.StatusCreated, &operation)
	if operation.State != model.BatchPendingApproval || !operation.RequiresDualApproval ||
		operation.ActorHash != actors[0] || len(operation.Items) != 2 ||
		operation.Items[0].Service != "demo" || operation.Items[1].Service != "demo-two" {
		t.Fatalf("created batch=%+v", operation)
	}

	var replay model.BatchOperation
	client.as(actors[0]).request(http.MethodPost, "/v1/batches", create, http.StatusOK, &replay)
	if replay.ID != operation.ID || replay.Digest != operation.Digest || len(replay.Items) != 2 {
		t.Fatalf("idempotent replay=%+v original=%+v", replay, operation)
	}

	approval := model.BatchApproveRequest{Digest: operation.Digest, Confirmation: operation.ConfirmationPhrase}
	client.as(actors[0]).request(http.MethodPost, "/v1/batches/"+operation.ID+"/approve", approval, http.StatusConflict, nil)
	client.as(actors[1]).request(http.MethodPost, "/v1/batches/"+operation.ID+"/approve", approval, http.StatusOK, &operation)
	if operation.State != model.BatchPendingApproval || operation.ApprovedByHash != actors[1] || operation.SecondApprovedByHash != "" {
		t.Fatalf("first approval=%+v", operation)
	}
	client.as(actors[2]).request(http.MethodPost, "/v1/batches/"+operation.ID+"/approve", approval, http.StatusOK, &operation)
	if operation.State != model.BatchApproved || operation.ApprovedByHash != actors[1] || operation.SecondApprovedByHash != actors[2] {
		t.Fatalf("second approval=%+v", operation)
	}

	execution := model.BatchExecuteRequest{IdempotencyKey: mustUUID(t)}
	client.as(actors[2]).request(http.MethodPost, "/v1/batches/"+operation.ID+"/run", execution, http.StatusConflict, nil)
	client.as(actors[3]).request(http.MethodPost, "/v1/batches/"+operation.ID+"/run", execution, http.StatusAccepted, &operation)
	if operation.State != model.BatchRunning || operation.ExecutedByHash != actors[3] {
		t.Fatalf("started batch=%+v", operation)
	}
	client.as(actors[3]).request(http.MethodPost, "/v1/batches/"+operation.ID+"/run", execution, http.StatusAccepted, &replay)
	client.as(actors[4]).request(http.MethodPost, "/v1/batches/"+operation.ID+"/run", execution, http.StatusConflict, nil)

	engine.Wait()
	client.as(actors[3]).request(http.MethodGet, "/v1/batches/"+operation.ID, nil, http.StatusOK, &operation)
	if operation.State != model.BatchSucceeded || operation.CanaryObservedAt == nil || operation.ExecutedByHash != actors[3] {
		t.Fatalf("finished batch=%+v", operation)
	}
	for _, item := range operation.Items {
		if item.State != model.BatchNodeSucceeded || item.PlanID == "" || item.TaskID == "" {
			t.Fatalf("finished item=%+v", item)
		}
		plan, err := database.GetReleasePlan(ctx, item.PlanID)
		if err != nil {
			t.Fatal(err)
		}
		if plan.ActorHash != actors[0] || plan.ApprovedByHash != actors[1] ||
			plan.SecondApprovedByHash != actors[2] || plan.ExecutedByHash != actors[3] ||
			!plan.RequiresDualApproval {
			t.Fatalf("child plan identity=%+v", plan)
		}
	}
	if got := countBatchPhaseCalls(executor, "restart"); got != 2 {
		t.Fatalf("restart phase calls=%d, want 2", got)
	}

	audit, err := database.ListAudit(ctx, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	events := map[string]int{}
	for _, entry := range audit {
		if entry.Resource == operation.ID {
			events[entry.Event]++
		}
	}
	if events["batch.created"] != 1 || events["batch.approved"] != 2 || events["batch.started"] != 1 {
		t.Fatalf("batch audit events=%v", events)
	}
}

type runnerAPITestClient struct {
	t       *testing.T
	handler http.Handler
	actor   string
}

func (client runnerAPITestClient) as(actor string) runnerAPITestClient {
	client.actor = actor
	return client
}

func (client runnerAPITestClient) request(
	method, path string,
	body any,
	wantStatus int,
	result any,
) *httptest.ResponseRecorder {
	client.t.Helper()
	var payload []byte
	var err error
	if body != nil {
		payload, err = json.Marshal(body)
		if err != nil {
			client.t.Fatal(err)
		}
	}
	request := httptest.NewRequest(method, path, bytes.NewReader(payload))
	request.Header.Set(actorHeader, client.actor)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	client.handler.ServeHTTP(response, request)
	if response.Code != wantStatus {
		client.t.Fatalf("%s %s status=%d want=%d body=%s", method, path, response.Code, wantStatus, response.Body.String())
	}
	if result != nil {
		if err := json.Unmarshal(response.Body.Bytes(), result); err != nil {
			client.t.Fatalf("decode %s %s: %v body=%s", method, path, err, response.Body.String())
		}
	}
	return response
}

func TestBatchChildBindingRejectsPreoccupiedIdempotencyKeys(t *testing.T) {
	ctx := context.Background()
	executor := &fakeExecutor{}
	engine, database := testEngine(t, executor)
	engine.catalog.SchemaVersion = 4
	addSecondBatchService(engine)
	setBatchActionRisk(engine, "demo", model.RiskHigh, 0)
	setBatchActionRisk(engine, "demo-two", model.RiskHigh, 0)
	actors := installBatchSecurityActors(engine, 5)

	op := createSecurityBatch(t, engine, actors[0], []string{"demo", "demo-two"})
	op = approveSecurityBatch(t, engine, op, actors[1], actors[2])
	first := op.Items[0]
	planKey := batchItemIdempotencyKey(op.ID, first.ID, "plan")
	taskKey := batchItemIdempotencyKey(op.ID, first.ID, "execute")
	foreign, err := engine.CreateReleasePlan(ctx, actors[4], model.PreviewRequest{
		Service: first.Service, Action: "inspect", IdempotencyKey: planKey,
	})
	if err != nil {
		t.Fatal(err)
	}
	foreign, err = engine.ApproveReleasePlan(ctx, actors[4], foreign.ID, model.ApprovePlanRequest{
		Digest: foreign.Digest, Confirmation: foreign.ConfirmationPhrase,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := engine.ExecuteReleasePlan(ctx, actors[4], foreign.ID, model.ExecutePlanRequest{IdempotencyKey: taskKey}); err != nil {
		t.Fatal(err)
	}
	engine.Wait()

	if _, err := engine.ExecuteBatch(ctx, actors[3], op.ID, model.BatchExecuteRequest{IdempotencyKey: mustUUID(t)}); err != nil {
		t.Fatal(err)
	}
	engine.Wait()
	finished, err := database.GetBatchOperation(ctx, op.ID)
	if err != nil || finished.State != model.BatchPaused || finished.Items[0].State != model.BatchNodeFailed {
		t.Fatalf("preoccupied batch=%+v err=%v", finished, err)
	}
	if got := countBatchPhaseCalls(executor, "restart"); got != 0 {
		t.Fatalf("preoccupied child executed restart %d times", got)
	}
}

func TestMixedRiskBatchUsesPerChildApprovalPolicy(t *testing.T) {
	ctx := context.Background()
	engine, database := testEngine(t, &fakeExecutor{})
	engine.catalog.SchemaVersion = 4
	addSecondBatchService(engine)
	setBatchActionRisk(engine, "demo", model.RiskHigh, 0)
	setBatchActionRisk(engine, "demo-two", model.RiskMedium, 0)
	actors := installBatchSecurityActors(engine, 4)

	op := createSecurityBatch(t, engine, actors[0], []string{"demo", "demo-two"})
	op = approveSecurityBatch(t, engine, op, actors[1], actors[2])
	if _, err := engine.ExecuteBatch(ctx, actors[3], op.ID, model.BatchExecuteRequest{IdempotencyKey: mustUUID(t)}); err != nil {
		t.Fatal(err)
	}
	engine.Wait()
	finished, err := database.GetBatchOperation(ctx, op.ID)
	if err != nil || finished.State != model.BatchSucceeded {
		t.Fatalf("mixed-risk batch=%+v err=%v", finished, err)
	}
	for _, item := range finished.Items {
		plan, err := database.GetReleasePlan(ctx, item.PlanID)
		if err != nil {
			t.Fatal(err)
		}
		if item.Service == "demo" {
			if !plan.RequiresDualApproval || plan.ActorHash != actors[0] ||
				plan.ApprovedByHash != actors[1] || plan.SecondApprovedByHash != actors[2] ||
				plan.ExecutedByHash != actors[3] {
				t.Fatalf("high-risk child=%+v", plan)
			}
			continue
		}
		if plan.RequiresDualApproval || plan.ActorHash != actors[3] ||
			plan.ApprovedByHash != actors[3] || plan.SecondApprovedByHash != "" ||
			plan.ExecutedByHash != actors[3] {
			t.Fatalf("medium-risk child=%+v", plan)
		}
	}
}

func TestLegacyRunningBatchFailsClosedBeforeCreatingChildPlan(t *testing.T) {
	ctx := context.Background()
	engine, database := testEngine(t, &fakeExecutor{})
	op := durableBatchForRecovery(model.BatchRunning)
	op.ID = "batch-legacy-running"
	op.IdempotencyKey = "batch-legacy-running-key"
	op.ApprovalPolicyVersion = 0
	op.Items[0].State = model.BatchNodeReady
	op.Task.Nodes[0].State = model.BatchNodeReady
	if _, created, err := database.CreateBatchOperation(ctx, store.BatchOperationInput{
		Operation: op, ConfirmationHash: store.HashConfirmation(op.ConfirmationPhrase),
	}); err != nil || !created {
		t.Fatalf("create legacy batch: created=%v err=%v", created, err)
	}

	engine.runBatch(op)
	stored, err := database.GetBatchOperation(ctx, op.ID)
	if err != nil || stored.State != model.BatchNeedsAttention || stored.Items[0].PlanID != "" {
		t.Fatalf("legacy batch=%+v err=%v", stored, err)
	}
}

func TestAreaForgeLifecycleBatchDoesNotInheritC2SingleActorException(t *testing.T) {
	ctx := context.Background()
	engine, database := testEngine(t, &fakeExecutor{})
	engine.lifecycleObservationSeconds = 0
	engine.catalog.Services["areaforge"] = model.ServiceDefinition{
		Name: "areaforge", ObjectID: "service:areaforge", TenantID: "production", Adapter: "/tmp/areaforge",
		Metadata: model.ObjectMetadata{Type: "service", Environment: "production", Lifecycle: "active"},
		AlertPolicy: model.AlertPolicyDefinition{
			Matchers: map[string]string{"service": "areaforge"}, MaintenanceAlerts: []string{"AppHttpProbeFailed"},
		},
	}
	op := model.BatchOperation{
		ID: "batch-c2-scope", ActorHash: strings.Repeat("1", 64),
		ApprovedByHash: strings.Repeat("2", 64), SecondApprovedByHash: strings.Repeat("3", 64),
		ExecutedByHash: strings.Repeat("4", 64), Action: "stop", RequiresDualApproval: true,
		ApprovalPolicyVersion: model.CurrentBatchApprovalPolicyVersion,
	}
	item := model.BatchItem{ID: "item-c2-scope", Service: "areaforge", State: model.BatchNodeReady}
	engine.startBatchItem(ctx, op, item)
	engine.Wait()
	plan, found, err := database.GetReleasePlanByRequest(ctx, batchItemIdempotencyKey(op.ID, item.ID, "plan"))
	if err != nil || !found {
		t.Fatalf("child plan=%+v found=%v err=%v", plan, found, err)
	}
	if plan.ExecutedByHash == "" {
		if _, _, err := engine.ExecuteReleasePlan(ctx, op.ExecutedByHash, plan.ID, model.ExecutePlanRequest{
			IdempotencyKey: batchItemIdempotencyKey(op.ID, item.ID, "execute"),
		}); err != nil {
			t.Fatalf("execute C2 batch child: %v", err)
		}
		engine.Wait()
		plan, err = database.GetReleasePlan(ctx, plan.ID)
		if err != nil {
			t.Fatal(err)
		}
	}
	if !plan.RequiresDualApproval || plan.ApprovalSummary.ApprovalException != "" ||
		plan.ActorHash != op.ActorHash || plan.ApprovedByHash != op.ApprovedByHash ||
		plan.SecondApprovedByHash != op.SecondApprovedByHash || plan.ExecutedByHash != op.ExecutedByHash {
		t.Fatalf("C2 batch child inherited direct-plan exception: %+v", plan)
	}
}

func createSecurityBatch(
	t *testing.T,
	engine *Engine,
	creator string,
	targets []string,
) model.BatchOperation {
	t.Helper()
	op, created, err := engine.CreateBatch(context.Background(), creator, model.BatchCreateRequest{
		Action: "restart", TargetIDs: targets,
		BatchPolicy:   model.BatchPolicy{Strategy: model.BatchCanary, CanarySize: 1, BatchSize: 1, ObservationSeconds: 1},
		Concurrency:   model.ConcurrencyPolicy{Scope: model.ConcurrencyGlobal, MaxConcurrent: 1},
		FailurePolicy: model.FailureStop, IdempotencyKey: mustUUID(t),
	})
	if err != nil || !created {
		t.Fatalf("create batch=%+v created=%v err=%v", op, created, err)
	}
	return op
}

func approveSecurityBatch(
	t *testing.T,
	engine *Engine,
	op model.BatchOperation,
	first, second string,
) model.BatchOperation {
	t.Helper()
	var err error
	op, err = engine.ApproveBatch(context.Background(), first, op.ID, model.BatchApproveRequest{Digest: op.Digest, Confirmation: op.ConfirmationPhrase})
	if err == nil {
		op, err = engine.ApproveBatch(context.Background(), second, op.ID, model.BatchApproveRequest{Digest: op.Digest, Confirmation: op.ConfirmationPhrase})
	}
	if err != nil {
		t.Fatal(err)
	}
	return op
}

func installBatchSecurityActors(engine *Engine, count int) []string {
	actors := make([]string, count)
	principals := make(map[string]config.AccessPrincipal, count)
	for index := range actors {
		actors[index] = strings.Repeat(string(rune('1'+index)), 64)
		principals[actors[index]] = config.AccessPrincipal{Subject: actors[index], TenantID: "default", Roles: []string{"admin"}}
	}
	engine.catalog.Access = &config.AccessPolicy{
		Enforced: true, DefaultTenant: "default",
		Tenants:    map[string]model.Tenant{"default": {ID: "default", Status: "active"}},
		Roles:      map[string]model.Role{"admin": {ID: "admin", Permissions: []model.Permission{model.Permission("*")}}},
		Principals: principals,
	}
	return actors
}

func setBatchActionRisk(engine *Engine, serviceName string, risk model.Risk, observationSeconds int) {
	service := engine.catalog.Services[serviceName]
	action := service.Actions["restart"]
	action.Risk = risk
	action.ObservationSeconds = observationSeconds
	service.Actions["restart"] = action
	engine.catalog.Services[serviceName] = service
}
