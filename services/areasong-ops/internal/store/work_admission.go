package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

var (
	ErrWorkAdmissionConflict    = errors.New("工作准入状态、所有权或请求冲突")
	ErrWorkAdmissionCorrupt     = errors.New("工作准入持久化合同无法核验")
	ErrUnfinishedRegisteredWork = errors.New("租户存在未结束的已登记工作")
)

type WorkAdmissionInput struct {
	AdmissionID string
	OwnerToken  string
	Request     model.WorkAdmissionRequest
}

// 首条零行DML取得SQLite写串行化边界；不是依赖单Store连接池互斥。
type registeredWorkTx struct {
	*sql.Tx
	conn *sql.Conn
}

// 当前驱动Commit失败不自动回滚。固定连接上显式回滚，清理失败则丢弃，避免污染连接池。
func (tx *registeredWorkTx) Commit() error {
	err := tx.Tx.Commit()
	if err != nil {
		if _, rollbackErr := tx.conn.ExecContext(context.Background(), "ROLLBACK"); rollbackErr != nil {
			_ = tx.conn.Raw(func(any) error { return driver.ErrBadConn })
		}
	}
	_ = tx.conn.Close()
	return err
}

func (tx *registeredWorkTx) Rollback() error {
	err := tx.Tx.Rollback()
	_ = tx.conn.Close()
	return err
}

func (store *Store) beginRegisteredWorkTx(ctx context.Context) (*registeredWorkTx, error) {
	conn, err := store.db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		conn.Close()
		return nil, err
	}
	registered := &registeredWorkTx{Tx: tx, conn: conn}
	if _, err = tx.ExecContext(ctx, `UPDATE tenants SET lifecycle_generation=lifecycle_generation WHERE 0`); err != nil {
		registered.Rollback()
		return nil, err
	}
	return registered, nil
}

func (store *Store) AdmitWork(ctx context.Context, input WorkAdmissionInput) (model.WorkAdmission, bool, error) {
	tx, err := store.beginRegisteredWorkTx(ctx)
	if err != nil {
		return model.WorkAdmission{}, false, err
	}
	defer tx.Rollback()
	record, created, err := store.admitWorkTx(ctx, tx.Tx, input)
	if err != nil {
		return model.WorkAdmission{}, false, err
	}
	if err = tx.Commit(); err != nil {
		return model.WorkAdmission{}, false, err
	}
	return record, created, nil
}

func (store *Store) admitWorkTx(ctx context.Context, tx *sql.Tx, input WorkAdmissionInput) (model.WorkAdmission, bool, error) {
	request, raw, digest, err := model.CanonicalWorkRequest(input.Request)
	if err != nil {
		return model.WorkAdmission{}, false, err
	}
	if input.AdmissionID == "" || strings.TrimSpace(input.AdmissionID) != input.AdmissionID || strings.ContainsRune(input.AdmissionID, '\x00') || !model.ValidWorkDigest(input.OwnerToken) {
		return model.WorkAdmission{}, false, model.ErrWorkAdmissionContract
	}
	var existing string
	err = tx.QueryRowContext(ctx, `SELECT id FROM work_admissions WHERE idempotency_key=? OR (kind=? AND work_id=?) OR id=? LIMIT 1`, request.IdempotencyKey, request.Kind, request.WorkID, input.AdmissionID).Scan(&existing)
	if err == nil {
		record, _, storedRaw, readErr := readWorkAdmissionTx(ctx, tx, existing)
		if readErr != nil {
			return model.WorkAdmission{}, false, readErr
		}
		if storedRaw != raw {
			return model.WorkAdmission{}, false, ErrIdempotency
		}
		return record, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return model.WorkAdmission{}, false, err
	}
	if err = validateWorkTargetsTx(ctx, tx, request.Targets); err != nil {
		return model.WorkAdmission{}, false, err
	}
	now := store.now()
	_, err = tx.ExecContext(ctx, `INSERT INTO work_admissions(id,kind,work_id,idempotency_key,approval_digest,actor_hash,request_digest,request_json,owner_token_hash,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, input.AdmissionID, request.Kind, request.WorkID, request.IdempotencyKey, request.ApprovalDigest, request.ActorHash, digest, raw, model.WorkDigest(input.OwnerToken), timeText(now), timeText(now))
	if err != nil {
		return model.WorkAdmission{}, false, err
	}
	for _, target := range request.Targets {
		if _, err = tx.ExecContext(ctx, `INSERT INTO work_admission_targets(admission_id,tenant_id,expected_generation) VALUES(?,?,?)`, input.AdmissionID, target.TenantID, target.ExpectedGeneration); err != nil {
			return model.WorkAdmission{}, false, err
		}
	}
	record := model.WorkAdmission{ID: input.AdmissionID, Request: request, RequestDigest: digest, State: model.WorkAdmitted, Revision: 1, CreatedAt: now, UpdatedAt: now}
	return record, true, nil
}

func validateWorkTargetsTx(ctx context.Context, tx *sql.Tx, targets []model.WorkAdmissionTarget) error {
	policy, _, err := lifecyclePolicyTx(ctx, tx)
	if err != nil {
		return err
	}
	rows, err := tenantRowsTx(ctx, tx)
	if err != nil {
		return err
	}
	for _, target := range targets {
		row, err := lifecycleMirror(policy, rows, target.TenantID)
		if err != nil {
			return err
		}
		if lifecycleStatus(row.Status) != "active" || row.Generation <= 0 || row.Generation != target.ExpectedGeneration {
			return ErrTenantLifecycleConflict
		}
	}
	return nil
}

// 只检查已登记工作，空结果不能证明真实执行器均空闲。
func assertNoUnfinishedRegisteredWorkTx(ctx context.Context, tx *sql.Tx, tenantID string) error {
	var id string
	err := tx.QueryRowContext(ctx, `SELECT a.id FROM work_admission_targets t JOIN work_admissions a ON a.id=t.admission_id WHERE t.tenant_id=? AND a.state<>'closed' LIMIT 1`, tenantID).Scan(&id)
	if err == nil {
		return ErrUnfinishedRegisteredWork
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	return err
}

func (store *Store) GetWorkAdmission(ctx context.Context, id string) (model.WorkAdmission, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return model.WorkAdmission{}, err
	}
	defer tx.Rollback()
	record, _, _, err := readWorkAdmissionTx(ctx, tx, id)
	if err != nil {
		return model.WorkAdmission{}, err
	}
	return record, tx.Commit()
}

func readWorkAdmissionTx(ctx context.Context, tx *sql.Tx, id string) (model.WorkAdmission, string, string, error) {
	var record model.WorkAdmission
	var owner, raw, kind, workID, key, approval, actor, created, updated string
	var task, closed sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT id,kind,work_id,idempotency_key,approval_digest,actor_hash,request_digest,request_json,owner_token_hash,state,revision,task_id,created_at,updated_at,closed_at,close_kind,close_digest,close_evidence_json FROM work_admissions WHERE id=?`, id).Scan(&record.ID, &kind, &workID, &key, &approval, &actor, &record.RequestDigest, &raw, &owner, &record.State, &record.Revision, &task, &created, &updated, &closed, &record.CloseKind, &record.CloseDigest, &record.CloseEvidenceJSON)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	if err != nil {
		return record, "", "", err
	}
	if json.Unmarshal([]byte(raw), &record.Request) != nil {
		return record, "", "", ErrWorkAdmissionCorrupt
	}
	request, canonical, digest, err := model.CanonicalWorkRequest(record.Request)
	if err != nil || canonical != raw || digest != record.RequestDigest || request.Kind != kind || request.WorkID != workID || request.IdempotencyKey != key || request.ApprovalDigest != approval || request.ActorHash != actor || !model.ValidWorkDigest(owner) {
		return record, "", "", ErrWorkAdmissionCorrupt
	}
	record.TaskID = task.String
	if err = readWorkTimes(&record, created, updated, closed); err != nil {
		return record, "", "", err
	}
	targets, err := readWorkTargetsTx(ctx, tx, id)
	if err != nil {
		return record, "", "", err
	}
	if !reflect.DeepEqual(targets, request.Targets) {
		return record, "", "", ErrWorkAdmissionCorrupt
	}
	return record, owner, raw, nil
}

func readWorkTimes(record *model.WorkAdmission, created, updated string, closed sql.NullString) error {
	var err error
	record.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return ErrWorkAdmissionCorrupt
	}
	record.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
	if err != nil {
		return ErrWorkAdmissionCorrupt
	}
	if closed.Valid {
		value, err := time.Parse(time.RFC3339Nano, closed.String)
		if err != nil {
			return ErrWorkAdmissionCorrupt
		}
		record.ClosedAt = &value
	}
	return nil
}

func readWorkTargetsTx(ctx context.Context, tx *sql.Tx, id string) ([]model.WorkAdmissionTarget, error) {
	rows, err := tx.QueryContext(ctx, `SELECT tenant_id,expected_generation FROM work_admission_targets WHERE admission_id=? ORDER BY tenant_id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []model.WorkAdmissionTarget{}
	for rows.Next() {
		var target model.WorkAdmissionTarget
		if err = rows.Scan(&target.TenantID, &target.ExpectedGeneration); err != nil {
			return nil, err
		}
		result = append(result, target)
	}
	return result, rows.Err()
}
