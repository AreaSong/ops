package store

import (
	"context"
	"errors"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

const AutomaticObservationPhase = "auto-update-observation-baseline"

func (store *Store) AutomaticRollbackPendingTasks(ctx context.Context) ([]model.Task, error) {
	rows, err := store.db.QueryContext(ctx, taskSelect+` WHERE state=? AND plan_id IN
		(SELECT id FROM release_plans WHERE state=? AND approval_summary_json LIKE '%"autoUpdatePolicy":%')`,
		model.TaskRollingBack, model.PlanExecuting)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tasks []model.Task
	for rows.Next() {
		task, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	return tasks, rows.Err()
}

func (store *Store) BlockAutomaticObservation(ctx context.Context, plan model.ReleasePlan, reason string) error {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE release_plans SET state=?,closure_reason=?,updated_at=? WHERE id=? AND state=? AND digest=?`,
		model.PlanNeedsAttention, reason, timeText(store.now()), plan.ID, model.PlanObserving, plan.Digest)
	if err = requireOne(result, err, "自动更新观察状态已变化"); err != nil {
		return err
	}
	if err := appendPlanAudit(ctx, tx, model.AuditEntry{ActorHash: plan.ActorHash, Event: "auto_update.observation.blocked",
		Resource: plan.ID, Outcome: "needs_attention", Detail: map[string]any{"reason": reason}}, store.now()); err != nil {
		return err
	}
	return tx.Commit()
}

func (store *Store) AutomaticObservationPlans(ctx context.Context, now time.Time) ([]model.ReleasePlan, error) {
	rows, err := store.db.QueryContext(ctx, planSelect+` WHERE state=? AND observation_ends_at>? ORDER BY created_at`,
		model.PlanObserving, timeText(now))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var plans []model.ReleasePlan
	for rows.Next() {
		plan, err := scanPlan(rows)
		if err != nil {
			return nil, err
		}
		if plan.ApprovalSummary.AutoUpdatePolicy != nil {
			plans = append(plans, plan)
		}
	}
	return plans, rows.Err()
}

func (store *Store) AutomaticObservationBaseline(ctx context.Context, taskID string) (map[string]any, error) {
	var raw string
	err := store.db.QueryRowContext(ctx, `SELECT data_json FROM events WHERE task_id=? AND phase=? ORDER BY sequence DESC LIMIT 1`,
		taskID, AutomaticObservationPhase).Scan(&raw)
	if err != nil {
		return nil, err
	}
	var baseline map[string]any
	err = decodeJSON(raw, &baseline)
	return baseline, err
}

type AutomaticRollbackInput struct {
	PlanID string
	Digest string
	Phase  string
	Owner  string
	Reason string
}

// 已成功任务只能通过这个受审批计划约束的事务进入观察期回滚。
func (store *Store) StartAutomaticRollback(ctx context.Context, input AutomaticRollbackInput) (model.Task, bool, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return model.Task{}, false, err
	}
	defer tx.Rollback()
	plan, err := scanPlan(tx.QueryRowContext(ctx, planSelect+` WHERE id=?`, input.PlanID))
	if err != nil {
		return model.Task{}, false, err
	}
	if plan.State != model.PlanObserving {
		return model.Task{}, false, nil
	}
	policy := plan.ApprovalSummary.AutoUpdatePolicy
	if policy == nil || !policy.RollbackOnAlert || !policy.RequireBackup || plan.Digest != input.Digest ||
		plan.ObservationEndsAt == nil || !store.now().Before(*plan.ObservationEndsAt) ||
		!plan.HasRequiredApprovalPolicy() || !plan.AllowsExecutor(plan.ExecutedByHash) {
		return model.Task{}, false, errors.New("观察期回滚缺少有效审批或已经过期")
	}
	allowedPhase := false
	for _, semantics := range plan.ApprovalSummary.PhaseSemantics {
		allowedPhase = allowedPhase || semantics.FailurePolicy == "rollback" && semantics.RecoveryPhase == input.Phase
	}
	if !allowedPhase || input.Phase == "" || input.Owner == "" {
		return model.Task{}, false, errors.New("回滚阶段不在批准合同中")
	}
	task, err := scanTask(tx.QueryRowContext(ctx, taskSelect+` WHERE id=?`, plan.TaskID))
	if err != nil || task.State != model.TaskSucceeded || task.PlanDigest != plan.Digest || task.PlanID != plan.ID {
		return model.Task{}, false, errors.New("观察期任务身份或状态不一致")
	}
	var conflicts int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks WHERE service=? AND id<>? AND
		(state IN (?,?,?,?) OR (created_at>? AND production_changed=1))`, task.Service, task.ID,
		model.TaskWaitingConfirmation, model.TaskQueued, model.TaskRunning, model.TaskRollingBack, timeText(task.CreatedAt)).Scan(&conflicts); err != nil {
		return model.Task{}, false, err
	}
	if conflicts != 0 {
		return model.Task{}, false, errors.New("服务已有活动任务或后续变更，不能回滚旧更新")
	}
	now := store.now()
	task.Stages = append(task.Stages, model.TaskStage{Name: input.Phase, State: model.StageRunning, StartedAt: &now})
	stages, err := encodeJSON(task.Stages)
	if err != nil {
		return model.Task{}, false, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE tasks SET state=?,current_phase=?,stages_json=?,runner_owner=?,heartbeat_at=?,finished_at=NULL
		WHERE id=? AND state=?`, model.TaskRollingBack, input.Phase, stages, input.Owner, timeText(now), task.ID, model.TaskSucceeded)
	if err = requireOne(result, err, "观察期任务无法进入回滚状态"); err != nil {
		return model.Task{}, false, err
	}
	result, err = tx.ExecContext(ctx, `UPDATE release_plans SET state=?,closure_reason=?,updated_at=? WHERE id=? AND state=?`,
		model.PlanExecuting, input.Reason, timeText(now), plan.ID, model.PlanObserving)
	if err = requireOne(result, err, "观察期计划已变化"); err != nil {
		return model.Task{}, false, err
	}
	if err := appendPlanAudit(ctx, tx, model.AuditEntry{ActorHash: plan.ActorHash, Event: "auto_update.rollback.started",
		Resource: plan.ID, Outcome: "started", Detail: map[string]any{"taskId": task.ID, "reason": input.Reason, "phase": input.Phase}}, now); err != nil {
		return model.Task{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return model.Task{}, false, err
	}
	task.State, task.CurrentPhase, task.RunnerOwner = model.TaskRollingBack, input.Phase, input.Owner
	return task, true, nil
}
