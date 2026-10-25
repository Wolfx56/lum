package server

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Wolfx56/lum/internal/agent"
	"github.com/Wolfx56/lum/internal/store"
)

type sseEvent struct {
	event, id, data string
}

// readSSE parses a Server-Sent Events stream until it ends.
func readSSE(t *testing.T, resp *http.Response, events chan<- sseEvent) {
	t.Helper()
	defer close(events)
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	var ev sseEvent
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if ev.event != "" {
				events <- ev
			}
			ev = sseEvent{}
		case strings.HasPrefix(line, "event: "):
			ev.event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "id: "):
			ev.id = strings.TrimPrefix(line, "id: ")
		case strings.HasPrefix(line, "data: "):
			ev.data = strings.TrimPrefix(line, "data: ")
		}
	}
}

type jobResponse struct {
	Job struct {
		ID     string `json:"id"`
		AppID  string `json:"app_id"`
		Status string `json:"status"`
		Error  string `json:"error"`
	} `json:"job"`
}

// Acceptance criteria §7.4: trigger, 409 on a second trigger, live logs (SSE), cancel.
func TestAPIUpdateJobFlow(t *testing.T) {
	e := newAPIEnv(t)
	if status, body := e.do(t, http.MethodPost, "/api/v1/apps/sonarr/check", nil); status != http.StatusOK || !strings.Contains(body, "update_available") {
		t.Fatalf("check: %d %s", status, body)
	}

	status, body := e.do(t, http.MethodPost, "/api/v1/apps/sonarr/update", nil)
	if status != http.StatusAccepted {
		t.Fatalf("POST /apps/sonarr/update: %d %s", status, body)
	}
	job := decode[jobResponse](t, body).Job
	if job.AppID != "sonarr" || job.Status != store.JobPending {
		t.Fatalf("job = %+v", job)
	}

	// One active job per app: the second trigger gets 409 with the active job.
	status, body = e.do(t, http.MethodPost, "/api/v1/apps/sonarr/update", nil)
	if status != http.StatusConflict || !strings.Contains(body, `"code":"job_active"`) || !strings.Contains(body, `"job_id":"`+job.ID+`"`) {
		t.Errorf("second trigger: %d %s", status, body)
	}

	if _, body := e.do(t, http.MethodGet, "/api/v1/apps", nil); !strings.Contains(body, `"active_job":{"id":"`+job.ID+`"`) {
		t.Errorf("GET /apps must show the active job: %s", body)
	}
	if _, body := e.do(t, http.MethodGet, "/api/v1/jobs?app=sonarr&status=active", nil); !strings.Contains(body, job.ID) || !strings.Contains(body, `"app_name":"Sonarr"`) {
		t.Errorf("GET /jobs?status=active: %s", body)
	}
	status, body = e.do(t, http.MethodGet, "/api/v1/jobs/"+job.ID, nil)
	if status != http.StatusOK || !strings.Contains(body, `"running":true`) || !strings.Contains(body, `"reason":"no_snapshot"`) {
		t.Errorf("GET /jobs/{id}: %d %s", status, body)
	}

	// Live logs, then cancellation: the stream delivers the cancellation and ends.
	resp, err := http.Get(e.srv.URL + "/api/v1/jobs/" + job.ID + "/logs")
	if err != nil {
		t.Fatal(err)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q", ct)
	}
	events := make(chan sseEvent, 100)
	go readSSE(t, resp, events)
	waitLine := func(substr string) {
		t.Helper()
		timeout := time.After(10 * time.Second)
		for {
			select {
			case ev, ok := <-events:
				if !ok {
					t.Fatalf("stream ended before %q", substr)
				}
				if ev.event == "log" && strings.Contains(ev.data, substr) {
					return
				}
			case <-timeout:
				t.Fatalf("no log line %q", substr)
			}
		}
	}
	waitLine("Request accepted")

	status, body = e.do(t, http.MethodPost, "/api/v1/jobs/"+job.ID+"/cancel", nil)
	if status != http.StatusAccepted {
		t.Fatalf("cancel: %d %s", status, body)
	}
	waitLine("Job cancelled by anonymous")
	var end sseEvent
	for ev := range events {
		if ev.event == "end" {
			end = ev
		}
	}
	if !strings.Contains(end.data, `"status":"cancelled"`) {
		t.Fatalf("end event = %+v", end)
	}

	// A finished job: the stream replays the logs after Last-Event-ID and ends at once.
	req, _ := http.NewRequest(http.MethodGet, e.srv.URL+"/api/v1/jobs/"+job.ID+"/logs", nil)
	req.Header.Set("Last-Event-ID", "1")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	replay := make(chan sseEvent, 100)
	readSSE(t, resp, replay)
	var got []sseEvent
	for ev := range replay {
		got = append(got, ev)
	}
	if len(got) < 3 || got[0].event != "log" || got[0].id != "2" || got[len(got)-1].event != "end" {
		t.Errorf("replay = %+v", got)
	}

	for _, c := range []struct {
		method, path string
		status       int
		code         string
	}{
		{http.MethodPost, "/api/v1/jobs/" + job.ID + "/cancel", http.StatusConflict, "job_not_running"},
		{http.MethodPost, "/api/v1/jobs/" + job.ID + "/rollback", http.StatusConflict, "no_snapshot"},
		{http.MethodGet, "/api/v1/jobs/nope", http.StatusNotFound, "job_not_found"},
		{http.MethodGet, "/api/v1/jobs/nope/logs", http.StatusNotFound, "job_not_found"},
		{http.MethodPost, "/api/v1/apps/tautulli/update", http.StatusUnprocessableEntity, "no_update_recipe"},
		{http.MethodPost, "/api/v1/apps/nope/update", http.StatusNotFound, "app_not_found"},
		// Spec §7.4: agent offline, refused before any snapshot with an explicit message.
		{http.MethodPost, "/api/v1/apps/vaultwarden/update", http.StatusServiceUnavailable, "agent_offline"},
		{http.MethodGet, "/api/v1/jobs?limit=0", http.StatusBadRequest, "invalid_limit"},
	} {
		if status, body := e.do(t, c.method, c.path, nil); status != c.status || !strings.Contains(body, `"code":"`+c.code+`"`) {
			t.Errorf("%s %s: %d %s, want %d %s", c.method, c.path, status, body, c.status, c.code)
		}
	}
	if jobs, _ := e.store.Jobs(context.Background(), "vaultwarden", 10); len(jobs) != 0 {
		t.Error("no job must be created for an offline agent")
	}

	// Audit: every write attempt, refusals included.
	_, body = e.do(t, http.MethodGet, "/api/v1/audit?limit=50", nil)
	for _, want := range []string{
		`"action":"app.update","target":"sonarr","details":"job ` + job.ID + `"`,
		`"action":"app.update","target":"sonarr","details":"refused: job_active"`,
		`"action":"job.cancel","target":"` + job.ID + `","details":"sonarr"`,
		`"action":"job.rollback","target":"` + job.ID + `","details":"refused: no_snapshot"`,
		`"action":"app.update","target":"vaultwarden","details":"refused: agent_offline"`,
		`"user":"anonymous"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("audit: missing %s in %s", want, body)
		}
	}

	if _, body := e.do(t, http.MethodGet, "/metrics", nil); !strings.Contains(body, `lum_jobs_total{status="cancelled",type="api"} 1`) ||
		!strings.Contains(body, "lum_jobs_active 0") || !strings.Contains(body, "lum_agents_connected 0") {
		t.Error("job metrics missing")
	}
}

type agentsResponse struct {
	Agents []struct {
		ID      string   `json:"id"`
		Status  string   `json:"status"`
		Apps    []string `json:"apps"`
		Scripts []struct {
			Name  string `json:"name"`
			State string `json:"state"`
		} `json:"scripts"`
	} `json:"agents"`
}

func (e *apiEnv) agent(t *testing.T, id string) (status string, scripts map[string]string) {
	t.Helper()
	_, body := e.do(t, http.MethodGet, "/api/v1/agents", nil)
	for _, a := range decode[agentsResponse](t, body).Agents {
		if a.ID == id {
			scripts = map[string]string{}
			for _, s := range a.Scripts {
				scripts[s.Name] = s.State
			}
			return a.Status, scripts
		}
	}
	return "", nil
}

func TestAPIAgents(t *testing.T) {
	e := newAPIEnv(t)

	// Referenced by apps.yaml, never enrolled: listed, its script missing.
	if status, scripts := e.agent(t, "vw-lxc"); status != "not_enrolled" || scripts["up.sh"] != "missing" {
		t.Fatalf("vw-lxc before enrollment: %s %v", status, scripts)
	}
	for _, c := range []struct {
		method, path string
		status       int
		code         string
	}{
		{http.MethodPost, "/api/v1/agents/vw-lxc/sync-scripts", http.StatusServiceUnavailable, "agent_offline"},
		{http.MethodPost, "/api/v1/agents/vw-lxc/revoke", http.StatusConflict, "agent_not_enrolled"},
		{http.MethodDelete, "/api/v1/agents/vw-lxc", http.StatusConflict, "agent_not_revoked"},
		{http.MethodPost, "/api/v1/agents/nope/revoke", http.StatusNotFound, "agent_not_found"},
	} {
		if status, body := e.do(t, c.method, c.path, nil); status != c.status || !strings.Contains(body, `"code":"`+c.code+`"`) {
			t.Errorf("%s %s: %d %s, want %d %s", c.method, c.path, status, body, c.status, c.code)
		}
	}

	// A real agent enrolls: online, script synchronized automatically.
	dir := t.TempDir()
	a, err := agent.New(agent.Options{
		ID: "vw-lxc", ServerURL: e.srv.URL, Secret: secrets["AGENT_SECRET"],
		TokenFile: filepath.Join(dir, "token"), ScriptsDir: filepath.Join(dir, "scripts"),
		MinBackoff: 20 * time.Millisecond, MaxBackoff: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("agent: %v", err)
		}
	})
	waitUntil(t, "agent online and in sync", func() bool {
		status, scripts := e.agent(t, "vw-lxc")
		return status == "online" && scripts["up.sh"] == "in_sync"
	})

	if status, body := e.do(t, http.MethodPost, "/api/v1/agents/vw-lxc/sync-scripts", nil); status != http.StatusOK || !strings.Contains(body, `"state":"in_sync"`) {
		t.Errorf("forced sync: %d %s", status, body)
	}

	// Revocation: the connection is closed and the agent cannot come back.
	if status, body := e.do(t, http.MethodPost, "/api/v1/agents/vw-lxc/revoke", nil); status != http.StatusOK || !strings.Contains(body, `"status":"revoked"`) {
		t.Fatalf("revoke: %d %s", status, body)
	}
	waitUntil(t, "agent disconnected", func() bool { return !e.svc.Hub().Connected("vw-lxc") })
	time.Sleep(200 * time.Millisecond) // several reconnection attempts, all refused
	if e.svc.Hub().Connected("vw-lxc") {
		t.Fatal("a revoked agent must not reconnect")
	}
	if status, body := e.do(t, http.MethodPost, "/api/v1/agents/vw-lxc/sync-scripts", nil); status != http.StatusConflict || !strings.Contains(body, `"code":"agent_revoked"`) {
		t.Errorf("sync of a revoked agent: %d %s", status, body)
	}

	// Deletion of the revoked agent: back to "not enrolled".
	if status, body := e.do(t, http.MethodDelete, "/api/v1/agents/vw-lxc", nil); status != http.StatusOK {
		t.Fatalf("delete: %d %s", status, body)
	}
	if status, _ := e.agent(t, "vw-lxc"); status != "not_enrolled" {
		t.Errorf("after delete: %s", status)
	}

	_, body := e.do(t, http.MethodGet, "/api/v1/audit", nil)
	var audit struct {
		Entries []struct{ Action, Target, Details string } `json:"entries"`
	}
	if err := json.Unmarshal([]byte(body), &audit); err != nil {
		t.Fatal(err)
	}
	var actions []string
	for _, en := range audit.Entries {
		if en.Target == "vw-lxc" {
			actions = append(actions, en.Action+" "+en.Details)
		}
	}
	want := []string{"agent.delete ", "agent.sync_scripts refused: agent_revoked", "agent.revoke ", "agent.sync_scripts ", "agent.sync_scripts refused: agent_offline"}
	if strings.Join(actions, "|") != strings.Join(want, "|") {
		t.Errorf("audit of vw-lxc = %q, want %q", actions, want)
	}
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for: %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
