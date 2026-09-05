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
	`ALTER TABLE release_plans ADD COLUMN maintenance_silence_id TEXT NOT NULL DEFAULT '';
ALTER TABLE release_plans ADD COLUMN maintenance_silence_ends_at TEXT;
ALTER TABLE release_plans ADD COLUMN maintenance_silence_released_at TEXT;
ALTER TABLE release_plans ADD COLUMN blocking_alert_fingerprints_json TEXT NOT NULL DEFAULT '[]';`,
	`ALTER TABLE recovery_points ADD COLUMN expected_before_digest TEXT NOT NULL DEFAULT '';
ALTER TABLE recovery_points ADD COLUMN required_roles_json TEXT NOT NULL DEFAULT '[]';
CREATE INDEX idx_recovery_points_status_expiry
    ON recovery_points(status, recoverable_until);`,
	`CREATE TABLE credential_rotations (
    id TEXT PRIMARY KEY,
    idempotency_key TEXT NOT NULL UNIQUE,
    closure_idempotency_key TEXT NOT NULL DEFAULT '',
    actor_hash TEXT NOT NULL,
    credential_type TEXT NOT NULL,
    target TEXT NOT NULL,
    state TEXT NOT NULL,
    fingerprint TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    validation_result TEXT NOT NULL DEFAULT '',
    outcome TEXT NOT NULL DEFAULT '',
    rollback_result TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    finished_at TEXT,
    closed_at TEXT
);
CREATE INDEX idx_credential_rotations_created_at ON credential_rotations(created_at DESC);
CREATE UNIQUE INDEX idx_credential_rotations_active_type ON credential_rotations(credential_type)
    WHERE state IN ('running', 'switched_pending_revocation', 'revocation_verified', 'needs_attention');
CREATE UNIQUE INDEX idx_credential_rotations_closure_key
	ON credential_rotations(closure_idempotency_key) WHERE closure_idempotency_key != '';`,
	`CREATE TABLE desired_states (
		    service TEXT PRIMARY KEY,
		    object_id TEXT NOT NULL,
		    tenant_id TEXT NOT NULL DEFAULT 'default',
		    desired_state TEXT NOT NULL,
		    reason TEXT NOT NULL DEFAULT '',
		    actor_hash TEXT NOT NULL DEFAULT '',
		    generation INTEGER NOT NULL DEFAULT 1,
		    maintenance_until TEXT,
		    updated_at TEXT NOT NULL
		);
		CREATE TABLE state_observations (
		    service TEXT PRIMARY KEY,
		    object_id TEXT NOT NULL,
		    tenant_id TEXT NOT NULL DEFAULT 'default',
		    actual_state TEXT NOT NULL,
		    health_state TEXT NOT NULL,
		    reason TEXT NOT NULL DEFAULT '',
		    data_json TEXT NOT NULL DEFAULT '{}',
		    observed_at TEXT NOT NULL,
		    drift_detected INTEGER NOT NULL DEFAULT 0 CHECK (drift_detected IN (0, 1))
		);
		CREATE TABLE tenants (
		    id TEXT PRIMARY KEY,
		    display_name TEXT NOT NULL,
		    status TEXT NOT NULL,
		    created_at TEXT NOT NULL,
		    updated_at TEXT NOT NULL
		);
		CREATE TABLE roles (
		    id TEXT PRIMARY KEY,
		    display_name TEXT NOT NULL,
		    permissions_json TEXT NOT NULL,
		    built_in INTEGER NOT NULL DEFAULT 0 CHECK (built_in IN (0, 1))
		);
		CREATE TABLE role_bindings (
		    id TEXT PRIMARY KEY,
		    subject TEXT NOT NULL,
		    tenant_id TEXT NOT NULL,
		    role_id TEXT NOT NULL,
		    object_ids_json TEXT NOT NULL DEFAULT '[]',
		    expires_at TEXT,
		    created_at TEXT NOT NULL,
		    created_by TEXT NOT NULL
		);
		CREATE INDEX idx_role_bindings_subject ON role_bindings(subject, tenant_id);
		CREATE TABLE server_nodes (
		    id TEXT PRIMARY KEY,
		    hostname TEXT NOT NULL,
		    environment TEXT NOT NULL,
		    region TEXT NOT NULL DEFAULT '',
		    address TEXT NOT NULL DEFAULT '',
		    labels_json TEXT NOT NULL DEFAULT '{}',
		    capabilities_json TEXT NOT NULL DEFAULT '[]',
		    runner_id TEXT NOT NULL DEFAULT '',
		    status TEXT NOT NULL DEFAULT 'unknown',
		    max_concurrency INTEGER NOT NULL DEFAULT 1,
		    last_heartbeat_at TEXT,
		    disabled_reason TEXT NOT NULL DEFAULT '',
		    created_at TEXT NOT NULL,
		    updated_at TEXT NOT NULL
		);
		CREATE TABLE runner_nodes (
		    id TEXT PRIMARY KEY,
		    server_id TEXT NOT NULL,
		    hostname TEXT NOT NULL DEFAULT '',
		    tenant_id TEXT NOT NULL DEFAULT 'default',
		    labels_json TEXT NOT NULL DEFAULT '{}',
		    capabilities_json TEXT NOT NULL DEFAULT '[]',
		    version TEXT NOT NULL DEFAULT '',
		    status TEXT NOT NULL DEFAULT 'unknown',
		    max_concurrency INTEGER NOT NULL DEFAULT 1,
		    last_heartbeat_at TEXT,
		    lease_expires_at TEXT,
		    disabled_reason TEXT NOT NULL DEFAULT '',
		    created_at TEXT NOT NULL,
		    updated_at TEXT NOT NULL
		);
		CREATE TABLE batch_jobs (
		    id TEXT PRIMARY KEY,
		    idempotency_key TEXT NOT NULL UNIQUE,
		    actor_hash TEXT NOT NULL,
		    tenant_id TEXT NOT NULL DEFAULT 'default',
		    action TEXT NOT NULL,
		    target TEXT NOT NULL DEFAULT '',
		    strategy TEXT NOT NULL,
		    policy_json TEXT NOT NULL,
		    task_json TEXT NOT NULL,
		    digest TEXT NOT NULL,
		    confirmation_hash TEXT NOT NULL,
		    state TEXT NOT NULL,
		    failure_policy TEXT NOT NULL,
		    approved_by_hash TEXT NOT NULL DEFAULT '',
		    approved_at TEXT,
		    started_at TEXT,
		    finished_at TEXT,
		    summary TEXT NOT NULL DEFAULT '',
		    error TEXT NOT NULL DEFAULT '',
		    created_at TEXT NOT NULL,
		    updated_at TEXT NOT NULL
		);
		CREATE TABLE batch_items (
		    job_id TEXT NOT NULL,
		    item_id TEXT NOT NULL,
		    object_id TEXT NOT NULL,
		    service TEXT NOT NULL,
		    server_id TEXT NOT NULL DEFAULT '',
		    runner_id TEXT NOT NULL DEFAULT '',
		    batch_index INTEGER NOT NULL DEFAULT 0,
		    dependencies_json TEXT NOT NULL DEFAULT '[]',
		    state TEXT NOT NULL,
		    plan_id TEXT NOT NULL DEFAULT '',
		    task_id TEXT NOT NULL DEFAULT '',
		    error TEXT NOT NULL DEFAULT '',
		    updated_at TEXT NOT NULL,
		    PRIMARY KEY(job_id, item_id),
		    FOREIGN KEY(job_id) REFERENCES batch_jobs(id) ON DELETE CASCADE
		);
		CREATE TABLE control_plane_events (
		    sequence INTEGER PRIMARY KEY AUTOINCREMENT,
		    occurred_at TEXT NOT NULL,
		    event_type TEXT NOT NULL,
		    resource TEXT NOT NULL,
		    tenant_id TEXT NOT NULL DEFAULT 'default',
		    data_json TEXT NOT NULL DEFAULT '{}'
		);
		CREATE INDEX idx_control_plane_events_time ON control_plane_events(occurred_at DESC);`,
	`CREATE TABLE compose_revisions (
		    id TEXT PRIMARY KEY,
		    service TEXT NOT NULL,
		    digest TEXT NOT NULL,
		    source TEXT NOT NULL,
		    content TEXT NOT NULL,
		    validated INTEGER NOT NULL DEFAULT 0 CHECK (validated IN (0, 1)),
		    approved_by TEXT NOT NULL DEFAULT '',
		    created_at TEXT NOT NULL
		);
		CREATE INDEX idx_compose_revisions_service ON compose_revisions(service, created_at DESC);
		CREATE TABLE kubernetes_operations (
		    id TEXT PRIMARY KEY,
		    actor_hash TEXT NOT NULL,
		    cluster TEXT NOT NULL,
		    context_name TEXT NOT NULL,
		    namespace TEXT NOT NULL,
		    action TEXT NOT NULL,
		    manifest_digest TEXT NOT NULL,
		    dry_run INTEGER NOT NULL CHECK (dry_run IN (0, 1)),
		    state TEXT NOT NULL,
		    output TEXT NOT NULL DEFAULT '',
		    created_at TEXT NOT NULL,
		    finished_at TEXT
		);`,
	`CREATE TABLE terminal_sessions (
		    id TEXT PRIMARY KEY,
		    idempotency_key TEXT NOT NULL UNIQUE,
		    request_digest TEXT NOT NULL,
		    object_id TEXT NOT NULL,
		    command_name TEXT NOT NULL,
		    state TEXT NOT NULL,
		    actor_hash TEXT NOT NULL,
		    exit_code INTEGER NOT NULL DEFAULT 0,
		    output TEXT NOT NULL DEFAULT '',
		    expires_at TEXT NOT NULL,
		    created_at TEXT NOT NULL
		);
		CREATE INDEX idx_terminal_sessions_created_at ON terminal_sessions(created_at DESC);
		CREATE TABLE managed_file_proposals (
		    id TEXT PRIMARY KEY,
		    idempotency_key TEXT NOT NULL UNIQUE,
		    request_digest TEXT NOT NULL,
		    actor_hash TEXT NOT NULL,
		    root_id TEXT NOT NULL,
		    relative_path TEXT NOT NULL,
		    expected_digest TEXT NOT NULL,
		    proposed_digest TEXT NOT NULL,
		    content TEXT NOT NULL,
		    state TEXT NOT NULL,
		    created_at TEXT NOT NULL
		);
		CREATE INDEX idx_managed_file_proposals_path
		    ON managed_file_proposals(root_id, relative_path, created_at DESC);
		CREATE TABLE extension_packages (
		    package_id TEXT NOT NULL,
		    version TEXT NOT NULL,
		    idempotency_key TEXT NOT NULL UNIQUE,
		    request_digest TEXT NOT NULL,
		    actor_hash TEXT NOT NULL,
		    manifest_json TEXT NOT NULL,
		    storage_path TEXT NOT NULL,
		    storage_digest TEXT NOT NULL,
		    state TEXT NOT NULL,
		    created_at TEXT NOT NULL,
		    PRIMARY KEY(package_id, version)
		);
		CREATE TABLE runner_updates (
		    id TEXT PRIMARY KEY,
		    idempotency_key TEXT NOT NULL UNIQUE,
		    request_digest TEXT NOT NULL,
		    actor_hash TEXT NOT NULL,
		    runner_id TEXT NOT NULL,
		    target_version TEXT NOT NULL,
		    artifact_path TEXT NOT NULL,
		    artifact_digest TEXT NOT NULL,
		    state TEXT NOT NULL,
		    previous_version TEXT NOT NULL,
		    created_at TEXT NOT NULL,
		    finished_at TEXT
		);
		CREATE INDEX idx_runner_updates_runner_state
		    ON runner_updates(runner_id, state, created_at DESC);`,
	`ALTER TABLE batch_jobs ADD COLUMN run_idempotency_key TEXT NOT NULL DEFAULT '';
		CREATE UNIQUE INDEX idx_batch_jobs_run_idempotency
		    ON batch_jobs(run_idempotency_key) WHERE run_idempotency_key != '';
		CREATE UNIQUE INDEX idx_batch_items_task_id
		    ON batch_items(task_id) WHERE task_id != '';
		CREATE INDEX idx_batch_items_job_wave_state
		    ON batch_items(job_id,batch_index,state);
	CREATE INDEX idx_batch_items_plan_id
		    ON batch_items(plan_id) WHERE plan_id != '';`,
	`ALTER TABLE runner_nodes ADD COLUMN lease_generation INTEGER NOT NULL DEFAULT 0;
	 ALTER TABLE runner_nodes ADD COLUMN lease_token TEXT NOT NULL DEFAULT '';
	 ALTER TABLE runner_nodes ADD COLUMN certificate_fingerprint TEXT NOT NULL DEFAULT '';
	 ALTER TABLE runner_nodes ADD COLUMN heartbeat_public_key TEXT NOT NULL DEFAULT '';
	 CREATE INDEX idx_runner_nodes_lease ON runner_nodes(status, lease_expires_at);`,
	`CREATE TABLE batch_coordinator_leases (
		 job_id TEXT PRIMARY KEY,
		 owner_id TEXT NOT NULL,
		 generation INTEGER NOT NULL DEFAULT 0,
		 fencing_token TEXT NOT NULL,
		 lease_expires_at TEXT NOT NULL,
		 updated_at TEXT NOT NULL
	 );
	 CREATE UNIQUE INDEX idx_batch_coordinator_fencing ON batch_coordinator_leases(fencing_token);
	 CREATE INDEX idx_batch_coordinator_expiry ON batch_coordinator_leases(lease_expires_at);`,
	`ALTER TABLE kubernetes_operations ADD COLUMN idempotency_key TEXT NOT NULL DEFAULT '';
		ALTER TABLE kubernetes_operations ADD COLUMN request_digest TEXT NOT NULL DEFAULT '';
		ALTER TABLE kubernetes_operations ADD COLUMN error TEXT NOT NULL DEFAULT '';
		CREATE UNIQUE INDEX idx_kubernetes_operations_idempotency
		    ON kubernetes_operations(idempotency_key) WHERE idempotency_key != '';`,
	`ALTER TABLE kubernetes_operations ADD COLUMN allowlist_json TEXT NOT NULL DEFAULT '[]';
		ALTER TABLE kubernetes_operations ADD COLUMN resource_kinds_json TEXT NOT NULL DEFAULT '[]';`,
	`ALTER TABLE release_plans ADD COLUMN request_idempotency_key TEXT NOT NULL DEFAULT '';
		ALTER TABLE release_plans ADD COLUMN request_digest TEXT NOT NULL DEFAULT '';
		ALTER TABLE release_plans ADD COLUMN restore_mode TEXT NOT NULL DEFAULT '';
		ALTER TABLE release_plans ADD COLUMN recovery_point_id TEXT NOT NULL DEFAULT '';
		ALTER TABLE release_plans ADD COLUMN requires_dual_approval INTEGER NOT NULL DEFAULT 0 CHECK (requires_dual_approval IN (0, 1));
		ALTER TABLE release_plans ADD COLUMN second_approved_by_hash TEXT NOT NULL DEFAULT '';
		CREATE UNIQUE INDEX idx_release_plans_request_idempotency
			ON release_plans(request_idempotency_key) WHERE request_idempotency_key != '';`,
	`CREATE TABLE desired_state_requests (
			idempotency_key TEXT PRIMARY KEY,
			actor_hash TEXT NOT NULL,
			request_digest TEXT NOT NULL,
			service TEXT NOT NULL,
			state_json TEXT NOT NULL,
			generation INTEGER NOT NULL,
			event_sequence INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL
		);
		CREATE INDEX idx_desired_state_requests_created_at ON desired_state_requests(created_at DESC);`,
	`ALTER TABLE kubernetes_operations ADD COLUMN tenant_id TEXT NOT NULL DEFAULT 'default';
		CREATE INDEX idx_kubernetes_operations_tenant_created
			ON kubernetes_operations(tenant_id, created_at DESC);`,
	`CREATE TABLE runner_heartbeat_receipts (
			runner_id TEXT NOT NULL,
			nonce TEXT NOT NULL,
			occurred_at TEXT NOT NULL,
			payload_digest TEXT NOT NULL DEFAULT '',
			PRIMARY KEY(runner_id, nonce)
		);
		CREATE INDEX idx_runner_heartbeat_receipts_time ON runner_heartbeat_receipts(occurred_at DESC);`,
	`CREATE TABLE access_mutation_receipts (
			idempotency_key TEXT PRIMARY KEY,
			actor_hash TEXT NOT NULL,
			request_digest TEXT NOT NULL,
			created_at TEXT NOT NULL
		);
		CREATE INDEX idx_access_mutation_receipts_created_at ON access_mutation_receipts(created_at DESC);`,
	`CREATE TABLE kubernetes_plans (
			id TEXT PRIMARY KEY,
			idempotency_key TEXT NOT NULL UNIQUE,
			request_digest TEXT NOT NULL,
			actor_hash TEXT NOT NULL,
			tenant_id TEXT NOT NULL DEFAULT 'default',
			target_json TEXT NOT NULL,
			manifest_digest TEXT NOT NULL,
			manifest TEXT NOT NULL,
			action TEXT NOT NULL,
			state TEXT NOT NULL,
			confirmation_hash TEXT NOT NULL,
			confirmation_phrase TEXT NOT NULL,
			approved_by_hash TEXT NOT NULL DEFAULT '',
			approved_at TEXT,
			second_approved_by_hash TEXT NOT NULL DEFAULT '',
			second_approved_at TEXT,
			requires_dual_approval INTEGER NOT NULL DEFAULT 1 CHECK (requires_dual_approval IN (0, 1)),
			operation_id TEXT NOT NULL DEFAULT '',
			error TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			started_at TEXT,
			finished_at TEXT
		);
		CREATE INDEX idx_kubernetes_plans_tenant_created ON kubernetes_plans(tenant_id, created_at DESC);
		CREATE INDEX idx_kubernetes_plans_state ON kubernetes_plans(state, created_at DESC);`,
	`ALTER TABLE runner_updates ADD COLUMN rollback_path TEXT NOT NULL DEFAULT '';
		ALTER TABLE runner_updates ADD COLUMN error TEXT NOT NULL DEFAULT '';
		ALTER TABLE runner_updates ADD COLUMN activated_at TEXT;`,
	`ALTER TABLE kubernetes_plans ADD COLUMN execute_idempotency_key TEXT NOT NULL DEFAULT '';
		CREATE UNIQUE INDEX idx_kubernetes_plans_execute_idempotency
			ON kubernetes_plans(execute_idempotency_key) WHERE execute_idempotency_key != '';`,
	`ALTER TABLE runner_updates ADD COLUMN artifact_revision TEXT NOT NULL DEFAULT '';
		ALTER TABLE runner_updates ADD COLUMN publisher TEXT NOT NULL DEFAULT '';
		ALTER TABLE runner_updates ADD COLUMN artifact_signature TEXT NOT NULL DEFAULT '';
		ALTER TABLE runner_updates ADD COLUMN staged_path TEXT NOT NULL DEFAULT '';
		ALTER TABLE runner_updates ADD COLUMN phase TEXT NOT NULL DEFAULT '';
		ALTER TABLE runner_updates ADD COLUMN previous_revision TEXT NOT NULL DEFAULT '';
		ALTER TABLE runner_updates ADD COLUMN previous_digest TEXT NOT NULL DEFAULT '';
		ALTER TABLE runner_updates ADD COLUMN confirmation_hash TEXT NOT NULL DEFAULT '';
		ALTER TABLE runner_updates ADD COLUMN confirmation_phrase TEXT NOT NULL DEFAULT '';
		ALTER TABLE runner_updates ADD COLUMN approved_by_hash TEXT NOT NULL DEFAULT '';
		ALTER TABLE runner_updates ADD COLUMN activation_idempotency_key TEXT NOT NULL DEFAULT '';
		ALTER TABLE runner_updates ADD COLUMN resolved_by_hash TEXT NOT NULL DEFAULT '';
		ALTER TABLE runner_updates ADD COLUMN resolution_idempotency_key TEXT NOT NULL DEFAULT '';
		CREATE UNIQUE INDEX idx_runner_updates_activation_idempotency
			ON runner_updates(activation_idempotency_key) WHERE activation_idempotency_key != '';
		CREATE UNIQUE INDEX idx_runner_updates_resolution_idempotency
			ON runner_updates(resolution_idempotency_key) WHERE resolution_idempotency_key != '';`,
	`ALTER TABLE runner_updates ADD COLUMN binary_path TEXT NOT NULL DEFAULT '';
		ALTER TABLE runner_updates ADD COLUMN unit_name TEXT NOT NULL DEFAULT '';
		ALTER TABLE runner_updates ADD COLUMN health_timeout_seconds INTEGER NOT NULL DEFAULT 30;`,
	`ALTER TABLE runner_updates ADD COLUMN executor_heartbeat_at TEXT;`,
	`ALTER TABLE runner_updates ADD COLUMN cancelled_by_hash TEXT NOT NULL DEFAULT '';
		ALTER TABLE runner_updates ADD COLUMN cancellation_idempotency_key TEXT NOT NULL DEFAULT '';
		CREATE UNIQUE INDEX idx_runner_updates_cancellation_idempotency
			ON runner_updates(cancellation_idempotency_key) WHERE cancellation_idempotency_key != '';`,
	`CREATE TABLE auto_update_policies (
			service TEXT PRIMARY KEY,
			object_id TEXT NOT NULL,
			tenant_id TEXT NOT NULL,
			policy_json TEXT NOT NULL,
			last_evaluation_at TEXT,
			next_evaluation_at TEXT,
			last_plan_id TEXT NOT NULL DEFAULT '',
			last_error TEXT NOT NULL DEFAULT '',
			updated_at TEXT NOT NULL
		);
		CREATE TABLE auto_update_receipts (
			idempotency_key TEXT PRIMARY KEY,
			actor_hash TEXT NOT NULL,
			request_digest TEXT NOT NULL,
			created_at TEXT NOT NULL
		);
		CREATE INDEX idx_auto_update_policies_due ON auto_update_policies(next_evaluation_at);`,
	`CREATE TABLE terminal_shell_plans (
			id TEXT PRIMARY KEY,
			idempotency_key TEXT NOT NULL UNIQUE,
			request_digest TEXT NOT NULL,
			object_id TEXT NOT NULL,
			state TEXT NOT NULL,
			actor_hash TEXT NOT NULL,
			input_digest TEXT NOT NULL,
			confirmation_hash TEXT NOT NULL,
			confirmation_phrase TEXT NOT NULL,
			approved_by_hash TEXT NOT NULL DEFAULT '',
			execution_idempotency_key TEXT NOT NULL DEFAULT '',
			exit_code INTEGER NOT NULL DEFAULT 0,
			output TEXT NOT NULL DEFAULT '',
			error TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			expires_at TEXT NOT NULL,
			approved_at TEXT,
			started_at TEXT,
			finished_at TEXT
		);
		CREATE INDEX idx_terminal_shell_plans_created_at ON terminal_shell_plans(created_at DESC);
		CREATE UNIQUE INDEX idx_terminal_shell_execution_idempotency
			ON terminal_shell_plans(execution_idempotency_key) WHERE execution_idempotency_key != '';`,
	`ALTER TABLE managed_file_proposals ADD COLUMN confirmation_hash TEXT NOT NULL DEFAULT '';
		ALTER TABLE managed_file_proposals ADD COLUMN confirmation_phrase TEXT NOT NULL DEFAULT '';
		ALTER TABLE managed_file_proposals ADD COLUMN approved_by_hash TEXT NOT NULL DEFAULT '';
		ALTER TABLE managed_file_proposals ADD COLUMN second_approved_by_hash TEXT NOT NULL DEFAULT '';
		ALTER TABLE managed_file_proposals ADD COLUMN applied_by_hash TEXT NOT NULL DEFAULT '';
		ALTER TABLE managed_file_proposals ADD COLUMN apply_idempotency_key TEXT NOT NULL DEFAULT '';
		ALTER TABLE managed_file_proposals ADD COLUMN backup_path TEXT NOT NULL DEFAULT '';
		ALTER TABLE managed_file_proposals ADD COLUMN error TEXT NOT NULL DEFAULT '';
		ALTER TABLE managed_file_proposals ADD COLUMN approved_at TEXT;
		ALTER TABLE managed_file_proposals ADD COLUMN second_approved_at TEXT;
		ALTER TABLE managed_file_proposals ADD COLUMN applied_at TEXT;
		ALTER TABLE managed_file_proposals ADD COLUMN finished_at TEXT;
		ALTER TABLE managed_file_proposals ADD COLUMN rolled_back_by_hash TEXT NOT NULL DEFAULT '';
		ALTER TABLE managed_file_proposals ADD COLUMN rollback_idempotency_key TEXT NOT NULL DEFAULT '';
		ALTER TABLE managed_file_proposals ADD COLUMN rolled_back_at TEXT;
		CREATE UNIQUE INDEX idx_managed_file_apply_idempotency
			ON managed_file_proposals(apply_idempotency_key) WHERE apply_idempotency_key != '';
		CREATE UNIQUE INDEX idx_managed_file_rollback_idempotency
			ON managed_file_proposals(rollback_idempotency_key) WHERE rollback_idempotency_key != '';`,
	`ALTER TABLE compose_revisions ADD COLUMN proposal_idempotency_key TEXT NOT NULL DEFAULT '';
		ALTER TABLE compose_revisions ADD COLUMN request_digest TEXT NOT NULL DEFAULT '';
		ALTER TABLE compose_revisions ADD COLUMN expected_digest TEXT NOT NULL DEFAULT '';
		ALTER TABLE compose_revisions ADD COLUMN state TEXT NOT NULL DEFAULT 'proposed';
		ALTER TABLE compose_revisions ADD COLUMN actor_hash TEXT NOT NULL DEFAULT '';
		ALTER TABLE compose_revisions ADD COLUMN confirmation_hash TEXT NOT NULL DEFAULT '';
		ALTER TABLE compose_revisions ADD COLUMN confirmation_phrase TEXT NOT NULL DEFAULT '';
		ALTER TABLE compose_revisions ADD COLUMN second_approved_by_hash TEXT NOT NULL DEFAULT '';
		ALTER TABLE compose_revisions ADD COLUMN applied_by_hash TEXT NOT NULL DEFAULT '';
		ALTER TABLE compose_revisions ADD COLUMN apply_idempotency_key TEXT NOT NULL DEFAULT '';
		ALTER TABLE compose_revisions ADD COLUMN rollback_idempotency_key TEXT NOT NULL DEFAULT '';
		ALTER TABLE compose_revisions ADD COLUMN backup_controlled_path TEXT NOT NULL DEFAULT '';
		ALTER TABLE compose_revisions ADD COLUMN backup_runtime_path TEXT NOT NULL DEFAULT '';
		ALTER TABLE compose_revisions ADD COLUMN error TEXT NOT NULL DEFAULT '';
		ALTER TABLE compose_revisions ADD COLUMN approved_at TEXT;
		ALTER TABLE compose_revisions ADD COLUMN second_approved_at TEXT;
		ALTER TABLE compose_revisions ADD COLUMN applied_at TEXT;
		ALTER TABLE compose_revisions ADD COLUMN finished_at TEXT;
		CREATE UNIQUE INDEX idx_compose_revision_apply_idempotency
			ON compose_revisions(apply_idempotency_key) WHERE apply_idempotency_key != '';
		CREATE UNIQUE INDEX idx_compose_revision_proposal_idempotency
			ON compose_revisions(proposal_idempotency_key) WHERE proposal_idempotency_key != '';
		CREATE UNIQUE INDEX idx_compose_revision_rollback_idempotency
			ON compose_revisions(rollback_idempotency_key) WHERE rollback_idempotency_key != '';`,
	`ALTER TABLE tenants ADD COLUMN created_by TEXT NOT NULL DEFAULT 'bootstrap';
		ALTER TABLE roles ADD COLUMN created_at TEXT NOT NULL DEFAULT '';
		ALTER TABLE roles ADD COLUMN updated_at TEXT NOT NULL DEFAULT '';
		ALTER TABLE roles ADD COLUMN created_by TEXT NOT NULL DEFAULT 'bootstrap';
		ALTER TABLE role_bindings ADD COLUMN jit INTEGER NOT NULL DEFAULT 0 CHECK (jit IN (0, 1));
		ALTER TABLE role_bindings ADD COLUMN requires_dual_approval INTEGER NOT NULL DEFAULT 0 CHECK (requires_dual_approval IN (0, 1));
		ALTER TABLE role_bindings ADD COLUMN approval_state TEXT NOT NULL DEFAULT 'applied';
		ALTER TABLE role_bindings ADD COLUMN approved_by_hash TEXT NOT NULL DEFAULT '';
		ALTER TABLE role_bindings ADD COLUMN second_approved_by_hash TEXT NOT NULL DEFAULT '';
		ALTER TABLE role_bindings ADD COLUMN approved_at TEXT;
		ALTER TABLE role_bindings ADD COLUMN second_approved_at TEXT;
		CREATE TABLE access_policy_snapshots (
			version INTEGER PRIMARY KEY,
			digest TEXT NOT NULL,
			policy_json TEXT NOT NULL,
			actor_hash TEXT NOT NULL,
			created_at TEXT NOT NULL
		);
			CREATE INDEX idx_access_policy_snapshots_created_at ON access_policy_snapshots(created_at DESC);
		CREATE TABLE access_changes (
			id TEXT PRIMARY KEY,
			idempotency_key TEXT NOT NULL UNIQUE,
			request_digest TEXT NOT NULL,
			actor_hash TEXT NOT NULL,
			payload_json TEXT NOT NULL,
			state TEXT NOT NULL,
			requires_dual_approval INTEGER NOT NULL DEFAULT 1 CHECK (requires_dual_approval IN (0, 1)),
			confirmation_hash TEXT NOT NULL,
			confirmation_phrase TEXT NOT NULL,
			approved_by_hash TEXT NOT NULL DEFAULT '',
			second_approved_by_hash TEXT NOT NULL DEFAULT '',
			error TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			approved_at TEXT,
			second_approved_at TEXT,
			applied_at TEXT
		);
			CREATE INDEX idx_access_changes_state_created ON access_changes(state, created_at DESC);`,
	`ALTER TABLE runner_updates ADD COLUMN manifest_purpose TEXT NOT NULL DEFAULT '';
		 ALTER TABLE runner_updates ADD COLUMN manifest_schema INTEGER NOT NULL DEFAULT 0;
		 ALTER TABLE runner_updates ADD COLUMN manifest_goos TEXT NOT NULL DEFAULT '';
		 ALTER TABLE runner_updates ADD COLUMN manifest_goarch TEXT NOT NULL DEFAULT '';
		 ALTER TABLE runner_updates ADD COLUMN fencing_token TEXT NOT NULL DEFAULT '';
		 ALTER TABLE runner_updates ADD COLUMN lease_expires_at TEXT;
		 ALTER TABLE runner_updates ADD COLUMN resolution_decision TEXT NOT NULL DEFAULT '';
		 ALTER TABLE runner_updates ADD COLUMN resolution_evidence_json TEXT NOT NULL DEFAULT '{}';
		 CREATE UNIQUE INDEX idx_runner_updates_fencing_token
		     ON runner_updates(fencing_token) WHERE fencing_token != '';
		 CREATE INDEX idx_runner_updates_lease
		     ON runner_updates(state, lease_expires_at);`,
	`ALTER TABLE release_plans ADD COLUMN restore_tenant_id TEXT NOT NULL DEFAULT '';
			ALTER TABLE release_plans ADD COLUMN restore_server_id TEXT NOT NULL DEFAULT '';
			ALTER TABLE release_plans ADD COLUMN restore_expected_before_digest TEXT NOT NULL DEFAULT '';
			ALTER TABLE release_plans ADD COLUMN restore_contract_digest TEXT NOT NULL DEFAULT '';
			ALTER TABLE release_plans ADD COLUMN restore_revalidation_digest TEXT NOT NULL DEFAULT '';
			ALTER TABLE release_plans ADD COLUMN restore_revalidated_at TEXT;
			ALTER TABLE release_plans ADD COLUMN executed_by_hash TEXT NOT NULL DEFAULT '';
			ALTER TABLE release_plans ADD COLUMN restore_outcome TEXT NOT NULL DEFAULT '';
			ALTER TABLE release_plans ADD COLUMN restore_evidence_digest TEXT NOT NULL DEFAULT '';
			ALTER TABLE recovery_points ADD COLUMN tenant_id TEXT NOT NULL DEFAULT '';
			ALTER TABLE recovery_points ADD COLUMN server_id TEXT NOT NULL DEFAULT '';
			ALTER TABLE recovery_points ADD COLUMN expected_before_json TEXT NOT NULL DEFAULT '{}';
			ALTER TABLE recovery_points ADD COLUMN binding_digest TEXT NOT NULL DEFAULT '';
			ALTER TABLE recovery_points ADD COLUMN restore_outcome TEXT NOT NULL DEFAULT '';
			ALTER TABLE recovery_points ADD COLUMN restore_evidence_digest TEXT NOT NULL DEFAULT '';
			ALTER TABLE recovery_points ADD COLUMN outcome_at TEXT;
			ALTER TABLE tasks ADD COLUMN restore_mode TEXT NOT NULL DEFAULT '';
			ALTER TABLE tasks ADD COLUMN restore_tenant_id TEXT NOT NULL DEFAULT '';
			ALTER TABLE tasks ADD COLUMN restore_server_id TEXT NOT NULL DEFAULT '';
			ALTER TABLE tasks ADD COLUMN restore_expected_before_digest TEXT NOT NULL DEFAULT '';
			ALTER TABLE tasks ADD COLUMN restore_contract_digest TEXT NOT NULL DEFAULT '';
			ALTER TABLE tasks ADD COLUMN restore_revalidated_at TEXT;
			ALTER TABLE tasks ADD COLUMN restore_outcome TEXT NOT NULL DEFAULT '';
			ALTER TABLE tasks ADD COLUMN restore_evidence_digest TEXT NOT NULL DEFAULT '';
			CREATE INDEX idx_recovery_points_binding ON recovery_points(service, tenant_id, server_id, binding_digest);`,
	`ALTER TABLE batch_jobs ADD COLUMN canary_observation_started_at TEXT;
		ALTER TABLE batch_jobs ADD COLUMN canary_observed_at TEXT;`,
	`CREATE TABLE task_assignments (
			task_id TEXT PRIMARY KEY,
			server_id TEXT NOT NULL,
			runner_id TEXT NOT NULL,
			generation INTEGER NOT NULL DEFAULT 0 CHECK (generation >= 0),
			state TEXT NOT NULL CHECK (state IN ('assigned','claimed','completed','expired')),
			claim_token_hash TEXT NOT NULL DEFAULT '',
			contract_json TEXT NOT NULL,
			contract_digest TEXT NOT NULL,
			claimed_at TEXT,
			last_heartbeat_at TEXT,
			lease_expires_at TEXT,
			execution_deadline_at TEXT NOT NULL,
			completion_digest TEXT NOT NULL DEFAULT '',
			completion_idempotency_key TEXT NOT NULL DEFAULT '',
			completion_event_sequence INTEGER NOT NULL DEFAULT 0,
			last_runner_sequence INTEGER NOT NULL DEFAULT 0 CHECK (last_runner_sequence >= 0),
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			FOREIGN KEY(task_id) REFERENCES tasks(id) ON DELETE CASCADE
		);
		CREATE INDEX idx_task_assignments_runner_state
			ON task_assignments(runner_id,state,created_at);
		CREATE INDEX idx_task_assignments_expiry
			ON task_assignments(state,lease_expires_at,execution_deadline_at);
		CREATE UNIQUE INDEX idx_task_assignments_completion_key
			ON task_assignments(completion_idempotency_key)
			WHERE completion_idempotency_key != '';
		CREATE TABLE task_assignment_events (
			task_id TEXT NOT NULL,
			generation INTEGER NOT NULL,
			runner_sequence INTEGER NOT NULL CHECK (runner_sequence > 0),
			payload_digest TEXT NOT NULL,
			event_sequence INTEGER NOT NULL,
			created_at TEXT NOT NULL,
			PRIMARY KEY(task_id,generation,runner_sequence),
			FOREIGN KEY(task_id) REFERENCES task_assignments(task_id) ON DELETE CASCADE
		);
		CREATE UNIQUE INDEX idx_task_assignment_event_sequence
			ON task_assignment_events(event_sequence);`,
	`ALTER TABLE batch_jobs ADD COLUMN executed_by_hash TEXT NOT NULL DEFAULT '';
	 ALTER TABLE batch_jobs ADD COLUMN executed_at TEXT;`,
	`ALTER TABLE release_plans ADD COLUMN tenant_id TEXT NOT NULL DEFAULT 'default';
	 ALTER TABLE release_plans ADD COLUMN server_id TEXT NOT NULL DEFAULT '';
	 ALTER TABLE release_plans ADD COLUMN schedule_at TEXT;
	 CREATE INDEX idx_release_plans_schedule ON release_plans(state, schedule_at);`,
	`ALTER TABLE terminal_shell_plans ADD COLUMN second_approved_by_hash TEXT NOT NULL DEFAULT '';
	 ALTER TABLE terminal_shell_plans ADD COLUMN second_approved_at TEXT;
	 UPDATE terminal_shell_plans
	 SET state = 'pending_second_approval'
	 WHERE state = 'approved' AND approved_by_hash != '' AND execution_idempotency_key = '';`,
	`CREATE TABLE extension_plans (
		id TEXT PRIMARY KEY,
		idempotency_key TEXT NOT NULL UNIQUE,
		request_digest TEXT NOT NULL,
		plan_digest TEXT NOT NULL,
		actor_hash TEXT NOT NULL,
		tenant_id TEXT NOT NULL,
		object_id TEXT NOT NULL,
		extension_id TEXT NOT NULL,
		extension_version TEXT NOT NULL,
		extension_digest TEXT NOT NULL,
		publisher TEXT NOT NULL,
		manifest_digest TEXT NOT NULL,
		policy_digest TEXT NOT NULL,
		sandbox TEXT NOT NULL,
		input_json TEXT NOT NULL,
		input_digest TEXT NOT NULL,
		timeout_seconds INTEGER NOT NULL,
		max_package_bytes INTEGER NOT NULL,
		max_input_bytes INTEGER NOT NULL,
		max_output_bytes INTEGER NOT NULL,
		max_memory_pages INTEGER NOT NULL,
		state TEXT NOT NULL CHECK (state IN ('pending_approval','pending_second_approval','approved','running','succeeded','failed','needs_attention','expired')),
		confirmation_hash TEXT NOT NULL,
		confirmation_phrase TEXT NOT NULL,
		approved_by_hash TEXT NOT NULL DEFAULT '',
		second_approved_by_hash TEXT NOT NULL DEFAULT '',
		executed_by_hash TEXT NOT NULL DEFAULT '',
		execution_idempotency_key TEXT NOT NULL DEFAULT '',
		output TEXT NOT NULL DEFAULT '',
		exit_code INTEGER NOT NULL DEFAULT 0,
		error TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		expires_at TEXT NOT NULL,
		approved_at TEXT,
		second_approved_at TEXT,
		started_at TEXT,
		finished_at TEXT
	);
	CREATE INDEX idx_extension_plans_tenant_created ON extension_plans(tenant_id,created_at DESC);
	CREATE INDEX idx_extension_plans_state_expiry ON extension_plans(state,expires_at);
		CREATE UNIQUE INDEX idx_extension_plans_execution_key
			ON extension_plans(execution_idempotency_key) WHERE execution_idempotency_key != '';`,
	`ALTER TABLE runner_nodes ADD COLUMN revision TEXT NOT NULL DEFAULT '';
	 ALTER TABLE runner_nodes ADD COLUMN binary_digest TEXT NOT NULL DEFAULT '';
	 ALTER TABLE runner_nodes ADD COLUMN identity_payload_version INTEGER NOT NULL DEFAULT 0;

	 CREATE TABLE runner_fleet_update_plans (
		id TEXT PRIMARY KEY,
		idempotency_key TEXT NOT NULL UNIQUE,
		execution_idempotency_key TEXT NOT NULL DEFAULT '',
		cancellation_idempotency_key TEXT NOT NULL DEFAULT '',
		request_digest TEXT NOT NULL,
		plan_digest TEXT NOT NULL,
		policy_digest TEXT NOT NULL,
		actor_hash TEXT NOT NULL,
		tenant_id TEXT NOT NULL,
		manifest_json TEXT NOT NULL,
		artifact_path TEXT NOT NULL,
		artifact_signature TEXT NOT NULL,
		staged_path TEXT NOT NULL,
		target_runner_ids_json TEXT NOT NULL,
		batch_policy_json TEXT NOT NULL,
		max_concurrent INTEGER NOT NULL CHECK (max_concurrent > 0),
		change_window_json TEXT NOT NULL,
		rollback_on_failure INTEGER NOT NULL CHECK (rollback_on_failure = 1),
		state TEXT NOT NULL CHECK (state IN ('pending_approval','pending_second_approval','approved','running','observing','rolling_back','succeeded','rolled_back','needs_attention','cancelled','expired')),
		current_batch INTEGER NOT NULL DEFAULT -1,
		confirmation_hash TEXT NOT NULL,
		confirmation_phrase TEXT NOT NULL,
		approved_by_hash TEXT NOT NULL DEFAULT '',
		second_approved_by_hash TEXT NOT NULL DEFAULT '',
		executed_by_hash TEXT NOT NULL DEFAULT '',
		cancelled_by_hash TEXT NOT NULL DEFAULT '',
		summary TEXT NOT NULL DEFAULT '',
		error TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		expires_at TEXT NOT NULL,
		approved_at TEXT,
		second_approved_at TEXT,
		started_at TEXT,
		observation_started_at TEXT,
		observation_ends_at TEXT,
		finished_at TEXT,
		updated_at TEXT NOT NULL
	 );
	 CREATE INDEX idx_runner_fleet_update_plans_state_created
		ON runner_fleet_update_plans(state,created_at DESC);
	 CREATE INDEX idx_runner_fleet_update_plans_tenant_created
		ON runner_fleet_update_plans(tenant_id,created_at DESC);
	 CREATE UNIQUE INDEX idx_runner_fleet_update_plans_execution_key
		ON runner_fleet_update_plans(execution_idempotency_key) WHERE execution_idempotency_key != '';
	 CREATE UNIQUE INDEX idx_runner_fleet_update_plans_cancellation_key
		ON runner_fleet_update_plans(cancellation_idempotency_key) WHERE cancellation_idempotency_key != '';

	 CREATE TABLE runner_fleet_update_items (
		id TEXT PRIMARY KEY,
		plan_id TEXT NOT NULL,
		runner_id TEXT NOT NULL,
		server_id TEXT NOT NULL,
		batch_index INTEGER NOT NULL CHECK (batch_index >= 0),
		state TEXT NOT NULL CHECK (state IN ('pending','ready','running','succeeded','failed','rollback_ready','rolling_back','rolled_back','needs_attention','skipped')),
		previous_version TEXT NOT NULL,
		previous_revision TEXT NOT NULL,
		previous_digest TEXT NOT NULL,
		expected_lease_generation INTEGER NOT NULL CHECK (expected_lease_generation > 0),
		certificate_fingerprint TEXT NOT NULL DEFAULT '',
		assignment_action TEXT NOT NULL DEFAULT '',
		assignment_generation INTEGER NOT NULL DEFAULT 0 CHECK (assignment_generation >= 0),
		assignment_token_hash TEXT NOT NULL DEFAULT '',
		assignment_idempotency_key TEXT NOT NULL DEFAULT '',
		completion_idempotency_key TEXT NOT NULL DEFAULT '',
		observed_version TEXT NOT NULL DEFAULT '',
		observed_revision TEXT NOT NULL DEFAULT '',
		observed_digest TEXT NOT NULL DEFAULT '',
		error TEXT NOT NULL DEFAULT '',
		rollback_error TEXT NOT NULL DEFAULT '',
		claimed_at TEXT,
		last_heartbeat_at TEXT,
		lease_expires_at TEXT,
		execution_deadline_at TEXT,
		started_at TEXT,
		finished_at TEXT,
		updated_at TEXT NOT NULL,
		UNIQUE(plan_id,runner_id),
		FOREIGN KEY(plan_id) REFERENCES runner_fleet_update_plans(id) ON DELETE CASCADE
	 );
	 CREATE INDEX idx_runner_fleet_update_items_claim
		ON runner_fleet_update_items(runner_id,state,batch_index,updated_at);
	 CREATE INDEX idx_runner_fleet_update_items_plan_state
		ON runner_fleet_update_items(plan_id,state,batch_index);
	 CREATE UNIQUE INDEX idx_runner_fleet_update_items_assignment_key
		ON runner_fleet_update_items(assignment_idempotency_key) WHERE assignment_idempotency_key != '';
	 CREATE UNIQUE INDEX idx_runner_fleet_update_items_completion_key
		ON runner_fleet_update_items(completion_idempotency_key) WHERE completion_idempotency_key != '';

	 CREATE TABLE runner_fleet_update_receipts (
		item_id TEXT NOT NULL,
		assignment_generation INTEGER NOT NULL,
		plan_id TEXT NOT NULL,
		claim_token TEXT NOT NULL,
		control_plane_endpoint TEXT NOT NULL,
		assignment_json TEXT NOT NULL,
		local_update_id TEXT NOT NULL DEFAULT '',
		action TEXT NOT NULL CHECK (action IN ('update','rollback')),
		state TEXT NOT NULL CHECK (state IN ('prepared','launching','launched','reported','needs_attention')),
		last_error TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		PRIMARY KEY(item_id,assignment_generation)
	 );`,
	`ALTER TABLE kubernetes_plans ADD COLUMN rollback_of_plan_id TEXT NOT NULL DEFAULT '';
	 ALTER TABLE kubernetes_plans ADD COLUMN source_manifest_digest TEXT NOT NULL DEFAULT '';
	 ALTER TABLE kubernetes_operations ADD COLUMN phase TEXT NOT NULL DEFAULT '';
	 ALTER TABLE kubernetes_operations ADD COLUMN rollout_state TEXT NOT NULL DEFAULT '';
	 ALTER TABLE kubernetes_operations ADD COLUMN rollout_resources_json TEXT NOT NULL DEFAULT '[]';
	 ALTER TABLE kubernetes_operations ADD COLUMN rollback_of_plan_id TEXT NOT NULL DEFAULT '';
	 CREATE INDEX idx_kubernetes_operations_rollout_state ON kubernetes_operations(rollout_state, created_at DESC);`,
	`ALTER TABLE access_changes ADD COLUMN applied_by_hash TEXT NOT NULL DEFAULT '';
	 ALTER TABLE access_changes ADD COLUMN applied_policy_digest TEXT NOT NULL DEFAULT '';
	 ALTER TABLE access_changes ADD COLUMN applied_policy_version INTEGER NOT NULL DEFAULT 0;
	 CREATE INDEX idx_access_changes_applied_by ON access_changes(applied_by_hash, state);`,
	`ALTER TABLE batch_jobs ADD COLUMN requires_dual_approval INTEGER NOT NULL DEFAULT 0;
	 ALTER TABLE batch_jobs ADD COLUMN second_approved_by_hash TEXT NOT NULL DEFAULT '';
	 ALTER TABLE batch_jobs ADD COLUMN second_approved_at TEXT;
	 ALTER TABLE batch_jobs ADD COLUMN confirmation_phrase TEXT NOT NULL DEFAULT '';`,
	`ALTER TABLE compose_revisions ADD COLUMN tenant_id TEXT NOT NULL DEFAULT '';
	 ALTER TABLE compose_revisions ADD COLUMN server_id TEXT NOT NULL DEFAULT '';
	 ALTER TABLE compose_revisions ADD COLUMN project_name TEXT NOT NULL DEFAULT '';
	 ALTER TABLE compose_revisions ADD COLUMN baseline_semantic_digest TEXT NOT NULL DEFAULT '';
	 ALTER TABLE compose_revisions ADD COLUMN candidate_semantic_digest TEXT NOT NULL DEFAULT '';
	 ALTER TABLE compose_revisions ADD COLUMN baseline_effective_digest TEXT NOT NULL DEFAULT '';
	 ALTER TABLE compose_revisions ADD COLUMN candidate_effective_digest TEXT NOT NULL DEFAULT '';
	 ALTER TABLE compose_revisions ADD COLUMN env_file_digest TEXT NOT NULL DEFAULT '';
	 ALTER TABLE compose_revisions ADD COLUMN semantic_diff_json TEXT NOT NULL DEFAULT '[]';
	 ALTER TABLE compose_revisions ADD COLUMN policy_digest TEXT NOT NULL DEFAULT '';
	 ALTER TABLE compose_revisions ADD COLUMN recovery_point_id TEXT NOT NULL DEFAULT '';
	 ALTER TABLE compose_revisions ADD COLUMN recovery_point_expected_digest TEXT NOT NULL DEFAULT '';
	 ALTER TABLE compose_revisions ADD COLUMN recovery_point_binding_digest TEXT NOT NULL DEFAULT '';
	 ALTER TABLE compose_revisions ADD COLUMN recovery_point_evidence_digest TEXT NOT NULL DEFAULT '';
	 ALTER TABLE compose_revisions ADD COLUMN recovery_point_verified_at TEXT;
	 ALTER TABLE compose_revisions ADD COLUMN recovery_point_recoverable_until TEXT;
	 ALTER TABLE compose_revisions ADD COLUMN alert_evidence_digest TEXT NOT NULL DEFAULT '';
	 ALTER TABLE compose_revisions ADD COLUMN blocking_alert_fingerprints_json TEXT NOT NULL DEFAULT '[]';
	 ALTER TABLE compose_revisions ADD COLUMN alert_checked_at TEXT;
	 ALTER TABLE compose_revisions ADD COLUMN expires_at TEXT NOT NULL DEFAULT '';
	 ALTER TABLE compose_revisions ADD COLUMN expected_runtime_identity_digest TEXT NOT NULL DEFAULT '';
	 ALTER TABLE compose_revisions ADD COLUMN expected_runtime_image TEXT NOT NULL DEFAULT '';
	 ALTER TABLE compose_revisions ADD COLUMN expected_runtime_image_id TEXT NOT NULL DEFAULT '';
	 ALTER TABLE compose_revisions ADD COLUMN candidate_image TEXT NOT NULL DEFAULT '';
	 ALTER TABLE compose_revisions ADD COLUMN candidate_image_digest TEXT NOT NULL DEFAULT '';
	 ALTER TABLE compose_revisions ADD COLUMN candidate_image_id TEXT NOT NULL DEFAULT '';
	 ALTER TABLE compose_revisions ADD COLUMN applied_runtime_identity_digest TEXT NOT NULL DEFAULT '';
	 ALTER TABLE compose_revisions ADD COLUMN rolled_back_by_hash TEXT NOT NULL DEFAULT '';
	 ALTER TABLE compose_revisions ADD COLUMN rollback_started_at TEXT;
	 ALTER TABLE compose_revisions ADD COLUMN rollback_finished_at TEXT;
	 CREATE INDEX idx_compose_revision_expiry ON compose_revisions(state,expires_at);
		 CREATE INDEX idx_compose_revision_binding ON compose_revisions(service,tenant_id,server_id,recovery_point_id);`,
	`ALTER TABLE batch_jobs ADD COLUMN approval_policy_version INTEGER NOT NULL DEFAULT 0;
	 UPDATE batch_jobs
	 SET state='needs_attention',
	     summary='遗留批量计划缺少可证明的审批策略',
	     error='请按当前审批策略重新创建批量计划',
	     updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')
	 WHERE approval_policy_version=0
	   AND state IN ('pending_approval','approved');
	 CREATE INDEX idx_batch_jobs_approval_policy
	 ON batch_jobs(approval_policy_version,state);`,
	`ALTER TABLE kubernetes_plans ADD COLUMN rollback_target_plan_id TEXT NOT NULL DEFAULT '';
	 ALTER TABLE kubernetes_plans ADD COLUMN executed_by_hash TEXT NOT NULL DEFAULT '';
	 CREATE INDEX idx_kubernetes_plans_rollback_target
	 ON kubernetes_plans(rollback_target_plan_id) WHERE rollback_target_plan_id != '';`,
}
