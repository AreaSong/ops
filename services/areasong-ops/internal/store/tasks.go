package store

import (
	"context"
	"database/sql"
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
	           current_phase, summary, error, preview_id, snapshot_json, created_at,
           started_at, finished_at
    FROM tasks`

type scanner interface {
	Scan(...any) error
}

func scanTask(row scanner) (model.Task, error) {
	var task model.Task
	var risk, state, snapshotJSON, createdAt string
	var startedAt, finishedAt sql.NullString
	err := row.Scan(&task.ID, &task.IdempotencyKey, &task.RequestHash, &task.ActorHash, &task.Service,
		&task.Action, &task.Target, &risk, &state, &task.CurrentPhase, &task.Summary,
		&task.Error, &task.PreviewID, &snapshotJSON, &createdAt, &startedAt, &finishedAt)
	if err != nil {
		return model.Task{}, err
	}
	task.Risk = model.Risk(risk)
	task.State = model.TaskState(state)
	if err := decodeJSON(snapshotJSON, &task.Snapshot); err != nil {
		return model.Task{}, err
	}
	task.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return model.Task{}, err
	}
	if task.StartedAt, err = nullableTime(startedAt); err != nil {
		return model.Task{}, err
	}
	task.FinishedAt, err = nullableTime(finishedAt)
	return task, err
}

func (store *Store) GetTask(ctx context.Context, id string) (model.Task, error) {
	task, err := scanTask(store.db.QueryRowContext(ctx, taskSelect+` WHERE id = ?`, id))
	if err == sql.ErrNoRows {
		return model.Task{}, ErrNotFound
	}
	return task, err
}

func (store *Store) ListTasks(ctx context.Context, limit int) ([]model.Task, error) {
	rows, err := store.db.QueryContext(ctx, taskSelect+` ORDER BY created_at DESC LIMIT ?`, clampLimit(limit, 200))
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

func (store *Store) ActiveTask(ctx context.Context, service string) (model.Task, bool, error) {
	row := store.db.QueryRowContext(ctx, taskSelect+`
        WHERE service = ? AND state IN (?, ?) ORDER BY created_at DESC LIMIT 1`,
		service, model.TaskQueued, model.TaskRunning)
	task, err := scanTask(row)
	if err == sql.ErrNoRows {
		return model.Task{}, false, nil
	}
	return task, err == nil, err
}

func (store *Store) MarkRunning(ctx context.Context, id, phase string) error {
	result, err := store.db.ExecContext(ctx, `
        UPDATE tasks SET state = ?, current_phase = ?, started_at = ?
        WHERE id = ? AND state = ?
    `, model.TaskRunning, phase, timeText(store.now()), id, model.TaskQueued)
	return requireOne(result, err, "任务无法进入运行状态")
}

func (store *Store) SetPhase(ctx context.Context, id, phase, summary string) error {
	result, err := store.db.ExecContext(ctx, `
        UPDATE tasks SET current_phase = ?, summary = ? WHERE id = ? AND state = ?
    `, phase, summary, id, model.TaskRunning)
	return requireOne(result, err, "任务阶段更新失败")
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
        UPDATE tasks SET state = ?, summary = ?, error = ?, finished_at = ?
        WHERE id = ? AND state IN (?, ?)
    `, state, summary, errorMessage, timeText(store.now()), id,
		model.TaskQueued, model.TaskRunning)
	return requireOne(result, err, "任务无法写入终态")
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
