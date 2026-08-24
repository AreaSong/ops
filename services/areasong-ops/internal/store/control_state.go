package store

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

type DesiredStateInput struct {
	Service          string
	ObjectID         string
	TenantID         string
	Desired          model.DesiredState
	Reason           string
	ActorHash        string
	MaintenanceUntil *time.Time
	// 幂等键和摘要对遗留/内部写入可选；公开请求应使用
	// SetDesiredStateIdempotent，确保 receipt 与状态变更处于同一事务。
	IdempotencyKey string
	RequestDigest  string
}

func (store *Store) GetServiceState(ctx context.Context, service string) (model.ServiceState, bool, error) {
	return getServiceState(ctx, store.db, service)
}

func getServiceState(ctx context.Context, db queryer, service string) (model.ServiceState, bool, error) {
	var desired, objectID, tenantID, reason, updatedAt string
	var generation int64
	var maintenance sql.NullString
	err := db.QueryRowContext(ctx, `
		SELECT object_id, tenant_id, desired_state, reason, generation, maintenance_until, updated_at
		FROM desired_states WHERE service = ?`, service).
		Scan(&objectID, &tenantID, &desired, &reason, &generation, &maintenance, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return model.ServiceState{}, false, nil
	}
	if err != nil {
		return model.ServiceState{}, false, err
	}
	state := model.ServiceState{Service: service, ObjectID: objectID, TenantID: tenantID,
		Desired: model.DesiredState(desired), Reason: reason, Generation: generation}
	state.DesiredUpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return model.ServiceState{}, false, err
	}
	if maintenance.Valid {
		value, parseErr := time.Parse(time.RFC3339Nano, maintenance.String)
		if parseErr != nil {
			return model.ServiceState{}, false, parseErr
		}
		state.MaintenanceUntil = &value
	}
	var actual, health, observedReason, dataJSON, observedAt string
	var drift int
	err = db.QueryRowContext(ctx, `
		SELECT actual_state, health_state, reason, data_json, observed_at, drift_detected
		FROM state_observations WHERE service = ?`, service).
		Scan(&actual, &health, &observedReason, &dataJSON, &observedAt, &drift)
	if errors.Is(err, sql.ErrNoRows) {
		return state, true, nil
	}
	if err != nil {
		return model.ServiceState{}, false, err
	}
	state.Actual = model.ActualState(actual)
	state.Health = model.HealthState(health)
	state.Reason = firstNonEmpty(observedReason, state.Reason)
	state.ObservedAt, err = time.Parse(time.RFC3339Nano, observedAt)
	if err != nil {
		return model.ServiceState{}, false, err
	}
	if err := decodeJSON(dataJSON, &state.Data); err != nil {
		return model.ServiceState{}, false, err
	}
	if drift != 0 {
		state.Drift = &model.StateDrift{Detected: true, Expected: string(state.Desired), Observed: string(state.Actual), Reason: state.Reason, DetectedAt: state.ObservedAt}
	}
	return state, true, nil
}

func (store *Store) ListServiceStates(ctx context.Context) ([]model.ServiceState, error) {
	rows, err := store.db.QueryContext(ctx, `SELECT service FROM desired_states ORDER BY service`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	states := make([]model.ServiceState, 0)
	for rows.Next() {
		var service string
		if err := rows.Scan(&service); err != nil {
			return nil, err
		}
		state, found, err := store.GetServiceState(ctx, service)
		if err != nil {
			return nil, err
		}
		if found {
			states = append(states, state)
		}
	}
	return states, rows.Err()
}

func (store *Store) SetDesiredState(ctx context.Context, input DesiredStateInput) (model.ServiceState, error) {
	state, _, err := store.setDesiredState(ctx, input, input.IdempotencyKey, input.RequestDigest)
	return state, err
}

// SetDesiredStateIdempotent 将 request receipt 与目标状态变更一起持久化。
// 同一操作者以同一摘要重放时返回原状态，不再生成新的 generation 或事件。
func (store *Store) SetDesiredStateIdempotent(
	ctx context.Context,
	input DesiredStateInput,
	idempotencyKey, requestDigest string,
) (model.ServiceState, bool, error) {
	if idempotencyKey == "" {
		idempotencyKey = input.IdempotencyKey
	}
	if requestDigest == "" {
		requestDigest = input.RequestDigest
	}
	if idempotencyKey == "" {
		return model.ServiceState{}, false, errors.New("幂等请求缺少幂等键")
	}
	return store.setDesiredState(ctx, input, idempotencyKey, requestDigest)
}

// SetDesiredStateWithRequest 为按请求建模的调用方提供明确别名。
func (store *Store) SetDesiredStateWithRequest(
	ctx context.Context,
	input DesiredStateInput,
	idempotencyKey, requestDigest string,
) (model.ServiceState, bool, error) {
	return store.SetDesiredStateIdempotent(ctx, input, idempotencyKey, requestDigest)
}

// UpdateDesiredStateReceiptState 在 runner 完成 inspect 后保存请求的最终观测表示。
// 它刻意不修改 generation/event_sequence，响应归一化不会变成新的状态迁移。
func (store *Store) UpdateDesiredStateReceiptState(
	ctx context.Context,
	idempotencyKey, actorHash string,
	state model.ServiceState,
) error {
	if idempotencyKey == "" || actorHash == "" {
		return errors.New("目标状态 receipt 标识不完整")
	}
	stateJSON, err := encodeJSON(state)
	if err != nil {
		return err
	}
	result, err := store.db.ExecContext(ctx, `
		UPDATE desired_state_requests SET state_json=?
		WHERE idempotency_key=? AND actor_hash=?`, stateJSON, idempotencyKey, actorHash)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return ErrNotFound
	}
	return nil
}

func (store *Store) setDesiredState(
	ctx context.Context,
	input DesiredStateInput,
	idempotencyKey, requestDigest string,
) (model.ServiceState, bool, error) {
	if idempotencyKey == "" {
		idempotencyKey = input.IdempotencyKey
	}
	if requestDigest == "" {
		requestDigest = input.RequestDigest
	}
	input.IdempotencyKey = idempotencyKey
	input.RequestDigest = requestDigest
	now := store.now()
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return model.ServiceState{}, false, err
	}
	defer tx.Rollback()
	state, _, replayed, err := store.setDesiredStateTx(ctx, tx, input, now)
	if err != nil {
		return model.ServiceState{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return model.ServiceState{}, false, err
	}
	return state, replayed, nil
}

// setDesiredStateTx persists one desired-state transition on a caller-owned
// transaction. Callers use this for lifecycle completion so the task, plan,
// audit, terminal event, and desired-state control event commit together.
func (store *Store) setDesiredStateTx(
	ctx context.Context, tx *sql.Tx, input DesiredStateInput, now time.Time,
) (model.ServiceState, int64, bool, error) {
	if input.Service == "" || input.ObjectID == "" || input.Desired == "" {
		return model.ServiceState{}, 0, false, errors.New("目标状态请求不完整")
	}
	if input.TenantID == "" {
		input.TenantID = "default"
	}
	if input.IdempotencyKey != "" && input.RequestDigest == "" {
		return model.ServiceState{}, 0, false, errors.New("幂等请求缺少 request digest")
	}
	if input.IdempotencyKey != "" && input.ActorHash == "" {
		return model.ServiceState{}, 0, false, errors.New("幂等请求缺少操作者")
	}
	if input.IdempotencyKey != "" {
		var actor, existingDigest, existingService, stateJSON string
		var eventSequence int64
		err := tx.QueryRowContext(ctx, `
			SELECT actor_hash, request_digest, service, state_json, event_sequence
			FROM desired_state_requests WHERE idempotency_key = ?`, input.IdempotencyKey).
			Scan(&actor, &existingDigest, &existingService, &stateJSON, &eventSequence)
		if err == nil {
			if actor != input.ActorHash {
				return model.ServiceState{}, 0, false, ErrActorMismatch
			}
			if subtle.ConstantTimeCompare([]byte(existingDigest), []byte(input.RequestDigest)) != 1 || existingService != input.Service {
				return model.ServiceState{}, 0, false, ErrIdempotency
			}
			var state model.ServiceState
			if err := decodeJSON(stateJSON, &state); err != nil {
				return model.ServiceState{}, 0, false, err
			}
			if state.Service != input.Service || state.ObjectID != input.ObjectID ||
				state.TenantID != input.TenantID || state.Desired != input.Desired {
				return model.ServiceState{}, 0, false, ErrIdempotency
			}
			return state, eventSequence, true, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return model.ServiceState{}, 0, false, err
		}
	}
	maintenance := any(nil)
	if input.MaintenanceUntil != nil {
		maintenance = timeText(*input.MaintenanceUntil)
	}
	var generation int64
	err := tx.QueryRowContext(ctx, `SELECT generation FROM desired_states WHERE service = ?`, input.Service).
		Scan(&generation)
	if errors.Is(err, sql.ErrNoRows) {
		generation = 1
	} else if err != nil {
		return model.ServiceState{}, 0, false, err
	} else {
		generation++
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO desired_states(service, object_id, tenant_id, desired_state, reason, actor_hash, generation, maintenance_until, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(service) DO UPDATE SET object_id=excluded.object_id, tenant_id=excluded.tenant_id,
		 desired_state=excluded.desired_state, reason=excluded.reason, actor_hash=excluded.actor_hash,
		 generation=excluded.generation, maintenance_until=excluded.maintenance_until, updated_at=excluded.updated_at`,
		input.Service, input.ObjectID, input.TenantID, input.Desired, input.Reason, input.ActorHash,
		generation, maintenance, timeText(now)); err != nil {
		return model.ServiceState{}, 0, false, err
	}
	eventResult, err := tx.ExecContext(ctx, `
		INSERT INTO control_plane_events(occurred_at, event_type, resource, tenant_id, data_json)
		VALUES(?, 'desired_state.changed', ?, ?, ?)`, timeText(now), input.Service, input.TenantID,
		mustJSON(map[string]any{"desiredState": input.Desired, "generation": generation, "reason": input.Reason}))
	if err != nil {
		return model.ServiceState{}, 0, false, err
	}
	eventSequence, err := eventResult.LastInsertId()
	if err != nil {
		return model.ServiceState{}, 0, false, err
	}
	state, found, err := getServiceState(ctx, tx, input.Service)
	if err != nil {
		return model.ServiceState{}, 0, false, err
	}
	if !found {
		return model.ServiceState{}, 0, false, errors.New("目标状态写入后无法读取")
	}
	if input.IdempotencyKey != "" {
		stateJSON, err := encodeJSON(state)
		if err != nil {
			return model.ServiceState{}, 0, false, err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO desired_state_requests(
				idempotency_key, actor_hash, request_digest, service, state_json,
				generation, event_sequence, created_at)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?)`, input.IdempotencyKey, input.ActorHash, input.RequestDigest,
			input.Service, stateJSON, generation, eventSequence, timeText(now)); err != nil {
			return model.ServiceState{}, 0, false, err
		}
	}
	return state, eventSequence, false, nil
}

func (store *Store) SaveObservation(ctx context.Context, observation model.StateObservation) error {
	if observation.Service == "" || observation.ObjectID == "" {
		return errors.New("状态观测缺少服务标识")
	}
	if observation.TenantID == "" {
		observation.TenantID = "default"
	}
	if observation.ObservedAt.IsZero() {
		observation.ObservedAt = store.now()
	}
	data, err := encodeJSON(observation.Data)
	if err != nil {
		return err
	}
	_, err = store.db.ExecContext(ctx, `
		INSERT INTO state_observations(service, object_id, tenant_id, actual_state, health_state, reason, data_json, observed_at, drift_detected)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(service) DO UPDATE SET object_id=excluded.object_id, tenant_id=excluded.tenant_id,
		 actual_state=excluded.actual_state, health_state=excluded.health_state, reason=excluded.reason,
		 data_json=excluded.data_json, observed_at=excluded.observed_at, drift_detected=excluded.drift_detected`,
		observation.Service, observation.ObjectID, observation.TenantID, observation.Actual,
		observation.Health, observation.Reason, data, timeText(observation.ObservedAt), observation.Drift)
	return err
}

func (store *Store) AppendControlEvent(ctx context.Context, event model.ControlPlaneEvent) error {
	if event.OccurredAt.IsZero() {
		event.OccurredAt = store.now()
	}
	if event.TenantID == "" {
		event.TenantID = "default"
	}
	data, err := encodeJSON(event.Data)
	if err != nil {
		return err
	}
	_, err = store.db.ExecContext(ctx, `INSERT INTO control_plane_events(occurred_at,event_type,resource,tenant_id,data_json) VALUES(?,?,?,?,?)`,
		timeText(event.OccurredAt), event.Type, event.Resource, event.TenantID, data)
	return err
}

func mustJSON(value any) string {
	data, err := encodeJSON(value)
	if err != nil {
		return "{}"
	}
	return data
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
