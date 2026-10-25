package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func openTest(t *testing.T) (*Store, string) {
	t.Helper()
	file := filepath.Join(t.TempDir(), "lum.db")
	s, err := Open(context.Background(), file)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, file
}

func TestOpenIsIdempotentAndUsesWAL(t *testing.T) {
	s, file := openTest(t)
	var mode string
	if err := s.db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil || mode != "wal" {
		t.Errorf("journal_mode = %q (%v), want wal", mode, err)
	}
	s.Close()

	// Re-opening must not re-apply migrations.
	s2, err := Open(context.Background(), file)
	if err != nil {
		t.Fatalf("re-open: %v", err)
	}
	defer s2.Close()
	var n int
	if err := s2.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil || n != 4 {
		t.Errorf("schema_migrations rows = %d (%v), want 4", n, err)
	}
}

func TestRecordCheckAndState(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	t0 := time.Date(2026, 10, 18, 7, 0, 0, 0, time.UTC)

	prev, err := s.RecordCheck(ctx, Check{AppID: "sonarr", CheckedAt: t0, CurrentVersion: "4.0.0", LatestVersion: "4.0.0", Status: "up_to_date"})
	if err != nil {
		t.Fatal(err)
	}
	if prev.AppID != "" {
		t.Errorf("first check: previous state should be empty, got %+v", prev)
	}
	if err := s.MarkNotified(ctx, "sonarr", "4.0.0"); err != nil {
		t.Fatal(err)
	}

	prev, err = s.RecordCheck(ctx, Check{AppID: "sonarr", CheckedAt: t0.Add(time.Hour), CurrentVersion: "4.0.0", LatestVersion: "4.0.1",
		Status: "update_available", ReleaseURL: "https://example.com/r", ReleaseNotes: "notes"})
	if err != nil {
		t.Fatal(err)
	}
	if prev.Status != "up_to_date" || !prev.LastCheckAt.Equal(t0) {
		t.Errorf("previous state = %+v", prev)
	}

	st, err := s.AppState(ctx, "sonarr")
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != "update_available" || st.LatestVersion != "4.0.1" || st.LastNotifiedVersion != "4.0.0" || st.ReleaseURL != "https://example.com/r" {
		t.Errorf("state = %+v (last_notified_version must be preserved)", st)
	}

	checks, err := s.Checks(ctx, "sonarr", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(checks) != 2 || checks[0].LatestVersion != "4.0.1" || checks[0].ReleaseNotes != "notes" {
		t.Errorf("checks = %+v, want 2, most recent first", checks)
	}

	states, err := s.AppStates(ctx)
	if err != nil || len(states) != 1 {
		t.Errorf("AppStates = %v (%v)", states, err)
	}
}

func TestNotFound(t *testing.T) {
	s, _ := openTest(t)
	if _, err := s.AppState(context.Background(), "absent"); !errors.Is(err, ErrNotFound) {
		t.Errorf("AppState: want ErrNotFound, got %v", err)
	}
	if err := s.MarkNotified(context.Background(), "absent", "1.0.0"); !errors.Is(err, ErrNotFound) {
		t.Errorf("MarkNotified: want ErrNotFound, got %v", err)
	}
}

func TestPurgeChecks(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)
	for _, age := range []time.Duration{100 * 24 * time.Hour, 91 * 24 * time.Hour, 10 * 24 * time.Hour} {
		if _, err := s.RecordCheck(ctx, Check{AppID: "a", CheckedAt: now.Add(-age), Status: "up_to_date"}); err != nil {
			t.Fatal(err)
		}
	}
	n, err := s.PurgeChecks(ctx, now.Add(-90*24*time.Hour))
	if err != nil || n != 2 {
		t.Fatalf("purged %d (%v), want 2", n, err)
	}
	if checks, _ := s.Checks(ctx, "a", 10); len(checks) != 1 {
		t.Errorf("remaining checks = %d, want 1", len(checks))
	}
}

func TestAudit(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	for _, action := range []string{"config.reload", "app.check"} {
		if err := s.Audit(ctx, AuditEntry{User: "alice", Action: action, Target: "sonarr"}); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := s.AuditEntries(ctx, 10)
	if err != nil || len(entries) != 2 || entries[0].Action != "app.check" || entries[0].TS.IsZero() {
		t.Fatalf("entries = %+v (%v)", entries, err)
	}
}

func TestConcurrentRecordCheck(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make(chan error, 50)
	for i := range 50 {
		wg.Go(func() {
			_, err := s.RecordCheck(ctx, Check{AppID: fmt.Sprintf("app%d", i%10), CheckedAt: time.Now(), Status: "up_to_date"})
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent RecordCheck: %v", err)
		}
	}
	if states, _ := s.AppStates(ctx); len(states) != 10 {
		t.Errorf("states = %d, want 10", len(states))
	}
}

func TestSessions(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	sess := Session{IDHash: "h1", Subject: "sub-1", Username: "alice", Name: "Alice", CreatedAt: now, ExpiresAt: now.Add(12 * time.Hour)}
	if err := s.CreateSession(ctx, sess); err != nil {
		t.Fatal(err)
	}
	got, err := s.ValidSession(ctx, "h1", now.Add(time.Hour))
	if err != nil || got.Username != "alice" || !got.ExpiresAt.Equal(sess.ExpiresAt) {
		t.Fatalf("ValidSession = %+v (%v)", got, err)
	}
	if _, err := s.ValidSession(ctx, "h1", now.Add(13*time.Hour)); !errors.Is(err, ErrNotFound) {
		t.Errorf("expired session must not be valid: %v", err)
	}
	if n, err := s.PurgeSessions(ctx, now.Add(13*time.Hour)); err != nil || n != 1 {
		t.Errorf("PurgeSessions = %d (%v), want 1", n, err)
	}
	_ = s.CreateSession(ctx, Session{IDHash: "h2", Subject: "s", Username: "bob", CreatedAt: now, ExpiresAt: now.Add(time.Hour)})
	if err := s.DeleteSession(ctx, "h2"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ValidSession(ctx, "h2", now); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleted session must not be valid: %v", err)
	}
}

func TestAgents(t *testing.T) {
	s, _ := openTest(t)
	ctx := context.Background()
	t0 := time.Date(2026, 10, 18, 7, 0, 0, 0, time.UTC)

	if err := s.EnrollAgent(ctx, "vw-lxc", "hash1", t0); err != nil {
		t.Fatal(err)
	}
	if err := s.AgentSeen(ctx, "vw-lxc", "1.0.0", "vaultwarden", t0); err != nil {
		t.Fatal(err)
	}
	a, err := s.Agent(ctx, "vw-lxc")
	if err != nil || a.Status != AgentOnline || a.TokenHash != "hash1" || a.Hostname != "vaultwarden" || !a.LastSeenAt.Equal(t0) {
		t.Fatalf("agent = %+v, %v", a, err)
	}

	// Re-enrollment replaces the token, but never for a revoked agent.
	if err := s.EnrollAgent(ctx, "vw-lxc", "hash2", t0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE agents SET revoked = 1 WHERE id = 'vw-lxc'`); err != nil {
		t.Fatal(err)
	}
	if err := s.EnrollAgent(ctx, "vw-lxc", "hash3", t0); !errors.Is(err, ErrNotFound) {
		t.Errorf("re-enrolling a revoked agent: err = %v, want ErrNotFound", err)
	}
	if a, _ := s.Agent(ctx, "vw-lxc"); a.TokenHash != "hash2" || !a.Revoked {
		t.Errorf("agent = %+v", a)
	}

	ids, err := s.MarkAgentsOffline(ctx, t0.Add(time.Minute))
	if err != nil || len(ids) != 1 || ids[0] != "vw-lxc" {
		t.Fatalf("MarkAgentsOffline = %v, %v", ids, err)
	}
	if ids, _ := s.MarkAgentsOffline(ctx, t0.Add(time.Minute)); len(ids) != 0 {
		t.Errorf("already offline agents must not be reported again: %v", ids)
	}

	if err := s.SetAgentScripts(ctx, "vw-lxc", map[string]string{"a.sh": "x", "b.sh": "y"}, t0); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAgentScripts(ctx, "vw-lxc", map[string]string{"a.sh": "z"}, t0); err != nil {
		t.Fatal(err)
	}
	got, err := s.AgentScripts(ctx, "vw-lxc")
	if err != nil || len(got) != 1 || got["a.sh"] != "z" {
		t.Errorf("AgentScripts = %v, %v", got, err)
	}
	if all, err := s.Agents(ctx); err != nil || len(all) != 1 {
		t.Errorf("Agents = %v, %v", all, err)
	}
}

func TestFormatTimeSortable(t *testing.T) {
	base := time.Date(2026, 10, 25, 12, 0, 5, 0, time.UTC)
	a := formatTime(base.Add(120 * time.Millisecond))
	b := formatTime(base.Add(123 * time.Millisecond))
	if a >= b {
		t.Errorf("formatTime not lexically sortable: %q >= %q", a, b)
	}
	if got, err := parseTime(a); err != nil || !got.Equal(base.Add(120*time.Millisecond)) {
		t.Errorf("parseTime(%q) = %v, %v", a, got, err)
	}
}
