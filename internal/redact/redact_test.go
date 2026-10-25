package redact

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestURL(t *testing.T) {
	cases := map[string]string{
		"http://192.0.2.30:8181/api/v2?cmd=get_tautulli_info&apikey=test-secret": "http://192.0.2.30:8181/api/v2?apikey=****&cmd=get_tautulli_info",
		"https://user:test-secret@host/path":                                     "https://user:****@host/path",
		"https://host/hook?access_token=test-secret":                             "https://host/hook?access_token=****",
		"http://192.0.2.15:8989/api/v3/system/status":                            "http://192.0.2.15:8989/api/v3/system/status",
		"https://host/?page=2":                                                   "https://host/?page=2",
	}
	for in, want := range cases {
		if got := URL(in); got != want {
			t.Errorf("URL(%q) = %q, want %q", in, got, want)
		}
	}
	if got := URL("http://[::1"); got != mask {
		t.Errorf("unparsable URL must be fully masked, got %q", got)
	}
}

func TestErrorRewritesHTTPClientErrors(t *testing.T) {
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://127.0.0.1:1/api?apikey=test-secret", nil)
	_, err := http.DefaultClient.Do(req)
	if err == nil {
		t.Fatal("expected a connection error")
	}
	msg := Error(err).Error()
	if strings.Contains(msg, "test-secret") || !strings.Contains(msg, "apikey=****") {
		t.Errorf("error not redacted: %s", msg)
	}
}
