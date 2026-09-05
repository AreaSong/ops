package store

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

type BatchOperationInput struct {
	Operation        model.BatchOperation
	ConfirmationHash string
}

// BatchCoordinatorFence is the durable owner/token pair required for writes
// made by a batch coordinator.  The token is intentionally opaque; the
// database checks both fields and the unexpired lease before accepting a
// mutation, which prevents a stale Runner from writing after takeover.
type BatchCoordinatorFence struct {
	Owner string
	Token string
}

func validBatchCoordinatorFence(fence []BatchCoordinatorFence) bool {
	return len(fence) == 1 && fence[0].Owner != "" && fence[0].Token != ""
}

func (store *Store) CreateBatchOperation(ctx context.Context, input BatchOperationInput) (model.BatchOperation, bool, error) {
	op := input.Operation
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return model.BatchOperation{}, false, err
	}
	defer tx.Rollback()
	if existing, found, err := batchByIdempotency(ctx, tx, op.IdempotencyKey); err != nil {
		return model.BatchOperation{}, false, err
	} else if found {
		if existing.ActorHash != op.ActorHash || existing.Digest != op.Digest {
			return model.BatchOperation{}, false, ErrIdempotency
		}
		return existing, false, nil
	}
	policyJSON, err := encodeJSON(op.Task.BatchPolicy)
	if err != nil {
		return model.BatchOperation{}, false, err
	}
	taskJSON, err := encodeJSON(op.Task)
	if err != nil {
		return model.BatchOperation{}, false, err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO batch_jobs(id,idempotency_key,actor_hash,tenant_id,action,target,strategy,policy_json,task_json,digest,
		 confirmation_hash,confirmation_phrase,state,failure_policy,requires_dual_approval,second_approved_by_hash,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, op.ID, op.IdempotencyKey, op.ActorHash, op.TenantID, op.Action, op.Target,
		op.Task.BatchPolicy.Strategy, policyJSON, taskJSON, op.Digest, input.ConfirmationHash, op.ConfirmationPhrase, op.State,
		op.Task.FailurePolicy, op.RequiresDualApproval, op.SecondApprovedByHash, timeText(op.CreatedAt), timeText(op.UpdatedAt))
	if err != nil {
		return model.BatchOperation{}, false, err
	}
	for _, item := range op.Items {
		dependencies, err := encodeJSON(item.DependsOn)
		if err != nil {
			return model.BatchOperation{}, false, err
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO batch_items(job_id,item_id,object_id,service,server_id,runner_id,batch_index,dependencies_json,state,plan_id,task_id,error,updated_at)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, op.ID, item.ID, item.ObjectID, item.Service, item.ServerID, item.RunnerID,
			item.BatchIndex, dependencies, item.State, item.PlanID, item.TaskID, item.Error, timeText(item.UpdatedAt))
		if err != nil {
			return model.BatchOperation{}, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return model.BatchOperation{}, false, err
	}
	return op, true, nil
}

func batchByIdempotency(ctx context.Context, db queryer, key string) (model.BatchOperation, bool, error) {
	if key == "" {
		return model.BatchOperation{}, false, nil
	}
	op, err := scanBatchOperation(db.QueryRowContext(ctx, batchSelect+` WHERE idempotency_key=?`, key))
	if errors.Is(err, sql.ErrNoRows) {
		return model.BatchOperation{}, false, nil
	}
	return op, err == nil, err
}

const batchSelect = `SELECT id,idempotency_key,run_idempotency_key,actor_hash,tenant_id,action,target,task_json,digest,confirmation_hash,confirmation_phrase,state,
	approved_by_hash,approved_at,requires_dual_approval,second_approved_by_hash,second_approved_at,
	executed_by_hash,executed_at,started_at,finished_at,summary,error,created_at,updated_at,
	canary_observation_started_at,canary_observed_at FROM batch_jobs`

func scanBatchOperation(row scanner) (model.BatchOperation, error) {
	var op model.BatchOperation
	var taskJSON, confirmationHash, state, created, updated string
	var approved, secondApproved, executed, started, finished, canaryStarted, canaryObserved sql.NullString
	var dual int
	if err := row.Scan(&op.ID, &op.IdempotencyKey, &op.RunIdempotencyKey, &op.ActorHash, &op.TenantID, &op.Action, &op.Target, &taskJSON,
		&op.Digest, &confirmationHash, &op.ConfirmationPhrase, &state, &op.ApprovedByHash, &approved, &dual, &op.SecondApprovedByHash, &secondApproved,
		&op.ExecutedByHash, &executed, &started, &finished, &op.Summary, &op.Error,
		&created, &updated, &canaryStarted, &canaryObserved); err != nil {
		return op, err
	}
	op.State = model.BatchOperationState(state)
	op.RequiresDualApproval = dual != 0
	if err := decodeJSON(taskJSON, &op.Task); err != nil {
		return op, err
	}
	var err error
	op.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return op, err
	}
	op.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
	if err != nil {
		return op, err
	}
	if op.ApprovedAt, err = nullableTime(approved); err != nil {
		return op, err
	}
	if op.SecondApprovedAt, err = nullableTime(secondApproved); err != nil {
		return op, err
	}
	if op.ExecutedAt, err = nullableTime(executed); err != nil {
		return op, err
	}
	if op.StartedAt, err = nullableTime(started); err != nil {
		return op, err
	}
	if op.FinishedAt, err = nullableTime(finished); err != nil {
		return op, err
	}
	if op.CanaryObservationStartedAt, err = nullableTime(canaryStarted); err != nil {
		return op, err
	}
	if op.CanaryObservedAt, err = nullableTime(canaryObserved); err != nil {
		return op, err
	}
	_ = confirmationHash
	return op, nil
}

func (store *Store) GetBatchOperation(ctx context.Context, id string) (model.BatchOperation, error) {
	op, err := scanBatchOperation(store.db.QueryRowContext(ctx, batchSelect+` WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return op, ErrNotFound
	}
	if err != nil {
		return op, err
	}
	op.Items, err = store.listBatchItems(ctx, id)
	if err == nil {
		op.SyncTask()
	}
	return op, err
}

// syncBatchTaskSnapshot keeps the compatibility task_json projection aligned
// with the normalized batch envelope. The outer batch columns remain the
// source of truth; this projection is refreshed after every state mutation so
// clients reading the nested task cannot observe a stale pending state.
func (store *Store) syncBatchTaskSnapshot(ctx context.Context, id string) error {
	op, err := store.GetBatchOperation(ctx, id)
	if err != nil {
		return err
	}
	raw, err := encodeJSON(op.Task)
	if err != nil {
		return err
	}
	_, err = store.db.ExecContext(ctx, `UPDATE batch_jobs SET task_json=?,updated_at=? WHERE id=?`, raw, timeText(store.now()), id)
	return err
}

func (store *Store) ListBatchOperations(ctx context.Context, limit, offset int) ([]model.BatchOperation, error) {
	rows, err := store.db.QueryContext(ctx, batchSelect+` ORDER BY created_at DESC,id DESC LIMIT ? OFFSET ?`, clampLimit(limit, 201), nonNegative(offset))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]model.BatchOperation, 0)
	for rows.Next() {
		op, err := scanBatchOperation(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, op)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for index := range result {
		result[index].Items, err = store.listBatchItems(ctx, result[index].ID)
		if err != nil {
			return nil, err
		}
		result[index].SyncTask()
	}
	return result, nil
}

func (store *Store) listBatchItems(ctx context.Context, id string) ([]model.BatchItem, error) {
	rows, err := store.db.QueryContext(ctx, `SELECT item_id,object_id,service,server_id,runner_id,batch_index,dependencies_json,state,plan_id,task_id,error,updated_at FROM batch_items WHERE job_id=? ORDER BY batch_index,item_id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]model.BatchItem, 0)
	for rows.Next() {
		var item model.BatchItem
		var dependencies, state, updated string
		if err := rows.Scan(&item.ID, &item.ObjectID, &item.Service, &item.ServerID, &item.RunnerID, &item.BatchIndex, &dependencies, &state, &item.PlanID, &item.TaskID, &item.Error, &updated); err != nil {
			return nil, err
		}
		item.State = model.BatchNodeState(state)
		if err := decodeJSON(dependencies, &item.DependsOn); err != nil {
			return nil, err
		}
		item.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (store *Store) ApproveBatchOperation(ctx context.Context, id, actor, digest, confirmation string) (model.BatchOperation, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return model.BatchOperation{}, err
	}
	defer tx.Rollback()
	op, err := scanBatchOperation(tx.QueryRowContext(ctx, batchSelect+` WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return model.BatchOperation{}, ErrNotFound
	}
	if err != nil {
		return op, err
	}
	if op.Digest != digest {
		return op, errors.New("批量计划已变化或不能批准")
	}
	if op.RequiresDualApproval && op.State == model.BatchApproved {
		if op.SecondApprovedByHash == actor {
			// A retry of the second approval is idempotent and must not create a
			// second durable transition.
			if err := tx.Commit(); err != nil {
				return model.BatchOperation{}, err
			}
			return op, nil
		}
		return op, errors.New("批量计划已完成双人批准")
	}
	if op.State != model.BatchPendingApproval {
		return op, errors.New("批量计划已变化或不能批准")
	}
	var expected string
	if err := tx.QueryRowContext(ctx, `SELECT confirmation_hash FROM batch_jobs WHERE id=?`, id).Scan(&expected); err != nil {
		return op, err
	}
	provided := HashConfirmation(confirmation)
	if subtle.ConstantTimeCompare([]byte(expected), []byte(provided)) != 1 {
		return op, ErrConfirmation
	}
	now := store.now()
	if op.RequiresDualApproval {
		if op.ApprovedByHash == "" {
			if op.ActorHash == actor {
				return model.BatchOperation{}, ErrActorMismatch
			}
			result, err := tx.ExecContext(ctx, `UPDATE batch_jobs SET approved_by_hash=?,approved_at=?,updated_at=? WHERE id=? AND state=? AND approved_by_hash=''`, actor, timeText(now), timeText(now), id, model.BatchPendingApproval)
			if err = requireOne(result, err, "批量计划第一批准无法写入"); err != nil {
				return model.BatchOperation{}, err
			}
			if err := tx.Commit(); err != nil {
				return model.BatchOperation{}, err
			}
			op.ApprovedByHash, op.ApprovedAt, op.UpdatedAt = actor, &now, now
			op.Items, _ = store.listBatchItems(ctx, id)
			op.SyncTask()
			if err := store.syncBatchTaskSnapshot(ctx, id); err != nil {
				return op, err
			}
			return op, nil
		}
		if op.ActorHash == actor || op.ApprovedByHash == actor {
			if op.ApprovedByHash == actor {
				if err := tx.Commit(); err != nil {
					return model.BatchOperation{}, err
				}
				return op, nil
			}
			return model.BatchOperation{}, ErrActorMismatch
		}
		result, err := tx.ExecContext(ctx, `UPDATE batch_jobs SET state=?,second_approved_by_hash=?,second_approved_at=?,updated_at=? WHERE id=? AND state=? AND approved_by_hash!='' AND second_approved_by_hash=''`, model.BatchApproved, actor, timeText(now), timeText(now), id, model.BatchPendingApproval)
		if err = requireOne(result, err, "批量计划第二批准无法写入"); err != nil {
			return model.BatchOperation{}, err
		}
		if err := tx.Commit(); err != nil {
			return model.BatchOperation{}, err
		}
		op.State, op.SecondApprovedByHash, op.SecondApprovedAt, op.UpdatedAt = model.BatchApproved, actor, &now, now
		op.Items, _ = store.listBatchItems(ctx, id)
		op.SyncTask()
		if err := store.syncBatchTaskSnapshot(ctx, id); err != nil {
			return op, err
		}
		return op, nil
	}
	result, err := tx.ExecContext(ctx, `UPDATE batch_jobs SET state=?,approved_by_hash=?,approved_at=?,updated_at=? WHERE id=? AND state=?`, model.BatchApproved, actor, timeText(now), timeText(now), id, model.BatchPendingApproval)
	if err = requireOne(result, err, "批量计划无法批准"); err != nil {
		return op, err
	}
	if err := tx.Commit(); err != nil {
		return op, err
	}
	op.State, op.ApprovedByHash, op.ApprovedAt, op.UpdatedAt = model.BatchApproved, actor, &now, now
	op.Items, _ = store.listBatchItems(ctx, id)
	op.SyncTask()
	if err := store.syncBatchTaskSnapshot(ctx, id); err != nil {
		return op, err
	}
	return op, nil
}

func (store *Store) StartBatchOperation(
	ctx context.Context,
	id, actor, runKey string,
) (model.BatchOperation, bool, error) {
	if runKey == "" {
		return model.BatchOperation{}, false, ErrIdempotency
	}
	var existingKey string
	if err := store.db.QueryRowContext(ctx, `SELECT run_idempotency_key FROM batch_jobs WHERE id=?`, id).Scan(&existingKey); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.BatchOperation{}, false, ErrNotFound
		}
		return model.BatchOperation{}, false, err
	}
	if existingKey != "" {
		if existingKey != runKey {
			return model.BatchOperation{}, false, ErrIdempotency
		}
		op, err := store.GetBatchOperation(ctx, id)
		return op, false, err
	}
	now := store.now()
	result, err := store.db.ExecContext(ctx, `UPDATE batch_jobs SET state=?,run_idempotency_key=?,executed_by_hash=?,executed_at=?,started_at=?,updated_at=? WHERE id=? AND state=? AND run_idempotency_key=''`, model.BatchRunning, runKey, actor, timeText(now), timeText(now), timeText(now), id, model.BatchApproved)
	if err = requireOne(result, err, "批量计划无法开始"); err != nil {
		return model.BatchOperation{}, false, err
	}
	op, err := store.GetBatchOperation(ctx, id)
	if err == nil {
		err = store.syncBatchTaskSnapshot(ctx, id)
	}
	return op, true, err
}

func (store *Store) UpdateBatchItem(ctx context.Context, jobID, itemID string, state model.BatchNodeState, planID, taskID, errorMessage string) error {
	result, err := store.db.ExecContext(ctx, `UPDATE batch_items SET state=?,plan_id=CASE WHEN ?='' THEN plan_id ELSE ? END,task_id=CASE WHEN ?='' THEN task_id ELSE ? END,error=?,updated_at=? WHERE job_id=? AND item_id=?`, state, planID, planID, taskID, taskID, errorMessage, timeText(store.now()), jobID, itemID)
	if err := requireOne(result, err, "批量项目状态更新失败"); err != nil {
		return err
	}
	return store.syncBatchTaskSnapshot(ctx, jobID)
}

func (store *Store) UpdateBatchItemCAS(
	ctx context.Context,
	jobID, itemID string,
	from, to model.BatchNodeState,
	planID, taskID, errorMessage string,
	fence ...BatchCoordinatorFence,
) error {
	now := store.now()
	query := `UPDATE batch_items SET state=?,
		plan_id=CASE WHEN ?='' THEN plan_id ELSE ? END,
		task_id=CASE WHEN ?='' THEN task_id ELSE ? END,error=?,updated_at=?
		WHERE job_id=? AND item_id=? AND state=?`
	args := []any{to, planID, planID, taskID, taskID, errorMessage, timeText(now), jobID, itemID, from}
	if len(fence) > 0 {
		if !validBatchCoordinatorFence(fence) {
			return fmt.Errorf("批量项目状态更新失败: %w", ErrNotFound)
		}
		query += ` AND EXISTS (
			SELECT 1 FROM batch_coordinator_leases
			WHERE job_id=? AND owner_id=? AND fencing_token=? AND lease_expires_at>?
		)`
		args = append(args, jobID, fence[0].Owner, fence[0].Token, timeText(now))
	}
	result, err := store.db.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return fmt.Errorf("批量项目状态更新失败: %w", ErrNotFound)
	}
	return store.syncBatchTaskSnapshot(ctx, jobID)
}

func (store *Store) BatchItemByTask(
	ctx context.Context,
	taskID string,
) (string, string, model.BatchNodeState, error) {
	var jobID, itemID string
	var state model.BatchNodeState
	err := store.db.QueryRowContext(ctx, `SELECT job_id,item_id,state FROM batch_items WHERE task_id=?`, taskID).Scan(&jobID, &itemID, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", "", ErrNotFound
	}
	return jobID, itemID, state, err
}

// BindBatchItemExecution closes the crash window between the idempotent plan
// task transaction and the batch item update.  If a Runner dies after
// StartPlanTask commits, the next coordinator can recover both identifiers by
// deterministic request keys and bind the existing execution instead of
// launching a duplicate.
func (store *Store) BindBatchItemExecution(
	ctx context.Context,
	jobID, itemID, planRequestKey, taskRequestKey string,
	fence ...BatchCoordinatorFence,
) (string, string, error) {
	var planID, taskID string
	err := store.db.QueryRowContext(ctx, `SELECT p.id,t.id FROM release_plans p
		JOIN tasks t ON t.plan_id=p.id
		WHERE p.request_idempotency_key=? AND t.idempotency_key=?`, planRequestKey, taskRequestKey).
		Scan(&planID, &taskID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", ErrNotFound
	}
	if err != nil {
		return "", "", err
	}
	if err := store.UpdateBatchItemCAS(ctx, jobID, itemID, model.BatchNodeReady, model.BatchNodeRunning,
		planID, taskID, "", fence...); err != nil {
		return "", "", err
	}
	return planID, taskID, nil
}

func (store *Store) ListActiveBatchOperations(ctx context.Context) ([]string, error) {
	rows, err := store.db.QueryContext(ctx, `SELECT id FROM batch_jobs WHERE state IN (?,?,?,?) ORDER BY created_at`, model.BatchRunning, model.BatchObserving, model.BatchPaused, model.BatchRollingBack)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		result = append(result, id)
	}
	return result, rows.Err()
}

func (store *Store) FinishBatchOperation(ctx context.Context, id string, state model.BatchOperationState, summary, errorMessage string, fence ...BatchCoordinatorFence) error {
	if !state.Terminal() && state != model.BatchPaused && state != model.BatchObserving {
		return errors.New("批量作业状态无效")
	}
	now := store.now()
	var finished any
	if state.Terminal() {
		finished = timeText(now)
	}
	query := `UPDATE batch_jobs SET state=?,summary=?,error=?,finished_at=?,canary_observation_started_at=CASE WHEN ? IN (?,?) THEN NULL ELSE canary_observation_started_at END,updated_at=? WHERE id=? AND state IN (?,?,?,?)`
	args := []any{state, summary, errorMessage, finished, state, model.BatchSucceeded, model.BatchNeedsAttention, timeText(now), id,
		model.BatchRunning, model.BatchPaused, model.BatchObserving, model.BatchRollingBack}
	if len(fence) > 0 {
		if !validBatchCoordinatorFence(fence) {
			return fmt.Errorf("批量作业无法收口: %w", ErrNotFound)
		}
		query += ` AND EXISTS (
			SELECT 1 FROM batch_coordinator_leases
			WHERE job_id=? AND owner_id=? AND fencing_token=? AND lease_expires_at>?
		)`
		args = append(args, id, fence[0].Owner, fence[0].Token, timeText(now))
	}
	result, err := store.db.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return fmt.Errorf("批量作业无法收口: %w", ErrNotFound)
	}
	return store.syncBatchTaskSnapshot(ctx, id)
}

// BeginBatchCanaryObservation durably enters the independent canary gate.
// The operation remains in observing state across Runner restarts; the
// timestamp is the source of truth for the observation deadline.
func (store *Store) BeginBatchCanaryObservation(ctx context.Context, id string, fence BatchCoordinatorFence) error {
	if !validBatchCoordinatorFence([]BatchCoordinatorFence{fence}) {
		return fmt.Errorf("批量 canary 观察租约无效: %w", ErrNotFound)
	}
	now := store.now()
	result, err := store.db.ExecContext(ctx, `UPDATE batch_jobs SET state=?,canary_observation_started_at=?,updated_at=?
		WHERE id=? AND state=? AND canary_observed_at IS NULL
		AND EXISTS (SELECT 1 FROM batch_coordinator_leases WHERE job_id=? AND owner_id=? AND fencing_token=? AND lease_expires_at>?)`,
		model.BatchObserving, timeText(now), timeText(now), id, model.BatchRunning,
		id, fence.Owner, fence.Token, timeText(now))
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return fmt.Errorf("批量 canary 观察无法开始: %w", ErrNotFound)
	}
	return store.syncBatchTaskSnapshot(ctx, id)
}

// CompleteBatchCanaryObservation closes the independent gate and records that
// it has been evaluated, preventing a resumed coordinator from re-entering it.
func (store *Store) CompleteBatchCanaryObservation(ctx context.Context, id string, fence BatchCoordinatorFence) error {
	if !validBatchCoordinatorFence([]BatchCoordinatorFence{fence}) {
		return fmt.Errorf("批量 canary 观察租约无效: %w", ErrNotFound)
	}
	now := store.now()
	result, err := store.db.ExecContext(ctx, `UPDATE batch_jobs SET state=?,canary_observation_started_at=NULL,canary_observed_at=?,updated_at=?
		WHERE id=? AND state=? AND canary_observed_at IS NULL
		AND EXISTS (SELECT 1 FROM batch_coordinator_leases WHERE job_id=? AND owner_id=? AND fencing_token=? AND lease_expires_at>?)`,
		model.BatchRunning, timeText(now), timeText(now), id, model.BatchObserving,
		id, fence.Owner, fence.Token, timeText(now))
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return fmt.Errorf("批量 canary 观察无法完成: %w", ErrNotFound)
	}
	return store.syncBatchTaskSnapshot(ctx, id)
}

// AcquireBatchCoordinator obtains a durable lease for the wave coordinator.
// The lease is separate from the batch state so a restarted Runner cannot
// accidentally run the same job concurrently with an older process.
func (store *Store) AcquireBatchCoordinator(ctx context.Context, jobID, owner string, ttl time.Duration) (string, uint64, bool, error) {
	if jobID == "" || owner == "" || ttl <= 0 {
		return "", 0, false, errors.New("批量协调器租约参数无效")
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return "", 0, false, err
	}
	defer tx.Rollback()
	now := store.now()
	expires := now.Add(ttl)
	var currentOwner, currentToken string
	var generation uint64
	var expiryText string
	err = tx.QueryRowContext(ctx, `SELECT owner_id,generation,fencing_token,lease_expires_at FROM batch_coordinator_leases WHERE job_id=?`, jobID).
		Scan(&currentOwner, &generation, &currentToken, &expiryText)
	if errors.Is(err, sql.ErrNoRows) {
		token, tokenErr := coordinatorToken()
		if tokenErr != nil {
			return "", 0, false, tokenErr
		}
		result, insertErr := tx.ExecContext(ctx, `INSERT INTO batch_coordinator_leases(job_id,owner_id,generation,fencing_token,lease_expires_at,updated_at)
			SELECT ?,?,?,?,?,? WHERE EXISTS (SELECT 1 FROM batch_jobs WHERE id=?)
			ON CONFLICT(job_id) DO NOTHING`, jobID, owner, 1, token, timeText(expires), timeText(now), jobID)
		if insertErr != nil {
			return "", 0, false, insertErr
		}
		rows, rowsErr := result.RowsAffected()
		if rowsErr != nil {
			return "", 0, false, rowsErr
		}
		if rows != 1 {
			return "", 0, false, nil
		}
		if err := tx.Commit(); err != nil {
			return "", 0, false, err
		}
		return token, 1, true, nil
	}
	if err != nil {
		return "", 0, false, err
	}
	currentExpiry, parseErr := time.Parse(time.RFC3339Nano, expiryText)
	if parseErr != nil {
		return "", 0, false, parseErr
	}
	if currentOwner != owner && currentExpiry.After(now) {
		return "", generation, false, nil
	}
	if currentOwner == owner && currentExpiry.After(now) {
		result, updateErr := tx.ExecContext(ctx, `UPDATE batch_coordinator_leases SET lease_expires_at=?,updated_at=? WHERE job_id=? AND owner_id=? AND fencing_token=? AND lease_expires_at>?`, timeText(expires), timeText(now), jobID, owner, currentToken, timeText(now))
		if updateErr != nil {
			return "", 0, false, updateErr
		}
		rows, rowsErr := result.RowsAffected()
		if rowsErr != nil {
			return "", 0, false, rowsErr
		}
		if rows != 1 {
			return "", generation, false, nil
		}
		if err := tx.Commit(); err != nil {
			return "", 0, false, err
		}
		return currentToken, generation, true, nil
	}
	token, tokenErr := coordinatorToken()
	if tokenErr != nil {
		return "", 0, false, tokenErr
	}
	generation++
	result, err := tx.ExecContext(ctx, `UPDATE batch_coordinator_leases SET owner_id=?,generation=?,fencing_token=?,lease_expires_at=?,updated_at=? WHERE job_id=? AND lease_expires_at<=?`, owner, generation, token, timeText(expires), timeText(now), jobID, timeText(now))
	if err != nil {
		return "", generation, false, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return "", generation, false, err
	}
	if rows != 1 {
		return "", generation, false, nil
	}
	if err := tx.Commit(); err != nil {
		return "", 0, false, err
	}
	return token, generation, true, nil
}

func (store *Store) RenewBatchCoordinator(ctx context.Context, jobID, owner, token string, ttl time.Duration) (bool, error) {
	if jobID == "" || owner == "" || token == "" || ttl <= 0 {
		return false, nil
	}
	now := store.now()
	result, err := store.db.ExecContext(ctx, `UPDATE batch_coordinator_leases SET lease_expires_at=?,updated_at=? WHERE job_id=? AND owner_id=? AND fencing_token=? AND lease_expires_at>?`, timeText(now.Add(ttl)), timeText(now), jobID, owner, token, timeText(now))
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	return rows == 1, err
}

func (store *Store) BatchCoordinatorLeaseValid(ctx context.Context, jobID, owner, token string) (bool, error) {
	if jobID == "" || owner == "" || token == "" {
		return false, nil
	}
	var expiryText string
	err := store.db.QueryRowContext(ctx, `SELECT lease_expires_at FROM batch_coordinator_leases WHERE job_id=? AND owner_id=? AND fencing_token=?`, jobID, owner, token).Scan(&expiryText)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	expires, err := time.Parse(time.RFC3339Nano, expiryText)
	if err != nil {
		return false, err
	}
	return expires.After(store.now()), nil
}

func (store *Store) ReleaseBatchCoordinator(ctx context.Context, jobID, owner, token string) error {
	if jobID == "" || owner == "" || token == "" {
		return nil
	}
	_, err := store.db.ExecContext(ctx, `DELETE FROM batch_coordinator_leases WHERE job_id=? AND owner_id=? AND fencing_token=?`, jobID, owner, token)
	return err
}

func (store *Store) GetBatchCoordinator(ctx context.Context, jobID string) (BatchCoordinatorFence, uint64, *time.Time, error) {
	var fence BatchCoordinatorFence
	var generation uint64
	var expiry string
	err := store.db.QueryRowContext(ctx, `SELECT owner_id,generation,fencing_token,lease_expires_at FROM batch_coordinator_leases WHERE job_id=?`, jobID).
		Scan(&fence.Owner, &generation, &fence.Token, &expiry)
	if errors.Is(err, sql.ErrNoRows) {
		return BatchCoordinatorFence{}, 0, nil, ErrNotFound
	}
	if err != nil {
		return BatchCoordinatorFence{}, 0, nil, err
	}
	expires, err := time.Parse(time.RFC3339Nano, expiry)
	if err != nil {
		return BatchCoordinatorFence{}, 0, nil, err
	}
	return fence, generation, &expires, nil
}

func coordinatorToken() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(value), nil
}
