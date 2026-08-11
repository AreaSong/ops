package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

func (store *Store) SaveRecoveryPoint(ctx context.Context, point model.RecoveryPoint) error {
	evidence, err := encodeJSON(point.Evidence)
	if err != nil {
		return err
	}
	var verifiedAt, recoverableUntil any
	if point.VerifiedAt != nil {
		verifiedAt = timeText(*point.VerifiedAt)
	}
	if point.RecoverableUntil != nil {
		recoverableUntil = timeText(*point.RecoverableUntil)
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO recovery_points (
			id, task_id, service, status, evidence_json, evidence_digest,
			created_at, verified_at, recoverable_until
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, point.ID, point.TaskID, point.Service, point.Status, evidence, point.EvidenceDigest,
		timeText(point.CreatedAt), verifiedAt, recoverableUntil); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE tasks SET recovery_point_id = ? WHERE id = ? AND service = ? AND state = ?
	`, point.ID, point.TaskID, point.Service, model.TaskRunning)
	if err = requireOne(result, err, "恢复点无法绑定任务"); err != nil {
		return err
	}
	return tx.Commit()
}

func (store *Store) GetRecoveryPoint(ctx context.Context, id string) (model.RecoveryPoint, error) {
	var point model.RecoveryPoint
	var evidenceJSON, createdAt string
	var verifiedAt, recoverableUntil sql.NullString
	err := store.db.QueryRowContext(ctx, `
		SELECT id, task_id, service, status, evidence_json, evidence_digest,
		       created_at, verified_at, recoverable_until
		FROM recovery_points WHERE id = ?
	`, id).Scan(&point.ID, &point.TaskID, &point.Service, &point.Status, &evidenceJSON,
		&point.EvidenceDigest, &createdAt, &verifiedAt, &recoverableUntil)
	if errors.Is(err, sql.ErrNoRows) {
		return model.RecoveryPoint{}, ErrNotFound
	}
	if err != nil {
		return model.RecoveryPoint{}, err
	}
	if err := decodeJSON(evidenceJSON, &point.Evidence); err != nil {
		return model.RecoveryPoint{}, err
	}
	if point.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt); err != nil {
		return model.RecoveryPoint{}, err
	}
	if point.VerifiedAt, err = nullableTime(verifiedAt); err != nil {
		return model.RecoveryPoint{}, err
	}
	point.RecoverableUntil, err = nullableTime(recoverableUntil)
	return point, err
}
