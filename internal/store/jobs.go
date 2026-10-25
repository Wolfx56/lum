package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Job states (spec §7.2).
const (
	JobPending        = "pending"
	JobSnapshotting   = "snapshotting"
	JobRunning        = "running"
	JobHealthchecking = "healthchecking"
	JobSuccess        = "success"
	JobFailed         = "failed"
	JobRolledBack     = "rolled_back"
	JobCancelled      = "cancelled"
)

// Job log streams.
const (
	StreamStdout = "stdout"
	StreamStderr = "stderr"
	StreamSystem = "system"
)

// ErrJobActive is returned when the app already has an active job.
var ErrJobActive = errors.New("a job is already active for this app")

// ActiveJobStates are the states of a job that is not finished.
var ActiveJobStates = []string{JobPending, JobSnapshotting, JobRunning, JobHealthchecking}

// Job is one row of jobs.
type Job struct {
	ID             string    `json:"id"`
	AppID          string    `json:"app_id"`
	Type           string    `json:"type"`
	Status         string    `json:"status"`
	CurrentVersion string    `json:"current_version"`
	TargetVersion  string    `json:"target_version"`
	SnapshotName   string    `json:"snapshot_name,omitempty"`
	CreatedBy      string    `json:"created_by"`
	CreatedAt      time.Time `json:"created_at"`
	StartedAt      time.Time `json:"started_at,omitzero"`
	FinishedAt     time.Time `json:"finished_at,omitzero"`
	Error          string    `json:"error,omitempty"`
}

// JobLog is one line of job_logs.
type JobLog struct {
	Seq    int64     `json:"seq"`
	Time   time.Time `json:"ts"`
	Stream string    `json:"stream"`
	Line   string    `json:"line"`
}

const selectJob = `SELECT id, app_id, type, status, current_version, target_version, snapshot_name,
	created_by, created_at, started_at, finished_at, error FROM jobs`

func scanJob(row scanner) (Job, error) {
	var j Job
	var created, started, finished string
	err := row.Scan(&j.ID, &j.AppID, &j.Type, &j.Status, &j.CurrentVersion, &j.TargetVersion, &j.SnapshotName,
		&j.CreatedBy, &created, &started, &finished, &j.Error)
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, ErrNotFound
	}
	if err != nil {
		return Job{}, err
	}
	if j.CreatedAt, err = parseTime(created); err != nil {
		return Job{}, err
	}
	if j.StartedAt, err = parseTime(started); err != nil {
		return Job{}, err
	}
	j.FinishedAt, err = parseTime(finished)
	return j, err
}

// CreateJob inserts a pending job; ErrJobActive if the app already has an active one.
func (s *Store) CreateJob(ctx context.Context, j Job) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO jobs
		(id, app_id, type, status, current_version, target_version, created_by, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		j.ID, j.AppID, j.Type, JobPending, j.CurrentVersion, j.TargetVersion, j.CreatedBy, formatTime(j.CreatedAt))
	if err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed: jobs.app_id") {
		return ErrJobActive
	}
	return err
}

// Job returns one job.
func (s *Store) Job(ctx context.Context, id string) (Job, error) {
	return scanJob(s.db.QueryRowContext(ctx, selectJob+` WHERE id = ?`, id))
}

// Jobs returns the jobs of an app (all apps if appID is empty), newest first.
func (s *Store) Jobs(ctx context.Context, appID string, limit int) ([]Job, error) {
	return s.ListJobs(ctx, JobFilter{AppID: appID, Limit: limit})
}

// JobFilter selects jobs; empty fields match everything.
type JobFilter struct {
	AppID    string
	Statuses []string
	Limit    int
}

// ListJobs returns the jobs matching f, newest first.
func (s *Store) ListJobs(ctx context.Context, f JobFilter) ([]Job, error) {
	var where []string
	var args []any
	if f.AppID != "" {
		where = append(where, `app_id = ?`)
		args = append(args, f.AppID)
	}
	if len(f.Statuses) > 0 {
		where = append(where, `status IN (?`+strings.Repeat(`, ?`, len(f.Statuses)-1)+`)`)
		for _, st := range f.Statuses {
			args = append(args, st)
		}
	}
	query := selectJob
	if len(where) > 0 {
		query += ` WHERE ` + strings.Join(where, ` AND `)
	}
	query += ` ORDER BY created_at DESC LIMIT ?`
	args = append(args, f.Limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var jobs []Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}

// ActiveJobs returns the active job of each app that has one, by app id.
func (s *Store) ActiveJobs(ctx context.Context) (map[string]Job, error) {
	jobs, err := s.ListJobs(ctx, JobFilter{Statuses: ActiveJobStates, Limit: 1000})
	if err != nil {
		return nil, err
	}
	out := make(map[string]Job, len(jobs))
	for _, j := range jobs {
		out[j.AppID] = j
	}
	return out, nil
}

// SetJobStatus changes the state of a job. started_at is set on the first
// active state after pending, finished_at on a final state.
func (s *Store) SetJobStatus(ctx context.Context, id, status, jobErr string, now time.Time) error {
	ts := formatTime(now)
	final := status == JobSuccess || status == JobFailed || status == JobRolledBack || status == JobCancelled
	_, err := s.db.ExecContext(ctx, `UPDATE jobs SET status = ?, error = ?,
		started_at = CASE WHEN started_at = '' AND ? != 'pending' THEN ? ELSE started_at END,
		finished_at = CASE WHEN ? THEN ? ELSE finished_at END
		WHERE id = ?`, status, jobErr, status, ts, final, ts, id)
	return err
}

// SetJobSnapshot records the name of the pre-update snapshot.
func (s *Store) SetJobSnapshot(ctx context.Context, id, name string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE jobs SET snapshot_name = ? WHERE id = ?`, name, id)
	return err
}

// FailActiveJobs marks every active job failed with reason (server restart):
// their goroutine no longer exists. It returns the jobs concerned.
func (s *Store) FailActiveJobs(ctx context.Context, reason string, now time.Time) ([]string, error) {
	var ids []string
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT id FROM jobs WHERE status IN (?, ?, ?, ?)`,
			JobPending, JobSnapshotting, JobRunning, JobHealthchecking)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		ts := formatTime(now)
		for _, id := range ids {
			if _, err := tx.ExecContext(ctx, `UPDATE jobs SET status = ?, error = ?, finished_at = ? WHERE id = ?`,
				JobFailed, reason, ts, id); err != nil {
				return err
			}
		}
		return nil
	})
	return ids, err
}

// AppendJobLog adds a line; seq is the caller's per-job counter (one writer per job).
func (s *Store) AppendJobLog(ctx context.Context, jobID string, seq int64, stream, line string, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO job_logs (job_id, seq, ts, stream, line) VALUES (?, ?, ?, ?, ?)`,
		jobID, seq, formatTime(now), stream, line)
	return err
}

// LastJobLogSeq returns the seq of the last line of a job (0 without lines).
func (s *Store) LastJobLogSeq(ctx context.Context, jobID string) (int64, error) {
	var seq int64
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) FROM job_logs WHERE job_id = ?`, jobID).Scan(&seq)
	return seq, err
}

// JobLogs returns the lines of a job with seq > after, in order.
func (s *Store) JobLogs(ctx context.Context, jobID string, after int64) ([]JobLog, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT seq, ts, stream, line FROM job_logs WHERE job_id = ? AND seq > ? ORDER BY seq`, jobID, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var logs []JobLog
	for rows.Next() {
		var l JobLog
		var ts string
		if err := rows.Scan(&l.Seq, &ts, &l.Stream, &l.Line); err != nil {
			return nil, err
		}
		if l.Time, err = parseTime(ts); err != nil {
			return nil, err
		}
		logs = append(logs, l)
	}
	return logs, rows.Err()
}

// PurgeJobLogs deletes the job log lines older than before.
func (s *Store) PurgeJobLogs(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM job_logs WHERE ts < ?`, formatTime(before))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
