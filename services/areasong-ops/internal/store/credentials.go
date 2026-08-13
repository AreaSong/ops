package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

func (store *Store) StartCredentialRotation(
	ctx context.Context,
	rotation model.CredentialRotation,
) (model.CredentialRotation, bool, error) {
	_, err := store.db.ExecContext(ctx, `
		INSERT INTO credential_rotations (
			id, idempotency_key, actor_hash, credential_type, target, state,
			fingerprint, expires_at, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, rotation.ID, rotation.IdempotencyKey, rotation.ActorHash, rotation.CredentialType,
		rotation.Target, rotation.State, rotation.Fingerprint, rotation.ExpiresAt,
		timeText(rotation.CreatedAt))
	if err != nil {
		existing, lookupErr := store.credentialRotationByIdempotency(ctx, rotation.IdempotencyKey)
		if lookupErr == nil {
			if existing.ActorHash != rotation.ActorHash || existing.CredentialType != rotation.CredentialType ||
				existing.Fingerprint != rotation.Fingerprint || existing.ExpiresAt != rotation.ExpiresAt {
				return model.CredentialRotation{}, false, ErrIdempotency
			}
			return existing, false, nil
		}
		if !errors.Is(lookupErr, ErrNotFound) {
			return model.CredentialRotation{}, false, lookupErr
		}
		return model.CredentialRotation{}, false, fmt.Errorf("创建凭据轮换失败: %w", err)
	}
	return rotation, true, nil
}

func (store *Store) FinishCredentialRotation(
	ctx context.Context,
	id string,
	result model.CredentialRotationResult,
) error {
	if result.State == model.CredentialRotationRunning || result.State == model.CredentialRotationCompleted {
		return errors.New("凭据轮换终态无效")
	}
	now := store.now()
	res, err := store.db.ExecContext(ctx, `
		UPDATE credential_rotations SET state = ?, validation_result = ?, outcome = ?,
			rollback_result = ?, finished_at = ?
		WHERE id = ? AND state = ?
	`, result.State, result.ValidationResult,
		result.Outcome, result.RollbackResult, timeText(now), id, model.CredentialRotationRunning)
	return requireOne(res, err, "凭据轮换无法写入终态")
}

func (store *Store) CloseCredentialRotation(
	ctx context.Context,
	id, actorHash, idempotencyKey, outcome string,
) (model.CredentialRotation, bool, error) {
	rotation, err := store.GetCredentialRotation(ctx, id)
	if err != nil {
		return model.CredentialRotation{}, false, err
	}
	if rotation.State == model.CredentialRotationCompleted {
		if rotation.ActorHash != actorHash {
			return model.CredentialRotation{}, false, ErrIdempotency
		}
		return rotation, false, nil
	}
	if rotation.State != model.CredentialRotationRevocationVerified ||
		rotation.ClosureIdempotencyKey != idempotencyKey || rotation.ActorHash != actorHash {
		return model.CredentialRotation{}, false, errors.New("凭据轮换当前不能收口")
	}
	now := store.now()
	res, err := store.db.ExecContext(ctx, `
		UPDATE credential_rotations SET state = ?, outcome = ?, closed_at = ?
		WHERE id = ? AND state = ? AND closure_idempotency_key = ?
	`, model.CredentialRotationCompleted, outcome, timeText(now), id,
		model.CredentialRotationRevocationVerified, idempotencyKey)
	if err = requireOne(res, err, "凭据轮换无法完成收口"); err != nil {
		return model.CredentialRotation{}, false, err
	}
	rotation, err = store.GetCredentialRotation(ctx, id)
	return rotation, true, err
}

func (store *Store) MarkCredentialRevocationVerified(
	ctx context.Context,
	id, actorHash, idempotencyKey string,
) (model.CredentialRotation, error) {
	rotation, err := store.GetCredentialRotation(ctx, id)
	if err != nil {
		return model.CredentialRotation{}, err
	}
	if rotation.State == model.CredentialRotationRevocationVerified {
		if rotation.ActorHash != actorHash || rotation.ClosureIdempotencyKey != idempotencyKey {
			return model.CredentialRotation{}, ErrIdempotency
		}
		return rotation, nil
	}
	if rotation.State != model.CredentialRotationSwitchedPendingRevocation || rotation.ActorHash != actorHash {
		return model.CredentialRotation{}, errors.New("凭据撤销证据当前不能写入")
	}
	result, err := store.db.ExecContext(ctx, `
		UPDATE credential_rotations SET state = ?, closure_idempotency_key = ?, outcome = ?
		WHERE id = ? AND state = ? AND actor_hash = ?
	`, model.CredentialRotationRevocationVerified, idempotencyKey,
		"旧凭据已验证撤销，正在清理隔离回滚副本", id,
		model.CredentialRotationSwitchedPendingRevocation, actorHash)
	if err = requireOne(result, err, "凭据撤销证据无法持久化"); err != nil {
		return model.CredentialRotation{}, err
	}
	return store.GetCredentialRotation(ctx, id)
}

func (store *Store) LatestCredentialRotation(
	ctx context.Context,
	credentialType string,
) (model.CredentialRotation, bool, error) {
	rotation, err := scanCredentialRotation(store.db.QueryRowContext(ctx, credentialRotationSelect+`
		WHERE credential_type = ? ORDER BY created_at DESC LIMIT 1`, credentialType))
	if errors.Is(err, sql.ErrNoRows) {
		return model.CredentialRotation{}, false, nil
	}
	return rotation, err == nil, err
}

func (store *Store) GetCredentialRotation(ctx context.Context, id string) (model.CredentialRotation, error) {
	rotation, err := scanCredentialRotation(store.db.QueryRowContext(ctx, credentialRotationSelect+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return model.CredentialRotation{}, ErrNotFound
	}
	return rotation, err
}

func (store *Store) RecoverInterruptedCredentialRotations(ctx context.Context) (int64, error) {
	result, err := store.db.ExecContext(ctx, `
		UPDATE credential_rotations SET state = ?, outcome = ?, rollback_result = ?
		WHERE state = ?
	`, model.CredentialRotationNeedsAttention,
		"Runner 在轮换过程中停止，必须核对当前配置和回滚副本",
		"自动回滚状态未知", model.CredentialRotationRunning)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (store *Store) credentialRotationByIdempotency(
	ctx context.Context,
	key string,
) (model.CredentialRotation, error) {
	rotation, err := scanCredentialRotation(store.db.QueryRowContext(ctx,
		credentialRotationSelect+` WHERE idempotency_key = ?`, key))
	if errors.Is(err, sql.ErrNoRows) {
		return model.CredentialRotation{}, ErrNotFound
	}
	return rotation, err
}

const credentialRotationSelect = `SELECT id, idempotency_key, closure_idempotency_key,
	actor_hash, credential_type, target, state, fingerprint, expires_at,
	validation_result, outcome, rollback_result, created_at, finished_at, closed_at
	FROM credential_rotations`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanCredentialRotation(row rowScanner) (model.CredentialRotation, error) {
	var rotation model.CredentialRotation
	var createdAt string
	var finishedAt, closedAt sql.NullString
	err := row.Scan(&rotation.ID, &rotation.IdempotencyKey, &rotation.ClosureIdempotencyKey,
		&rotation.ActorHash, &rotation.CredentialType, &rotation.Target, &rotation.State,
		&rotation.Fingerprint, &rotation.ExpiresAt, &rotation.ValidationResult,
		&rotation.Outcome, &rotation.RollbackResult,
		&createdAt, &finishedAt, &closedAt)
	if err != nil {
		return model.CredentialRotation{}, err
	}
	if rotation.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt); err != nil {
		return model.CredentialRotation{}, fmt.Errorf("解析凭据轮换创建时间: %w", err)
	}
	if rotation.FinishedAt, err = nullableTime(finishedAt); err != nil {
		return model.CredentialRotation{}, err
	}
	if rotation.ClosedAt, err = nullableTime(closedAt); err != nil {
		return model.CredentialRotation{}, err
	}
	return rotation, nil
}
