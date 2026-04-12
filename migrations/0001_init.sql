CREATE TABLE IF NOT EXISTS machine_credentials (
    machine_id TEXT PRIMARY KEY,
    machine_token TEXT NOT NULL,
    issued_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS provider_sessions (
    session_id TEXT PRIMARY KEY,
    runtime TEXT NOT NULL,
    provider_session_ref TEXT NOT NULL,
    workspace_root TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS task_runs (
    task_run_id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL,
    runtime TEXT NOT NULL,
    status TEXT NOT NULL,
    waiting_user_reason TEXT,
    started_at TEXT,
    finished_at TEXT
);

CREATE TABLE IF NOT EXISTS command_runs (
    command_run_id TEXT PRIMARY KEY,
    task_run_id TEXT NOT NULL,
    executable TEXT NOT NULL,
    args_json TEXT NOT NULL,
    cwd TEXT NOT NULL,
    reason TEXT NOT NULL,
    command_fingerprint TEXT NOT NULL,
    risk_level TEXT NOT NULL,
    approval_action_id TEXT,
    exit_code INTEGER,
    started_at TEXT,
    finished_at TEXT,
    FOREIGN KEY(task_run_id) REFERENCES task_runs(task_run_id)
);

CREATE TABLE IF NOT EXISTS workspace_snapshots (
    snapshot_id TEXT PRIMARY KEY,
    task_run_id TEXT NOT NULL,
    git_head TEXT,
    created_at TEXT NOT NULL,
    FOREIGN KEY(task_run_id) REFERENCES task_runs(task_run_id)
);
