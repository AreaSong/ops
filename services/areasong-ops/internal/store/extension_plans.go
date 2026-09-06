package store

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

const extensionPlanSelect = `SELECT id,idempotency_key,request_digest,plan_digest,actor_hash,
	tenant_id,object_id,extension_id,extension_version,extension_digest,publisher,
	manifest_digest,policy_digest,sandbox,input_json,input_digest,timeout_seconds,max_package_bytes,
	max_input_bytes,max_output_bytes,max_memory_pages,state,confirmation_hash,confirmation_phrase,approved_by_hash,
	second_approved_by_hash,approval_policy,executed_by_hash,execution_idempotency_key,output,exit_code,error,
	created_at,expires_at,approved_at,second_approved_at,started_at,finished_at FROM extension_plans`

type storedExtensionPlan struct {
	plan         model.ExtensionPlan
	input        string
	confirmation string
}

func (store *Store) CreateExtensionPlan(
	ctx context.Context,
	plan model.ExtensionPlan,
	input string,
) (model.ExtensionPlan, bool, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return model.ExtensionPlan{}, false, err
	}
	defer tx.Rollback()
	existing, found, err := extensionPlanByIdempotency(ctx, tx, plan.IdempotencyKey)
	if err != nil {
		return model.ExtensionPlan{}, false, err
	}
	if found {
		if existing.plan.ActorHash != plan.ActorHash || existing.plan.RequestDigest != plan.RequestDigest {
			return model.ExtensionPlan{}, false, ErrIdempotency
		}
		return existing.plan, false, nil
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO extension_plans(
		id,idempotency_key,request_digest,plan_digest,actor_hash,tenant_id,object_id,
		extension_id,extension_version,extension_digest,publisher,manifest_digest,policy_digest,
		sandbox,input_json,input_digest,timeout_seconds,max_package_bytes,max_input_bytes,
		max_output_bytes,max_memory_pages,state,confirmation_hash,confirmation_phrase,approval_policy,created_at,expires_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		plan.ID, plan.IdempotencyKey, plan.RequestDigest, plan.PlanDigest, plan.ActorHash,
		plan.TenantID, plan.ObjectID, plan.ExtensionID, plan.ExtensionVersion,
		plan.ExtensionDigest, plan.Publisher, plan.ManifestDigest, plan.PolicyDigest,
		plan.Sandbox, input, plan.InputDigest, plan.TimeoutSeconds, plan.MaxPackageBytes,
		plan.MaxInputBytes, plan.MaxOutputBytes, plan.MaxMemoryPages,
		plan.State, HashConfirmation(plan.ConfirmationPhrase), plan.ConfirmationPhrase, plan.ApprovalPolicy,
		timeText(plan.CreatedAt), timeText(plan.ExpiresAt))
	if err != nil {
		return model.ExtensionPlan{}, false, err
	}
	if err := appendExtensionPlanAudit(ctx, tx, plan.ActorHash, "extension.plan.created",
		"pending_approval", plan, plan.CreatedAt); err != nil {
		return model.ExtensionPlan{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return model.ExtensionPlan{}, false, err
	}
	return plan, true, nil
}

func (store *Store) GetExtensionPlanWithInput(
	ctx context.Context,
	id string,
) (model.ExtensionPlan, string, error) {
	stored, found, err := extensionPlanByID(ctx, store.db, id)
	if err != nil {
		return model.ExtensionPlan{}, "", err
	}
	if !found {
		return model.ExtensionPlan{}, "", ErrNotFound
	}
	return stored.plan, stored.input, nil
}

func (store *Store) ListExtensionPlans(
	ctx context.Context,
	tenantID string,
	limit int,
) ([]model.ExtensionPlan, error) {
	rows, err := store.db.QueryContext(ctx, extensionPlanSelect+`
		WHERE tenant_id=? ORDER BY created_at DESC LIMIT ?`, tenantID, clampLimit(limit, 200))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]model.ExtensionPlan, 0)
	for rows.Next() {
		item, _, err := scanExtensionPlan(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, item.plan)
	}
	return result, rows.Err()
}

func (store *Store) ExpireExtensionPlans(ctx context.Context, now time.Time) error {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	plans, err := extensionPlansForTransition(ctx, tx,
		`state IN ('pending_approval','pending_second_approval','approved') AND expires_at<=?`, timeText(now))
	if err != nil {
		return err
	}
	for _, stored := range plans {
		result, updateErr := tx.ExecContext(ctx, `UPDATE extension_plans SET state='expired',finished_at=?
			WHERE id=? AND state=? AND expires_at<=?`, timeText(now), stored.plan.ID, stored.plan.State, timeText(now))
		if updateErr != nil {
			return updateErr
		}
		if affected, _ := result.RowsAffected(); affected != 1 {
			return errors.New("扩展计划过期状态已变化")
		}
		stored.plan.State, stored.plan.FinishedAt = "expired", &now
		if err := appendExtensionPlanAudit(ctx, tx, "system", "extension.plan.expired",
			"expired", stored.plan, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (store *Store) RecoverInterruptedExtensionPlans(ctx context.Context) error {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	plans, err := extensionPlansForTransition(ctx, tx, `state='running'`)
	if err != nil {
		return err
	}
	now := store.now()
	for _, stored := range plans {
		message := "扩展执行被 Runner 重启中断，结果无法证明"
		result, updateErr := tx.ExecContext(ctx, `UPDATE extension_plans
			SET state='needs_attention',error=?,finished_at=? WHERE id=? AND state='running'`,
			message, timeText(now), stored.plan.ID)
		if updateErr != nil {
			return updateErr
		}
		if affected, _ := result.RowsAffected(); affected != 1 {
			return errors.New("扩展执行恢复状态已变化")
		}
		stored.plan.State, stored.plan.Error, stored.plan.FinishedAt = "needs_attention", message, &now
		if err := appendExtensionPlanAudit(ctx, tx, "system", "extension.plan.recovered",
			"needs_attention", stored.plan, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (store *Store) ApproveExtensionPlan(
	ctx context.Context,
	id, actor, digest, confirmation string,
) (model.ExtensionPlan, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return model.ExtensionPlan{}, err
	}
	defer tx.Rollback()
	stored, found, err := extensionPlanByID(ctx, tx, id)
	if err != nil || !found {
		if err == nil {
			err = ErrNotFound
		}
		return model.ExtensionPlan{}, err
	}
	plan := stored.plan
	// Two-party approvals are terminal at the approval step. Keep an
	// idempotent retry available for the same approver, but do not let any
	// other actor reinterpret an already-approved plan.
	if plan.State == "approved" && model.UsesTwoPartyApproval(plan.ApprovalPolicy) && plan.ApprovedByHash == actor {
		// Confirmation and expiry are still checked below before returning.
	} else if plan.State != "pending_approval" && plan.State != "pending_second_approval" {
		return model.ExtensionPlan{}, errors.New("扩展计划不在待批准状态")
	}
	if !store.now().Before(plan.ExpiresAt) {
		return model.ExtensionPlan{}, errors.New("扩展计划已过期")
	}
	if plan.ActorHash == actor {
		return model.ExtensionPlan{}, errors.New("扩展计划创建人不能批准自己的计划")
	}
	if subtle.ConstantTimeCompare([]byte(plan.PlanDigest), []byte(digest)) != 1 {
		return model.ExtensionPlan{}, errors.New("扩展计划摘要已变化，批准失效")
	}
	if subtle.ConstantTimeCompare([]byte(stored.confirmation), []byte(HashConfirmation(confirmation))) != 1 {
		return model.ExtensionPlan{}, ErrConfirmation
	}
	now := store.now()
	if model.UsesTwoPartyApproval(plan.ApprovalPolicy) {
		if plan.State == "approved" && plan.ApprovedByHash == actor {
			if err := tx.Commit(); err != nil {
				return model.ExtensionPlan{}, err
			}
			return plan, nil
		}
		if plan.State != "pending_approval" {
			return model.ExtensionPlan{}, errors.New("扩展计划已完成批准或执行")
		}
		result, updateErr := tx.ExecContext(ctx, `UPDATE extension_plans SET state='approved',approved_by_hash=?,approved_at=? WHERE id=? AND state='pending_approval'`, actor, timeText(now), id)
		if updateErr != nil {
			return model.ExtensionPlan{}, updateErr
		}
		if affected, _ := result.RowsAffected(); affected != 1 {
			return model.ExtensionPlan{}, errors.New("扩展计划状态已变化")
		}
		plan.State, plan.ApprovedByHash, plan.ApprovedAt = "approved", actor, &now
		if err := appendExtensionPlanAudit(ctx, tx, actor, "extension.plan.approved", plan.State, plan, now); err != nil {
			return model.ExtensionPlan{}, err
		}
		if err := tx.Commit(); err != nil {
			return model.ExtensionPlan{}, err
		}
		return plan, nil
	}
	if plan.State == "pending_approval" {
		result, updateErr := tx.ExecContext(ctx, `UPDATE extension_plans
			SET state='pending_second_approval',approved_by_hash=?,approved_at=?
			WHERE id=? AND state='pending_approval'`, actor, timeText(now), id)
		if updateErr != nil {
			return model.ExtensionPlan{}, updateErr
		}
		if affected, _ := result.RowsAffected(); affected != 1 {
			return model.ExtensionPlan{}, errors.New("扩展计划状态已变化")
		}
		plan.State, plan.ApprovedByHash, plan.ApprovedAt = "pending_second_approval", actor, &now
		if err := appendExtensionPlanAudit(ctx, tx, actor, "extension.plan.approved",
			plan.State, plan, now); err != nil {
			return model.ExtensionPlan{}, err
		}
		if err := tx.Commit(); err != nil {
			return model.ExtensionPlan{}, err
		}
		return plan, nil
	}
	if plan.ApprovedByHash == actor {
		return model.ExtensionPlan{}, errors.New("扩展计划两名批准人必须独立")
	}
	result, err := tx.ExecContext(ctx, `UPDATE extension_plans
		SET state='approved',second_approved_by_hash=?,second_approved_at=?
		WHERE id=? AND state='pending_second_approval' AND approved_by_hash<>?`,
		actor, timeText(now), id, actor)
	if err != nil {
		return model.ExtensionPlan{}, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return model.ExtensionPlan{}, errors.New("扩展计划状态已变化")
	}
	plan.State, plan.SecondApprovedByHash, plan.SecondApprovedAt = "approved", actor, &now
	if err := appendExtensionPlanAudit(ctx, tx, actor, "extension.plan.approved",
		plan.State, plan, now); err != nil {
		return model.ExtensionPlan{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.ExtensionPlan{}, err
	}
	return plan, nil
}

func (store *Store) StartExtensionPlan(
	ctx context.Context,
	id, actor, executionKey string,
) (model.ExtensionPlan, string, bool, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return model.ExtensionPlan{}, "", false, err
	}
	defer tx.Rollback()
	stored, found, err := extensionPlanByID(ctx, tx, id)
	if err != nil || !found {
		if err == nil {
			err = ErrNotFound
		}
		return model.ExtensionPlan{}, "", false, err
	}
	plan := stored.plan
	if plan.ExecutionIdempotencyKey != "" {
		if plan.ExecutionIdempotencyKey != executionKey {
			return model.ExtensionPlan{}, "", false, ErrIdempotency
		}
		if plan.ExecutedByHash != actor {
			return model.ExtensionPlan{}, "", false, ErrIdempotency
		}
		return plan, stored.input, false, nil
	}
	if plan.State != "approved" || plan.ApprovedByHash == "" ||
		(!model.UsesTwoPartyApproval(plan.ApprovalPolicy) && (plan.SecondApprovedByHash == "" || plan.ApprovedByHash == plan.SecondApprovedByHash)) {
		return model.ExtensionPlan{}, "", false, errors.New("扩展计划尚未完成两名独立批准")
	}
	if model.UsesTwoPartyApproval(plan.ApprovalPolicy) {
		if actor != plan.ActorHash || actor == plan.ApprovedByHash {
			return model.ExtensionPlan{}, "", false, errors.New("扩展执行必须由创建人完成，且批准人必须独立")
		}
	} else if actor == plan.ActorHash || actor == plan.ApprovedByHash || actor == plan.SecondApprovedByHash {
		return model.ExtensionPlan{}, "", false, errors.New("扩展执行人必须独立于创建人和两名批准人")
	}
	if !store.now().Before(plan.ExpiresAt) {
		return model.ExtensionPlan{}, "", false, errors.New("扩展计划已过期")
	}
	now := store.now()
	result, err := tx.ExecContext(ctx, `UPDATE extension_plans
		SET state='running',executed_by_hash=?,execution_idempotency_key=?,started_at=?
		WHERE id=? AND state='approved'`, actor, executionKey, timeText(now), id)
	if err != nil {
		return model.ExtensionPlan{}, "", false, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return model.ExtensionPlan{}, "", false, errors.New("扩展计划状态已变化")
	}
	plan.State, plan.ExecutedByHash, plan.ExecutionIdempotencyKey, plan.StartedAt =
		"running", actor, executionKey, &now
	if err := appendExtensionPlanAudit(ctx, tx, actor, "extension.plan.started",
		"running", plan, now); err != nil {
		return model.ExtensionPlan{}, "", false, err
	}
	if err := tx.Commit(); err != nil {
		return model.ExtensionPlan{}, "", false, err
	}
	return plan, stored.input, true, nil
}

func (store *Store) FinishExtensionPlan(
	ctx context.Context,
	id, state string,
	exitCode int,
	output, errorText string,
) error {
	if state != "succeeded" && state != "failed" && state != "needs_attention" {
		return errors.New("扩展计划终态无效")
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stored, found, err := extensionPlanByID(ctx, tx, id)
	if err != nil || !found {
		if err == nil {
			err = ErrNotFound
		}
		return err
	}
	now := store.now()
	result, err := tx.ExecContext(ctx, `UPDATE extension_plans
		SET state=?,exit_code=?,output=?,error=?,finished_at=? WHERE id=? AND state='running'`,
		state, exitCode, output, errorText, timeText(now), id)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return errors.New("扩展执行状态无法收口")
	}
	stored.plan.State, stored.plan.ExitCode, stored.plan.Output, stored.plan.Error, stored.plan.FinishedAt =
		state, exitCode, output, errorText, &now
	if err := appendExtensionPlanAudit(ctx, tx, stored.plan.ExecutedByHash,
		"extension.plan.executed", state, stored.plan, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (store *Store) GetStoredExtensionPackage(
	ctx context.Context,
	id, version string,
) (model.ExtensionManifest, string, error) {
	var manifestJSON, path, state string
	err := store.db.QueryRowContext(ctx, `SELECT manifest_json,storage_path,state
		FROM extension_packages WHERE package_id=? AND version=?`, id, version).
		Scan(&manifestJSON, &path, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return model.ExtensionManifest{}, "", ErrNotFound
	}
	if err != nil {
		return model.ExtensionManifest{}, "", err
	}
	if state != "stored" {
		return model.ExtensionManifest{}, "", errors.New("扩展包未处于可执行状态")
	}
	var manifest model.ExtensionManifest
	if err := json.Unmarshal([]byte(manifestJSON), &manifest); err != nil {
		return model.ExtensionManifest{}, "", err
	}
	return manifest, path, nil
}

func extensionPlanByID(ctx context.Context, db queryer, id string) (storedExtensionPlan, bool, error) {
	return extensionPlanQuery(ctx, db, extensionPlanSelect+` WHERE id=?`, id)
}

func extensionPlanByIdempotency(ctx context.Context, db queryer, key string) (storedExtensionPlan, bool, error) {
	return extensionPlanQuery(ctx, db, extensionPlanSelect+` WHERE idempotency_key=?`, key)
}

func extensionPlanQuery(ctx context.Context, db queryer, query, value string) (storedExtensionPlan, bool, error) {
	return scanExtensionPlan(db.QueryRowContext(ctx, query, value))
}

func extensionPlansForTransition(
	ctx context.Context,
	tx *sql.Tx,
	predicate string,
	arguments ...any,
) ([]storedExtensionPlan, error) {
	rows, err := tx.QueryContext(ctx, extensionPlanSelect+` WHERE `+predicate, arguments...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var plans []storedExtensionPlan
	for rows.Next() {
		stored, _, err := scanExtensionPlan(rows)
		if err != nil {
			return nil, err
		}
		plans = append(plans, stored)
	}
	return plans, rows.Err()
}

func appendExtensionPlanAudit(
	ctx context.Context,
	tx *sql.Tx,
	actor, event, outcome string,
	plan model.ExtensionPlan,
	now time.Time,
) error {
	return appendPlanAudit(ctx, tx, model.AuditEntry{
		ActorHash: actor, Event: event, Resource: plan.ID, Outcome: outcome,
		Detail: map[string]any{
			"extensionId": plan.ExtensionID, "extensionVersion": plan.ExtensionVersion,
			"extensionDigest": plan.ExtensionDigest, "manifestDigest": plan.ManifestDigest,
			"policyDigest": plan.PolicyDigest, "objectId": plan.ObjectID,
			"tenantId": plan.TenantID, "inputDigest": plan.InputDigest,
		},
	}, now)
}

type extensionPlanScanner interface{ Scan(...any) error }

func scanExtensionPlan(scanner extensionPlanScanner) (storedExtensionPlan, bool, error) {
	var item storedExtensionPlan
	var created, expires string
	var approved, secondApproved, started, finished sql.NullString
	err := scanner.Scan(
		&item.plan.ID, &item.plan.IdempotencyKey, &item.plan.RequestDigest, &item.plan.PlanDigest,
		&item.plan.ActorHash, &item.plan.TenantID, &item.plan.ObjectID, &item.plan.ExtensionID,
		&item.plan.ExtensionVersion, &item.plan.ExtensionDigest, &item.plan.Publisher,
		&item.plan.ManifestDigest, &item.plan.PolicyDigest, &item.plan.Sandbox, &item.input,
		&item.plan.InputDigest, &item.plan.TimeoutSeconds, &item.plan.MaxPackageBytes,
		&item.plan.MaxInputBytes, &item.plan.MaxOutputBytes, &item.plan.MaxMemoryPages,
		&item.plan.State, &item.confirmation,
		&item.plan.ConfirmationPhrase, &item.plan.ApprovedByHash, &item.plan.SecondApprovedByHash, &item.plan.ApprovalPolicy,
		&item.plan.ExecutedByHash, &item.plan.ExecutionIdempotencyKey, &item.plan.Output,
		&item.plan.ExitCode, &item.plan.Error, &created, &expires, &approved, &secondApproved,
		&started, &finished,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return storedExtensionPlan{}, false, nil
	}
	if err != nil {
		return storedExtensionPlan{}, false, err
	}
	item.plan.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err == nil {
		item.plan.ExpiresAt, err = time.Parse(time.RFC3339Nano, expires)
	}
	if err == nil {
		item.plan.ApprovedAt, err = nullableTime(approved)
	}
	if err == nil {
		item.plan.SecondApprovedAt, err = nullableTime(secondApproved)
	}
	if err == nil {
		item.plan.StartedAt, err = nullableTime(started)
	}
	if err == nil {
		item.plan.FinishedAt, err = nullableTime(finished)
	}
	return item, err == nil, err
}
