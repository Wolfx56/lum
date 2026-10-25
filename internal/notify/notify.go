// Package notify sends events to the configured channels (ntfy, Telegram, generic
// webhook), filtered by each channel's "on" list (spec §13).
// See doc/checks-notifications.md.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/Wolfx56/lum/internal/config"
	"github.com/Wolfx56/lum/internal/version"
)

// Event is a notification payload. It is sent as is (JSON) to webhooks.
type Event struct {
	Type           string    `json:"event"` // config.Event* constant
	AppID          string    `json:"app_id"`
	AppName        string    `json:"app_name"`
	CurrentVersion string    `json:"current_version,omitempty"`
	LatestVersion  string    `json:"latest_version,omitempty"`
	ReleaseURL     string    `json:"release_url,omitempty"`
	Title          string    `json:"title"`
	Message        string    `json:"message"`
	Time           time.Time `json:"time"`
}

// Notifier delivers an event to one channel.
type Notifier interface {
	Notify(ctx context.Context, e Event) error
}

type channel struct {
	name     string // e.g. "notifications[1] (webhook)", never the URL (it is a secret)
	on       []string
	notifier Notifier
}

// Dispatcher fans an event out to the channels subscribed to its type.
type Dispatcher struct {
	channels []channel
}

// FromConfig builds a dispatcher from the notifications section. client may be nil.
func FromConfig(list []config.Notification, client *http.Client) *Dispatcher {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	d := &Dispatcher{}
	for i, n := range list {
		var notifier Notifier
		switch n.Type {
		case config.NotifyNtfy:
			notifier = &Ntfy{URL: n.URL, Client: client}
		case config.NotifyWebhook:
			notifier = &Webhook{URL: n.URL, Client: client}
		case config.NotifyTelegram:
			notifier = &Telegram{URL: n.URL, ChatID: n.ChatID, Client: client}
		default:
			continue // rejected by config validation
		}
		d.channels = append(d.channels, channel{
			name:     fmt.Sprintf("notifications[%d] (%s)", i, n.Type),
			on:       n.On,
			notifier: notifier,
		})
	}
	return d
}

// Dispatch sends e to every subscribed channel. A failing channel does not stop
// the others; the returned error joins all failures.
func (d *Dispatcher) Dispatch(ctx context.Context, e Event) error {
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	var errs []error
	for _, ch := range d.channels {
		if !slices.Contains(ch.on, e.Type) {
			continue
		}
		if err := ch.notifier.Notify(ctx, e); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", ch.name, err))
		}
	}
	return errors.Join(errs...)
}

// Ntfy publishes to an ntfy topic URL (https://ntfy.example.com/<topic>) using
// ntfy's JSON publishing, which handles UTF-8 titles.
type Ntfy struct {
	URL    config.Secret
	Client *http.Client
}

var ntfyTags = map[string][]string{
	config.EventUpdateAvailable: {"arrow_up"},
	config.EventJobSuccess:      {"white_check_mark"},
	config.EventJobFailed:       {"x"},
	config.EventAgentOffline:    {"warning"},
}

// Notify implements Notifier.
func (n *Ntfy) Notify(ctx context.Context, e Event) error {
	base, topic, err := splitTopic(n.URL.Reveal())
	if err != nil {
		return err
	}
	msg := map[string]any{"topic": topic, "title": e.Title, "message": e.Message, "tags": ntfyTags[e.Type]}
	if e.ReleaseURL != "" {
		msg["click"] = e.ReleaseURL
	}
	return postJSON(ctx, n.Client, base, msg)
}

// splitTopic turns https://host/prefix/topic?q into (https://host/prefix/?q, topic).
func splitTopic(raw string) (base, topic string, err error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", errors.New("invalid ntfy URL")
	}
	p := strings.TrimRight(u.Path, "/")
	i := strings.LastIndex(p, "/")
	if i < 0 || p[i+1:] == "" {
		return "", "", errors.New("ntfy URL must end with the topic name")
	}
	topic = p[i+1:]
	u.Path = p[:i] + "/" // the query (e.g. ntfy's ?auth=) is kept
	return u.String(), topic, nil
}

// Webhook POSTs the event as JSON (e.g. a Home Assistant webhook).
type Webhook struct {
	URL    config.Secret
	Client *http.Client
}

// Notify implements Notifier.
func (w *Webhook) Notify(ctx context.Context, e Event) error {
	return postJSON(ctx, w.Client, w.URL.Reveal(), e)
}

// Telegram sends a message through the Bot API. URL is the bot's sendMessage
// endpoint (https://api.telegram.org/bot<token>/sendMessage).
type Telegram struct {
	URL    config.Secret
	ChatID string
	Client *http.Client
}

// Notify implements Notifier.
func (t *Telegram) Notify(ctx context.Context, e Event) error {
	text := "<b>" + html.EscapeString(e.Title) + "</b>\n" + html.EscapeString(e.Message)
	if e.ReleaseURL != "" {
		text += "\n<a href=\"" + html.EscapeString(e.ReleaseURL) + "\">Release notes</a>"
	}
	return postJSON(ctx, t.Client, t.URL.Reveal(), map[string]any{
		"chat_id":              t.ChatID,
		"text":                 text,
		"parse_mode":           "HTML",
		"link_preview_options": map[string]bool{"is_disabled": true},
	})
}

// postJSON sends body and requires a 2xx answer. Errors never contain the URL.
func postJSON(ctx context.Context, client *http.Client, target string, body any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(data))
	if err != nil {
		return errors.New("invalid URL")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "lum/"+version.Version)
	resp, err := client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			return ue.Err // drop the URL: it may embed a token
		}
		return err
	}
	defer resp.Body.Close()
	answer, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		// Telegram explains its refusals ("Bad Request: chat not found"); the
		// description never contains the bot token.
		var apiErr struct {
			Description string `json:"description"`
		}
		if json.Unmarshal(answer, &apiErr) == nil && apiErr.Description != "" {
			return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, apiErr.Description)
		}
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	return nil
}
