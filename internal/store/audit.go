package store

import (
	"context"
	"time"
)

// AuditEntry is one row of audit_log (write actions, with the user identity).
type AuditEntry struct {
	ID      int64     `json:"id"`
	TS      time.Time `json:"ts"`
	User    string    `json:"user"`
	Action  string    `json:"action"`
	Target  string    `json:"target,omitempty"`
	Details string    `json:"details,omitempty"`
}

// Audit appends an entry to the audit log.
func (s *Store) Audit(ctx context.Context, e AuditEntry) error {
	if e.TS.IsZero() {
		e.TS = time.Now()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO audit_log (ts, user, action, target, details) VALUES (?, ?, ?, ?, ?)`,
		formatTime(e.TS), e.User, e.Action, e.Target, e.Details)
	return err
}

// AuditEntries returns the latest audit entries, most recent first.
func (s *Store) AuditEntries(ctx context.Context, limit int) ([]AuditEntry, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, ts, user, action, target, details
		FROM audit_log ORDER BY ts DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []AuditEntry
	for rows.Next() {
		var e AuditEntry
		var ts string
		if err := rows.Scan(&e.ID, &ts, &e.User, &e.Action, &e.Target, &e.Details); err != nil {
			return nil, err
		}
		if e.TS, err = parseTime(ts); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}
