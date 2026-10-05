package store

import (
	"context"
	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

type RegisteredTaskCompletion struct {
	Mutation                    WorkAdmissionMutation
	State                       model.TaskState
	Summary, Error, FailureCode string
	Retryable                   bool
	Desired                     *DesiredStateInput
}

func (store *Store) CompleteRegisteredTask(ctx context.Context, input RegisteredTaskCompletion) (model.Event, error) {
	if !input.State.Terminal() {
		return model.Event{}, ErrWorkAdmissionConflict
	}
	tx, err := store.beginRegisteredWorkTx(ctx)
	if err != nil {
		return model.Event{}, err
	}
	defer tx.Rollback()
	task, err := checkRegisteredTaskTx(ctx, tx.Tx, input.Mutation)
	if err != nil {
		return model.Event{}, err
	}
	event, err := store.completeTaskTx(ctx, tx.Tx, true, task.ID, input.State, input.Summary, input.Error, input.FailureCode, input.Retryable, false, "",
		model.Event{TaskID: task.ID, Level: "info", Phase: "terminal", Message: string(input.State)},
		model.AuditEntry{ActorHash: task.ActorHash, Event: "task.terminal", Resource: task.ID, Outcome: string(input.State)}, input.Desired)
	if err != nil {
		return model.Event{}, err
	}
	if err = tx.Commit(); err != nil {
		return model.Event{}, err
	}
	return event, nil
}

// 精确静默ID的释放事实和审计同事务持久化；外部确认由原owner协调者完成。
func (store *Store) RecordRegisteredSilenceReleased(ctx context.Context, m WorkAdmissionMutation, silenceID string) error {
	tx, err := store.beginRegisteredWorkTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = checkRegisteredTaskTx(ctx, tx.Tx, m); err != nil {
		return err
	}
	plan, err := scanPlan(tx.QueryRowContext(ctx, planSelect+` WHERE id=?`, m.WorkID))
	if err != nil {
		return err
	}
	if silenceID == "" || plan.MaintenanceSilenceID != silenceID {
		return ErrWorkAdmissionConflict
	}
	if plan.MaintenanceSilenceReleasedAt != nil {
		return nil
	}
	now := store.now()
	result, err := tx.ExecContext(ctx, `UPDATE release_plans SET maintenance_silence_released_at=?,updated_at=? WHERE id=? AND maintenance_silence_id=? AND maintenance_silence_released_at IS NULL`, timeText(now), timeText(now), plan.ID, silenceID)
	if err = requireOne(result, err, "静默释放事实提交失败"); err != nil {
		return err
	}
	if err = appendPlanAudit(ctx, tx.Tx, model.AuditEntry{ActorHash: m.ActorHash, Event: "plan.maintenance_silence_released", Resource: plan.ID, Outcome: "released", Detail: map[string]any{"silenceId": silenceID}}, now); err != nil {
		return err
	}
	return tx.Commit()
}

type RegisteredPlanClose struct {
	Work           WorkAdmissionClose
	Authority      model.ReleaseAuthority
	IdempotencyKey string
}

func (store *Store) ClosePlanAndRegisteredWork(ctx context.Context, input RegisteredPlanClose) (model.ReleasePlan, error) {
	tx, err := store.beginRegisteredWorkTx(ctx)
	if err != nil {
		return model.ReleasePlan{}, err
	}
	defer tx.Rollback()
	m := input.Work.Mutation
	task, err := checkRegisteredTaskTx(ctx, tx.Tx, m)
	if err != nil {
		return model.ReleasePlan{}, err
	}
	plan, err := scanPlan(tx.QueryRowContext(ctx, planSelect+` WHERE id=?`, m.WorkID))
	if err != nil {
		return plan, err
	}
	if input.Work.CloseKind != "settled" || (plan.MaintenanceSilenceID != "" && plan.MaintenanceSilenceReleasedAt == nil) {
		return plan, ErrWorkAdmissionConflict
	}
	now := store.now()
	if task.State == model.TaskSucceeded {
		if input.IdempotencyKey == "" || plan.State != model.PlanObserving || plan.ObservationEndsAt == nil || now.Before(*plan.ObservationEndsAt) || input.Authority.ActorHash != plan.ActorHash {
			return plan, ErrWorkAdmissionConflict
		}
		if err = validateReleaseAuthorityTx(ctx, tx.Tx, input.Authority); err != nil {
			return plan, err
		}
		if err = validateReleaseObjectPermissionsTx(ctx, tx.Tx, input.Authority, plan.ApprovalSummary.Lifecycle.TargetObjects); err != nil {
			return plan, err
		}
		result, e := tx.ExecContext(ctx, `UPDATE release_plans SET state=?,closure_reason='',blocking_alert_fingerprints_json='[]',closure_idempotency_key=?,closed_at=?,updated_at=? WHERE id=? AND state=? AND task_id=?`, model.PlanCompleted, input.IdempotencyKey, timeText(now), timeText(now), plan.ID, model.PlanObserving, task.ID)
		if err = requireOne(result, e, "计划收口失败"); err != nil {
			return plan, err
		}
		if err = appendPlanAudit(ctx, tx.Tx, model.AuditEntry{ActorHash: input.Authority.ActorHash, Event: "plan.closed", Resource: plan.ID, Outcome: "completed", Detail: map[string]any{"taskId": task.ID, "attemptId": input.IdempotencyKey}}, now); err != nil {
			return plan, err
		}
		plan.State = model.PlanCompleted
		plan.ClosedAt = &now
		plan.UpdatedAt = now
		plan.ClosureReason = ""
		plan.BlockingAlertFingerprints = nil
	} else if plan.State != model.PlanNeedsAttention {
		return plan, ErrWorkAdmissionConflict
	}
	if _, err = store.closeRegisteredWorkTx(ctx, tx.Tx, input.Work); err != nil {
		return model.ReleasePlan{}, err
	}
	if err = tx.Commit(); err != nil {
		return model.ReleasePlan{}, err
	}
	return plan, nil
}

type RegisteredClosureBlocker struct {
	Mutation               WorkAdmissionMutation
	Authority              model.ReleaseAuthority
	IdempotencyKey, Reason string
	Fingerprints           []string
}

// 明确结束阻断也是新尝试凭据的签发点，必须复验原owner及当前完整目标授权。
func (store *Store) RecordRegisteredClosureBlocker(ctx context.Context, input RegisteredClosureBlocker) error {
	tx, err := store.beginRegisteredWorkTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	task, err := checkRegisteredTaskTx(ctx, tx.Tx, input.Mutation)
	if err != nil {
		return err
	}
	plan, err := scanPlan(tx.QueryRowContext(ctx, planSelect+` WHERE id=?`, task.PlanID))
	if err != nil {
		return err
	}
	if task.State != model.TaskSucceeded || plan.State != model.PlanObserving || input.IdempotencyKey == "" || input.Authority.ActorHash != plan.ActorHash {
		return ErrWorkAdmissionConflict
	}
	if err = validateReleaseAuthorityTx(ctx, tx.Tx, input.Authority); err != nil {
		return err
	}
	if err = validateReleaseObjectPermissionsTx(ctx, tx.Tx, input.Authority, plan.ApprovalSummary.Lifecycle.TargetObjects); err != nil {
		return err
	}
	fingerprints, err := encodeJSON(input.Fingerprints)
	if err != nil {
		return err
	}
	now := store.now()
	result, err := tx.ExecContext(ctx, `UPDATE release_plans SET closure_reason=?,blocking_alert_fingerprints_json=?,updated_at=? WHERE id=? AND state=? AND task_id=?`, input.Reason, fingerprints, timeText(now), plan.ID, model.PlanObserving, task.ID)
	if err = requireOne(result, err, "收口阻断事实提交失败"); err != nil {
		return err
	}
	audit := model.AuditEntry{ActorHash: input.Authority.ActorHash, Event: "plan.close_rejected", Resource: plan.ID, Outcome: "rejected", Detail: map[string]any{"reason": input.Reason, "attemptId": input.IdempotencyKey, "attemptState": "finished"}}
	if err = appendPlanAudit(ctx, tx.Tx, audit, now); err != nil {
		return err
	}
	return tx.Commit()
}
