-- Agents (spec §12, M5). Only the SHA-256 of the agent token is stored.
CREATE TABLE agents (
    id            TEXT PRIMARY KEY,
    token_hash    TEXT    NOT NULL,
    agent_version TEXT    NOT NULL DEFAULT '',
    hostname      TEXT    NOT NULL DEFAULT '',
    last_seen_at  TEXT    NOT NULL DEFAULT '',
    status        TEXT    NOT NULL DEFAULT 'offline', -- online | offline
    enrolled_at   TEXT    NOT NULL,
    revoked       INTEGER NOT NULL DEFAULT 0
);

-- Scripts reported by each agent (name and checksum of its local copy).
CREATE TABLE agent_scripts (
    agent_id    TEXT NOT NULL REFERENCES agents (id) ON DELETE CASCADE,
    script_name TEXT NOT NULL,
    checksum    TEXT NOT NULL,
    synced_at   TEXT NOT NULL,
    PRIMARY KEY (agent_id, script_name)
);
