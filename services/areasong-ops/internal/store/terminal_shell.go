package store

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

type storedTerminalShellPlan struct {
	plan           model.TerminalShellPlan
	idempotencyKey string
	requestDigest  string
	confirmation   string
}

func (store *Store) CreateTerminalShellPlan(
	ctx context.Context, plan model.TerminalShellPlan, idempotencyKey, requestDigest string,
) (model.TerminalShellPlan, bool, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return model.TerminalShellPlan{}, false, err
	}
	defer tx.Rollback()
	existing, found, err := terminalShellPlanByIdempotency(ctx, tx, idempotencyKey)
	if err != nil {
		return model.TerminalShellPlan{}, false, err
	}
	if found {
		if existing.plan.ActorHash != plan.ActorHash || existing.requestDigest != requestDigest {
			return model.TerminalShellPlan{}, false, ErrIdempotency
		}
		return existing.plan, false, nil
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO terminal_shell_plans(
		id,idempotency_key,request_digest,object_id,state,actor_hash,input_digest,
		confirmation_hash,confirmation_phrase,created_at,expires_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?)`, plan.ID, idempotencyKey, requestDigest, plan.ObjectID,
		plan.State, plan.ActorHash, plan.InputDigest, HashConfirmation(plan.ConfirmationPhrase),
		plan.ConfirmationPhrase, timeText(plan.CreatedAt), timeText(plan.ExpiresAt))
	if err != nil {
		return model.TerminalShellPlan{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return model.TerminalShellPlan{}, false, err
	}
	return plan, true, nil
}

func (store *Store) GetTerminalShellPlan(ctx context.Context, id string) (model.TerminalShellPlan, error) {
	stored, found, err := terminalShellPlanByID(ctx, store.db, id)
	if err != nil {
		return model.TerminalShellPlan{}, err
	}
	if !found {
		return model.TerminalShellPlan{}, ErrNotFound
	}
	return stored.plan, nil
}

func (store *Store) ListTerminalShellPlans(ctx context.Context, limit int) ([]model.TerminalShellPlan, error) {
	rows, err := store.db.QueryContext(ctx, `SELECT id,idempotency_key,request_digest,object_id,state,actor_hash,input_digest,
		confirmation_hash,confirmation_phrase,approved_by_hash,execution_idempotency_key,exit_code,output,error,
		created_at,expires_at,approved_at,started_at,finished_at
		FROM terminal_shell_plans ORDER BY created_at DESC LIMIT ?`, clampLimit(limit, 200))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]model.TerminalShellPlan, 0)
	for rows.Next() {
		item, err := scanTerminalShellPlan(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, item.plan)
	}
	return result, rows.Err()
}

func (store *Store) ApproveTerminalShellPlan(
	ctx context.Context, id, actor, confirmation string,
) (model.TerminalShellPlan, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return model.TerminalShellPlan{}, err
	}
	defer tx.Rollback()
	stored, found, err := terminalShellPlanByID(ctx, tx, id)
	if err != nil || !found {
		if err == nil {
			err = ErrNotFound
		}
		return model.TerminalShellPlan{}, err
	}
	if stored.plan.State != "pending_approval" {
		return model.TerminalShellPlan{}, errors.New("紧急终端计划不在待批准状态")
	}
	if stored.plan.ActorHash == actor {
		return model.TerminalShellPlan{}, errors.New("紧急终端创建人不能批准自己的计划")
	}
	if !store.now().Before(stored.plan.ExpiresAt) {
		return model.TerminalShellPlan{}, errors.New("紧急终端计划已过期")
	}
	expected := HashConfirmation(confirmation)
	if subtle.ConstantTimeCompare([]byte(expected), []byte(stored.confirmation)) != 1 {
		return model.TerminalShellPlan{}, ErrConfirmation
	}
	now := store.now()
	result, err := tx.ExecContext(ctx, `UPDATE terminal_shell_plans SET state='approved',approved_by_hash=?,approved_at=? WHERE id=? AND state='pending_approval'`, actor, timeText(now), id)
	if err != nil {
		return model.TerminalShellPlan{}, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return model.TerminalShellPlan{}, errors.New("紧急终端计划状态已变化")
	}
	if err := tx.Commit(); err != nil {
		return model.TerminalShellPlan{}, err
	}
	stored.plan.State, stored.plan.ApprovedByHash, stored.plan.ApprovedAt = "approved", actor, &now
	return stored.plan, nil
}

func (store *Store) StartTerminalShellPlan(
	ctx context.Context, id, actor, executionKey, inputDigest string,
) (model.TerminalShellPlan, bool, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return model.TerminalShellPlan{}, false, err
	}
	defer tx.Rollback()
	stored, found, err := terminalShellPlanByID(ctx, tx, id)
	if err != nil || !found {
		if err == nil {
			err = ErrNotFound
		}
		return model.TerminalShellPlan{}, false, err
	}
	plan := stored.plan
	if plan.ActorHash != actor {
		return model.TerminalShellPlan{}, false, ErrActorMismatch
	}
	if plan.InputDigest != inputDigest {
		return model.TerminalShellPlan{}, false, errors.New("紧急终端输入与批准摘要不一致")
	}
	if plan.ExecutionIdempotencyKey != "" {
		if plan.ExecutionIdempotencyKey != executionKey {
			return model.TerminalShellPlan{}, false, ErrIdempotency
		}
		if plan.State == "running" {
			_, _ = tx.ExecContext(ctx, `UPDATE terminal_shell_plans SET state='needs_attention',error='终端执行中断，结果未知',finished_at=? WHERE id=? AND state='running'`, timeText(store.now()), id)
			if err := tx.Commit(); err != nil {
				return model.TerminalShellPlan{}, false, err
			}
			plan.State, plan.Error = "needs_attention", "终端执行中断，结果未知"
			return plan, false, errors.New(plan.Error)
		}
		return plan, false, nil
	}
	if plan.State != "approved" || plan.ApprovedByHash == "" {
		return model.TerminalShellPlan{}, false, errors.New("紧急终端计划尚未完成独立批准")
	}
	if !store.now().Before(plan.ExpiresAt) {
		return model.TerminalShellPlan{}, false, errors.New("紧急终端计划已过期")
	}
	now := store.now()
	result, err := tx.ExecContext(ctx, `UPDATE terminal_shell_plans SET state='running',execution_idempotency_key=?,started_at=? WHERE id=? AND state='approved'`, executionKey, timeText(now), id)
	if err != nil {
		return model.TerminalShellPlan{}, false, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return model.TerminalShellPlan{}, false, errors.New("紧急终端计划状态已变化")
	}
	if err := tx.Commit(); err != nil {
		return model.TerminalShellPlan{}, false, err
	}
	plan.State, plan.ExecutionIdempotencyKey, plan.StartedAt = "running", executionKey, &now
	return plan, true, nil
}

func (store *Store) FinishTerminalShellPlan(
	ctx context.Context, id, state string, exitCode int, output, errorText string,
) error {
	if state != "succeeded" && state != "failed" && state != "timed_out" && state != "needs_attention" {
		return errors.New("紧急终端终态无效")
	}
	result, err := store.db.ExecContext(ctx, `UPDATE terminal_shell_plans SET state=?,exit_code=?,output=?,error=?,finished_at=? WHERE id=? AND state='running'`,
		state, exitCode, output, errorText, timeText(store.now()), id)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return errors.New("紧急终端执行状态无法收口")
	}
	return nil
}

func terminalShellPlanByID(ctx context.Context, db queryer, id string) (storedTerminalShellPlan, bool, error) {
	return terminalShellPlanQuery(ctx, db, `SELECT id,idempotency_key,request_digest,object_id,state,actor_hash,input_digest,
		confirmation_hash,confirmation_phrase,approved_by_hash,execution_idempotency_key,exit_code,output,error,
		created_at,expires_at,approved_at,started_at,finished_at FROM terminal_shell_plans WHERE id=?`, id)
}

func terminalShellPlanByIdempotency(ctx context.Context, db queryer, key string) (storedTerminalShellPlan, bool, error) {
	return terminalShellPlanQuery(ctx, db, `SELECT id,idempotency_key,request_digest,object_id,state,actor_hash,input_digest,
		confirmation_hash,confirmation_phrase,approved_by_hash,execution_idempotency_key,exit_code,output,error,
		created_at,expires_at,approved_at,started_at,finished_at FROM terminal_shell_plans WHERE idempotency_key=?`, key)
}

func terminalShellPlanQuery(ctx context.Context, db queryer, query, value string) (storedTerminalShellPlan, bool, error) {
	return scanTerminalShellPlanRow(db.QueryRowContext(ctx, query, value))
}

type terminalShellScanner interface{ Scan(...any) error }

func scanTerminalShellPlan(scanner terminalShellScanner) (storedTerminalShellPlan, error) {
	item, _, err := scanTerminalShellPlanScanner(scanner)
	return item, err
}

func scanTerminalShellPlanRow(scanner terminalShellScanner) (storedTerminalShellPlan, bool, error) {
	return scanTerminalShellPlanScanner(scanner)
}

func scanTerminalShellPlanScanner(scanner terminalShellScanner) (storedTerminalShellPlan, bool, error) {
	var item storedTerminalShellPlan
	var created, expires string
	var approved, started, finished sql.NullString
	err := scanner.Scan(&item.plan.ID, &item.idempotencyKey, &item.requestDigest, &item.plan.ObjectID,
		&item.plan.State, &item.plan.ActorHash, &item.plan.InputDigest, &item.confirmation,
		&item.plan.ConfirmationPhrase, &item.plan.ApprovedByHash, &item.plan.ExecutionIdempotencyKey,
		&item.plan.ExitCode, &item.plan.Output, &item.plan.Error, &created, &expires,
		&approved, &started, &finished)
	if errors.Is(err, sql.ErrNoRows) {
		return storedTerminalShellPlan{}, false, nil
	}
	if err != nil {
		return storedTerminalShellPlan{}, false, err
	}
	item.plan.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err == nil {
		item.plan.ExpiresAt, err = time.Parse(time.RFC3339Nano, expires)
	}
	if err == nil {
		item.plan.ApprovedAt, err = nullableTime(approved)
	}
	if err == nil {
		item.plan.StartedAt, err = nullableTime(started)
	}
	if err == nil {
		item.plan.FinishedAt, err = nullableTime(finished)
	}
	return item, err == nil, err
}
