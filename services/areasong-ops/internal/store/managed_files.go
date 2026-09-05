package store

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

type managedFileRow struct {
	proposal         model.ManagedFileProposal
	requestDigest    string
	confirmationHash string
}

func (store *Store) GetManagedFileProposal(ctx context.Context, id string) (model.ManagedFileProposal, error) {
	row, found, err := queryManagedFileProposal(ctx, store.db, "id", id)
	if err != nil {
		return model.ManagedFileProposal{}, err
	}
	if !found {
		return model.ManagedFileProposal{}, ErrNotFound
	}
	return row.proposal, nil
}

func (store *Store) ListManagedFileProposals(ctx context.Context, limit int) ([]model.ManagedFileProposal, error) {
	rows, err := store.db.QueryContext(ctx, managedFileProposalSelect+` ORDER BY created_at DESC LIMIT ?`, clampLimit(limit, 200))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]model.ManagedFileProposal, 0)
	for rows.Next() {
		item, err := scanManagedFileProposal(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, item.proposal)
	}
	return result, rows.Err()
}

func (store *Store) ApproveManagedFileProposal(
	ctx context.Context, id, actor, digest, confirmation string,
) (model.ManagedFileProposal, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return model.ManagedFileProposal{}, err
	}
	defer tx.Rollback()
	row, found, err := queryManagedFileProposal(ctx, tx, "id", id)
	if err != nil || !found {
		if err == nil {
			err = ErrNotFound
		}
		return model.ManagedFileProposal{}, err
	}
	proposal := row.proposal
	if proposal.ProposedDigest != digest {
		return model.ManagedFileProposal{}, errors.New("文件提案摘要不匹配")
	}
	if proposal.ActorHash == actor {
		return model.ManagedFileProposal{}, errors.New("文件提案创建人不能批准自己的提案")
	}
	if subtle.ConstantTimeCompare([]byte(HashConfirmation(confirmation)), []byte(row.confirmationHash)) != 1 {
		return model.ManagedFileProposal{}, ErrConfirmation
	}
	now := store.now()
	switch proposal.State {
	case "proposed":
		result, updateErr := tx.ExecContext(ctx, `UPDATE managed_file_proposals SET state='pending_second_approval',approved_by_hash=?,approved_at=? WHERE id=? AND state='proposed'`, actor, timeText(now), id)
		err = requireOne(result, updateErr, "文件第一批准无法写入")
		proposal.State, proposal.ApprovedByHash, proposal.ApprovedAt = "pending_second_approval", actor, &now
	case "pending_second_approval":
		if proposal.ApprovedByHash == actor {
			return proposal, nil
		}
		result, updateErr := tx.ExecContext(ctx, `UPDATE managed_file_proposals SET state='approved',second_approved_by_hash=?,second_approved_at=? WHERE id=? AND state='pending_second_approval' AND approved_by_hash<>?`, actor, timeText(now), id, actor)
		err = requireOne(result, updateErr, "文件第二批准无法写入")
		proposal.State, proposal.SecondApprovedByHash, proposal.SecondApprovedAt = "approved", actor, &now
	case "approved", "applying", "applied", "rolling_back", "rolled_back", "needs_attention":
		if proposal.ApprovedByHash == actor || proposal.SecondApprovedByHash == actor {
			return proposal, nil
		}
		return model.ManagedFileProposal{}, errors.New("文件提案已完成批准或执行")
	default:
		return model.ManagedFileProposal{}, errors.New("文件提案状态无效")
	}
	if err != nil {
		return model.ManagedFileProposal{}, err
	}
	if err := appendManagedFileAudit(ctx, tx, actor, "file.proposal.approved", proposal.State, proposal, now); err != nil {
		return model.ManagedFileProposal{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.ManagedFileProposal{}, err
	}
	return proposal, nil
}

func (store *Store) StartManagedFileApply(
	ctx context.Context, id, actor, idempotencyKey string,
) (model.ManagedFileProposal, bool, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return model.ManagedFileProposal{}, false, err
	}
	defer tx.Rollback()
	row, found, err := queryManagedFileProposal(ctx, tx, "id", id)
	if err != nil || !found {
		if err == nil {
			err = ErrNotFound
		}
		return model.ManagedFileProposal{}, false, err
	}
	proposal := row.proposal
	if proposal.ApplyIdempotencyKey != "" {
		if proposal.ApplyIdempotencyKey != idempotencyKey {
			return model.ManagedFileProposal{}, false, ErrIdempotency
		}
		if proposal.AppliedByHash != actor {
			return model.ManagedFileProposal{}, false, ErrActorMismatch
		}
		if proposal.State == "applying" {
			now := store.now()
			result, updateErr := tx.ExecContext(ctx, `UPDATE managed_file_proposals SET state='needs_attention',error='文件应用中断，结果未知',finished_at=? WHERE id=? AND state='applying'`, timeText(now), id)
			if err := requireOne(result, updateErr, "文件应用中断状态无法写入"); err != nil {
				return model.ManagedFileProposal{}, false, err
			}
			proposal.State, proposal.Error, proposal.FinishedAt = "needs_attention", "文件应用中断，结果未知", &now
			if err := appendManagedFileAudit(ctx, tx, actor, "file.proposal.recovered", proposal.State, proposal, now); err != nil {
				return model.ManagedFileProposal{}, false, err
			}
			if err := tx.Commit(); err != nil {
				return model.ManagedFileProposal{}, false, err
			}
			return proposal, false, errors.New(proposal.Error)
		}
		return proposal, false, nil
	}
	if proposal.State != "approved" || proposal.ApprovedByHash == "" || proposal.SecondApprovedByHash == "" {
		return model.ManagedFileProposal{}, false, errors.New("文件提案尚未完成双人批准")
	}
	if actor == proposal.ActorHash || actor == proposal.ApprovedByHash || actor == proposal.SecondApprovedByHash {
		return model.ManagedFileProposal{}, false, errors.New("文件应用执行人必须独立于创建人与批准人")
	}
	now := store.now()
	result, err := tx.ExecContext(ctx, `UPDATE managed_file_proposals SET state='applying',applied_by_hash=?,apply_idempotency_key=?,applied_at=? WHERE id=? AND state='approved'`, actor, idempotencyKey, timeText(now), id)
	if err != nil {
		return model.ManagedFileProposal{}, false, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return model.ManagedFileProposal{}, false, errors.New("文件提案状态已变化")
	}
	proposal.State, proposal.AppliedByHash, proposal.ApplyIdempotencyKey, proposal.AppliedAt = "applying", actor, idempotencyKey, &now
	if err := appendManagedFileAudit(ctx, tx, actor, "file.proposal.apply_started", proposal.State, proposal, now); err != nil {
		return model.ManagedFileProposal{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return model.ManagedFileProposal{}, false, err
	}
	return proposal, true, nil
}

func (store *Store) FinishManagedFileApply(
	ctx context.Context, id, state, backupPath, errorText string,
) error {
	if state != "applied" && state != "failed" && state != "needs_attention" {
		return errors.New("文件应用终态无效")
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	row, found, err := queryManagedFileProposal(ctx, tx, "id", id)
	if err != nil || !found {
		if err == nil {
			err = ErrNotFound
		}
		return err
	}
	now := store.now()
	result, updateErr := tx.ExecContext(ctx, `UPDATE managed_file_proposals SET state=?,backup_path=?,error=?,finished_at=? WHERE id=? AND state='applying'`, state, backupPath, errorText, timeText(now), id)
	if err := requireOne(result, updateErr, "文件应用状态无法收口"); err != nil {
		return err
	}
	proposal := row.proposal
	proposal.State, proposal.BackupPath, proposal.Error, proposal.FinishedAt = state, backupPath, errorText, &now
	if err := appendManagedFileAudit(ctx, tx, proposal.AppliedByHash, "file.proposal.applied", state, proposal, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (store *Store) StartManagedFileRollback(
	ctx context.Context, id, actor, idempotencyKey string,
) (model.ManagedFileProposal, bool, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return model.ManagedFileProposal{}, false, err
	}
	defer tx.Rollback()
	row, found, err := queryManagedFileProposal(ctx, tx, "id", id)
	if err != nil || !found {
		if err == nil {
			err = ErrNotFound
		}
		return model.ManagedFileProposal{}, false, err
	}
	proposal := row.proposal
	if proposal.RollbackIdempotencyKey != "" {
		if proposal.RollbackIdempotencyKey != idempotencyKey {
			return model.ManagedFileProposal{}, false, ErrIdempotency
		}
		if proposal.RolledBackByHash != actor {
			return model.ManagedFileProposal{}, false, ErrActorMismatch
		}
		return proposal, false, nil
	}
	if proposal.State != "applied" || proposal.BackupPath == "" {
		return model.ManagedFileProposal{}, false, errors.New("文件提案没有可用回滚副本")
	}
	result, err := tx.ExecContext(ctx, `UPDATE managed_file_proposals SET state='rolling_back',rolled_back_by_hash=?,rollback_idempotency_key=? WHERE id=? AND state='applied'`, actor, idempotencyKey, id)
	if err != nil {
		return model.ManagedFileProposal{}, false, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return model.ManagedFileProposal{}, false, errors.New("文件提案状态已变化")
	}
	proposal.State, proposal.RolledBackByHash, proposal.RollbackIdempotencyKey = "rolling_back", actor, idempotencyKey
	if err := appendManagedFileAudit(ctx, tx, actor, "file.proposal.rollback_started", proposal.State, proposal, store.now()); err != nil {
		return model.ManagedFileProposal{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return model.ManagedFileProposal{}, false, err
	}
	return proposal, true, nil
}

func (store *Store) FinishManagedFileRollback(ctx context.Context, id, state, errorText string) error {
	if state != "rolled_back" && state != "needs_attention" {
		return errors.New("文件回滚终态无效")
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	row, found, err := queryManagedFileProposal(ctx, tx, "id", id)
	if err != nil || !found {
		if err == nil {
			err = ErrNotFound
		}
		return err
	}
	now := store.now()
	result, updateErr := tx.ExecContext(ctx, `UPDATE managed_file_proposals SET state=?,error=?,rolled_back_at=?,finished_at=? WHERE id=? AND state='rolling_back'`, state, errorText, timeText(now), timeText(now), id)
	if err := requireOne(result, updateErr, "文件回滚状态无法收口"); err != nil {
		return err
	}
	proposal := row.proposal
	proposal.State, proposal.Error, proposal.RolledBackAt, proposal.FinishedAt = state, errorText, &now, &now
	if err := appendManagedFileAudit(ctx, tx, proposal.RolledBackByHash, "file.proposal.rolled_back", state, proposal, now); err != nil {
		return err
	}
	return tx.Commit()
}

func appendManagedFileAudit(
	ctx context.Context,
	tx *sql.Tx,
	actor, event, outcome string,
	proposal model.ManagedFileProposal,
	now time.Time,
) error {
	detail := map[string]any{
		"rootId": proposal.RootID, "path": proposal.Path,
		"expectedDigest": proposal.ExpectedDigest, "proposedDigest": proposal.ProposedDigest,
	}
	if proposal.BackupPath != "" {
		detail["backupPath"] = proposal.BackupPath
	}
	if proposal.Error != "" {
		detail["error"] = proposal.Error
	}
	return appendPlanAudit(ctx, tx, model.AuditEntry{
		ActorHash: actor, Event: event, Resource: proposal.ID, Outcome: outcome, Detail: detail,
	}, now)
}

const managedFileProposalSelect = `SELECT id,idempotency_key,request_digest,actor_hash,root_id,relative_path,
	expected_digest,proposed_digest,content,state,confirmation_hash,confirmation_phrase,
	approved_by_hash,second_approved_by_hash,applied_by_hash,apply_idempotency_key,
	backup_path,rolled_back_by_hash,rollback_idempotency_key,error,created_at,
	approved_at,second_approved_at,applied_at,finished_at,rolled_back_at
	FROM managed_file_proposals`

func queryManagedFileProposal(ctx context.Context, db queryer, field, value string) (managedFileRow, bool, error) {
	query := managedFileProposalSelect
	switch field {
	case "id":
		query += " WHERE id=?"
	case "idempotency_key":
		query += " WHERE idempotency_key=?"
	default:
		return managedFileRow{}, false, errors.New("文件提案查询字段无效")
	}
	row, err := scanManagedFileProposal(db.QueryRowContext(ctx, query, value))
	if errors.Is(err, sql.ErrNoRows) {
		return managedFileRow{}, false, nil
	}
	return row, err == nil, err
}

type managedFileScanner interface{ Scan(...any) error }

func scanManagedFileProposal(scanner managedFileScanner) (managedFileRow, error) {
	var row managedFileRow
	var created string
	var approved, secondApproved, applied, finished, rolledBack sql.NullString
	err := scanner.Scan(&row.proposal.ID, &row.proposal.IdempotencyKey, &row.requestDigest,
		&row.proposal.ActorHash, &row.proposal.RootID, &row.proposal.Path,
		&row.proposal.ExpectedDigest, &row.proposal.ProposedDigest, &row.proposal.Content,
		&row.proposal.State, &row.confirmationHash, &row.proposal.ConfirmationPhrase,
		&row.proposal.ApprovedByHash, &row.proposal.SecondApprovedByHash,
		&row.proposal.AppliedByHash, &row.proposal.ApplyIdempotencyKey,
		&row.proposal.BackupPath, &row.proposal.RolledBackByHash,
		&row.proposal.RollbackIdempotencyKey, &row.proposal.Error, &created,
		&approved, &secondApproved, &applied, &finished, &rolledBack)
	if err != nil {
		return managedFileRow{}, err
	}
	row.proposal.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err == nil {
		row.proposal.ApprovedAt, err = nullableTime(approved)
	}
	if err == nil {
		row.proposal.SecondApprovedAt, err = nullableTime(secondApproved)
	}
	if err == nil {
		row.proposal.AppliedAt, err = nullableTime(applied)
	}
	if err == nil {
		row.proposal.FinishedAt, err = nullableTime(finished)
	}
	if err == nil {
		row.proposal.RolledBackAt, err = nullableTime(rolledBack)
	}
	return row, err
}
