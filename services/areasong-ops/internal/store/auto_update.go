package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

func (store *Store) UpsertAutoUpdatePolicy(ctx context.Context, policy model.AutoUpdatePolicyView) error {
	if policy.Service == "" || policy.ObjectID == "" || policy.TenantID == "" {
		return errors.New("自动更新策略缺少对象身份")
	}
	raw, err := encodeJSON(model.AutoUpdatePolicy{
		Enabled: policy.Enabled, Channel: policy.Channel,
		MaintenanceWindow: policy.MaintenanceWindow, MaintenanceTimezone: policy.MaintenanceTimezone,
		CanaryPercent:  policy.CanaryPercent,
		MaxUnavailable: policy.MaxUnavailable, RequireBackup: policy.RequireBackup,
		RequireApproval: policy.RequireApproval, RollbackOnAlert: policy.RollbackOnAlert,
		ObservationSeconds: policy.ObservationSeconds,
	})
	if err != nil {
		return err
	}
	return store.upsertAutoUpdatePolicy(ctx, store.db, policy, raw)
}

func (store *Store) upsertAutoUpdatePolicy(ctx context.Context, db accessExecer, policy model.AutoUpdatePolicyView, raw string) error {
	now := store.now()
	_, err := db.ExecContext(ctx, `
		INSERT INTO auto_update_policies(service,object_id,tenant_id,policy_json,last_evaluation_at,next_evaluation_at,last_plan_id,last_error,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?)
		ON CONFLICT(service) DO UPDATE SET object_id=excluded.object_id,tenant_id=excluded.tenant_id,
		 policy_json=excluded.policy_json,updated_at=excluded.updated_at`,
		policy.Service, policy.ObjectID, policy.TenantID, raw, autoNullableTimeText(policy.LastEvaluationAt),
		autoNullableTimeText(policy.NextEvaluationAt), policy.LastPlanID, policy.LastError, timeText(now))
	return err
}

func autoNullableTimeText(value *time.Time) any {
	if value == nil {
		return nil
	}
	return timeText(*value)
}

func (store *Store) ApplyAutoUpdatePolicy(
	ctx context.Context,
	actor, idempotencyKey, requestDigest string,
	policy model.AutoUpdatePolicyView,
	audit model.AuditEntry,
) (bool, error) {
	if actor == "" || idempotencyKey == "" || requestDigest == "" {
		return false, errors.New("自动更新策略幂等信息不完整")
	}
	raw, err := encodeJSON(model.AutoUpdatePolicy{
		Enabled: policy.Enabled, Channel: policy.Channel,
		MaintenanceWindow: policy.MaintenanceWindow, MaintenanceTimezone: policy.MaintenanceTimezone,
		CanaryPercent:  policy.CanaryPercent,
		MaxUnavailable: policy.MaxUnavailable, RequireBackup: policy.RequireBackup,
		RequireApproval: policy.RequireApproval, RollbackOnAlert: policy.RollbackOnAlert,
		ObservationSeconds: policy.ObservationSeconds,
	})
	if err != nil {
		return false, err
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var existingActor, existingDigest string
	err = tx.QueryRowContext(ctx, `SELECT actor_hash,request_digest FROM auto_update_receipts WHERE idempotency_key=?`, idempotencyKey).
		Scan(&existingActor, &existingDigest)
	if err == nil {
		if existingActor != actor {
			return false, ErrActorMismatch
		}
		if existingDigest != requestDigest {
			return false, ErrIdempotency
		}
		return false, tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if err := store.upsertAutoUpdatePolicy(ctx, tx, policy, raw); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO auto_update_receipts(idempotency_key,actor_hash,request_digest,created_at) VALUES(?,?,?,?)`, idempotencyKey, actor, requestDigest, timeText(store.now())); err != nil {
		return false, err
	}
	if audit.Event == "" || audit.Resource == "" {
		return false, errors.New("自动更新策略审计信息不完整")
	}
	if audit.ActorHash == "" {
		audit.ActorHash = actor
	}
	if err := appendPlanAudit(ctx, tx, audit, store.now()); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (store *Store) ListAutoUpdatePolicies(ctx context.Context) ([]model.AutoUpdatePolicyView, error) {
	rows, err := store.db.QueryContext(ctx, `SELECT service,object_id,tenant_id,policy_json,last_evaluation_at,next_evaluation_at,last_plan_id,last_error FROM auto_update_policies ORDER BY service`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]model.AutoUpdatePolicyView, 0)
	for rows.Next() {
		var item model.AutoUpdatePolicyView
		var raw string
		var last, next sql.NullString
		if err := rows.Scan(&item.Service, &item.ObjectID, &item.TenantID, &raw, &last, &next, &item.LastPlanID, &item.LastError); err != nil {
			return nil, err
		}
		var policy model.AutoUpdatePolicy
		if err := decodeJSON(raw, &policy); err != nil {
			return nil, err
		}
		item.Enabled, item.Channel = policy.Enabled, policy.Channel
		item.MaintenanceWindow, item.CanaryPercent = policy.MaintenanceWindow, policy.CanaryPercent
		item.MaintenanceTimezone = policy.MaintenanceTimezone
		if item.MaintenanceTimezone == "" {
			item.MaintenanceTimezone = "UTC"
		}
		item.MaxUnavailable, item.RequireBackup = policy.MaxUnavailable, policy.RequireBackup
		item.RequireApproval, item.RollbackOnAlert = policy.RequireApproval, policy.RollbackOnAlert
		item.ObservationSeconds = policy.ObservationSeconds
		if last.Valid {
			value, parseErr := time.Parse(time.RFC3339Nano, last.String)
			if parseErr != nil {
				return nil, parseErr
			}
			item.LastEvaluationAt = &value
		}
		if next.Valid {
			value, parseErr := time.Parse(time.RFC3339Nano, next.String)
			if parseErr != nil {
				return nil, parseErr
			}
			item.NextEvaluationAt = &value
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (store *Store) GetAutoUpdatePolicy(ctx context.Context, service string) (model.AutoUpdatePolicyView, error) {
	items, err := store.ListAutoUpdatePolicies(ctx)
	if err != nil {
		return model.AutoUpdatePolicyView{}, err
	}
	for _, item := range items {
		if item.Service == service {
			return item, nil
		}
	}
	return model.AutoUpdatePolicyView{}, ErrNotFound
}

func (store *Store) MarkAutoUpdateEvaluation(
	ctx context.Context, service string, evaluatedAt, nextAt *time.Time, planID, lastError string,
) error {
	result, err := store.db.ExecContext(ctx, `UPDATE auto_update_policies SET last_evaluation_at=?,next_evaluation_at=?,last_plan_id=?,last_error=?,updated_at=? WHERE service=?`,
		autoNullableTimeText(evaluatedAt), autoNullableTimeText(nextAt), planID, lastError, timeText(store.now()), service)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return ErrNotFound
	}
	return nil
}
