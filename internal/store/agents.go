package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Agent statuses.
const (
	AgentOnline  = "online"
	AgentOffline = "offline"
)

// Agent is one row of agents.
type Agent struct {
	ID           string    `json:"id"`
	TokenHash    string    `json:"-"` // SHA-256 (hex) of the agent token
	AgentVersion string    `json:"agent_version"`
	Hostname     string    `json:"hostname"`
	LastSeenAt   time.Time `json:"last_seen_at"`
	Status       string    `json:"status"`
	EnrolledAt   time.Time `json:"enrolled_at"`
	Revoked      bool      `json:"revoked"`
}

const selectAgent = `SELECT id, token_hash, agent_version, hostname, last_seen_at, status, enrolled_at, revoked FROM agents`

func scanAgent(row scanner) (Agent, error) {
	var a Agent
	var seen, enrolled string
	err := row.Scan(&a.ID, &a.TokenHash, &a.AgentVersion, &a.Hostname, &seen, &a.Status, &enrolled, &a.Revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return Agent{}, ErrNotFound
	}
	if err != nil {
		return Agent{}, err
	}
	if a.LastSeenAt, err = parseTime(seen); err != nil {
		return Agent{}, err
	}
	a.EnrolledAt, err = parseTime(enrolled)
	return a, err
}

// Agent returns one agent.
func (s *Store) Agent(ctx context.Context, id string) (Agent, error) {
	return scanAgent(s.db.QueryRowContext(ctx, selectAgent+` WHERE id = ?`, id))
}

// Agents returns every enrolled agent, by id.
func (s *Store) Agents(ctx context.Context) ([]Agent, error) {
	rows, err := s.db.QueryContext(ctx, selectAgent+` ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var agents []Agent
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		agents = append(agents, a)
	}
	return agents, rows.Err()
}

// EnrollAgent records a new token for the agent (first enrollment or
// re-enrollment). A revoked agent is never re-enrolled: ErrNotFound.
func (s *Store) EnrollAgent(ctx context.Context, id, tokenHash string, now time.Time) error {
	res, err := s.db.ExecContext(ctx, `INSERT INTO agents (id, token_hash, enrolled_at) VALUES (?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET token_hash = excluded.token_hash, enrolled_at = excluded.enrolled_at
		WHERE agents.revoked = 0`, id, tokenHash, formatTime(now))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// AgentSeen marks the agent online and records what it reported.
func (s *Store) AgentSeen(ctx context.Context, id, agentVersion, hostname string, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE agents SET status = ?, agent_version = ?, hostname = ?, last_seen_at = ? WHERE id = ?`,
		AgentOnline, agentVersion, hostname, formatTime(now), id)
	return err
}

// MarkAgentsOffline sets offline the online agents not seen since before and
// returns their ids (the transitions, for notifications).
func (s *Store) MarkAgentsOffline(ctx context.Context, before time.Time) ([]string, error) {
	var ids []string
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT id FROM agents WHERE status = ? AND last_seen_at < ? ORDER BY id`, AgentOnline, formatTime(before))
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
		for _, id := range ids {
			if _, err := tx.ExecContext(ctx, `UPDATE agents SET status = ? WHERE id = ?`, AgentOffline, id); err != nil {
				return err
			}
		}
		return nil
	})
	return ids, err
}

// RevokeAgent revokes the agent's token for good (spec §10): its connections
// are refused, and it cannot enroll again until deleted. ErrNotFound if unknown.
func (s *Store) RevokeAgent(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE agents SET revoked = 1, status = ? WHERE id = ?`, AgentOffline, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteAgent forgets an agent and its scripts, so that it can enroll again
// with the shared secret. ErrNotFound if unknown.
func (s *Store) DeleteAgent(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM agents WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetAgentScripts replaces the scripts reported by an agent (name → checksum).
func (s *Store) SetAgentScripts(ctx context.Context, agentID string, scripts map[string]string, now time.Time) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM agent_scripts WHERE agent_id = ?`, agentID); err != nil {
			return err
		}
		for name, sum := range scripts {
			if _, err := tx.ExecContext(ctx, `INSERT INTO agent_scripts (agent_id, script_name, checksum, synced_at) VALUES (?, ?, ?, ?)`,
				agentID, name, sum, formatTime(now)); err != nil {
				return err
			}
		}
		return nil
	})
}

// AgentScripts returns the scripts last reported by an agent (name → checksum).
func (s *Store) AgentScripts(ctx context.Context, agentID string) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT script_name, checksum FROM agent_scripts WHERE agent_id = ?`, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	scripts := map[string]string{}
	for rows.Next() {
		var name, sum string
		if err := rows.Scan(&name, &sum); err != nil {
			return nil, err
		}
		scripts[name] = sum
	}
	return scripts, rows.Err()
}
