package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

func (store *Store) UpsertServerNode(ctx context.Context, node model.ServerNode) error {
	if err := node.Validate(); err != nil {
		return err
	}
	labels, err := encodeJSON(node.Labels)
	if err != nil {
		return err
	}
	capabilities, err := encodeJSON(node.Capabilities)
	if err != nil {
		return err
	}
	now := store.now()
	var heartbeat any
	if node.LastHeartbeat != nil {
		heartbeat = timeText(*node.LastHeartbeat)
	}
	_, err = store.db.ExecContext(ctx, `
		INSERT INTO server_nodes(id,hostname,environment,region,address,labels_json,capabilities_json,runner_id,status,max_concurrency,last_heartbeat_at,disabled_reason,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET hostname=excluded.hostname,environment=excluded.environment,region=excluded.region,
		 address=excluded.address,labels_json=excluded.labels_json,capabilities_json=excluded.capabilities_json,
		 runner_id=excluded.runner_id,status=excluded.status,max_concurrency=excluded.max_concurrency,
		 last_heartbeat_at=excluded.last_heartbeat_at,disabled_reason=excluded.disabled_reason,updated_at=excluded.updated_at`,
		node.ID, node.Hostname, node.Environment, node.Region, node.Address, labels, capabilities, node.RunnerID, node.State,
		node.MaxConcurrency, heartbeat, node.DisabledReason, timeText(now), timeText(now))
	return err
}

func (store *Store) UpsertRunnerNode(ctx context.Context, node model.RunnerNode, tenantID string) error {
	if err := node.Validate(); err != nil {
		return err
	}
	if tenantID == "" {
		tenantID = node.TenantID
	}
	if tenantID == "" {
		tenantID = "default"
	}
	node.TenantID = tenantID
	labels, err := encodeJSON(node.Labels)
	if err != nil {
		return err
	}
	capabilities, err := encodeJSON(node.Capabilities)
	if err != nil {
		return err
	}
	now := store.now()
	var heartbeat, lease any
	if node.LastHeartbeat != nil {
		heartbeat = timeText(*node.LastHeartbeat)
	}
	if node.LeaseExpiresAt != nil {
		lease = timeText(*node.LeaseExpiresAt)
	}
	_, err = store.db.ExecContext(ctx, `
		INSERT INTO runner_nodes(id,server_id,hostname,tenant_id,labels_json,capabilities_json,version,revision,binary_digest,identity_payload_version,status,max_concurrency,last_heartbeat_at,lease_expires_at,lease_generation,lease_token,certificate_fingerprint,heartbeat_public_key,disabled_reason,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET server_id=excluded.server_id,hostname=excluded.hostname,tenant_id=excluded.tenant_id,
		 labels_json=excluded.labels_json,capabilities_json=excluded.capabilities_json,version=excluded.version,
		 revision=CASE WHEN excluded.revision='' THEN runner_nodes.revision ELSE excluded.revision END,
		 binary_digest=CASE WHEN excluded.binary_digest='' THEN runner_nodes.binary_digest ELSE excluded.binary_digest END,
		 identity_payload_version=CASE WHEN excluded.identity_payload_version=0 THEN runner_nodes.identity_payload_version ELSE excluded.identity_payload_version END,
		 status=excluded.status,
		 max_concurrency=excluded.max_concurrency,last_heartbeat_at=excluded.last_heartbeat_at,lease_expires_at=excluded.lease_expires_at,
		 certificate_fingerprint=CASE WHEN excluded.certificate_fingerprint='' THEN runner_nodes.certificate_fingerprint ELSE excluded.certificate_fingerprint END,
		 heartbeat_public_key=CASE WHEN excluded.heartbeat_public_key='' THEN runner_nodes.heartbeat_public_key ELSE excluded.heartbeat_public_key END,
		 disabled_reason=excluded.disabled_reason,updated_at=excluded.updated_at`,
		node.ID, node.ServerID, node.Hostname, tenantID, labels, capabilities, node.Version,
		node.Revision, node.BinaryDigest, node.IdentityPayloadVersion, node.State, node.MaxConcurrency,
		heartbeat, lease, node.LeaseGeneration, node.LeaseToken, node.CertificateFingerprint, node.HeartbeatPublicKey,
		node.DisabledReason, timeText(now), timeText(now))
	return err
}

func (store *Store) HeartbeatRunner(ctx context.Context, id, version string, lease time.Duration) (model.RunnerNode, error) {
	return store.heartbeatRunner(ctx, id, version, lease, "", "", false)
}

func (store *Store) HeartbeatRunnerWithReceipt(
	ctx context.Context, id, version string, lease time.Duration, nonce, payloadDigest string,
) (model.RunnerNode, error) {
	return store.heartbeatRunnerWithReceipt(ctx, id, version, "", "", 0, nil, nil, lease, nonce, payloadDigest, false)
}

func (store *Store) HeartbeatRunnerWithReceiptData(
	ctx context.Context, id, version string, capabilities []string, labels map[string]string,
	lease time.Duration, nonce, payloadDigest string,
) (model.RunnerNode, error) {
	return store.heartbeatRunnerWithReceipt(ctx, id, version, "", "", 0, capabilities, labels, lease, nonce, payloadDigest, true)
}

// HeartbeatRunnerAuthenticated is an explicit storage API for the signed
// Runner path; it keeps the legacy receipt method available to existing tests
// and internal callers.
func (store *Store) HeartbeatRunnerAuthenticated(
	ctx context.Context, id, version string, capabilities []string, labels map[string]string,
	lease time.Duration, nonce, payloadDigest string,
) (model.RunnerNode, error) {
	return store.HeartbeatRunnerWithReceiptData(ctx, id, version, capabilities, labels, lease, nonce, payloadDigest)
}

func (store *Store) HeartbeatRunnerIdentityAuthenticated(
	ctx context.Context,
	id, version, revision, binaryDigest string,
	payloadVersion int,
	capabilities []string,
	labels map[string]string,
	lease time.Duration,
	nonce, payloadDigest string,
) (model.RunnerNode, error) {
	return store.heartbeatRunnerWithReceipt(ctx, id, version, revision, binaryDigest,
		payloadVersion, capabilities, labels, lease, nonce, payloadDigest, true)
}

func (store *Store) heartbeatRunnerWithReceipt(
	ctx context.Context, id, version, revision, binaryDigest string, payloadVersion int,
	capabilities []string, labels map[string]string,
	lease time.Duration, nonce, payloadDigest string, updateMetadata bool,
) (model.RunnerNode, error) {
	if nonce == "" {
		return model.RunnerNode{}, errors.New("Runner 心跳 nonce 不能为空")
	}
	if lease <= 0 {
		return model.RunnerNode{}, errors.New("Runner 心跳租约必须大于零")
	}
	var encodedCapabilities, encodedLabels string
	var err error
	if updateMetadata {
		if err := model.ValidateCapabilities(capabilities); err != nil {
			return model.RunnerNode{}, err
		}
		if err := model.ValidateLabels(labels); err != nil {
			return model.RunnerNode{}, err
		}
		if encodedCapabilities, err = encodeJSON(capabilities); err != nil {
			return model.RunnerNode{}, err
		}
		if encodedLabels, err = encodeJSON(labels); err != nil {
			return model.RunnerNode{}, err
		}
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return model.RunnerNode{}, err
	}
	defer tx.Rollback()
	now := store.now()
	if _, err := tx.ExecContext(ctx, `INSERT INTO runner_heartbeat_receipts(runner_id,nonce,occurred_at,payload_digest) VALUES(?,?,?,?)`, id, nonce, timeText(now), payloadDigest); err != nil {
		if isSQLiteConstraintError(err) {
			return model.RunnerNode{}, errors.New("Runner 心跳 nonce 重放")
		}
		return model.RunnerNode{}, err
	}
	expires := now.Add(lease)
	token, tokenErr := newLeaseToken()
	if tokenErr != nil {
		return model.RunnerNode{}, tokenErr
	}
	query := `UPDATE runner_nodes SET version=?,revision=CASE WHEN ?='' THEN revision ELSE ? END,
		binary_digest=CASE WHEN ?='' THEN binary_digest ELSE ? END,
		identity_payload_version=CASE WHEN ?=0 THEN identity_payload_version ELSE ? END,
		status=?,last_heartbeat_at=?,lease_expires_at=?,lease_generation=lease_generation+1,lease_token=?,updated_at=?
		WHERE id=? AND status NOT IN (?,?)`
	arguments := []any{version, revision, revision, binaryDigest, binaryDigest, payloadVersion, payloadVersion,
		model.NodeOnline, timeText(now), timeText(expires), token, timeText(now), id, model.NodeDisabled, model.NodeDraining}
	if updateMetadata {
		query = `UPDATE runner_nodes SET version=?,revision=CASE WHEN ?='' THEN revision ELSE ? END,
			binary_digest=CASE WHEN ?='' THEN binary_digest ELSE ? END,
			identity_payload_version=CASE WHEN ?=0 THEN identity_payload_version ELSE ? END,
			labels_json=?,capabilities_json=?,status=?,last_heartbeat_at=?,lease_expires_at=?,
			lease_generation=lease_generation+1,lease_token=?,updated_at=? WHERE id=? AND status NOT IN (?,?)`
		arguments = []any{version, revision, revision, binaryDigest, binaryDigest, payloadVersion, payloadVersion,
			encodedLabels, encodedCapabilities, model.NodeOnline, timeText(now), timeText(expires), token,
			timeText(now), id, model.NodeDisabled, model.NodeDraining}
	}
	result, err := tx.ExecContext(ctx, query, arguments...)
	if err = requireOne(result, err, "Runner 未登记或当前不可用"); err != nil {
		return model.RunnerNode{}, err
	}
	row := tx.QueryRowContext(ctx, runnerNodeSelect+` WHERE id=?`, id)
	node, err := scanRunnerNode(row)
	if err != nil {
		return model.RunnerNode{}, err
	}
	serverResult, err := tx.ExecContext(ctx, `UPDATE server_nodes
		SET status=?,last_heartbeat_at=?,updated_at=?
		WHERE id=? AND status NOT IN (?,?)`, model.NodeOnline, timeText(now), timeText(now),
		node.ServerID, model.NodeDisabled, model.NodeDraining)
	if err = requireOne(serverResult, err, "Runner 所属 server 未登记或当前不可用"); err != nil {
		return model.RunnerNode{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.RunnerNode{}, err
	}
	node.LastHeartbeatNonce = nonce
	node.LeaseToken = token
	return node, nil
}

func isSQLiteConstraintError(err error) bool {
	type errorCoder interface{ Code() int }
	var coded errorCoder
	if errors.As(err, &coded) {
		// SQLite constraint errors have primary result code 19. Extended result
		// codes retain the primary code in the low byte.
		return coded.Code()&0xff == 19
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "constraint") || strings.Contains(message, "unique")
}

func (store *Store) heartbeatRunner(ctx context.Context, id, version string, lease time.Duration, nonce, payloadDigest string, receipt bool) (model.RunnerNode, error) {
	if receipt {
		return store.HeartbeatRunnerWithReceipt(ctx, id, version, lease, nonce, payloadDigest)
	}
	if lease <= 0 {
		return model.RunnerNode{}, errors.New("Runner 心跳租约必须大于零")
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return model.RunnerNode{}, err
	}
	defer tx.Rollback()
	now := store.now()
	expires := now.Add(lease)
	token, err := newLeaseToken()
	if err != nil {
		return model.RunnerNode{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE runner_nodes SET version=?,status=?,last_heartbeat_at=?,lease_expires_at=?,lease_generation=lease_generation+1,lease_token=?,updated_at=? WHERE id=? AND status NOT IN (?,?)`,
		version, model.NodeOnline, timeText(now), timeText(expires), token, timeText(now), id, model.NodeDisabled, model.NodeDraining)
	if err = requireOne(result, err, "Runner 未登记或当前不可用"); err != nil {
		return model.RunnerNode{}, err
	}
	node, err := scanRunnerNode(tx.QueryRowContext(ctx, runnerNodeSelect+` WHERE id=?`, id))
	if err != nil {
		return model.RunnerNode{}, err
	}
	serverResult, err := tx.ExecContext(ctx, `UPDATE server_nodes
		SET status=?,last_heartbeat_at=?,updated_at=?
		WHERE id=? AND status NOT IN (?,?)`, model.NodeOnline, timeText(now), timeText(now),
		node.ServerID, model.NodeDisabled, model.NodeDraining)
	if err = requireOne(serverResult, err, "Runner 所属 server 未登记或当前不可用"); err != nil {
		return model.RunnerNode{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.RunnerNode{}, err
	}
	node.LeaseToken = token
	return node, nil
}

func (store *Store) GetRunnerNode(ctx context.Context, id string) (model.RunnerNode, bool, error) {
	row := store.db.QueryRowContext(ctx, runnerNodeSelect+` WHERE id=?`, id)
	node, err := scanRunnerNode(row)
	if errors.Is(err, sql.ErrNoRows) {
		return model.RunnerNode{}, false, nil
	}
	return node, err == nil, err
}

func scanRunnerNode(row scanner) (model.RunnerNode, error) {
	var node model.RunnerNode
	var labels, capabilities, state string
	var heartbeat, lease, token, fingerprint, publicKey sql.NullString
	if err := row.Scan(&node.ID, &node.ServerID, &node.TenantID, &node.Hostname, &node.Version, &node.Revision,
		&node.BinaryDigest, &node.IdentityPayloadVersion, &labels, &capabilities, &state,
		&node.MaxConcurrency, &heartbeat, &lease, &node.LeaseGeneration, &token,
		&fingerprint, &publicKey, &node.DisabledReason); err != nil {
		return node, err
	}
	node.State = model.NodeState(state)
	if err := decodeJSON(labels, &node.Labels); err != nil {
		return node, err
	}
	if err := decodeJSON(capabilities, &node.Capabilities); err != nil {
		return node, err
	}
	var err error
	if node.LastHeartbeat, err = nullableTime(heartbeat); err != nil {
		return node, err
	}
	if node.LeaseExpiresAt, err = nullableTime(lease); err != nil {
		return node, err
	}
	if token.Valid {
		node.LeaseToken = token.String
	}
	if fingerprint.Valid {
		node.CertificateFingerprint = fingerprint.String
	}
	if publicKey.Valid {
		node.HeartbeatPublicKey = publicKey.String
	}
	return node, nil
}

const runnerNodeSelect = `SELECT id,server_id,tenant_id,hostname,version,revision,binary_digest,
	identity_payload_version,labels_json,capabilities_json,status,max_concurrency,last_heartbeat_at,
	lease_expires_at,lease_generation,lease_token,certificate_fingerprint,heartbeat_public_key,
	disabled_reason FROM runner_nodes`

func (store *Store) ListFleet(ctx context.Context) (model.Fleet, error) {
	fleet := model.Fleet{Servers: []model.ServerNode{}, Runners: []model.RunnerNode{}}
	serverRows, err := store.db.QueryContext(ctx, `SELECT id,hostname,environment,region,address,labels_json,capabilities_json,runner_id,status,max_concurrency,last_heartbeat_at,disabled_reason FROM server_nodes ORDER BY id`)
	if err != nil {
		return fleet, err
	}
	for serverRows.Next() {
		var node model.ServerNode
		var labels, capabilities, state string
		var heartbeat sql.NullString
		if err := serverRows.Scan(&node.ID, &node.Hostname, &node.Environment, &node.Region, &node.Address, &labels, &capabilities, &node.RunnerID, &state, &node.MaxConcurrency, &heartbeat, &node.DisabledReason); err != nil {
			serverRows.Close()
			return fleet, err
		}
		node.State = model.NodeState(state)
		if err := decodeJSON(labels, &node.Labels); err != nil {
			serverRows.Close()
			return fleet, err
		}
		if err := decodeJSON(capabilities, &node.Capabilities); err != nil {
			serverRows.Close()
			return fleet, err
		}
		node.LastHeartbeat, err = nullableTime(heartbeat)
		if err != nil {
			serverRows.Close()
			return fleet, err
		}
		fleet.Servers = append(fleet.Servers, node)
	}
	if err := serverRows.Close(); err != nil {
		return fleet, err
	}
	runnerRows, err := store.db.QueryContext(ctx, runnerNodeSelect+` ORDER BY id`)
	if err != nil {
		return fleet, err
	}
	defer runnerRows.Close()
	for runnerRows.Next() {
		node, err := scanRunnerNode(runnerRows)
		if err != nil {
			return fleet, err
		}
		fleet.Runners = append(fleet.Runners, node)
	}
	return fleet, runnerRows.Err()
}

func newLeaseToken() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(value), nil
}

// RunnerLeaseValid is the final fencing check before a remote mutation. The
// token is never accepted from a user request; it comes from the authenticated
// heartbeat response and must still be the current durable lease.
func (store *Store) RunnerLeaseValid(ctx context.Context, runnerID string, generation uint64, token string) (bool, error) {
	if runnerID == "" || generation == 0 || token == "" {
		return false, nil
	}
	var expires sql.NullString
	var status string
	var storedGeneration uint64
	var storedToken string
	if err := store.db.QueryRowContext(ctx, `SELECT status,lease_generation,lease_token,lease_expires_at FROM runner_nodes WHERE id=?`, runnerID).
		Scan(&status, &storedGeneration, &storedToken, &expires); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	expiresAt, err := nullableTime(expires)
	if err != nil {
		return false, err
	}
	return status == string(model.NodeOnline) && storedGeneration == generation && storedToken == token && expiresAt != nil && expiresAt.After(store.now()), nil
}
