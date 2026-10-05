package store

import (
	"context"
	"encoding/json"
	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
	"testing"
)

// 仅构造迁移前已存在的事实；不经被关闭的新执行入口或任何 legacy 执行实现。
func seedHistoricalPreviewTask(t *testing.T, db *Store, ctx context.Context, actor string, request model.StartTaskRequest, id string) (model.Task, bool, error) {
	t.Helper()
	preview, err := db.GetPreview(ctx, request.PreviewID)
	if err != nil {
		return model.Task{}, false, err
	}
	stages := []model.TaskStage{}
	for _, step := range preview.Steps {
		stages = append(stages, model.TaskStage{Name: step, State: model.StagePending})
	}
	snapshot, _ := json.Marshal(preview.Snapshot)
	stageJSON, _ := json.Marshal(stages)
	now := db.now()
	_, err = db.db.Exec(`INSERT INTO tasks(id,idempotency_key,request_hash,actor_hash,service,action,target,risk,state,preview_id,snapshot_json,stages_json,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, request.IdempotencyKey, hashTaskRequest(request.PreviewID, request.Confirmation), actor, preview.Service, preview.Action, preview.Target, preview.Risk, model.TaskQueued, preview.ID, snapshot, stageJSON, timeText(now))
	if err != nil {
		return model.Task{}, false, err
	}
	if _, err = db.db.Exec(`UPDATE previews SET consumed_at=? WHERE id=?`, timeText(now), preview.ID); err != nil {
		return model.Task{}, false, err
	}
	_, err = db.AppendEvent(ctx, model.Event{TaskID: id, Level: "info", Phase: "queued", Message: "历史排队夹具"})
	if err != nil {
		return model.Task{}, false, err
	}
	_, err = db.AppendAudit(ctx, model.AuditEntry{ActorHash: actor, Event: "task.accepted", Resource: id, Outcome: "accepted", Detail: map[string]any{"service": preview.Service, "action": preview.Action}})
	if err != nil {
		return model.Task{}, false, err
	}
	task, err := db.GetTask(ctx, id)
	return task, true, err
}

func seedHistoricalRunning(t *testing.T, db *Store, ctx context.Context, id, phase, owner string) error {
	t.Helper()
	now := timeText(db.now())
	_, err := db.db.ExecContext(ctx, `UPDATE tasks SET state=?,current_phase=?,started_at=?,heartbeat_at=?,runner_owner=? WHERE id=?`, model.TaskRunning, phase, now, now, owner, id)
	return err
}

func seedHistoricalApproval(t *testing.T, db *Store, id, first, second string, state model.PlanState) {
	t.Helper()
	now := timeText(db.now())
	_, err := db.db.Exec(`UPDATE release_plans SET state=?,approved_by_hash=?,second_approved_by_hash=?,approved_at=?,updated_at=? WHERE id=?`, state, first, second, now, now, id)
	if err != nil {
		t.Fatal(err)
	}
	actor := first
	if second != "" {
		actor = second
	}
	if _, err = db.AppendAudit(context.Background(), model.AuditEntry{ActorHash: actor, Event: "plan.approved", Resource: id, Outcome: string(state)}); err != nil {
		t.Fatal(err)
	}
}

func seedHistoricalPlanTask(t *testing.T, db *Store, ctx context.Context, plan model.ReleasePlan, actor, key, id string, silence *model.MaintenanceSilence) (model.Task, bool, error) {
	t.Helper()
	stages := []model.TaskStage{}
	for _, step := range plan.ApprovalSummary.Steps {
		stages = append(stages, model.TaskStage{Name: step, State: model.StagePending})
	}
	snapshot, _ := json.Marshal(plan.ApprovalSummary.ExpectedBefore)
	stageJSON, _ := json.Marshal(stages)
	now := db.now()
	_, err := db.db.Exec(`INSERT INTO tasks(id,idempotency_key,request_hash,actor_hash,service,action,target,risk,state,preview_id,plan_id,plan_digest,snapshot_json,stages_json,created_at) VALUES(?,?,?,?,?,?,?,?,?,'',?,?,?,?,?)`,
		id, key, HashConfirmation(plan.ID+"\x00"+plan.Digest), actor, plan.Service, plan.Action, plan.Target, plan.Risk, model.TaskQueued, plan.ID, plan.Digest, snapshot, stageJSON, timeText(now))
	if err != nil {
		return model.Task{}, false, err
	}
	silenceID := ""
	var ends any
	if silence != nil {
		silenceID = silence.ID
		ends = timeText(silence.EndsAt)
	}
	_, err = db.db.Exec(`UPDATE release_plans SET state=?,task_id=?,executed_by_hash=?,maintenance_silence_id=?,maintenance_silence_ends_at=?,updated_at=? WHERE id=?`,
		model.PlanExecuting, id, actor, silenceID, ends, timeText(now), plan.ID)
	if err != nil {
		return model.Task{}, false, err
	}
	if silence != nil {
		_, err = db.AppendAudit(ctx, model.AuditEntry{ActorHash: actor, Event: "plan.maintenance_silence_created", Resource: plan.ID, Outcome: "created"})
		if err != nil {
			return model.Task{}, false, err
		}
	}
	if _, err = db.AppendEvent(ctx, model.Event{TaskID: id, Level: "info", Phase: "queued", Message: "历史排队夹具"}); err != nil {
		return model.Task{}, false, err
	}
	if _, err = db.AppendAudit(ctx, model.AuditEntry{ActorHash: actor, Event: "task.accepted", Resource: id, Outcome: "accepted"}); err != nil {
		return model.Task{}, false, err
	}
	task, err := db.GetTask(ctx, id)
	return task, true, err
}

func seedHistoricalPlanClosed(t *testing.T, db *Store, id, key, actor string) {
	t.Helper()
	_, err := db.db.Exec(`UPDATE release_plans SET state=?,closure_idempotency_key=?,closed_at=? WHERE id=?`, model.PlanCompleted, key, timeText(db.now()), id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.AppendAudit(context.Background(), model.AuditEntry{ActorHash: actor, Event: "plan.closed", Resource: id, Outcome: "completed"}); err != nil {
		t.Fatal(err)
	}
}
