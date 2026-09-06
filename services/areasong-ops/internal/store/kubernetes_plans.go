package store

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"slices"
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
		second_approved_by_hash,second_approved_at,requires_dual_approval,approval_policy,operation_id,error,created_at,started_at,finished_at
			,execute_idempotency_key,rollback_of_plan_id,rollback_target_plan_id,source_manifest_digest,executed_by_hash
		) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		plan.ID, plan.IdempotencyKey, plan.RequestDigest, plan.ActorHash, plan.TenantID,
		targetJSON, plan.ManifestDigest, manifest, plan.Action, plan.State, confirmationHash,
		plan.ConfirmationPhrase, plan.ApprovedByHash, nullableTimeText(plan.ApprovedAt),
		plan.SecondApprovedByHash, nullableTimeText(plan.SecondApprovedAt), plan.RequiresDualApproval, plan.ApprovalPolicy,
		plan.OperationID, plan.Error, timeText(plan.CreatedAt), nullableTimeText(plan.StartedAt), nullableTimeText(plan.FinishedAt), plan.ExecuteIdempotencyKey,
		plan.RollbackOfPlanID, plan.RollbackTargetPlanID, plan.SourceManifestDigest, plan.ExecutedByHash)
	if err != nil {
		return model.KubernetesPlan{}, false, err
	}
	createdEvent := "kubernetes.plan.created"
	if plan.Action == "rollback" {
		createdEvent = "kubernetes.rollback_plan.created"
	}
	if err := appendKubernetesPlanAudit(ctx, tx, plan.ActorHash, createdEvent, "accepted", plan, plan.CreatedAt); err != nil {
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
		if model.UsesTwoPartyApproval(plan.ApprovalPolicy) {
			if plan.State == "approved" && plan.ApprovedByHash == actorHash {
				if err := tx.Commit(); err != nil {
					return model.KubernetesPlan{}, err
				}
				return plan, nil
			}
			if plan.State != "pending_approval" {
				return model.KubernetesPlan{}, errors.New("Kubernetes 计划已完成批准")
			}
			result, execErr := tx.ExecContext(ctx, `UPDATE kubernetes_plans SET state='approved',approved_by_hash=?,approved_at=? WHERE id=? AND state='pending_approval' AND approved_by_hash=''`, actorHash, timeText(now), id)
			if err := requireOne(result, execErr, "Kubernetes 独立批准无法写入"); err != nil {
				return model.KubernetesPlan{}, err
			}
			plan.State, plan.ApprovedByHash, plan.ApprovedAt = "approved", actorHash, &now
			if err := appendKubernetesPlanAudit(ctx, tx, actorHash, "kubernetes.plan.approved", "accepted", plan, now); err != nil {
				return model.KubernetesPlan{}, err
			}
			if err := tx.Commit(); err != nil {
				return model.KubernetesPlan{}, err
			}
			return plan, nil
		}
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
	if err := appendKubernetesPlanAudit(ctx, tx, actorHash, "kubernetes.plan.approved", "accepted", plan, now); err != nil {
		return model.KubernetesPlan{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.KubernetesPlan{}, err
	}
	return plan, nil
}

func (store *Store) StartKubernetesPlan(
	ctx context.Context,
	id, actorHash, idempotencyKey string,
	operation model.KubernetesOperation,
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
		if plan.ExecutedByHash != actorHash {
			return model.KubernetesPlan{}, false, ErrActorMismatch
		}
		if plan.OperationID == operation.ID {
			return plan, false, tx.Commit()
		}
		return model.KubernetesPlan{}, false, ErrIdempotency
	}
	if plan.State != "approved" {
		return model.KubernetesPlan{}, false, errors.New("Kubernetes 计划尚未完成批准")
	}
	if plan.RequiresDualApproval && model.UsesTwoPartyApproval(plan.ApprovalPolicy) {
		if actorHash != plan.ActorHash || plan.ApprovedByHash == "" || plan.ApprovedByHash == actorHash {
			return model.KubernetesPlan{}, false, errors.New("Kubernetes 执行必须由创建人完成，且批准人必须独立")
		}
	} else if plan.RequiresDualApproval && !model.IndependentExecutor(actorHash, plan.ActorHash, plan.ApprovedByHash, plan.SecondApprovedByHash) {
		return model.KubernetesPlan{}, false, errors.New("Kubernetes 执行人必须独立于创建人和两名批准人")
	}
	if err := validateKubernetesPlanOperation(plan, operation); err != nil {
		return model.KubernetesPlan{}, false, err
	}
	now := store.now()
	operation.CreatedAt = now
	result, execErr := tx.ExecContext(ctx, `UPDATE kubernetes_plans SET state='running',operation_id=?,execute_idempotency_key=?,executed_by_hash=?,started_at=? WHERE id=? AND state='approved'`, operation.ID, idempotencyKey, actorHash, timeText(now), id)
	if err := requireOne(result, execErr, "Kubernetes 计划启动失败"); err != nil {
		return model.KubernetesPlan{}, false, err
	}
	if err := insertKubernetesOperation(ctx, tx, operation, actorHash, operation.RequestDigest); err != nil {
		return model.KubernetesPlan{}, false, err
	}
	plan.State, plan.OperationID, plan.ExecuteIdempotencyKey, plan.ExecutedByHash, plan.StartedAt = "running", operation.ID, idempotencyKey, actorHash, &now
	if err := appendKubernetesPlanAudit(ctx, tx, actorHash, "kubernetes.plan.started", "running", plan, now); err != nil {
		return model.KubernetesPlan{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return model.KubernetesPlan{}, false, err
	}
	return plan, true, nil
}

func (store *Store) FinishKubernetesPlan(ctx context.Context, id, state, errorText string) error {
	if state != "succeeded" && state != "failed" && state != "needs_attention" {
		return errors.New("Kubernetes 计划终态无效")
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	plan, _, err := scanKubernetesPlan(tx.QueryRowContext(ctx, kubernetesPlanSelect+` WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if state == "succeeded" {
		if err := requireKubernetesSuccessEvidence(ctx, tx, plan); err != nil {
			return err
		}
	}
	finished := store.now()
	result, updateErr := tx.ExecContext(ctx, `UPDATE kubernetes_plans SET state=?,error=?,finished_at=? WHERE id=? AND state='running'`, state, errorText, timeText(finished), id)
	if err := requireOne(result, updateErr, "Kubernetes 计划终态无法写入"); err != nil {
		return err
	}
	plan.State, plan.Error, plan.FinishedAt = state, errorText, &finished
	event := "kubernetes.plan.failed"
	if state == "succeeded" {
		event = "kubernetes.plan.executed"
	}
	if err := appendKubernetesPlanAudit(ctx, tx, plan.ExecutedByHash, event, state, plan, finished); err != nil {
		return err
	}
	return tx.Commit()
}

func requireKubernetesSuccessEvidence(ctx context.Context, tx *sql.Tx, plan model.KubernetesPlan) error {
	var operation model.KubernetesOperation
	var actor, output string
	err := scanKubernetesOperation(
		tx.QueryRowContext(ctx, kubernetesOperationSelect+` WHERE id=?`, plan.OperationID),
		&operation, &actor, &output,
	)
	if err != nil {
		return fmt.Errorf("Kubernetes 成功操作证据不可用: %w", err)
	}
	if actor != plan.ExecutedByHash || operation.State != "succeeded" ||
		operation.ManifestDigest != plan.ManifestDigest || operation.Action != plan.Action ||
		operation.RollbackOfPlanID != plan.RollbackOfPlanID || operation.TenantID != plan.TenantID ||
		operation.Target.Cluster != plan.Target.Cluster || operation.Target.Context != plan.Target.Context ||
		operation.Target.Namespace != plan.Target.Namespace ||
		(operation.RolloutState != "succeeded" && operation.RolloutState != "not_required") {
		return errors.New("Kubernetes 成功操作证据与计划身份不一致")
	}
	return nil
}

func validateKubernetesPlanOperation(plan model.KubernetesPlan, operation model.KubernetesOperation) error {
	if operation.ID != "plan-"+plan.ID || operation.IdempotencyKey != operation.ID || operation.RequestDigest == "" ||
		operation.ManifestDigest != plan.ManifestDigest || operation.Action != plan.Action ||
		operation.RollbackOfPlanID != plan.RollbackOfPlanID || operation.State != "pending" ||
		operation.TenantID != plan.TenantID || operation.Target.Cluster != plan.Target.Cluster ||
		operation.Target.Context != plan.Target.Context || operation.Target.Namespace != plan.Target.Namespace ||
		!slices.Equal(operation.Target.Allowlist, plan.Target.Allowlist) ||
		!slices.Equal(operation.Target.ResourceKinds, plan.Target.ResourceKinds) {
		return errors.New("Kubernetes 操作与批准计划身份不一致")
	}
	return nil
}

func appendKubernetesPlanAudit(
	ctx context.Context,
	tx *sql.Tx,
	actor, event, outcome string,
	plan model.KubernetesPlan,
	now time.Time,
) error {
	detail := map[string]any{
		"action": plan.Action, "tenantId": plan.TenantID, "cluster": plan.Target.Cluster,
		"namespace": plan.Target.Namespace, "manifestDigest": plan.ManifestDigest,
	}
	if plan.OperationID != "" {
		detail["operationId"] = plan.OperationID
	}
	if plan.RollbackOfPlanID != "" {
		detail["sourcePlanId"] = plan.RollbackOfPlanID
		detail["sourceManifestDigest"] = plan.SourceManifestDigest
		detail["rollbackTargetPlanId"] = plan.RollbackTargetPlanID
	}
	if plan.Error != "" {
		detail["error"] = plan.Error
	}
	return appendPlanAudit(ctx, tx, model.AuditEntry{
		ActorHash: actor, Event: event, Resource: plan.ID, Outcome: outcome, Detail: detail,
	}, now)
}

const kubernetesPlanSelect = `SELECT id,idempotency_key,request_digest,actor_hash,tenant_id,target_json,
		manifest_digest,manifest,action,state,confirmation_phrase,approved_by_hash,approved_at,
	second_approved_by_hash,second_approved_at,requires_dual_approval,approval_policy,operation_id,error,
		created_at,started_at,finished_at,execute_idempotency_key,rollback_of_plan_id,rollback_target_plan_id,source_manifest_digest,executed_by_hash FROM kubernetes_plans`

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
	var rollbackOfPlanID, rollbackTargetPlanID, sourceManifestDigest, executedByHash string
	var dual int
	err := row.Scan(&plan.ID, &plan.IdempotencyKey, &plan.RequestDigest, &plan.ActorHash, &plan.TenantID,
		&targetJSON, &plan.ManifestDigest, &manifest, &plan.Action, &plan.State, &plan.ConfirmationPhrase,
		&plan.ApprovedByHash, &approvedAt, &plan.SecondApprovedByHash, &secondApprovedAt, &dual, &plan.ApprovalPolicy,
		&plan.OperationID, &plan.Error, &createdAt, &startedAt, &finishedAt, &plan.ExecuteIdempotencyKey,
		&rollbackOfPlanID, &rollbackTargetPlanID, &sourceManifestDigest, &executedByHash)
	if err != nil {
		return model.KubernetesPlan{}, "", err
	}
	if err := decodeJSON(targetJSON, &plan.Target); err != nil {
		return model.KubernetesPlan{}, "", fmt.Errorf("解析 Kubernetes 目标失败: %w", err)
	}
	plan.RequiresDualApproval = dual != 0
	plan.RollbackOfPlanID = rollbackOfPlanID
	plan.RollbackTargetPlanID = rollbackTargetPlanID
	plan.SourceManifestDigest = sourceManifestDigest
	plan.ExecutedByHash = executedByHash
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
