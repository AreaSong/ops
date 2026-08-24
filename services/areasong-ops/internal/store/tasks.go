package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

type queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func taskByIdempotency(ctx context.Context, db queryer, key string) (model.Task, bool, error) {
	row := db.QueryRowContext(ctx, taskSelect+` WHERE idempotency_key = ?`, key)
	task, err := scanTask(row)
	if err == sql.ErrNoRows {
		return model.Task{}, false, nil
	}
	return task, err == nil, err
}

const taskSelect = `
	    SELECT id, idempotency_key, request_hash, actor_hash, service, action, target, risk, state,
	           current_phase, summary, error, preview_id, plan_id, plan_digest, parent_task_id,
	           snapshot_json, stages_json, runner_owner, heartbeat_at, production_changed,
	           retryable, failure_code, rollback_available, rollback_reason, recovery_point_id,
	           restore_mode, restore_tenant_id, restore_server_id, restore_expected_before_digest,
	           restore_contract_digest, restore_revalidated_at, restore_outcome, restore_evidence_digest,
	           created_at, started_at, finished_at
    FROM tasks`

type scanner interface {
	Scan(...any) error
}

func scanTask(row scanner) (model.Task, error) {
	var task model.Task
	var risk, state, snapshotJSON, stagesJSON, createdAt string
	var heartbeatAt, restoreRevalidatedAt, startedAt, finishedAt sql.NullString
	err := row.Scan(&task.ID, &task.IdempotencyKey, &task.RequestHash, &task.ActorHash, &task.Service,
		&task.Action, &task.Target, &risk, &state, &task.CurrentPhase, &task.Summary,
		&task.Error, &task.PreviewID, &task.PlanID, &task.PlanDigest, &task.ParentTaskID,
		&snapshotJSON, &stagesJSON, &task.RunnerOwner, &heartbeatAt, &task.ProductionChanged,
		&task.Retryable, &task.FailureCode, &task.RollbackAvailable, &task.RollbackReason,
		&task.RecoveryPointID, &task.RestoreMode, &task.RestoreTenantID, &task.RestoreServerID,
		&task.RestoreExpectedBeforeDigest, &task.RestoreContractDigest, &restoreRevalidatedAt,
		&task.RestoreOutcome, &task.RestoreEvidenceDigest, &createdAt, &startedAt, &finishedAt)
	if err != nil {
		return model.Task{}, err
	}
	task.Risk = model.Risk(risk)
	task.State = model.TaskState(state)
	if err := decodeJSON(snapshotJSON, &task.Snapshot); err != nil {
		return model.Task{}, err
	}
	if task.Snapshot != nil {
		task.TrafficPolicyDigest, _ = task.Snapshot["trafficPolicyDigest"].(string)
	}
	if err := decodeJSON(stagesJSON, &task.Stages); err != nil {
		return model.Task{}, err
	}
	task.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return model.Task{}, err
	}
	if task.StartedAt, err = nullableTime(startedAt); err != nil {
		return model.Task{}, err
	}
	if task.HeartbeatAt, err = nullableTime(heartbeatAt); err != nil {
		return model.Task{}, err
	}
	if task.RestoreRevalidatedAt, err = nullableTime(restoreRevalidatedAt); err != nil {
		return model.Task{}, err
	}
	task.FinishedAt, err = nullableTime(finishedAt)
	task.RecoveryActions = recoveryActions(task)
	return task, err
}

func (store *Store) GetTask(ctx context.Context, id string) (model.Task, error) {
	task, err := scanTask(store.db.QueryRowContext(ctx, taskSelect+` WHERE id = ?`, id))
	if err == sql.ErrNoRows {
		return model.Task{}, ErrNotFound
	}
	return task, err
}

func (store *Store) ListTasks(ctx context.Context, limit, offset int) ([]model.Task, error) {
	rows, err := store.db.QueryContext(ctx, taskSelect+` ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`,
		clampLimit(limit, 201), nonNegative(offset))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tasks := make([]model.Task, 0)
	for rows.Next() {
		task, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	return tasks, rows.Err()
}

func (store *Store) LatestSucceededUpdate(ctx context.Context, service string) (model.Task, bool, error) {
	task, err := scanTask(store.db.QueryRowContext(ctx, taskSelect+`
		WHERE service = ? AND action = 'update' AND state = ?
		ORDER BY created_at DESC, id DESC LIMIT 1`, service, model.TaskSucceeded))
	if err == sql.ErrNoRows {
		return model.Task{}, false, nil
	}
	return task, err == nil, err
}

// LatestSucceededRestoreDrill returns real isolated-restore evidence for the
// exact recovery-point artifact digest currently shown to the operator. A
// verified backup alone must never be presented as a successful restore drill.
func (store *Store) LatestSucceededRestoreDrill(
	ctx context.Context, service, evidenceDigest string,
) (model.Task, bool, error) {
	if service == "" || evidenceDigest == "" {
		return model.Task{}, false, nil
	}
	task, err := scanTask(store.db.QueryRowContext(ctx, taskSelect+`
		WHERE service = ? AND action = 'restore-drill' AND restore_mode = 'isolated'
		  AND restore_evidence_digest = ? AND state = ?
		ORDER BY finished_at DESC, id DESC LIMIT 1`, service, evidenceDigest, model.TaskSucceeded))
	if err == sql.ErrNoRows {
		return model.Task{}, false, nil
	}
	return task, err == nil, err
}

func (store *Store) ActiveTask(ctx context.Context, service string) (model.Task, bool, error) {
	row := store.db.QueryRowContext(ctx, taskSelect+`
	    WHERE service = ? AND state IN (?, ?, ?, ?) ORDER BY created_at DESC LIMIT 1`,
		service, model.TaskWaitingConfirmation, model.TaskQueued, model.TaskRunning, model.TaskRollingBack)
	task, err := scanTask(row)
	if err == sql.ErrNoRows {
		return model.Task{}, false, nil
	}
	return task, err == nil, err
}

func (store *Store) MarkRunning(ctx context.Context, id, phase string) error {
	return store.MarkRunningOwned(ctx, id, phase, "")
}

func (store *Store) MarkRunningOwned(ctx context.Context, id, phase, owner string) error {
	now := timeText(store.now())
	result, err := store.db.ExecContext(ctx, `
	    UPDATE tasks SET state = ?, current_phase = ?, started_at = ?, heartbeat_at = ?, runner_owner = ?
	    WHERE id = ? AND state = ?
	`, model.TaskRunning, phase, now, now, owner, id, model.TaskQueued)
	return requireOne(result, err, "任务无法进入运行状态")
}

func (store *Store) SetPhase(ctx context.Context, id, phase, summary string) error {
	task, err := store.GetTask(ctx, id)
	if err != nil {
		return err
	}
	now := store.now()
	for index := range task.Stages {
		if task.Stages[index].State == model.StageRunning && task.Stages[index].Name != phase {
			task.Stages[index].State = model.StageSucceeded
			task.Stages[index].FinishedAt = &now
		}
		if task.Stages[index].Name == phase && task.Stages[index].State == model.StagePending {
			task.Stages[index].State = model.StageRunning
			task.Stages[index].StartedAt = &now
		}
	}
	stages, err := encodeJSON(task.Stages)
	if err != nil {
		return err
	}
	result, err := store.db.ExecContext(ctx, `
	    UPDATE tasks SET current_phase = ?, summary = ?, stages_json = ?, heartbeat_at = ?
	    WHERE id = ? AND state IN (?, ?)
	`, phase, summary, stages, timeText(now), id, model.TaskRunning, model.TaskRollingBack)
	return requireOne(result, err, "任务阶段更新失败")
}

func (store *Store) Heartbeat(ctx context.Context, id, owner string) error {
	result, err := store.db.ExecContext(ctx, `
		UPDATE tasks SET heartbeat_at = ? WHERE id = ? AND runner_owner = ? AND state IN (?, ?)
	`, timeText(store.now()), id, owner, model.TaskRunning, model.TaskRollingBack)
	return requireOne(result, err, "任务心跳更新失败")
}

func (store *Store) MarkProductionChanged(ctx context.Context, id string, rollbackAvailable bool, reason string) error {
	result, err := store.db.ExecContext(ctx, `
		UPDATE tasks SET production_changed = 1, rollback_available = ?, rollback_reason = ?
		WHERE id = ? AND state = ?
	`, rollbackAvailable, reason, id, model.TaskRunning)
	return requireOne(result, err, "生产变更事实写入失败")
}

func (store *Store) TransitionTask(ctx context.Context, id string, from, to model.TaskState) error {
	if !from.CanTransition(to) {
		return fmt.Errorf("任务状态迁移无效: %s -> %s", from, to)
	}
	result, err := store.db.ExecContext(ctx, `UPDATE tasks SET state = ? WHERE id = ? AND state = ?`, to, id, from)
	return requireOne(result, err, "任务状态已变化")
}

func (store *Store) StartRecovery(ctx context.Context, id, phase, failureSummary string) error {
	task, err := store.GetTask(ctx, id)
	if err != nil {
		return err
	}
	if task.State != model.TaskRunning {
		return fmt.Errorf("任务无法进入回滚状态")
	}
	now := store.now()
	for index := range task.Stages {
		if task.Stages[index].State == model.StageRunning {
			task.Stages[index].State = model.StageFailed
			task.Stages[index].Summary = failureSummary
			task.Stages[index].FinishedAt = &now
		}
	}
	task.Stages = append(task.Stages, model.TaskStage{
		Name: phase, State: model.StageRunning, StartedAt: &now,
	})
	stagesJSON, err := encodeJSON(task.Stages)
	if err != nil {
		return err
	}
	result, err := store.db.ExecContext(ctx, `
		UPDATE tasks SET state = ?, current_phase = ?, stages_json = ?, heartbeat_at = ?
		WHERE id = ? AND state = ?
	`, model.TaskRollingBack, phase, stagesJSON, timeText(now), id, model.TaskRunning)
	return requireOne(result, err, "任务无法进入回滚状态")
}

func (store *Store) FinishTask(
	ctx context.Context,
	id string,
	state model.TaskState,
	summary, errorMessage string,
) error {
	if !state.Terminal() {
		return fmt.Errorf("任务终态无效: %s", state)
	}
	result, err := store.db.ExecContext(ctx, `
	    UPDATE tasks SET state = ?, summary = ?, error = ?, finished_at = ?, heartbeat_at = NULL
	    WHERE id = ? AND state IN (?, ?, ?, ?)
	`, state, summary, errorMessage, timeText(store.now()), id,
		model.TaskWaitingConfirmation, model.TaskQueued, model.TaskRunning, model.TaskRollingBack)
	return requireOne(result, err, "任务无法写入终态")
}

func (store *Store) CompleteTask(
	ctx context.Context,
	id string,
	state model.TaskState,
	summary, errorMessage, failureCode string,
	retryable, rollbackAvailable bool,
	rollbackReason string,
	event model.Event,
	audit model.AuditEntry,
) (model.Event, error) {
	return store.CompleteTaskWithDesired(ctx, id, state, summary, errorMessage, failureCode,
		retryable, rollbackAvailable, rollbackReason, event, audit, nil)
}

// CompleteTaskWithDesired atomically completes a task and, for a successful
// lifecycle terminal state, optionally persists the control-plane desired
// state and its changed event in the same transaction.
func (store *Store) CompleteTaskWithDesired(
	ctx context.Context,
	id string,
	state model.TaskState,
	summary, errorMessage, failureCode string,
	retryable, rollbackAvailable bool,
	rollbackReason string,
	event model.Event,
	audit model.AuditEntry,
	desired *DesiredStateInput,
) (model.Event, error) {
	if !state.Terminal() {
		return model.Event{}, fmt.Errorf("任务终态无效: %s", state)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return model.Event{}, err
	}
	defer tx.Rollback()
	task, err := scanTask(tx.QueryRowContext(ctx, taskSelect+` WHERE id = ?`, id))
	if err != nil {
		return model.Event{}, err
	}
	if !task.State.Active() {
		return model.Event{}, fmt.Errorf("任务无法写入终态")
	}
	now := store.now()
	for index := range task.Stages {
		if task.Stages[index].State != model.StageRunning {
			continue
		}
		task.Stages[index].FinishedAt = &now
		if state == model.TaskSucceeded {
			task.Stages[index].State = model.StageSucceeded
		} else if state == model.TaskRolledBack {
			task.Stages[index].State = model.StageRolledBack
		} else {
			task.Stages[index].State = model.StageFailed
		}
		task.Stages[index].Summary = summary
	}
	stagesJSON, err := encodeJSON(task.Stages)
	if err != nil {
		return model.Event{}, err
	}
	restoreOutcome := task.RestoreOutcome
	restoreEvidenceDigest := task.RestoreEvidenceDigest
	if task.RestoreMode != "" {
		restoreOutcome = string(state)
		// The selected recovery-point evidence remains the immutable evidence
		// reference for the outcome; task/plan state records whether applying it
		// succeeded, failed, or became uncertain.
		if restoreEvidenceDigest == "" {
			return model.Event{}, errors.New("恢复任务缺少恢复点证据摘要")
		}
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE tasks SET state = ?, summary = ?, error = ?, failure_code = ?, retryable = ?,
			rollback_available = ?, rollback_reason = ?, stages_json = ?, finished_at = ?, heartbeat_at = NULL,
			restore_outcome = ?, restore_evidence_digest = ?
		WHERE id = ? AND state = ?
	`, state, summary, errorMessage, failureCode, retryable, rollbackAvailable,
		rollbackReason, stagesJSON, timeText(now), restoreOutcome, restoreEvidenceDigest, id, task.State)
	if err = requireOne(result, err, "任务无法写入终态"); err != nil {
		return model.Event{}, err
	}
	if task.PlanID != "" {
		var observationSeconds int
		if err := tx.QueryRowContext(ctx, `
			SELECT observation_seconds FROM release_plans WHERE id = ? AND task_id = ?
		`, task.PlanID, task.ID).Scan(&observationSeconds); err != nil {
			return model.Event{}, err
		}
		planState := model.PlanNeedsAttention
		closureReason := planClosureReason(state, errorMessage)
		var observationStartedAt, observationEndsAt, closedAt any
		if state == model.TaskSucceeded && observationSeconds > 0 {
			planState = model.PlanObserving
			closureReason = ""
			observationStartedAt = timeText(now)
			observationEndsAt = timeText(now.Add(time.Duration(observationSeconds) * time.Second))
		} else if state == model.TaskSucceeded {
			planState = model.PlanCompleted
			closureReason = ""
			closedAt = timeText(now)
		}
		result, err := tx.ExecContext(ctx, `
			UPDATE release_plans SET state = ?, observation_started_at = ?, observation_ends_at = ?,
				closure_reason = ?, closed_at = ?, restore_outcome = ?, restore_evidence_digest = ?, updated_at = ?
			WHERE id = ? AND task_id = ? AND state = ?
		`, planState, observationStartedAt, observationEndsAt, closureReason, closedAt,
			restoreOutcome, restoreEvidenceDigest, timeText(now), task.PlanID, task.ID, model.PlanExecuting)
		if err = requireOne(result, err, "任务终态无法更新计划状态"); err != nil {
			return model.Event{}, err
		}
		if task.RestoreMode != "" {
			result, err := tx.ExecContext(ctx, `
				UPDATE recovery_points
				SET restore_outcome = ?, restore_evidence_digest = ?, outcome_at = ?
				WHERE id = ? AND service = ? AND evidence_digest = ?
			`, restoreOutcome, restoreEvidenceDigest, timeText(now), task.RecoveryPointID,
				task.Service, task.RestoreEvidenceDigest)
			if err = requireOne(result, err, "恢复点结果无法写入"); err != nil {
				return model.Event{}, err
			}
		}
	}
	if state == model.TaskSucceeded && desired != nil {
		if _, _, _, err := store.setDesiredStateTx(ctx, tx, *desired, now); err != nil {
			return model.Event{}, err
		}
	}
	if event.OccurredAt.IsZero() {
		event.OccurredAt = now
	}
	dataJSON, err := encodeJSON(event.Data)
	if err != nil {
		return model.Event{}, err
	}
	result, err = tx.ExecContext(ctx, `
		INSERT INTO events (task_id, occurred_at, level, phase, message, data_json)
		VALUES (?, ?, ?, ?, ?, ?)
	`, event.TaskID, timeText(event.OccurredAt), event.Level, event.Phase, event.Message, dataJSON)
	if err != nil {
		return model.Event{}, err
	}
	event.Sequence, err = result.LastInsertId()
	if err != nil {
		return model.Event{}, err
	}
	if audit.OccurredAt.IsZero() {
		audit.OccurredAt = now
	}
	detailJSON, err := encodeJSON(audit.Detail)
	if err != nil {
		return model.Event{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO audit_entries (occurred_at, actor_hash, event, resource, outcome, detail_json)
		VALUES (?, ?, ?, ?, ?, ?)
	`, timeText(audit.OccurredAt), audit.ActorHash, audit.Event, audit.Resource,
		audit.Outcome, detailJSON); err != nil {
		return model.Event{}, err
	}
	return event, tx.Commit()
}

func planClosureReason(state model.TaskState, errorMessage string) string {
	if state == model.TaskRolledBack {
		return "执行失败后已回滚，计划需要人工核对"
	}
	if errorMessage != "" {
		return errorMessage
	}
	return "执行未成功，计划需要人工处理"
}

func recoveryActions(task model.Task) []model.RecoveryAction {
	actions := []model.RecoveryAction{{Name: "inspect", Label: "执行前检查", Enabled: task.State.Terminal()}}
	if task.State == model.TaskFailedRecoverable {
		actions = append(actions, model.RecoveryAction{Name: "retry", Label: "重新执行", Enabled: task.Retryable})
	}
	if task.RollbackAvailable {
		actions = append(actions, model.RecoveryAction{Name: "rollback", Label: "受控回滚", Enabled: true})
	}
	if task.State == model.TaskNeedsAttention || task.State == model.TaskRecoveryUncertain {
		actions = append(actions, model.RecoveryAction{
			Name: "reconcile", Label: "人工核对", Enabled: false,
			Reason: "必须按运行手册核对生产身份、Compose、迁移与恢复点后处置",
		})
	}
	return actions
}

func requireOne(result sql.Result, err error, message string) error {
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return fmt.Errorf("%s", message)
	}
	return nil
}
