-- UI sessions (BFF authentication, see doc/authentication.md).
-- Only the SHA-256 of the session id is stored: reading the database does not
-- give usable cookies.
CREATE TABLE sessions (
    id_hash    TEXT PRIMARY KEY,
    subject    TEXT NOT NULL,
    username   TEXT NOT NULL,
    name       TEXT NOT NULL DEFAULT '',
    email      TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL
);
CREATE INDEX sessions_expires ON sessions (expires_at);
