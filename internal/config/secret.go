package config

import (
	"encoding/json"
	"log/slog"
)

const masked = "****"

// Secret is a sensitive string (token, password, webhook URL). Every textual
// representation is masked; only Reveal returns the actual value.
type Secret string

// Reveal returns the clear value. Use it only where the secret is actually sent.
func (s Secret) Reveal() string { return string(s) }

// IsSet reports whether the secret has a value.
func (s Secret) IsSet() bool { return s != "" }

// String masks the value (fmt %v, %s, %q). An empty secret prints as "".
func (s Secret) String() string {
	if s == "" {
		return ""
	}
	return masked
}

// GoString masks the value for %#v.
func (s Secret) GoString() string { return `"` + s.String() + `"` }

// MarshalJSON masks the value in API responses.
func (s Secret) MarshalJSON() ([]byte, error) { return json.Marshal(s.String()) }

// MarshalYAML masks the value when the config is dumped.
func (s Secret) MarshalYAML() (any, error) { return s.String(), nil }

// LogValue masks the value in slog output.
func (s Secret) LogValue() slog.Value { return slog.StringValue(s.String()) }
