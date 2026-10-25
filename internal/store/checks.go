package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Check is one row of version_checks: the result of checking one app.
type Check struct {
	ID             int64     `json:"id"`
	AppID          string    `json:"app_id"`
	CheckedAt      time.Time `json:"checked_at"`
	CurrentVersion string    `json:"current_version"`
	LatestVersion  string    `json:"latest_version"`
	Status         string    `json:"status"`
	Error          string    `json:"error,omitempty"`
	ReleaseURL     string    `json:"release_url,omitempty"`
	ReleaseNotes   string    `json:"release_notes,omitempty"`
}

// AppState is one row of app_state: the latest known state of an app.
type AppState struct {
	AppID               string    `json:"app_id"`
	Status              string    `json:"status"`
	CurrentVersion      string    `json:"current_version"`
	LatestVersion       string    `json:"latest_version"`
	LastCheckAt         time.Time `json:"last_check_at"`
	LastNotifiedVersion string    `json:"last_notified_version,omitempty"`
	ReleaseURL          string    `json:"release_url,omitempty"`
	Error               string    `json:"error,omitempty"`
}

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("not found")

// RecordCheck stores a check and updates the app state in one transaction.
// It returns the state as it was before (zero AppState if the app is new),
// so the caller can detect transitions. last_notified_version is preserved.
func (s *Store) RecordCheck(ctx context.Context, c Check) (previous AppState, err error) {
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		previous, err = scanState(tx.QueryRowContext(ctx, selectState+` WHERE app_id = ?`, c.AppID))
		if errors.Is(err, ErrNotFound) {
			err = nil
		}
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO version_checks
			(app_id, checked_at, current_version, latest_version, status, error, release_url, release_notes)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			c.AppID, formatTime(c.CheckedAt), c.CurrentVersion, c.LatestVersion, c.Status, c.Error, c.ReleaseURL, c.ReleaseNotes,
		); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO app_state
			(app_id, status, current_version, latest_version, last_check_at, release_url, error)
			VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (app_id) DO UPDATE SET
				status = excluded.status,
				current_version = excluded.current_version,
				latest_version = excluded.latest_version,
				last_check_at = excluded.last_check_at,
				release_url = excluded.release_url,
				error = excluded.error`,
			c.AppID, c.Status, c.CurrentVersion, c.LatestVersion, formatTime(c.CheckedAt), c.ReleaseURL, c.Error,
		)
		return err
	})
	return previous, err
}

// MarkNotified records that update_available was notified for version.
func (s *Store) MarkNotified(ctx context.Context, appID, version string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE app_state SET last_notified_version = ? WHERE app_id = ?`, version, appID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

const selectState = `SELECT app_id, status, current_version, latest_version, last_check_at,
	last_notified_version, release_url, error FROM app_state`

// AppState returns the state of one app, or ErrNotFound if it was never checked.
func (s *Store) AppState(ctx context.Context, appID string) (AppState, error) {
	return scanState(s.db.QueryRowContext(ctx, selectState+` WHERE app_id = ?`, appID))
}

// AppStates returns the state of every checked app, by app_id.
func (s *Store) AppStates(ctx context.Context) (map[string]AppState, error) {
	rows, err := s.db.QueryContext(ctx, selectState)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	states := map[string]AppState{}
	for rows.Next() {
		st, err := scanState(rows)
		if err != nil {
			return nil, err
		}
		states[st.AppID] = st
	}
	return states, rows.Err()
}

// Checks returns the latest checks of an app, most recent first.
func (s *Store) Checks(ctx context.Context, appID string, limit int) ([]Check, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, app_id, checked_at, current_version, latest_version,
		status, error, release_url, release_notes
		FROM version_checks WHERE app_id = ? ORDER BY checked_at DESC, id DESC LIMIT ?`, appID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var checks []Check
	for rows.Next() {
		var c Check
		var at string
		if err := rows.Scan(&c.ID, &c.AppID, &at, &c.CurrentVersion, &c.LatestVersion,
			&c.Status, &c.Error, &c.ReleaseURL, &c.ReleaseNotes); err != nil {
			return nil, err
		}
		if c.CheckedAt, err = parseTime(at); err != nil {
			return nil, err
		}
		checks = append(checks, c)
	}
	return checks, rows.Err()
}

// PurgeChecks deletes the checks older than before and returns how many were removed.
func (s *Store) PurgeChecks(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM version_checks WHERE checked_at < ?`, formatTime(before))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

type scanner interface{ Scan(dest ...any) error }

func scanState(row scanner) (AppState, error) {
	var st AppState
	var at string
	err := row.Scan(&st.AppID, &st.Status, &st.CurrentVersion, &st.LatestVersion, &at,
		&st.LastNotifiedVersion, &st.ReleaseURL, &st.Error)
	if errors.Is(err, sql.ErrNoRows) {
		return AppState{}, ErrNotFound
	}
	if err != nil {
		return AppState{}, err
	}
	st.LastCheckAt, err = parseTime(at)
	return st, err
}
