package runner

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	"github.com/AreaSong/ops/services/areasong-ops/internal/store"
)

func TestTenantIDORCannotReadMutateOrInvalidateForeignResources(t *testing.T) {
	fixture := newSecurityEngine(t)
	plan := seedTenantBPlan(t, fixture)
	task := seedTenantBTask(t, fixture)
	batch := seedTenantBBatch(t, fixture)
	client := runnerAPITestClient{t: t, handler: NewServer(fixture.engine, fixture.db)}.as(fixture.actorA)

	for _, path := range []string{
		"/v1/plans/" + plan.ID,
		"/v1/tasks/" + task.ID,
		"/v1/tasks/" + task.ID + "/events",
		"/v1/batches/" + batch.ID,
	} {
		client.request(http.MethodGet, path, nil, http.StatusForbidden, nil)
	}
	for _, request := range []struct {
		path string
		body any
	}{
		{"/v1/plans/" + plan.ID + "/approve", model.ApprovePlanRequest{Digest: plan.Digest, Confirmation: plan.ConfirmationPhrase}},
		{"/v1/plans/" + plan.ID + "/execute", model.ExecutePlanRequest{IdempotencyKey: mustUUID(t)}},
		{"/v1/plans/" + plan.ID + "/close", model.ClosePlanRequest{IdempotencyKey: mustUUID(t)}},
		{"/v1/tasks/" + task.ID + "/recovery", model.RecoveryRequest{Action: "inspect", IdempotencyKey: mustUUID(t)}},
		{"/v1/batches/" + batch.ID + "/approve", model.BatchApproveRequest{Digest: batch.Digest, Confirmation: batch.ConfirmationPhrase}},
		{"/v1/batches/" + batch.ID + "/run", model.BatchExecuteRequest{IdempotencyKey: mustUUID(t)}},
	} {
		client.request(http.MethodPost, request.path, request.body, http.StatusForbidden, nil)
	}

	for _, path := range []string{"/v1/plans", "/v1/tasks", "/v1/batches", "/v1/audit"} {
		response := client.request(http.MethodGet, path, nil, http.StatusOK, nil)
		for _, id := range []string{plan.ID, task.ID, batch.ID} {
			if strings.Contains(response.Body.String(), id) {
				t.Fatalf("collection %s leaked foreign resource %s: %s", path, id, response.Body.String())
			}
		}
	}

	persistedPlan, err := fixture.db.GetReleasePlan(context.Background(), plan.ID)
	if err != nil || persistedPlan.State != model.PlanPendingApproval || persistedPlan.InvalidatedReason != "" {
		t.Fatalf("foreign plan changed=%+v err=%v", persistedPlan, err)
	}
	persistedTask, err := fixture.db.GetTask(context.Background(), task.ID)
	if err != nil || persistedTask.State != model.TaskQueued {
		t.Fatalf("foreign task changed=%+v err=%v", persistedTask, err)
	}
	persistedBatch, err := fixture.db.GetBatchOperation(context.Background(), batch.ID)
	if err != nil || persistedBatch.State != model.BatchPendingApproval ||
		persistedBatch.ApprovedByHash != "" || persistedBatch.ExecutedByHash != "" {
		t.Fatalf("foreign batch changed=%+v err=%v", persistedBatch, err)
	}
}

func seedTenantBPlan(t *testing.T, fixture securityEngineFixture) model.ReleasePlan {
	t.Helper()
	now := time.Now().UTC()
	plan := model.ReleasePlan{
		ID: mustUUID(t), ActorHash: fixture.actorB, Service: "svc-b", Action: "inspect",
		TenantID: "tenant-b", ServerID: "server-b", Risk: model.RiskHigh,
		State: model.PlanPendingApproval, Digest: "sha256:" + strings.Repeat("1", 64),
		ConfirmationPhrase: "批准 tenant-b 计划", CreatedAt: now, UpdatedAt: now,
		ApprovalSummary: model.ApprovalSummary{
			SchemaVersion: 4, Service: "svc-b", Action: "inspect", TenantID: "tenant-b", Risk: model.RiskHigh,
		},
		// Deliberately malformed: the denied cross-tenant execution must not
		// reach the legacy-policy invalidation write.
		RequiresDualApproval: false,
	}
	if err := fixture.db.CreateReleasePlan(context.Background(), store.ReleasePlanInput{
		Plan: plan, ConfirmationHash: store.HashConfirmation(plan.ConfirmationPhrase),
	}); err != nil {
		t.Fatal(err)
	}
	seedForeignAudit(t, fixture, plan.ID)
	return plan
}

func seedTenantBTask(t *testing.T, fixture securityEngineFixture) model.Task {
	t.Helper()
	now := time.Now().UTC()
	preview := model.Preview{
		ID: mustUUID(t), ActorHash: fixture.actorB, Service: "svc-b", Action: "inspect",
		Risk: model.RiskReadOnly, Steps: []string{"inspect"}, CreatedAt: now,
		ExpiresAt: now.Add(time.Hour), ConfirmationPhrase: "检查 svc-b",
	}
	if err := fixture.db.CreatePreview(context.Background(), store.PreviewInput{
		Preview: preview, ConfirmationHash: store.HashConfirmation(preview.ConfirmationPhrase),
	}); err != nil {
		t.Fatal(err)
	}
	task, _, err := fixture.db.StartTask(context.Background(), fixture.actorB, model.StartTaskRequest{
		PreviewID: preview.ID, Confirmation: preview.ConfirmationPhrase, IdempotencyKey: mustUUID(t),
	}, mustUUID(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.db.AppendEvent(context.Background(), model.Event{
		TaskID: task.ID, Level: "info", Message: "tenant-b-only",
	}); err != nil {
		t.Fatal(err)
	}
	seedForeignAudit(t, fixture, task.ID)
	return task
}

func seedTenantBBatch(t *testing.T, fixture securityEngineFixture) model.BatchOperation {
	t.Helper()
	now := time.Now().UTC()
	item := model.BatchItem{
		ID: "item-tenant-b", ObjectID: "service:svc-b", Service: "svc-b",
		State: model.BatchNodePending, UpdatedAt: now,
	}
	operation := model.BatchOperation{
		ID: "batch-" + mustUUID(t)[:8], IdempotencyKey: mustUUID(t), ActorHash: fixture.actorB,
		TenantID: "tenant-b", Action: "inspect", Digest: "sha256:" + strings.Repeat("2", 64),
		ConfirmationPhrase: "批量检查 1 项", State: model.BatchPendingApproval,
		RequiresDualApproval: true, ApprovalPolicyVersion: model.CurrentBatchApprovalPolicyVersion,
		Items: []model.BatchItem{item}, CreatedAt: now, UpdatedAt: now,
		Task: model.BatchTask{
			ID: "batch-tenant-b", Action: "inspect", TargetIDs: []string{"svc-b"},
			Nodes:         []model.DAGNode{{ID: item.ID, Action: "inspect", TargetID: "svc-b", State: model.BatchNodePending}},
			BatchPolicy:   model.BatchPolicy{Strategy: model.BatchFixed, BatchSize: 1},
			Concurrency:   model.ConcurrencyPolicy{Scope: model.ConcurrencyGlobal, MaxConcurrent: 1},
			FailurePolicy: model.FailureStop, State: model.BatchTaskPending, CreatedAt: now,
		},
	}
	if _, created, err := fixture.db.CreateBatchOperation(context.Background(), store.BatchOperationInput{
		Operation: operation, ConfirmationHash: store.HashConfirmation(operation.ConfirmationPhrase),
	}); err != nil || !created {
		t.Fatalf("seed batch created=%v err=%v", created, err)
	}
	seedForeignAudit(t, fixture, operation.ID)
	return operation
}

func seedForeignAudit(t *testing.T, fixture securityEngineFixture, resource string) {
	t.Helper()
	if _, err := fixture.db.AppendAudit(context.Background(), model.AuditEntry{
		ActorHash: fixture.actorB, Event: "tenant-b.event", Resource: resource, Outcome: "accepted",
	}); err != nil {
		t.Fatal(err)
	}
}
