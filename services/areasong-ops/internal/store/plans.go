package store

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

type ReleasePlanInput struct {
	Plan             model.ReleasePlan
	ConfirmationHash string
}

const planSelect = `
	SELECT id, actor_hash, service, action, target, tenant_id, server_id, schedule_at, risk, state, digest,
	       approval_summary_json, confirmation_phrase, approved_by_hash, approved_at,
	       invalidated_reason, task_id, observation_seconds, observation_started_at,
	       observation_ends_at, closure_reason, maintenance_silence_id,
	       maintenance_silence_ends_at, maintenance_silence_released_at,
	       blocking_alert_fingerprints_json, closed_at, created_at, updated_at,
	       request_idempotency_key, request_digest, restore_mode, recovery_point_id,
	       requires_dual_approval, second_approved_by_hash, approval_policy, restore_tenant_id,
		       restore_server_id, restore_expected_before_digest, restore_contract_digest,
		       restore_revalidation_digest, restore_revalidated_at, executed_by_hash,
		       restore_outcome, restore_evidence_digest
		FROM release_plans`

func (store *Store) CreateReleasePlan(ctx context.Context, input ReleasePlanInput) error {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := store.createReleasePlan(ctx, tx, input); err != nil {
		return err
	}
	if err := appendPlanAudit(ctx, tx, releasePlanCreatedAudit(input.Plan), store.now()); err != nil {
		return err
	}
	return tx.Commit()
}

type planExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func (store *Store) createReleasePlan(ctx context.Context, db planExecer, input ReleasePlanInput) error {
	summary, err := encodeJSON(input.Plan.ApprovalSummary)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `
		INSERT INTO release_plans (
			id, actor_hash, service, action, target, tenant_id, server_id, schedule_at, risk, state, digest,
			approval_summary_json, confirmation_hash, confirmation_phrase,
			observation_seconds, created_at, updated_at, request_idempotency_key,
			request_digest, restore_mode, recovery_point_id, requires_dual_approval,
			second_approved_by_hash, approval_policy, restore_tenant_id, restore_server_id,
			restore_expected_before_digest, restore_contract_digest,
			restore_revalidation_digest, restore_revalidated_at, executed_by_hash,
			restore_outcome, restore_evidence_digest
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?,
			?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, input.Plan.ID, input.Plan.ActorHash, input.Plan.Service, input.Plan.Action,
		input.Plan.Target, input.Plan.TenantID, input.Plan.ServerID, nullableTimeValue(input.Plan.ScheduleAt), input.Plan.Risk, input.Plan.State, input.Plan.Digest,
		summary, input.ConfirmationHash, input.Plan.ConfirmationPhrase,
		input.Plan.ObservationSeconds, timeText(input.Plan.CreatedAt), timeText(input.Plan.UpdatedAt),
		input.Plan.RequestIdempotencyKey, input.Plan.RequestDigest, input.Plan.RestoreMode,
		input.Plan.RecoveryPointID, input.Plan.RequiresDualApproval, input.Plan.SecondApprovedByHash, input.Plan.ApprovalPolicy,
		input.Plan.RestoreTenantID, input.Plan.RestoreServerID, input.Plan.RestoreExpectedBeforeDigest,
		input.Plan.RestoreContractDigest, input.Plan.RestoreRevalidationDigest,
		nullableTimeValue(input.Plan.RestoreRevalidatedAt), input.Plan.ExecutedByHash,
		input.Plan.RestoreOutcome, input.Plan.RestoreEvidenceDigest)
	return err
}

func releasePlanCreatedAudit(plan model.ReleasePlan) model.AuditEntry {
	detail := map[string]any{
		"service": plan.Service, "action": plan.Action, "target": plan.Target, "digest": plan.Digest,
	}
	if plan.RequestIdempotencyKey != "" {
		detail["idempotencyKey"] = plan.RequestIdempotencyKey
	}
	if plan.AllowsC2LifecycleSingleActorApproval() {
		detail["approvalException"] = model.ApprovalExceptionC2LifecycleSingleActor
	}
	return model.AuditEntry{
		ActorHash: plan.ActorHash, Event: "plan.created", Resource: plan.ID,
		Outcome: "accepted", Detail: detail,
	}
}

// CreateReleasePlanIdempotent atomically consumes a request key. Replaying the
// same actor and digest returns the original plan without creating a second
// approval record.
func (store *Store) CreateReleasePlanIdempotent(
	ctx context.Context, input ReleasePlanInput, actor, idempotencyKey, requestDigest string,
) (model.ReleasePlan, bool, error) {
	if idempotencyKey == "" || requestDigest == "" {
		return model.ReleasePlan{}, false, errors.New("发布计划幂等信息不完整")
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return model.ReleasePlan{}, false, err
	}
	defer tx.Rollback()
	var existingID, existingActor, existingDigest string
	err = tx.QueryRowContext(ctx, `SELECT id,actor_hash,request_digest FROM release_plans WHERE request_idempotency_key=?`, idempotencyKey).
		Scan(&existingID, &existingActor, &existingDigest)
	if err == nil {
		if existingActor != actor {
			return model.ReleasePlan{}, false, ErrActorMismatch
		}
		if existingDigest != requestDigest {
			return model.ReleasePlan{}, false, ErrIdempotency
		}
		plan, getErr := scanPlan(tx.QueryRowContext(ctx, planSelect+` WHERE id=?`, existingID))
		if getErr != nil {
			return model.ReleasePlan{}, false, getErr
		}
		if err := tx.Commit(); err != nil {
			return model.ReleasePlan{}, false, err
		}
		return plan, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return model.ReleasePlan{}, false, err
	}
	input.Plan.RequestIdempotencyKey = idempotencyKey
	input.Plan.RequestDigest = requestDigest
	if err := store.createReleasePlan(ctx, tx, input); err != nil {
		return model.ReleasePlan{}, false, err
	}
	if err := appendPlanAudit(ctx, tx, releasePlanCreatedAudit(input.Plan), store.now()); err != nil {
		return model.ReleasePlan{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return model.ReleasePlan{}, false, err
	}
	return input.Plan, true, nil
}

func scanPlan(row scanner) (model.ReleasePlan, error) {
	var plan model.ReleasePlan
	var risk, state, summaryJSON, createdAt, updatedAt string
	var scheduleAt sql.NullString
	var tenantID, serverID string
	var approvedAt, observationStartedAt, observationEndsAt, silenceEndsAt, silenceReleasedAt,
		closedAt, restoreRevalidatedAt sql.NullString
	var approvalPolicy string
	var blockingAlertsJSON string
	err := row.Scan(&plan.ID, &plan.ActorHash, &plan.Service, &plan.Action, &plan.Target,
		&tenantID, &serverID, &scheduleAt, &risk, &state, &plan.Digest, &summaryJSON, &plan.ConfirmationPhrase,
		&plan.ApprovedByHash, &approvedAt, &plan.InvalidatedReason, &plan.TaskID,
		&plan.ObservationSeconds, &observationStartedAt, &observationEndsAt,
		&plan.ClosureReason, &plan.MaintenanceSilenceID, &silenceEndsAt, &silenceReleasedAt,
		&blockingAlertsJSON, &closedAt,
		&createdAt, &updatedAt, &plan.RequestIdempotencyKey, &plan.RequestDigest,
		&plan.RestoreMode, &plan.RecoveryPointID, &plan.RequiresDualApproval,
		&plan.SecondApprovedByHash, &approvalPolicy, &plan.RestoreTenantID, &plan.RestoreServerID,
		&plan.RestoreExpectedBeforeDigest, &plan.RestoreContractDigest,
		&plan.RestoreRevalidationDigest, &restoreRevalidatedAt, &plan.ExecutedByHash,
		&plan.RestoreOutcome, &plan.RestoreEvidenceDigest)
	if err != nil {
		return model.ReleasePlan{}, err
	}
	plan.Risk = model.Risk(risk)
	plan.State = model.PlanState(state)
	plan.TenantID, plan.ServerID = tenantID, serverID
	plan.ApprovalPolicy = approvalPolicy
	plan.ScheduleAt, err = nullableTime(scheduleAt)
	if err != nil {
		return model.ReleasePlan{}, err
	}
	plan.RequiresConfirmation = plan.Risk != model.RiskReadOnly
	if err := decodeJSON(summaryJSON, &plan.ApprovalSummary); err != nil {
		return model.ReleasePlan{}, err
	}
	if plan.ApprovalPolicy == "" {
		plan.ApprovalPolicy = plan.ApprovalSummary.ApprovalPolicy
	}
	var parseErr error
	if plan.CreatedAt, parseErr = time.Parse(time.RFC3339Nano, createdAt); parseErr != nil {
		return model.ReleasePlan{}, parseErr
	}
	if plan.UpdatedAt, parseErr = time.Parse(time.RFC3339Nano, updatedAt); parseErr != nil {
		return model.ReleasePlan{}, parseErr
	}
	plan.ApprovedAt, parseErr = nullableTime(approvedAt)
	if parseErr != nil {
		return model.ReleasePlan{}, parseErr
	}
	plan.ObservationStartedAt, parseErr = nullableTime(observationStartedAt)
	if parseErr != nil {
		return model.ReleasePlan{}, parseErr
	}
	plan.ObservationEndsAt, parseErr = nullableTime(observationEndsAt)
	if parseErr != nil {
		return model.ReleasePlan{}, parseErr
	}
	plan.MaintenanceSilenceEndsAt, parseErr = nullableTime(silenceEndsAt)
	if parseErr != nil {
		return model.ReleasePlan{}, parseErr
	}
	plan.MaintenanceSilenceReleasedAt, parseErr = nullableTime(silenceReleasedAt)
	if parseErr != nil {
		return model.ReleasePlan{}, parseErr
	}
	if parseErr = decodeJSON(blockingAlertsJSON, &plan.BlockingAlertFingerprints); parseErr != nil {
		return model.ReleasePlan{}, parseErr
	}
	plan.ClosedAt, parseErr = nullableTime(closedAt)
	if parseErr != nil {
		return model.ReleasePlan{}, parseErr
	}
	plan.RestoreRevalidatedAt, parseErr = nullableTime(restoreRevalidatedAt)
	return plan, parseErr
}

// ActivateScheduledPlan 在到达调度时间后释放计划；状态和审计在同一事务提交。
func (store *Store) ActivateScheduledPlan(
	ctx context.Context,
	id, actorHash string,
	now time.Time,
) (bool, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
		UPDATE release_plans SET state = ?, updated_at = ?
		WHERE id = ? AND state = ? AND schedule_at IS NOT NULL AND schedule_at <= ?
	`, model.PlanApproved, timeText(now), id, model.PlanScheduled, timeText(now))
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	if err != nil || count == 0 {
		return false, err
	}
	var scheduleAt string
	if err := tx.QueryRowContext(ctx, `SELECT schedule_at FROM release_plans WHERE id=?`, id).Scan(&scheduleAt); err != nil {
		return false, err
	}
	if err := appendPlanAudit(ctx, tx, model.AuditEntry{
		ActorHash: actorHash, Event: "plan.schedule.released", Resource: id,
		Outcome: "approved", Detail: map[string]any{"scheduleAt": scheduleAt},
	}, now); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (store *Store) GetReleasePlan(ctx context.Context, id string) (model.ReleasePlan, error) {
	plan, err := scanPlan(store.db.QueryRowContext(ctx, planSelect+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return model.ReleasePlan{}, ErrNotFound
	}
	return plan, err
}

// MarkRestorePlanRevalidated persists the exact recovery-point binding that
// passed the last pre-production check.  It is deliberately conditional on an
// approved plan so a stale/invalidated plan cannot be revalidated out of band.
func (store *Store) MarkRestorePlanRevalidated(
	ctx context.Context, id, actorHash, bindingDigest string, at time.Time,
) error {
	if id == "" || actorHash == "" || bindingDigest == "" {
		return errors.New("恢复计划复验信息不完整")
	}
	result, err := store.db.ExecContext(ctx, `
		UPDATE release_plans
		SET restore_revalidation_digest = ?, restore_revalidated_at = ?, executed_by_hash = ?, updated_at = ?
		WHERE id = ? AND state = ? AND restore_mode != ''
		  AND restore_contract_digest = ?
	`, bindingDigest, timeText(at), actorHash, timeText(at), id, model.PlanApproved, bindingDigest)
	return requireOne(result, err, "恢复计划复验结果无法写入")
}

func (store *Store) MarkRestorePlanOutcome(
	ctx context.Context, id, outcome, evidenceDigest string, at time.Time,
) error {
	result, err := store.db.ExecContext(ctx, `
		UPDATE release_plans SET restore_outcome = ?, restore_evidence_digest = ?, updated_at = ?
		WHERE id = ? AND restore_mode != ''
	`, outcome, evidenceDigest, timeText(at), id)
	return requireOne(result, err, "恢复计划结果无法写入")
}

func (store *Store) GetReleasePlanByRequest(
	ctx context.Context, idempotencyKey string,
) (model.ReleasePlan, bool, error) {
	if idempotencyKey == "" {
		return model.ReleasePlan{}, false, nil
	}
	plan, err := scanPlan(store.db.QueryRowContext(ctx, planSelect+` WHERE request_idempotency_key = ?`, idempotencyKey))
	if errors.Is(err, sql.ErrNoRows) {
		return model.ReleasePlan{}, false, nil
	}
	if err != nil {
		return model.ReleasePlan{}, false, err
	}
	return plan, true, nil
}

func (store *Store) ListReleasePlans(ctx context.Context, limit, offset int) ([]model.ReleasePlan, error) {
	rows, err := store.db.QueryContext(ctx, planSelect+`
		ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`, clampLimit(limit, 201), nonNegative(offset))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	plans := make([]model.ReleasePlan, 0)
	for rows.Next() {
		plan, err := scanPlan(rows)
		if err != nil {
			return nil, err
		}
		plans = append(plans, plan)
	}
	return plans, rows.Err()
}

func (store *Store) ApproveReleasePlan(
	ctx context.Context,
	id, actorHash, digest, confirmation string,
) (model.ReleasePlan, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return model.ReleasePlan{}, err
	}
	defer tx.Rollback()
	plan, err := scanPlan(tx.QueryRowContext(ctx, planSelect+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return model.ReleasePlan{}, ErrNotFound
	}
	if err != nil {
		return model.ReleasePlan{}, err
	}
	if !plan.HasRequiredApprovalPolicy() {
		return model.ReleasePlan{}, errors.New("高风险计划缺少双人审批门禁")
	}
	// 高风险计划必须由独立批准人批准；其他计划保留创建者批准规则，
	// 只有显式双人审批流程的第二步允许非创建者批准。
	if plan.Risk == model.RiskHigh && plan.ActorHash == actorHash &&
		!plan.AllowsC2LifecycleSingleActorApproval() {
		return model.ReleasePlan{}, ErrActorMismatch
	}
	if plan.ActorHash != actorHash &&
		plan.Risk != model.RiskHigh &&
		!(plan.RequiresDualApproval && plan.ApprovedByHash != "") {
		return model.ReleasePlan{}, ErrActorMismatch
	}
	if (plan.State != model.PlanPendingApproval && plan.State != model.PlanApproved) || plan.Digest != digest {
		return model.ReleasePlan{}, errors.New("发布计划已变化，批准失效")
	}
	var expectedHash string
	if err := tx.QueryRowContext(ctx, `SELECT confirmation_hash FROM release_plans WHERE id = ?`, id).Scan(&expectedHash); err != nil {
		return model.ReleasePlan{}, err
	}
	if subtle.ConstantTimeCompare([]byte(expectedHash), []byte(HashConfirmation(confirmation))) != 1 {
		return model.ReleasePlan{}, ErrConfirmation
	}
	now := store.now()
	targetState := model.PlanApproved
	if plan.ScheduleAt != nil && now.Before(*plan.ScheduleAt) {
		targetState = model.PlanScheduled
	}
	if model.UsesTwoPartyApproval(plan.ApprovalPolicy) && plan.RequiresDualApproval {
		if plan.State != model.PlanPendingApproval {
			if plan.ApprovedByHash == actorHash && (plan.State == model.PlanApproved || plan.State == model.PlanScheduled) {
				if err := tx.Commit(); err != nil {
					return model.ReleasePlan{}, err
				}
				return plan, nil
			}
			return model.ReleasePlan{}, errors.New("发布计划已完成批准")
		}
		if plan.ActorHash == actorHash {
			return model.ReleasePlan{}, ErrActorMismatch
		}
		result, err := tx.ExecContext(ctx, `
			UPDATE release_plans SET state = ?, approved_by_hash = ?, approved_at = ?, updated_at = ?
			WHERE id = ? AND state = ? AND digest = ? AND approved_by_hash = ''
		`, targetState, actorHash, timeText(now), timeText(now), id, model.PlanPendingApproval, digest)
		if err = requireOne(result, err, "生产计划独立批准无法写入"); err != nil {
			return model.ReleasePlan{}, err
		}
		plan.State, plan.ApprovedByHash, plan.ApprovedAt, plan.UpdatedAt = targetState, actorHash, &now, now
		if err := appendPlanApprovalAudit(ctx, tx, plan, actorHash, now); err != nil {
			return model.ReleasePlan{}, err
		}
		if err := tx.Commit(); err != nil {
			return model.ReleasePlan{}, err
		}
		return plan, nil
	}
	if plan.RequiresDualApproval {
		if plan.State == model.PlanApproved {
			if plan.SecondApprovedByHash == actorHash || plan.ApprovedByHash == actorHash {
				return model.ReleasePlan{}, ErrActorMismatch
			}
			return model.ReleasePlan{}, errors.New("生产恢复计划已完成双人批准")
		}
		if plan.ApprovedByHash == "" {
			result, err := tx.ExecContext(ctx, `
				UPDATE release_plans SET approved_by_hash = ?, approved_at = ?, updated_at = ?
				WHERE id = ? AND state = ? AND digest = ? AND approved_by_hash = ''
			`, actorHash, timeText(now), timeText(now), id, model.PlanPendingApproval, digest)
			if err = requireOne(result, err, "生产恢复第一批准无法写入"); err != nil {
				return model.ReleasePlan{}, err
			}
			plan.ApprovedByHash, plan.ApprovedAt, plan.UpdatedAt = actorHash, &now, now
			if err := appendPlanApprovalAudit(ctx, tx, plan, actorHash, now); err != nil {
				return model.ReleasePlan{}, err
			}
			if err := tx.Commit(); err != nil {
				return model.ReleasePlan{}, err
			}
			return plan, nil
		}
		if plan.ApprovedByHash == actorHash {
			return model.ReleasePlan{}, ErrActorMismatch
		}
		result, err := tx.ExecContext(ctx, `
			UPDATE release_plans SET state = ?, second_approved_by_hash = ?, updated_at = ?
			WHERE id = ? AND state = ? AND digest = ? AND approved_by_hash != '' AND second_approved_by_hash = ''
		`, targetState, actorHash, timeText(now), id, model.PlanPendingApproval, digest)
		if err = requireOne(result, err, "生产恢复第二批准无法写入"); err != nil {
			return model.ReleasePlan{}, err
		}
		plan.State, plan.SecondApprovedByHash, plan.UpdatedAt = targetState, actorHash, now
		if err := appendPlanApprovalAudit(ctx, tx, plan, actorHash, now); err != nil {
			return model.ReleasePlan{}, err
		}
		if err := tx.Commit(); err != nil {
			return model.ReleasePlan{}, err
		}
		return plan, nil
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE release_plans SET state = ?, approved_by_hash = ?, approved_at = ?, updated_at = ?
		WHERE id = ? AND state = ? AND digest = ?
	`, targetState, actorHash, timeText(now), timeText(now), id,
		model.PlanPendingApproval, digest)
	if err = requireOne(result, err, "发布计划批准失败"); err != nil {
		return model.ReleasePlan{}, err
	}
	plan.State = targetState
	plan.ApprovedByHash = actorHash
	plan.ApprovedAt = &now
	plan.UpdatedAt = now
	if err := appendPlanApprovalAudit(ctx, tx, plan, actorHash, now); err != nil {
		return model.ReleasePlan{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.ReleasePlan{}, err
	}
	return plan, nil
}

func appendPlanApprovalAudit(
	ctx context.Context,
	tx *sql.Tx,
	plan model.ReleasePlan,
	actorHash string,
	now time.Time,
) error {
	detail := map[string]any{"digest": plan.Digest, "scheduleAt": plan.ScheduleAt}
	if plan.AllowsC2LifecycleSingleActorApproval() {
		detail["approvalException"] = model.ApprovalExceptionC2LifecycleSingleActor
		detail["selfApproval"] = true
	}
	return appendPlanAudit(ctx, tx, model.AuditEntry{
		ActorHash: actorHash, Event: "plan.approved", Outcome: string(plan.State), Resource: plan.ID,
		Detail: detail,
	}, now)
}

func (store *Store) InvalidateReleasePlan(ctx context.Context, id, actorHash, reason string) error {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := store.now()
	result, err := tx.ExecContext(ctx, `
		UPDATE release_plans SET state = ?, invalidated_reason = ?, approved_by_hash = '',
			second_approved_by_hash = '', approved_at = NULL, updated_at = ?
		WHERE id = ? AND state IN (?, ?, ?)
	`, model.PlanInvalidated, reason, timeText(now), id,
		model.PlanPendingApproval, model.PlanApproved, model.PlanScheduled)
	if err := requireOne(result, err, "发布计划无法失效"); err != nil {
		return err
	}
	if err := appendPlanAudit(ctx, tx, model.AuditEntry{
		ActorHash: actorHash, Event: "plan.invalidated", Resource: id,
		Outcome: string(model.PlanInvalidated), Detail: map[string]any{"reason": reason},
	}, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (store *Store) RecordPlanClosureBlocker(
	ctx context.Context,
	id, reason string,
	blockingAlertFingerprints []string,
	audit model.AuditEntry,
) error {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := store.now()
	blockingAlertsJSON, err := encodeJSON(blockingAlertFingerprints)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE release_plans SET closure_reason = ?, blocking_alert_fingerprints_json = ?, updated_at = ?
		WHERE id = ? AND state = ?
	`, reason, blockingAlertsJSON, timeText(now), id, model.PlanObserving)
	if err = requireOne(result, err, "计划收口阻断原因无法写入"); err != nil {
		return err
	}
	if err := appendPlanAudit(ctx, tx, audit, now); err != nil {
		return err
	}
	return tx.Commit()
}

// RecordPlanClosureAudit appends a close-attempt audit without changing the
// durable blocker fields on the release plan.  Temporary observation-window
// races must remain retryable and must not become a sticky closure reason.
func (store *Store) RecordPlanClosureAudit(
	ctx context.Context,
	id string,
	audit model.AuditEntry,
) error {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var state string
	if err := tx.QueryRowContext(ctx, `SELECT state FROM release_plans WHERE id = ?`, id).Scan(&state); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if state != string(model.PlanObserving) {
		return errors.New("计划当前不能收口")
	}
	if err := appendPlanAudit(ctx, tx, audit, store.now()); err != nil {
		return err
	}
	return tx.Commit()
}

func (store *Store) RecordPlanSilenceReleased(
	ctx context.Context,
	id string,
	audit model.AuditEntry,
) error {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := store.now()
	result, err := tx.ExecContext(ctx, `
		UPDATE release_plans SET maintenance_silence_released_at = ?, updated_at = ?
		WHERE id = ? AND maintenance_silence_id != '' AND maintenance_silence_released_at IS NULL
	`, timeText(now), timeText(now), id)
	if err = requireOne(result, err, "维护静默解除状态无法写入"); err != nil {
		return err
	}
	if err := appendPlanAudit(ctx, tx, audit, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (store *Store) CloseReleasePlan(
	ctx context.Context,
	id, actorHash, idempotencyKey string,
	audit model.AuditEntry,
) (model.ReleasePlan, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return model.ReleasePlan{}, err
	}
	defer tx.Rollback()
	plan, err := scanPlan(tx.QueryRowContext(ctx, planSelect+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return model.ReleasePlan{}, ErrNotFound
	}
	if err != nil {
		return model.ReleasePlan{}, err
	}
	if plan.ActorHash != actorHash {
		return model.ReleasePlan{}, ErrActorMismatch
	}
	var storedKey string
	if err := tx.QueryRowContext(ctx, `
		SELECT closure_idempotency_key FROM release_plans WHERE id = ?
	`, id).Scan(&storedKey); err != nil {
		return model.ReleasePlan{}, err
	}
	if plan.State == model.PlanCompleted {
		if storedKey == idempotencyKey && storedKey != "" {
			return plan, nil
		}
		return model.ReleasePlan{}, ErrIdempotency
	}
	if plan.State != model.PlanObserving || plan.ObservationEndsAt == nil {
		return model.ReleasePlan{}, errors.New("计划当前不能收口")
	}
	now := store.now()
	if now.Before(*plan.ObservationEndsAt) {
		return model.ReleasePlan{}, errors.New("观察窗口尚未结束")
	}
	var taskState model.TaskState
	if err := tx.QueryRowContext(ctx, `SELECT state FROM tasks WHERE id = ?`, plan.TaskID).Scan(&taskState); err != nil {
		return model.ReleasePlan{}, err
	}
	if taskState != model.TaskSucceeded {
		return model.ReleasePlan{}, errors.New("执行任务未成功，计划不能收口")
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE release_plans SET state = ?, closure_reason = '', blocking_alert_fingerprints_json = '[]',
			closure_idempotency_key = ?,
			closed_at = ?, updated_at = ? WHERE id = ? AND state = ?
	`, model.PlanCompleted, idempotencyKey, timeText(now), timeText(now), id, model.PlanObserving)
	if err = requireOne(result, err, "计划收口失败"); err != nil {
		return model.ReleasePlan{}, err
	}
	if err := appendPlanAudit(ctx, tx, audit, now); err != nil {
		return model.ReleasePlan{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.ReleasePlan{}, err
	}
	plan.State = model.PlanCompleted
	plan.ClosureReason = ""
	plan.BlockingAlertFingerprints = nil
	plan.ClosedAt = &now
	plan.UpdatedAt = now
	return plan, nil
}

func appendPlanAudit(
	ctx context.Context,
	tx *sql.Tx,
	audit model.AuditEntry,
	now time.Time,
) error {
	if audit.OccurredAt.IsZero() {
		audit.OccurredAt = now
	}
	detail, err := encodeJSON(audit.Detail)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO audit_entries (occurred_at, actor_hash, event, resource, outcome, detail_json)
		VALUES (?, ?, ?, ?, ?, ?)
	`, timeText(audit.OccurredAt), audit.ActorHash, audit.Event, audit.Resource,
		audit.Outcome, detail)
	return err
}

func (store *Store) StartPlanTask(
	ctx context.Context,
	plan model.ReleasePlan,
	actorHash, idempotencyKey, taskID string,
	silence *model.MaintenanceSilence,
) (model.Task, bool, error) {
	result, err := store.startPlanTask(ctx, plan, actorHash, idempotencyKey, taskID, silence)
	return result.Task, result.Created, err
}

func (store *Store) StartPlanTaskWithEvent(
	ctx context.Context,
	plan model.ReleasePlan,
	actorHash, idempotencyKey, taskID string,
	silence *model.MaintenanceSilence,
) (TaskStartResult, error) {
	return store.startPlanTask(ctx, plan, actorHash, idempotencyKey, taskID, silence)
}

func (store *Store) startPlanTask(
	ctx context.Context,
	plan model.ReleasePlan,
	actorHash, idempotencyKey, taskID string,
	silence *model.MaintenanceSilence,
) (TaskStartResult, error) {
	requestHash := HashConfirmation(plan.ID + "\x00" + plan.Digest)
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return TaskStartResult{}, err
	}
	defer tx.Rollback()
	if task, found, err := taskByIdempotency(ctx, tx, idempotencyKey); err != nil {
		return TaskStartResult{}, err
	} else if found {
		if task.ActorHash != actorHash || task.RequestHash != requestHash {
			return TaskStartResult{}, ErrIdempotency
		}
		return TaskStartResult{Task: task}, nil
	}
	storedPlan, err := scanPlan(tx.QueryRowContext(ctx, planSelect+` WHERE id=?`, plan.ID))
	if err != nil {
		return TaskStartResult{}, err
	}
	if !storedPlan.AllowsExecutor(actorHash) {
		return TaskStartResult{}, ErrActorMismatch
	}
	if storedPlan.State != model.PlanApproved || storedPlan.Digest != plan.Digest {
		return TaskStartResult{}, errors.New("发布计划未批准或已变化")
	}
	plan = storedPlan
	var activeID string
	err = tx.QueryRowContext(ctx, `
		SELECT id FROM tasks WHERE service = ? AND state IN (?, ?, ?, ?) LIMIT 1
	`, plan.Service, model.TaskWaitingConfirmation, model.TaskQueued, model.TaskRunning,
		model.TaskRollingBack).Scan(&activeID)
	if err == nil {
		return TaskStartResult{}, fmt.Errorf("服务已有活动任务: %s", activeID)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return TaskStartResult{}, err
	}
	stages := make([]model.TaskStage, 0, len(plan.ApprovalSummary.Steps))
	for _, step := range plan.ApprovalSummary.Steps {
		stages = append(stages, model.TaskStage{Name: step, State: model.StagePending})
	}
	stagesJSON, err := encodeJSON(stages)
	if err != nil {
		return TaskStartResult{}, err
	}
	snapshotJSON, err := encodeJSON(plan.ApprovalSummary.ExpectedBefore)
	if err != nil {
		return TaskStartResult{}, err
	}
	now := store.now()
	_, err = tx.ExecContext(ctx, `
		INSERT INTO tasks (
			id, idempotency_key, request_hash, actor_hash, service, action, target, risk,
			state, preview_id, plan_id, plan_digest, snapshot_json, stages_json, created_at,
			recovery_point_id,
			restore_mode, restore_tenant_id, restore_server_id, restore_expected_before_digest,
			restore_contract_digest, restore_revalidated_at, restore_outcome, restore_evidence_digest
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, '', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, taskID, idempotencyKey, requestHash, actorHash, plan.Service, plan.Action,
		plan.Target, plan.Risk, model.TaskQueued, plan.ID, plan.Digest, snapshotJSON,
		stagesJSON, timeText(now), plan.RecoveryPointID, plan.RestoreMode, plan.RestoreTenantID, plan.RestoreServerID,
		plan.RestoreExpectedBeforeDigest, plan.RestoreContractDigest,
		nullableTimeValue(plan.RestoreRevalidatedAt), plan.RestoreOutcome, plan.RestoreEvidenceDigest)
	if err != nil {
		return TaskStartResult{}, err
	}
	var silenceID string
	var silenceEndsAt any
	if silence != nil {
		silenceID = silence.ID
		silenceEndsAt = timeText(silence.EndsAt)
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE release_plans SET state = ?, task_id = ?, executed_by_hash = ?, maintenance_silence_id = ?,
			maintenance_silence_ends_at = ?, maintenance_silence_released_at = NULL, updated_at = ?
		WHERE id = ? AND state = ? AND digest = ?
		  AND (restore_mode = '' OR (restore_revalidation_digest = restore_contract_digest AND restore_revalidated_at IS NOT NULL))
	`, model.PlanExecuting, taskID, actorHash, silenceID, silenceEndsAt, timeText(now),
		plan.ID, model.PlanApproved, plan.Digest)
	if err = requireOne(result, err, "发布计划无法进入执行状态"); err != nil {
		return TaskStartResult{}, err
	}
	if silence != nil {
		if err := appendPlanAudit(ctx, tx, model.AuditEntry{
			ActorHash: actorHash, Event: "plan.maintenance_silence_created",
			Resource: plan.ID, Outcome: "created",
			Detail: map[string]any{"silenceId": silence.ID, "endsAt": silence.EndsAt},
		}, now); err != nil {
			return TaskStartResult{}, err
		}
	}
	task := model.Task{
		ID: taskID, IdempotencyKey: idempotencyKey, RequestHash: requestHash,
		ActorHash: actorHash, Service: plan.Service, Action: plan.Action, Target: plan.Target,
		Risk: plan.Risk, State: model.TaskQueued, PlanID: plan.ID, PlanDigest: plan.Digest,
		TrafficPolicyDigest: plan.ApprovalSummary.TrafficPolicyDigest,
		Snapshot:            plan.ApprovalSummary.ExpectedBefore, Stages: stages, CreatedAt: now,
		RecoveryPointID: plan.RecoveryPointID,
		RestoreMode:     plan.RestoreMode, RestoreTenantID: plan.RestoreTenantID,
		RestoreServerID:             plan.RestoreServerID,
		RestoreExpectedBeforeDigest: plan.RestoreExpectedBeforeDigest,
		RestoreContractDigest:       plan.RestoreContractDigest,
		RestoreRevalidatedAt:        plan.RestoreRevalidatedAt,
		RestoreOutcome:              plan.RestoreOutcome, RestoreEvidenceDigest: plan.RestoreEvidenceDigest,
	}
	queued, err := appendEventRecord(ctx, tx, model.Event{
		TaskID: task.ID, Level: "info", Phase: "queued", Message: "任务已进入执行队列",
	}, now)
	if err != nil {
		return TaskStartResult{}, err
	}
	if err := appendPlanAudit(ctx, tx, model.AuditEntry{
		ActorHash: task.ActorHash, Event: "task.accepted", Resource: task.ID,
		Outcome: "accepted", Detail: map[string]any{
			"service": task.Service, "action": task.Action, "target": task.Target, "planId": plan.ID,
		},
	}, now); err != nil {
		return TaskStartResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return TaskStartResult{}, err
	}
	return TaskStartResult{Task: task, QueuedEvent: queued, Created: true}, nil
}
