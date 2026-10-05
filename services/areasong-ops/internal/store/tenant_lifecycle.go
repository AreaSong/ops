package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/config"
	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

type tenantLifecycleExecution = accessChangeExecution

// 仅阻断未结束的已登记工作；入口覆盖仍不完整，只允许同包测试调用。
func (store *Store) applyTenantLifecycleChange(ctx context.Context, execution tenantLifecycleExecution) (model.AccessChange, error) {
	if execution.ChangeID == "" || !lifecycleSourceOrdinary(execution.Actor) {
		return model.AccessChange{}, ErrActorMismatch
	}
	tx, err := store.beginRegisteredWorkTx(ctx)
	if err != nil {
		return model.AccessChange{}, err
	}
	defer tx.Rollback()
	change, payload, err := accessChangeForApplyTx(ctx, tx.Tx, execution)
	if err != nil {
		return model.AccessChange{}, err
	}
	if change.State == model.AccessChangeApplied {
		return change, tx.Commit()
	}
	if change.ApprovalPolicy != model.ApprovalPolicyTwoParty || !change.RequiresDualApproval ||
		!lifecycleSourceOrdinary(change.ApprovedByHash) || change.ApprovedAt == nil || change.ApprovedAt.Before(change.CreatedAt) {
		return model.AccessChange{}, errors.New("租户生命周期需要有效的独立双人批准")
	}
	request, err := model.DecodeTenantLifecycleTransition(payload)
	if err != nil {
		return model.AccessChange{}, err
	}
	if request.IdempotencyKey != change.IdempotencyKey {
		return model.AccessChange{}, ErrIdempotency
	}
	snapshot, err := store.transitionTenantTx(ctx, tx.Tx, request, execution)
	if err != nil {
		return model.AccessChange{}, err
	}
	change, err = store.finishAccessChangeTx(ctx, tx.Tx, change, execution.Actor, snapshot)
	if err != nil {
		return model.AccessChange{}, err
	}
	return change, tx.Commit()
}

func (store *Store) transitionTenantTx(ctx context.Context, tx *sql.Tx, request model.TenantLifecycleTransitionRequest, execution tenantLifecycleExecution) (model.AccessPolicySnapshot, error) {
	policy, current, err := lifecyclePolicyTx(ctx, tx)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrTenantLifecycleUnknown
	}
	if err != nil {
		return model.AccessPolicySnapshot{}, err
	}
	if current.Version != request.ExpectedVersion || current.Version == math.MaxInt64 {
		return model.AccessPolicySnapshot{}, ErrAccessVersion
	}
	rows, err := tenantRowsTx(ctx, tx)
	if err != nil {
		return model.AccessPolicySnapshot{}, err
	}
	row, err := lifecycleMirror(policy, rows, request.TenantID)
	if err != nil {
		return model.AccessPolicySnapshot{}, err
	}
	if err := validateLifecycleTarget(policy, row, request); err != nil {
		return model.AccessPolicySnapshot{}, err
	}
	if err := lifecycleBindingsTx(ctx, tx, policy, row.ID); err != nil {
		return model.AccessPolicySnapshot{}, err
	}
	if request.TargetStatus == "disabled" {
		if err := assertNoUnfinishedRegisteredWorkTx(ctx, tx, row.ID); err != nil {
			return model.AccessPolicySnapshot{}, err
		}
		if err := assertNoUnfinishedPlanPreparationTx(ctx, tx, row.ID); err != nil {
			return model.AccessPolicySnapshot{}, err
		}

	}
	now := store.now()
	tenant := policy.Tenants[row.ID]
	tenant.Status, tenant.UpdatedAt = request.TargetStatus, now
	policy.Tenants[row.ID] = tenant
	if err := lifecycleReferencesExist(policy); err != nil {
		return model.AccessPolicySnapshot{}, err
	}
	policyJSON, err := json.Marshal(policy)
	if err != nil {
		return model.AccessPolicySnapshot{}, err
	}
	if err := ensureLifecyclePlatformAdmin(policy, now); err != nil {
		return model.AccessPolicySnapshot{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE tenants SET status=?,lifecycle_generation=lifecycle_generation+1,updated_at=?
		WHERE id=? AND status=? AND lifecycle_generation=? AND lifecycle_generation>0 AND lifecycle_generation<9223372036854775807`,
		request.TargetStatus, timeText(now), row.ID, row.Status, row.Generation)
	if err != nil {
		return model.AccessPolicySnapshot{}, err
	}
	if n, err := result.RowsAffected(); err != nil {
		return model.AccessPolicySnapshot{}, err
	} else if n != 1 {
		return model.AccessPolicySnapshot{}, ErrTenantLifecycleConflict
	}
	snapshot, _, err := store.writeAccessPolicySnapshotTx(ctx, tx, AccessPolicyMutation{
		Actor: execution.Actor, IdempotencyKey: execution.IdempotencyKey, RequestDigest: digestPolicyJSON(string(policyJSON)),
		Snapshot: model.AccessPolicySnapshot{PolicyJSON: string(policyJSON), Digest: digestPolicyJSON(string(policyJSON)), ActorHash: execution.Actor, CreatedAt: now},
		Audit:    &model.AuditEntry{ActorHash: execution.Actor, Event: "access.policy.updated", Resource: "access", Outcome: "accepted"},
	}, current.Version)
	if err != nil {
		return model.AccessPolicySnapshot{}, err
	}
	err = appendPlanAudit(ctx, tx, model.AuditEntry{ActorHash: execution.Actor, Event: "tenant.lifecycle.changed", Resource: "tenant/" + row.ID, Outcome: "accepted",
		Detail: map[string]any{"changeId": execution.ChangeID, "tenantId": row.ID, "fromStatus": request.ExpectedStatus, "toStatus": request.TargetStatus,
			"fromGeneration": row.Generation, "toGeneration": row.Generation + 1, "policyVersion": snapshot.Version, "policyDigest": snapshot.Digest}}, now)
	return snapshot, err
}

func validateLifecycleTarget(policy config.AccessPolicy, row tenantLifecycleRow, request model.TenantLifecycleTransitionRequest) error {
	if row.ID == "default" || row.ID == policy.DefaultTenant || row.CreatedBy == "bootstrap" || policy.Tenants[row.ID].CreatedBy == "bootstrap" {
		return ErrTenantLifecycleProtected
	}
	if lifecycleStatus(row.Status) != "active" && row.Status != "disabled" {
		return ErrTenantLifecycleUnsupported
	}
	if row.Generation == 0 {
		return ErrTenantLifecycleUnknown
	}
	if row.Generation != request.ExpectedGeneration || row.Generation == math.MaxInt64 || lifecycleStatus(row.Status) != request.ExpectedStatus {
		return ErrTenantLifecycleConflict
	}
	return nil
}

func lifecycleReferencesExist(policy config.AccessPolicy) error {
	for _, principal := range policy.Principals {
		id := principal.TenantID
		if id == "" {
			id = policy.DefaultTenant
		}
		if _, ok := policy.Tenants[id]; !ok {
			return ErrTenantLifecycleUnknown
		}
		for _, role := range principal.Roles {
			if _, ok := policy.Roles[role]; !ok {
				return ErrTenantLifecycleUnknown
			}
		}
	}
	for _, binding := range policy.Bindings {
		if binding.TenantID != "*" {
			if _, ok := policy.Tenants[binding.TenantID]; !ok {
				return ErrTenantLifecycleUnknown
			}
		}
		if _, ok := policy.Roles[binding.RoleID]; !ok {
			return ErrTenantLifecycleUnknown
		}
	}
	return nil
}

// 比较绑定的共同持久化字段；创建/更新时间在旧路径并非一致镜像，不作历史修复。
func lifecycleBindingsTx(ctx context.Context, tx *sql.Tx, policy config.AccessPolicy, id string) error {
	expected := make(map[string]model.RoleBinding)
	for _, binding := range policy.Bindings {
		if binding.TenantID != id {
			continue
		}
		if _, duplicate := expected[binding.ID]; duplicate || binding.ID == "" ||
			(binding.CreatedBy != "bootstrap" && !lifecycleSourceOrdinary(binding.CreatedBy)) {
			return ErrTenantLifecycleUnknown
		}
		expected[binding.ID] = binding
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,subject,tenant_id,role_id,object_ids_json,expires_at,created_by,
		jit,requires_dual_approval,approval_state,approved_by_hash,second_approved_by_hash,approved_at,second_approved_at
		FROM role_bindings WHERE tenant_id=?`, id)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var binding model.RoleBinding
		var objects string
		var expires, approved, second sql.NullString
		if err := rows.Scan(&binding.ID, &binding.Subject, &binding.TenantID, &binding.RoleID, &objects, &expires, &binding.CreatedBy,
			&binding.JIT, &binding.RequiresDualApproval, &binding.ApprovalState, &binding.ApprovedByHash, &binding.SecondApprovedByHash, &approved, &second); err != nil {
			return err
		}
		want, ok := expected[binding.ID]
		if !ok || json.Unmarshal([]byte(objects), &binding.ObjectIDs) != nil {
			return ErrTenantLifecycleUnknown
		}
		if err := bindingMirrorTimes(&binding, expires, approved, second); err != nil {
			return err
		}
		want.CreatedAt, want.UpdatedAt = time.Time{}, time.Time{}
		if !reflect.DeepEqual(binding, want) {
			return ErrTenantLifecycleUnknown
		}
		delete(expected, binding.ID)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(expected) != 0 {
		return ErrTenantLifecycleUnknown
	}
	return nil
}

func bindingMirrorTimes(binding *model.RoleBinding, expires, approved, second sql.NullString) error {
	for _, item := range []struct {
		raw    sql.NullString
		target **time.Time
	}{{expires, &binding.ExpiresAt}, {approved, &binding.ApprovedAt}, {second, &binding.SecondApprovedAt}} {
		if !item.raw.Valid {
			continue
		}
		value, err := time.Parse(time.RFC3339Nano, item.raw.String)
		if err != nil {
			return ErrTenantLifecycleUnknown
		}
		*item.target = &value
	}
	return nil
}

// 普通快照只能保留有效状态；初次配置允许原样承载代次 0 的旧声明。
func guardOrdinaryTenantSnapshotTx(ctx context.Context, tx *sql.Tx, snapshot model.AccessPolicySnapshot) error {
	if err := model.RejectTenantLifecyclePayload(snapshot.PolicyJSON); err != nil {
		return err
	}
	var proposed, before config.AccessPolicy
	if json.Unmarshal([]byte(snapshot.PolicyJSON), &proposed) != nil {
		return ErrTenantLifecycleUnknown
	}
	current, err := latestSnapshotTx(ctx, tx)
	first := errors.Is(err, sql.ErrNoRows)
	if err != nil && !first {
		return err
	}
	if !first && json.Unmarshal([]byte(current.PolicyJSON), &before) != nil {
		return ErrTenantLifecycleUnknown
	}
	rows, err := tenantRowsTx(ctx, tx)
	if err != nil {
		return err
	}
	for id, tenant := range proposed.Tenants {
		status := lifecycleStatus(tenant.Status)
		if old, exists := before.Tenants[id]; exists {
			if status != lifecycleStatus(old.Status) {
				return ErrTenantLifecycleConflict
			}
		} else if status != "active" && !(first && rows[id].CreatedBy == "bootstrap" && rows[id].Generation == 0) {
			return ErrTenantLifecycleUnsupported
		}
		if row, exists := rows[id]; exists && status != lifecycleStatus(row.Status) {
			return ErrTenantLifecycleConflict
		}
	}
	if first {
		return ordinaryReferencesExist(proposed)
	}
	return retainedDisabledReferencesTx(ctx, tx, before, proposed, current, rows)
}

// 初次配置允许非 active 的未知记录，但不能保存明确悬空的主体或绑定。
func ordinaryReferencesExist(policy config.AccessPolicy) error {
	for _, principal := range policy.Principals {
		for _, role := range principal.Roles {
			if _, ok := policy.Roles[role]; !ok {
				return ErrTenantLifecycleUnknown
			}
		}
		id := principal.TenantID
		if id == "" && len(policy.Tenants) == 0 {
			continue
		}
		if id == "" {
			id = policy.DefaultTenant
		}
		if _, ok := policy.Tenants[id]; !ok {
			return ErrTenantLifecycleUnknown
		}
	}
	for _, binding := range policy.Bindings {
		if _, ok := policy.Roles[binding.RoleID]; !ok {
			return ErrTenantLifecycleUnknown
		}
		if binding.TenantID == "*" || (binding.TenantID == "" && len(policy.Tenants) == 0) {
			continue
		}
		if _, ok := policy.Tenants[binding.TenantID]; !ok {
			return ErrTenantLifecycleUnknown
		}
	}
	return nil
}

func retainedDisabledReferencesTx(ctx context.Context, tx *sql.Tx, before, proposed config.AccessPolicy, current model.AccessPolicySnapshot, rows map[string]tenantLifecycleRow) error {
	checked := make(map[string]bool)
	check := func(id string) error {
		if checked[id] {
			return nil
		}
		encoded, err := json.Marshal(before)
		if err != nil || string(encoded) != current.PolicyJSON || digestPolicyJSON(current.PolicyJSON) != current.Digest {
			return ErrTenantLifecycleUnknown
		}
		row, err := lifecycleMirror(before, rows, id)
		if err != nil {
			return err
		}
		if row.Generation <= 0 || row.Status != "disabled" || proposed.Tenants[id].Status != "disabled" {
			return ErrTenantLifecycleUnknown
		}
		if err := lifecycleBindingsTx(ctx, tx, proposed, id); err != nil {
			return err
		}
		checked[id] = true
		return nil
	}
	for subject, principal := range proposed.Principals {
		id := principal.TenantID
		if id == "" {
			id = proposed.DefaultTenant
		}
		for _, role := range principal.Roles {
			if _, ok := proposed.Roles[role]; !ok {
				return ErrTenantLifecycleUnknown
			}
		}
		tenant, exists := proposed.Tenants[id]
		if !exists {
			if principal.TenantID == "" && len(before.Tenants) == 0 && len(proposed.Tenants) == 0 {
				continue
			}
			return ErrTenantLifecycleUnknown
		}
		if lifecycleStatus(tenant.Status) == "active" {
			continue
		}
		if principal.TenantID == "" && before.DefaultTenant != proposed.DefaultTenant {
			return ErrTenantLifecycleConflict
		}
		if old, ok := before.Principals[subject]; !ok || !reflect.DeepEqual(old, principal) {
			return ErrTenantLifecycleConflict
		}
		if err := check(id); err != nil {
			return err
		}
		for _, role := range principal.Roles {
			if _, ok := proposed.Roles[role]; !ok {
				return ErrTenantLifecycleUnknown
			}
		}
	}
	oldBindings := make(map[string]model.RoleBinding)
	for _, binding := range before.Bindings {
		oldBindings[binding.ID] = binding
	}
	for _, binding := range proposed.Bindings {
		if _, ok := proposed.Roles[binding.RoleID]; !ok {
			return ErrTenantLifecycleUnknown
		}
		if binding.TenantID == "*" {
			continue
		}
		tenant, exists := proposed.Tenants[binding.TenantID]
		if !exists {
			if binding.TenantID == "" && len(before.Tenants) == 0 && len(proposed.Tenants) == 0 {
				continue
			}
			return ErrTenantLifecycleUnknown
		}
		if lifecycleStatus(tenant.Status) == "active" {
			continue
		}
		if old, ok := oldBindings[binding.ID]; !ok || !reflect.DeepEqual(old, binding) {
			return ErrTenantLifecycleConflict
		}
		if err := check(binding.TenantID); err != nil {
			return err
		}
		if _, ok := proposed.Roles[binding.RoleID]; !ok {
			return ErrTenantLifecycleUnknown
		}
	}
	return nil
}

func (store *Store) upsertTenant(ctx context.Context, db accessExecer, tenant model.Tenant) error {
	if tenant.ID == "" || tenant.DisplayName == "" {
		return errors.New("租户标识或名称不能为空")
	}
	if tenant.Status == "" {
		tenant.Status = "active"
	}
	if tenant.CreatedAt.IsZero() {
		tenant.CreatedAt = store.now()
	}
	tenant.UpdatedAt = store.now()
	if tenant.CreatedBy == "" {
		tenant.CreatedBy = "bootstrap"
	}
	if tenant.Status != "active" && tenant.Status != "disabled" {
		return ErrTenantLifecycleUnsupported
	}
	// INSERT 与冲突更新均在 SQL 中检查，跨 Store 竞争也不能把停用变成 active。
	result, err := db.ExecContext(ctx, `
		INSERT INTO tenants(id,display_name,status,created_at,updated_at,created_by,lifecycle_generation)
		SELECT ?,?,?,?,?,?,? WHERE ?='active' OR EXISTS
		 (SELECT 1 FROM tenants WHERE id=? AND status='disabled' AND lifecycle_generation>0)
		ON CONFLICT(id) DO UPDATE SET display_name=excluded.display_name,status=excluded.status,updated_at=excluded.updated_at
		WHERE (tenants.status=excluded.status OR (tenants.status='' AND excluded.status='active'))
		 AND (excluded.status='active' OR tenants.lifecycle_generation>0)`,
		tenant.ID, tenant.DisplayName, tenant.Status, timeText(tenant.CreatedAt), timeText(tenant.UpdatedAt), tenant.CreatedBy, initialTenantGeneration(tenant),
		tenant.Status, tenant.ID)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil {
		return err
	} else if n != 1 {
		return ErrTenantLifecycleConflict
	}
	return nil
}
