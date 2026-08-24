package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

func TestCompleteTaskWithDesiredCommitsLifecycleAndDesiredStateTogether(t *testing.T) {
	ctx := context.Background()
	database := openTestStore(t)
	now := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
	database.now = func() time.Time { return now }
	preview := testPreview(now)
	if err := database.CreatePreview(ctx, PreviewInput{Preview: preview, ConfirmationHash: HashConfirmation("重启 demo")}); err != nil {
		t.Fatal(err)
	}
	task, _, err := database.StartTask(ctx, "a", model.StartTaskRequest{
		PreviewID: preview.ID, Confirmation: "重启 demo", IdempotencyKey: "atomic-success",
	}, "task-atomic-success")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.MarkRunningOwned(ctx, task.ID, "restart", "runner"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.SetDesiredState(ctx, DesiredStateInput{
		Service: "demo", ObjectID: "demo-object", Desired: model.DesiredRunning, ActorHash: "a",
	}); err != nil {
		t.Fatal(err)
	}
	maintenanceUntil := now.Add(15 * time.Minute)
	desired := &DesiredStateInput{
		Service: "demo", ObjectID: "demo-object", Desired: model.DesiredMaintenance,
		Reason: "生命周期动作完成", ActorHash: "a", TenantID: "ops",
		MaintenanceUntil: &maintenanceUntil,
	}
	event, err := database.CompleteTaskWithDesired(ctx, task.ID, model.TaskSucceeded, "完成", "", "",
		false, false, "", model.Event{TaskID: task.ID, Level: "info", Phase: "terminal", Message: "succeeded"},
		model.AuditEntry{ActorHash: "a", Event: "task.terminal", Resource: task.ID, Outcome: "succeeded"}, desired)
	if err != nil || event.Sequence == 0 {
		t.Fatalf("event=%+v err=%v", event, err)
	}
	finished, err := database.GetTask(ctx, task.ID)
	if err != nil || finished.State != model.TaskSucceeded {
		t.Fatalf("task=%+v err=%v", finished, err)
	}
	state, found, err := database.GetServiceState(ctx, "demo")
	if err != nil || !found || state.Generation != 2 || state.TenantID != "ops" || state.Desired != model.DesiredMaintenance {
		t.Fatalf("state=%+v found=%v err=%v", state, found, err)
	}
	if state.MaintenanceUntil == nil || !state.MaintenanceUntil.Equal(maintenanceUntil) {
		t.Fatalf("maintenanceUntil=%v want=%v", state.MaintenanceUntil, maintenanceUntil)
	}
	var desiredEvents, taskEvents, audits int
	if err := database.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM control_plane_events WHERE event_type='desired_state.changed'`).Scan(&desiredEvents); err != nil {
		t.Fatal(err)
	}
	if err := database.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM events WHERE task_id=?`, task.ID).Scan(&taskEvents); err != nil {
		t.Fatal(err)
	}
	if err := database.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_entries WHERE resource=?`, task.ID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if desiredEvents != 2 || taskEvents != 1 || audits != 1 {
		t.Fatalf("desiredEvents=%d taskEvents=%d audits=%d", desiredEvents, taskEvents, audits)
	}
}

func TestCompleteTaskWithDesiredOnlyWritesDesiredForSucceeded(t *testing.T) {
	ctx := context.Background()
	database := openTestStore(t)
	now := time.Now().UTC()
	database.now = func() time.Time { return now }
	preview := testPreview(now)
	if err := database.CreatePreview(ctx, PreviewInput{Preview: preview, ConfirmationHash: HashConfirmation("重启 demo")}); err != nil {
		t.Fatal(err)
	}
	task, _, err := database.StartTask(ctx, "a", model.StartTaskRequest{
		PreviewID: preview.ID, Confirmation: "重启 demo", IdempotencyKey: "atomic-failed",
	}, "task-atomic-failed")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.MarkRunningOwned(ctx, task.ID, "restart", "runner"); err != nil {
		t.Fatal(err)
	}
	_, err = database.CompleteTaskWithDesired(ctx, task.ID, model.TaskFailedRecoverable, "失败", "错误", "failed",
		true, false, "", model.Event{TaskID: task.ID, Level: "error", Phase: "terminal", Message: "failed"},
		model.AuditEntry{ActorHash: "a", Event: "task.terminal", Resource: task.ID, Outcome: "failed"},
		&DesiredStateInput{Service: "demo", ObjectID: "demo-object", Desired: model.DesiredRunning})
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := database.GetServiceState(ctx, "demo"); err != nil || found {
		t.Fatalf("desired state found=%v err=%v", found, err)
	}
	var events int
	if err := database.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM control_plane_events`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 0 {
		t.Fatalf("control-plane events=%d want 0", events)
	}
}

func TestCompleteTaskWithDesiredRollsBackOnDesiredStateError(t *testing.T) {
	ctx := context.Background()
	database := openTestStore(t)
	now := time.Now().UTC()
	database.now = func() time.Time { return now }
	preview := testPreview(now)
	if err := database.CreatePreview(ctx, PreviewInput{Preview: preview, ConfirmationHash: HashConfirmation("重启 demo")}); err != nil {
		t.Fatal(err)
	}
	task, _, err := database.StartTask(ctx, "a", model.StartTaskRequest{
		PreviewID: preview.ID, Confirmation: "重启 demo", IdempotencyKey: "atomic-rollback",
	}, "task-atomic-rollback")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.MarkRunningOwned(ctx, task.ID, "restart", "runner"); err != nil {
		t.Fatal(err)
	}
	_, err = database.CompleteTaskWithDesired(ctx, task.ID, model.TaskSucceeded, "完成", "", "",
		false, false, "", model.Event{TaskID: task.ID, Level: "info", Phase: "terminal", Message: "succeeded"},
		model.AuditEntry{ActorHash: "a", Event: "task.terminal", Resource: task.ID, Outcome: "succeeded"},
		&DesiredStateInput{Service: "demo", ObjectID: "demo-object"})
	if err == nil {
		t.Fatal("invalid desired state unexpectedly committed")
	}
	unchanged, err := database.GetTask(ctx, task.ID)
	if err != nil || unchanged.State != model.TaskRunning {
		t.Fatalf("task=%+v err=%v", unchanged, err)
	}
	var taskEvents, audits int
	if err := database.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM events WHERE task_id=?`, task.ID).Scan(&taskEvents); err != nil {
		t.Fatal(err)
	}
	if err := database.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_entries WHERE resource=?`, task.ID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if taskEvents != 0 || audits != 0 {
		t.Fatalf("rollback left taskEvents=%d audits=%d", taskEvents, audits)
	}
}

func TestCompleteTaskWithDesiredRollsBackAfterDesiredWriteFailure(t *testing.T) {
	ctx := context.Background()
	database := openTestStore(t)
	now := time.Now().UTC()
	database.now = func() time.Time { return now }
	preview := testPreview(now)
	if err := database.CreatePreview(ctx, PreviewInput{Preview: preview, ConfirmationHash: HashConfirmation("重启 demo")}); err != nil {
		t.Fatal(err)
	}
	task, _, err := database.StartTask(ctx, "a", model.StartTaskRequest{
		PreviewID: preview.ID, Confirmation: "重启 demo", IdempotencyKey: "atomic-event-encode",
	}, "task-atomic-event-encode")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.MarkRunningOwned(ctx, task.ID, "restart", "runner"); err != nil {
		t.Fatal(err)
	}
	_, err = database.CompleteTaskWithDesired(ctx, task.ID, model.TaskSucceeded, "完成", "", "",
		false, false, "", model.Event{TaskID: task.ID, Level: "info", Phase: "terminal", Message: "succeeded",
			Data: map[string]any{"unsupported": func() {}}},
		model.AuditEntry{ActorHash: "a", Event: "task.terminal", Resource: task.ID, Outcome: "succeeded"},
		&DesiredStateInput{Service: "demo", ObjectID: "demo-object", Desired: model.DesiredRunning})
	if err == nil {
		t.Fatal("unsupported terminal event unexpectedly committed")
	}
	unchanged, err := database.GetTask(ctx, task.ID)
	if err != nil || unchanged.State != model.TaskRunning {
		t.Fatalf("task=%+v err=%v", unchanged, err)
	}
	if _, found, err := database.GetServiceState(ctx, "demo"); err != nil || found {
		t.Fatalf("desired state found=%v err=%v", found, err)
	}
}

func TestCompleteTaskAssignmentWithDesiredUsesControlPlaneInput(t *testing.T) {
	ctx := context.Background()
	database := openTestStore(t)
	now := time.Now().UTC()
	database.now = func() time.Time { return now }
	preview := testPreview(now)
	if err := database.CreatePreview(ctx, PreviewInput{Preview: preview, ConfirmationHash: HashConfirmation("重启 demo")}); err != nil {
		t.Fatal(err)
	}
	task, _, err := database.StartTask(ctx, "a", model.StartTaskRequest{
		PreviewID: preview.ID, Confirmation: "重启 demo", IdempotencyKey: "atomic-assignment",
	}, "task-atomic-assignment")
	if err != nil {
		t.Fatal(err)
	}
	const runnerID = "runner-atomic"
	const claimToken = "claim-token-atomic"
	owner := assignmentOwner(runnerID, 1)
	if err := database.MarkRunningOwned(ctx, task.ID, "restart", owner); err != nil {
		t.Fatal(err)
	}
	contractJSON, err := json.Marshal(model.NewTaskDispatch(task))
	if err != nil {
		t.Fatal(err)
	}
	deadline := now.Add(time.Minute)
	_, err = database.db.ExecContext(ctx, `
		INSERT INTO task_assignments(task_id,server_id,runner_id,generation,state,contract_json,contract_digest,claim_token_hash,
			claimed_at,last_heartbeat_at,lease_expires_at,execution_deadline_at,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, task.ID, "server-atomic", runnerID, 1, model.AssignmentClaimed,
		contractJSON, "sha256:contract", assignmentTokenHash(claimToken), timeText(now), timeText(now),
		timeText(deadline), timeText(deadline), timeText(now), timeText(now))
	if err != nil {
		t.Fatal(err)
	}
	desired := &DesiredStateInput{Service: "demo", ObjectID: "control-plane-object", Desired: model.DesiredDrained}
	input := model.AssignmentCompletionRequest{
		AssignmentFence: model.AssignmentFence{RunnerID: runnerID, Generation: 1, ClaimToken: claimToken},
		IdempotencyKey:  "completion-atomic", State: model.TaskSucceeded, Summary: "runner said success",
	}
	completed, assignment, sequence, err := database.CompleteTaskAssignmentWithDesired(ctx, runnerID, task.ID, input, desired)
	if err != nil || sequence == 0 || completed.State != model.TaskSucceeded || assignment.State != model.AssignmentCompleted {
		t.Fatalf("task=%+v assignment=%+v sequence=%d err=%v", completed, assignment, sequence, err)
	}
	state, found, err := database.GetServiceState(ctx, "demo")
	if err != nil || !found || state.Desired != model.DesiredDrained || state.ObjectID != "control-plane-object" || state.TenantID != "default" {
		t.Fatalf("state=%+v found=%v err=%v", state, found, err)
	}
}

func TestCompleteTaskAssignmentWithDesiredRollsBackOnDesiredStateError(t *testing.T) {
	ctx := context.Background()
	database := openTestStore(t)
	now := time.Now().UTC()
	database.now = func() time.Time { return now }
	preview := testPreview(now)
	if err := database.CreatePreview(ctx, PreviewInput{Preview: preview, ConfirmationHash: HashConfirmation("重启 demo")}); err != nil {
		t.Fatal(err)
	}
	task, _, err := database.StartTask(ctx, "a", model.StartTaskRequest{
		PreviewID: preview.ID, Confirmation: "重启 demo", IdempotencyKey: "atomic-assignment-rollback",
	}, "task-atomic-assignment-rollback")
	if err != nil {
		t.Fatal(err)
	}
	const runnerID = "runner-atomic-rollback"
	const claimToken = "claim-token-atomic-rollback"
	if err := database.MarkRunningOwned(ctx, task.ID, "restart", assignmentOwner(runnerID, 1)); err != nil {
		t.Fatal(err)
	}
	contractJSON, err := json.Marshal(model.NewTaskDispatch(task))
	if err != nil {
		t.Fatal(err)
	}
	deadline := now.Add(time.Minute)
	_, err = database.db.ExecContext(ctx, `
		INSERT INTO task_assignments(task_id,server_id,runner_id,generation,state,contract_json,contract_digest,claim_token_hash,
			claimed_at,last_heartbeat_at,lease_expires_at,execution_deadline_at,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, task.ID, "server-atomic", runnerID, 1, model.AssignmentClaimed,
		contractJSON, "sha256:contract", assignmentTokenHash(claimToken), timeText(now), timeText(now),
		timeText(deadline), timeText(deadline), timeText(now), timeText(now))
	if err != nil {
		t.Fatal(err)
	}
	input := model.AssignmentCompletionRequest{
		AssignmentFence: model.AssignmentFence{RunnerID: runnerID, Generation: 1, ClaimToken: claimToken},
		IdempotencyKey:  "completion-atomic-rollback", State: model.TaskSucceeded,
	}
	_, _, _, err = database.CompleteTaskAssignmentWithDesired(ctx, runnerID, task.ID, input,
		&DesiredStateInput{Service: "demo", ObjectID: ""})
	if err == nil {
		t.Fatal("invalid desired state unexpectedly committed")
	}
	unchanged, err := database.GetTask(ctx, task.ID)
	if err != nil || unchanged.State != model.TaskRunning {
		t.Fatalf("task=%+v err=%v", unchanged, err)
	}
	var assignmentState string
	if err := database.db.QueryRowContext(ctx, `SELECT state FROM task_assignments WHERE task_id=?`, task.ID).Scan(&assignmentState); err != nil {
		t.Fatal(err)
	}
	if assignmentState != string(model.AssignmentClaimed) {
		t.Fatalf("assignment state=%s want claimed", assignmentState)
	}
}
