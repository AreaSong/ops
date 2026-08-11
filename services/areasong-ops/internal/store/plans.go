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
	SELECT id, actor_hash, service, action, target, risk, state, digest,
	       approval_summary_json, confirmation_phrase, approved_by_hash, approved_at,
	       invalidated_reason, task_id, observation_seconds, observation_started_at,
	       observation_ends_at, closure_reason, closed_at, created_at, updated_at
	FROM release_plans`

func (store *Store) CreateReleasePlan(ctx context.Context, input ReleasePlanInput) error {
	summary, err := encodeJSON(input.Plan.ApprovalSummary)
	if err != nil {
		return err
	}
	_, err = store.db.ExecContext(ctx, `
		INSERT INTO release_plans (
			id, actor_hash, service, action, target, risk, state, digest,
			approval_summary_json, confirmation_hash, confirmation_phrase,
			observation_seconds, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, input.Plan.ID, input.Plan.ActorHash, input.Plan.Service, input.Plan.Action,
		input.Plan.Target, input.Plan.Risk, input.Plan.State, input.Plan.Digest,
		summary, input.ConfirmationHash, input.Plan.ConfirmationPhrase,
		input.Plan.ObservationSeconds,
		timeText(input.Plan.CreatedAt), timeText(input.Plan.UpdatedAt))
	return err
}

func scanPlan(row scanner) (model.ReleasePlan, error) {
	var plan model.ReleasePlan
	var risk, state, summaryJSON, createdAt, updatedAt string
	var approvedAt, observationStartedAt, observationEndsAt, closedAt sql.NullString
	err := row.Scan(&plan.ID, &plan.ActorHash, &plan.Service, &plan.Action, &plan.Target,
		&risk, &state, &plan.Digest, &summaryJSON, &plan.ConfirmationPhrase,
		&plan.ApprovedByHash, &approvedAt, &plan.InvalidatedReason, &plan.TaskID,
		&plan.ObservationSeconds, &observationStartedAt, &observationEndsAt,
		&plan.ClosureReason, &closedAt,
		&createdAt, &updatedAt)
	if err != nil {
		return model.ReleasePlan{}, err
	}
	plan.Risk = model.Risk(risk)
	plan.State = model.PlanState(state)
	plan.RequiresConfirmation = plan.Risk != model.RiskReadOnly
	if err := decodeJSON(summaryJSON, &plan.ApprovalSummary); err != nil {
		return model.ReleasePlan{}, err
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
	plan.ClosedAt, parseErr = nullableTime(closedAt)
	return plan, parseErr
}

func (store *Store) GetReleasePlan(ctx context.Context, id string) (model.ReleasePlan, error) {
	plan, err := scanPlan(store.db.QueryRowContext(ctx, planSelect+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return model.ReleasePlan{}, ErrNotFound
	}
	return plan, err
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
	if plan.ActorHash != actorHash {
		return model.ReleasePlan{}, ErrActorMismatch
	}
	if plan.State != model.PlanPendingApproval || plan.Digest != digest {
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
	result, err := tx.ExecContext(ctx, `
		UPDATE release_plans SET state = ?, approved_by_hash = ?, approved_at = ?, updated_at = ?
		WHERE id = ? AND state = ? AND digest = ?
	`, model.PlanApproved, actorHash, timeText(now), timeText(now), id,
		model.PlanPendingApproval, digest)
	if err = requireOne(result, err, "发布计划批准失败"); err != nil {
		return model.ReleasePlan{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.ReleasePlan{}, err
	}
	plan.State = model.PlanApproved
	plan.ApprovedByHash = actorHash
	plan.ApprovedAt = &now
	plan.UpdatedAt = now
	return plan, nil
}

func (store *Store) InvalidateReleasePlan(ctx context.Context, id, reason string) error {
	result, err := store.db.ExecContext(ctx, `
		UPDATE release_plans SET state = ?, invalidated_reason = ?, approved_by_hash = '',
			approved_at = NULL, updated_at = ? WHERE id = ? AND state IN (?, ?)
	`, model.PlanInvalidated, reason, timeText(store.now()), id,
		model.PlanPendingApproval, model.PlanApproved)
	return requireOne(result, err, "发布计划无法失效")
}

func (store *Store) RecordPlanClosureBlocker(
	ctx context.Context,
	id, reason string,
	audit model.AuditEntry,
) error {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := store.now()
	result, err := tx.ExecContext(ctx, `
		UPDATE release_plans SET closure_reason = ?, updated_at = ?
		WHERE id = ? AND state = ?
	`, reason, timeText(now), id, model.PlanObserving)
	if err = requireOne(result, err, "计划收口阻断原因无法写入"); err != nil {
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
		UPDATE release_plans SET state = ?, closure_reason = '', closure_idempotency_key = ?,
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
) (model.Task, bool, error) {
	requestHash := HashConfirmation(plan.ID + "\x00" + plan.Digest)
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return model.Task{}, false, err
	}
	defer tx.Rollback()
	if task, found, err := taskByIdempotency(ctx, tx, idempotencyKey); err != nil {
		return model.Task{}, false, err
	} else if found {
		if task.ActorHash != actorHash || task.RequestHash != requestHash {
			return model.Task{}, false, ErrIdempotency
		}
		return task, false, nil
	}
	var state, digest, owner string
	if err := tx.QueryRowContext(ctx, `SELECT state, digest, actor_hash FROM release_plans WHERE id = ?`, plan.ID).
		Scan(&state, &digest, &owner); err != nil {
		return model.Task{}, false, err
	}
	if owner != actorHash {
		return model.Task{}, false, ErrActorMismatch
	}
	if model.PlanState(state) != model.PlanApproved || digest != plan.Digest {
		return model.Task{}, false, errors.New("发布计划未批准或已变化")
	}
	var activeID string
	err = tx.QueryRowContext(ctx, `
		SELECT id FROM tasks WHERE service = ? AND state IN (?, ?, ?, ?) LIMIT 1
	`, plan.Service, model.TaskWaitingConfirmation, model.TaskQueued, model.TaskRunning,
		model.TaskRollingBack).Scan(&activeID)
	if err == nil {
		return model.Task{}, false, fmt.Errorf("服务已有活动任务: %s", activeID)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return model.Task{}, false, err
	}
	stages := make([]model.TaskStage, 0, len(plan.ApprovalSummary.Steps))
	for _, step := range plan.ApprovalSummary.Steps {
		stages = append(stages, model.TaskStage{Name: step, State: model.StagePending})
	}
	stagesJSON, err := encodeJSON(stages)
	if err != nil {
		return model.Task{}, false, err
	}
	snapshotJSON, err := encodeJSON(plan.ApprovalSummary.ExpectedBefore)
	if err != nil {
		return model.Task{}, false, err
	}
	now := store.now()
	_, err = tx.ExecContext(ctx, `
		INSERT INTO tasks (
			id, idempotency_key, request_hash, actor_hash, service, action, target, risk,
			state, preview_id, plan_id, plan_digest, snapshot_json, stages_json, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, '', ?, ?, ?, ?, ?)
	`, taskID, idempotencyKey, requestHash, actorHash, plan.Service, plan.Action,
		plan.Target, plan.Risk, model.TaskQueued, plan.ID, plan.Digest, snapshotJSON,
		stagesJSON, timeText(now))
	if err != nil {
		return model.Task{}, false, err
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE release_plans SET state = ?, task_id = ?, updated_at = ?
		WHERE id = ? AND state = ? AND digest = ?
	`, model.PlanExecuting, taskID, timeText(now), plan.ID, model.PlanApproved, plan.Digest)
	if err = requireOne(result, err, "发布计划无法进入执行状态"); err != nil {
		return model.Task{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return model.Task{}, false, err
	}
	return model.Task{
		ID: taskID, IdempotencyKey: idempotencyKey, RequestHash: requestHash,
		ActorHash: actorHash, Service: plan.Service, Action: plan.Action, Target: plan.Target,
		Risk: plan.Risk, State: model.TaskQueued, PlanID: plan.ID, PlanDigest: plan.Digest,
		Snapshot: plan.ApprovalSummary.ExpectedBefore, Stages: stages, CreatedAt: now,
	}, true, nil
}
