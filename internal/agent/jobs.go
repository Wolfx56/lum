package agent

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Wolfx56/lum/internal/agentproto"
)

// Limits of the script output.
const (
	maxLineLength = 8 << 10 // longer lines are truncated
	maxOutbox     = 20000   // job events kept while the server is unreachable
)

// killGrace is the delay between SIGTERM and SIGKILL on cancel_job (spec §8.2).
var killGrace = 30 * time.Second

// runningJob is a script being executed.
type runningJob struct {
	cmd       *exec.Cmd
	mu        sync.Mutex
	cancelled bool
}

// outbox queues the job events (job_log, job_done): they are sent by the
// current connection and survive a reconnection.
type outbox struct {
	mu      sync.Mutex
	queue   []agentproto.Envelope
	dropped int
	notify  chan struct{}
}

func newOutbox() *outbox { return &outbox{notify: make(chan struct{}, 1)} }

func (o *outbox) push(env agentproto.Envelope, droppable bool) {
	o.mu.Lock()
	if droppable && len(o.queue) >= maxOutbox {
		o.dropped++
		o.mu.Unlock()
		return
	}
	o.queue = append(o.queue, env)
	o.mu.Unlock()
	select {
	case o.notify <- struct{}{}:
	default:
	}
}

// pop returns the next event, or false if the queue is empty.
func (o *outbox) pop() (agentproto.Envelope, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.queue) == 0 {
		return agentproto.Envelope{}, false
	}
	env := o.queue[0]
	o.queue = o.queue[1:]
	return env, true
}

// unpop puts back an event whose sending failed.
func (o *outbox) unpop(env agentproto.Envelope) {
	o.mu.Lock()
	o.queue = append([]agentproto.Envelope{env}, o.queue...)
	o.mu.Unlock()
}

func (o *outbox) takeDropped() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	n := o.dropped
	o.dropped = 0
	return n
}

// runUpdate starts the script and returns once it is running; the output and
// the end are reported through the outbox.
func (a *Agent) runUpdate(req agentproto.RunUpdate) error {
	if req.JobID == "" || !validScriptName(req.Script) {
		return errors.New("invalid run_update order")
	}
	path := filepath.Join(a.opts.ScriptsDir, req.Script)
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("script %s: %w", req.Script, err)
	}
	// The agent never runs a script that is not the one the server expects (spec §9).
	if got := agentproto.Checksum(data); got != req.SHA256 {
		return fmt.Errorf("script %s: checksum mismatch (local %s, expected %s)", req.Script, got, req.SHA256)
	}

	a.jobsMu.Lock()
	defer a.jobsMu.Unlock()
	if _, ok := a.jobs[req.JobID]; ok {
		return fmt.Errorf("job %s is already running", req.JobID)
	}
	cmd := exec.Command(path) //nolint:gosec // the script is the server's, checksum verified above
	cmd.Dir = "/"
	cmd.Env = os.Environ()
	for _, k := range sortedKeys(req.Env) {
		if strings.HasPrefix(k, "LUM_") {
			cmd.Env = append(cmd.Env, k+"="+req.Env[k])
		}
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} // cancel_job signals the whole group
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting %s: %w", req.Script, err)
	}
	job := &runningJob{cmd: cmd}
	a.jobs[req.JobID] = job
	a.logger.Info("update script started", "job_id", req.JobID, "script", req.Script, "pid", cmd.Process.Pid)

	go a.watchJob(req.JobID, job, stdout, stderr)
	return nil
}

func (a *Agent) watchJob(jobID string, job *runningJob, stdout, stderr io.Reader) {
	var wg sync.WaitGroup
	for stream, r := range map[string]io.Reader{"stdout": stdout, "stderr": stderr} {
		wg.Go(func() {
			readLines(r, func(line string) {
				env, _ := agentproto.New(agentproto.TypeJobLog, "", agentproto.JobLog{JobID: jobID, Stream: stream, Line: line})
				a.outbox.push(env, true)
			})
		})
	}
	wg.Wait() // the pipes must be drained before Wait
	err := job.cmd.Wait()

	done := agentproto.JobDone{JobID: jobID}
	job.mu.Lock()
	done.Cancelled = job.cancelled
	job.mu.Unlock()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exitErr):
		done.ExitCode = exitErr.ExitCode()
		if status, ok := exitErr.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			done.Error = "killed by signal " + status.Signal().String()
		}
	default:
		done.ExitCode = -1
		done.Error = err.Error()
	}
	if n := a.outbox.takeDropped(); n > 0 {
		lost, _ := agentproto.New(agentproto.TypeJobLog, "", agentproto.JobLog{JobID: jobID, Stream: "stderr",
			Line: fmt.Sprintf("[lum-agent] %d lines lost (server unreachable for too long)", n)})
		a.outbox.push(lost, false)
	}

	a.jobsMu.Lock()
	delete(a.jobs, jobID)
	a.jobsMu.Unlock()
	a.logger.Info("update script finished", "job_id", jobID, "exit_code", done.ExitCode, "cancelled", done.Cancelled)
	env, _ := agentproto.New(agentproto.TypeJobDone, "", done)
	a.outbox.push(env, false)
}

// cancelJob sends SIGTERM to the script's process group, then SIGKILL after killGrace.
func (a *Agent) cancelJob(req agentproto.CancelJob) error {
	a.jobsMu.Lock()
	job, ok := a.jobs[req.JobID]
	a.jobsMu.Unlock()
	if !ok {
		return fmt.Errorf("job %s is not running", req.JobID)
	}
	job.mu.Lock()
	job.cancelled = true
	job.mu.Unlock()
	pgid, grace := job.cmd.Process.Pid, killGrace
	a.logger.Warn("cancelling update script", "job_id", req.JobID, "grace", grace.String())
	if err := syscall.Kill(-pgid, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	go func() {
		time.Sleep(grace)
		a.jobsMu.Lock()
		_, still := a.jobs[req.JobID]
		a.jobsMu.Unlock()
		if still {
			a.logger.Warn("update script still running, sending SIGKILL", "job_id", req.JobID)
			_ = syscall.Kill(-pgid, syscall.SIGKILL)
		}
	}()
	return nil
}

func (a *Agent) runningJobs() []string {
	a.jobsMu.Lock()
	defer a.jobsMu.Unlock()
	ids := make([]string, 0, len(a.jobs))
	for id := range a.jobs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// flush sends the queued job events until ctx is cancelled or a write fails.
func (a *Agent) flush(ctx context.Context, send func(agentproto.Envelope) error) {
	for {
		for {
			env, ok := a.outbox.pop()
			if !ok {
				break
			}
			if err := send(env); err != nil {
				a.outbox.unpop(env)
				return
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-a.outbox.notify:
		}
	}
}

var ansiEscape = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b[()][0-9A-Za-z]|\x1b[=>]`)

// readLines calls fn for every line of r, cleaned for display: ANSI escape
// sequences removed, carriage returns resolved as a terminal would (only the
// text after the last \r is kept), length capped.
func readLines(r io.Reader, fn func(string)) {
	br := bufio.NewReaderSize(r, 64<<10)
	var line []byte
	truncated := false
	emit := func() {
		s := string(line)
		if i := strings.LastIndexByte(strings.TrimRight(s, "\r"), '\r'); i >= 0 {
			s = s[i+1:]
		}
		s = strings.TrimRight(ansiEscape.ReplaceAllString(s, ""), "\r")
		if truncated {
			s += " […]"
		}
		fn(s)
		line, truncated = line[:0], false
	}
	for {
		chunk, err := br.ReadSlice('\n')
		data := chunk
		if n := len(data); n > 0 && data[n-1] == '\n' {
			data = data[:n-1]
		}
		if len(line)+len(data) > maxLineLength {
			truncated = true
		}
		if room := maxLineLength - len(line); room > 0 {
			line = append(line, data[:min(len(data), room)]...)
		}
		switch {
		case err == nil:
			emit()
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		default:
			if len(line) > 0 {
				emit()
			}
			return
		}
	}
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
