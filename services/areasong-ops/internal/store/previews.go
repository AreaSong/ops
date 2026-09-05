package store

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

type PreviewInput struct {
	Preview          model.Preview
	ConfirmationHash string
}

type TaskStartResult struct {
	Task        model.Task
	QueuedEvent model.Event
	Created     bool
}

func (store *Store) GetPreview(ctx context.Context, id string) (model.Preview, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return model.Preview{}, err
	}
	defer tx.Rollback()
	preview, _, _, err := previewForUpdate(ctx, tx, id)
	return preview, err
}

func HashConfirmation(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func hashTaskRequest(previewID, confirmation string) string {
	sum := sha256.Sum256([]byte(previewID + "\x00" + confirmation))
	return hex.EncodeToString(sum[:])
}

func (store *Store) CreatePreview(ctx context.Context, input PreviewInput) error {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := store.insertPreview(ctx, tx, input); err != nil {
		return err
	}
	preview := input.Preview
	if _, err := appendAuditRecord(ctx, tx, model.AuditEntry{
		ActorHash: preview.ActorHash,
		Event:     "preview.created",
		Resource:  preview.Service + "/" + preview.Action,
		Outcome:   "accepted",
		Detail:    map[string]any{"target": preview.Target, "risk": preview.Risk},
	}, store.now()); err != nil {
		return err
	}
	return tx.Commit()
}

func (store *Store) insertPreview(ctx context.Context, db eventAuditExecer, input PreviewInput) error {
	steps, err := encodeJSON(input.Preview.Steps)
	if err != nil {
		return err
	}
	snapshot, err := encodeJSON(input.Preview.Snapshot)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `
        INSERT INTO previews (
            id, actor_hash, service, action, target, risk, impact, rollback,
            scope, steps_json, snapshot_json, confirmation_hash, created_at, expires_at
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
    `, input.Preview.ID, input.Preview.ActorHash, input.Preview.Service,
		input.Preview.Action, input.Preview.Target, input.Preview.Risk,
		input.Preview.Impact, input.Preview.Rollback, input.Preview.Scope,
		steps, snapshot, input.ConfirmationHash, timeText(input.Preview.CreatedAt),
		timeText(input.Preview.ExpiresAt))
	if err != nil {
		return fmt.Errorf("保存操作预览失败: %w", err)
	}
	return nil
}

func (store *Store) StartTask(
	ctx context.Context,
	actorHash string,
	request model.StartTaskRequest,
	taskID string,
) (model.Task, bool, error) {
	result, err := store.startTask(ctx, actorHash, request, taskID)
	return result.Task, result.Created, err
}

func (store *Store) StartTaskWithEvent(
	ctx context.Context,
	actorHash string,
	request model.StartTaskRequest,
	taskID string,
) (TaskStartResult, error) {
	return store.startTask(ctx, actorHash, request, taskID)
}

func (store *Store) startTask(
	ctx context.Context,
	actorHash string,
	request model.StartTaskRequest,
	taskID string,
) (TaskStartResult, error) {
	requestHash := hashTaskRequest(request.PreviewID, request.Confirmation)
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return TaskStartResult{}, err
	}
	defer tx.Rollback()
	if task, found, err := taskByIdempotency(ctx, tx, request.IdempotencyKey); err != nil {
		return TaskStartResult{}, err
	} else if found {
		if task.ActorHash != actorHash {
			return TaskStartResult{}, ErrActorMismatch
		}
		if subtle.ConstantTimeCompare([]byte(task.RequestHash), []byte(requestHash)) != 1 {
			return TaskStartResult{}, ErrIdempotency
		}
		return TaskStartResult{Task: task}, nil
	}
	preview, confirmationHash, consumed, err := previewForUpdate(ctx, tx, request.PreviewID)
	if err != nil {
		return TaskStartResult{}, err
	}
	if preview.ActorHash != actorHash {
		return TaskStartResult{}, ErrActorMismatch
	}
	if consumed.Valid {
		return TaskStartResult{}, ErrPreviewConsumed
	}
	if store.now().After(preview.ExpiresAt) {
		return TaskStartResult{}, ErrPreviewExpired
	}
	actualHash := HashConfirmation(request.Confirmation)
	if subtle.ConstantTimeCompare([]byte(actualHash), []byte(confirmationHash)) != 1 {
		return TaskStartResult{}, ErrConfirmation
	}
	var activeID string
	err = tx.QueryRowContext(ctx, `
		SELECT id FROM tasks WHERE service = ? AND state IN (?, ?, ?, ?) LIMIT 1
	`, preview.Service, model.TaskWaitingConfirmation, model.TaskQueued, model.TaskRunning,
		model.TaskRollingBack).Scan(&activeID)
	if err == nil {
		return TaskStartResult{}, fmt.Errorf("服务已有活动任务: %s", activeID)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return TaskStartResult{}, err
	}
	now := store.now()
	snapshot, err := encodeJSON(preview.Snapshot)
	if err != nil {
		return TaskStartResult{}, err
	}
	stages := make([]model.TaskStage, 0, len(preview.Steps))
	for _, step := range preview.Steps {
		stages = append(stages, model.TaskStage{Name: step, State: model.StagePending})
	}
	stagesJSON, err := encodeJSON(stages)
	if err != nil {
		return TaskStartResult{}, err
	}
	_, err = tx.ExecContext(ctx, `
	        INSERT INTO tasks (
	            id, idempotency_key, request_hash, actor_hash, service, action, target, risk,
	            state, preview_id, snapshot_json, stages_json, created_at
	        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	    `, taskID, request.IdempotencyKey, requestHash, actorHash, preview.Service, preview.Action,
		preview.Target, preview.Risk, model.TaskQueued, preview.ID, snapshot, stagesJSON, timeText(now))
	if err != nil {
		return TaskStartResult{}, fmt.Errorf("创建任务失败: %w", err)
	}
	result, err := tx.ExecContext(ctx,
		`UPDATE previews SET consumed_at = ? WHERE id = ? AND consumed_at IS NULL`,
		timeText(now), preview.ID)
	if err != nil {
		return TaskStartResult{}, err
	}
	rows, err := result.RowsAffected()
	if err != nil || rows != 1 {
		return TaskStartResult{}, ErrPreviewConsumed
	}
	task := model.Task{
		ID: taskID, IdempotencyKey: request.IdempotencyKey, RequestHash: requestHash, ActorHash: actorHash,
		Service: preview.Service, Action: preview.Action, Target: preview.Target,
		Risk: preview.Risk, State: model.TaskQueued, PreviewID: preview.ID,
		Snapshot: preview.Snapshot, Stages: stages, CreatedAt: now,
	}
	if preview.Snapshot != nil {
		task.TrafficPolicyDigest, _ = preview.Snapshot["trafficPolicyDigest"].(string)
	}
	queued, err := appendEventRecord(ctx, tx, model.Event{
		TaskID: task.ID, Level: "info", Phase: "queued", Message: "任务已进入执行队列",
	}, now)
	if err != nil {
		return TaskStartResult{}, err
	}
	if _, err := appendAuditRecord(ctx, tx, model.AuditEntry{
		ActorHash: task.ActorHash, Event: "task.accepted", Resource: task.ID,
		Outcome: "accepted", Detail: map[string]any{
			"service": task.Service, "action": task.Action, "target": task.Target,
		},
	}, now); err != nil {
		return TaskStartResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return TaskStartResult{}, err
	}
	return TaskStartResult{Task: task, QueuedEvent: queued, Created: true}, nil
}

func previewForUpdate(
	ctx context.Context,
	tx *sql.Tx,
	id string,
) (model.Preview, string, sql.NullString, error) {
	var preview model.Preview
	var risk, stepsJSON, snapshotJSON, createdAt, expiresAt, confirmationHash string
	var consumed sql.NullString
	err := tx.QueryRowContext(ctx, `
        SELECT id, actor_hash, service, action, target, risk, impact, rollback,
               scope, steps_json, snapshot_json, confirmation_hash, created_at,
               expires_at, consumed_at
        FROM previews WHERE id = ?
    `, id).Scan(&preview.ID, &preview.ActorHash, &preview.Service, &preview.Action,
		&preview.Target, &risk, &preview.Impact, &preview.Rollback, &preview.Scope,
		&stepsJSON, &snapshotJSON, &confirmationHash, &createdAt, &expiresAt, &consumed)
	if err == sql.ErrNoRows {
		return model.Preview{}, "", consumed, ErrNotFound
	}
	if err != nil {
		return model.Preview{}, "", consumed, err
	}
	preview.Risk = model.Risk(risk)
	if err := decodeJSON(stepsJSON, &preview.Steps); err != nil {
		return model.Preview{}, "", consumed, err
	}
	if err := decodeJSON(snapshotJSON, &preview.Snapshot); err != nil {
		return model.Preview{}, "", consumed, err
	}
	preview.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return model.Preview{}, "", consumed, err
	}
	preview.ExpiresAt, err = time.Parse(time.RFC3339Nano, expiresAt)
	return preview, confirmationHash, consumed, err
}
