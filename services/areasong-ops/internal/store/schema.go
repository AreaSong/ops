package store

const schema = `
PRAGMA foreign_keys = ON;
PRAGMA journal_mode = WAL;
PRAGMA synchronous = FULL;
PRAGMA busy_timeout = 5000;

CREATE TABLE IF NOT EXISTS previews (
    id TEXT PRIMARY KEY,
    actor_hash TEXT NOT NULL,
    service TEXT NOT NULL,
    action TEXT NOT NULL,
    target TEXT NOT NULL,
    risk TEXT NOT NULL,
    impact TEXT NOT NULL,
    rollback TEXT NOT NULL,
    scope TEXT NOT NULL,
    steps_json TEXT NOT NULL,
    snapshot_json TEXT NOT NULL,
    confirmation_hash TEXT NOT NULL,
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    consumed_at TEXT
);

CREATE TABLE IF NOT EXISTS tasks (
    id TEXT PRIMARY KEY,
    idempotency_key TEXT NOT NULL UNIQUE,
    request_hash TEXT NOT NULL,
    actor_hash TEXT NOT NULL,
    service TEXT NOT NULL,
    action TEXT NOT NULL,
    target TEXT NOT NULL,
    risk TEXT NOT NULL,
    state TEXT NOT NULL,
    current_phase TEXT NOT NULL DEFAULT '',
    summary TEXT NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT '',
    preview_id TEXT NOT NULL,
    snapshot_json TEXT NOT NULL,
    created_at TEXT NOT NULL,
    started_at TEXT,
    finished_at TEXT
);

CREATE INDEX IF NOT EXISTS idx_tasks_created_at ON tasks(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_tasks_service_state ON tasks(service, state);

CREATE TABLE IF NOT EXISTS events (
    sequence INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id TEXT NOT NULL DEFAULT '',
    occurred_at TEXT NOT NULL,
    level TEXT NOT NULL,
    phase TEXT NOT NULL DEFAULT '',
    message TEXT NOT NULL,
    data_json TEXT NOT NULL,
    FOREIGN KEY(task_id) REFERENCES tasks(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_events_task_sequence ON events(task_id, sequence);

CREATE TABLE IF NOT EXISTS audit_entries (
    sequence INTEGER PRIMARY KEY AUTOINCREMENT,
    occurred_at TEXT NOT NULL,
    actor_hash TEXT NOT NULL,
    event TEXT NOT NULL,
    resource TEXT NOT NULL,
    outcome TEXT NOT NULL,
    detail_json TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_audit_occurred_at ON audit_entries(occurred_at DESC);

CREATE TABLE IF NOT EXISTS metadata (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
`
