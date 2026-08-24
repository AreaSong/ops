package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

func (store *Store) SaveRecoveryPoint(ctx context.Context, point model.RecoveryPoint) error {
	evidenceValue := point.Evidence
	// Keep the top-level binding and the signed evidence envelope in sync.  The
	// top-level columns are queried during authorization, while the envelope is
	// what gets hashed and audited.
	if point.TenantID == "" {
		point.TenantID = evidenceValue.TenantID
	}
	if point.ServerID == "" {
		point.ServerID = evidenceValue.ServerID
	}
	if point.ExpectedBeforeDigest == "" {
		point.ExpectedBeforeDigest = evidenceValue.ExpectedBeforeDigest
	}
	if evidenceValue.TenantID == "" {
		evidenceValue.TenantID = point.TenantID
	}
	if evidenceValue.ServerID == "" {
		evidenceValue.ServerID = point.ServerID
	}
	if evidenceValue.ExpectedBeforeDigest == "" {
		evidenceValue.ExpectedBeforeDigest = point.ExpectedBeforeDigest
	}
	if point.BindingDigest == "" {
		point.BindingDigest = evidenceValue.BindingDigest
	}
	if evidenceValue.BindingDigest == "" {
		evidenceValue.BindingDigest = point.BindingDigest
	}
	evidence, err := encodeJSON(evidenceValue)
	if err != nil {
		return err
	}
	requiredRoles, err := encodeJSON(point.RequiredArtifactRoles)
	if err != nil {
		return err
	}
	expectedBefore, err := encodeJSON(point.ExpectedBefore)
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
			expected_before_digest, required_roles_json, created_at, verified_at, recoverable_until,
			tenant_id, server_id, expected_before_json, binding_digest, restore_outcome,
			restore_evidence_digest, outcome_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, point.ID, point.TaskID, point.Service, point.Status, evidence, point.EvidenceDigest,
		point.ExpectedBeforeDigest, requiredRoles, timeText(point.CreatedAt), verifiedAt, recoverableUntil,
		point.TenantID, point.ServerID, expectedBefore, point.BindingDigest, point.RestoreOutcome,
		point.RestoreEvidenceDigest, nullableTimeValue(point.OutcomeAt)); err != nil {
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
	var evidenceJSON, requiredRolesJSON, expectedBeforeJSON, createdAt string
	var verifiedAt, recoverableUntil, outcomeAt sql.NullString
	err := store.db.QueryRowContext(ctx, `
		SELECT id, task_id, service, status, evidence_json, evidence_digest,
		       expected_before_digest, required_roles_json, created_at, verified_at, recoverable_until,
		       tenant_id, server_id, expected_before_json, binding_digest, restore_outcome,
		       restore_evidence_digest, outcome_at
		FROM recovery_points WHERE id = ?
	`, id).Scan(&point.ID, &point.TaskID, &point.Service, &point.Status, &evidenceJSON,
		&point.EvidenceDigest, &point.ExpectedBeforeDigest, &requiredRolesJSON,
		&createdAt, &verifiedAt, &recoverableUntil, &point.TenantID, &point.ServerID,
		&expectedBeforeJSON, &point.BindingDigest, &point.RestoreOutcome,
		&point.RestoreEvidenceDigest, &outcomeAt)
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
	if err := decodeJSON(expectedBeforeJSON, &point.ExpectedBefore); err != nil {
		return model.RecoveryPoint{}, err
	}
	if point.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt); err != nil {
		return model.RecoveryPoint{}, err
	}
	if point.VerifiedAt, err = nullableTime(verifiedAt); err != nil {
		return model.RecoveryPoint{}, err
	}
	if point.RecoverableUntil, err = nullableTime(recoverableUntil); err != nil {
		return model.RecoveryPoint{}, err
	}
	point.OutcomeAt, err = nullableTime(outcomeAt)
	if err != nil {
		return model.RecoveryPoint{}, err
	}
	// Rows created before binding columns were introduced retain the values in
	// the signed evidence JSON.  Hydrate those values for callers while keeping
	// the legacy row readable.
	if point.TenantID == "" {
		point.TenantID = point.Evidence.TenantID
	}
	if point.ServerID == "" {
		point.ServerID = point.Evidence.ServerID
	}
	if point.ExpectedBeforeDigest == "" {
		point.ExpectedBeforeDigest = point.Evidence.ExpectedBeforeDigest
	}
	if point.BindingDigest == "" {
		point.BindingDigest = point.Evidence.BindingDigest
	}
	return point, err
}

func (store *Store) ListRecoveryPoints(ctx context.Context, service string, limit int) ([]model.RecoveryPoint, error) {
	rows, err := store.db.QueryContext(ctx, `
		SELECT id FROM recovery_points WHERE service = ? ORDER BY created_at DESC, id DESC LIMIT ?`,
		service, clampLimit(limit, 200))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	points := make([]model.RecoveryPoint, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		point, err := store.GetRecoveryPoint(ctx, id)
		if err != nil {
			return nil, err
		}
		points = append(points, point)
	}
	return points, rows.Err()
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

// MarkRecoveryPointOutcome records the immutable restore result separately from
// the verification status.  A failed restore must not turn a verified backup
// into a usable replacement point, so status is intentionally left unchanged.
func (store *Store) MarkRecoveryPointOutcome(
	ctx context.Context, id, outcome, evidenceDigest string, occurredAt time.Time,
) error {
	result, err := store.db.ExecContext(ctx, `
		UPDATE recovery_points SET restore_outcome = ?, restore_evidence_digest = ?, outcome_at = ?
		WHERE id = ?
	`, outcome, evidenceDigest, timeText(occurredAt), id)
	return requireOne(result, err, "恢复点结果无法写入")
}

func nullableTimeValue(value *time.Time) any {
	if value == nil {
		return nil
	}
	return timeText(*value)
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
