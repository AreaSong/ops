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
	requiredRoles, err := encodeJSON(point.RequiredArtifactRoles)
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
			expected_before_digest, required_roles_json, created_at, verified_at, recoverable_until
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, point.ID, point.TaskID, point.Service, point.Status, evidence, point.EvidenceDigest,
		point.ExpectedBeforeDigest, requiredRoles, timeText(point.CreatedAt), verifiedAt, recoverableUntil); err != nil {
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
	var evidenceJSON, requiredRolesJSON, createdAt string
	var verifiedAt, recoverableUntil sql.NullString
	err := store.db.QueryRowContext(ctx, `
		SELECT id, task_id, service, status, evidence_json, evidence_digest,
		       expected_before_digest, required_roles_json, created_at, verified_at, recoverable_until
		FROM recovery_points WHERE id = ?
	`, id).Scan(&point.ID, &point.TaskID, &point.Service, &point.Status, &evidenceJSON,
		&point.EvidenceDigest, &point.ExpectedBeforeDigest, &requiredRolesJSON,
		&createdAt, &verifiedAt, &recoverableUntil)
	if errors.Is(err, sql.ErrNoRows) {
		return model.RecoveryPoint{}, ErrNotFound
	}
	if err != nil {
		return model.RecoveryPoint{}, err
	}
	if err := decodeJSON(evidenceJSON, &point.Evidence); err != nil {
		return model.RecoveryPoint{}, err
	}
	if err := decodeJSON(requiredRolesJSON, &point.RequiredArtifactRoles); err != nil {
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

func (store *Store) ExpireRecoveryPoints(ctx context.Context, now time.Time) (int64, error) {
	result, err := store.db.ExecContext(ctx, `
		UPDATE recovery_points SET status = 'expired'
		WHERE status = 'verified' AND recoverable_until IS NOT NULL AND recoverable_until <= ?
	`, timeText(now))
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (store *Store) ProtectedOperationIDs(ctx context.Context, now time.Time) (map[string]struct{}, error) {
	rows, err := store.db.QueryContext(ctx, `
		SELECT id FROM tasks WHERE rollback_available = 1
		UNION
		SELECT task_id FROM recovery_points
		WHERE status = 'verified' AND (recoverable_until IS NULL OR recoverable_until > ?)
	`, timeText(now))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	protected := make(map[string]struct{})
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		protected[id] = struct{}{}
	}
	return protected, rows.Err()
}
