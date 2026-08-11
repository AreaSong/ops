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

var migrations = []string{
	`ALTER TABLE tasks ADD COLUMN plan_id TEXT NOT NULL DEFAULT '';
ALTER TABLE tasks ADD COLUMN plan_digest TEXT NOT NULL DEFAULT '';
ALTER TABLE tasks ADD COLUMN parent_task_id TEXT NOT NULL DEFAULT '';
ALTER TABLE tasks ADD COLUMN stages_json TEXT NOT NULL DEFAULT '[]';
ALTER TABLE tasks ADD COLUMN runner_owner TEXT NOT NULL DEFAULT '';
ALTER TABLE tasks ADD COLUMN heartbeat_at TEXT;
ALTER TABLE tasks ADD COLUMN production_changed INTEGER NOT NULL DEFAULT 0 CHECK (production_changed IN (0, 1));
ALTER TABLE tasks ADD COLUMN retryable INTEGER NOT NULL DEFAULT 0 CHECK (retryable IN (0, 1));
ALTER TABLE tasks ADD COLUMN failure_code TEXT NOT NULL DEFAULT '';
ALTER TABLE tasks ADD COLUMN rollback_available INTEGER NOT NULL DEFAULT 0 CHECK (rollback_available IN (0, 1));
ALTER TABLE tasks ADD COLUMN rollback_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE tasks ADD COLUMN recovery_point_id TEXT NOT NULL DEFAULT '';

CREATE TABLE release_plans (
    id TEXT PRIMARY KEY,
    actor_hash TEXT NOT NULL,
    service TEXT NOT NULL,
    action TEXT NOT NULL,
    target TEXT NOT NULL,
    risk TEXT NOT NULL,
    state TEXT NOT NULL,
    digest TEXT NOT NULL,
    approval_summary_json TEXT NOT NULL,
    confirmation_hash TEXT NOT NULL,
    confirmation_phrase TEXT NOT NULL DEFAULT '',
    approved_by_hash TEXT NOT NULL DEFAULT '',
    approved_at TEXT,
    invalidated_reason TEXT NOT NULL DEFAULT '',
    task_id TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE INDEX idx_release_plans_created_at ON release_plans(created_at DESC);
CREATE INDEX idx_release_plans_service_state ON release_plans(service, state);

CREATE TABLE recovery_points (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL,
    service TEXT NOT NULL,
    status TEXT NOT NULL,
    evidence_json TEXT NOT NULL,
    evidence_digest TEXT NOT NULL,
    created_at TEXT NOT NULL,
    verified_at TEXT,
    recoverable_until TEXT,
    FOREIGN KEY(task_id) REFERENCES tasks(id) ON DELETE RESTRICT
);
CREATE INDEX idx_recovery_points_task ON recovery_points(task_id);
CREATE INDEX idx_recovery_points_service_status ON recovery_points(service, status);`,
	`ALTER TABLE release_plans ADD COLUMN observation_seconds INTEGER NOT NULL DEFAULT 0;
ALTER TABLE release_plans ADD COLUMN observation_started_at TEXT;
ALTER TABLE release_plans ADD COLUMN observation_ends_at TEXT;
ALTER TABLE release_plans ADD COLUMN closure_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE release_plans ADD COLUMN closure_idempotency_key TEXT NOT NULL DEFAULT '';
ALTER TABLE release_plans ADD COLUMN closed_at TEXT;
CREATE UNIQUE INDEX idx_release_plans_closure_key
    ON release_plans(closure_idempotency_key) WHERE closure_idempotency_key != '';`,
}
