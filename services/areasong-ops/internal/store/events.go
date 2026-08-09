package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/AreaSong/ops/services/areasong-ops/internal/model"
)

func (store *Store) AppendEvent(ctx context.Context, event model.Event) (model.Event, error) {
	if event.OccurredAt.IsZero() {
		event.OccurredAt = store.now()
	}
	data, err := encodeJSON(event.Data)
	if err != nil {
		return model.Event{}, err
	}
	result, err := store.db.ExecContext(ctx, `
        INSERT INTO events (task_id, occurred_at, level, phase, message, data_json)
        VALUES (?, ?, ?, ?, ?, ?)
    `, event.TaskID, timeText(event.OccurredAt), event.Level, event.Phase, event.Message, data)
	if err != nil {
		return model.Event{}, err
	}
	event.Sequence, err = result.LastInsertId()
	return event, err
}

func (store *Store) ListEvents(ctx context.Context, after int64, limit int) ([]model.Event, error) {
	rows, err := store.db.QueryContext(ctx, `
        SELECT sequence, task_id, occurred_at, level, phase, message, data_json
        FROM events WHERE sequence > ? ORDER BY sequence ASC LIMIT ?
    `, after, clampLimit(limit, 500))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := make([]model.Event, 0)
	for rows.Next() {
		var event model.Event
		var occurredAt, data string
		if err := rows.Scan(&event.Sequence, &event.TaskID, &occurredAt, &event.Level,
			&event.Phase, &event.Message, &data); err != nil {
			return nil, err
		}
		if event.OccurredAt, err = time.Parse(time.RFC3339Nano, occurredAt); err != nil {
			return nil, err
		}
		if err := decodeJSON(data, &event.Data); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func (store *Store) ListTaskEvents(ctx context.Context, taskID string, limit int) ([]model.Event, error) {
	rows, err := store.db.QueryContext(ctx, `
        SELECT sequence, task_id, occurred_at, level, phase, message, data_json
        FROM events WHERE task_id = ? ORDER BY sequence ASC LIMIT ?
    `, taskID, clampLimit(limit, 500))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := make([]model.Event, 0)
	for rows.Next() {
		var event model.Event
		var occurredAt, data string
		if err := rows.Scan(&event.Sequence, &event.TaskID, &occurredAt, &event.Level,
			&event.Phase, &event.Message, &data); err != nil {
			return nil, err
		}
		if event.OccurredAt, err = time.Parse(time.RFC3339Nano, occurredAt); err != nil {
			return nil, err
		}
		if err := decodeJSON(data, &event.Data); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func (store *Store) AppendAudit(ctx context.Context, entry model.AuditEntry) (model.AuditEntry, error) {
	if entry.OccurredAt.IsZero() {
		entry.OccurredAt = store.now()
	}
	detail, err := encodeJSON(entry.Detail)
	if err != nil {
		return model.AuditEntry{}, err
	}
	result, err := store.db.ExecContext(ctx, `
        INSERT INTO audit_entries (occurred_at, actor_hash, event, resource, outcome, detail_json)
        VALUES (?, ?, ?, ?, ?, ?)
    `, timeText(entry.OccurredAt), entry.ActorHash, entry.Event, entry.Resource,
		entry.Outcome, detail)
	if err != nil {
		return model.AuditEntry{}, err
	}
	entry.Sequence, err = result.LastInsertId()
	return entry, err
}

func (store *Store) ListAudit(ctx context.Context, limit int) ([]model.AuditEntry, error) {
	rows, err := store.db.QueryContext(ctx, `
        SELECT sequence, occurred_at, actor_hash, event, resource, outcome, detail_json
        FROM audit_entries ORDER BY sequence DESC LIMIT ?
    `, clampLimit(limit, 200))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := make([]model.AuditEntry, 0)
	for rows.Next() {
		var entry model.AuditEntry
		var occurredAt, detail string
		if err := rows.Scan(&entry.Sequence, &occurredAt, &entry.ActorHash, &entry.Event,
			&entry.Resource, &entry.Outcome, &detail); err != nil {
			return nil, err
		}
		if entry.OccurredAt, err = time.Parse(time.RFC3339Nano, occurredAt); err != nil {
			return nil, err
		}
		if err := decodeJSON(detail, &entry.Detail); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func (store *Store) LatestEventSequence(ctx context.Context) (int64, error) {
	var sequence sql.NullInt64
	err := store.db.QueryRowContext(ctx, `SELECT MAX(sequence) FROM events`).Scan(&sequence)
	return sequence.Int64, err
}
