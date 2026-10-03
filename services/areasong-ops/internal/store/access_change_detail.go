package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

// GetAccessPolicySnapshotVersion 只读已有不可变快照；绝不补建缺失基准。
func (store *Store) GetAccessPolicySnapshotVersion(ctx context.Context, version int64) (model.AccessPolicySnapshot, bool, error) {
	var snapshot model.AccessPolicySnapshot
	var created string
	err := store.db.QueryRowContext(ctx, `SELECT version,digest,policy_json,actor_hash,created_at FROM access_policy_snapshots WHERE version=?`, version).
		Scan(&snapshot.Version, &snapshot.Digest, &snapshot.PolicyJSON, &snapshot.ActorHash, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return snapshot, false, nil
	}
	if err != nil {
		return snapshot, false, err
	}
	snapshot.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	return snapshot, err == nil, err
}

// GetBindingReviewTarget 只读目标及其大小写/空白别名；不把当前表行当历史 before。
func (store *Store) GetBindingReviewTarget(ctx context.Context, id string) (model.RoleBinding, bool, error) {
	var b model.RoleBinding
	rows, err := store.db.QueryContext(ctx, `SELECT id,subject,tenant_id,role_id,object_ids_json,expires_at,created_by,
 jit,requires_dual_approval,approval_state,approved_by_hash,second_approved_by_hash,approved_at,second_approved_at
 FROM role_bindings WHERE lower(trim(id))=? LIMIT 2`, id)
	if err != nil {
		return b, false, err
	}
	defer rows.Close()
	if !rows.Next() {
		return b, false, rows.Err()
	}
	var objects string
	var expires, approved, second sql.NullString
	if err = rows.Scan(&b.ID, &b.Subject, &b.TenantID, &b.RoleID, &objects, &expires, &b.CreatedBy, &b.JIT, &b.RequiresDualApproval, &b.ApprovalState, &b.ApprovedByHash, &b.SecondApprovedByHash, &approved, &second); err != nil {
		return b, false, err
	}
	if b.ID != id || rows.Next() {
		return b, false, errors.New("ambiguous binding target")
	}
	if err = rows.Err(); err != nil {
		return b, false, err
	}
	if err = decodeJSON(objects, &b.ObjectIDs); err != nil {
		return b, false, err
	}
	for _, item := range []struct {
		raw    sql.NullString
		target **time.Time
	}{{expires, &b.ExpiresAt}, {approved, &b.ApprovedAt}, {second, &b.SecondApprovedAt}} {
		if item.raw.Valid {
			value, parseErr := time.Parse(time.RFC3339Nano, item.raw.String)
			if parseErr != nil {
				return b, false, parseErr
			}
			*item.target = &value
		}
	}
	return b, true, nil
}
