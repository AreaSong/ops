package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

type accessExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func (store *Store) UpsertTenant(ctx context.Context, tenant model.Tenant) error {
	return store.upsertTenant(ctx, store.db, tenant)
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
	_, err := db.ExecContext(ctx, `
		INSERT INTO tenants(id,display_name,status,created_at,updated_at,created_by) VALUES(?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET display_name=excluded.display_name,status=excluded.status,updated_at=excluded.updated_at`,
		tenant.ID, tenant.DisplayName, tenant.Status, timeText(tenant.CreatedAt), timeText(tenant.UpdatedAt), tenant.CreatedBy)
	return err
}

// EnsureAccessDefaults seeds only inert metadata. It never grants a binding;
// deployments opt into enforcement explicitly through the catalog policy.
func (store *Store) EnsureAccessDefaults(ctx context.Context) error {
	now := store.now()
	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO tenants(id,display_name,status,created_at,updated_at,created_by)
		VALUES(?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING`,
		"default", "Default", "active", timeText(now), timeText(now), "bootstrap"); err != nil {
		return err
	}
	roles := []model.Role{
		{ID: "viewer", DisplayName: "只读观察者", Permissions: []model.Permission{model.PermissionRead, model.PermissionInspect}, BuiltIn: true},
		{ID: "operator", DisplayName: "运维操作员", Permissions: []model.Permission{model.PermissionRead, model.PermissionInspect, model.PermissionLifecycle, model.PermissionRecover}, BuiltIn: true},
		{ID: "release-manager", DisplayName: "发布管理员", Permissions: []model.Permission{model.PermissionRead, model.PermissionInspect, model.PermissionLifecycle, model.PermissionDeploy, model.PermissionBatch, model.PermissionRecover}, BuiltIn: true},
		{ID: "platform-admin", DisplayName: "平台管理员", Permissions: []model.Permission{model.Permission("*"), model.PermissionManageAccess}, BuiltIn: true},
	}
	for _, role := range roles {
		permissions, err := encodeJSON(role.Permissions)
		if err != nil {
			return err
		}
		if _, err := store.db.ExecContext(ctx, `
			INSERT INTO roles(id,display_name,permissions_json,built_in,created_at,updated_at,created_by)
			VALUES(?,?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING`,
			role.ID, role.DisplayName, permissions, role.BuiltIn, timeText(now), timeText(now), "bootstrap"); err != nil {
			return err
		}
	}
	return nil
}

func (store *Store) HasRoleBindings(ctx context.Context) (bool, error) {
	var count int
	err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM role_bindings`).Scan(&count)
	return count > 0, err
}

func (store *Store) ListTenants(ctx context.Context) ([]model.Tenant, error) {
	rows, err := store.db.QueryContext(ctx, `SELECT id,display_name,status,created_at,updated_at,created_by FROM tenants ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]model.Tenant, 0)
	for rows.Next() {
		var item model.Tenant
		var created, updated string
		if err := rows.Scan(&item.ID, &item.DisplayName, &item.Status, &created, &updated, &item.CreatedBy); err != nil {
			return nil, err
		}
		item.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return nil, err
		}
		item.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (store *Store) UpsertRole(ctx context.Context, role model.Role) error {
	return store.upsertRole(ctx, store.db, role)
}

func (store *Store) upsertRole(ctx context.Context, db accessExecer, role model.Role) error {
	if role.ID == "" || role.DisplayName == "" {
		return errors.New("角色标识或名称不能为空")
	}
	permissions, err := encodeJSON(role.Permissions)
	if err != nil {
		return err
	}
	if role.CreatedAt.IsZero() {
		role.CreatedAt = store.now()
	}
	role.UpdatedAt = store.now()
	if role.CreatedBy == "" {
		role.CreatedBy = "bootstrap"
	}
	_, err = db.ExecContext(ctx, `
		INSERT INTO roles(id,display_name,permissions_json,built_in,created_at,updated_at,created_by) VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET display_name=excluded.display_name,permissions_json=excluded.permissions_json,
			built_in=excluded.built_in,updated_at=excluded.updated_at`,
		role.ID, role.DisplayName, permissions, role.BuiltIn, timeText(role.CreatedAt), timeText(role.UpdatedAt), role.CreatedBy)
	return err
}

func (store *Store) ListRoles(ctx context.Context) ([]model.Role, error) {
	rows, err := store.db.QueryContext(ctx, `SELECT id,display_name,permissions_json,built_in,created_at,updated_at,created_by FROM roles ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]model.Role, 0)
	for rows.Next() {
		var item model.Role
		var permissions, created, updated string
		if err := rows.Scan(&item.ID, &item.DisplayName, &permissions, &item.BuiltIn, &created, &updated, &item.CreatedBy); err != nil {
			return nil, err
		}
		if err := decodeJSON(permissions, &item.Permissions); err != nil {
			return nil, err
		}
		if created != "" {
			item.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
			if err != nil {
				return nil, err
			}
		}
		if updated != "" {
			item.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
			if err != nil {
				return nil, err
			}
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (store *Store) UpsertRoleBinding(ctx context.Context, binding model.RoleBinding) error {
	return store.upsertRoleBinding(ctx, store.db, binding)
}

// ApplyAccessMutation applies the complete Access API request in one SQLite
// transaction. The receipt is consumed before any row is changed, so retries
// cannot partially replay a policy update.
func (store *Store) ApplyAccessMutation(
	ctx context.Context,
	actor, idempotencyKey, requestDigest string,
	tenants []model.Tenant,
	roles []model.Role,
	bindings []model.RoleBinding,
	removeBindingIDs []string,
) (bool, error) {
	if actor == "" || idempotencyKey == "" || requestDigest == "" {
		return false, errors.New("访问策略幂等信息不完整")
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var existingActor, existingDigest string
	err = tx.QueryRowContext(ctx, `SELECT actor_hash,request_digest FROM access_mutation_receipts WHERE idempotency_key=?`, idempotencyKey).
		Scan(&existingActor, &existingDigest)
	if err == nil {
		if existingActor != actor {
			return false, ErrActorMismatch
		}
		if existingDigest != requestDigest {
			return false, ErrIdempotency
		}
		if err := tx.Commit(); err != nil {
			return false, err
		}
		return false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	for _, tenant := range tenants {
		if err := store.upsertTenant(ctx, tx, tenant); err != nil {
			return false, err
		}
	}
	for _, role := range roles {
		if role.BuiltIn {
			return false, errors.New("内置角色不可通过网页修改")
		}
		if err := store.upsertRole(ctx, tx, role); err != nil {
			return false, err
		}
	}
	for _, binding := range bindings {
		if binding.CreatedBy == "bootstrap" {
			return false, errors.New("bootstrap 绑定不可通过网页修改")
		}
		binding.CreatedBy = actor
		if err := store.upsertRoleBinding(ctx, tx, binding); err != nil {
			return false, err
		}
	}
	for _, id := range removeBindingIDs {
		var createdBy string
		if err := tx.QueryRowContext(ctx, `SELECT created_by FROM role_bindings WHERE id=?`, id).Scan(&createdBy); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			return false, err
		}
		if createdBy == "bootstrap" {
			return false, errors.New("bootstrap 绑定不可撤销")
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM role_bindings WHERE id=?`, id); err != nil {
			return false, err
		}
	}
	// Never leave the dynamic policy without an effective platform-admin
	// binding. Bootstrap principals are checked by the Engine before this call;
	// this database-side check protects direct Store callers as well.
	var admins int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM role_bindings rb JOIN roles r ON r.id=rb.role_id
		WHERE (rb.expires_at IS NULL OR rb.expires_at > ?) AND r.permissions_json LIKE '%"*"%'`, timeText(store.now())).Scan(&admins); err != nil {
		return false, err
	}
	if admins == 0 {
		// A catalog-owned bootstrap binding may be the only administrator and is
		// intentionally not counted in dynamic rows; callers can explicitly
		// preserve it by making no removal that would erase the last admin.
		var bootstrapAdmins int
		if err := tx.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM role_bindings rb JOIN roles r ON r.id=rb.role_id
			WHERE rb.created_by='bootstrap' AND r.permissions_json LIKE '%"*"%'`).Scan(&bootstrapAdmins); err != nil {
			return false, err
		}
		if bootstrapAdmins == 0 {
			return false, errors.New("不能撤销最后一个平台管理员")
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO access_mutation_receipts(idempotency_key,actor_hash,request_digest,created_at) VALUES(?,?,?,?)`, idempotencyKey, actor, requestDigest, timeText(store.now())); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (store *Store) upsertRoleBinding(ctx context.Context, db accessExecer, binding model.RoleBinding) error {
	if binding.ID == "" || binding.Subject == "" || binding.TenantID == "" || binding.RoleID == "" {
		return errors.New("角色绑定不完整")
	}
	objects, err := encodeJSON(binding.ObjectIDs)
	if err != nil {
		return err
	}
	if binding.CreatedAt.IsZero() {
		binding.CreatedAt = store.now()
	}
	var expires any
	if binding.ExpiresAt != nil {
		expires = timeText(*binding.ExpiresAt)
	}
	_, err = db.ExecContext(ctx, `
		INSERT INTO role_bindings(id,subject,tenant_id,role_id,object_ids_json,expires_at,created_at,created_by,
			jit,requires_dual_approval,approval_state,approved_by_hash,second_approved_by_hash,approved_at,second_approved_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET subject=excluded.subject,tenant_id=excluded.tenant_id,role_id=excluded.role_id,
		 object_ids_json=excluded.object_ids_json,expires_at=excluded.expires_at,
		 jit=excluded.jit,requires_dual_approval=excluded.requires_dual_approval,
		 approval_state=excluded.approval_state,approved_by_hash=excluded.approved_by_hash,
		 second_approved_by_hash=excluded.second_approved_by_hash,approved_at=excluded.approved_at,
		 second_approved_at=excluded.second_approved_at`,
		binding.ID, model.NormalizeSubject(binding.Subject), binding.TenantID, binding.RoleID, objects, expires,
		timeText(binding.CreatedAt), binding.CreatedBy, binding.JIT, binding.RequiresDualApproval,
		binding.ApprovalState, binding.ApprovedByHash, binding.SecondApprovedByHash,
		nullableTimeText(binding.ApprovedAt), nullableTimeText(binding.SecondApprovedAt))
	return err
}

// ReconcileBootstrapAccess atomically refreshes catalog-owned authorization
// records and revokes bootstrap bindings removed from the catalog. Bindings
// created through the control plane use an actor hash as created_by and are
// intentionally left untouched.
func (store *Store) ReconcileBootstrapAccess(
	ctx context.Context,
	tenants []model.Tenant,
	roles []model.Role,
	bindings []model.RoleBinding,
) error {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	desiredBindings := make(map[string]struct{}, len(bindings))
	for _, tenant := range tenants {
		if createdBy, found, err := rowCreatedBy(ctx, tx, "tenants", tenant.ID); err != nil {
			return err
		} else if found && createdBy != "bootstrap" {
			return errors.New("动态租户不可被 bootstrap 配置覆盖")
		}
		if err := store.upsertTenant(ctx, tx, tenant); err != nil {
			return err
		}
	}
	for _, role := range roles {
		if createdBy, found, err := rowCreatedBy(ctx, tx, "roles", role.ID); err != nil {
			return err
		} else if found && createdBy != "bootstrap" {
			return errors.New("动态角色不可被 bootstrap 配置覆盖")
		}
		if err := store.upsertRole(ctx, tx, role); err != nil {
			return err
		}
	}
	for _, binding := range bindings {
		if createdBy, found, err := rowCreatedBy(ctx, tx, "role_bindings", binding.ID); err != nil {
			return err
		} else if found && createdBy != "bootstrap" {
			return errors.New("动态绑定不可被 bootstrap 配置覆盖")
		}
		binding.CreatedBy = "bootstrap"
		if err := store.upsertRoleBinding(ctx, tx, binding); err != nil {
			return err
		}
		desiredBindings[binding.ID] = struct{}{}
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM role_bindings WHERE created_by='bootstrap'`)
	if err != nil {
		return err
	}
	stale := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		if _, exists := desiredBindings[id]; !exists {
			stale = append(stale, id)
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range stale {
		if _, err := tx.ExecContext(ctx, `DELETE FROM role_bindings WHERE id=? AND created_by='bootstrap'`, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (store *Store) ListRoleBindings(ctx context.Context) ([]model.RoleBinding, error) {
	rows, err := store.db.QueryContext(ctx, `SELECT id,subject,tenant_id,role_id,object_ids_json,expires_at,created_at,created_by,
		jit,requires_dual_approval,approval_state,approved_by_hash,second_approved_by_hash,approved_at,second_approved_at
		FROM role_bindings ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]model.RoleBinding, 0)
	for rows.Next() {
		var binding model.RoleBinding
		var objects, created, createdBy string
		var expires, approvedAt, secondApprovedAt sql.NullString
		if err := rows.Scan(&binding.ID, &binding.Subject, &binding.TenantID, &binding.RoleID, &objects, &expires, &created, &createdBy,
			&binding.JIT, &binding.RequiresDualApproval, &binding.ApprovalState, &binding.ApprovedByHash,
			&binding.SecondApprovedByHash, &approvedAt, &secondApprovedAt); err != nil {
			return nil, err
		}
		if err := decodeJSON(objects, &binding.ObjectIDs); err != nil {
			return nil, err
		}
		binding.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return nil, err
		}
		binding.CreatedBy = createdBy
		if approvedAt.Valid {
			value, parseErr := time.Parse(time.RFC3339Nano, approvedAt.String)
			if parseErr != nil {
				return nil, parseErr
			}
			binding.ApprovedAt = &value
		}
		if secondApprovedAt.Valid {
			value, parseErr := time.Parse(time.RFC3339Nano, secondApprovedAt.String)
			if parseErr != nil {
				return nil, parseErr
			}
			binding.SecondApprovedAt = &value
		}
		if expires.Valid {
			value, parseErr := time.Parse(time.RFC3339Nano, expires.String)
			if parseErr != nil {
				return nil, parseErr
			}
			binding.ExpiresAt = &value
		}
		result = append(result, binding)
	}
	return result, rows.Err()
}

type authorizationBinding struct {
	roleID, tenantID, objectsJSON, permissionsJSON string
	expiresAt                                      sql.NullString
}

func (store *Store) Authorize(ctx context.Context, subject, tenantID, objectID string, permission model.Permission) (model.AuthorizationDecision, error) {
	decision := model.AuthorizationDecision{Permission: permission, TenantID: tenantID, ObjectID: objectID}
	if tenantID == "" {
		tenantID = "default"
		decision.TenantID = tenantID
	}
	rows, err := store.db.QueryContext(ctx, `
		SELECT rb.role_id,rb.tenant_id,rb.object_ids_json,rb.expires_at,
		       COALESCE(r.permissions_json, '[]')
		FROM role_bindings rb
		LEFT JOIN roles r ON r.id = rb.role_id
		WHERE rb.subject = ? AND (rb.tenant_id = ? OR rb.tenant_id = '*')`, model.NormalizeSubject(subject), tenantID)
	if err != nil {
		return decision, err
	}
	defer rows.Close()
	now := store.now()
	for rows.Next() {
		var binding authorizationBinding
		if err := rows.Scan(&binding.roleID, &binding.tenantID, &binding.objectsJSON, &binding.expiresAt, &binding.permissionsJSON); err != nil {
			return decision, err
		}
		if binding.expiresAt.Valid {
			expires, parseErr := time.Parse(time.RFC3339Nano, binding.expiresAt.String)
			if parseErr != nil || !now.Before(expires) {
				continue
			}
		}
		var objects []string
		if err := decodeJSON(binding.objectsJSON, &objects); err != nil {
			return decision, err
		}
		if len(objects) > 0 && !containsString(objects, objectID) && !containsString(objects, "*") {
			continue
		}
		var permissions []model.Permission
		if err := decodeJSON(binding.permissionsJSON, &permissions); err != nil {
			return decision, err
		}
		for _, candidate := range permissions {
			if candidate == permission || candidate == model.Permission("*") {
				decision.Allowed = true
				decision.Reason = "角色授权"
				return decision, nil
			}
		}
	}
	if err := rows.Err(); err != nil {
		return decision, err
	}
	decision.Reason = "没有匹配的租户角色或对象范围"
	return decision, nil
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
