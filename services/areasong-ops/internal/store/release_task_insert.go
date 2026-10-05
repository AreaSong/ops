package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

// 任务、计划、事件和审计由调用方的同一事务提交。
func (store *Store) insertPlanTaskTx(ctx context.Context, tx *sql.Tx, plan model.ReleasePlan, actorHash, idempotencyKey, taskID string, silence *model.MaintenanceSilence) (TaskStartResult, error) {
	requestHash := HashConfirmation(plan.ID + "\x00" + plan.Digest)
	var activeID string
	err := tx.QueryRowContext(ctx, `
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
	return TaskStartResult{Task: task, QueuedEvent: queued, Created: true}, nil
}
