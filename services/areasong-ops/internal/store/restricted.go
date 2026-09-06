package store

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

func (store *Store) ReserveTerminalSession(
	ctx context.Context,
	session model.TerminalSession,
) (model.TerminalSession, bool, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return model.TerminalSession{}, false, err
	}
	defer tx.Rollback()
	existing, found, err := terminalSessionByKey(ctx, tx, session.IdempotencyKey)
	if err != nil {
		return model.TerminalSession{}, false, err
	}
	if found {
		if existing.RequestDigest != session.RequestDigest || existing.ActorHash != session.ActorHash {
			return model.TerminalSession{}, false, ErrIdempotency
		}
		return existing, false, nil
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO terminal_sessions(
		id,idempotency_key,request_digest,object_id,command_name,state,actor_hash,
		exit_code,output,expires_at,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		session.ID, session.IdempotencyKey, session.RequestDigest, session.ObjectID,
		session.Command, session.State, session.ActorHash, session.ExitCode, session.Output,
		timeText(session.ExpiresAt), timeText(session.CreatedAt))
	if err != nil {
		return model.TerminalSession{}, false, err
	}
	if err := appendTerminalSessionAudit(ctx, tx, session, "terminal.command.started", "accepted", session.CreatedAt); err != nil {
		return model.TerminalSession{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return model.TerminalSession{}, false, err
	}
	return session, true, nil
}

func (store *Store) CompleteTerminalSession(
	ctx context.Context,
	id, state string,
	exitCode int,
	output string,
) error {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE terminal_sessions
		SET state=?,exit_code=?,output=? WHERE id=? AND state='running'`, state, exitCode, output, id)
	if err := requireOne(result, err, "终端会话无法写入终态"); err != nil {
		return err
	}
	session, found, err := terminalSessionByID(ctx, tx, id)
	if err != nil {
		return err
	}
	if !found {
		return ErrNotFound
	}
	if err := appendTerminalSessionAudit(ctx, tx, session, "terminal.command", state, store.now()); err != nil {
		return err
	}
	return tx.Commit()
}

func terminalSessionByKey(
	ctx context.Context,
	db queryer,
	key string,
) (model.TerminalSession, bool, error) {
	row := db.QueryRowContext(ctx, `SELECT id,idempotency_key,request_digest,object_id,
		command_name,state,actor_hash,exit_code,output,expires_at,created_at
		FROM terminal_sessions WHERE idempotency_key=?`, key)
	var session model.TerminalSession
	var expiresAt, createdAt string
	err := row.Scan(&session.ID, &session.IdempotencyKey, &session.RequestDigest,
		&session.ObjectID, &session.Command, &session.State, &session.ActorHash,
		&session.ExitCode, &session.Output, &expiresAt, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return model.TerminalSession{}, false, nil
	}
	if err != nil {
		return model.TerminalSession{}, false, err
	}
	session.ExpiresAt, err = time.Parse(time.RFC3339Nano, expiresAt)
	if err == nil {
		session.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	}
	return session, err == nil, err
}

func terminalSessionByID(
	ctx context.Context,
	db queryer,
	id string,
) (model.TerminalSession, bool, error) {
	row := db.QueryRowContext(ctx, `SELECT id,idempotency_key,request_digest,object_id,
		command_name,state,actor_hash,exit_code,output,expires_at,created_at
		FROM terminal_sessions WHERE id=?`, id)
	var session model.TerminalSession
	var expiresAt, createdAt string
	err := row.Scan(&session.ID, &session.IdempotencyKey, &session.RequestDigest,
		&session.ObjectID, &session.Command, &session.State, &session.ActorHash,
		&session.ExitCode, &session.Output, &expiresAt, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return model.TerminalSession{}, false, nil
	}
	if err != nil {
		return model.TerminalSession{}, false, err
	}
	session.ExpiresAt, err = time.Parse(time.RFC3339Nano, expiresAt)
	if err == nil {
		session.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	}
	return session, err == nil, err
}

func appendTerminalSessionAudit(
	ctx context.Context,
	tx *sql.Tx,
	session model.TerminalSession,
	event, outcome string,
	now time.Time,
) error {
	return appendPlanAudit(ctx, tx, model.AuditEntry{
		ActorHash: session.ActorHash, Event: event, Resource: session.ObjectID, Outcome: outcome,
		Detail: map[string]any{
			"command": session.Command, "sessionId": session.ID, "exitCode": session.ExitCode,
		},
	}, now)
}

func (store *Store) SaveManagedFileProposal(
	ctx context.Context,
	proposal model.ManagedFileProposal,
	requestDigest string,
) (model.ManagedFileProposal, bool, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return model.ManagedFileProposal{}, false, err
	}
	defer tx.Rollback()
	existing, found, err := managedFileProposalByKey(ctx, tx, proposal.IdempotencyKey)
	if err != nil {
		return model.ManagedFileProposal{}, false, err
	}
	if found {
		if existing.requestDigest != requestDigest || existing.proposal.ActorHash != proposal.ActorHash {
			return model.ManagedFileProposal{}, false, ErrIdempotency
		}
		return existing.proposal, false, nil
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO managed_file_proposals(
		id,idempotency_key,request_digest,actor_hash,root_id,relative_path,
		expected_digest,proposed_digest,content,state,confirmation_hash,confirmation_phrase,approval_policy,created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, proposal.ID, proposal.IdempotencyKey, requestDigest,
		proposal.ActorHash, proposal.RootID, proposal.Path, proposal.ExpectedDigest,
		proposal.ProposedDigest, proposal.Content, proposal.State,
		HashConfirmation(proposal.ConfirmationPhrase), proposal.ConfirmationPhrase, proposal.ApprovalPolicy, timeText(proposal.CreatedAt))
	if err != nil {
		return model.ManagedFileProposal{}, false, err
	}
	if err := appendManagedFileAudit(ctx, tx, proposal.ActorHash, "file.proposal.created", proposal.State, proposal, proposal.CreatedAt); err != nil {
		return model.ManagedFileProposal{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return model.ManagedFileProposal{}, false, err
	}
	return proposal, true, nil
}

type storedFileProposal struct {
	proposal      model.ManagedFileProposal
	requestDigest string
}

func managedFileProposalByKey(
	ctx context.Context,
	db queryer,
	key string,
) (storedFileProposal, bool, error) {
	row, found, err := queryManagedFileProposal(ctx, db, "idempotency_key", key)
	if err != nil || !found {
		return storedFileProposal{}, found, err
	}
	return storedFileProposal{proposal: row.proposal, requestDigest: row.requestDigest}, true, nil
}

func (store *Store) ReserveExtensionPackage(
	ctx context.Context,
	result model.ExtensionUploadResult,
	actor, requestDigest, storagePath string,
) (model.ExtensionUploadResult, bool, error) {
	manifestJSON, err := json.Marshal(result.Manifest)
	if err != nil {
		return model.ExtensionUploadResult{}, false, err
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return model.ExtensionUploadResult{}, false, err
	}
	defer tx.Rollback()
	existing, found, err := extensionPackageByKey(ctx, tx, result.IdempotencyKey)
	if err != nil {
		return model.ExtensionUploadResult{}, false, err
	}
	if found {
		if existing.requestDigest != requestDigest || existing.actor != actor {
			return model.ExtensionUploadResult{}, false, ErrIdempotency
		}
		return existing.result, false, nil
	}
	var collision int
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM extension_packages
		WHERE package_id=? AND version=?`, result.Manifest.ID, result.Manifest.Version).Scan(&collision)
	if err != nil {
		return model.ExtensionUploadResult{}, false, err
	}
	if collision != 0 {
		return model.ExtensionUploadResult{}, false, errors.New("扩展版本已经存在")
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO extension_packages(
		package_id,version,idempotency_key,request_digest,actor_hash,manifest_json,
		storage_path,storage_digest,state,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		result.Manifest.ID, result.Manifest.Version, result.IdempotencyKey, requestDigest,
		actor, string(manifestJSON), storagePath, result.StorageDigest, result.State,
		timeText(result.CreatedAt))
	if err != nil {
		return model.ExtensionUploadResult{}, false, err
	}
	if err := appendExtensionUploadAudit(ctx, tx, actor,
		"extension.upload.reserved", "accepted", result, result.CreatedAt); err != nil {
		return model.ExtensionUploadResult{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return model.ExtensionUploadResult{}, false, err
	}
	return result, true, nil
}

type storedExtensionPackage struct {
	result        model.ExtensionUploadResult
	actor         string
	requestDigest string
}

func extensionPackageByKey(
	ctx context.Context,
	db queryer,
	key string,
) (storedExtensionPackage, bool, error) {
	row := db.QueryRowContext(ctx, `SELECT manifest_json,idempotency_key,request_digest,
		actor_hash,storage_digest,state,created_at FROM extension_packages WHERE idempotency_key=?`, key)
	var result storedExtensionPackage
	var manifestJSON, createdAt string
	err := row.Scan(&manifestJSON, &result.result.IdempotencyKey, &result.requestDigest,
		&result.actor, &result.result.StorageDigest, &result.result.State, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return storedExtensionPackage{}, false, nil
	}
	if err != nil {
		return storedExtensionPackage{}, false, err
	}
	if err = json.Unmarshal([]byte(manifestJSON), &result.result.Manifest); err != nil {
		return storedExtensionPackage{}, false, err
	}
	result.result.Stored = result.result.State == "stored"
	result.result.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	return result, err == nil, err
}

func (store *Store) MarkExtensionStored(ctx context.Context, packageID, version string) error {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stored, found, err := extensionPackageByIdentity(ctx, tx, packageID, version)
	if err != nil {
		return err
	}
	if !found {
		return ErrNotFound
	}
	result, err := tx.ExecContext(ctx, `UPDATE extension_packages SET state='stored'
		WHERE package_id=? AND version=? AND state='staging'`, packageID, version)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrNotFound
	}
	stored.result.State, stored.result.Stored = "stored", true
	if err := appendExtensionUploadAudit(ctx, tx, stored.actor,
		"extension.uploaded", "accepted", stored.result, store.now()); err != nil {
		return err
	}
	return tx.Commit()
}

// MarkExtensionFailed closes a staging reservation after an I/O or integrity
// failure. The row is retained for audit/idempotency, while callers remove the
// controlled staging file separately.
func (store *Store) MarkExtensionFailed(ctx context.Context, packageID, version string, reason ...string) error {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stored, found, err := extensionPackageByIdentity(ctx, tx, packageID, version)
	if err != nil {
		return err
	}
	if !found {
		return ErrNotFound
	}
	result, err := tx.ExecContext(ctx, `UPDATE extension_packages SET state='failed'
		WHERE package_id=? AND version=? AND state='staging'`, packageID, version)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrNotFound
	}
	stored.result.State, stored.result.Stored = "failed", false
	if len(reason) > 0 {
		stored.result.Reason = reason[0]
	}
	if err := appendExtensionUploadAudit(ctx, tx, stored.actor,
		"extension.upload.failed", "failed", stored.result, store.now()); err != nil {
		return err
	}
	return tx.Commit()
}

func extensionPackageByIdentity(
	ctx context.Context,
	db queryer,
	packageID, version string,
) (storedExtensionPackage, bool, error) {
	row := db.QueryRowContext(ctx, `SELECT manifest_json,idempotency_key,request_digest,
		actor_hash,storage_digest,state,created_at FROM extension_packages
		WHERE package_id=? AND version=?`, packageID, version)
	var result storedExtensionPackage
	var manifestJSON, createdAt string
	err := row.Scan(&manifestJSON, &result.result.IdempotencyKey, &result.requestDigest,
		&result.actor, &result.result.StorageDigest, &result.result.State, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return storedExtensionPackage{}, false, nil
	}
	if err != nil {
		return storedExtensionPackage{}, false, err
	}
	if err = json.Unmarshal([]byte(manifestJSON), &result.result.Manifest); err != nil {
		return storedExtensionPackage{}, false, err
	}
	result.result.Stored = result.result.State == "stored"
	result.result.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	return result, err == nil, err
}

func appendExtensionUploadAudit(
	ctx context.Context,
	tx *sql.Tx,
	actor, event, outcome string,
	result model.ExtensionUploadResult,
	now time.Time,
) error {
	return appendPlanAudit(ctx, tx, model.AuditEntry{
		ActorHash: actor, Event: event, Resource: "extension:" + result.Manifest.ID, Outcome: outcome,
		Detail: map[string]any{
			"version": result.Manifest.Version, "digest": result.Manifest.Digest,
			"state": result.State, "reason": result.Reason,
		},
	}, now)
}

func (store *Store) ListExtensionPackages(
	ctx context.Context,
) ([]model.ExtensionUploadResult, error) {
	rows, err := store.db.QueryContext(ctx, `SELECT manifest_json,idempotency_key,storage_digest,state,created_at FROM extension_packages ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]model.ExtensionUploadResult, 0)
	for rows.Next() {
		var item model.ExtensionUploadResult
		var manifestJSON, createdAt string
		if err := rows.Scan(&manifestJSON, &item.IdempotencyKey, &item.StorageDigest, &item.State, &createdAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(manifestJSON), &item.Manifest); err != nil {
			return nil, err
		}
		item.Stored = item.State == "stored"
		item.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (store *Store) ReserveRunnerUpdate(
	ctx context.Context,
	update model.RunnerUpdate,
	actor string,
) (model.RunnerUpdate, bool, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return model.RunnerUpdate{}, false, err
	}
	defer tx.Rollback()
	existing, existingActor, found, err := runnerUpdateByKey(ctx, tx, update.IdempotencyKey)
	if err != nil {
		return model.RunnerUpdate{}, false, err
	}
	if found {
		if existing.RequestDigest != update.RequestDigest || existingActor != actor {
			return model.RunnerUpdate{}, false, ErrIdempotency
		}
		return existing, false, nil
	}
	var active int
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM runner_updates
		WHERE runner_id=? AND state IN ('prepared','activating','needs_attention')`, update.RunnerID).Scan(&active)
	if err != nil {
		return model.RunnerUpdate{}, false, err
	}
	if active != 0 {
		return model.RunnerUpdate{}, false, errors.New("Runner 已有待处理更新")
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO runner_updates(
		id,idempotency_key,request_digest,actor_hash,runner_id,target_version,
		artifact_path,artifact_digest,artifact_revision,publisher,artifact_signature,
		manifest_purpose,manifest_schema,manifest_goos,manifest_goarch,staged_path,
		binary_path,unit_name,health_timeout_seconds,state,phase,previous_version,
		previous_revision,previous_digest,confirmation_hash,confirmation_phrase,approved_by_hash,
			activation_idempotency_key,resolved_by_hash,resolution_idempotency_key,cancelled_by_hash,
			cancellation_idempotency_key,rollback_path,error,fencing_token,lease_expires_at,
			resolution_decision,resolution_evidence_json,created_at,activated_at,finished_at)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, update.ID, update.IdempotencyKey,
		update.RequestDigest, actor, update.RunnerID, update.TargetVersion,
		update.ArtifactPath, update.ArtifactDigest, update.ArtifactRevision, update.Publisher,
		update.ArtifactSignature, update.ManifestPurpose, update.ManifestSchema,
		update.ManifestGOOS, update.ManifestGOARCH, update.StagedPath, update.BinaryPath, update.UnitName,
		update.HealthTimeoutSeconds,
		update.State, update.Phase, update.PreviousVersion, update.PreviousRevision, update.PreviousDigest,
		HashConfirmation(update.ConfirmationPhrase), update.ConfirmationPhrase, update.ApprovedByHash,
		update.ActivationIdempotencyKey, update.ResolvedByHash, update.ResolutionIdempotencyKey,
		update.CancelledByHash, update.CancellationIdempotencyKey,
		update.RollbackPath, update.Error, update.FencingToken, nullableTimeText(update.LeaseExpiresAt),
		update.ResolutionDecision, "{}", timeText(update.CreatedAt), nullableTimeText(update.ActivatedAt), nullableTimeText(update.FinishedAt))
	if err != nil {
		return model.RunnerUpdate{}, false, err
	}
	if err := appendRunnerUpdateAudit(ctx, tx, actor, "runner.update.prepared", "accepted", update, store.now()); err != nil {
		return model.RunnerUpdate{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return model.RunnerUpdate{}, false, err
	}
	return update, true, nil
}

func (store *Store) ListPendingRunnerUpdates(
	ctx context.Context,
	runnerID string,
) ([]model.RunnerUpdate, error) {
	rows, err := store.db.QueryContext(ctx, runnerUpdateSelect+` WHERE runner_id=?
		AND state IN ('prepared','activating','needs_attention') ORDER BY created_at DESC`, runnerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]model.RunnerUpdate, 0)
	for rows.Next() {
		update, err := scanRunnerUpdate(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, update)
	}
	return result, rows.Err()
}

func (store *Store) ListRunnerUpdates(
	ctx context.Context,
	runnerID string,
	limit int,
) ([]model.RunnerUpdate, error) {
	rows, err := store.db.QueryContext(ctx, runnerUpdateSelect+` WHERE runner_id=? ORDER BY created_at DESC LIMIT ?`, runnerID, clampLimit(limit, 100))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]model.RunnerUpdate, 0)
	for rows.Next() {
		update, err := scanRunnerUpdate(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, update)
	}
	return result, rows.Err()
}

func (store *Store) GetRunnerUpdate(ctx context.Context, id string) (model.RunnerUpdate, error) {
	row := store.db.QueryRowContext(ctx, runnerUpdateSelect+` WHERE id=?`, id)
	update, _, err := scanRunnerUpdateWithActor(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.RunnerUpdate{}, ErrNotFound
	}
	return update, err
}

func (store *Store) GetRunnerUpdateByIdempotency(
	ctx context.Context,
	key string,
) (model.RunnerUpdate, string, bool, error) {
	return runnerUpdateByKey(ctx, store.db, key)
}

func (store *Store) BeginRunnerUpdateActivation(
	ctx context.Context,
	id, actor, idempotencyKey, confirmation string,
) (model.RunnerUpdate, bool, error) {
	if actor == "" || idempotencyKey == "" || confirmation == "" {
		return model.RunnerUpdate{}, false, errors.New("Runner 更新激活信息不完整")
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return model.RunnerUpdate{}, false, err
	}
	defer tx.Rollback()
	row := tx.QueryRowContext(ctx, runnerUpdateSelect+` WHERE id=?`, id)
	update, preparedBy, err := scanRunnerUpdateWithActor(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.RunnerUpdate{}, false, ErrNotFound
	}
	if err != nil {
		return model.RunnerUpdate{}, false, err
	}
	if update.ActivationIdempotencyKey != "" {
		if update.ActivationIdempotencyKey != idempotencyKey || update.ApprovedByHash != actor {
			return model.RunnerUpdate{}, false, ErrIdempotency
		}
		return update, false, tx.Commit()
	}
	if update.State != "prepared" {
		return model.RunnerUpdate{}, false, errors.New("Runner 更新当前不能激活")
	}
	if preparedBy == actor {
		return model.RunnerUpdate{}, false, errors.New("Runner 更新准备人与激活批准人必须不同")
	}
	var expectedConfirmationHash string
	if err := tx.QueryRowContext(ctx, `SELECT confirmation_hash FROM runner_updates WHERE id=?`, id).Scan(&expectedConfirmationHash); err != nil {
		return model.RunnerUpdate{}, false, err
	}
	if expectedConfirmationHash == "" || !constantTimeEqual(expectedConfirmationHash, HashConfirmation(confirmation)) {
		return model.RunnerUpdate{}, false, ErrConfirmation
	}
	now := store.now()
	fencingToken, err := newFencingToken()
	if err != nil {
		return model.RunnerUpdate{}, false, err
	}
	leaseExpires := now.Add(defaultRunnerUpdateLease)
	result, err := tx.ExecContext(ctx, `UPDATE runner_updates SET state='activating',phase='queued',approved_by_hash=?,activation_idempotency_key=?,activated_at=?,executor_heartbeat_at=?,fencing_token=?,lease_expires_at=? WHERE id=? AND state='prepared' AND fencing_token=''`, actor, idempotencyKey, timeText(now), timeText(now), fencingToken, timeText(leaseExpires), id)
	if err = requireOne(result, err, "Runner 更新激活状态写入失败"); err != nil {
		return model.RunnerUpdate{}, false, err
	}
	update.State, update.Phase, update.ApprovedByHash = "activating", "queued", actor
	update.ActivationIdempotencyKey, update.ActivatedAt, update.ExecutorHeartbeatAt = idempotencyKey, &now, &now
	update.FencingToken, update.LeaseExpiresAt = fencingToken, &leaseExpires
	if err := appendRunnerUpdateAudit(ctx, tx, actor, "runner.update.activation_requested", "accepted", update, now); err != nil {
		return model.RunnerUpdate{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return model.RunnerUpdate{}, false, err
	}
	return update, true, nil
}

const defaultRunnerUpdateLease = 2 * time.Minute

func newFencingToken() (string, error) {
	buffer := make([]byte, 32)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return hex.EncodeToString(buffer), nil
}

func constantTimeEqual(left, right string) bool {
	if left == "" || right == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}

func (store *Store) UpdateRunnerUpdatePhase(ctx context.Context, id, phase, rollbackPath string, fencingToken ...string) error {
	if id == "" || phase == "" {
		return errors.New("Runner 更新阶段无效")
	}
	if len(fencingToken) == 0 || fencingToken[0] == "" {
		return errors.New("Runner 更新阶段 fencing token 缺失")
	}
	return store.UpdateRunnerUpdatePhaseCAS(ctx, id, fencingToken[0], "", phase, rollbackPath)
}

// UpdateRunnerUpdatePhaseCAS changes a phase only while the caller still owns
// the fencing token. expectedPhase may be empty for a lease-only transition.
func (store *Store) UpdateRunnerUpdatePhaseCAS(
	ctx context.Context, id, fencingToken, expectedPhase, phase, rollbackPath string,
) error {
	if id == "" || fencingToken == "" || phase == "" {
		return errors.New("Runner 更新 fencing/阶段无效")
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := store.now()
	query := `UPDATE runner_updates SET phase=?,rollback_path=CASE WHEN ?='' THEN rollback_path ELSE ? END,
		 executor_heartbeat_at=?,lease_expires_at=? WHERE id=? AND state='activating' AND fencing_token=?
		AND lease_expires_at>?`
	args := []any{phase, rollbackPath, rollbackPath, timeText(now), timeText(now.Add(defaultRunnerUpdateLease)), id, fencingToken, timeText(now)}
	if expectedPhase != "" {
		query += ` AND phase=?`
		args = append(args, expectedPhase)
	}
	result, err := tx.ExecContext(ctx, query, args...)
	if err := requireOne(result, err, "Runner 更新阶段 fencing 校验失败"); err != nil {
		return err
	}
	update, preparedBy, err := scanRunnerUpdateWithActor(tx.QueryRowContext(ctx, runnerUpdateSelect+` WHERE id=?`, id))
	if err != nil {
		return err
	}
	actor := update.ApprovedByHash
	if actor == "" {
		actor = preparedBy
	}
	if err := appendRunnerUpdateAudit(ctx, tx, actor, "runner.update.phase_changed", "accepted", update, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (store *Store) HeartbeatRunnerUpdate(ctx context.Context, id string, fencingToken ...string) error {
	if len(fencingToken) == 0 || fencingToken[0] == "" {
		return errors.New("Runner 更新心跳 fencing token 缺失")
	}
	now := store.now()
	result, err := store.db.ExecContext(ctx, `UPDATE runner_updates SET executor_heartbeat_at=?,lease_expires_at=?
		WHERE id=? AND state='activating' AND fencing_token=? AND lease_expires_at>?`,
		timeText(now), timeText(now.Add(defaultRunnerUpdateLease)), id, fencingToken[0], timeText(now))
	return requireOne(result, err, "Runner 更新执行心跳写入失败")
}

func (store *Store) FinishRunnerUpdate(ctx context.Context, id, state, phase, rollbackPath, errorText string, fencingToken ...string) error {
	if len(fencingToken) == 0 || fencingToken[0] == "" {
		return errors.New("Runner 更新终态 fencing token 缺失")
	}
	return store.FinishRunnerUpdateCAS(ctx, id, fencingToken[0], state, phase, rollbackPath, errorText)
}

func (store *Store) FinishRunnerUpdateCAS(
	ctx context.Context, id, fencingToken, state, phase, rollbackPath, errorText string,
) error {
	if fencingToken == "" {
		return errors.New("Runner 更新 fencing token 缺失")
	}
	if state != "succeeded" && state != "rolled_back" && state != "needs_attention" && state != "failed" {
		return errors.New("Runner 更新终态无效")
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	finished := store.now()
	result, err := tx.ExecContext(ctx, `UPDATE runner_updates SET state=?,phase=?,rollback_path=?,error=?,finished_at=?,executor_heartbeat_at=NULL,lease_expires_at=NULL
		WHERE id=? AND state='activating' AND fencing_token=? AND lease_expires_at>?`,
		state, phase, rollbackPath, errorText, timeText(finished), id, fencingToken, timeText(finished))
	if err := requireOne(result, err, "Runner 更新终态 fencing 校验失败"); err != nil {
		return err
	}
	update, preparedBy, err := scanRunnerUpdateWithActor(tx.QueryRowContext(ctx, runnerUpdateSelect+` WHERE id=?`, id))
	if err != nil {
		return err
	}
	actor := update.ApprovedByHash
	if actor == "" {
		actor = preparedBy
	}
	if err := appendRunnerUpdateAudit(ctx, tx, actor, "runner.update."+state, state, update, finished); err != nil {
		return err
	}
	return tx.Commit()
}

func (store *Store) RecoverInterruptedRunnerUpdates(ctx context.Context) (int64, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	now := store.now()
	cutoff := timeText(now.Add(-2 * time.Minute))
	rows, err := tx.QueryContext(ctx, runnerUpdateSelect+` WHERE state='activating' AND
		((lease_expires_at IS NOT NULL AND lease_expires_at <= ?) OR
		 (lease_expires_at IS NULL AND (executor_heartbeat_at IS NULL OR executor_heartbeat_at < ?)))`,
		timeText(now), cutoff)
	if err != nil {
		return 0, err
	}
	updates := make([]model.RunnerUpdate, 0)
	preparedBy := make(map[string]string)
	for rows.Next() {
		update, actor, scanErr := scanRunnerUpdateWithActor(rows)
		if scanErr != nil {
			rows.Close()
			return 0, scanErr
		}
		updates = append(updates, update)
		preparedBy[update.ID] = actor
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	const interruptionError = "Runner 更新执行器租约超时，当前二进制身份需要人工核对"
	for index := range updates {
		update := &updates[index]
		result, err := tx.ExecContext(ctx, `UPDATE runner_updates
			SET state='needs_attention',phase='interrupted',error=?,finished_at=?,executor_heartbeat_at=NULL,lease_expires_at=NULL
			WHERE id=? AND state='activating'`, interruptionError, timeText(now), update.ID)
		if err := requireOne(result, err, "Runner 更新中断恢复失败"); err != nil {
			return 0, err
		}
		update.State, update.Phase, update.Error = "needs_attention", "interrupted", interruptionError
		update.FinishedAt, update.ExecutorHeartbeatAt, update.LeaseExpiresAt = &now, nil, nil
		actor := update.ApprovedByHash
		if actor == "" {
			actor = preparedBy[update.ID]
		}
		if err := appendRunnerUpdateAudit(ctx, tx, actor, "runner.update.needs_attention", "needs_attention", *update, now); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return int64(len(updates)), nil
}

func (store *Store) ResolveRunnerUpdate(
	ctx context.Context,
	id, actor, idempotencyKey string,
	evidence ...model.RunnerUpdateResolutionEvidence,
) (model.RunnerUpdate, bool, error) {
	if actor == "" || idempotencyKey == "" {
		return model.RunnerUpdate{}, false, errors.New("Runner 更新人工收口信息不完整")
	}
	var submitted model.RunnerUpdateResolutionEvidence
	if len(evidence) > 0 {
		submitted = evidence[0]
	}
	legacy := len(evidence) == 0
	if !legacy {
		if err := validateRunnerResolutionEvidence(submitted); err != nil {
			return model.RunnerUpdate{}, false, err
		}
	} else {
		// The Engine rejects this path for schema 4. Keeping the store-level
		// compatibility branch lets a schema-3 control plane drain records that
		// were created before structured resolution evidence existed.
		submitted = model.RunnerUpdateResolutionEvidence{}
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return model.RunnerUpdate{}, false, err
	}
	defer tx.Rollback()
	update, _, err := scanRunnerUpdateWithActor(tx.QueryRowContext(ctx, runnerUpdateSelect+` WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return model.RunnerUpdate{}, false, ErrNotFound
	}
	if err != nil {
		return model.RunnerUpdate{}, false, err
	}
	if update.ResolutionIdempotencyKey != "" {
		if update.ResolutionIdempotencyKey != idempotencyKey || update.ResolvedByHash != actor {
			return model.RunnerUpdate{}, false, ErrIdempotency
		}
		if len(evidence) > 0 {
			encoded, marshalErr := json.Marshal(submitted)
			if marshalErr != nil || string(encoded) != string(update.ResolutionEvidenceJSON) {
				return model.RunnerUpdate{}, false, ErrIdempotency
			}
		}
		return update, false, tx.Commit()
	}
	if update.State != "needs_attention" {
		return model.RunnerUpdate{}, false, errors.New("Runner 更新当前不需要人工收口")
	}
	if !legacy && (actor == update.PreparedByHash || actor == update.ApprovedByHash) {
		return model.RunnerUpdate{}, false, errors.New("Runner 人工收口人与准备人或批准人必须不同")
	}
	evidenceJSON, err := json.Marshal(submitted)
	if err != nil {
		return model.RunnerUpdate{}, false, err
	}
	finished := store.now()
	result, execErr := tx.ExecContext(ctx, `UPDATE runner_updates SET state='failed',phase='manually_resolved',resolved_by_hash=?,resolution_idempotency_key=?,resolution_decision=?,resolution_evidence_json=?,finished_at=?,lease_expires_at=NULL WHERE id=? AND state='needs_attention'`, actor, idempotencyKey, submitted.Decision, string(evidenceJSON), timeText(finished), id)
	if err := requireOne(result, execErr, "Runner 更新人工收口失败"); err != nil {
		return model.RunnerUpdate{}, false, err
	}
	update.State, update.Phase, update.ResolvedByHash = "failed", "manually_resolved", actor
	update.ResolutionIdempotencyKey, update.FinishedAt = idempotencyKey, &finished
	update.ResolutionDecision, update.ResolutionEvidence = submitted.Decision, submitted
	if err := appendRunnerUpdateAudit(ctx, tx, actor, "runner.update.manually_resolved", "accepted", update, finished); err != nil {
		return model.RunnerUpdate{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return model.RunnerUpdate{}, false, err
	}
	return update, true, nil
}

func validateRunnerResolutionEvidence(evidence model.RunnerUpdateResolutionEvidence) error {
	if evidence.Decision != "keep" && evidence.Decision != "rollback" && evidence.Decision != "abort" {
		return errors.New("Runner 人工收口 decision 无效")
	}
	if evidence.ObservedVersion == "" || evidence.ObservedRevision == "" || evidence.ObservedDigest == "" {
		return errors.New("Runner 人工收口必须提供完整观测身份")
	}
	if evidence.Reason == "" || len(evidence.Reason) > 2048 {
		return errors.New("Runner 人工收口 reason 无效")
	}
	return nil
}

func (store *Store) CancelRunnerUpdate(
	ctx context.Context,
	id, actor, idempotencyKey string,
) (model.RunnerUpdate, bool, error) {
	if actor == "" || idempotencyKey == "" {
		return model.RunnerUpdate{}, false, errors.New("Runner 更新取消信息不完整")
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return model.RunnerUpdate{}, false, err
	}
	defer tx.Rollback()
	update, _, err := scanRunnerUpdateWithActor(tx.QueryRowContext(ctx, runnerUpdateSelect+` WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return model.RunnerUpdate{}, false, ErrNotFound
	}
	if err != nil {
		return model.RunnerUpdate{}, false, err
	}
	if update.CancellationIdempotencyKey != "" {
		if update.CancellationIdempotencyKey != idempotencyKey || update.CancelledByHash != actor {
			return model.RunnerUpdate{}, false, ErrIdempotency
		}
		return update, false, tx.Commit()
	}
	if update.State != "prepared" {
		return model.RunnerUpdate{}, false, errors.New("Runner 更新当前不能取消")
	}
	finished := store.now()
	result, execErr := tx.ExecContext(ctx, `UPDATE runner_updates
		SET state='cancelled',phase='cancelled',cancelled_by_hash=?,cancellation_idempotency_key=?,finished_at=?
		WHERE id=? AND state='prepared'`, actor, idempotencyKey, timeText(finished), id)
	if err := requireOne(result, execErr, "Runner 更新取消失败"); err != nil {
		return model.RunnerUpdate{}, false, err
	}
	update.State, update.Phase, update.CancelledByHash = "cancelled", "cancelled", actor
	update.CancellationIdempotencyKey, update.FinishedAt = idempotencyKey, &finished
	if err := appendRunnerUpdateAudit(ctx, tx, actor, "runner.update.cancelled", "accepted", update, finished); err != nil {
		return model.RunnerUpdate{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return model.RunnerUpdate{}, false, err
	}
	return update, true, nil
}

func appendRunnerUpdateAudit(
	ctx context.Context,
	tx *sql.Tx,
	actor, event, outcome string,
	update model.RunnerUpdate,
	now time.Time,
) error {
	return appendPlanAudit(ctx, tx, model.AuditEntry{
		ActorHash: actor, Event: event, Resource: "runner:" + update.RunnerID, Outcome: outcome,
		Detail: map[string]any{
			"updateId": update.ID, "targetVersion": update.TargetVersion,
			"artifactDigest": update.ArtifactDigest, "artifactRevision": update.ArtifactRevision,
			"phase": update.Phase, "error": update.Error,
		},
	}, now)
}

func runnerUpdateByKey(
	ctx context.Context,
	db queryer,
	key string,
) (model.RunnerUpdate, string, bool, error) {
	row := db.QueryRowContext(ctx, runnerUpdateSelect+` WHERE idempotency_key=?`, key)
	update, actor, err := scanRunnerUpdateWithActor(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.RunnerUpdate{}, "", false, nil
	}
	return update, actor, err == nil, err
}

func scanRunnerUpdate(row scanner) (model.RunnerUpdate, error) {
	update, _, err := scanRunnerUpdateWithActor(row)
	return update, err
}

func scanRunnerUpdateWithActor(row scanner) (model.RunnerUpdate, string, error) {
	var update model.RunnerUpdate
	var actor, createdAt string
	var activatedAt, finishedAt, heartbeatAt, leaseExpiresAt sql.NullString
	var resolutionEvidenceJSON string
	err := row.Scan(&update.ID, &update.IdempotencyKey, &update.RequestDigest,
		&update.RunnerID, &update.TargetVersion, &update.ArtifactPath,
		&update.ArtifactDigest, &update.ArtifactRevision, &update.Publisher, &update.ArtifactSignature,
		&update.ManifestPurpose, &update.ManifestSchema, &update.ManifestGOOS, &update.ManifestGOARCH,
		&update.StagedPath, &update.BinaryPath, &update.UnitName, &update.HealthTimeoutSeconds,
		&update.State, &update.Phase,
		&update.PreviousVersion, &update.PreviousRevision, &update.PreviousDigest,
		&update.ConfirmationPhrase, &update.ApprovedByHash, &update.ActivationIdempotencyKey,
		&update.ResolvedByHash, &update.ResolutionIdempotencyKey, &update.CancelledByHash,
		&update.CancellationIdempotencyKey, &update.RollbackPath, &update.Error,
		&update.FencingToken, &leaseExpiresAt, &update.ResolutionDecision, &resolutionEvidenceJSON,
		&createdAt, &activatedAt, &finishedAt, &heartbeatAt, &actor)
	if err != nil {
		return model.RunnerUpdate{}, "", err
	}
	update.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err == nil {
		update.ActivatedAt, err = nullableTime(activatedAt)
	}
	if err == nil {
		update.FinishedAt, err = nullableTime(finishedAt)
	}
	if err == nil {
		update.ExecutorHeartbeatAt, err = nullableTime(heartbeatAt)
	}
	if err == nil {
		update.LeaseExpiresAt, err = nullableTime(leaseExpiresAt)
	}
	if err == nil {
		update.ResolutionEvidenceJSON = resolutionEvidenceJSON
		if resolutionEvidenceJSON != "" && resolutionEvidenceJSON != "{}" {
			err = json.Unmarshal([]byte(resolutionEvidenceJSON), &update.ResolutionEvidence)
		}
	}
	update.PreparedByHash = actor
	return update, actor, err
}

const runnerUpdateSelect = `SELECT id,idempotency_key,request_digest,runner_id,
	target_version,artifact_path,artifact_digest,artifact_revision,publisher,artifact_signature,
	manifest_purpose,manifest_schema,manifest_goos,manifest_goarch,staged_path,
	binary_path,unit_name,health_timeout_seconds,state,phase,
	previous_version,previous_revision,previous_digest,confirmation_phrase,approved_by_hash,
	activation_idempotency_key,resolved_by_hash,resolution_idempotency_key,cancelled_by_hash,
	cancellation_idempotency_key,rollback_path,error,fencing_token,lease_expires_at,
	resolution_decision,resolution_evidence_json,created_at,activated_at,finished_at,
	executor_heartbeat_at,actor_hash FROM runner_updates`
