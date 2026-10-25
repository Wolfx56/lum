-- Deliverable 1 tables (spec §12). Timestamps are UTC RFC 3339 strings.

-- History of every version check (90-day retention).
CREATE TABLE version_checks (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    app_id          TEXT    NOT NULL,
    checked_at      TEXT    NOT NULL,
    current_version TEXT    NOT NULL DEFAULT '',
    latest_version  TEXT    NOT NULL DEFAULT '',
    status          TEXT    NOT NULL,
    error           TEXT    NOT NULL DEFAULT '',
    release_url     TEXT    NOT NULL DEFAULT '',
    release_notes   TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX version_checks_app_checked ON version_checks (app_id, checked_at DESC);
CREATE INDEX version_checks_checked ON version_checks (checked_at);

-- Current state per app, denormalized for the dashboard.
CREATE TABLE app_state (
    app_id                TEXT PRIMARY KEY,
    status                TEXT NOT NULL,
    current_version       TEXT NOT NULL DEFAULT '',
    latest_version        TEXT NOT NULL DEFAULT '',
    last_check_at         TEXT NOT NULL,
    last_notified_version TEXT NOT NULL DEFAULT '',
    release_url           TEXT NOT NULL DEFAULT '',
    error                 TEXT NOT NULL DEFAULT ''
);

-- Audit trail of every write action (filled from M4).
CREATE TABLE audit_log (
    id      INTEGER PRIMARY KEY AUTOINCREMENT,
    ts      TEXT NOT NULL,
    user    TEXT NOT NULL,
    action  TEXT NOT NULL,
    target  TEXT NOT NULL DEFAULT '',
    details TEXT NOT NULL DEFAULT ''
);
CREATE INDEX audit_log_ts ON audit_log (ts DESC);
