package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Wolfx56/lum/internal/agentproto"
)

func TestReadLines(t *testing.T) {
	long := strings.Repeat("x", maxLineLength+100)
	in := "plain\n\x1b[1;32mgreen\x1b[0m\nspin 1\rspin 2\rdone\n" + long + "\nwindows\r\nlast without newline"
	var got []string
	readLines(strings.NewReader(in), func(l string) { got = append(got, l) })
	want := []string{"plain", "green", "done", strings.Repeat("x", maxLineLength) + " […]", "windows", "last without newline"}
	if len(got) != len(want) {
		t.Fatalf("got %d lines %q, want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d = %.40q, want %.40q", i, got[i], want[i])
		}
	}
}

// waitDone reads the outbox until the job_done of the job.
func waitDone(t *testing.T, a *Agent, jobID string) (agentproto.JobDone, []string) {
	t.Helper()
	var lines []string
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		env, ok := a.outbox.pop()
		if !ok {
			time.Sleep(5 * time.Millisecond)
			continue
		}
		switch env.Type {
		case agentproto.TypeJobLog:
			var l agentproto.JobLog
			_ = env.Decode(&l)
			lines = append(lines, l.Stream+": "+l.Line)
		case agentproto.TypeJobDone:
			var d agentproto.JobDone
			_ = env.Decode(&d)
			if d.JobID == jobID {
				return d, lines
			}
		}
	}
	t.Fatal("no job_done")
	return agentproto.JobDone{}, nil
}

func installScript(t *testing.T, a *Agent, name, content string) string {
	t.Helper()
	if err := os.MkdirAll(a.opts.ScriptsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a.opts.ScriptsDir, name), []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
	return agentproto.Checksum([]byte(content))
}

func TestRunUpdateEnvAndChecksum(t *testing.T) {
	a := newTestAgent(t)
	sum := installScript(t, a, "up.sh", "#!/bin/sh\necho \"$LUM_JOB_ID $LUM_TARGET_VERSION ${EVIL:-unset}\"\n")

	if err := a.runUpdate(agentproto.RunUpdate{JobID: "j1", Script: "up.sh", SHA256: "0000"}); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("checksum mismatch must be refused: %v", err)
	}
	if err := a.runUpdate(agentproto.RunUpdate{JobID: "j1", Script: "../up.sh", SHA256: sum}); err == nil {
		t.Fatal("a path must be refused")
	}
	err := a.runUpdate(agentproto.RunUpdate{JobID: "j1", Script: "up.sh", SHA256: sum,
		Env: map[string]string{"LUM_JOB_ID": "j1", "LUM_TARGET_VERSION": "2.0.0", "EVIL": "injected"}})
	if err != nil {
		t.Fatal(err)
	}
	done, lines := waitDone(t, a, "j1")
	if done.ExitCode != 0 || done.Error != "" || len(lines) != 1 || lines[0] != "stdout: j1 2.0.0 unset" {
		t.Fatalf("done = %+v, lines = %q (only LUM_* variables are passed)", done, lines)
	}
}

func TestCancelEscalatesToSIGKILL(t *testing.T) {
	killGrace = 200 * time.Millisecond
	defer func() { killGrace = 30 * time.Second }()
	a := newTestAgent(t)
	sum := installScript(t, a, "stubborn.sh", "#!/usr/bin/env bash\ntrap 'echo ignoring TERM' TERM\necho ready\nwhile true; do sleep 0.05; done\n")
	if err := a.runUpdate(agentproto.RunUpdate{JobID: "j2", Script: "stubborn.sh", SHA256: sum}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond) // let bash install its trap
	if err := a.cancelJob(agentproto.CancelJob{JobID: "j2"}); err != nil {
		t.Fatal(err)
	}
	done, lines := waitDone(t, a, "j2")
	if !done.Cancelled || !strings.Contains(done.Error, "killed") {
		t.Fatalf("done = %+v, lines = %q", done, lines)
	}
	if err := a.cancelJob(agentproto.CancelJob{JobID: "j2"}); err == nil {
		t.Error("cancelling a finished job must fail")
	}
}
