package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/AreaSong/ops/services/areasong-ops/internal/config"
	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

var (
	ErrTenantLifecycleUnknown     = errors.New("租户生命周期来源或镜像无法核验")
	ErrTenantLifecycleProtected   = errors.New("默认或 bootstrap 租户不支持生命周期转换")
	ErrTenantLifecycleConflict    = errors.New("租户生命周期状态或代次冲突")
	ErrTenantLifecycleUnsupported = errors.New("租户生命周期状态不受支持")
)

type tenantLifecycleRow struct {
	ID, DisplayName, Status, CreatedBy string
	Generation                         int64
}

func lifecyclePolicyTx(ctx context.Context, tx *sql.Tx) (config.AccessPolicy, model.AccessPolicySnapshot, error) {
	var policy config.AccessPolicy
	var snapshot model.AccessPolicySnapshot
	err := tx.QueryRowContext(ctx, `SELECT version,digest,policy_json FROM access_policy_snapshots ORDER BY version DESC LIMIT 1`).
		Scan(&snapshot.Version, &snapshot.Digest, &snapshot.PolicyJSON)
	if err != nil {
		return policy, snapshot, err
	}
	if digestPolicyJSON(snapshot.PolicyJSON) != snapshot.Digest || json.Unmarshal([]byte(snapshot.PolicyJSON), &policy) != nil {
		return policy, snapshot, ErrTenantLifecycleUnknown
	}
	encoded, err := json.Marshal(policy)
	if err != nil || string(encoded) != snapshot.PolicyJSON {
		return policy, snapshot, ErrTenantLifecycleUnknown
	}
	return policy, snapshot, nil
}

func tenantRowsTx(ctx context.Context, tx *sql.Tx) (map[string]tenantLifecycleRow, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,display_name,status,created_by,lifecycle_generation FROM tenants`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]tenantLifecycleRow)
	for rows.Next() {
		var row tenantLifecycleRow
		if err := rows.Scan(&row.ID, &row.DisplayName, &row.Status, &row.CreatedBy, &row.Generation); err != nil {
			return nil, err
		}
		result[row.ID] = row
	}
	return result, rows.Err()
}

func lifecycleStatus(status string) string {
	if status == "" {
		return "active"
	}
	return status
}

func canonicalLifecycleID(id string) bool {
	return id != "" && id == strings.ToLower(strings.TrimSpace(id))
}

func lifecycleSourceOrdinary(source string) bool {
	return len(source) == 64 && source == strings.ToLower(source) && config.IsAccessHash(source)
}

func lifecycleMirror(policy config.AccessPolicy, rows map[string]tenantLifecycleRow, id string) (tenantLifecycleRow, error) {
	row, rowExists := rows[id]
	tenant, exists := policy.Tenants[id]
	if !rowExists && !exists {
		return row, ErrNotFound
	}
	if !rowExists || !exists {
		return row, ErrTenantLifecycleUnknown
	}
	if !canonicalLifecycleID(id) || tenant.ID != id || row.DisplayName != tenant.DisplayName ||
		lifecycleStatus(row.Status) != lifecycleStatus(tenant.Status) {
		return row, ErrTenantLifecycleUnknown
	}
	for key := range rows {
		if key != id && strings.ToLower(strings.TrimSpace(key)) == id {
			return row, ErrTenantLifecycleUnknown
		}
	}
	for key, value := range policy.Tenants {
		if key != id && (strings.ToLower(strings.TrimSpace(key)) == id || strings.ToLower(strings.TrimSpace(value.ID)) == id) {
			return row, ErrTenantLifecycleUnknown
		}
	}
	if row.CreatedBy == "bootstrap" && (tenant.CreatedBy == "" || tenant.CreatedBy == "bootstrap") {
		return row, nil
	}
	if !lifecycleSourceOrdinary(row.CreatedBy) || !lifecycleSourceOrdinary(tenant.CreatedBy) {
		return row, ErrTenantLifecycleUnknown
	}
	return row, nil
}

// 仅建立可证明的当前基准，不推断停用历史，不修复旧数据。
func initializeTenantLifecycleTx(ctx context.Context, tx *sql.Tx) error {
	policy, _, err := lifecyclePolicyTx(ctx, tx)
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, ErrTenantLifecycleUnknown) {
		return nil
	}
	if err != nil {
		return err
	}
	rows, err := tenantRowsTx(ctx, tx)
	if err != nil {
		return err
	}
	for id, row := range rows {
		if row.Generation != 0 || lifecycleStatus(row.Status) != "active" {
			continue
		}
		if _, err := lifecycleMirror(policy, rows, id); err != nil {
			continue
		}
		result, err := tx.ExecContext(ctx, `UPDATE tenants SET lifecycle_generation=1
			WHERE id=? AND lifecycle_generation=0 AND status=? AND created_by=?`, id, row.Status, row.CreatedBy)
		if err := requireOne(result, err, "租户生命周期初始化冲突"); err != nil {
			return err
		}
	}
	return nil
}

// 同事务读取当前权威状态与代次。此读方法不提供工作准入或执行授权。
func (store *Store) GetTenantLifecycle(ctx context.Context, id string) (model.TenantLifecycle, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return model.TenantLifecycle{}, err
	}
	defer tx.Rollback()
	policy, snapshot, err := lifecyclePolicyTx(ctx, tx)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrTenantLifecycleUnknown
	}
	if err != nil {
		return model.TenantLifecycle{}, err
	}
	rows, err := tenantRowsTx(ctx, tx)
	if err != nil {
		return model.TenantLifecycle{}, err
	}
	row, err := lifecycleMirror(policy, rows, id)
	if err != nil {
		return model.TenantLifecycle{}, err
	}
	value := model.TenantLifecycle{TenantID: id, Status: lifecycleStatus(policy.Tenants[id].Status), Generation: row.Generation, PolicyVersion: snapshot.Version}
	return value, tx.Commit()
}

// 配置来源可保留非 active 旧声明，但只建立未知代次；冲突时不能改变有效状态。
func (store *Store) seedTenant(ctx context.Context, tx *sql.Tx, tenant model.Tenant) error {
	if tenant.ID == "" || tenant.DisplayName == "" {
		return errors.New("租户标识或名称不能为空")
	}
	if tenant.Status == "" {
		tenant.Status = "active"
	}
	if tenant.CreatedBy == "" {
		tenant.CreatedBy = "bootstrap"
	}
	if tenant.CreatedAt.IsZero() {
		tenant.CreatedAt = store.now()
	}
	generation := initialTenantGeneration(tenant)
	result, err := tx.ExecContext(ctx, `INSERT INTO tenants(id,display_name,status,created_at,updated_at,created_by,lifecycle_generation)
		VALUES(?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET display_name=excluded.display_name,
		status=excluded.status,updated_at=excluded.updated_at
		WHERE tenants.status=excluded.status OR (tenants.status='' AND excluded.status='active')`,
		tenant.ID, tenant.DisplayName, tenant.Status, timeText(tenant.CreatedAt), timeText(store.now()), tenant.CreatedBy, generation)
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

func initialTenantGeneration(tenant model.Tenant) int64 {
	if tenant.Status == "active" && canonicalLifecycleID(tenant.ID) && (tenant.CreatedBy == "bootstrap" || lifecycleSourceOrdinary(tenant.CreatedBy)) {
		return 1
	}
	return 0
}
