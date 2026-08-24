package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

var (
	ErrAssignmentExists      = errors.New("任务已存在远程分派")
	ErrAssignmentNotFound    = errors.New("远程任务分派不存在")
	ErrAssignmentFence       = errors.New("远程任务分派 fencing 校验失败")
	ErrAssignmentExpired     = errors.New("远程任务分派租约已过期")
	ErrAssignmentCompleted   = errors.New("远程任务分派已经完成")
	ErrAssignmentSequence    = errors.New("Runner 事件序号不连续")
	ErrAssignmentIdempotency = errors.New("远程任务请求幂等键或摘要不匹配")
)

const assignmentSelect = `
	SELECT task_id, server_id, runner_id, generation, state, contract_json,
	       contract_digest, claim_token_hash, claimed_at, last_heartbeat_at, lease_expires_at,
	       execution_deadline_at, completion_digest, completion_idempotency_key,
	       completion_event_sequence, last_runner_sequence,
	       created_at, updated_at
	FROM task_assignments`

type assignmentRow struct {
	assignment model.TaskAssignment
	contract   model.TaskDispatch
	tokenHash  string
}

func scanAssignment(row interface{ Scan(...any) error }) (assignmentRow, error) {
	var result assignmentRow
	var state, contractJSON, claimedAt, heartbeatAt, leaseExpiresAt string
	var deadline, createdAt, updatedAt string
	var generation, sequence uint64
	if err := row.Scan(
		&result.assignment.TaskID, &result.assignment.ServerID, &result.assignment.RunnerID,
		&generation, &state, &contractJSON, &result.assignment.ContractDigest, &result.tokenHash,
		&claimedAt, &heartbeatAt, &leaseExpiresAt, &deadline, &result.assignment.CompletionDigest,
		&result.assignment.CompletionKey, &result.assignment.CompletionSequence,
		&sequence, &createdAt, &updatedAt,
	); err != nil {
		return result, err
	}
	result.assignment.Generation = generation
	result.assignment.State = model.AssignmentState(state)
	result.assignment.LastRunnerSequence = sequence
	var err error
	if result.assignment.ClaimedAt, err = nullableAssignmentTime(claimedAt); err != nil {
		return result, err
	}
	if result.assignment.LastHeartbeatAt, err = nullableAssignmentTime(heartbeatAt); err != nil {
		return result, err
	}
	if result.assignment.LeaseExpiresAt, err = nullableAssignmentTime(leaseExpiresAt); err != nil {
		return result, err
	}
	if result.assignment.ExecutionDeadlineAt, err = time.Parse(time.RFC3339Nano, deadline); err != nil {
		return result, err
	}
	if result.assignment.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt); err != nil {
		return result, err
	}
	if result.assignment.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt); err != nil {
		return result, err
	}
	if err := json.Unmarshal([]byte(contractJSON), &result.contract); err != nil {
		return result, fmt.Errorf("解析远程任务合同失败: %w", err)
	}
	return result, nil
}

func nullableAssignmentTime(value string) (*time.Time, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func assignmentTokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func assignmentOwner(runnerID string, generation uint64) string {
	return fmt.Sprintf("remote:%s:%d", runnerID, generation)
}

func assignmentLease(now time.Time, lease, deadline time.Time) time.Time {
	if lease.After(deadline) {
		return deadline
	}
	return lease
}

func validateAssignmentLease(lease time.Duration) error {
	if lease <= 0 || lease > 15*time.Minute {
		return errors.New("远程任务租约必须在 1 秒到 15 分钟之间")
	}
	return nil
}

// CreateTaskAssignment persists the immutable task contract before a Runner
// can claim it. The caller is responsible for selecting a live Runner and for
// ensuring the task has not been started locally.
func (store *Store) CreateTaskAssignment(
	ctx context.Context, taskID, serverID, runnerID string, executionDeadline time.Time,
) (model.TaskAssignment, error) {
	if taskID == "" || serverID == "" || runnerID == "" || executionDeadline.IsZero() {
		return model.TaskAssignment{}, errors.New("远程任务分派参数不完整")
	}
	now := store.now()
	if !executionDeadline.After(now) {
		return model.TaskAssignment{}, errors.New("远程任务执行截止时间必须在未来")
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return model.TaskAssignment{}, err
	}
	defer tx.Rollback()
	task, err := scanTask(tx.QueryRowContext(ctx, taskSelect+` WHERE id = ?`, taskID))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.TaskAssignment{}, ErrNotFound
		}
		return model.TaskAssignment{}, err
	}
	if task.State != model.TaskQueued {
		return model.TaskAssignment{}, fmt.Errorf("任务当前状态不可远程分派: %s", task.State)
	}
	var existing string
	err = tx.QueryRowContext(ctx, `SELECT task_id FROM task_assignments WHERE task_id = ?`, taskID).Scan(&existing)
	if err == nil {
		return model.TaskAssignment{}, ErrAssignmentExists
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return model.TaskAssignment{}, err
	}
	contract := model.NewTaskDispatch(task)
	contractJSON, err := json.Marshal(contract)
	if err != nil {
		return model.TaskAssignment{}, err
	}
	contractSum := sha256.Sum256(contractJSON)
	contractDigest := "sha256:" + hex.EncodeToString(contractSum[:])
	_, err = tx.ExecContext(ctx, `
		INSERT INTO task_assignments (
			task_id, server_id, runner_id, generation, state, contract_json,
			contract_digest, execution_deadline_at, created_at, updated_at
		) VALUES (?, ?, ?, 1, ?, ?, ?, ?, ?, ?)
	`, taskID, serverID, runnerID, model.AssignmentAssigned, contractJSON, contractDigest,
		timeText(executionDeadline), timeText(now), timeText(now))
	if err != nil {
		if isSQLiteConstraintError(err) {
			return model.TaskAssignment{}, ErrAssignmentExists
		}
		return model.TaskAssignment{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.TaskAssignment{}, err
	}
	return model.TaskAssignment{
		TaskID: taskID, ServerID: serverID, RunnerID: runnerID, Generation: 1,
		State: model.AssignmentAssigned, ContractDigest: contractDigest,
		ExecutionDeadlineAt: executionDeadline, CreatedAt: now, UpdatedAt: now,
	}, nil
}

func (store *Store) GetTaskAssignment(ctx context.Context, taskID string) (model.TaskAssignment, model.TaskDispatch, error) {
	result, err := scanAssignment(store.db.QueryRowContext(ctx, assignmentSelect+` WHERE task_id = ?`, taskID))
	if errors.Is(err, sql.ErrNoRows) {
		return model.TaskAssignment{}, model.TaskDispatch{}, ErrAssignmentNotFound
	}
	if err != nil {
		return model.TaskAssignment{}, model.TaskDispatch{}, err
	}
	return result.assignment, result.contract, nil
}

// ClaimTaskAssignment atomically selects one assigned task, fences the claim,
// and moves the task itself to running. SQLite's single writer transaction
// makes the select/update pair safe against concurrent Runner claims.
func (store *Store) ClaimTaskAssignment(
	ctx context.Context, runnerID, taskID string, lease time.Duration,
) (model.TaskAssignment, model.Task, bool, error) {
	if runnerID == "" {
		return model.TaskAssignment{}, model.Task{}, false, errors.New("Runner 标识不能为空")
	}
	if err := validateAssignmentLease(lease); err != nil {
		return model.TaskAssignment{}, model.Task{}, false, err
	}
	now := store.now()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return model.TaskAssignment{}, model.Task{}, false, err
	}
	defer tx.Rollback()
	runnerServerID, err := requireClaimableRunner(ctx, tx, runnerID, now)
	if err != nil {
		return model.TaskAssignment{}, model.Task{}, false, err
	}
	query := assignmentSelect + ` WHERE runner_id = ? AND state = ? AND execution_deadline_at > ?`
	args := []any{runnerID, model.AssignmentAssigned, timeText(now)}
	if taskID != "" {
		query += ` AND task_id = ?`
		args = append(args, taskID)
	}
	query += ` ORDER BY created_at ASC, task_id ASC LIMIT 1`
	row, err := scanAssignment(tx.QueryRowContext(ctx, query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return model.TaskAssignment{}, model.Task{}, false, nil
	}
	if err != nil {
		return model.TaskAssignment{}, model.Task{}, false, err
	}
	if !row.assignment.ExecutionDeadlineAt.After(now) {
		return model.TaskAssignment{}, model.Task{}, false, ErrAssignmentExpired
	}
	if row.assignment.ServerID != runnerServerID {
		return model.TaskAssignment{}, model.Task{}, false, ErrAssignmentFence
	}
	deadline := row.assignment.ExecutionDeadlineAt
	leaseExpires := assignmentLease(now, now.Add(lease), deadline)
	token, err := newLeaseToken()
	if err != nil {
		return model.TaskAssignment{}, model.Task{}, false, err
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE task_assignments SET state = ?, claim_token_hash = ?, claimed_at = ?,
			last_heartbeat_at = ?, lease_expires_at = ?, updated_at = ?
		WHERE task_id = ? AND state = ? AND generation = ?
			AND execution_deadline_at > ?
	`, model.AssignmentClaimed, assignmentTokenHash(token), timeText(now), timeText(now),
		timeText(leaseExpires), timeText(now), row.assignment.TaskID, model.AssignmentAssigned,
		row.assignment.Generation, timeText(now))
	if err = requireOne(result, err, "远程任务 claim 竞争失败"); err != nil {
		return model.TaskAssignment{}, model.Task{}, false, ErrAssignmentFence
	}
	owner := assignmentOwner(runnerID, row.assignment.Generation)
	result, err = tx.ExecContext(ctx, `
		UPDATE tasks SET state = ?, started_at = COALESCE(started_at, ?),
			heartbeat_at = ?, runner_owner = ?
		WHERE id = ? AND state = ?
	`, model.TaskRunning, timeText(now), timeText(now), owner, row.assignment.TaskID, model.TaskQueued)
	if err = requireOne(result, err, "远程任务无法进入运行状态"); err != nil {
		return model.TaskAssignment{}, model.Task{}, false, ErrAssignmentFence
	}
	task, err := scanTask(tx.QueryRowContext(ctx, taskSelect+` WHERE id = ?`, row.assignment.TaskID))
	if err != nil {
		return model.TaskAssignment{}, model.Task{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return model.TaskAssignment{}, model.Task{}, false, err
	}
	row.assignment.State = model.AssignmentClaimed
	row.assignment.ClaimToken = token
	row.assignment.ClaimedAt = &now
	row.assignment.LastHeartbeatAt = &now
	row.assignment.LeaseExpiresAt = &leaseExpires
	row.assignment.UpdatedAt = now
	return row.assignment, task, true, nil
}

func (store *Store) assignmentForFence(
	ctx context.Context, tx *sql.Tx, runnerID, taskID string, fence model.AssignmentFence, now time.Time,
) (assignmentRow, error) {
	if runnerID == "" || taskID == "" || fence.Generation == 0 || strings.TrimSpace(fence.ClaimToken) == "" ||
		(fence.RunnerID != "" && fence.RunnerID != runnerID) {
		return assignmentRow{}, ErrAssignmentFence
	}
	row, err := scanAssignment(tx.QueryRowContext(ctx, assignmentSelect+` WHERE task_id = ?`, taskID))
	if errors.Is(err, sql.ErrNoRows) {
		return assignmentRow{}, ErrAssignmentNotFound
	}
	if err != nil {
		return assignmentRow{}, err
	}
	if row.assignment.RunnerID != runnerID || row.assignment.Generation != fence.Generation {
		return assignmentRow{}, ErrAssignmentFence
	}
	if row.assignment.State == model.AssignmentCompleted {
		return row, ErrAssignmentCompleted
	}
	if row.assignment.State != model.AssignmentClaimed || row.assignment.LeaseExpiresAt == nil ||
		!row.assignment.LeaseExpiresAt.After(now) || !row.assignment.ExecutionDeadlineAt.After(now) {
		return assignmentRow{}, ErrAssignmentExpired
	}
	if row.tokenHash != assignmentTokenHash(fence.ClaimToken) {
		return assignmentRow{}, ErrAssignmentFence
	}
	return row, nil
}

func (store *Store) renewAssignmentTx(
	ctx context.Context, tx *sql.Tx, runnerID, taskID string, fence model.AssignmentFence,
	lease time.Duration, now time.Time,
) (assignmentRow, error) {
	if err := validateAssignmentLease(lease); err != nil {
		return assignmentRow{}, err
	}
	row, err := store.assignmentForFence(ctx, tx, runnerID, taskID, fence, now)
	if err != nil {
		return assignmentRow{}, err
	}
	leaseExpires := assignmentLease(now, now.Add(lease), row.assignment.ExecutionDeadlineAt)
	result, err := tx.ExecContext(ctx, `
		UPDATE task_assignments SET last_heartbeat_at = ?, lease_expires_at = ?, updated_at = ?
		WHERE task_id = ? AND runner_id = ? AND state = ? AND generation = ? AND claim_token_hash = ?
			AND lease_expires_at > ? AND execution_deadline_at > ?
	`, timeText(now), timeText(leaseExpires), timeText(now), taskID, runnerID, model.AssignmentClaimed,
		fence.Generation, assignmentTokenHash(fence.ClaimToken), timeText(now), timeText(now))
	if err = requireOne(result, err, "远程任务心跳 fencing 失败"); err != nil {
		return assignmentRow{}, ErrAssignmentFence
	}
	row.assignment.LastHeartbeatAt = &now
	row.assignment.LeaseExpiresAt = &leaseExpires
	row.assignment.UpdatedAt = now
	return row, nil
}

func (store *Store) HeartbeatTaskAssignment(
	ctx context.Context, runnerID, taskID string, fence model.AssignmentFence, lease time.Duration,
) (model.TaskAssignment, error) {
	now := store.now()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return model.TaskAssignment{}, err
	}
	defer tx.Rollback()
	row, err := store.renewAssignmentTx(ctx, tx, runnerID, taskID, fence, lease, now)
	if err != nil {
		return model.TaskAssignment{}, err
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE tasks SET heartbeat_at = ?
		WHERE id = ? AND runner_owner = ? AND state IN (?, ?)
	`, timeText(now), taskID, assignmentOwner(row.assignment.RunnerID, fence.Generation),
		model.TaskRunning, model.TaskRollingBack)
	if err = requireOne(result, err, "远程任务心跳无法写入任务"); err != nil {
		return model.TaskAssignment{}, ErrAssignmentFence
	}
	if err := tx.Commit(); err != nil {
		return model.TaskAssignment{}, err
	}
	return row.assignment, nil
}

func (store *Store) UpdateTaskAssignmentProgress(
	ctx context.Context, runnerID, taskID string, input model.AssignmentProgressRequest, lease time.Duration,
) (model.TaskAssignment, error) {
	if strings.TrimSpace(input.Phase) == "" {
		return model.TaskAssignment{}, errors.New("远程任务阶段不能为空")
	}
	now := store.now()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return model.TaskAssignment{}, err
	}
	defer tx.Rollback()
	row, err := store.renewAssignmentTx(ctx, tx, runnerID, taskID, input.AssignmentFence, lease, now)
	if err != nil {
		return model.TaskAssignment{}, err
	}
	task, err := scanTask(tx.QueryRowContext(ctx, taskSelect+` WHERE id=?`, taskID))
	if err != nil {
		return model.TaskAssignment{}, err
	}
	if input.State != "" && input.State != model.TaskRunning && input.State != model.TaskRollingBack {
		return model.TaskAssignment{}, errors.New("远程任务进度状态无效")
	}
	taskState := task.State
	if input.State != "" {
		taskState = input.State
	}
	for index := range task.Stages {
		if task.Stages[index].State == model.StageRunning && task.Stages[index].Name != input.Phase {
			task.Stages[index].State = model.StageSucceeded
			task.Stages[index].FinishedAt = &now
		}
		if task.Stages[index].Name == input.Phase && task.Stages[index].State == model.StagePending {
			task.Stages[index].State = model.StageRunning
			task.Stages[index].StartedAt = &now
		}
	}
	stagesJSON, err := encodeJSON(task.Stages)
	if err != nil {
		return model.TaskAssignment{}, err
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE tasks SET state = ?, current_phase = ?, summary = ?, stages_json = ?, heartbeat_at = ?,
			production_changed = CASE WHEN ? THEN 1 ELSE production_changed END,
			rollback_available = CASE WHEN ? THEN 1 ELSE rollback_available END,
			rollback_reason = CASE WHEN ? != '' THEN ? ELSE rollback_reason END
		WHERE id = ? AND runner_owner = ? AND state IN (?, ?)
	`, taskState, input.Phase, input.Summary, stagesJSON, timeText(now), input.ProductionChanged,
		input.RollbackAvailable, input.RollbackReason, input.RollbackReason, taskID,
		assignmentOwner(row.assignment.RunnerID, input.Generation), model.TaskRunning, model.TaskRollingBack)
	if err = requireOne(result, err, "远程任务阶段更新失败"); err != nil {
		return model.TaskAssignment{}, ErrAssignmentFence
	}
	if err := tx.Commit(); err != nil {
		return model.TaskAssignment{}, err
	}
	return row.assignment, nil
}

func assignmentEventDigest(input model.AssignmentEventRequest) (string, error) {
	payload, err := json.Marshal(struct {
		Level   string         `json:"level"`
		Phase   string         `json:"phase"`
		Message string         `json:"message"`
		Data    map[string]any `json:"data,omitempty"`
	}{input.Level, input.Phase, input.Message, input.Data})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func (store *Store) AppendTaskAssignmentEvent(
	ctx context.Context, runnerID, taskID string, input model.AssignmentEventRequest, lease time.Duration,
) (model.Event, error) {
	if input.RunnerSequence == 0 || strings.TrimSpace(input.Message) == "" {
		return model.Event{}, errors.New("远程任务事件参数无效")
	}
	digest, err := assignmentEventDigest(input)
	if err != nil {
		return model.Event{}, err
	}
	now := store.now()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return model.Event{}, err
	}
	defer tx.Rollback()
	row, err := store.renewAssignmentTx(ctx, tx, runnerID, taskID, input.AssignmentFence, lease, now)
	if err != nil {
		return model.Event{}, err
	}
	if input.RunnerSequence <= row.assignment.LastRunnerSequence {
		var existingDigest string
		var eventSequence int64
		err = tx.QueryRowContext(ctx, `SELECT payload_digest,event_sequence FROM task_assignment_events WHERE task_id=? AND generation=? AND runner_sequence=?`, taskID, input.Generation, input.RunnerSequence).Scan(&existingDigest, &eventSequence)
		if errors.Is(err, sql.ErrNoRows) {
			return model.Event{}, ErrAssignmentSequence
		}
		if err != nil {
			return model.Event{}, err
		}
		if existingDigest != digest {
			return model.Event{}, ErrAssignmentIdempotency
		}
		return eventBySequence(ctx, tx, eventSequence)
	}
	if input.RunnerSequence != row.assignment.LastRunnerSequence+1 {
		return model.Event{}, ErrAssignmentSequence
	}
	dataJSON, err := encodeJSON(input.Data)
	if err != nil {
		return model.Event{}, err
	}
	result, err := tx.ExecContext(ctx, `
		INSERT INTO events (task_id, occurred_at, level, phase, message, data_json)
		VALUES (?, ?, ?, ?, ?, ?)
	`, taskID, timeText(now), input.Level, input.Phase, input.Message, dataJSON)
	if err != nil {
		return model.Event{}, err
	}
	eventSequence, err := result.LastInsertId()
	if err != nil {
		return model.Event{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO task_assignment_events(task_id,generation,runner_sequence,payload_digest,event_sequence,created_at)
		VALUES(?,?,?,?,?,?)
	`, taskID, input.Generation, input.RunnerSequence, digest, eventSequence, timeText(now)); err != nil {
		if isSQLiteConstraintError(err) {
			return model.Event{}, ErrAssignmentIdempotency
		}
		return model.Event{}, err
	}
	result, err = tx.ExecContext(ctx, `
		UPDATE task_assignments SET last_runner_sequence=?, updated_at=?
		WHERE task_id=? AND runner_id=? AND state=? AND generation=? AND claim_token_hash=?
			AND lease_expires_at > ?
	`, input.RunnerSequence, timeText(now), taskID, runnerID, model.AssignmentClaimed,
		input.Generation, assignmentTokenHash(input.ClaimToken), timeText(now))
	if err = requireOne(result, err, "远程任务事件 fencing 失败"); err != nil {
		return model.Event{}, ErrAssignmentFence
	}
	if err := tx.Commit(); err != nil {
		return model.Event{}, err
	}
	return model.Event{Sequence: eventSequence, TaskID: taskID, OccurredAt: now,
		Level: input.Level, Phase: input.Phase, Message: input.Message, Data: input.Data}, nil
}

func eventBySequence(ctx context.Context, tx *sql.Tx, sequence int64) (model.Event, error) {
	var event model.Event
	var occurredAt, dataJSON string
	if err := tx.QueryRowContext(ctx, `SELECT sequence,task_id,occurred_at,level,phase,message,data_json FROM events WHERE sequence=?`, sequence).
		Scan(&event.Sequence, &event.TaskID, &occurredAt, &event.Level, &event.Phase, &event.Message, &dataJSON); err != nil {
		return model.Event{}, err
	}
	var err error
	if event.OccurredAt, err = time.Parse(time.RFC3339Nano, occurredAt); err != nil {
		return model.Event{}, err
	}
	if err := decodeJSON(dataJSON, &event.Data); err != nil {
		return model.Event{}, err
	}
	return event, nil
}

func completionDigest(input model.AssignmentCompletionRequest) (string, error) {
	payload, err := json.Marshal(struct {
		IdempotencyKey    string          `json:"idempotencyKey"`
		State             model.TaskState `json:"state"`
		Summary           string          `json:"summary,omitempty"`
		Error             string          `json:"error,omitempty"`
		FailureCode       string          `json:"failureCode,omitempty"`
		Retryable         bool            `json:"retryable,omitempty"`
		RollbackAvailable bool            `json:"rollbackAvailable,omitempty"`
		ProductionChanged bool            `json:"productionChanged,omitempty"`
		RollbackReason    string          `json:"rollbackReason,omitempty"`
		ResultDigest      string          `json:"resultDigest,omitempty"`
	}{input.IdempotencyKey, input.State, input.Summary, input.Error, input.FailureCode,
		input.Retryable, input.RollbackAvailable, input.ProductionChanged, input.RollbackReason, input.ResultDigest})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// CompleteTaskAssignment commits the task terminal state and assignment state
// in one transaction. It intentionally mirrors the core task terminal update;
// plan-specific observation handling remains available to the control-plane
// completion path and is applied when a plan is present.
func (store *Store) CompleteTaskAssignment(
	ctx context.Context, runnerID, taskID string, input model.AssignmentCompletionRequest,
) (model.Task, model.TaskAssignment, int64, error) {
	return store.CompleteTaskAssignmentWithDesired(ctx, runnerID, taskID, input, nil)
}

// CompleteTaskAssignmentWithDesired completes a remote assignment and, when
// the control plane supplies a desired state for a successful lifecycle task,
// persists that state and its control event in the same transaction. Desired
// state is deliberately a separate argument: Runner completion payloads are
// execution results and are not trusted as control-plane intent.
func (store *Store) CompleteTaskAssignmentWithDesired(
	ctx context.Context, runnerID, taskID string, input model.AssignmentCompletionRequest,
	desired *DesiredStateInput,
) (model.Task, model.TaskAssignment, int64, error) {
	if !input.State.Terminal() || strings.TrimSpace(input.IdempotencyKey) == "" {
		return model.Task{}, model.TaskAssignment{}, 0, errors.New("远程任务终态或幂等键无效")
	}
	digest, err := completionDigest(input)
	if err != nil {
		return model.Task{}, model.TaskAssignment{}, 0, err
	}
	now := store.now()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return model.Task{}, model.TaskAssignment{}, 0, err
	}
	defer tx.Rollback()
	row, err := scanAssignment(tx.QueryRowContext(ctx, assignmentSelect+` WHERE task_id=?`, taskID))
	if errors.Is(err, sql.ErrNoRows) {
		return model.Task{}, model.TaskAssignment{}, 0, ErrAssignmentNotFound
	}
	if err != nil {
		return model.Task{}, model.TaskAssignment{}, 0, err
	}
	if row.assignment.State == model.AssignmentCompleted {
		if row.assignment.RunnerID != runnerID || row.assignment.Generation != input.Generation ||
			row.tokenHash != assignmentTokenHash(input.ClaimToken) ||
			row.assignment.CompletionKey != input.IdempotencyKey || row.assignment.CompletionDigest != digest {
			return model.Task{}, model.TaskAssignment{}, 0, ErrAssignmentIdempotency
		}
		task, taskErr := scanTask(tx.QueryRowContext(ctx, taskSelect+` WHERE id=?`, taskID))
		return task, row.assignment, row.assignment.CompletionSequence, taskErr
	}
	row, err = store.assignmentForFence(ctx, tx, runnerID, taskID, input.AssignmentFence, now)
	if err != nil {
		return model.Task{}, model.TaskAssignment{}, 0, err
	}
	task, err := scanTask(tx.QueryRowContext(ctx, taskSelect+` WHERE id=?`, taskID))
	if err != nil {
		return model.Task{}, model.TaskAssignment{}, 0, err
	}
	if !task.State.Active() {
		return model.Task{}, model.TaskAssignment{}, 0, ErrAssignmentCompleted
	}
	for index := range task.Stages {
		if task.Stages[index].State != model.StageRunning {
			continue
		}
		task.Stages[index].FinishedAt = &now
		task.Stages[index].Summary = input.Summary
		if input.State == model.TaskSucceeded {
			task.Stages[index].State = model.StageSucceeded
		} else if input.State == model.TaskRolledBack {
			task.Stages[index].State = model.StageRolledBack
		} else {
			task.Stages[index].State = model.StageFailed
		}
	}
	stagesJSON, err := encodeJSON(task.Stages)
	if err != nil {
		return model.Task{}, model.TaskAssignment{}, 0, err
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE tasks SET state=?,summary=?,error=?,failure_code=?,retryable=?,
			rollback_available=?,rollback_reason=?,stages_json=?,finished_at=?,heartbeat_at=NULL,
			production_changed=CASE WHEN ? THEN 1 ELSE production_changed END
		WHERE id=? AND state IN (?,?) AND runner_owner=?
	`, input.State, input.Summary, input.Error, input.FailureCode, input.Retryable,
		input.RollbackAvailable, input.RollbackReason, stagesJSON, timeText(now), input.ProductionChanged,
		taskID, model.TaskRunning, model.TaskRollingBack, assignmentOwner(row.assignment.RunnerID, input.Generation))
	if err = requireOne(result, err, "远程任务终态 fencing 失败"); err != nil {
		return model.Task{}, model.TaskAssignment{}, 0, ErrAssignmentFence
	}
	if task.PlanID != "" {
		var observationSeconds int
		if err := tx.QueryRowContext(ctx, `SELECT observation_seconds FROM release_plans WHERE id=? AND task_id=?`, task.PlanID, task.ID).Scan(&observationSeconds); err != nil {
			return model.Task{}, model.TaskAssignment{}, 0, err
		}
		planState := model.PlanNeedsAttention
		closureReason := planClosureReason(input.State, input.Error)
		var startedAt, endsAt, closedAt any
		if input.State == model.TaskSucceeded && observationSeconds > 0 {
			planState = model.PlanObserving
			closureReason = ""
			startedAt, endsAt = timeText(now), timeText(now.Add(time.Duration(observationSeconds)*time.Second))
		} else if input.State == model.TaskSucceeded {
			planState, closureReason, closedAt = model.PlanCompleted, "", timeText(now)
		}
		result, err := tx.ExecContext(ctx, `UPDATE release_plans SET state=?,observation_started_at=?,observation_ends_at=?,closed_at=?,closure_reason=?,updated_at=? WHERE id=? AND task_id=? AND state=?`,
			planState, startedAt, endsAt, closedAt, closureReason, timeText(now), task.PlanID, task.ID, model.PlanExecuting)
		if err = requireOne(result, err, "任务终态无法更新计划状态"); err != nil {
			return model.Task{}, model.TaskAssignment{}, 0, err
		}
	}
	if input.State == model.TaskSucceeded && desired != nil {
		if _, _, _, err := store.setDesiredStateTx(ctx, tx, *desired, now); err != nil {
			return model.Task{}, model.TaskAssignment{}, 0, err
		}
	}
	terminalEvent := model.Event{TaskID: taskID, OccurredAt: now, Level: "info", Phase: "terminal", Message: string(input.State), Data: map[string]any{"state": input.State}}
	if input.State != model.TaskSucceeded {
		terminalEvent.Level = "error"
	}
	dataJSON, err := encodeJSON(terminalEvent.Data)
	if err != nil {
		return model.Task{}, model.TaskAssignment{}, 0, err
	}
	result, err = tx.ExecContext(ctx, `INSERT INTO events(task_id,occurred_at,level,phase,message,data_json) VALUES(?,?,?,?,?,?)`,
		taskID, timeText(now), terminalEvent.Level, terminalEvent.Phase, terminalEvent.Message, dataJSON)
	if err != nil {
		return model.Task{}, model.TaskAssignment{}, 0, err
	}
	terminalEvent.Sequence, err = result.LastInsertId()
	if err != nil {
		return model.Task{}, model.TaskAssignment{}, 0, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_entries(occurred_at,actor_hash,event,resource,outcome,detail_json) VALUES(?,?,?,?,?,?)`,
		timeText(now), task.ActorHash, "task.terminal", taskID, string(input.State), `{"remote":true}`); err != nil {
		return model.Task{}, model.TaskAssignment{}, 0, err
	}
	result, err = tx.ExecContext(ctx, `UPDATE task_assignments SET state=?,lease_expires_at=NULL,completion_digest=?,completion_idempotency_key=?,completion_event_sequence=?,updated_at=? WHERE task_id=? AND runner_id=? AND state=? AND generation=? AND claim_token_hash=? AND lease_expires_at > ? AND execution_deadline_at > ?`,
		model.AssignmentCompleted, digest, input.IdempotencyKey, terminalEvent.Sequence, timeText(now), taskID, runnerID, model.AssignmentClaimed,
		input.Generation, assignmentTokenHash(input.ClaimToken), timeText(now), timeText(now))
	if err = requireOne(result, err, "远程任务分派终态 fencing 失败"); err != nil {
		return model.Task{}, model.TaskAssignment{}, 0, ErrAssignmentFence
	}
	task.State = input.State
	task.Summary, task.Error, task.FailureCode = input.Summary, input.Error, input.FailureCode
	task.Retryable, task.RollbackAvailable, task.RollbackReason = input.Retryable, input.RollbackAvailable, input.RollbackReason
	task.FinishedAt = &now
	row.assignment.State = model.AssignmentCompleted
	row.assignment.ClaimToken = ""
	row.assignment.CompletionDigest, row.assignment.UpdatedAt = digest, now
	row.assignment.CompletionKey, row.assignment.CompletionSequence = input.IdempotencyKey, terminalEvent.Sequence
	row.assignment.LeaseExpiresAt = nil
	if err := tx.Commit(); err != nil {
		return model.Task{}, model.TaskAssignment{}, 0, err
	}
	return task, row.assignment, terminalEvent.Sequence, nil
}

func requireClaimableRunner(ctx context.Context, tx *sql.Tx, runnerID string, now time.Time) (string, error) {
	var serverID, status, leaseExpires string
	var maxConcurrency int
	if err := tx.QueryRowContext(ctx, `SELECT server_id,status,max_concurrency,COALESCE(lease_expires_at,'') FROM runner_nodes WHERE id=?`, runnerID).
		Scan(&serverID, &status, &maxConcurrency, &leaseExpires); err != nil {
		return "", ErrAssignmentFence
	}
	lease, err := nullableAssignmentTime(leaseExpires)
	if err != nil || status != string(model.NodeOnline) || lease == nil || !lease.After(now) {
		return "", ErrAssignmentExpired
	}
	if maxConcurrency > 0 {
		var active int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_assignments WHERE runner_id=? AND state=?`, runnerID, model.AssignmentClaimed).Scan(&active); err != nil {
			return "", err
		}
		if active >= maxConcurrency {
			return "", ErrAssignmentFence
		}
	}
	return serverID, nil
}

// ExpireTaskAssignments is called by a maintenance loop. Assigned tasks can
// be retried by policy; claimed tasks are fail-closed because their remote
// mutation may have happened after the last durable heartbeat.
func (store *Store) ExpireTaskAssignments(ctx context.Context) (int64, error) {
	now := store.now()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT task_id,state FROM task_assignments WHERE (state=? AND execution_deadline_at <= ?) OR (state=? AND (lease_expires_at <= ? OR execution_deadline_at <= ?))`,
		model.AssignmentAssigned, timeText(now), model.AssignmentClaimed, timeText(now), timeText(now))
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var expired int64
	for rows.Next() {
		var taskID, state string
		if err := rows.Scan(&taskID, &state); err != nil {
			return 0, err
		}
		assignmentState := model.AssignmentExpired
		if _, err := tx.ExecContext(ctx, `UPDATE task_assignments SET state=?,claim_token_hash='',lease_expires_at=NULL,updated_at=? WHERE task_id=? AND state=?`, assignmentState, timeText(now), taskID, state); err != nil {
			return 0, err
		}
		if state == string(model.AssignmentAssigned) {
			_, err = tx.ExecContext(ctx, `UPDATE tasks SET state=?,summary=?,error=?,failure_code=?,retryable=1,finished_at=?,heartbeat_at=NULL WHERE id=? AND state=?`, model.TaskFailedRecoverable, "远程任务未在截止时间前领取", "远程 Runner 未领取任务", "remote_assignment_expired", timeText(now), taskID, model.TaskQueued)
		} else {
			_, err = tx.ExecContext(ctx, `UPDATE tasks SET state=?,summary=?,error=?,failure_code=?,retryable=0,finished_at=?,heartbeat_at=NULL WHERE id=? AND state IN (?,?)`, model.TaskNeedsAttention, "远程 Runner 租约过期，必须人工核对", "远程执行结果不确定，禁止自动重试", "remote_claim_expired", timeText(now), taskID, model.TaskRunning, model.TaskRollingBack)
		}
		if err != nil {
			return 0, err
		}
		expired++
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return expired, nil
}
