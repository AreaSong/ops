package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

func (store *Store) SaveComposeRevision(ctx context.Context, revision model.ComposeRevision) error {
	_, _, err := store.SaveComposeRevisionIdempotent(ctx, revision, "")
	return err
}

func (store *Store) ListComposeRevisions(ctx context.Context, service string, limit int) ([]model.ComposeRevision, error) {
	rows, err := store.db.QueryContext(ctx, composeRevisionSelect+` WHERE service=? ORDER BY created_at DESC,id DESC LIMIT ?`, service, clampLimit(limit, 100))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]model.ComposeRevision, 0)
	for rows.Next() {
		item, _, err := scanComposeRevision(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (store *Store) SaveKubernetesOperation(ctx context.Context, operation model.KubernetesOperation, actor, output string) error {
	if operation.ID == "" || operation.Target.Cluster == "" || operation.Target.Context == "" || operation.Target.Namespace == "" {
		return errors.New("Kubernetes 操作信息不完整")
	}
	if operation.CreatedAt.IsZero() {
		operation.CreatedAt = store.now()
	}
	if operation.TenantID == "" {
		operation.TenantID = operation.Target.TenantID
	}
	if operation.TenantID == "" {
		operation.TenantID = "default"
	}
	allowlistJSON, resourceKindsJSON, err := kubernetesTargetJSON(operation.Target)
	if err != nil {
		return err
	}
	resourcesJSON, err := kubernetesResourcesJSON(operation.RolloutResources)
	if err != nil {
		return err
	}
	_, err = store.db.ExecContext(ctx, `INSERT INTO kubernetes_operations(id,actor_hash,tenant_id,cluster,context_name,namespace,action,manifest_digest,dry_run,state,output,error,created_at,finished_at,idempotency_key,request_digest,allowlist_json,resource_kinds_json,phase,rollout_state,rollout_resources_json,rollback_of_plan_id) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		operation.ID, actor, operation.TenantID, operation.Target.Cluster, operation.Target.Context, operation.Target.Namespace,
		operation.Action, operation.ManifestDigest, operation.DryRun, operation.State, output, operation.Error,
		timeText(operation.CreatedAt), nullableTimeText(operation.FinishedAt), operation.IdempotencyKey, operation.RequestDigest,
		allowlistJSON, resourceKindsJSON, operation.Phase, operation.RolloutState, resourcesJSON, operation.RollbackOfPlanID)
	return err
}

// BeginKubernetesOperation durably records a pending operation before kubectl
// is invoked. A repeated idempotency key returns the original operation only
// when both the request digest and actor match.
func (store *Store) BeginKubernetesOperation(
	ctx context.Context,
	operation model.KubernetesOperation,
	actor, requestDigest string,
) (model.KubernetesOperation, string, bool, error) {
	if operation.ID == "" || operation.IdempotencyKey == "" || requestDigest == "" ||
		operation.Target.Cluster == "" || operation.Target.Context == "" || operation.Target.Namespace == "" {
		return model.KubernetesOperation{}, "", false, errors.New("Kubernetes 幂等操作信息不完整")
	}
	if operation.CreatedAt.IsZero() {
		operation.CreatedAt = store.now()
	}
	operation.RequestDigest = requestDigest
	if operation.TenantID == "" {
		operation.TenantID = operation.Target.TenantID
	}
	if operation.TenantID == "" {
		operation.TenantID = "default"
	}
	if operation.State == "" {
		operation.State = "pending"
	}
	allowlistJSON, resourceKindsJSON, err := kubernetesTargetJSON(operation.Target)
	if err != nil {
		return model.KubernetesOperation{}, "", false, err
	}
	resourcesJSON, err := kubernetesResourcesJSON(operation.RolloutResources)
	if err != nil {
		return model.KubernetesOperation{}, "", false, err
	}

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return model.KubernetesOperation{}, "", false, err
	}
	defer tx.Rollback()

	var existing model.KubernetesOperation
	var existingActor, existingOutput string
	err = scanKubernetesOperation(tx.QueryRowContext(ctx, kubernetesOperationSelect+` WHERE idempotency_key=?`, operation.IdempotencyKey), &existing, &existingActor, &existingOutput)
	switch {
	case err == nil:
		if existingActor != actor {
			return model.KubernetesOperation{}, "", false, ErrActorMismatch
		}
		if existing.RequestDigest != requestDigest {
			return model.KubernetesOperation{}, "", false, ErrIdempotency
		}
		if err := tx.Commit(); err != nil {
			return model.KubernetesOperation{}, "", false, err
		}
		return existing, existingOutput, false, nil
	case !errors.Is(err, sql.ErrNoRows):
		return model.KubernetesOperation{}, "", false, err
	}

	_, err = tx.ExecContext(ctx, `INSERT INTO kubernetes_operations(id,actor_hash,tenant_id,cluster,context_name,namespace,action,manifest_digest,dry_run,state,output,error,created_at,finished_at,idempotency_key,request_digest,allowlist_json,resource_kinds_json,phase,rollout_state,rollout_resources_json,rollback_of_plan_id) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		operation.ID, actor, operation.TenantID, operation.Target.Cluster, operation.Target.Context, operation.Target.Namespace,
		operation.Action, operation.ManifestDigest, operation.DryRun, operation.State, "", operation.Error,
		timeText(operation.CreatedAt), nullableTimeText(operation.FinishedAt), operation.IdempotencyKey, requestDigest,
		allowlistJSON, resourceKindsJSON, operation.Phase, operation.RolloutState, resourcesJSON, operation.RollbackOfPlanID)
	if err != nil {
		return model.KubernetesOperation{}, "", false, err
	}
	if err := tx.Commit(); err != nil {
		return model.KubernetesOperation{}, "", false, err
	}
	return operation, "", true, nil
}

// UpdateKubernetesOperation closes or advances a previously persisted
// operation. Terminal states retain a completion timestamp for auditability.
func (store *Store) UpdateKubernetesOperation(
	ctx context.Context,
	id, state, output, operationError string,
) error {
	if id == "" || state == "" {
		return errors.New("Kubernetes 操作状态信息不完整")
	}
	var finished any
	if state == "succeeded" || state == "failed" || state == "needs_attention" {
		finished = timeText(store.now())
	}
	result, err := store.db.ExecContext(ctx, `UPDATE kubernetes_operations SET state=?,output=?,error=?,finished_at=? WHERE id=?`,
		state, output, operationError, finished, id)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return err
	} else if affected != 1 {
		return ErrNotFound
	}
	return nil
}

// UpdateKubernetesOperationRollout records the explicit rollout phase while
// keeping the operation in the same durable state machine as apply/rollback.
func (store *Store) UpdateKubernetesOperationRollout(
	ctx context.Context,
	id, state, phase, rolloutState string,
	resources []string,
	output, operationError string,
) error {
	if id == "" || state == "" || phase == "" || rolloutState == "" {
		return errors.New("Kubernetes rollout 状态信息不完整")
	}
	resourcesJSON, err := kubernetesResourcesJSON(resources)
	if err != nil {
		return err
	}
	var finished any
	if state == "succeeded" || state == "failed" || state == "needs_attention" {
		finished = timeText(store.now())
	}
	result, err := store.db.ExecContext(ctx, `UPDATE kubernetes_operations SET state=?,phase=?,rollout_state=?,rollout_resources_json=?,output=?,error=?,finished_at=? WHERE id=?`,
		state, phase, rolloutState, resourcesJSON, output, operationError, finished, id)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return err
	} else if affected != 1 {
		return ErrNotFound
	}
	return nil
}

func nullableTimeText(value *time.Time) any {
	if value == nil {
		return nil
	}
	return timeText(*value)
}

func (store *Store) GetKubernetesOperation(ctx context.Context, id string) (model.KubernetesOperation, string, error) {
	var op model.KubernetesOperation
	var actor, output string
	err := scanKubernetesOperation(store.db.QueryRowContext(ctx, kubernetesOperationSelect+` WHERE id=?`, id), &op, &actor, &output)
	if errors.Is(err, sql.ErrNoRows) {
		return op, "", ErrNotFound
	}
	if err != nil {
		return op, "", err
	}
	return op, output, nil
}

func (store *Store) GetKubernetesOperationByIdempotency(ctx context.Context, key string) (model.KubernetesOperation, string, error) {
	var op model.KubernetesOperation
	var actor, output string
	err := scanKubernetesOperation(store.db.QueryRowContext(ctx, kubernetesOperationSelect+` WHERE idempotency_key=?`, key), &op, &actor, &output)
	if errors.Is(err, sql.ErrNoRows) {
		return op, "", ErrNotFound
	}
	if err != nil {
		return op, "", err
	}
	return op, output, nil
}

func (store *Store) ListKubernetesOperations(
	ctx context.Context,
	limit int,
) ([]model.KubernetesOperation, error) {
	rows, err := store.db.QueryContext(ctx, kubernetesOperationSelect+` ORDER BY created_at DESC LIMIT ?`, clampLimit(limit, 100))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]model.KubernetesOperation, 0)
	for rows.Next() {
		var item model.KubernetesOperation
		var actor, output string
		if err := scanKubernetesOperation(rows, &item, &actor, &output); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (store *Store) ListKubernetesOperationsForTenant(
	ctx context.Context, tenantID string, limit int,
) ([]model.KubernetesOperation, error) {
	if tenantID == "" {
		tenantID = "default"
	}
	rows, err := store.db.QueryContext(ctx, kubernetesOperationSelect+` WHERE tenant_id=? ORDER BY created_at DESC LIMIT ?`, tenantID, clampLimit(limit, 100))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]model.KubernetesOperation, 0)
	for rows.Next() {
		var item model.KubernetesOperation
		var actor, output string
		if err := scanKubernetesOperation(rows, &item, &actor, &output); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

const kubernetesOperationSelect = `SELECT id,actor_hash,tenant_id,cluster,context_name,namespace,action,
		manifest_digest,dry_run,state,output,error,created_at,finished_at,idempotency_key,request_digest,
	allowlist_json,resource_kinds_json,phase,rollout_state,rollout_resources_json,rollback_of_plan_id
	FROM kubernetes_operations`

type kubernetesRowScanner interface {
	Scan(dest ...any) error
}

func scanKubernetesOperation(
	scanner kubernetesRowScanner,
	operation *model.KubernetesOperation,
	actor, output *string,
) error {
	var tenantID, cluster, contextName, namespace, action, createdAt, allowlistJSON, resourceKindsJSON string
	var phase, rolloutState, rolloutResourcesJSON, rollbackOfPlanID string
	var dryRun int
	var finishedAt sql.NullString
	if err := scanner.Scan(
		&operation.ID, actor, &tenantID, &cluster, &contextName, &namespace, &action,
		&operation.ManifestDigest, &dryRun, &operation.State, output, &operation.Error,
		&createdAt, &finishedAt, &operation.IdempotencyKey, &operation.RequestDigest,
		&allowlistJSON, &resourceKindsJSON, &phase, &rolloutState, &rolloutResourcesJSON, &rollbackOfPlanID,
	); err != nil {
		return err
	}
	operation.TenantID = tenantID
	operation.Target = model.KubernetesTarget{Cluster: cluster, Context: contextName, Namespace: namespace, TenantID: tenantID}
	if allowlistJSON != "" {
		if err := json.Unmarshal([]byte(allowlistJSON), &operation.Target.Allowlist); err != nil {
			return err
		}
	}
	if resourceKindsJSON != "" {
		if err := json.Unmarshal([]byte(resourceKindsJSON), &operation.Target.ResourceKinds); err != nil {
			return err
		}
	}
	if rolloutResourcesJSON != "" {
		if err := json.Unmarshal([]byte(rolloutResourcesJSON), &operation.RolloutResources); err != nil {
			return err
		}
	}
	operation.Action = action
	operation.Phase = phase
	operation.RolloutState = rolloutState
	operation.RollbackOfPlanID = rollbackOfPlanID
	operation.DryRun = dryRun != 0
	var err error
	operation.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return err
	}
	operation.FinishedAt, err = nullableTime(finishedAt)
	return err
}

func kubernetesResourcesJSON(resources []string) (string, error) {
	if resources == nil {
		resources = []string{}
	}
	return encodeJSON(resources)
}

func kubernetesTargetJSON(target model.KubernetesTarget) (string, string, error) {
	allowlist, err := json.Marshal(target.Allowlist)
	if err != nil {
		return "", "", err
	}
	kinds, err := json.Marshal(target.ResourceKinds)
	if err != nil {
		return "", "", err
	}
	return string(allowlist), string(kinds), nil
}
