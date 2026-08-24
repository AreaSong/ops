package store

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

// CreateKubernetesPlan persists the exact manifest behind a digest. The
// manifest is never copied into the public model, but retaining it in the
// root-owned SQLite database allows an approved plan to survive a Runner
// restart without accepting a changed request body.
func (store *Store) CreateKubernetesPlan(
	ctx context.Context,
	plan model.KubernetesPlan,
	manifest string,
	confirmationHash string,
) (model.KubernetesPlan, bool, error) {
	if plan.ID == "" || plan.IdempotencyKey == "" || plan.RequestDigest == "" ||
		plan.ActorHash == "" || plan.ManifestDigest == "" || manifest == "" {
		return model.KubernetesPlan{}, false, errors.New("Kubernetes 计划信息不完整")
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return model.KubernetesPlan{}, false, err
	}
	defer tx.Rollback()
	existing, existingActor, existingManifest, found, err := kubernetesPlanByKey(ctx, tx, plan.IdempotencyKey)
	if err != nil {
		return model.KubernetesPlan{}, false, err
	}
	if found {
		if existingActor != plan.ActorHash || existing.RequestDigest != plan.RequestDigest || existingManifest != manifest {
			return model.KubernetesPlan{}, false, ErrIdempotency
		}
		if err := tx.Commit(); err != nil {
			return model.KubernetesPlan{}, false, err
		}
		return existing, false, nil
	}
	targetJSON, err := encodeJSON(plan.Target)
	if err != nil {
		return model.KubernetesPlan{}, false, err
	}
	if plan.CreatedAt.IsZero() {
		plan.CreatedAt = store.now()
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO kubernetes_plans(
		id,idempotency_key,request_digest,actor_hash,tenant_id,target_json,manifest_digest,manifest,
		action,state,confirmation_hash,confirmation_phrase,approved_by_hash,approved_at,
		second_approved_by_hash,second_approved_at,requires_dual_approval,operation_id,error,created_at,started_at,finished_at
		,execute_idempotency_key
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		plan.ID, plan.IdempotencyKey, plan.RequestDigest, plan.ActorHash, plan.TenantID,
		targetJSON, plan.ManifestDigest, manifest, plan.Action, plan.State, confirmationHash,
		plan.ConfirmationPhrase, plan.ApprovedByHash, nullableTimeText(plan.ApprovedAt),
		plan.SecondApprovedByHash, nullableTimeText(plan.SecondApprovedAt), plan.RequiresDualApproval,
		plan.OperationID, plan.Error, timeText(plan.CreatedAt), nullableTimeText(plan.StartedAt), nullableTimeText(plan.FinishedAt), plan.ExecuteIdempotencyKey)
	if err != nil {
		return model.KubernetesPlan{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return model.KubernetesPlan{}, false, err
	}
	return plan, true, nil
}

func (store *Store) GetKubernetesPlan(ctx context.Context, id string) (model.KubernetesPlan, error) {
	plan, _, err := store.getKubernetesPlan(ctx, id)
	return plan, err
}

func (store *Store) GetKubernetesPlanWithManifest(ctx context.Context, id string) (model.KubernetesPlan, string, error) {
	return store.getKubernetesPlan(ctx, id)
}

func (store *Store) ListKubernetesPlans(ctx context.Context, tenantID string, limit int) ([]model.KubernetesPlan, error) {
	if tenantID == "" {
		tenantID = "default"
	}
	rows, err := store.db.QueryContext(ctx, kubernetesPlanSelect+` WHERE tenant_id=? ORDER BY created_at DESC LIMIT ?`, tenantID, clampLimit(limit, 200))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]model.KubernetesPlan, 0)
	for rows.Next() {
		plan, _, err := scanKubernetesPlan(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, plan)
	}
	return result, rows.Err()
}

func (store *Store) ApproveKubernetesPlan(
	ctx context.Context,
	id, actorHash, digest, confirmation string,
) (model.KubernetesPlan, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return model.KubernetesPlan{}, err
	}
	defer tx.Rollback()
	plan, _, err := scanKubernetesPlan(tx.QueryRowContext(ctx, kubernetesPlanSelect+` WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return model.KubernetesPlan{}, ErrNotFound
	}
	if err != nil {
		return model.KubernetesPlan{}, err
	}
	if plan.ManifestDigest != digest {
		return model.KubernetesPlan{}, errors.New("Kubernetes 计划摘要已变化，批准失效")
	}
	var expectedConfirmationHash string
	if err := tx.QueryRowContext(ctx, `SELECT confirmation_hash FROM kubernetes_plans WHERE id=?`, id).Scan(&expectedConfirmationHash); err != nil {
		return model.KubernetesPlan{}, err
	}
	if subtle.ConstantTimeCompare([]byte(expectedConfirmationHash), []byte(HashConfirmation(confirmation))) != 1 {
		return model.KubernetesPlan{}, ErrConfirmation
	}
	if plan.State != "pending_approval" && plan.State != "approved" {
		return model.KubernetesPlan{}, errors.New("Kubernetes 计划当前不能批准")
	}
	if plan.ActorHash == actorHash {
		return model.KubernetesPlan{}, errors.New("Kubernetes 计划创建人不能批准自己的计划")
	}
	now := store.now()
	if plan.RequiresDualApproval {
		if plan.State == "approved" {
			return model.KubernetesPlan{}, errors.New("Kubernetes 计划已完成双人批准")
		}
		if plan.ApprovedByHash == "" {
			result, execErr := tx.ExecContext(ctx, `UPDATE kubernetes_plans SET approved_by_hash=?,approved_at=? WHERE id=? AND state='pending_approval' AND approved_by_hash=''`, actorHash, timeText(now), id)
			if err := requireOne(result, execErr, "Kubernetes 第一批准无法写入"); err != nil {
				return model.KubernetesPlan{}, err
			}
			plan.ApprovedByHash, plan.ApprovedAt = actorHash, &now
		} else {
			if plan.ApprovedByHash == actorHash {
				return model.KubernetesPlan{}, ErrActorMismatch
			}
			result, execErr := tx.ExecContext(ctx, `UPDATE kubernetes_plans SET state='approved',second_approved_by_hash=?,second_approved_at=? WHERE id=? AND state='pending_approval' AND approved_by_hash!=? AND second_approved_by_hash=''`, actorHash, timeText(now), id, actorHash)
			if err := requireOne(result, execErr, "Kubernetes 第二批准无法写入"); err != nil {
				return model.KubernetesPlan{}, err
			}
			plan.State, plan.SecondApprovedByHash, plan.SecondApprovedAt = "approved", actorHash, &now
		}
	} else {
		result, execErr := tx.ExecContext(ctx, `UPDATE kubernetes_plans SET state='approved',approved_by_hash=?,approved_at=? WHERE id=? AND state='pending_approval'`, actorHash, timeText(now), id)
		if err := requireOne(result, execErr, "Kubernetes 计划批准失败"); err != nil {
			return model.KubernetesPlan{}, err
		}
		plan.State, plan.ApprovedByHash, plan.ApprovedAt = "approved", actorHash, &now
	}
	if err := tx.Commit(); err != nil {
		return model.KubernetesPlan{}, err
	}
	return plan, nil
}

func (store *Store) StartKubernetesPlan(
	ctx context.Context,
	id, operationID, actorHash, idempotencyKey string,
) (model.KubernetesPlan, bool, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return model.KubernetesPlan{}, false, err
	}
	defer tx.Rollback()
	plan, _, err := scanKubernetesPlan(tx.QueryRowContext(ctx, kubernetesPlanSelect+` WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return model.KubernetesPlan{}, false, ErrNotFound
	}
	if err != nil {
		return model.KubernetesPlan{}, false, err
	}
	if plan.State == "running" || plan.State == "succeeded" || plan.State == "needs_attention" || plan.State == "failed" {
		if plan.ExecuteIdempotencyKey != "" && plan.ExecuteIdempotencyKey != idempotencyKey {
			return model.KubernetesPlan{}, false, ErrIdempotency
		}
		if plan.OperationID == operationID || plan.OperationID == idempotencyKey {
			return plan, false, tx.Commit()
		}
		return model.KubernetesPlan{}, false, ErrIdempotency
	}
	if plan.State != "approved" {
		return model.KubernetesPlan{}, false, errors.New("Kubernetes 计划尚未完成批准")
	}
	if plan.RequiresDualApproval && (plan.ApprovedByHash == "" || plan.SecondApprovedByHash == "" || actorHash == plan.ApprovedByHash || actorHash == plan.SecondApprovedByHash) {
		return model.KubernetesPlan{}, false, errors.New("Kubernetes 执行人必须独立于两名批准人")
	}
	now := store.now()
	result, execErr := tx.ExecContext(ctx, `UPDATE kubernetes_plans SET state='running',operation_id=?,execute_idempotency_key=?,started_at=? WHERE id=? AND state='approved'`, operationID, idempotencyKey, timeText(now), id)
	if err := requireOne(result, execErr, "Kubernetes 计划启动失败"); err != nil {
		return model.KubernetesPlan{}, false, err
	}
	plan.State, plan.OperationID, plan.ExecuteIdempotencyKey, plan.StartedAt = "running", operationID, idempotencyKey, &now
	if err := tx.Commit(); err != nil {
		return model.KubernetesPlan{}, false, err
	}
	return plan, true, nil
}

func (store *Store) FinishKubernetesPlan(ctx context.Context, id, state, errorText string) error {
	if state != "succeeded" && state != "failed" && state != "needs_attention" {
		return errors.New("Kubernetes 计划终态无效")
	}
	finished := store.now()
	_, err := store.db.ExecContext(ctx, `UPDATE kubernetes_plans SET state=?,error=?,finished_at=? WHERE id=? AND state='running'`, state, errorText, timeText(finished), id)
	return err
}

func (store *Store) SetKubernetesPlanOperationID(ctx context.Context, id, operationID string) error {
	if id == "" || operationID == "" {
		return errors.New("Kubernetes 计划操作标识不能为空")
	}
	_, err := store.db.ExecContext(ctx, `UPDATE kubernetes_plans SET operation_id=? WHERE id=? AND state IN ('running','succeeded','failed','needs_attention')`, operationID, id)
	return err
}

const kubernetesPlanSelect = `SELECT id,idempotency_key,request_digest,actor_hash,tenant_id,target_json,
		manifest_digest,manifest,action,state,confirmation_phrase,approved_by_hash,approved_at,
	second_approved_by_hash,second_approved_at,requires_dual_approval,operation_id,error,
	created_at,started_at,finished_at,execute_idempotency_key FROM kubernetes_plans`

func (store *Store) getKubernetesPlan(ctx context.Context, id string) (model.KubernetesPlan, string, error) {
	plan, manifest, err := scanKubernetesPlan(store.db.QueryRowContext(ctx, kubernetesPlanSelect+` WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return model.KubernetesPlan{}, "", ErrNotFound
	}
	return plan, manifest, err
}

type kubernetesPlanScanner interface{ Scan(...any) error }

func scanKubernetesPlan(row kubernetesPlanScanner) (model.KubernetesPlan, string, error) {
	var plan model.KubernetesPlan
	var targetJSON, manifest string
	var approvedAt, secondApprovedAt, createdAt, startedAt, finishedAt sql.NullString
	var dual int
	err := row.Scan(&plan.ID, &plan.IdempotencyKey, &plan.RequestDigest, &plan.ActorHash, &plan.TenantID,
		&targetJSON, &plan.ManifestDigest, &manifest, &plan.Action, &plan.State, &plan.ConfirmationPhrase,
		&plan.ApprovedByHash, &approvedAt, &plan.SecondApprovedByHash, &secondApprovedAt, &dual,
		&plan.OperationID, &plan.Error, &createdAt, &startedAt, &finishedAt, &plan.ExecuteIdempotencyKey)
	if err != nil {
		return model.KubernetesPlan{}, "", err
	}
	if err := decodeJSON(targetJSON, &plan.Target); err != nil {
		return model.KubernetesPlan{}, "", fmt.Errorf("解析 Kubernetes 目标失败: %w", err)
	}
	plan.RequiresDualApproval = dual != 0
	var parseErr error
	if plan.ApprovedAt, parseErr = nullableTime(approvedAt); parseErr != nil {
		return model.KubernetesPlan{}, "", parseErr
	}
	if plan.SecondApprovedAt, parseErr = nullableTime(secondApprovedAt); parseErr != nil {
		return model.KubernetesPlan{}, "", parseErr
	}
	if plan.CreatedAt, parseErr = time.Parse(time.RFC3339Nano, createdAt.String); parseErr != nil {
		return model.KubernetesPlan{}, "", parseErr
	}
	if plan.StartedAt, parseErr = nullableTime(startedAt); parseErr != nil {
		return model.KubernetesPlan{}, "", parseErr
	}
	if plan.FinishedAt, parseErr = nullableTime(finishedAt); parseErr != nil {
		return model.KubernetesPlan{}, "", parseErr
	}
	return plan, manifest, nil
}

func kubernetesPlanByKey(ctx context.Context, db interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, key string) (model.KubernetesPlan, string, string, bool, error) {
	plan, manifest, err := scanKubernetesPlan(db.QueryRowContext(ctx, kubernetesPlanSelect+` WHERE idempotency_key=?`, key))
	if errors.Is(err, sql.ErrNoRows) {
		return model.KubernetesPlan{}, "", "", false, nil
	}
	if err != nil {
		return model.KubernetesPlan{}, "", "", false, err
	}
	return plan, plan.ActorHash, manifest, true, nil
}
