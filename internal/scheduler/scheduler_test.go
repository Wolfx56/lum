package scheduler

import (
	"context"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wolfx56/lum/internal/store"
)

type countingRunner struct {
	runs  atomic.Int32
	block chan struct{} // when set, CheckAll blocks until closed or ctx is done
}

func (r *countingRunner) CheckAll(ctx context.Context) []store.Check {
	r.runs.Add(1)
	if r.block != nil {
		select {
		case <-r.block:
		case <-ctx.Done():
		}
	}
	return nil
}

type fakePurger struct{ before time.Time }

func (p *fakePurger) PurgeChecks(_ context.Context, before time.Time) (int64, error) {
	p.before = before
	return 3, nil
}

func (p *fakePurger) PurgeSessions(context.Context, time.Time) (int64, error) { return 0, nil }

func (p *fakePurger) PurgeJobLogs(context.Context, time.Time) (int64, error) { return 0, nil }

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestRunsChecksOnSchedule(t *testing.T) {
	r := &countingRunner{}
	s, err := Start(Options{CheckCron: "@every 1s", Runner: r, Purger: &fakePurger{}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	if s.NextCheck().IsZero() {
		t.Error("NextCheck must be known")
	}
	waitFor(t, func() bool { return r.runs.Load() >= 1 })
}

func TestChecksNeverOverlap(t *testing.T) {
	r := &countingRunner{block: make(chan struct{})}
	s, err := Start(Options{CheckCron: "@every 1s", Runner: r, Purger: &fakePurger{}})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return r.runs.Load() == 1 })
	time.Sleep(2200 * time.Millisecond) // two more ticks while the first check is still running
	if n := r.runs.Load(); n != 1 {
		t.Errorf("runs = %d, a check must not start while the previous one runs", n)
	}
	s.Stop() // cancels the blocked check
}

func TestInvalidCron(t *testing.T) {
	if _, err := Start(Options{CheckCron: "every day", Runner: &countingRunner{}, Purger: &fakePurger{}}); err == nil {
		t.Fatal("expected an error")
	}
}

func TestPurgeUsesRetention(t *testing.T) {
	p := &fakePurger{}
	Purge(context.Background(), p, slog.New(slog.DiscardHandler))
	age := time.Since(p.before)
	if age < Retention || age > Retention+time.Minute {
		t.Errorf("purge cutoff is %v ago, want %v", age, Retention)
	}
}

func TestJitter(t *testing.T) {
	if jitter(0) != 0 {
		t.Error("no jitter configured must give no delay")
	}
	for range 100 {
		if d := jitter(time.Minute); d < 0 || d >= time.Minute {
			t.Fatalf("jitter out of range: %v", d)
		}
	}
}
