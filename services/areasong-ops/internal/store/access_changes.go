package store

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

// CreateAccessChange stores the immutable request envelope for a high-risk
// access-policy mutation. The payload is retained only in the root-owned
// SQLite database and is never echoed by the list endpoint.
func (store *Store) CreateAccessChange(
	ctx context.Context,
	change model.AccessChange,
	payloadJSON string,
	confirmationHash string,
) (model.AccessChange, bool, error) {
	if change.ID == "" || change.IdempotencyKey == "" || change.RequestDigest == "" ||
		change.ActorHash == "" || payloadJSON == "" || confirmationHash == "" {
		return model.AccessChange{}, false, errors.New("访问策略审批变更信息不完整")
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return model.AccessChange{}, false, err
	}
	defer tx.Rollback()
	var existing model.AccessChange
	var existingPayload, existingConfirmationHash string
	var created, approvedAt, secondApprovedAt, appliedAt sql.NullString
	var dual int
	err = tx.QueryRowContext(ctx, `SELECT id,idempotency_key,request_digest,actor_hash,payload_json,state,
		requires_dual_approval,confirmation_hash,confirmation_phrase,approved_by_hash,second_approved_by_hash,
		error,created_at,approved_at,second_approved_at,applied_at
		FROM access_changes WHERE idempotency_key=?`, change.IdempotencyKey).Scan(
		&existing.ID, &existing.IdempotencyKey, &existing.RequestDigest, &existing.ActorHash, &existingPayload,
		&existing.State, &dual, &existingConfirmationHash, &existing.ConfirmationPhrase,
		&existing.ApprovedByHash, &existing.SecondApprovedByHash, &existing.Error,
		&created, &approvedAt, &secondApprovedAt, &appliedAt)
	if err == nil {
		if existing.ActorHash != change.ActorHash || existing.RequestDigest != change.RequestDigest || existingPayload != payloadJSON {
			return model.AccessChange{}, false, ErrIdempotency
		}
		existing.RequiresDualApproval = dual != 0
		if err := scanAccessChangeTimes(&existing, created, approvedAt, secondApprovedAt, appliedAt); err != nil {
			return model.AccessChange{}, false, err
		}
		if err := tx.Commit(); err != nil {
			return model.AccessChange{}, false, err
		}
		return existing, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return model.AccessChange{}, false, err
	}
	if change.CreatedAt.IsZero() {
		change.CreatedAt = store.now()
	}
	if change.State == "" {
		change.State = model.AccessChangePendingApproval
	}
	if !change.RequiresDualApproval {
		return model.AccessChange{}, false, errors.New("访问策略审批变更必须启用双人批准")
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO access_changes(
		id,idempotency_key,request_digest,actor_hash,payload_json,state,requires_dual_approval,
		confirmation_hash,confirmation_phrase,approved_by_hash,second_approved_by_hash,error,
		created_at,approved_at,second_approved_at,applied_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		change.ID, change.IdempotencyKey, change.RequestDigest, change.ActorHash, payloadJSON,
		change.State, change.RequiresDualApproval, confirmationHash, change.ConfirmationPhrase,
		change.ApprovedByHash, change.SecondApprovedByHash, change.Error, timeText(change.CreatedAt),
		nullableTimeText(change.ApprovedAt), nullableTimeText(change.SecondApprovedAt), nullableTimeText(change.AppliedAt))
	if err != nil {
		return model.AccessChange{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return model.AccessChange{}, false, err
	}
	return change, true, nil
}

func (store *Store) GetAccessChange(ctx context.Context, id string) (model.AccessChange, error) {
	change, _, err := store.getAccessChange(ctx, id)
	return change, err
}

func (store *Store) GetAccessChangeWithPayload(ctx context.Context, id string) (model.AccessChange, string, error) {
	return store.getAccessChange(ctx, id)
}

func (store *Store) ListAccessChanges(ctx context.Context, limit int) ([]model.AccessChange, error) {
	rows, err := store.db.QueryContext(ctx, `SELECT id,idempotency_key,request_digest,actor_hash,state,
		requires_dual_approval,confirmation_phrase,approved_by_hash,second_approved_by_hash,error,
		created_at,approved_at,second_approved_at,applied_at
		FROM access_changes ORDER BY created_at DESC LIMIT ?`, clampLimit(limit, 200))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]model.AccessChange, 0)
	for rows.Next() {
		var change model.AccessChange
		var created, approvedAt, secondApprovedAt, appliedAt sql.NullString
		var dual int
		if err := rows.Scan(&change.ID, &change.IdempotencyKey, &change.RequestDigest, &change.ActorHash,
			&change.State, &dual, &change.ConfirmationPhrase, &change.ApprovedByHash,
			&change.SecondApprovedByHash, &change.Error, &created, &approvedAt, &secondApprovedAt, &appliedAt); err != nil {
			return nil, err
		}
		change.RequiresDualApproval = dual != 0
		if err := scanAccessChangeTimes(&change, created, approvedAt, secondApprovedAt, appliedAt); err != nil {
			return nil, err
		}
		// Idempotency keys are implementation details and are never needed by
		// the browser to approve a change.
		change.IdempotencyKey = ""
		result = append(result, change)
	}
	return result, rows.Err()
}

func (store *Store) ApproveAccessChange(
	ctx context.Context,
	id, actor, digest, confirmation string,
) (model.AccessChange, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return model.AccessChange{}, err
	}
	defer tx.Rollback()
	change, _, err := scanAccessChange(tx.QueryRowContext(ctx, accessChangeSelect+` WHERE id=?`, id), false)
	if errors.Is(err, sql.ErrNoRows) {
		return model.AccessChange{}, ErrNotFound
	}
	if err != nil {
		return model.AccessChange{}, err
	}
	if change.RequestDigest != digest {
		return model.AccessChange{}, errors.New("访问策略审批摘要已变化，批准失效")
	}
	var expectedHash string
	if err := tx.QueryRowContext(ctx, `SELECT confirmation_hash FROM access_changes WHERE id=?`, id).Scan(&expectedHash); err != nil {
		return model.AccessChange{}, err
	}
	if subtle.ConstantTimeCompare([]byte(expectedHash), []byte(HashConfirmation(confirmation))) != 1 {
		return model.AccessChange{}, ErrConfirmation
	}
	if change.ActorHash == actor {
		return model.AccessChange{}, errors.New("访问策略变更创建人不能批准自己的变更")
	}
	if change.State != model.AccessChangePendingApproval {
		if change.ApprovedByHash == actor || change.SecondApprovedByHash == actor {
			return change, tx.Commit()
		}
		return model.AccessChange{}, errors.New("访问策略变更当前不能批准")
	}
	now := store.now()
	if change.ApprovedByHash == "" {
		result, execErr := tx.ExecContext(ctx, `UPDATE access_changes SET approved_by_hash=?,approved_at=? WHERE id=? AND state=? AND approved_by_hash=''`, actor, timeText(now), id, model.AccessChangePendingApproval)
		if err := requireOne(result, execErr, "访问策略第一批准无法写入"); err != nil {
			return model.AccessChange{}, err
		}
		change.ApprovedByHash, change.ApprovedAt = actor, &now
	} else {
		if change.ApprovedByHash == actor {
			return model.AccessChange{}, ErrActorMismatch
		}
		result, execErr := tx.ExecContext(ctx, `UPDATE access_changes SET state=?,second_approved_by_hash=?,second_approved_at=? WHERE id=? AND state=? AND approved_by_hash!=? AND second_approved_by_hash=''`, model.AccessChangeApproved, actor, timeText(now), id, model.AccessChangePendingApproval, actor)
		if err := requireOne(result, execErr, "访问策略第二批准无法写入"); err != nil {
			return model.AccessChange{}, err
		}
		change.State, change.SecondApprovedByHash, change.SecondApprovedAt = model.AccessChangeApproved, actor, &now
	}
	if err := tx.Commit(); err != nil {
		return model.AccessChange{}, err
	}
	return change, nil
}

func (store *Store) MarkAccessChangeApplied(ctx context.Context, id, actor string) (model.AccessChange, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return model.AccessChange{}, err
	}
	defer tx.Rollback()
	change, _, err := scanAccessChange(tx.QueryRowContext(ctx, accessChangeSelect+` WHERE id=?`, id), false)
	if errors.Is(err, sql.ErrNoRows) {
		return model.AccessChange{}, ErrNotFound
	}
	if err != nil {
		return model.AccessChange{}, err
	}
	if change.State == model.AccessChangeApplied {
		return change, tx.Commit()
	}
	if change.State != model.AccessChangeApproved {
		return model.AccessChange{}, errors.New("访问策略变更尚未完成双人批准")
	}
	if actor == change.ActorHash || actor == change.ApprovedByHash || actor == change.SecondApprovedByHash {
		return model.AccessChange{}, errors.New("访问策略变更执行人必须独立于创建人与批准人")
	}
	now := store.now()
	result, execErr := tx.ExecContext(ctx, `UPDATE access_changes SET state=?,applied_at=? WHERE id=? AND state=?`, model.AccessChangeApplied, timeText(now), id, model.AccessChangeApproved)
	if err := requireOne(result, execErr, "访问策略变更收口失败"); err != nil {
		return model.AccessChange{}, err
	}
	change.State, change.AppliedAt = model.AccessChangeApplied, &now
	if err := tx.Commit(); err != nil {
		return model.AccessChange{}, err
	}
	return change, nil
}

func (store *Store) RejectAccessChange(ctx context.Context, id, actor, reason string) (model.AccessChange, error) {
	if reason == "" {
		reason = "操作者拒绝访问策略变更"
	}
	result, err := store.db.ExecContext(ctx, `UPDATE access_changes SET state=?,error=? WHERE id=? AND state=? AND actor_hash=?`, model.AccessChangeRejected, reason, id, model.AccessChangePendingApproval, actor)
	if err != nil {
		return model.AccessChange{}, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return model.AccessChange{}, errors.New("访问策略变更无法拒绝")
	}
	return store.GetAccessChange(ctx, id)
}

const accessChangeSelect = `SELECT id,idempotency_key,request_digest,actor_hash,state,
	requires_dual_approval,confirmation_phrase,approved_by_hash,second_approved_by_hash,error,
	created_at,approved_at,second_approved_at,applied_at FROM access_changes`

const accessChangeSelectWithPayload = `SELECT id,idempotency_key,request_digest,actor_hash,state,
	requires_dual_approval,confirmation_phrase,approved_by_hash,second_approved_by_hash,error,
	created_at,approved_at,second_approved_at,applied_at,payload_json FROM access_changes`

func (store *Store) getAccessChange(ctx context.Context, id string) (model.AccessChange, string, error) {
	return scanAccessChange(store.db.QueryRowContext(ctx, accessChangeSelectWithPayload+` WHERE id=?`, id), true)
}

type accessChangeScanner interface{ Scan(...any) error }

func scanAccessChange(row accessChangeScanner, withPayload bool) (model.AccessChange, string, error) {
	var change model.AccessChange
	var payload string
	var created, approvedAt, secondApprovedAt, appliedAt sql.NullString
	var dual int
	var err error
	if withPayload {
		err = row.Scan(&change.ID, &change.IdempotencyKey, &change.RequestDigest, &change.ActorHash, &change.State,
			&dual, &change.ConfirmationPhrase, &change.ApprovedByHash, &change.SecondApprovedByHash,
			&change.Error, &created, &approvedAt, &secondApprovedAt, &appliedAt, &payload)
	} else {
		err = row.Scan(&change.ID, &change.IdempotencyKey, &change.RequestDigest, &change.ActorHash, &change.State,
			&dual, &change.ConfirmationPhrase, &change.ApprovedByHash, &change.SecondApprovedByHash,
			&change.Error, &created, &approvedAt, &secondApprovedAt, &appliedAt)
	}
	if err != nil {
		return model.AccessChange{}, "", err
	}
	change.RequiresDualApproval = dual != 0
	if err := scanAccessChangeTimes(&change, created, approvedAt, secondApprovedAt, appliedAt); err != nil {
		return model.AccessChange{}, "", err
	}
	return change, payload, nil
}

func scanAccessChangeTimes(change *model.AccessChange, created, approvedAt, secondApprovedAt, appliedAt sql.NullString) error {
	var err error
	if !created.Valid {
		return errors.New("访问策略变更缺少创建时间")
	}
	change.CreatedAt, err = time.Parse(time.RFC3339Nano, created.String)
	if err != nil {
		return err
	}
	if change.ApprovedAt, err = nullableTime(approvedAt); err != nil {
		return err
	}
	if change.SecondApprovedAt, err = nullableTime(secondApprovedAt); err != nil {
		return err
	}
	if change.AppliedAt, err = nullableTime(appliedAt); err != nil {
		return err
	}
	return nil
}
