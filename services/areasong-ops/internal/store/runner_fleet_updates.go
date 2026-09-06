package store

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

var (
	ErrFleetRunnerUpdateFence     = errors.New("Runner Fleet 更新 assignment fencing 校验失败")
	ErrFleetRunnerUpdateExpired   = errors.New("Runner Fleet 更新 assignment 租约已过期")
	ErrFleetRunnerUpdateCompleted = errors.New("Runner Fleet 更新 assignment 已结束")
)

const fleetRunnerUpdatePlanSelect = `SELECT id,idempotency_key,execution_idempotency_key,
	cancellation_idempotency_key,request_digest,plan_digest,policy_digest,actor_hash,tenant_id,
	manifest_json,artifact_path,artifact_signature,staged_path,target_runner_ids_json,batch_policy_json,
	max_concurrent,change_window_json,rollback_on_failure,state,current_batch,confirmation_hash,
	confirmation_phrase,approved_by_hash,second_approved_by_hash,approval_policy,executed_by_hash,cancelled_by_hash,
	summary,error,created_at,expires_at,approved_at,second_approved_at,started_at,
	observation_started_at,observation_ends_at,finished_at,updated_at FROM runner_fleet_update_plans`

const fleetRunnerUpdateItemSelect = `SELECT id,plan_id,runner_id,server_id,batch_index,state,
	previous_version,previous_revision,previous_digest,expected_lease_generation,
	certificate_fingerprint,assignment_action,assignment_generation,assignment_token_hash,
	assignment_idempotency_key,completion_idempotency_key,observed_version,observed_revision,
	observed_digest,error,rollback_error,claimed_at,last_heartbeat_at,lease_expires_at,
	execution_deadline_at,started_at,finished_at,updated_at FROM runner_fleet_update_items`

type fleetUpdateQuerier interface {
	queryer
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

type storedFleetRunnerUpdateItem struct {
	item      model.FleetRunnerUpdateItem
	tokenHash string
}

func (store *Store) CreateFleetRunnerUpdatePlan(
	ctx context.Context,
	plan model.FleetRunnerUpdatePlan,
) (model.FleetRunnerUpdatePlan, bool, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return model.FleetRunnerUpdatePlan{}, false, err
	}
	defer tx.Rollback()
	if existing, found, err := fleetRunnerUpdatePlanByKey(ctx, tx, plan.IdempotencyKey); err != nil {
		return model.FleetRunnerUpdatePlan{}, false, err
	} else if found {
		if existing.ActorHash != plan.ActorHash || existing.RequestDigest != plan.RequestDigest {
			return model.FleetRunnerUpdatePlan{}, false, ErrIdempotency
		}
		return existing, false, nil
	}
	for _, item := range plan.Items {
		var active int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM runner_fleet_update_items i
			JOIN runner_fleet_update_plans p ON p.id=i.plan_id
			WHERE i.runner_id=? AND p.state IN (
			'pending_approval','pending_second_approval','approved','running','observing','rolling_back'
			)`, item.RunnerID).Scan(&active); err != nil {
			return model.FleetRunnerUpdatePlan{}, false, err
		}
		if active > 0 {
			return model.FleetRunnerUpdatePlan{}, false,
				fmt.Errorf("Runner %s 已有活动 Fleet 更新计划", item.RunnerID)
		}
	}
	if err := insertFleetRunnerUpdatePlan(ctx, tx, plan); err != nil {
		return model.FleetRunnerUpdatePlan{}, false, err
	}
	for _, item := range plan.Items {
		if err := insertFleetRunnerUpdateItem(ctx, tx, item); err != nil {
			return model.FleetRunnerUpdatePlan{}, false, err
		}
	}
	if err := appendFleetRunnerUpdateAudit(ctx, tx, plan.ActorHash,
		"runner.fleet_update.created", string(plan.State), plan, plan.CreatedAt); err != nil {
		return model.FleetRunnerUpdatePlan{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return model.FleetRunnerUpdatePlan{}, false, err
	}
	return plan, true, nil
}

func insertFleetRunnerUpdatePlan(ctx context.Context, tx *sql.Tx, plan model.FleetRunnerUpdatePlan) error {
	manifest, _ := json.Marshal(plan.Manifest)
	targets, _ := json.Marshal(plan.TargetRunnerIDs)
	batch, _ := json.Marshal(plan.BatchPolicy)
	window, _ := json.Marshal(plan.ChangeWindow)
	_, err := tx.ExecContext(ctx, `INSERT INTO runner_fleet_update_plans(
		id,idempotency_key,request_digest,plan_digest,policy_digest,actor_hash,tenant_id,
		manifest_json,artifact_path,artifact_signature,staged_path,target_runner_ids_json,
		batch_policy_json,max_concurrent,change_window_json,rollback_on_failure,state,current_batch,
		confirmation_hash,confirmation_phrase,approval_policy,created_at,expires_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		plan.ID, plan.IdempotencyKey, plan.RequestDigest, plan.PlanDigest, plan.PolicyDigest,
		plan.ActorHash, plan.TenantID, string(manifest), plan.ArtifactPath, plan.ArtifactSignature,
		plan.StagedPath, string(targets), string(batch), plan.MaxConcurrent, string(window), plan.RollbackOnFailure,
		plan.State, plan.CurrentBatch, HashConfirmation(plan.ConfirmationPhrase),
		plan.ConfirmationPhrase, plan.ApprovalPolicy, timeText(plan.CreatedAt), timeText(plan.ExpiresAt), timeText(plan.UpdatedAt))
	return err
}

func insertFleetRunnerUpdateItem(ctx context.Context, tx *sql.Tx, item model.FleetRunnerUpdateItem) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO runner_fleet_update_items(
		id,plan_id,runner_id,server_id,batch_index,state,previous_version,previous_revision,
		previous_digest,expected_lease_generation,certificate_fingerprint,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, item.ID, item.PlanID, item.RunnerID, item.ServerID,
		item.BatchIndex, item.State, item.PreviousVersion, item.PreviousRevision,
		item.PreviousDigest, item.ExpectedLeaseGeneration, item.CertificateFingerprint,
		timeText(item.UpdatedAt))
	return err
}

func (store *Store) GetFleetRunnerUpdatePlan(ctx context.Context, id string) (model.FleetRunnerUpdatePlan, error) {
	plan, found, err := fleetRunnerUpdatePlanByID(ctx, store.db, id)
	if err != nil {
		return model.FleetRunnerUpdatePlan{}, err
	}
	if !found {
		return model.FleetRunnerUpdatePlan{}, ErrNotFound
	}
	plan.Items, err = listFleetRunnerUpdateItems(ctx, store.db, id)
	return plan, err
}

func (store *Store) GetFleetRunnerUpdatePlanByIdempotency(
	ctx context.Context,
	key string,
) (model.FleetRunnerUpdatePlan, bool, error) {
	plan, found, err := fleetRunnerUpdatePlanByKey(ctx, store.db, key)
	if err != nil || !found {
		return plan, found, err
	}
	plan.Items, err = listFleetRunnerUpdateItems(ctx, store.db, plan.ID)
	return plan, err == nil, err
}

func (store *Store) ListFleetRunnerUpdatePlans(
	ctx context.Context,
	tenantID string,
	limit int,
) ([]model.FleetRunnerUpdatePlan, error) {
	rows, err := store.db.QueryContext(ctx, fleetRunnerUpdatePlanSelect+
		` WHERE tenant_id=? ORDER BY created_at DESC LIMIT ?`, tenantID, clampLimit(limit, 100))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	plans := make([]model.FleetRunnerUpdatePlan, 0)
	for rows.Next() {
		plan, _, _, err := scanFleetRunnerUpdatePlan(rows)
		if err != nil {
			return nil, err
		}
		plans = append(plans, plan)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for index := range plans {
		plans[index].Items, err = listFleetRunnerUpdateItems(ctx, store.db, plans[index].ID)
		if err != nil {
			return nil, err
		}
	}
	return plans, nil
}

func (store *Store) ListActiveFleetRunnerUpdatePlanIDs(ctx context.Context) ([]string, error) {
	rows, err := store.db.QueryContext(ctx, `SELECT id FROM runner_fleet_update_plans
		WHERE state IN ('running','observing','rolling_back') ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (store *Store) ExpireFleetRunnerUpdatePlans(ctx context.Context, now time.Time) ([]string, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, fleetRunnerUpdatePlanSelect+
		` WHERE state IN ('pending_approval','pending_second_approval','approved') AND expires_at<=?`, timeText(now))
	if err != nil {
		return nil, err
	}
	var plans []model.FleetRunnerUpdatePlan
	for rows.Next() {
		plan, _, _, scanErr := scanFleetRunnerUpdatePlan(rows)
		if scanErr != nil {
			rows.Close()
			return nil, scanErr
		}
		plans = append(plans, plan)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(plans))
	for _, plan := range plans {
		if err := expireFleetRunnerUpdatePlan(ctx, tx, plan, now); err != nil {
			return nil, err
		}
		paths = append(paths, plan.StagedPath)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return paths, nil
}

func expireFleetRunnerUpdatePlan(ctx context.Context, tx *sql.Tx, plan model.FleetRunnerUpdatePlan, now time.Time) error {
	result, err := tx.ExecContext(ctx, `UPDATE runner_fleet_update_plans
		SET state='expired',finished_at=?,updated_at=? WHERE id=? AND state=?`,
		timeText(now), timeText(now), plan.ID, plan.State)
	if err = requireOne(result, err, "Runner Fleet 更新计划过期状态已变化"); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE runner_fleet_update_items SET state='skipped',updated_at=?
		WHERE plan_id=? AND state IN ('pending','ready')`, timeText(now), plan.ID)
	if err != nil {
		return err
	}
	plan.State, plan.FinishedAt, plan.UpdatedAt = model.FleetRunnerUpdateExpired, &now, now
	return appendFleetRunnerUpdateAudit(ctx, tx, "system", "runner.fleet_update.expired", "expired", plan, now)
}

func (store *Store) ApproveFleetRunnerUpdatePlan(
	ctx context.Context,
	id, actor, digest, confirmation string,
) (model.FleetRunnerUpdatePlan, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return model.FleetRunnerUpdatePlan{}, err
	}
	defer tx.Rollback()
	plan, found, confirmationHash, err := fleetRunnerUpdatePlanForTransition(ctx, tx, id)
	if err != nil || !found {
		return model.FleetRunnerUpdatePlan{}, transitionNotFound(err)
	}
	if err := validateFleetRunnerUpdateApproval(plan, actor, digest, confirmation, confirmationHash, store.now()); err != nil {
		return model.FleetRunnerUpdatePlan{}, err
	}
	if plan.State == model.FleetRunnerUpdateApproved && model.UsesTwoPartyApproval(plan.ApprovalPolicy) && plan.ApprovedByHash == actor {
		if err := tx.Commit(); err != nil {
			return model.FleetRunnerUpdatePlan{}, err
		}
		return plan, nil
	}
	now := store.now()
	if plan.State == model.FleetRunnerUpdatePendingApproval {
		err = approveFirstFleetRunnerUpdate(ctx, tx, &plan, actor, now)
	} else {
		err = approveSecondFleetRunnerUpdate(ctx, tx, &plan, actor, now)
	}
	if err != nil {
		return model.FleetRunnerUpdatePlan{}, err
	}
	if err := appendFleetRunnerUpdateAudit(ctx, tx, actor, "runner.fleet_update.approved", string(plan.State), plan, now); err != nil {
		return model.FleetRunnerUpdatePlan{}, err
	}
	return plan, tx.Commit()
}

func validateFleetRunnerUpdateApproval(
	plan model.FleetRunnerUpdatePlan,
	actor, digest, confirmation, confirmationHash string,
	now time.Time,
) error {
	idempotentRetry := plan.State == model.FleetRunnerUpdateApproved &&
		model.UsesTwoPartyApproval(plan.ApprovalPolicy) && plan.ApprovedByHash == actor
	if !idempotentRetry && plan.State != model.FleetRunnerUpdatePendingApproval && plan.State != model.FleetRunnerUpdatePendingSecondApproval {
		return errors.New("Runner Fleet 更新计划不在待批准状态")
	}
	if !now.Before(plan.ExpiresAt) {
		return errors.New("Runner Fleet 更新计划已过期")
	}
	if !idempotentRetry && (actor == plan.ActorHash || (plan.ApprovedByHash != "" && actor == plan.ApprovedByHash)) {
		return errors.New("Runner Fleet 更新创建人和批准人必须相互独立")
	}
	if subtle.ConstantTimeCompare([]byte(plan.PlanDigest), []byte(digest)) != 1 {
		return errors.New("Runner Fleet 更新计划摘要已变化")
	}
	if subtle.ConstantTimeCompare([]byte(confirmationHash), []byte(HashConfirmation(confirmation))) != 1 {
		return ErrConfirmation
	}
	return nil
}

func approveFirstFleetRunnerUpdate(ctx context.Context, tx *sql.Tx, plan *model.FleetRunnerUpdatePlan, actor string, now time.Time) error {
	if model.UsesTwoPartyApproval(plan.ApprovalPolicy) {
		result, err := tx.ExecContext(ctx, `UPDATE runner_fleet_update_plans
			SET state='approved',approved_by_hash=?,approved_at=?,updated_at=?
			WHERE id=? AND state='pending_approval'`, actor, timeText(now), timeText(now), plan.ID)
		if err = requireOne(result, err, "Runner Fleet 更新独立批准状态已变化"); err != nil {
			return err
		}
		plan.State, plan.ApprovedByHash, plan.ApprovedAt, plan.UpdatedAt = model.FleetRunnerUpdateApproved, actor, &now, now
		return nil
	}
	result, err := tx.ExecContext(ctx, `UPDATE runner_fleet_update_plans
		SET state='pending_second_approval',approved_by_hash=?,approved_at=?,updated_at=?
		WHERE id=? AND state='pending_approval'`, actor, timeText(now), timeText(now), plan.ID)
	if err = requireOne(result, err, "Runner Fleet 更新首次批准状态已变化"); err != nil {
		return err
	}
	plan.State, plan.ApprovedByHash, plan.ApprovedAt, plan.UpdatedAt =
		model.FleetRunnerUpdatePendingSecondApproval, actor, &now, now
	return nil
}

func approveSecondFleetRunnerUpdate(ctx context.Context, tx *sql.Tx, plan *model.FleetRunnerUpdatePlan, actor string, now time.Time) error {
	result, err := tx.ExecContext(ctx, `UPDATE runner_fleet_update_plans
		SET state='approved',second_approved_by_hash=?,second_approved_at=?,updated_at=?
		WHERE id=? AND state='pending_second_approval' AND approved_by_hash<>?`,
		actor, timeText(now), timeText(now), plan.ID, actor)
	if err = requireOne(result, err, "Runner Fleet 更新第二次批准状态已变化"); err != nil {
		return err
	}
	plan.State, plan.SecondApprovedByHash, plan.SecondApprovedAt, plan.UpdatedAt =
		model.FleetRunnerUpdateApproved, actor, &now, now
	return nil
}

func (store *Store) StartFleetRunnerUpdatePlan(
	ctx context.Context,
	id, actor, executionKey string,
) (model.FleetRunnerUpdatePlan, bool, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return model.FleetRunnerUpdatePlan{}, false, err
	}
	defer tx.Rollback()
	plan, found, err := fleetRunnerUpdatePlanByID(ctx, tx, id)
	if err != nil || !found {
		return model.FleetRunnerUpdatePlan{}, false, transitionNotFound(err)
	}
	if plan.ExecutionIdempotencyKey != "" {
		if plan.ExecutionIdempotencyKey != executionKey || plan.ExecutedByHash != actor {
			return model.FleetRunnerUpdatePlan{}, false, ErrIdempotency
		}
		plan.Items, err = listFleetRunnerUpdateItems(ctx, tx, id)
		return plan, false, err
	}
	if err := validateFleetRunnerUpdateExecution(plan, actor, store.now()); err != nil {
		return model.FleetRunnerUpdatePlan{}, false, err
	}
	now := store.now()
	if err := startFleetRunnerUpdateTx(ctx, tx, &plan, actor, executionKey, now); err != nil {
		return model.FleetRunnerUpdatePlan{}, false, err
	}
	if err := appendFleetRunnerUpdateAudit(ctx, tx, actor, "runner.fleet_update.started", "running", plan, now); err != nil {
		return model.FleetRunnerUpdatePlan{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return model.FleetRunnerUpdatePlan{}, false, err
	}
	plan.Items, err = store.listFleetRunnerUpdateItems(ctx, id)
	return plan, true, err
}

func validateFleetRunnerUpdateExecution(plan model.FleetRunnerUpdatePlan, actor string, now time.Time) error {
	if plan.State != model.FleetRunnerUpdateApproved || plan.ApprovedByHash == "" {
		return errors.New("Runner Fleet 更新尚未完成独立批准")
	}
	if model.UsesTwoPartyApproval(plan.ApprovalPolicy) {
		if actor != plan.ActorHash || actor == plan.ApprovedByHash {
			return errors.New("Runner Fleet 更新执行必须由创建人完成，且批准人必须独立")
		}
	} else {
		if plan.SecondApprovedByHash == "" || plan.ApprovedByHash == plan.SecondApprovedByHash {
			return errors.New("Runner Fleet 更新尚未完成两名独立批准")
		}
		if actor == plan.ActorHash || actor == plan.ApprovedByHash || actor == plan.SecondApprovedByHash {
			return errors.New("Runner Fleet 更新执行人必须独立于创建人和两名批准人")
		}
	}
	if !now.Before(plan.ExpiresAt) || plan.ChangeWindow == nil || !plan.ChangeWindow.Contains(now) {
		return errors.New("Runner Fleet 更新不在有效计划与变更窗口内")
	}
	return nil
}

func startFleetRunnerUpdateTx(
	ctx context.Context,
	tx *sql.Tx,
	plan *model.FleetRunnerUpdatePlan,
	actor, executionKey string,
	now time.Time,
) error {
	result, err := tx.ExecContext(ctx, `UPDATE runner_fleet_update_plans SET state='running',
		current_batch=0,executed_by_hash=?,execution_idempotency_key=?,started_at=?,updated_at=?
		WHERE id=? AND state='approved'`, actor, executionKey, timeText(now), timeText(now), plan.ID)
	if err = requireOne(result, err, "Runner Fleet 更新执行状态已变化"); err != nil {
		return err
	}
	result, err = tx.ExecContext(ctx, `UPDATE runner_fleet_update_items SET state='ready',updated_at=?
		WHERE plan_id=? AND batch_index=0 AND state='pending'`, timeText(now), plan.ID)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected < 1 {
		return errors.New("Runner Fleet 更新没有首批目标")
	}
	plan.State, plan.CurrentBatch, plan.ExecutedByHash = model.FleetRunnerUpdateRunning, 0, actor
	plan.ExecutionIdempotencyKey, plan.StartedAt, plan.UpdatedAt = executionKey, &now, now
	return nil
}

func (store *Store) CancelFleetRunnerUpdatePlan(
	ctx context.Context,
	id, actor, idempotencyKey, confirmation string,
) (model.FleetRunnerUpdatePlan, bool, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return model.FleetRunnerUpdatePlan{}, false, err
	}
	defer tx.Rollback()
	plan, found, err := fleetRunnerUpdatePlanByID(ctx, tx, id)
	if err != nil || !found {
		return model.FleetRunnerUpdatePlan{}, false, transitionNotFound(err)
	}
	if plan.CancellationIdempotencyKey != "" {
		if plan.CancellationIdempotencyKey != idempotencyKey || plan.CancelledByHash != actor {
			return model.FleetRunnerUpdatePlan{}, false, ErrIdempotency
		}
		return plan, false, nil
	}
	if plan.State != model.FleetRunnerUpdatePendingApproval &&
		plan.State != model.FleetRunnerUpdatePendingSecondApproval && plan.State != model.FleetRunnerUpdateApproved {
		return model.FleetRunnerUpdatePlan{}, false, errors.New("Runner Fleet 更新当前不能取消")
	}
	if confirmation != "取消 Runner Fleet 更新 "+id {
		return model.FleetRunnerUpdatePlan{}, false, ErrConfirmation
	}
	now := store.now()
	if err := cancelFleetRunnerUpdateTx(ctx, tx, &plan, actor, idempotencyKey, now); err != nil {
		return model.FleetRunnerUpdatePlan{}, false, err
	}
	if err := appendFleetRunnerUpdateAudit(ctx, tx, actor, "runner.fleet_update.cancelled", "cancelled", plan, now); err != nil {
		return model.FleetRunnerUpdatePlan{}, false, err
	}
	return plan, true, tx.Commit()
}

func cancelFleetRunnerUpdateTx(ctx context.Context, tx *sql.Tx, plan *model.FleetRunnerUpdatePlan, actor, key string, now time.Time) error {
	result, err := tx.ExecContext(ctx, `UPDATE runner_fleet_update_plans SET state='cancelled',
		cancelled_by_hash=?,cancellation_idempotency_key=?,finished_at=?,updated_at=?
		WHERE id=? AND state IN ('pending_approval','pending_second_approval','approved')`,
		actor, key, timeText(now), timeText(now), plan.ID)
	if err = requireOne(result, err, "Runner Fleet 更新取消状态已变化"); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE runner_fleet_update_items SET state='skipped',updated_at=?
		WHERE plan_id=? AND state='pending'`, timeText(now), plan.ID)
	plan.State, plan.CancelledByHash, plan.CancellationIdempotencyKey = model.FleetRunnerUpdateCancelled, actor, key
	plan.FinishedAt, plan.UpdatedAt = &now, now
	return err
}

func (store *Store) BeginFleetRunnerUpdateObservation(
	ctx context.Context,
	id string,
	started, ends time.Time,
) error {
	result, err := store.db.ExecContext(ctx, `UPDATE runner_fleet_update_plans SET state='observing',
		observation_started_at=?,observation_ends_at=?,updated_at=? WHERE id=? AND state='running'`,
		timeText(started), timeText(ends), timeText(started), id)
	return requireOne(result, err, "Runner Fleet Canary 观察状态已变化")
}

func (store *Store) ReleaseFleetRunnerUpdateBatch(ctx context.Context, id string, batch int, now time.Time) error {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE runner_fleet_update_plans SET state='running',
		current_batch=?,observation_started_at=NULL,observation_ends_at=NULL,updated_at=?
		WHERE id=? AND state IN ('running','observing')`, batch, timeText(now), id)
	if err = requireOne(result, err, "Runner Fleet 更新批次状态已变化"); err != nil {
		return err
	}
	result, err = tx.ExecContext(ctx, `UPDATE runner_fleet_update_items SET state='ready',updated_at=?
		WHERE plan_id=? AND batch_index=? AND state='pending'`, timeText(now), id, batch)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected < 1 {
		return errors.New("Runner Fleet 更新下一批没有待处理节点")
	}
	return tx.Commit()
}

func (store *Store) BeginFleetRunnerUpdateRollback(ctx context.Context, id, reason string) error {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := store.now()
	plan, found, err := fleetRunnerUpdatePlanByID(ctx, tx, id)
	if err != nil || !found {
		return transitionNotFound(err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE runner_fleet_update_plans SET state='rolling_back',
		summary='节点失败，已停止后续批次并开始逐节点回滚',error=?,updated_at=?
		WHERE id=? AND state IN ('running','observing')`, reason, timeText(now), id)
	if err = requireOne(result, err, "Runner Fleet 更新回滚状态已变化"); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE runner_fleet_update_items SET state='rollback_ready',
		assignment_action='',assignment_token_hash='',assignment_idempotency_key='',
		completion_idempotency_key='',lease_expires_at=NULL,finished_at=NULL,updated_at=?
		WHERE plan_id=? AND state='succeeded'`, timeText(now), id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE runner_fleet_update_items SET state='skipped',updated_at=?
		WHERE plan_id=? AND state IN ('pending','ready')`, timeText(now), id); err != nil {
		return err
	}
	plan.State, plan.Error, plan.UpdatedAt = model.FleetRunnerUpdateRollingBack, reason, now
	if err := appendFleetRunnerUpdateAudit(ctx, tx, "system", "runner.fleet_update.rollback_started", "rolling_back", plan, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (store *Store) FinishFleetRunnerUpdatePlan(
	ctx context.Context,
	id string,
	state model.FleetRunnerUpdatePlanState,
	summary, errorText string,
) error {
	if state != model.FleetRunnerUpdateSucceeded && state != model.FleetRunnerUpdateRolledBack &&
		state != model.FleetRunnerUpdateNeedsAttention {
		return errors.New("Runner Fleet 更新终态无效")
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	plan, found, err := fleetRunnerUpdatePlanByID(ctx, tx, id)
	if err != nil || !found {
		return transitionNotFound(err)
	}
	now := store.now()
	result, err := tx.ExecContext(ctx, `UPDATE runner_fleet_update_plans SET state=?,summary=?,error=?,
		finished_at=?,updated_at=? WHERE id=? AND state IN ('running','observing','rolling_back')`,
		state, summary, errorText, timeText(now), timeText(now), id)
	if err = requireOne(result, err, "Runner Fleet 更新终态已变化"); err != nil {
		return err
	}
	plan.State, plan.Summary, plan.Error, plan.FinishedAt, plan.UpdatedAt = state, summary, errorText, &now, now
	if err := appendFleetRunnerUpdateAudit(ctx, tx, "system", "runner.fleet_update.finished", string(state), plan, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (store *Store) ClaimFleetRunnerUpdate(
	ctx context.Context,
	runnerID string,
	lease time.Duration,
) (model.FleetRunnerUpdateAssignment, bool, error) {
	if lease < 30*time.Second || lease > 15*time.Minute {
		return model.FleetRunnerUpdateAssignment{}, false, errors.New("Runner Fleet 更新领取租约无效")
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return model.FleetRunnerUpdateAssignment{}, false, err
	}
	defer tx.Rollback()
	item, plan, found, err := claimableFleetRunnerUpdate(ctx, tx, runnerID, store.now())
	if err != nil || !found {
		return model.FleetRunnerUpdateAssignment{}, false, err
	}
	assignment, err := claimFleetRunnerUpdateTx(ctx, tx, &item, plan, lease, store.now())
	if err != nil {
		return model.FleetRunnerUpdateAssignment{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return model.FleetRunnerUpdateAssignment{}, false, err
	}
	return assignment, true, nil
}

func claimableFleetRunnerUpdate(
	ctx context.Context,
	tx *sql.Tx,
	runnerID string,
	now time.Time,
) (storedFleetRunnerUpdateItem, model.FleetRunnerUpdatePlan, bool, error) {
	row := tx.QueryRowContext(ctx, fleetRunnerUpdateItemSelect+` WHERE runner_id=?
		AND state IN ('ready','rollback_ready')
		ORDER BY CASE WHEN state='rollback_ready' THEN 0 ELSE 1 END,
		batch_index DESC,updated_at DESC LIMIT 1`, runnerID)
	item, found, err := scanFleetRunnerUpdateItem(row)
	if err != nil || !found {
		return storedFleetRunnerUpdateItem{}, model.FleetRunnerUpdatePlan{}, found, err
	}
	plan, found, err := fleetRunnerUpdatePlanByID(ctx, tx, item.item.PlanID)
	if err != nil || !found {
		return storedFleetRunnerUpdateItem{}, model.FleetRunnerUpdatePlan{}, false, transitionNotFound(err)
	}
	if item.item.State == model.FleetRunnerUpdateItemReady &&
		(plan.ChangeWindow == nil || !plan.ChangeWindow.Contains(now)) {
		return storedFleetRunnerUpdateItem{}, model.FleetRunnerUpdatePlan{}, false, nil
	}
	expectedState := model.FleetRunnerUpdateRunning
	if item.item.State == model.FleetRunnerUpdateItemRollbackReady {
		expectedState = model.FleetRunnerUpdateRollingBack
	}
	if plan.State != expectedState {
		return storedFleetRunnerUpdateItem{}, model.FleetRunnerUpdatePlan{}, false, nil
	}
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM runner_fleet_update_items
		WHERE plan_id=? AND state IN ('running','rolling_back')`, plan.ID).Scan(&active); err != nil {
		return storedFleetRunnerUpdateItem{}, model.FleetRunnerUpdatePlan{}, false, err
	}
	if item.item.State == model.FleetRunnerUpdateItemRollbackReady {
		return item, plan, active == 0, nil
	}
	return item, plan, active < plan.MaxConcurrent, nil
}

func claimFleetRunnerUpdateTx(
	ctx context.Context,
	tx *sql.Tx,
	item *storedFleetRunnerUpdateItem,
	plan model.FleetRunnerUpdatePlan,
	lease time.Duration,
	now time.Time,
) (model.FleetRunnerUpdateAssignment, error) {
	token, err := newLeaseToken()
	if err != nil {
		return model.FleetRunnerUpdateAssignment{}, err
	}
	action, nextState := "update", model.FleetRunnerUpdateItemRunning
	if item.item.State == model.FleetRunnerUpdateItemRollbackReady {
		action, nextState = "rollback", model.FleetRunnerUpdateItemRollingBack
	}
	deadline := now.Add(15 * time.Minute)
	if action == "update" && plan.ChangeWindow != nil && plan.ChangeWindow.EndAt.Before(deadline) {
		deadline = plan.ChangeWindow.EndAt
	}
	leaseExpires := now.Add(lease)
	if deadline.Before(leaseExpires) {
		leaseExpires = deadline
	}
	generation := item.item.AssignmentGeneration + 1
	assignmentKey := HashConfirmation(fmt.Sprintf("%s\x00%d\x00%s", item.item.ID, generation, token))
	result, err := tx.ExecContext(ctx, `UPDATE runner_fleet_update_items SET state=?,assignment_action=?,
		assignment_generation=?,assignment_token_hash=?,assignment_idempotency_key=?,claimed_at=?,
		last_heartbeat_at=?,lease_expires_at=?,execution_deadline_at=?,started_at=COALESCE(started_at,?),updated_at=?
		WHERE id=? AND state=? AND assignment_generation=?`, nextState, action, generation,
		HashConfirmation(token), assignmentKey, timeText(now), timeText(now), timeText(leaseExpires),
		timeText(deadline), timeText(now), timeText(now), item.item.ID, item.item.State,
		item.item.AssignmentGeneration)
	if err = requireOne(result, err, "Runner Fleet 更新领取状态已变化"); err != nil {
		return model.FleetRunnerUpdateAssignment{}, err
	}
	item.item.State, item.item.AssignmentAction = nextState, action
	item.item.AssignmentGeneration, item.item.AssignmentToken = generation, token
	item.item.AssignmentIdempotencyKey = assignmentKey
	item.item.ClaimedAt, item.item.LastHeartbeatAt, item.item.LeaseExpiresAt = &now, &now, &leaseExpires
	item.item.ExecutionDeadlineAt, item.item.StartedAt, item.item.UpdatedAt = &deadline, &now, now
	if err := appendFleetRunnerUpdateAudit(ctx, tx, item.item.RunnerID,
		"runner.fleet_update.claimed", action, plan, now); err != nil {
		return model.FleetRunnerUpdateAssignment{}, err
	}
	return fleetRunnerUpdateAssignment(plan, item.item), nil
}

func (store *Store) HeartbeatFleetRunnerUpdate(
	ctx context.Context,
	runnerID, itemID string,
	fence model.FleetRunnerUpdateFence,
	lease time.Duration,
) (model.FleetRunnerUpdateItem, error) {
	if lease < 30*time.Second || lease > 15*time.Minute {
		return model.FleetRunnerUpdateItem{}, errors.New("Runner Fleet 更新心跳租约无效")
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return model.FleetRunnerUpdateItem{}, err
	}
	defer tx.Rollback()
	stored, err := fleetRunnerUpdateItemForFence(ctx, tx, runnerID, itemID, fence, store.now())
	if err != nil {
		return model.FleetRunnerUpdateItem{}, err
	}
	now := store.now()
	expires := now.Add(lease)
	if stored.item.ExecutionDeadlineAt != nil && stored.item.ExecutionDeadlineAt.Before(expires) {
		expires = *stored.item.ExecutionDeadlineAt
	}
	result, err := tx.ExecContext(ctx, `UPDATE runner_fleet_update_items SET last_heartbeat_at=?,
		lease_expires_at=?,updated_at=? WHERE id=? AND assignment_generation=? AND assignment_token_hash=?`,
		timeText(now), timeText(expires), timeText(now), itemID, fence.Generation, HashConfirmation(fence.ClaimToken))
	if err = requireOne(result, err, "Runner Fleet 更新心跳 fencing 失败"); err != nil {
		return model.FleetRunnerUpdateItem{}, err
	}
	stored.item.LastHeartbeatAt, stored.item.LeaseExpiresAt, stored.item.UpdatedAt = &now, &expires, now
	return stored.item, tx.Commit()
}

func (store *Store) CompleteFleetRunnerUpdate(
	ctx context.Context,
	runnerID, itemID string,
	input model.FleetRunnerUpdateCompletionRequest,
) (model.FleetRunnerUpdateItem, bool, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return model.FleetRunnerUpdateItem{}, false, err
	}
	defer tx.Rollback()
	stored, found, err := fleetRunnerUpdateItemByID(ctx, tx, itemID)
	if err != nil || !found {
		return model.FleetRunnerUpdateItem{}, false, transitionNotFound(err)
	}
	if stored.item.CompletionIdempotencyKey != "" {
		if err := validateFleetRunnerUpdateCompletionReplay(stored, runnerID, input); err != nil {
			return model.FleetRunnerUpdateItem{}, false, err
		}
		return stored.item, false, nil
	}
	if _, err := fleetRunnerUpdateItemForFence(ctx, tx, runnerID, itemID,
		input.FleetRunnerUpdateFence, store.now()); err != nil {
		return model.FleetRunnerUpdateItem{}, false, err
	}
	plan, planFound, err := fleetRunnerUpdatePlanByID(ctx, tx, stored.item.PlanID)
	if err != nil || !planFound {
		return model.FleetRunnerUpdateItem{}, false, transitionNotFound(err)
	}
	nextState, err := fleetRunnerCompletionState(stored.item.AssignmentAction, input.State)
	if err != nil {
		return model.FleetRunnerUpdateItem{}, false, err
	}
	if stored.item.AssignmentAction == "update" && nextState == model.FleetRunnerUpdateItemSucceeded &&
		plan.State == model.FleetRunnerUpdateRollingBack {
		nextState = model.FleetRunnerUpdateItemRollbackReady
	}
	now := store.now()
	var finishedAt any = timeText(now)
	if nextState == model.FleetRunnerUpdateItemRollbackReady {
		finishedAt = nil
	}
	result, err := tx.ExecContext(ctx, `UPDATE runner_fleet_update_items SET state=?,
		completion_idempotency_key=?,observed_version=?,observed_revision=?,observed_digest=?,
		error=CASE WHEN assignment_action='update' THEN ? ELSE error END,
		rollback_error=CASE WHEN assignment_action='rollback' THEN ? ELSE rollback_error END,
		last_heartbeat_at=NULL,lease_expires_at=NULL,finished_at=?,updated_at=?
		WHERE id=? AND runner_id=? AND assignment_generation=? AND assignment_token_hash=?`,
		nextState, input.IdempotencyKey, input.ObservedVersion, input.ObservedRevision,
		input.ObservedDigest, input.Error, input.Error, finishedAt, timeText(now), itemID,
		runnerID, input.Generation, HashConfirmation(input.ClaimToken))
	if err = requireOne(result, err, "Runner Fleet 更新完成状态 fencing 失败"); err != nil {
		return model.FleetRunnerUpdateItem{}, false, err
	}
	stored.item.State, stored.item.CompletionIdempotencyKey = nextState, input.IdempotencyKey
	stored.item.ObservedVersion, stored.item.ObservedRevision = input.ObservedVersion, input.ObservedRevision
	stored.item.ObservedDigest, stored.item.UpdatedAt = input.ObservedDigest, now
	if nextState == model.FleetRunnerUpdateItemRollbackReady {
		stored.item.FinishedAt = nil
	} else {
		stored.item.FinishedAt = &now
	}
	if stored.item.AssignmentAction == "rollback" {
		stored.item.RollbackError = input.Error
	} else {
		stored.item.Error = input.Error
	}
	if err := appendFleetRunnerUpdateAudit(ctx, tx, runnerID,
		"runner.fleet_update.item_completed", string(nextState), plan, now); err != nil {
		return model.FleetRunnerUpdateItem{}, false, err
	}
	return stored.item, true, tx.Commit()
}

func (store *Store) FleetRunnerUpdateCompletionReplay(
	ctx context.Context,
	runnerID, itemID string,
	input model.FleetRunnerUpdateCompletionRequest,
) (model.FleetRunnerUpdateItem, bool, error) {
	stored, found, err := fleetRunnerUpdateItemByID(ctx, store.db, itemID)
	if err != nil || !found {
		return model.FleetRunnerUpdateItem{}, false, transitionNotFound(err)
	}
	if stored.item.CompletionIdempotencyKey == "" {
		return stored.item, false, nil
	}
	if err := validateFleetRunnerUpdateCompletionReplay(stored, runnerID, input); err != nil {
		return model.FleetRunnerUpdateItem{}, false, err
	}
	return stored.item, true, nil
}

func validateFleetRunnerUpdateCompletionReplay(
	stored storedFleetRunnerUpdateItem,
	runnerID string,
	input model.FleetRunnerUpdateCompletionRequest,
) error {
	item := stored.item
	if item.RunnerID != runnerID || item.AssignmentGeneration != input.Generation ||
		input.ClaimToken == "" || subtle.ConstantTimeCompare([]byte(stored.tokenHash),
		[]byte(HashConfirmation(input.ClaimToken))) != 1 {
		return ErrFleetRunnerUpdateFence
	}
	if item.CompletionIdempotencyKey != input.IdempotencyKey {
		return ErrIdempotency
	}
	expectedState, err := fleetRunnerCompletionState(item.AssignmentAction, input.State)
	if err != nil {
		return err
	}
	storedError := item.Error
	if item.AssignmentAction == "rollback" {
		storedError = item.RollbackError
	}
	if item.State != expectedState || item.ObservedVersion != input.ObservedVersion ||
		item.ObservedRevision != input.ObservedRevision || item.ObservedDigest != input.ObservedDigest ||
		storedError != input.Error {
		return ErrIdempotency
	}
	return nil
}

func fleetRunnerCompletionState(action, state string) (model.FleetRunnerUpdateItemState, error) {
	if action == "update" {
		switch state {
		case "succeeded":
			return model.FleetRunnerUpdateItemSucceeded, nil
		case "failed":
			return model.FleetRunnerUpdateItemFailed, nil
		case "rolled_back":
			return model.FleetRunnerUpdateItemRolledBack, nil
		case "needs_attention":
			return model.FleetRunnerUpdateItemNeedsAttention, nil
		}
	}
	if action == "rollback" {
		switch state {
		case "rolled_back":
			return model.FleetRunnerUpdateItemRolledBack, nil
		case "needs_attention":
			return model.FleetRunnerUpdateItemNeedsAttention, nil
		}
	}
	return "", errors.New("Runner Fleet 更新完成状态无效")
}

func (store *Store) FleetRunnerUpdateAssignmentForArtifact(
	ctx context.Context,
	runnerID, itemID string,
	fence model.FleetRunnerUpdateFence,
) (model.FleetRunnerUpdateAssignment, error) {
	stored, err := fleetRunnerUpdateItemForFence(ctx, store.db, runnerID, itemID, fence, store.now())
	if err != nil {
		return model.FleetRunnerUpdateAssignment{}, err
	}
	plan, found, err := fleetRunnerUpdatePlanByID(ctx, store.db, stored.item.PlanID)
	if err != nil || !found {
		return model.FleetRunnerUpdateAssignment{}, transitionNotFound(err)
	}
	return fleetRunnerUpdateAssignment(plan, stored.item), nil
}

func (store *Store) RecoverFleetRunnerUpdateLeases(ctx context.Context, now time.Time) (int64, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE runner_fleet_update_items
		SET state='needs_attention',error='Runner Fleet 更新执行租约已过期，节点身份需要人工核对',
		assignment_token_hash='',lease_expires_at=NULL,finished_at=?,updated_at=?
		WHERE state IN ('running','rolling_back') AND
		(lease_expires_at<=? OR execution_deadline_at<=?)`,
		timeText(now), timeText(now), timeText(now), timeText(now))
	if err != nil {
		return 0, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return count, nil
}

func fleetRunnerUpdateAssignment(plan model.FleetRunnerUpdatePlan, item model.FleetRunnerUpdateItem) model.FleetRunnerUpdateAssignment {
	deadline := time.Time{}
	if item.ExecutionDeadlineAt != nil {
		deadline = *item.ExecutionDeadlineAt
	}
	return model.FleetRunnerUpdateAssignment{
		PlanID: plan.ID, ItemID: item.ID, RunnerID: item.RunnerID, ServerID: item.ServerID,
		Action: item.AssignmentAction, Manifest: plan.Manifest,
		ArtifactSignature: plan.ArtifactSignature, PreviousVersion: item.PreviousVersion,
		PreviousRevision: item.PreviousRevision, PreviousDigest: item.PreviousDigest,
		PolicyDigest: plan.PolicyDigest, PlanDigest: plan.PlanDigest,
		ExecutionDeadlineAt: deadline,
		Fence:               model.FleetRunnerUpdateFence{Generation: item.AssignmentGeneration, ClaimToken: item.AssignmentToken},
	}
}

func fleetRunnerUpdateItemForFence(
	ctx context.Context,
	db queryer,
	runnerID, itemID string,
	fence model.FleetRunnerUpdateFence,
	now time.Time,
) (storedFleetRunnerUpdateItem, error) {
	stored, found, err := fleetRunnerUpdateItemByID(ctx, db, itemID)
	if err != nil || !found {
		return storedFleetRunnerUpdateItem{}, transitionNotFound(err)
	}
	item := stored.item
	if item.RunnerID != runnerID || item.AssignmentGeneration != fence.Generation ||
		fence.ClaimToken == "" || subtle.ConstantTimeCompare([]byte(stored.tokenHash),
		[]byte(HashConfirmation(fence.ClaimToken))) != 1 {
		return storedFleetRunnerUpdateItem{}, ErrFleetRunnerUpdateFence
	}
	if item.State != model.FleetRunnerUpdateItemRunning && item.State != model.FleetRunnerUpdateItemRollingBack {
		return storedFleetRunnerUpdateItem{}, ErrFleetRunnerUpdateCompleted
	}
	if item.LeaseExpiresAt == nil || !item.LeaseExpiresAt.After(now) ||
		item.ExecutionDeadlineAt == nil || !item.ExecutionDeadlineAt.After(now) {
		return storedFleetRunnerUpdateItem{}, ErrFleetRunnerUpdateExpired
	}
	return stored, nil
}

func (store *Store) listFleetRunnerUpdateItems(ctx context.Context, id string) ([]model.FleetRunnerUpdateItem, error) {
	return listFleetRunnerUpdateItems(ctx, store.db, id)
}

func listFleetRunnerUpdateItems(ctx context.Context, db fleetUpdateQuerier, id string) ([]model.FleetRunnerUpdateItem, error) {
	rows, err := db.QueryContext(ctx, fleetRunnerUpdateItemSelect+` WHERE plan_id=? ORDER BY batch_index,runner_id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]model.FleetRunnerUpdateItem, 0)
	for rows.Next() {
		stored, _, err := scanFleetRunnerUpdateItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, stored.item)
	}
	return items, rows.Err()
}

func fleetRunnerUpdatePlanByID(ctx context.Context, db queryer, id string) (model.FleetRunnerUpdatePlan, bool, error) {
	plan, _, found, err := fleetRunnerUpdatePlanQuery(ctx, db, fleetRunnerUpdatePlanSelect+` WHERE id=?`, id)
	return plan, found, err
}

func fleetRunnerUpdatePlanByKey(ctx context.Context, db queryer, key string) (model.FleetRunnerUpdatePlan, bool, error) {
	plan, _, found, err := fleetRunnerUpdatePlanQuery(ctx, db, fleetRunnerUpdatePlanSelect+` WHERE idempotency_key=?`, key)
	return plan, found, err
}

func fleetRunnerUpdatePlanForTransition(ctx context.Context, db queryer, id string) (model.FleetRunnerUpdatePlan, bool, string, error) {
	plan, confirmationHash, found, err := fleetRunnerUpdatePlanQuery(
		ctx, db, fleetRunnerUpdatePlanSelect+` WHERE id=?`, id,
	)
	return plan, found, confirmationHash, err
}

func fleetRunnerUpdatePlanQuery(
	ctx context.Context,
	db queryer,
	query, value string,
) (model.FleetRunnerUpdatePlan, string, bool, error) {
	return scanFleetRunnerUpdatePlan(db.QueryRowContext(ctx, query, value))
}

func scanFleetRunnerUpdatePlan(scanner interface{ Scan(...any) error }) (model.FleetRunnerUpdatePlan, string, bool, error) {
	var plan model.FleetRunnerUpdatePlan
	var manifestJSON, targetsJSON, batchJSON, windowJSON, state, confirmationHash string
	var rollback bool
	var created, expires, updated string
	var approved, secondApproved, started, observationStarted, observationEnds, finished sql.NullString
	err := scanner.Scan(&plan.ID, &plan.IdempotencyKey, &plan.ExecutionIdempotencyKey,
		&plan.CancellationIdempotencyKey, &plan.RequestDigest, &plan.PlanDigest, &plan.PolicyDigest,
		&plan.ActorHash, &plan.TenantID, &manifestJSON, &plan.ArtifactPath, &plan.ArtifactSignature,
		&plan.StagedPath, &targetsJSON, &batchJSON, &plan.MaxConcurrent, &windowJSON, &rollback,
		&state, &plan.CurrentBatch, &confirmationHash, &plan.ConfirmationPhrase, &plan.ApprovedByHash,
		&plan.SecondApprovedByHash, &plan.ApprovalPolicy, &plan.ExecutedByHash, &plan.CancelledByHash, &plan.Summary,
		&plan.Error, &created, &expires, &approved, &secondApproved, &started, &observationStarted,
		&observationEnds, &finished, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return model.FleetRunnerUpdatePlan{}, "", false, nil
	}
	if err != nil {
		return model.FleetRunnerUpdatePlan{}, "", false, err
	}
	plan.State, plan.RollbackOnFailure = model.FleetRunnerUpdatePlanState(state), rollback
	if err = json.Unmarshal([]byte(manifestJSON), &plan.Manifest); err == nil {
		err = json.Unmarshal([]byte(targetsJSON), &plan.TargetRunnerIDs)
	}
	if err == nil {
		err = json.Unmarshal([]byte(batchJSON), &plan.BatchPolicy)
	}
	if err == nil {
		err = json.Unmarshal([]byte(windowJSON), &plan.ChangeWindow)
	}
	if err == nil {
		plan.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	}
	if err == nil {
		plan.ExpiresAt, err = time.Parse(time.RFC3339Nano, expires)
	}
	if err == nil {
		plan.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
	}
	if err == nil {
		plan.ApprovedAt, err = nullableTime(approved)
	}
	if err == nil {
		plan.SecondApprovedAt, err = nullableTime(secondApproved)
	}
	if err == nil {
		plan.StartedAt, err = nullableTime(started)
	}
	if err == nil {
		plan.ObservationStartedAt, err = nullableTime(observationStarted)
	}
	if err == nil {
		plan.ObservationEndsAt, err = nullableTime(observationEnds)
	}
	if err == nil {
		plan.FinishedAt, err = nullableTime(finished)
	}
	return plan, confirmationHash, err == nil, err
}

func fleetRunnerUpdateItemByID(ctx context.Context, db queryer, id string) (storedFleetRunnerUpdateItem, bool, error) {
	return scanFleetRunnerUpdateItem(db.QueryRowContext(ctx, fleetRunnerUpdateItemSelect+` WHERE id=?`, id))
}

func scanFleetRunnerUpdateItem(scanner interface{ Scan(...any) error }) (storedFleetRunnerUpdateItem, bool, error) {
	var stored storedFleetRunnerUpdateItem
	var state string
	var claimed, heartbeat, lease, deadline, started, finished sql.NullString
	var updated string
	err := scanner.Scan(&stored.item.ID, &stored.item.PlanID, &stored.item.RunnerID,
		&stored.item.ServerID, &stored.item.BatchIndex, &state, &stored.item.PreviousVersion,
		&stored.item.PreviousRevision, &stored.item.PreviousDigest, &stored.item.ExpectedLeaseGeneration,
		&stored.item.CertificateFingerprint, &stored.item.AssignmentAction,
		&stored.item.AssignmentGeneration, &stored.tokenHash, &stored.item.AssignmentIdempotencyKey,
		&stored.item.CompletionIdempotencyKey, &stored.item.ObservedVersion,
		&stored.item.ObservedRevision, &stored.item.ObservedDigest, &stored.item.Error,
		&stored.item.RollbackError, &claimed, &heartbeat, &lease, &deadline, &started, &finished, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return storedFleetRunnerUpdateItem{}, false, nil
	}
	if err != nil {
		return storedFleetRunnerUpdateItem{}, false, err
	}
	stored.item.State = model.FleetRunnerUpdateItemState(state)
	if stored.item.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated); err == nil {
		stored.item.ClaimedAt, err = nullableTime(claimed)
	}
	if err == nil {
		stored.item.LastHeartbeatAt, err = nullableTime(heartbeat)
	}
	if err == nil {
		stored.item.LeaseExpiresAt, err = nullableTime(lease)
	}
	if err == nil {
		stored.item.ExecutionDeadlineAt, err = nullableTime(deadline)
	}
	if err == nil {
		stored.item.StartedAt, err = nullableTime(started)
	}
	if err == nil {
		stored.item.FinishedAt, err = nullableTime(finished)
	}
	return stored, err == nil, err
}

func appendFleetRunnerUpdateAudit(
	ctx context.Context,
	tx *sql.Tx,
	actor, event, outcome string,
	plan model.FleetRunnerUpdatePlan,
	now time.Time,
) error {
	return appendPlanAudit(ctx, tx, model.AuditEntry{
		ActorHash: actor, Event: event, Resource: plan.ID, Outcome: outcome,
		Detail: map[string]any{
			"tenantId": plan.TenantID, "targetRunnerIds": plan.TargetRunnerIDs,
			"artifactDigest":   plan.Manifest.ArtifactDigest,
			"artifactRevision": plan.Manifest.ArtifactRevision,
			"targetVersion":    plan.Manifest.TargetVersion, "planDigest": plan.PlanDigest,
		},
	}, now)
}

func transitionNotFound(err error) error {
	if err == nil {
		return ErrNotFound
	}
	return err
}
