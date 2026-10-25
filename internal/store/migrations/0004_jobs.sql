-- Update jobs (spec §7, §12, M6).
CREATE TABLE jobs (
    id              TEXT PRIMARY KEY,           -- UUID
    app_id          TEXT NOT NULL,
    type            TEXT NOT NULL,              -- api | script
    status          TEXT NOT NULL,              -- pending | snapshotting | running | healthchecking | success | failed | rolled_back | cancelled
    current_version TEXT NOT NULL DEFAULT '',   -- version before the update
    target_version  TEXT NOT NULL DEFAULT '',
    snapshot_name   TEXT NOT NULL DEFAULT '',
    created_by      TEXT NOT NULL,
    created_at      TEXT NOT NULL,
    started_at      TEXT NOT NULL DEFAULT '',
    finished_at     TEXT NOT NULL DEFAULT '',
    error           TEXT NOT NULL DEFAULT ''
);
CREATE INDEX jobs_app_created ON jobs (app_id, created_at DESC);
-- One active job per app (spec §7: concurrent requests get 409).
CREATE UNIQUE INDEX jobs_one_active ON jobs (app_id)
    WHERE status IN ('pending', 'snapshotting', 'running', 'healthchecking');

-- Job logs, line by line (90-day retention).
CREATE TABLE job_logs (
    job_id TEXT    NOT NULL REFERENCES jobs (id) ON DELETE CASCADE,
    seq    INTEGER NOT NULL,
    ts     TEXT    NOT NULL,
    stream TEXT    NOT NULL,                    -- stdout | stderr | system
    line   TEXT    NOT NULL,
    PRIMARY KEY (job_id, seq)
);
