// Package current reads the installed version of an application (spec §5.1).
// See doc/version-detection.md.
package current

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ohler55/ojg/jp"
	"github.com/ohler55/ojg/oj"

	"github.com/Wolfx56/lum/internal/agentproto"
	"github.com/Wolfx56/lum/internal/config"
	"github.com/Wolfx56/lum/internal/redact"
	"github.com/Wolfx56/lum/internal/version"
)

// ErrAgentUnavailable is returned for current.type agent when the app's agent
// is not connected: the app is then shown with an unknown installed version.
var ErrAgentUnavailable = errors.New("installed version unavailable: no connected agent")

const maxBody = 1 << 20

// AgentRunner runs a recipe command on an agent (get_version, agenthub.Hub).
// It returns an error wrapping ErrAgentUnavailable when the agent is not connected.
type AgentRunner interface {
	RunCommand(ctx context.Context, agentID string, command []string) (agentproto.GetVersionResult, error)
}

// Reader reads installed versions.
type Reader struct {
	client *http.Client
	agents AgentRunner
}

// NewReader returns a Reader; client defaults to one with a 10s timeout.
// agents may be nil: current.type agent then returns ErrAgentUnavailable.
func NewReader(client *http.Client, agents AgentRunner) *Reader {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &Reader{client: client, agents: agents}
}

// Current returns the installed version of app. Errors never contain secrets.
func (r *Reader) Current(ctx context.Context, app config.App) (string, error) {
	switch app.Current.Type {
	case config.TypeAPI:
		return r.fromAPI(ctx, app.Current)
	case config.TypeAgent:
		return r.fromAgent(ctx, app)
	default:
		return "", fmt.Errorf("unsupported current type %q", app.Current.Type)
	}
}

func (r *Reader) fromAPI(ctx context.Context, c config.Current) (string, error) {
	target := redact.URL(c.URL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.URL, nil)
	if err != nil {
		return "", fmt.Errorf("GET %s: %w", target, redact.Error(err))
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "lum/"+version.Version)
	if c.AuthHeader != "" {
		req.Header.Set(c.AuthHeader, c.Token.Reveal())
	}

	resp, err := r.client.Do(req)
	if err != nil {
		return "", redact.Error(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		hint := ""
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			hint = ", check the token"
		}
		return "", fmt.Errorf("GET %s: unexpected status %d%s", target, resp.StatusCode, hint)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return "", fmt.Errorf("GET %s: reading response: %w", target, redact.Error(err))
	}
	data, err := oj.Parse(body)
	if err != nil {
		return "", fmt.Errorf("GET %s: response is not valid JSON", target)
	}
	expr, err := jp.ParseString(c.JSONPath)
	if err != nil {
		return "", fmt.Errorf("json_path %q: %w", c.JSONPath, err)
	}
	results := expr.Get(data)
	if len(results) == 0 {
		return "", fmt.Errorf("GET %s: json_path %s matched nothing", target, c.JSONPath)
	}
	v, ok := scalar(results[0])
	if !ok || v == "" {
		return "", fmt.Errorf("GET %s: json_path %s is not a version string", target, c.JSONPath)
	}
	return v, nil
}

// fromAgent runs current.command on the agent and extracts the version with
// output_regex, from stdout or, failing that, stderr (some tools print their
// version there).
func (r *Reader) fromAgent(ctx context.Context, app config.App) (string, error) {
	if r.agents == nil {
		return "", ErrAgentUnavailable
	}
	res, err := r.agents.RunCommand(ctx, app.Agent, app.Current.Command)
	if err != nil {
		return "", err
	}
	if res.ExitCode != 0 {
		return "", fmt.Errorf("%s: exit code %d%s", app.Current.Command[0], res.ExitCode, excerpt(res.Stderr))
	}
	re, err := regexp.Compile(app.Current.OutputRegex)
	if err != nil {
		return "", fmt.Errorf("output_regex: %w", err)
	}
	for _, out := range []string{res.Stdout, res.Stderr} {
		if m := re.FindStringSubmatch(out); m != nil {
			if v := strings.TrimSpace(m[re.SubexpIndex("version")]); v != "" {
				return v, nil
			}
		}
	}
	return "", fmt.Errorf("%s: output_regex matched nothing in the output%s", app.Current.Command[0], excerpt(res.Stdout+res.Stderr))
}

// excerpt returns the first line of out, shortened, for error messages.
func excerpt(out string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(out), "\n")
	if line == "" {
		return ""
	}
	if len(line) > 120 {
		line = line[:120] + "…"
	}
	return fmt.Sprintf(" (%q)", line)
}

func scalar(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x), true
	case int64:
		return strconv.FormatInt(x, 10), true
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64), true
	}
	return "", false
}
