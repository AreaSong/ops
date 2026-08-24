package store

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

func (store *Store) SaveComposeRevisionIdempotent(
	ctx context.Context, revision model.ComposeRevision, requestDigest string,
) (model.ComposeRevision, bool, error) {
	if revision.ID == "" || revision.Service == "" || revision.Digest == "" {
		return model.ComposeRevision{}, false, errors.New("Compose 修订信息不完整")
	}
	if revision.CreatedAt.IsZero() {
		revision.CreatedAt = store.now()
	}
	if revision.State == "" {
		revision.State = "proposed"
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return model.ComposeRevision{}, false, err
	}
	defer tx.Rollback()
	if revision.IdempotencyKey != "" {
		existing, existingDigest, found, err := composeRevisionByIdempotency(ctx, tx, revision.IdempotencyKey)
		if err != nil {
			return model.ComposeRevision{}, false, err
		}
		if found {
			if existing.ActorHash != revision.ActorHash || existingDigest != requestDigest {
				return model.ComposeRevision{}, false, ErrIdempotency
			}
			return existing, false, nil
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO compose_revisions(
		id,service,digest,source,content,validated,approved_by,created_at,
		proposal_idempotency_key,request_digest,expected_digest,state,actor_hash,
		confirmation_hash,confirmation_phrase)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, revision.ID, revision.Service, revision.Digest,
		revision.Source, revision.Content, revision.Validated, revision.ApprovedBy,
		timeText(revision.CreatedAt), revision.IdempotencyKey, requestDigest,
		revision.ExpectedDigest, revision.State, revision.ActorHash,
		HashConfirmation(revision.ConfirmationPhrase), revision.ConfirmationPhrase)
	if err != nil {
		return model.ComposeRevision{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return model.ComposeRevision{}, false, err
	}
	return revision, true, nil
}

func (store *Store) GetComposeRevision(ctx context.Context, id string) (model.ComposeRevision, error) {
	revision, _, found, err := composeRevisionQuery(ctx, store.db, "id", id)
	if err != nil {
		return model.ComposeRevision{}, err
	}
	if !found {
		return model.ComposeRevision{}, ErrNotFound
	}
	return revision, nil
}

func (store *Store) ApproveComposeRevision(
	ctx context.Context, id, actor, digest, confirmation string,
) (model.ComposeRevision, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return model.ComposeRevision{}, err
	}
	defer tx.Rollback()
	revision, _, found, err := composeRevisionQuery(ctx, tx, "id", id)
	if err != nil || !found {
		if err == nil {
			err = ErrNotFound
		}
		return model.ComposeRevision{}, err
	}
	if revision.Digest != digest {
		return model.ComposeRevision{}, errors.New("Compose 修订摘要不匹配")
	}
	if revision.ActorHash == actor {
		return model.ComposeRevision{}, errors.New("Compose 提案创建人不能批准自己的提案")
	}
	var confirmationHash string
	if err := tx.QueryRowContext(ctx, `SELECT confirmation_hash FROM compose_revisions WHERE id=?`, id).Scan(&confirmationHash); err != nil {
		return model.ComposeRevision{}, err
	}
	if subtle.ConstantTimeCompare([]byte(HashConfirmation(confirmation)), []byte(confirmationHash)) != 1 {
		return model.ComposeRevision{}, ErrConfirmation
	}
	now := store.now()
	switch revision.State {
	case "proposed":
		_, err = tx.ExecContext(ctx, `UPDATE compose_revisions SET state='pending_second_approval',approved_by=?,approved_at=? WHERE id=? AND state='proposed'`, actor, timeText(now), id)
		revision.State, revision.ApprovedBy, revision.ApprovedAt = "pending_second_approval", actor, &now
	case "pending_second_approval":
		if revision.ApprovedBy == actor {
			return revision, nil
		}
		_, err = tx.ExecContext(ctx, `UPDATE compose_revisions SET state='approved',second_approved_by_hash=?,second_approved_at=? WHERE id=? AND state='pending_second_approval' AND approved_by<>?`, actor, timeText(now), id, actor)
		revision.State, revision.SecondApprovedByHash, revision.SecondApprovedAt = "approved", actor, &now
	default:
		if revision.ApprovedBy == actor || revision.SecondApprovedByHash == actor {
			return revision, nil
		}
		return model.ComposeRevision{}, errors.New("Compose 修订已完成批准或执行")
	}
	if err != nil {
		return model.ComposeRevision{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.ComposeRevision{}, err
	}
	return revision, nil
}

func (store *Store) StartComposeApply(
	ctx context.Context, id, actor, idempotencyKey string,
) (model.ComposeRevision, bool, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return model.ComposeRevision{}, false, err
	}
	defer tx.Rollback()
	revision, _, found, err := composeRevisionQuery(ctx, tx, "id", id)
	if err != nil || !found {
		if err == nil {
			err = ErrNotFound
		}
		return model.ComposeRevision{}, false, err
	}
	if revision.ApplyIdempotencyKey != "" {
		if revision.ApplyIdempotencyKey != idempotencyKey {
			return model.ComposeRevision{}, false, ErrIdempotency
		}
		if revision.State == "applying" {
			now := store.now()
			_, _ = tx.ExecContext(ctx, `UPDATE compose_revisions SET state='needs_attention',error='Compose 应用中断，结果未知',finished_at=? WHERE id=? AND state='applying'`, timeText(now), id)
			if err := tx.Commit(); err != nil {
				return model.ComposeRevision{}, false, err
			}
			revision.State, revision.Error, revision.FinishedAt = "needs_attention", "Compose 应用中断，结果未知", &now
			return revision, false, errors.New(revision.Error)
		}
		return revision, false, nil
	}
	if revision.State != "approved" || revision.ApprovedBy == "" || revision.SecondApprovedByHash == "" {
		return model.ComposeRevision{}, false, errors.New("Compose 修订尚未完成双人批准")
	}
	if actor == revision.ActorHash || actor == revision.ApprovedBy || actor == revision.SecondApprovedByHash {
		return model.ComposeRevision{}, false, errors.New("Compose 执行人必须独立于创建人与批准人")
	}
	now := store.now()
	result, err := tx.ExecContext(ctx, `UPDATE compose_revisions SET state='applying',applied_by_hash=?,apply_idempotency_key=?,applied_at=? WHERE id=? AND state='approved'`, actor, idempotencyKey, timeText(now), id)
	if err != nil {
		return model.ComposeRevision{}, false, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return model.ComposeRevision{}, false, errors.New("Compose 修订状态已变化")
	}
	if err := tx.Commit(); err != nil {
		return model.ComposeRevision{}, false, err
	}
	revision.State, revision.AppliedByHash, revision.ApplyIdempotencyKey, revision.AppliedAt = "applying", actor, idempotencyKey, &now
	return revision, true, nil
}

func (store *Store) FinishComposeApply(
	ctx context.Context, id, state, controlledBackup, runtimeBackup, errorText string,
) error {
	if state != "applied" && state != "rolled_back" && state != "failed" && state != "needs_attention" {
		return errors.New("Compose 应用终态无效")
	}
	result, err := store.db.ExecContext(ctx, `UPDATE compose_revisions SET state=?,backup_controlled_path=?,backup_runtime_path=?,error=?,finished_at=? WHERE id=? AND state='applying'`, state, controlledBackup, runtimeBackup, errorText, timeText(store.now()), id)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return errors.New("Compose 应用状态无法收口")
	}
	return nil
}

func (store *Store) StartComposeRollback(
	ctx context.Context, id, actor, idempotencyKey string,
) (model.ComposeRevision, bool, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return model.ComposeRevision{}, false, err
	}
	defer tx.Rollback()
	revision, _, found, err := composeRevisionQuery(ctx, tx, "id", id)
	if err != nil || !found {
		if err == nil {
			err = ErrNotFound
		}
		return model.ComposeRevision{}, false, err
	}
	if revision.RollbackIdempotencyKey != "" {
		if revision.RollbackIdempotencyKey != idempotencyKey {
			return model.ComposeRevision{}, false, ErrIdempotency
		}
		return revision, false, nil
	}
	if revision.State != "applied" || revision.BackupControlledPath == "" || revision.BackupRuntimePath == "" {
		return model.ComposeRevision{}, false, errors.New("Compose 修订没有可用回滚副本")
	}
	result, err := tx.ExecContext(ctx, `UPDATE compose_revisions SET state='rolling_back',rollback_idempotency_key=? WHERE id=? AND state='applied'`, idempotencyKey, id)
	if err != nil {
		return model.ComposeRevision{}, false, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return model.ComposeRevision{}, false, errors.New("Compose 修订状态已变化")
	}
	if err := tx.Commit(); err != nil {
		return model.ComposeRevision{}, false, err
	}
	revision.State, revision.RollbackIdempotencyKey = "rolling_back", idempotencyKey
	return revision, true, nil
}

func (store *Store) FinishComposeRollback(ctx context.Context, id, state, errorText string) error {
	if state != "rolled_back" && state != "needs_attention" {
		return errors.New("Compose 回滚终态无效")
	}
	result, err := store.db.ExecContext(ctx, `UPDATE compose_revisions SET state=?,error=?,finished_at=? WHERE id=? AND state='rolling_back'`, state, errorText, timeText(store.now()), id)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return errors.New("Compose 回滚状态无法收口")
	}
	return nil
}

const composeRevisionSelect = `SELECT id,proposal_idempotency_key,request_digest,service,digest,
	expected_digest,source,content,validated,state,actor_hash,confirmation_phrase,
	approved_by,second_approved_by_hash,applied_by_hash,apply_idempotency_key,
	rollback_idempotency_key,backup_controlled_path,backup_runtime_path,error,
	created_at,approved_at,second_approved_at,applied_at,finished_at FROM compose_revisions`

func composeRevisionByIdempotency(ctx context.Context, db queryer, key string) (model.ComposeRevision, string, bool, error) {
	return composeRevisionQuery(ctx, db, "proposal_idempotency_key", key)
}

func composeRevisionQuery(ctx context.Context, db queryer, field, value string) (model.ComposeRevision, string, bool, error) {
	query := composeRevisionSelect
	switch field {
	case "id":
		query += " WHERE id=?"
	case "proposal_idempotency_key":
		query += " WHERE proposal_idempotency_key=?"
	default:
		return model.ComposeRevision{}, "", false, errors.New("Compose 修订查询字段无效")
	}
	revision, digest, err := scanComposeRevision(db.QueryRowContext(ctx, query, value))
	if errors.Is(err, sql.ErrNoRows) {
		return model.ComposeRevision{}, "", false, nil
	}
	return revision, digest, err == nil, err
}

type composeRevisionScanner interface{ Scan(...any) error }

func scanComposeRevision(scanner composeRevisionScanner) (model.ComposeRevision, string, error) {
	var item model.ComposeRevision
	var requestDigest, created string
	var approved, secondApproved, applied, finished sql.NullString
	err := scanner.Scan(&item.ID, &item.IdempotencyKey, &requestDigest, &item.Service,
		&item.Digest, &item.ExpectedDigest, &item.Source, &item.Content, &item.Validated,
		&item.State, &item.ActorHash, &item.ConfirmationPhrase, &item.ApprovedBy,
		&item.SecondApprovedByHash, &item.AppliedByHash, &item.ApplyIdempotencyKey,
		&item.RollbackIdempotencyKey, &item.BackupControlledPath, &item.BackupRuntimePath,
		&item.Error, &created, &approved, &secondApproved, &applied, &finished)
	if err != nil {
		return model.ComposeRevision{}, "", err
	}
	item.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err == nil {
		item.ApprovedAt, err = nullableTime(approved)
	}
	if err == nil {
		item.SecondApprovedAt, err = nullableTime(secondApproved)
	}
	if err == nil {
		item.AppliedAt, err = nullableTime(applied)
	}
	if err == nil {
		item.FinishedAt, err = nullableTime(finished)
	}
	return item, requestDigest, err
}
