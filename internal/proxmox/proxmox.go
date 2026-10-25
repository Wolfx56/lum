// Package proxmox drives LXC snapshots through the Proxmox VE REST API with an
// API token (instead of the SSH + pct wrapper of the spec, DECISIONS.md D16).
// See doc/proxmox.md.
package proxmox

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Wolfx56/lum/internal/config"
	"github.com/Wolfx56/lum/internal/version"
)

// ErrNotFound is returned when the container is not visible with the token
// (absent, or the token lacks VM.Audit on it).
var ErrNotFound = errors.New("container not found")

// taskPoll is the interval between two task status reads.
var taskPoll = time.Second

// Snapshot is one snapshot of a container.
type Snapshot struct {
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Time        time.Time `json:"time"`
}

// Client is a Proxmox VE API client. It is safe for concurrent use.
type Client struct {
	base   string // https://host:8006/api2/json
	auth   string // PVEAPIToken=user@realm!name=secret
	client *http.Client
}

// New returns a client for cfg (cfg.Configured() must be true).
func New(cfg config.Proxmox) *Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if cfg.Fingerprint != "" {
		want := strings.ToLower(strings.ReplaceAll(cfg.Fingerprint, ":", ""))
		// The node certificate is usually self-signed: the chain is not
		// verified, the pinned fingerprint of the leaf certificate is.
		transport.TLSClientConfig = &tls.Config{
			InsecureSkipVerify: true, //nolint:gosec // replaced by VerifyPeerCertificate (fingerprint pinning)
			VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
				if len(raw) == 0 {
					return errors.New("proxmox: no server certificate")
				}
				sum := sha256.Sum256(raw[0])
				if got := hex.EncodeToString(sum[:]); got != want {
					return fmt.Errorf("proxmox: certificate fingerprint mismatch (got %s)", formatFingerprint(got))
				}
				return nil
			},
		}
	}
	return &Client{
		base:   strings.TrimRight(cfg.URL, "/") + "/api2/json",
		auth:   "PVEAPIToken=" + cfg.TokenID + "=" + cfg.TokenSecret.Reveal(),
		client: &http.Client{Timeout: 30 * time.Second, Transport: transport},
	}
}

func formatFingerprint(hexSum string) string {
	var b strings.Builder
	for i := 0; i < len(hexSum); i += 2 {
		if i > 0 {
			b.WriteByte(':')
		}
		b.WriteString(strings.ToUpper(hexSum[i : i+2]))
	}
	return b.String()
}

// apiError is a non-2xx answer of the API.
type apiError struct {
	status  int
	message string
}

func (e *apiError) Error() string {
	if e.message == "" {
		return fmt.Sprintf("proxmox API: status %d", e.status)
	}
	return fmt.Sprintf("proxmox API: status %d: %s", e.status, e.message)
}

// do calls the API and decodes its "data" field into out (may be nil).
// Errors never contain the token.
func (c *Client) do(ctx context.Context, method, path string, form url.Values, out any) error {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", c.auth)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "lum/"+version.Version)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := c.client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			return fmt.Errorf("proxmox API %s %s: %w", method, path, ue.Err)
		}
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &apiError{status: resp.StatusCode, message: errorMessage(resp.Status, data)}
	}
	if out == nil {
		return nil
	}
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return fmt.Errorf("proxmox API %s %s: invalid JSON answer", method, path)
	}
	return json.Unmarshal(envelope.Data, out)
}

// errorMessage extracts the reason of a refusal: Proxmox puts it in the HTTP
// status text and, for parameter errors, in "errors".
func errorMessage(status string, body []byte) string {
	msg := status
	if _, text, ok := strings.Cut(status, " "); ok {
		msg = text
	}
	var parsed struct {
		Errors map[string]string `json:"errors"`
	}
	if json.Unmarshal(body, &parsed) == nil && len(parsed.Errors) > 0 {
		var parts []string
		for k, v := range parsed.Errors {
			parts = append(parts, k+": "+strings.TrimSpace(v))
		}
		msg += " (" + strings.Join(parts, "; ") + ")"
	}
	return msg
}

// Version returns the Proxmox VE version (connectivity and token check).
func (c *Client) Version(ctx context.Context) (string, error) {
	var v struct {
		Version string `json:"version"`
	}
	if err := c.do(ctx, http.MethodGet, "/version", nil, &v); err != nil {
		return "", err
	}
	return v.Version, nil
}

// Nodes returns the node hosting each container visible with the token.
func (c *Client) Nodes(ctx context.Context) (map[int]string, error) {
	var resources []struct {
		VMID int    `json:"vmid"`
		Node string `json:"node"`
		Type string `json:"type"`
	}
	if err := c.do(ctx, http.MethodGet, "/cluster/resources?type=vm", nil, &resources); err != nil {
		return nil, err
	}
	out := map[int]string{}
	for _, r := range resources {
		if r.Type == "lxc" {
			out[r.VMID] = r.Node
		}
	}
	return out, nil
}

// node finds the node of a container: resolved at every call, so a container
// migrated to another node of the cluster is still found.
func (c *Client) node(ctx context.Context, vmid int) (string, error) {
	nodes, err := c.Nodes(ctx)
	if err != nil {
		return "", err
	}
	node, ok := nodes[vmid]
	if !ok {
		return "", fmt.Errorf("%w: LXC %d (check that it exists and that the token has VM.Audit on it)", ErrNotFound, vmid)
	}
	return node, nil
}

func lxcPath(node string, vmid int) string {
	return "/nodes/" + url.PathEscape(node) + "/lxc/" + strconv.Itoa(vmid)
}

// Snapshot creates a snapshot of the container and waits for its completion.
func (c *Client) Snapshot(ctx context.Context, vmid int, name, description string) error {
	node, err := c.node(ctx, vmid)
	if err != nil {
		return err
	}
	var upid string
	form := url.Values{"snapname": {name}, "description": {description}}
	if err := c.do(ctx, http.MethodPost, lxcPath(node, vmid)+"/snapshot", form, &upid); err != nil {
		return fmt.Errorf("snapshot %s of LXC %d: %w", name, vmid, err)
	}
	return c.wait(ctx, node, upid, "snapshot "+name)
}

// DeleteSnapshot deletes a snapshot and waits for its completion.
func (c *Client) DeleteSnapshot(ctx context.Context, vmid int, name string) error {
	node, err := c.node(ctx, vmid)
	if err != nil {
		return err
	}
	var upid string
	if err := c.do(ctx, http.MethodDelete, lxcPath(node, vmid)+"/snapshot/"+url.PathEscape(name), nil, &upid); err != nil {
		return fmt.Errorf("deleting snapshot %s of LXC %d: %w", name, vmid, err)
	}
	return c.wait(ctx, node, upid, "delete snapshot "+name)
}

// Rollback restores a snapshot. Proxmox stops the container to roll it back;
// it is started again if it was running before.
func (c *Client) Rollback(ctx context.Context, vmid int, name string) error {
	node, err := c.node(ctx, vmid)
	if err != nil {
		return err
	}
	running, err := c.running(ctx, node, vmid)
	if err != nil {
		return err
	}
	var upid string
	if err := c.do(ctx, http.MethodPost, lxcPath(node, vmid)+"/snapshot/"+url.PathEscape(name)+"/rollback", url.Values{}, &upid); err != nil {
		return fmt.Errorf("rollback of LXC %d to %s: %w", vmid, name, err)
	}
	if err := c.wait(ctx, node, upid, "rollback to "+name); err != nil {
		return err
	}
	if !running {
		return nil
	}
	now, err := c.running(ctx, node, vmid)
	if err != nil || now {
		return err
	}
	if err := c.do(ctx, http.MethodPost, lxcPath(node, vmid)+"/status/start", url.Values{}, &upid); err != nil {
		return fmt.Errorf("starting LXC %d after rollback: %w", vmid, err)
	}
	return c.wait(ctx, node, upid, "start after rollback")
}

func (c *Client) running(ctx context.Context, node string, vmid int) (bool, error) {
	var st struct {
		Status string `json:"status"`
	}
	if err := c.do(ctx, http.MethodGet, lxcPath(node, vmid)+"/status/current", nil, &st); err != nil {
		return false, fmt.Errorf("status of LXC %d: %w", vmid, err)
	}
	return st.Status == "running", nil
}

// Resources are the CPU cores and memory (MiB) of a container. Cores 0: no
// limit (all the cores of the host).
type Resources struct {
	Cores  int
	Memory int
}

// Resources reads the CPU cores and memory of a container (token: VM.Audit).
func (c *Client) Resources(ctx context.Context, vmid int) (Resources, error) {
	node, err := c.node(ctx, vmid)
	if err != nil {
		return Resources{}, err
	}
	var conf struct {
		Cores  int  `json:"cores"`
		Memory *int `json:"memory"`
	}
	if err := c.do(ctx, http.MethodGet, lxcPath(node, vmid)+"/config", nil, &conf); err != nil {
		return Resources{}, fmt.Errorf("configuration of LXC %d: %w", vmid, err)
	}
	r := Resources{Cores: conf.Cores, Memory: 512} // Proxmox omits a memory left at its default
	if conf.Memory != nil {
		r.Memory = *conf.Memory
	}
	return r, nil
}

// SetResources changes the non-zero fields of r, live on a running container
// (token: VM.Config.CPU and VM.Config.Memory).
func (c *Client) SetResources(ctx context.Context, vmid int, r Resources) error {
	node, err := c.node(ctx, vmid)
	if err != nil {
		return err
	}
	form := url.Values{}
	if r.Cores > 0 {
		form.Set("cores", strconv.Itoa(r.Cores))
	}
	if r.Memory > 0 {
		form.Set("memory", strconv.Itoa(r.Memory))
	}
	if len(form) == 0 {
		return nil
	}
	if err := c.do(ctx, http.MethodPut, lxcPath(node, vmid)+"/config", form, nil); err != nil {
		return fmt.Errorf("resources of LXC %d: %w", vmid, err)
	}
	return nil
}

// Snapshots lists the snapshots of a container (without the "current" pseudo-entry).
func (c *Client) Snapshots(ctx context.Context, vmid int) ([]Snapshot, error) {
	node, err := c.node(ctx, vmid)
	if err != nil {
		return nil, err
	}
	var raw []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		SnapTime    int64  `json:"snaptime"`
	}
	if err := c.do(ctx, http.MethodGet, lxcPath(node, vmid)+"/snapshot", nil, &raw); err != nil {
		return nil, fmt.Errorf("snapshots of LXC %d: %w", vmid, err)
	}
	out := make([]Snapshot, 0, len(raw))
	for _, s := range raw {
		if s.Name == "current" {
			continue
		}
		out = append(out, Snapshot{Name: s.Name, Description: s.Description, Time: time.Unix(s.SnapTime, 0)})
	}
	return out, nil
}

// wait polls a task (UPID) until it stops; its exit status must be OK.
func (c *Client) wait(ctx context.Context, node, upid, what string) error {
	path := "/nodes/" + url.PathEscape(node) + "/tasks/" + url.PathEscape(upid) + "/status"
	for {
		var st struct {
			Status     string `json:"status"`
			ExitStatus string `json:"exitstatus"`
		}
		if err := c.do(ctx, http.MethodGet, path, nil, &st); err != nil {
			return fmt.Errorf("%s: reading task status: %w", what, err)
		}
		if st.Status == "stopped" {
			if st.ExitStatus != "OK" {
				return fmt.Errorf("%s: proxmox task failed: %s", what, st.ExitStatus)
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s: %w", what, ctx.Err())
		case <-time.After(taskPoll):
		}
	}
}
