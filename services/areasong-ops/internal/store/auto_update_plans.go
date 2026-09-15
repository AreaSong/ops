package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

func (store *Store) invalidateChangedAutoUpdatePlans(ctx context.Context, tx *sql.Tx, actor, service, raw string) error {
	var oldRaw, linked string
	err := tx.QueryRowContext(ctx, `SELECT policy_json,last_plan_id FROM auto_update_policies WHERE service=?`, service).Scan(&oldRaw, &linked)
	if errors.Is(err, sql.ErrNoRows) || err == nil && raw == oldRaw {
		return nil
	}
	if err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, planSelect+` WHERE service=? AND state IN (?,?,?)`, service,
		model.PlanPendingApproval, model.PlanApproved, model.PlanScheduled)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		plan, scanErr := scanPlan(rows)
		if scanErr != nil {
			rows.Close()
			return scanErr
		}
		if plan.ID == linked || plan.ApprovalSummary.AutoUpdatePolicy != nil {
			ids = append(ids, plan.ID)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := store.invalidateAutoUpdatePlan(ctx, tx, actor, id); err != nil {
			return err
		}
	}
	return nil
}

func (store *Store) invalidateAutoUpdatePlan(ctx context.Context, tx *sql.Tx, actor, id string) error {
	now, reason := store.now(), "自动更新策略已变化，需要重新评估"
	_, err := tx.ExecContext(ctx, `UPDATE release_plans SET state=?,invalidated_reason=?,
		approved_by_hash='',second_approved_by_hash='',approved_at=NULL,updated_at=?
		WHERE id=? AND state IN (?,?,?)`, model.PlanInvalidated, reason, timeText(now), id,
		model.PlanPendingApproval, model.PlanApproved, model.PlanScheduled)
	if err != nil {
		return err
	}
	return appendPlanAudit(ctx, tx, model.AuditEntry{ActorHash: actor, Event: "plan.invalidated", Resource: id,
		Outcome: string(model.PlanInvalidated), Detail: map[string]any{"reason": reason}}, now)
}

// 启动事务内复验，避免检查策略后、任务创建前的配置变更穿透审批。
func verifyAutomaticPlanPolicy(ctx context.Context, tx *sql.Tx, plan model.ReleasePlan) error {
	var raw, linked string
	err := tx.QueryRowContext(ctx, `SELECT policy_json,last_plan_id FROM auto_update_policies WHERE service=?`, plan.Service).Scan(&raw, &linked)
	approved := plan.ApprovalSummary.AutoUpdatePolicy
	if approved == nil {
		if err == nil && linked == plan.ID {
			return errors.New("旧自动更新计划缺少策略绑定")
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		return nil
	}
	if err != nil {
		return errors.New("自动更新策略不可用")
	}
	var current model.AutoUpdatePolicy
	if err := decodeJSON(raw, &current); err != nil {
		return err
	}
	if !current.Enabled || current.Normalized() != *approved {
		return errors.New("自动更新策略已变化，禁止启动旧计划")
	}
	return nil
}
