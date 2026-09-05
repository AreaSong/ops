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
	if revision.ExpiresAt.IsZero() {
		revision.ExpiresAt = revision.CreatedAt.Add(15 * time.Minute)
	}
	semanticDiff, err := encodeJSON(revision.SemanticDiff)
	if err != nil {
		return model.ComposeRevision{}, false, err
	}
	alertFingerprints, err := encodeJSON(revision.BlockingAlertFingerprints)
	if err != nil {
		return model.ComposeRevision{}, false, err
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
			if err := tx.Commit(); err != nil {
				return model.ComposeRevision{}, false, err
			}
			return existing, false, nil
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO compose_revisions(
		id,service,digest,source,content,validated,approved_by,created_at,
		proposal_idempotency_key,request_digest,expected_digest,state,actor_hash,
		confirmation_hash,confirmation_phrase,tenant_id,server_id,project_name,
		baseline_semantic_digest,candidate_semantic_digest,semantic_diff_json,policy_digest,
		baseline_effective_digest,candidate_effective_digest,env_file_digest,
		recovery_point_id,recovery_point_expected_digest,recovery_point_binding_digest,
		recovery_point_evidence_digest,recovery_point_verified_at,recovery_point_recoverable_until,
		alert_evidence_digest,blocking_alert_fingerprints_json,alert_checked_at,expires_at,
		expected_runtime_identity_digest,expected_runtime_image,expected_runtime_image_id,
		candidate_image,candidate_image_digest,candidate_image_id)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, revision.ID, revision.Service, revision.Digest,
		revision.Source, revision.Content, revision.Validated, revision.ApprovedBy,
		timeText(revision.CreatedAt), revision.IdempotencyKey, requestDigest,
		revision.ExpectedDigest, revision.State, revision.ActorHash,
		HashConfirmation(revision.ConfirmationPhrase), revision.ConfirmationPhrase,
		revision.TenantID, revision.ServerID, revision.ProjectName,
		revision.BaselineSemanticDigest, revision.CandidateSemanticDigest, semanticDiff,
		revision.PolicyDigest, revision.BaselineEffectiveDigest, revision.CandidateEffectiveDigest,
		revision.EnvFileDigest, revision.RecoveryPointID, revision.RecoveryPointExpectedDigest,
		revision.RecoveryPointBindingDigest, revision.RecoveryPointEvidenceDigest,
		nullableTimeValue(revision.RecoveryPointVerifiedAt),
		nullableTimeValue(revision.RecoveryPointRecoverableUntil),
		revision.AlertEvidenceDigest, alertFingerprints, nullableTimeValue(revision.AlertCheckedAt),
		timeText(revision.ExpiresAt), revision.ExpectedRuntimeIdentityDigest,
		revision.ExpectedRuntimeImage, revision.ExpectedRuntimeImageID,
		revision.CandidateImage, revision.CandidateImageDigest, revision.CandidateImageID)
	if err != nil {
		return model.ComposeRevision{}, false, err
	}
	if err := appendPlanAudit(ctx, tx, model.AuditEntry{
		ActorHash: revision.ActorHash, Event: "compose.revision.proposed", Resource: revision.ID,
		Outcome: "accepted", Detail: map[string]any{
			"service": revision.Service, "digest": revision.Digest,
			"policyDigest": revision.PolicyDigest, "recoveryPointId": revision.RecoveryPointID,
		},
	}, revision.CreatedAt); err != nil {
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
	now := store.now()
	if revision.ExpiresAt.IsZero() || !now.Before(revision.ExpiresAt) {
		if revision.State == "proposed" || revision.State == "pending_second_approval" || revision.State == "approved" {
			result, updateErr := tx.ExecContext(ctx, `UPDATE compose_revisions SET state='expired',error='Compose 提案已过期',finished_at=? WHERE id=? AND state=?`, timeText(now), id, revision.State)
			if err := requireOne(result, updateErr, "Compose 提案过期状态无法写入"); err != nil {
				return model.ComposeRevision{}, err
			}
			revision.State, revision.Error, revision.FinishedAt = "expired", "Compose 提案已过期", &now
			if err := appendComposeAudit(ctx, tx, "system", "compose.revision.expired", revision.State, revision, now); err != nil {
				return model.ComposeRevision{}, err
			}
			if err := tx.Commit(); err != nil {
				return model.ComposeRevision{}, err
			}
		}
		return model.ComposeRevision{}, errors.New("Compose 提案已过期")
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
	switch revision.State {
	case "proposed":
		result, updateErr := tx.ExecContext(ctx, `UPDATE compose_revisions SET state='pending_second_approval',approved_by=?,approved_at=? WHERE id=? AND state='proposed'`, actor, timeText(now), id)
		err = requireOne(result, updateErr, "Compose 第一批准无法写入")
		revision.State, revision.ApprovedBy, revision.ApprovedAt = "pending_second_approval", actor, &now
	case "pending_second_approval":
		if revision.ApprovedBy == actor {
			if err := tx.Commit(); err != nil {
				return model.ComposeRevision{}, err
			}
			return revision, nil
		}
		result, updateErr := tx.ExecContext(ctx, `UPDATE compose_revisions SET state='approved',second_approved_by_hash=?,second_approved_at=? WHERE id=? AND state='pending_second_approval' AND approved_by<>?`, actor, timeText(now), id, actor)
		err = requireOne(result, updateErr, "Compose 第二批准无法写入")
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
	if err := appendComposeAudit(ctx, tx, actor, "compose.revision.approved", revision.State, revision, now); err != nil {
		return model.ComposeRevision{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.ComposeRevision{}, err
	}
	return revision, nil
}

func (store *Store) StartComposeApply(
	ctx context.Context, id, actor, idempotencyKey string, gate model.ComposeExecutionGate,
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
		if revision.AppliedByHash != actor {
			return model.ComposeRevision{}, false, ErrActorMismatch
		}
		if revision.State == "applying" {
			now := store.now()
			result, updateErr := tx.ExecContext(ctx, `UPDATE compose_revisions SET state='needs_attention',error='Compose 应用中断，结果未知',finished_at=? WHERE id=? AND state='applying'`, timeText(now), id)
			if err := requireOne(result, updateErr, "Compose 中断状态无法收口"); err != nil {
				return model.ComposeRevision{}, false, err
			}
			revision.State, revision.Error, revision.FinishedAt = "needs_attention", "Compose 应用中断，结果未知", &now
			if err := appendComposeAudit(ctx, tx, "system", "compose.revision.recovered", revision.State, revision, now); err != nil {
				return model.ComposeRevision{}, false, err
			}
			if err := tx.Commit(); err != nil {
				return model.ComposeRevision{}, false, err
			}
			return revision, false, errors.New(revision.Error)
		}
		if err := tx.Commit(); err != nil {
			return model.ComposeRevision{}, false, err
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
	if revision.ExpiresAt.IsZero() || !now.Before(revision.ExpiresAt) {
		return model.ComposeRevision{}, false, errors.New("Compose 提案已过期")
	}
	if gate.PolicyDigest == "" || gate.PolicyDigest != revision.PolicyDigest ||
		gate.RecoveryPointID != revision.RecoveryPointID ||
		gate.RecoveryPointExpectedDigest != revision.RecoveryPointExpectedDigest ||
		gate.RecoveryPointBindingDigest != revision.RecoveryPointBindingDigest ||
		gate.RecoveryPointEvidenceDigest != revision.RecoveryPointEvidenceDigest ||
		gate.ExpectedRuntimeIdentityDigest != revision.ExpectedRuntimeIdentityDigest {
		return model.ComposeRevision{}, false, errors.New("Compose 执行门禁与批准合同不一致")
	}
	if gate.CheckedAt.IsZero() || gate.CheckedAt.Before(now.Add(-30*time.Second)) || gate.CheckedAt.After(now.Add(5*time.Second)) ||
		gate.AlertEvidenceDigest == "" || len(gate.BlockingAlertFingerprints) != 0 {
		return model.ComposeRevision{}, false, errors.New("Compose 告警门禁证据无效")
	}
	if err := store.verifyComposeRecoveryBinding(ctx, tx, revision, now); err != nil {
		return model.ComposeRevision{}, false, err
	}
	alertFingerprints, err := encodeJSON(gate.BlockingAlertFingerprints)
	if err != nil {
		return model.ComposeRevision{}, false, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE compose_revisions SET state='applying',applied_by_hash=?,apply_idempotency_key=?,applied_at=?,alert_evidence_digest=?,blocking_alert_fingerprints_json=?,alert_checked_at=? WHERE id=? AND state='approved'`, actor, idempotencyKey, timeText(now), gate.AlertEvidenceDigest, alertFingerprints, timeText(gate.CheckedAt), id)
	if err != nil {
		return model.ComposeRevision{}, false, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return model.ComposeRevision{}, false, errors.New("Compose 修订状态已变化")
	}
	revision.State, revision.AppliedByHash, revision.ApplyIdempotencyKey, revision.AppliedAt = "applying", actor, idempotencyKey, &now
	revision.AlertEvidenceDigest, revision.BlockingAlertFingerprints, revision.AlertCheckedAt = gate.AlertEvidenceDigest, append([]string(nil), gate.BlockingAlertFingerprints...), &gate.CheckedAt
	if err := appendComposeAudit(ctx, tx, actor, "compose.revision.apply_started", revision.State, revision, now); err != nil {
		return model.ComposeRevision{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return model.ComposeRevision{}, false, err
	}
	return revision, true, nil
}

func (store *Store) FinishComposeApply(
	ctx context.Context, id, state, controlledBackup, runtimeBackup, errorText, runtimeIdentityDigest string,
) error {
	if state != "applied" && state != "rolled_back" && state != "failed" && state != "needs_attention" {
		return errors.New("Compose 应用终态无效")
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	revision, _, found, err := composeRevisionQuery(ctx, tx, "id", id)
	if err != nil || !found {
		if err == nil {
			err = ErrNotFound
		}
		return err
	}
	now := store.now()
	result, err := tx.ExecContext(ctx, `UPDATE compose_revisions SET state=?,backup_controlled_path=?,backup_runtime_path=?,error=?,applied_runtime_identity_digest=?,finished_at=? WHERE id=? AND state='applying'`, state, controlledBackup, runtimeBackup, errorText, runtimeIdentityDigest, timeText(now), id)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return errors.New("Compose 应用状态无法收口")
	}
	revision.State, revision.BackupControlledPath, revision.BackupRuntimePath = state, controlledBackup, runtimeBackup
	revision.Error, revision.AppliedRuntimeIdentityDigest, revision.FinishedAt = errorText, runtimeIdentityDigest, &now
	if err := appendComposeAudit(ctx, tx, revision.AppliedByHash, "compose.revision.apply_finished", state, revision, now); err != nil {
		return err
	}
	return tx.Commit()
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
		if revision.RolledBackByHash != actor {
			return model.ComposeRevision{}, false, ErrActorMismatch
		}
		if revision.State == "rolling_back" {
			now := store.now()
			message := "Compose 回滚中断，结果未知"
			result, updateErr := tx.ExecContext(ctx, `UPDATE compose_revisions
				SET state='needs_attention',error=?,rollback_finished_at=?
				WHERE id=? AND state='rolling_back'`, message, timeText(now), id)
			if err := requireOne(result, updateErr, "Compose 回滚中断状态无法收口"); err != nil {
				return model.ComposeRevision{}, false, err
			}
			revision.State, revision.Error, revision.RollbackFinishedAt = "needs_attention", message, &now
			if err := appendComposeAudit(ctx, tx, "system", "compose.revision.recovered", revision.State, revision, now); err != nil {
				return model.ComposeRevision{}, false, err
			}
			if err := tx.Commit(); err != nil {
				return model.ComposeRevision{}, false, err
			}
			return revision, false, errors.New(message)
		}
		if err := tx.Commit(); err != nil {
			return model.ComposeRevision{}, false, err
		}
		return revision, false, nil
	}
	if revision.State != "applied" || revision.BackupControlledPath == "" || revision.BackupRuntimePath == "" {
		return model.ComposeRevision{}, false, errors.New("Compose 修订没有可用回滚副本")
	}
	now := store.now()
	result, err := tx.ExecContext(ctx, `UPDATE compose_revisions SET state='rolling_back',rollback_idempotency_key=?,rolled_back_by_hash=?,rollback_started_at=? WHERE id=? AND state='applied'`, idempotencyKey, actor, timeText(now), id)
	if err != nil {
		return model.ComposeRevision{}, false, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return model.ComposeRevision{}, false, errors.New("Compose 修订状态已变化")
	}
	revision.State, revision.RollbackIdempotencyKey, revision.RolledBackByHash = "rolling_back", idempotencyKey, actor
	revision.RollbackStartedAt = &now
	if err := appendComposeAudit(ctx, tx, actor, "compose.revision.rollback_started", revision.State, revision, now); err != nil {
		return model.ComposeRevision{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return model.ComposeRevision{}, false, err
	}
	return revision, true, nil
}

func (store *Store) FinishComposeRollback(ctx context.Context, id, state, errorText string) error {
	if state != "rolled_back" && state != "needs_attention" {
		return errors.New("Compose 回滚终态无效")
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	revision, _, found, err := composeRevisionQuery(ctx, tx, "id", id)
	if err != nil || !found {
		if err == nil {
			err = ErrNotFound
		}
		return err
	}
	now := store.now()
	result, err := tx.ExecContext(ctx, `UPDATE compose_revisions SET state=?,error=?,rollback_finished_at=? WHERE id=? AND state='rolling_back'`, state, errorText, timeText(now), id)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return errors.New("Compose 回滚状态无法收口")
	}
	revision.State, revision.Error, revision.RollbackFinishedAt = state, errorText, &now
	if err := appendComposeAudit(ctx, tx, revision.RolledBackByHash, "compose.revision.rollback_finished", state, revision, now); err != nil {
		return err
	}
	return tx.Commit()
}

const composeRevisionSelect = `SELECT id,proposal_idempotency_key,request_digest,service,digest,
	expected_digest,source,content,validated,state,actor_hash,confirmation_phrase,
	approved_by,second_approved_by_hash,applied_by_hash,apply_idempotency_key,
	rollback_idempotency_key,backup_controlled_path,backup_runtime_path,error,
	created_at,approved_at,second_approved_at,applied_at,finished_at,
	tenant_id,server_id,project_name,baseline_semantic_digest,candidate_semantic_digest,
	semantic_diff_json,policy_digest,baseline_effective_digest,candidate_effective_digest,
	env_file_digest,recovery_point_id,recovery_point_expected_digest,
	recovery_point_binding_digest,recovery_point_evidence_digest,recovery_point_verified_at,
	recovery_point_recoverable_until,alert_evidence_digest,blocking_alert_fingerprints_json,
	alert_checked_at,expires_at,expected_runtime_identity_digest,expected_runtime_image,
	expected_runtime_image_id,candidate_image,candidate_image_digest,
	candidate_image_id,applied_runtime_identity_digest,rolled_back_by_hash,rollback_started_at,rollback_finished_at
	FROM compose_revisions`

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
	var requestDigest, created, semanticDiffJSON, alertFingerprintsJSON, expiresAt string
	var approved, secondApproved, applied, finished sql.NullString
	var recoveryVerified, recoveryUntil, alertChecked, rollbackStarted, rollbackFinished sql.NullString
	err := scanner.Scan(&item.ID, &item.IdempotencyKey, &requestDigest, &item.Service,
		&item.Digest, &item.ExpectedDigest, &item.Source, &item.Content, &item.Validated,
		&item.State, &item.ActorHash, &item.ConfirmationPhrase, &item.ApprovedBy,
		&item.SecondApprovedByHash, &item.AppliedByHash, &item.ApplyIdempotencyKey,
		&item.RollbackIdempotencyKey, &item.BackupControlledPath, &item.BackupRuntimePath,
		&item.Error, &created, &approved, &secondApproved, &applied, &finished,
		&item.TenantID, &item.ServerID, &item.ProjectName, &item.BaselineSemanticDigest,
		&item.CandidateSemanticDigest, &semanticDiffJSON, &item.PolicyDigest,
		&item.BaselineEffectiveDigest, &item.CandidateEffectiveDigest, &item.EnvFileDigest,
		&item.RecoveryPointID, &item.RecoveryPointExpectedDigest,
		&item.RecoveryPointBindingDigest, &item.RecoveryPointEvidenceDigest,
		&recoveryVerified, &recoveryUntil, &item.AlertEvidenceDigest,
		&alertFingerprintsJSON, &alertChecked, &expiresAt,
		&item.ExpectedRuntimeIdentityDigest, &item.ExpectedRuntimeImage,
		&item.ExpectedRuntimeImageID, &item.CandidateImage, &item.CandidateImageDigest,
		&item.CandidateImageID, &item.AppliedRuntimeIdentityDigest, &item.RolledBackByHash, &rollbackStarted, &rollbackFinished)
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
	if err == nil && expiresAt != "" {
		item.ExpiresAt, err = time.Parse(time.RFC3339Nano, expiresAt)
	}
	if err == nil {
		item.RecoveryPointVerifiedAt, err = nullableTime(recoveryVerified)
	}
	if err == nil {
		item.RecoveryPointRecoverableUntil, err = nullableTime(recoveryUntil)
	}
	if err == nil {
		item.AlertCheckedAt, err = nullableTime(alertChecked)
	}
	if err == nil {
		item.RollbackStartedAt, err = nullableTime(rollbackStarted)
	}
	if err == nil {
		item.RollbackFinishedAt, err = nullableTime(rollbackFinished)
	}
	if err == nil {
		err = decodeJSON(semanticDiffJSON, &item.SemanticDiff)
	}
	if err == nil {
		err = decodeJSON(alertFingerprintsJSON, &item.BlockingAlertFingerprints)
	}
	return item, requestDigest, err
}

func (store *Store) verifyComposeRecoveryBinding(
	ctx context.Context,
	tx *sql.Tx,
	revision model.ComposeRevision,
	now time.Time,
) error {
	if revision.RecoveryPointID == "" || revision.RecoveryPointExpectedDigest == "" ||
		revision.RecoveryPointBindingDigest == "" || revision.RecoveryPointEvidenceDigest == "" {
		return errors.New("Compose 提案缺少恢复点绑定")
	}
	var status, tenantID, serverID, expectedDigest, bindingDigest, evidenceDigest string
	var verifiedAt, recoverableUntil sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT status,tenant_id,server_id,expected_before_digest,
		binding_digest,evidence_digest,verified_at,recoverable_until
		FROM recovery_points WHERE id=?`, revision.RecoveryPointID).Scan(
		&status, &tenantID, &serverID, &expectedDigest, &bindingDigest,
		&evidenceDigest, &verifiedAt, &recoverableUntil)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("Compose 恢复点不存在")
	}
	if err != nil {
		return err
	}
	verified, err := nullableTime(verifiedAt)
	if err != nil || verified == nil {
		return errors.New("Compose 恢复点未验证")
	}
	recoverable, err := nullableTime(recoverableUntil)
	if err != nil || recoverable == nil || !recoverable.After(now) {
		return errors.New("Compose 恢复点已过期")
	}
	if status != "verified" || tenantID != revision.TenantID || serverID != revision.ServerID ||
		expectedDigest != revision.RecoveryPointExpectedDigest ||
		bindingDigest != revision.RecoveryPointBindingDigest ||
		evidenceDigest != revision.RecoveryPointEvidenceDigest ||
		revision.RecoveryPointVerifiedAt == nil || !verified.Equal(*revision.RecoveryPointVerifiedAt) ||
		revision.RecoveryPointRecoverableUntil == nil || !recoverable.Equal(*revision.RecoveryPointRecoverableUntil) {
		return errors.New("Compose 恢复点绑定已变化")
	}
	return nil
}

func appendComposeAudit(
	ctx context.Context,
	tx *sql.Tx,
	actor, event, outcome string,
	revision model.ComposeRevision,
	now time.Time,
) error {
	return appendPlanAudit(ctx, tx, model.AuditEntry{
		ActorHash: actor, Event: event, Resource: revision.ID, Outcome: outcome,
		Detail: map[string]any{
			"service": revision.Service, "digest": revision.Digest,
			"policyDigest": revision.PolicyDigest, "recoveryPointId": revision.RecoveryPointID,
		},
	}, now)
}

// ExpireComposeRevisions closes stale approval envelopes and records the state
// transition in the same SQLite transaction.
func (store *Store) ExpireComposeRevisions(ctx context.Context, now time.Time) error {
	return store.transitionComposeRevisions(ctx,
		`state IN ('proposed','pending_second_approval','approved') AND expires_at!='' AND expires_at<=?`,
		[]any{timeText(now)}, "expired", "Compose 提案已过期", "compose.revision.expired", now)
}

// RecoverInterruptedComposeRevisions makes a Runner restart fail closed. The
// files and Docker state are deliberately not guessed after process loss.
func (store *Store) RecoverInterruptedComposeRevisions(ctx context.Context) error {
	now := store.now()
	return store.transitionComposeRevisions(ctx,
		`state IN ('applying','rolling_back')`, nil, "needs_attention",
		"Compose 执行被 Runner 重启中断，结果无法证明", "compose.revision.recovered", now)
}

func (store *Store) transitionComposeRevisions(
	ctx context.Context,
	where string,
	args []any,
	state, message, event string,
	now time.Time,
) error {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, composeRevisionSelect+" WHERE "+where, args...)
	if err != nil {
		return err
	}
	items := make([]model.ComposeRevision, 0)
	for rows.Next() {
		item, _, scanErr := scanComposeRevision(rows)
		if scanErr != nil {
			rows.Close()
			return scanErr
		}
		items = append(items, item)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, item := range items {
		previous := item.State
		result, updateErr := tx.ExecContext(ctx, `UPDATE compose_revisions SET state=?,error=?,finished_at=? WHERE id=? AND state=?`,
			state, message, timeText(now), item.ID, previous)
		if err := requireOne(result, updateErr, "Compose 状态恢复冲突"); err != nil {
			return err
		}
		item.State, item.Error, item.FinishedAt = state, message, &now
		if err := appendComposeAudit(ctx, tx, "system", event, state, item, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}
