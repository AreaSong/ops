package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

var ErrAccessVersion = errors.New("访问策略版本已变化，请重新读取")

// AccessPolicyMutation is the durable unit for a control-plane RBAC change.
// The row mutation, immutable policy snapshot and idempotency receipt are
// committed together so a failed snapshot write cannot leave authorization
// rows ahead of the effective policy (or vice versa).
type AccessPolicyMutation struct {
	Actor              string
	IdempotencyKey     string
	RequestDigest      string
	AccessChangeDigest string
	ExpectedVersion    int64
	Snapshot           model.AccessPolicySnapshot
	Tenants            []model.Tenant
	Roles              []model.Role
	Bindings           []model.RoleBinding
	RemoveTenantIDs    []string
	RemoveRoleIDs      []string
	RemoveBindingIDs   []string
	Audit              *model.AuditEntry
}

// ApplyAccessPolicyMutation applies a complete policy update atomically. An
// ExpectedVersion below zero explicitly disables optimistic concurrency (used
// only by bootstrap); normal control-plane callers pass the version they read.
func (store *Store) ApplyAccessPolicyMutation(
	ctx context.Context,
	mutation AccessPolicyMutation,
) (model.AccessPolicySnapshot, bool, error) {
	if mutation.AccessChangeDigest != "" {
		return model.AccessPolicySnapshot{}, false, errors.New("普通访问策略写入不能携带审批上下文")
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return model.AccessPolicySnapshot{}, false, err
	}
	defer tx.Rollback()
	snapshot, created, err := store.applyAccessPolicyMutationTx(ctx, tx, mutation)
	if err != nil {
		return model.AccessPolicySnapshot{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return model.AccessPolicySnapshot{}, false, err
	}
	return snapshot, created, nil
}

// ApplyAccessChangeMutation commits the policy mutation and its approval
// envelope together. This is deliberately separate from the normal Access API
// path so an executor can lose access.manage as a result of the same change
// without leaving the access_changes row in approved state.
func (store *Store) ApplyAccessChangeMutation(
	ctx context.Context,
	changeID, actor string,
	mutation AccessPolicyMutation,
) (model.AccessChange, error) {
	if changeID == "" || actor == "" || mutation.Actor != actor {
		return model.AccessChange{}, errors.New("访问策略执行身份不一致")
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return model.AccessChange{}, err
	}
	defer tx.Rollback()
	change, _, err := scanAccessChange(tx.QueryRowContext(ctx, accessChangeSelect+` WHERE id=?`, changeID), false)
	if errors.Is(err, sql.ErrNoRows) {
		return model.AccessChange{}, ErrNotFound
	}
	if err != nil {
		return model.AccessChange{}, err
	}
	if change.IdempotencyKey != mutation.IdempotencyKey {
		return model.AccessChange{}, ErrIdempotency
	}
	if mutation.AccessChangeDigest == "" || change.RequestDigest != mutation.AccessChangeDigest {
		return model.AccessChange{}, ErrIdempotency
	}
	if change.State == model.AccessChangeApplied {
		if change.AppliedByHash != actor {
			return model.AccessChange{}, ErrActorMismatch
		}
		// The durable change envelope is the idempotency authority once the
		// change is applied. Do not rebuild or compare the policy digest here:
		// an unrelated policy update between two retries must not turn a
		// successful execution into a false conflict.
		if err := tx.Commit(); err != nil {
			return model.AccessChange{}, err
		}
		return change, nil
	}
	if change.State != model.AccessChangeApproved {
		return model.AccessChange{}, errors.New("访问策略变更尚未完成双人批准")
	}
	if model.UsesTwoPartyApproval(change.ApprovalPolicy) {
		if actor != change.ActorHash || change.ApprovedByHash == "" || change.ApprovedByHash == actor {
			return model.AccessChange{}, errors.New("访问策略变更需要由创建人执行，且批准人必须独立")
		}
	} else if actor == change.ActorHash || actor == change.ApprovedByHash || actor == change.SecondApprovedByHash {
		return model.AccessChange{}, errors.New("访问策略变更执行人必须独立于创建人与批准人")
	}
	snapshot, _, err := store.applyAccessPolicyMutationTx(ctx, tx, mutation)
	if err != nil {
		return model.AccessChange{}, err
	}
	now := store.now()
	result, err := tx.ExecContext(ctx, `UPDATE access_changes
		SET state=?,applied_by_hash=?,applied_policy_digest=?,applied_policy_version=?,applied_at=?,error=''
		WHERE id=? AND state=?`, model.AccessChangeApplied, actor, snapshot.Digest, snapshot.Version,
		timeText(now), changeID, model.AccessChangeApproved)
	if err := requireOne(result, err, "访问策略变更收口失败"); err != nil {
		return model.AccessChange{}, err
	}
	if err := appendPlanAudit(ctx, tx, model.AuditEntry{
		ActorHash: actor, Event: "access.change.applied", Resource: "access/" + changeID,
		Outcome: "accepted", Detail: map[string]any{
			"changeId": changeID, "policyDigest": snapshot.Digest, "policyVersion": snapshot.Version,
		},
	}, now); err != nil {
		return model.AccessChange{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.AccessChange{}, err
	}
	change.State = model.AccessChangeApplied
	change.AppliedByHash = actor
	change.AppliedPolicyDigest = snapshot.Digest
	change.AppliedPolicyVersion = snapshot.Version
	change.AppliedAt = &now
	return change, nil
}

func (store *Store) applyAccessPolicyMutationTx(
	ctx context.Context,
	tx *sql.Tx,
	mutation AccessPolicyMutation,
) (model.AccessPolicySnapshot, bool, error) {
	if mutation.Actor == "" || mutation.IdempotencyKey == "" || mutation.RequestDigest == "" {
		return model.AccessPolicySnapshot{}, false, errors.New("访问策略幂等信息不完整")
	}
	if mutation.Snapshot.PolicyJSON == "" || mutation.Snapshot.Digest == "" {
		return model.AccessPolicySnapshot{}, false, errors.New("访问策略快照信息不完整")
	}
	if mutation.Snapshot.ActorHash == "" {
		mutation.Snapshot.ActorHash = mutation.Actor
	}
	if digestPolicyJSON(mutation.Snapshot.PolicyJSON) != mutation.Snapshot.Digest {
		return model.AccessPolicySnapshot{}, false, errors.New("访问策略快照摘要不匹配")
	}
	var accessChangeDigest string
	err := tx.QueryRowContext(ctx, `SELECT request_digest FROM access_changes WHERE idempotency_key=?`, mutation.IdempotencyKey).
		Scan(&accessChangeDigest)
	if err == nil {
		if mutation.AccessChangeDigest == "" || mutation.AccessChangeDigest != accessChangeDigest {
			return model.AccessPolicySnapshot{}, false, ErrIdempotency
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return model.AccessPolicySnapshot{}, false, err
	} else if mutation.AccessChangeDigest != "" {
		return model.AccessPolicySnapshot{}, false, ErrIdempotency
	}

	var existingActor, existingDigest string
	err = tx.QueryRowContext(ctx, `SELECT actor_hash,request_digest FROM access_mutation_receipts WHERE idempotency_key=?`, mutation.IdempotencyKey).
		Scan(&existingActor, &existingDigest)
	if err == nil {
		if existingActor != mutation.Actor {
			return model.AccessPolicySnapshot{}, false, ErrActorMismatch
		}
		if existingDigest != mutation.RequestDigest {
			return model.AccessPolicySnapshot{}, false, ErrIdempotency
		}
		snapshot, snapshotErr := snapshotByDigestTx(ctx, tx, existingDigest)
		if snapshotErr != nil {
			return model.AccessPolicySnapshot{}, false, snapshotErr
		}
		return snapshot, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return model.AccessPolicySnapshot{}, false, err
	}

	currentVersion, err := accessVersionTx(ctx, tx)
	if err != nil {
		return model.AccessPolicySnapshot{}, false, err
	}
	if mutation.ExpectedVersion >= 0 && currentVersion != mutation.ExpectedVersion {
		return model.AccessPolicySnapshot{}, false, ErrAccessVersion
	}

	for _, tenant := range mutation.Tenants {
		if err := rejectBootstrapCollision(ctx, tx, "tenants", tenant.ID, tenant.CreatedBy); err != nil {
			return model.AccessPolicySnapshot{}, false, err
		}
		if err := store.upsertTenant(ctx, tx, tenant); err != nil {
			return model.AccessPolicySnapshot{}, false, err
		}
	}
	for _, role := range mutation.Roles {
		if role.BuiltIn {
			return model.AccessPolicySnapshot{}, false, errors.New("内置角色不可通过网页修改")
		}
		if err := rejectBootstrapCollision(ctx, tx, "roles", role.ID, role.CreatedBy); err != nil {
			return model.AccessPolicySnapshot{}, false, err
		}
		if err := store.upsertRole(ctx, tx, role); err != nil {
			return model.AccessPolicySnapshot{}, false, err
		}
	}
	for _, binding := range mutation.Bindings {
		if err := rejectBootstrapCollision(ctx, tx, "role_bindings", binding.ID, binding.CreatedBy); err != nil {
			return model.AccessPolicySnapshot{}, false, err
		}
		if binding.CreatedBy == "bootstrap" {
			return model.AccessPolicySnapshot{}, false, errors.New("bootstrap 绑定不可通过网页修改")
		}
		binding.CreatedBy = mutation.Actor
		if err := store.upsertRoleBinding(ctx, tx, binding); err != nil {
			return model.AccessPolicySnapshot{}, false, err
		}
	}
	for _, id := range mutation.RemoveBindingIDs {
		createdBy, found, lookupErr := rowCreatedBy(ctx, tx, "role_bindings", id)
		if lookupErr != nil {
			return model.AccessPolicySnapshot{}, false, lookupErr
		}
		if !found {
			continue
		}
		if createdBy == "bootstrap" {
			return model.AccessPolicySnapshot{}, false, errors.New("bootstrap 绑定不可撤销")
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM role_bindings WHERE id=?`, id); err != nil {
			return model.AccessPolicySnapshot{}, false, err
		}
	}
	for _, id := range mutation.RemoveRoleIDs {
		createdBy, found, lookupErr := rowCreatedBy(ctx, tx, "roles", id)
		if lookupErr != nil {
			return model.AccessPolicySnapshot{}, false, lookupErr
		}
		if !found {
			continue
		}
		if createdBy == "bootstrap" {
			return model.AccessPolicySnapshot{}, false, errors.New("bootstrap 角色不可删除")
		}
		var references int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM role_bindings WHERE role_id=?`, id).Scan(&references); err != nil {
			return model.AccessPolicySnapshot{}, false, err
		}
		if references > 0 {
			return model.AccessPolicySnapshot{}, false, errors.New("角色仍被绑定，不能删除")
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM roles WHERE id=?`, id); err != nil {
			return model.AccessPolicySnapshot{}, false, err
		}
	}
	for _, id := range mutation.RemoveTenantIDs {
		createdBy, found, lookupErr := rowCreatedBy(ctx, tx, "tenants", id)
		if lookupErr != nil {
			return model.AccessPolicySnapshot{}, false, lookupErr
		}
		if !found {
			continue
		}
		if createdBy == "bootstrap" {
			return model.AccessPolicySnapshot{}, false, errors.New("bootstrap 租户不可删除")
		}
		var references int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM role_bindings WHERE tenant_id=?`, id).Scan(&references); err != nil {
			return model.AccessPolicySnapshot{}, false, err
		}
		if references > 0 {
			return model.AccessPolicySnapshot{}, false, errors.New("租户仍有角色绑定，不能删除")
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM tenants WHERE id=?`, id); err != nil {
			return model.AccessPolicySnapshot{}, false, err
		}
	}

	if err := ensurePolicyHasPlatformAdmin(mutation.Snapshot.PolicyJSON); err != nil {
		return model.AccessPolicySnapshot{}, false, err
	}
	mutation.Snapshot.Version = currentVersion + 1
	if mutation.Snapshot.CreatedAt.IsZero() {
		mutation.Snapshot.CreatedAt = store.now()
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO access_policy_snapshots(version,digest,policy_json,actor_hash,created_at) VALUES(?,?,?,?,?)`,
		mutation.Snapshot.Version, mutation.Snapshot.Digest, mutation.Snapshot.PolicyJSON, mutation.Snapshot.ActorHash, timeText(mutation.Snapshot.CreatedAt)); err != nil {
		return model.AccessPolicySnapshot{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO access_mutation_receipts(idempotency_key,actor_hash,request_digest,created_at) VALUES(?,?,?,?)`,
		mutation.IdempotencyKey, mutation.Actor, mutation.RequestDigest, timeText(store.now())); err != nil {
		return model.AccessPolicySnapshot{}, false, err
	}
	if mutation.Audit != nil {
		audit := *mutation.Audit
		if audit.ActorHash == "" {
			audit.ActorHash = mutation.Actor
		}
		if err := appendPlanAudit(ctx, tx, audit, store.now()); err != nil {
			return model.AccessPolicySnapshot{}, false, err
		}
	}
	return mutation.Snapshot, true, nil
}

func accessVersionTx(ctx context.Context, tx *sql.Tx) (int64, error) {
	var version int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version),0) FROM access_policy_snapshots`).Scan(&version); err != nil {
		return 0, err
	}
	return version, nil
}

func latestSnapshotTx(ctx context.Context, tx *sql.Tx) (model.AccessPolicySnapshot, error) {
	var snapshot model.AccessPolicySnapshot
	var created string
	if err := tx.QueryRowContext(ctx, `SELECT version,digest,policy_json,actor_hash,created_at FROM access_policy_snapshots ORDER BY version DESC LIMIT 1`).
		Scan(&snapshot.Version, &snapshot.Digest, &snapshot.PolicyJSON, &snapshot.ActorHash, &created); err != nil {
		return model.AccessPolicySnapshot{}, err
	}
	var parseErr error
	snapshot.CreatedAt, parseErr = time.Parse(time.RFC3339Nano, created)
	return snapshot, parseErr
}

func snapshotByDigestTx(ctx context.Context, tx *sql.Tx, digest string) (model.AccessPolicySnapshot, error) {
	var snapshot model.AccessPolicySnapshot
	var created string
	if err := tx.QueryRowContext(ctx, `SELECT version,digest,policy_json,actor_hash,created_at
		FROM access_policy_snapshots WHERE digest=? ORDER BY version DESC LIMIT 1`, digest).
		Scan(&snapshot.Version, &snapshot.Digest, &snapshot.PolicyJSON, &snapshot.ActorHash, &created); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.AccessPolicySnapshot{}, errors.New("访问策略幂等收据缺少策略快照")
		}
		return model.AccessPolicySnapshot{}, err
	}
	var err error
	snapshot.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	return snapshot, err
}

func rowCreatedBy(ctx context.Context, tx *sql.Tx, table, id string) (string, bool, error) {
	// table is selected only from fixed internal call sites; it is never user
	// input. Keeping it explicit avoids interpolating arbitrary SQL identifiers.
	queries := map[string]string{
		"tenants":       `SELECT created_by FROM tenants WHERE id=?`,
		"roles":         `SELECT created_by FROM roles WHERE id=?`,
		"role_bindings": `SELECT created_by FROM role_bindings WHERE id=?`,
	}
	query, ok := queries[table]
	if !ok {
		return "", false, errors.New("未知访问策略表")
	}
	var createdBy string
	err := tx.QueryRowContext(ctx, query, id).Scan(&createdBy)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return createdBy, err == nil, err
}

func rejectBootstrapCollision(ctx context.Context, tx *sql.Tx, table, id, incomingCreatedBy string) error {
	if id == "" {
		return errors.New("访问策略记录缺少标识")
	}
	createdBy, found, err := rowCreatedBy(ctx, tx, table, id)
	if err != nil || !found {
		return err
	}
	if createdBy == "bootstrap" && incomingCreatedBy != "bootstrap" {
		return errors.New("bootstrap 访问策略记录不可被动态记录覆盖")
	}
	return nil
}

func digestPolicyJSON(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func ensurePolicyHasPlatformAdmin(policyJSON string) error {
	var policy struct {
		Roles map[string]struct {
			Permissions []model.Permission `json:"permissions"`
		} `json:"roles"`
		Principals map[string]struct {
			Roles []string `json:"roles"`
		} `json:"principals"`
		Bindings []struct {
			RoleID    string     `json:"roleId"`
			ExpiresAt *time.Time `json:"expiresAt"`
		} `json:"bindings"`
	}
	if err := json.Unmarshal([]byte(policyJSON), &policy); err != nil {
		return errors.New("访问策略快照 JSON 无效")
	}
	adminRoles := make(map[string]struct{})
	for id, role := range policy.Roles {
		for _, permission := range role.Permissions {
			if permission == model.Permission("*") {
				adminRoles[id] = struct{}{}
				break
			}
		}
	}
	for _, principal := range policy.Principals {
		for _, roleID := range principal.Roles {
			if _, ok := adminRoles[roleID]; ok {
				return nil
			}
		}
	}
	now := time.Now().UTC()
	for _, binding := range policy.Bindings {
		if binding.ExpiresAt != nil && !now.Before(*binding.ExpiresAt) {
			continue
		}
		if _, ok := adminRoles[binding.RoleID]; ok {
			return nil
		}
	}
	return errors.New("不能撤销最后一个平台管理员")
}

func (store *Store) GetAccessPolicySnapshot(
	ctx context.Context,
) (model.AccessPolicySnapshot, bool, error) {
	var snapshot model.AccessPolicySnapshot
	var created string
	err := store.db.QueryRowContext(ctx, `
		SELECT version,digest,policy_json,actor_hash,created_at
		FROM access_policy_snapshots ORDER BY version DESC LIMIT 1`).
		Scan(&snapshot.Version, &snapshot.Digest, &snapshot.PolicyJSON, &snapshot.ActorHash, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return model.AccessPolicySnapshot{}, false, nil
	}
	if err != nil {
		return model.AccessPolicySnapshot{}, false, err
	}
	snapshot.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return model.AccessPolicySnapshot{}, false, err
	}
	return snapshot, true, nil
}

func (store *Store) SaveAccessPolicySnapshot(
	ctx context.Context, snapshot model.AccessPolicySnapshot, expectedVersion int64,
) (model.AccessPolicySnapshot, error) {
	if snapshot.Digest == "" || snapshot.PolicyJSON == "" || snapshot.ActorHash == "" {
		return model.AccessPolicySnapshot{}, errors.New("访问策略快照信息不完整")
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return model.AccessPolicySnapshot{}, err
	}
	defer tx.Rollback()
	var current int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(version),0) FROM access_policy_snapshots`).Scan(&current); err != nil {
		return model.AccessPolicySnapshot{}, err
	}
	if expectedVersion >= 0 && current != expectedVersion {
		return model.AccessPolicySnapshot{}, ErrAccessVersion
	}
	if snapshot.CreatedAt.IsZero() {
		snapshot.CreatedAt = store.now()
	}
	snapshot.Version = current + 1
	if _, err := tx.ExecContext(ctx, `INSERT INTO access_policy_snapshots(version,digest,policy_json,actor_hash,created_at) VALUES(?,?,?,?,?)`,
		snapshot.Version, snapshot.Digest, snapshot.PolicyJSON, snapshot.ActorHash, timeText(snapshot.CreatedAt)); err != nil {
		return model.AccessPolicySnapshot{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.AccessPolicySnapshot{}, err
	}
	return snapshot, nil
}

func (store *Store) EnsureAccessPolicySnapshot(
	ctx context.Context, snapshot model.AccessPolicySnapshot,
) (model.AccessPolicySnapshot, bool, error) {
	current, found, err := store.GetAccessPolicySnapshot(ctx)
	if err != nil || found {
		return current, false, err
	}
	saved, err := store.SaveAccessPolicySnapshot(ctx, snapshot, 0)
	return saved, err == nil, err
}
