package store

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

type PreviewInput struct {
	Preview          model.Preview
	ConfirmationHash string
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
	steps, err := encodeJSON(input.Preview.Steps)
	if err != nil {
		return err
	}
	snapshot, err := encodeJSON(input.Preview.Snapshot)
	if err != nil {
		return err
	}
	_, err = store.db.ExecContext(ctx, `
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
	requestHash := hashTaskRequest(request.PreviewID, request.Confirmation)
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return model.Task{}, false, err
	}
	defer tx.Rollback()
	if task, found, err := taskByIdempotency(ctx, tx, request.IdempotencyKey); err != nil {
		return model.Task{}, false, err
	} else if found {
		if task.ActorHash != actorHash {
			return model.Task{}, false, ErrActorMismatch
		}
		if subtle.ConstantTimeCompare([]byte(task.RequestHash), []byte(requestHash)) != 1 {
			return model.Task{}, false, ErrIdempotency
		}
		return task, false, nil
	}
	preview, confirmationHash, consumed, err := previewForUpdate(ctx, tx, request.PreviewID)
	if err != nil {
		return model.Task{}, false, err
	}
	if preview.ActorHash != actorHash {
		return model.Task{}, false, ErrActorMismatch
	}
	if consumed.Valid {
		return model.Task{}, false, ErrPreviewConsumed
	}
	if store.now().After(preview.ExpiresAt) {
		return model.Task{}, false, ErrPreviewExpired
	}
	actualHash := HashConfirmation(request.Confirmation)
	if subtle.ConstantTimeCompare([]byte(actualHash), []byte(confirmationHash)) != 1 {
		return model.Task{}, false, ErrConfirmation
	}
	now := store.now()
	snapshot, err := encodeJSON(preview.Snapshot)
	if err != nil {
		return model.Task{}, false, err
	}
	_, err = tx.ExecContext(ctx, `
	        INSERT INTO tasks (
	            id, idempotency_key, request_hash, actor_hash, service, action, target, risk,
	            state, preview_id, snapshot_json, created_at
	        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	    `, taskID, request.IdempotencyKey, requestHash, actorHash, preview.Service, preview.Action,
		preview.Target, preview.Risk, model.TaskQueued, preview.ID, snapshot, timeText(now))
	if err != nil {
		return model.Task{}, false, fmt.Errorf("创建任务失败: %w", err)
	}
	result, err := tx.ExecContext(ctx,
		`UPDATE previews SET consumed_at = ? WHERE id = ? AND consumed_at IS NULL`,
		timeText(now), preview.ID)
	if err != nil {
		return model.Task{}, false, err
	}
	rows, err := result.RowsAffected()
	if err != nil || rows != 1 {
		return model.Task{}, false, ErrPreviewConsumed
	}
	if err := tx.Commit(); err != nil {
		return model.Task{}, false, err
	}
	task := model.Task{
		ID: taskID, IdempotencyKey: request.IdempotencyKey, RequestHash: requestHash, ActorHash: actorHash,
		Service: preview.Service, Action: preview.Action, Target: preview.Target,
		Risk: preview.Risk, State: model.TaskQueued, PreviewID: preview.ID,
		Snapshot: preview.Snapshot, CreatedAt: now,
	}
	return task, true, nil
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
