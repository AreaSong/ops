package store

import (
	"context"
	"database/sql"
	"errors"
	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

type ReleaseWorkInput struct {
	Admission WorkAdmissionInput
	Authority model.ReleaseAuthority
}

// 从持久化创建证明重建完整准入请求，不能信调用方传入的计划或目标子集。
func (store *Store) releasePlanForExecutionTx(ctx context.Context, tx *sql.Tx, id string, authority model.ReleaseAuthority) (model.ReleasePlan, error) {
	plan, err := scanPlan(tx.QueryRowContext(ctx, planSelect+` WHERE id=?`, id))
	if err != nil {
		return plan, err
	}
	if err = validatePreparedPlanTx(ctx, tx, plan); err != nil {
		return plan, err
	}
	if plan.Action != "update" || plan.RestoreMode != "" || plan.ApprovalSummary.AutoUpdatePolicy != nil || !plan.HasRequiredApprovalPolicy() || !plan.AllowsExecutor(authority.ActorHash) || plan.ApprovedAt == nil || plan.ApprovedByHash == "" {
		return plan, model.ErrReleaseLifecycle
	}
	if plan.RequiresDualApproval && !model.UsesTwoPartyApproval(plan.ApprovalPolicy) && (plan.SecondApprovedByHash == "" || plan.SecondApprovedByHash == plan.ApprovedByHash) {
		return plan, model.ErrReleaseLifecycle
	}
	if plan.State != model.PlanApproved && plan.State != model.PlanScheduled {
		return plan, ErrWorkAdmissionConflict
	}
	if plan.ScheduleAt != nil && store.now().Before(*plan.ScheduleAt) {
		return plan, errors.New("发布计划尚未到达调度时间")
	}
	if err = validateReleaseAuthorityTx(ctx, tx, authority); err != nil {
		return plan, err
	}
	if err = validateReleaseObjectPermissionsTx(ctx, tx, authority, plan.ApprovalSummary.Lifecycle.TargetObjects); err != nil {
		return plan, err
	}
	return plan, nil
}

func (store *Store) AdmitReleaseWork(ctx context.Context, input ReleaseWorkInput) (model.WorkAdmission, bool, error) {
	tx, err := store.beginRegisteredWorkTx(ctx)
	if err != nil {
		return model.WorkAdmission{}, false, err
	}
	defer tx.Rollback()
	plan, err := store.releasePlanForExecutionTx(ctx, tx.Tx, input.Admission.Request.WorkID, input.Authority)
	if err != nil {
		return model.WorkAdmission{}, false, err
	}
	targets, err := plan.ApprovalSummary.Lifecycle.WorkTargets()
	if err != nil {
		return model.WorkAdmission{}, false, err
	}
	expected := model.WorkAdmissionRequest{Version: 1, Kind: model.WorkAdmissionKind, WorkID: plan.ID, IdempotencyKey: input.Admission.Request.IdempotencyKey, ActorHash: input.Authority.ActorHash, ApprovalDigest: plan.Digest, Targets: targets}
	_, actual, _, err := model.CanonicalWorkRequest(input.Admission.Request)
	_, wanted, _, wantErr := model.CanonicalWorkRequest(expected)
	if err != nil || wantErr != nil || actual != wanted {
		return model.WorkAdmission{}, false, model.ErrReleaseLifecycle
	}
	record, created, err := store.admitWorkTx(ctx, tx.Tx, input.Admission)
	if err != nil {
		return model.WorkAdmission{}, false, err
	}
	if err = tx.Commit(); err != nil {
		return model.WorkAdmission{}, false, err
	}
	return record, created, nil
}

type RegisteredPlanTaskInput struct {
	Mutation  WorkAdmissionMutation
	Authority model.ReleaseAuthority
	TaskID    string
	Silence   *model.MaintenanceSilence
}

func (store *Store) StartRegisteredPlanTaskWithEvent(ctx context.Context, input RegisteredPlanTaskInput) (TaskStartResult, model.WorkAdmission, error) {
	tx, err := store.beginRegisteredWorkTx(ctx)
	if err != nil {
		return TaskStartResult{}, model.WorkAdmission{}, err
	}
	defer tx.Rollback()
	m := input.Mutation
	plan, err := store.releasePlanForExecutionTx(ctx, tx.Tx, m.WorkID, input.Authority)
	if err != nil {
		return TaskStartResult{}, model.WorkAdmission{}, err
	}
	record, err := checkRegisteredOwnerTx(ctx, tx.Tx, m)
	if err != nil {
		return TaskStartResult{}, record, err
	}
	targets, err := plan.ApprovalSummary.Lifecycle.WorkTargets()
	if err != nil || !sameReleaseJSON(targets, record.Request.Targets) || plan.Digest != m.ApprovalDigest || input.Authority.ActorHash != m.ActorHash || record.State != model.WorkPreparing {
		return TaskStartResult{}, record, ErrWorkAdmissionConflict
	}
	// 到期激活也是本次 preparing 执行权内的写入，并与入队共同提交。
	if plan.State == model.PlanScheduled {
		result, e := tx.ExecContext(ctx, `UPDATE release_plans SET state=? WHERE id=? AND state=?`, model.PlanApproved, plan.ID, model.PlanScheduled)
		if e = requireOne(result, e, "定时计划无法激活"); e != nil {
			return TaskStartResult{}, record, e
		}
		plan.State = model.PlanApproved
	}
	started, err := store.insertPlanTaskTx(ctx, tx.Tx, plan, m.ActorHash, m.IdempotencyKey, input.TaskID, input.Silence)
	if err != nil {
		return TaskStartResult{}, record, err
	}
	record, err = store.bindRegisteredWorkTaskTx(ctx, tx.Tx, m, input.TaskID)
	if err != nil {
		return TaskStartResult{}, record, err
	}
	if err = tx.Commit(); err != nil {
		return TaskStartResult{}, model.WorkAdmission{}, err
	}
	return started, record, nil
}

func checkRegisteredOwnerTx(ctx context.Context, tx *sql.Tx, m WorkAdmissionMutation) (model.WorkAdmission, error) {
	record, owner, _, err := readWorkAdmissionTx(ctx, tx, m.ID)
	if err != nil {
		return record, err
	}
	if err = matchWorkOwner(record, owner, m); err != nil {
		return record, err
	}
	return record, matchWorkRevision(record, m)
}

func checkRegisteredTaskTx(ctx context.Context, tx *sql.Tx, m WorkAdmissionMutation) (model.Task, error) {
	record, err := checkRegisteredOwnerTx(ctx, tx, m)
	if err != nil {
		return model.Task{}, err
	}
	if record.State != model.WorkTaskBound {
		return model.Task{}, ErrWorkAdmissionConflict
	}
	task, err := registeredTaskTx(ctx, tx, record, m.TaskID)
	if err != nil {
		return task, err
	}
	plan, err := scanPlan(tx.QueryRowContext(ctx, planSelect+` WHERE id=?`, m.WorkID))
	if err != nil {
		return task, err
	}
	if plan.TaskID != task.ID || plan.Digest != m.ApprovalDigest || plan.ApprovalSummary.Lifecycle == nil {
		return task, ErrWorkAdmissionConflict
	}
	if err = validatePreparedPlanTx(ctx, tx, plan); err != nil {
		return task, err
	}
	targets, err := plan.ApprovalSummary.Lifecycle.WorkTargets()
	if err != nil || !sameReleaseJSON(targets, record.Request.Targets) {
		return task, ErrWorkAdmissionConflict
	}
	return task, nil
}

func (store *Store) CheckRegisteredTask(ctx context.Context, m WorkAdmissionMutation) (model.Task, error) {
	tx, err := store.beginRegisteredWorkTx(ctx)
	if err != nil {
		return model.Task{}, err
	}
	defer tx.Rollback()
	task, err := checkRegisteredTaskTx(ctx, tx.Tx, m)
	if err != nil {
		return task, err
	}
	return task, tx.Commit()
}

func (store *Store) MarkRegisteredRunningOwned(ctx context.Context, m WorkAdmissionMutation, phase, runnerOwner string, authority model.ReleaseAuthority) error {
	tx, err := store.beginRegisteredWorkTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	task, err := checkRegisteredTaskTx(ctx, tx.Tx, m)
	if err != nil {
		return err
	}
	if task.State != model.TaskQueued {
		return ErrWorkAdmissionConflict
	}
	if authority.ActorHash != task.ActorHash {
		return ErrActorMismatch
	}
	plan, err := scanPlan(tx.QueryRowContext(ctx, planSelect+` WHERE id=?`, task.PlanID))
	if err != nil {
		return err
	}
	if err = validateReleaseAuthorityTx(ctx, tx.Tx, authority); err != nil {
		return err
	}
	if err = validateReleaseObjectPermissionsTx(ctx, tx.Tx, authority, plan.ApprovalSummary.Lifecycle.TargetObjects); err != nil {
		return err
	}
	now := timeText(store.now())
	result, err := tx.ExecContext(ctx, `UPDATE tasks SET state=?,current_phase=?,started_at=?,heartbeat_at=?,runner_owner=? WHERE id=? AND state=?`, model.TaskRunning, phase, now, now, runnerOwner, task.ID, model.TaskQueued)
	if err = requireOne(result, err, "登记任务无法进入运行状态"); err != nil {
		return err
	}
	return tx.Commit()
}

// preparing权的提交再次绑定最新批准/创建证明和当前授权；核心通用入口不能替代此边界。
func (store *Store) BeginReleaseWorkPreparation(ctx context.Context, m WorkAdmissionMutation, authority model.ReleaseAuthority) (model.WorkAdmission, bool, error) {
	tx, err := store.beginRegisteredWorkTx(ctx)
	if err != nil {
		return model.WorkAdmission{}, false, err
	}
	defer tx.Rollback()
	plan, err := store.releasePlanForExecutionTx(ctx, tx.Tx, m.WorkID, authority)
	if err != nil {
		return model.WorkAdmission{}, false, err
	}
	record, err := checkRegisteredOwnerTx(ctx, tx.Tx, m)
	if err != nil {
		return record, false, err
	}
	targets, err := plan.ApprovalSummary.Lifecycle.WorkTargets()
	if err != nil || !sameReleaseJSON(targets, record.Request.Targets) || plan.Digest != m.ApprovalDigest || authority.ActorHash != m.ActorHash || record.State != model.WorkAdmitted {
		return record, false, ErrWorkAdmissionConflict
	}
	record, err = store.updateRegisteredWorkTx(ctx, tx.Tx, m, workAdmissionUpdate{State: model.WorkPreparing})
	if err != nil {
		return model.WorkAdmission{}, false, err
	}
	if err = tx.Commit(); err != nil {
		return model.WorkAdmission{}, false, err
	}
	return record, true, nil
}
