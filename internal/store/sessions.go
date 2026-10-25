package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Session is an authenticated UI session.
type Session struct {
	IDHash    string // SHA-256 (hex) of the cookie value
	Subject   string // OIDC sub
	Username  string
	Name      string
	Email     string
	CreatedAt time.Time
	ExpiresAt time.Time
}

// CreateSession stores a new session.
func (s *Store) CreateSession(ctx context.Context, sess Session) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO sessions (id_hash, subject, username, name, email, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		sess.IDHash, sess.Subject, sess.Username, sess.Name, sess.Email, formatTime(sess.CreatedAt), formatTime(sess.ExpiresAt))
	return err
}

// ValidSession returns the session if it exists and has not expired at now.
func (s *Store) ValidSession(ctx context.Context, idHash string, now time.Time) (Session, error) {
	var sess Session
	var created, expires string
	err := s.db.QueryRowContext(ctx, `SELECT id_hash, subject, username, name, email, created_at, expires_at
		FROM sessions WHERE id_hash = ? AND expires_at > ?`, idHash, formatTime(now)).
		Scan(&sess.IDHash, &sess.Subject, &sess.Username, &sess.Name, &sess.Email, &created, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	if err != nil {
		return Session{}, err
	}
	if sess.CreatedAt, err = parseTime(created); err != nil {
		return Session{}, err
	}
	sess.ExpiresAt, err = parseTime(expires)
	return sess, err
}

// DeleteSession removes a session (logout).
func (s *Store) DeleteSession(ctx context.Context, idHash string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id_hash = ?`, idHash)
	return err
}

// PurgeSessions deletes the sessions expired at now.
func (s *Store) PurgeSessions(ctx context.Context, now time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, formatTime(now))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
