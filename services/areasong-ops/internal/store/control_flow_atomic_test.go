package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

func TestPreviewCreationAndAuditAreAtomic(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Round(0)
	database.now = func() time.Time { return now }
	preview := testPreview(now)

	installControlFlowTrigger(t, database, "fail_preview_audit", `
		BEFORE INSERT ON audit_entries WHEN NEW.event='preview.created'
		BEGIN SELECT RAISE(ABORT, 'forced preview audit failure'); END`)
	if err := database.CreatePreview(ctx, PreviewInput{
		Preview: preview, ConfirmationHash: HashConfirmation("重启 demo"),
	}); err == nil {
		t.Fatal("preview creation survived audit failure")
	}
	if _, err := database.GetPreview(ctx, preview.ID); err != ErrNotFound {
		t.Fatalf("preview survived audit rollback: %v", err)
	}
	dropControlFlowTrigger(t, database, "fail_preview_audit")

	if err := database.CreatePreview(ctx, PreviewInput{
		Preview: preview, ConfirmationHash: HashConfirmation("重启 demo"),
	}); err != nil {
		t.Fatal(err)
	}
	assertControlFlowCount(t, database,
		`SELECT COUNT(*) FROM audit_entries WHERE event='preview.created'`, 1)
}

func TestTaskStartEventAndAuditAreAtomic(t *testing.T) {
	for _, failure := range []struct {
		name, trigger, body string
	}{
		{
			name: "queued event", trigger: "fail_task_event",
			body: `BEFORE INSERT ON events WHEN NEW.phase='queued'
				BEGIN SELECT RAISE(ABORT, 'forced queued event failure'); END`,
		},
		{
			name: "accepted audit", trigger: "fail_task_audit",
			body: `BEFORE INSERT ON audit_entries WHEN NEW.event='task.accepted'
				BEGIN SELECT RAISE(ABORT, 'forced task audit failure'); END`,
		},
	} {
		t.Run(failure.name, func(t *testing.T) {
			database := openTestStore(t)
			ctx := context.Background()
			now := time.Now().UTC().Round(0)
			database.now = func() time.Time { return now }
			preview := testPreview(now)
			if err := database.CreatePreview(ctx, PreviewInput{
				Preview: preview, ConfirmationHash: HashConfirmation("重启 demo"),
			}); err != nil {
				t.Fatal(err)
			}
			installControlFlowTrigger(t, database, failure.trigger, failure.body)
			_, _, err := database.StartTask(ctx, "a", model.StartTaskRequest{
				PreviewID: preview.ID, Confirmation: "重启 demo", IdempotencyKey: "atomic-start",
			}, "task-atomic-start")
			if err == nil {
				t.Fatal("task start survived event/audit failure")
			}
			assertTaskStartRolledBack(t, database, preview.ID, "task-atomic-start")
		})
	}
}

func TestTaskStartReplayIsReadOnly(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Round(0)
	database.now = func() time.Time { return now }
	preview := testPreview(now)
	if err := database.CreatePreview(ctx, PreviewInput{
		Preview: preview, ConfirmationHash: HashConfirmation("重启 demo"),
	}); err != nil {
		t.Fatal(err)
	}
	request := model.StartTaskRequest{
		PreviewID: preview.ID, Confirmation: "重启 demo", IdempotencyKey: "replay-start",
	}
	started, err := database.StartTaskWithEvent(ctx, "a", request, "task-replay")
	if err != nil || !started.Created || started.QueuedEvent.Sequence == 0 {
		t.Fatalf("started=%+v err=%v", started, err)
	}
	replayed, err := database.StartTaskWithEvent(ctx, "a", request, "task-other")
	if err != nil || replayed.Created || replayed.Task.ID != started.Task.ID || replayed.QueuedEvent.Sequence != 0 {
		t.Fatalf("replayed=%+v err=%v", replayed, err)
	}
	assertControlFlowCount(t, database,
		`SELECT COUNT(*) FROM events WHERE task_id=? AND phase='queued'`, 1, started.Task.ID)
	assertControlFlowCount(t, database,
		`SELECT COUNT(*) FROM audit_entries WHERE resource=? AND event='task.accepted'`, 1, started.Task.ID)
}

func TestReleasePlanCreationAndReplayAuditAreAtomic(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Round(0)
	database.now = func() time.Time { return now }
	plan := atomicReleasePlan(now, "plan-create-atomic")
	input := ReleasePlanInput{Plan: plan, ConfirmationHash: HashConfirmation(plan.ConfirmationPhrase)}

	installControlFlowTrigger(t, database, "fail_plan_create_audit", `
		BEFORE INSERT ON audit_entries WHEN NEW.event='plan.created'
		BEGIN SELECT RAISE(ABORT, 'forced plan creation audit failure'); END`)
	if err := database.CreateReleasePlan(ctx, input); err == nil {
		t.Fatal("release plan creation survived audit failure")
	}
	if _, err := database.GetReleasePlan(ctx, plan.ID); err != ErrNotFound {
		t.Fatalf("release plan survived audit rollback: %v", err)
	}
	dropControlFlowTrigger(t, database, "fail_plan_create_audit")

	stored, created, err := database.CreateReleasePlanIdempotent(
		ctx, input, plan.ActorHash, "plan-create-key", "sha256:request",
	)
	if err != nil || !created {
		t.Fatalf("stored=%+v created=%v err=%v", stored, created, err)
	}
	replayInput := input
	replayInput.Plan.ID = "plan-create-other"
	replayed, created, err := database.CreateReleasePlanIdempotent(
		ctx, replayInput, plan.ActorHash, "plan-create-key", "sha256:request",
	)
	if err != nil || created || replayed.ID != stored.ID {
		t.Fatalf("replayed=%+v created=%v err=%v", replayed, created, err)
	}
	assertControlFlowCount(t, database,
		`SELECT COUNT(*) FROM audit_entries WHERE resource=? AND event='plan.created'`, 1, stored.ID)
}

func TestReleasePlanScheduleAndInvalidationAuditAreAtomic(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Round(0)
	database.now = func() time.Time { return now }
	plan := atomicReleasePlan(now, "plan-transition-atomic")
	scheduleAt := now.Add(time.Minute)
	plan.ScheduleAt = &scheduleAt
	plan.ApprovalSummary.ScheduleAt = &scheduleAt
	if err := database.CreateReleasePlan(ctx, ReleasePlanInput{
		Plan: plan, ConfirmationHash: HashConfirmation(plan.ConfirmationPhrase),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ApproveReleasePlan(ctx, plan.ID, "actor-b", plan.Digest, plan.ConfirmationPhrase); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ApproveReleasePlan(ctx, plan.ID, "actor-c", plan.Digest, plan.ConfirmationPhrase); err != nil {
		t.Fatal(err)
	}
	due := scheduleAt.Add(time.Second)

	installControlFlowTrigger(t, database, "fail_plan_schedule_audit", `
		BEFORE INSERT ON audit_entries WHEN NEW.event='plan.schedule.released'
		BEGIN SELECT RAISE(ABORT, 'forced plan schedule audit failure'); END`)
	if _, err := database.ActivateScheduledPlan(ctx, plan.ID, "actor-d", due); err == nil {
		t.Fatal("scheduled plan activation survived audit failure")
	}
	assertReleasePlanState(t, database, plan.ID, model.PlanScheduled)
	dropControlFlowTrigger(t, database, "fail_plan_schedule_audit")
	if activated, err := database.ActivateScheduledPlan(ctx, plan.ID, "actor-d", due); err != nil || !activated {
		t.Fatalf("activated=%v err=%v", activated, err)
	}
	if activated, err := database.ActivateScheduledPlan(ctx, plan.ID, "actor-d", due); err != nil || activated {
		t.Fatalf("replayed activation=%v err=%v", activated, err)
	}
	assertControlFlowCount(t, database,
		`SELECT COUNT(*) FROM audit_entries WHERE resource=? AND event='plan.schedule.released'`, 1, plan.ID)

	installControlFlowTrigger(t, database, "fail_plan_invalidation_audit", `
		BEFORE INSERT ON audit_entries WHEN NEW.event='plan.invalidated'
		BEGIN SELECT RAISE(ABORT, 'forced plan invalidation audit failure'); END`)
	if err := database.InvalidateReleasePlan(ctx, plan.ID, "actor-d", "runtime drift"); err == nil {
		t.Fatal("release plan invalidation survived audit failure")
	}
	assertReleasePlanState(t, database, plan.ID, model.PlanApproved)
	dropControlFlowTrigger(t, database, "fail_plan_invalidation_audit")
	if err := database.InvalidateReleasePlan(ctx, plan.ID, "actor-d", "runtime drift"); err != nil {
		t.Fatal(err)
	}
	invalidated, err := database.GetReleasePlan(ctx, plan.ID)
	if err != nil || invalidated.State != model.PlanInvalidated || invalidated.ApprovedByHash != "" ||
		invalidated.SecondApprovedByHash != "" {
		t.Fatalf("invalidated=%+v err=%v", invalidated, err)
	}
	assertControlFlowCount(t, database,
		`SELECT COUNT(*) FROM audit_entries WHERE resource=? AND event='plan.invalidated'`, 1, plan.ID)
}

func TestAutoUpdateEvaluationAndAuditAreAtomic(t *testing.T) {
	database := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Round(0)
	database.now = func() time.Time { return now }
	if err := database.UpsertAutoUpdatePolicy(ctx, model.AutoUpdatePolicyView{
		Service: "demo", ObjectID: "service:demo", TenantID: "default", Channel: "stable",
	}); err != nil {
		t.Fatal(err)
	}
	audit := model.AuditEntry{
		ActorHash: "actor-a", Event: "auto_update.plan.created", Resource: "plan-auto",
		Outcome: "accepted", Detail: map[string]any{"service": "demo", "target": "v1.1.0"},
	}
	next := now.Add(time.Minute)
	installControlFlowTrigger(t, database, "fail_auto_evaluation_audit", `
		BEFORE INSERT ON audit_entries WHEN NEW.event='auto_update.plan.created'
		BEGIN SELECT RAISE(ABORT, 'forced auto update audit failure'); END`)
	if err := database.MarkAutoUpdateEvaluationWithAudit(
		ctx, "demo", &now, &next, "plan-auto", "", audit,
	); err == nil {
		t.Fatal("auto update evaluation survived audit failure")
	}
	policy, err := database.GetAutoUpdatePolicy(ctx, "demo")
	if err != nil || policy.LastEvaluationAt != nil || policy.NextEvaluationAt != nil || policy.LastPlanID != "" {
		t.Fatalf("policy survived audit rollback: %+v err=%v", policy, err)
	}
	dropControlFlowTrigger(t, database, "fail_auto_evaluation_audit")
	if err := database.MarkAutoUpdateEvaluationWithAudit(
		ctx, "demo", &now, &next, "plan-auto", "", audit,
	); err != nil {
		t.Fatal(err)
	}
	policy, err = database.GetAutoUpdatePolicy(ctx, "demo")
	if err != nil || policy.LastPlanID != "plan-auto" || policy.LastEvaluationAt == nil || policy.NextEvaluationAt == nil {
		t.Fatalf("policy=%+v err=%v", policy, err)
	}
	assertControlFlowCount(t, database,
		`SELECT COUNT(*) FROM audit_entries WHERE resource=? AND event='auto_update.plan.created'`, 1, "plan-auto")
}

func atomicReleasePlan(now time.Time, id string) model.ReleasePlan {
	return model.ReleasePlan{
		ID: id, ActorHash: "actor-a", Service: "demo", Action: "update", Target: "v1.2.3",
		TenantID: "tenant-a", ServerID: "server-a", Risk: model.RiskHigh,
		State: model.PlanPendingApproval, Digest: "sha256:" + id,
		ConfirmationPhrase: "更新 demo", RequiresConfirmation: true, RequiresDualApproval: true,
		ApprovalSummary: model.ApprovalSummary{
			SchemaVersion: 1, Service: "demo", Action: "update", Target: "v1.2.3",
			TenantID: "tenant-a", ServerID: "server-a", Risk: model.RiskHigh,
			ConfirmationPhrase: "更新 demo", Steps: []string{"preflight", "update"},
			ExpectedBefore: map[string]any{"currentVersion": "v1.0.0"},
		},
		CreatedAt: now, UpdatedAt: now,
	}
}

func assertTaskStartRolledBack(t *testing.T, database *Store, previewID, taskID string) {
	t.Helper()
	if _, err := database.GetTask(context.Background(), taskID); err != ErrNotFound {
		t.Fatalf("task survived start rollback: %v", err)
	}
	var consumed sql.NullString
	if err := database.db.QueryRow(`SELECT consumed_at FROM previews WHERE id=?`, previewID).Scan(&consumed); err != nil {
		t.Fatal(err)
	}
	if consumed.Valid {
		t.Fatalf("preview was consumed after rollback: %s", consumed.String)
	}
	assertControlFlowCount(t, database, `SELECT COUNT(*) FROM events WHERE task_id=?`, 0, taskID)
	assertControlFlowCount(t, database, `SELECT COUNT(*) FROM audit_entries WHERE resource=?`, 0, taskID)
}

func assertReleasePlanState(t *testing.T, database *Store, id string, state model.PlanState) {
	t.Helper()
	plan, err := database.GetReleasePlan(context.Background(), id)
	if err != nil || plan.State != state {
		t.Fatalf("plan=%+v state=%s err=%v", plan, state, err)
	}
}

func assertControlFlowCount(t *testing.T, database *Store, query string, want int, args ...any) {
	t.Helper()
	var count int
	if err := database.db.QueryRow(query, args...).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Fatalf("count=%d want=%d query=%s", count, want, query)
	}
}

func installControlFlowTrigger(t *testing.T, database *Store, name, body string) {
	t.Helper()
	if _, err := database.db.Exec("CREATE TRIGGER " + name + " " + body); err != nil {
		t.Fatal(err)
	}
}

func dropControlFlowTrigger(t *testing.T, database *Store, name string) {
	t.Helper()
	if _, err := database.db.Exec("DROP TRIGGER " + name); err != nil {
		t.Fatal(err)
	}
}
