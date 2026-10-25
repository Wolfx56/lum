package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Wolfx56/lum/internal/config"
)

type received struct {
	path string
	body map[string]any
}

type recorder struct {
	mu     sync.Mutex
	got    []received
	status int
}

func (r *recorder) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	data, _ := io.ReadAll(req.Body)
	var body map[string]any
	_ = json.Unmarshal(data, &body)
	r.mu.Lock()
	r.got = append(r.got, received{path: req.URL.Path, body: body})
	r.mu.Unlock()
	if r.status != 0 {
		w.WriteHeader(r.status)
	}
}

func event() Event {
	return Event{
		Type: config.EventUpdateAvailable, AppID: "sonarr", AppName: "Sonarr",
		CurrentVersion: "4.0.9", LatestVersion: "4.0.10", ReleaseURL: "https://github.com/x/releases/tag/v4.0.10",
		Title: "Sonarr: update available", Message: "4.0.9 → 4.0.10",
	}
}

func TestDispatchFiltersByEvent(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(rec)
	defer srv.Close()

	d := FromConfig([]config.Notification{
		{Type: config.NotifyNtfy, URL: config.Secret(srv.URL + "/lum-updates"), On: []string{config.EventUpdateAvailable}},
		{Type: config.NotifyWebhook, URL: config.Secret(srv.URL + "/api/webhook/test-hook"), On: []string{config.EventUpdateAvailable, config.EventJobFailed}},
		{Type: config.NotifyWebhook, URL: config.Secret(srv.URL + "/only-jobs"), On: []string{config.EventJobFailed}},
	}, nil)

	if err := d.Dispatch(context.Background(), event()); err != nil {
		t.Fatal(err)
	}
	if len(rec.got) != 2 {
		t.Fatalf("got %d deliveries, want 2 (the job-only channel must be skipped)", len(rec.got))
	}

	ntfy := rec.got[0]
	if ntfy.path != "/" || ntfy.body["topic"] != "lum-updates" || ntfy.body["title"] != "Sonarr: update available" ||
		ntfy.body["click"] != "https://github.com/x/releases/tag/v4.0.10" {
		t.Errorf("ntfy publish = %+v", ntfy)
	}

	hook := rec.got[1]
	if hook.path != "/api/webhook/test-hook" || hook.body["event"] != "update_available" || hook.body["latest_version"] != "4.0.10" || hook.body["time"] == "" {
		t.Errorf("webhook payload = %+v", hook)
	}
}

func TestDispatchErrorsNeverContainTheURL(t *testing.T) {
	rec := &recorder{status: http.StatusInternalServerError}
	srv := httptest.NewServer(rec)
	defer srv.Close()

	d := FromConfig([]config.Notification{
		{Type: config.NotifyWebhook, URL: config.Secret(srv.URL + "/hook/test-secret-id"), On: []string{config.EventUpdateAvailable}},
		{Type: config.NotifyWebhook, URL: "http://127.0.0.1:1/hook/test-secret-id", On: []string{config.EventUpdateAvailable}},
	}, nil)
	err := d.Dispatch(context.Background(), event())
	if err == nil {
		t.Fatal("expected errors")
	}
	msg := err.Error()
	if strings.Contains(msg, "test-secret-id") {
		t.Errorf("error leaks the webhook URL: %s", msg)
	}
	if !strings.Contains(msg, "notifications[0] (webhook): unexpected status 500") || !strings.Contains(msg, "notifications[1] (webhook)") {
		t.Errorf("both failures must be reported with their channel: %s", msg)
	}
}

func TestSplitTopic(t *testing.T) {
	cases := map[string][2]string{
		"https://ntfy.example.com/updates":       {"https://ntfy.example.com/", "updates"},
		"https://ntfy.example.com/updates/":      {"https://ntfy.example.com/", "updates"},
		"https://host/ntfy/updates?auth=x":       {"https://host/ntfy/?auth=x", "updates"},
		"https://user:pass@ntfy.example.com/lum": {"https://user:pass@ntfy.example.com/", "lum"},
	}
	for in, want := range cases {
		base, topic, err := splitTopic(in)
		if err != nil || base != want[0] || topic != want[1] {
			t.Errorf("splitTopic(%q) = %q, %q, %v; want %q, %q", in, base, topic, err, want[0], want[1])
		}
	}
	if _, _, err := splitTopic("https://ntfy.example.com/"); err == nil {
		t.Error("a URL without topic must be rejected")
	}
}

func TestTelegram(t *testing.T) {
	rec := &recorder{}
	srv := httptest.NewServer(rec)
	defer srv.Close()

	d := FromConfig([]config.Notification{
		{Type: config.NotifyTelegram, URL: config.Secret(srv.URL + "/bottest-bot-token/sendMessage"), ChatID: "-100123", On: []string{config.EventUpdateAvailable}},
	}, nil)
	e := event()
	e.Title = "Sonarr <beta>: update"
	if err := d.Dispatch(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	e.ReleaseURL = ""
	if err := d.Dispatch(context.Background(), e); err != nil {
		t.Fatal(err)
	}

	msg := rec.got[0]
	want := "<b>Sonarr &lt;beta&gt;: update</b>\n4.0.9 → 4.0.10\n<a href=\"https://github.com/x/releases/tag/v4.0.10\">Release notes</a>"
	if msg.path != "/bottest-bot-token/sendMessage" || msg.body["chat_id"] != "-100123" || msg.body["parse_mode"] != "HTML" || msg.body["text"] != want {
		t.Errorf("telegram message = %+v", msg)
	}
	if text, _ := rec.got[1].body["text"].(string); strings.Contains(text, "<a ") {
		t.Errorf("no release link expected: %q", text)
	}
}

func TestTelegramErrorShowsDescriptionNotToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`)
	}))
	defer srv.Close()

	d := FromConfig([]config.Notification{
		{Type: config.NotifyTelegram, URL: config.Secret(srv.URL + "/bottest-bot-token/sendMessage"), ChatID: "1", On: []string{config.EventUpdateAvailable}},
	}, nil)
	err := d.Dispatch(context.Background(), event())
	if err == nil {
		t.Fatal("expected an error")
	}
	msg := err.Error()
	if strings.Contains(msg, "test-bot-token") {
		t.Errorf("error leaks the bot token: %s", msg)
	}
	if !strings.Contains(msg, "notifications[0] (telegram): unexpected status 400: Bad Request: chat not found") {
		t.Errorf("error = %s", msg)
	}
}
