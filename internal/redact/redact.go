// Package redact removes secrets from URLs and errors before they reach logs,
// the UI or API responses (spec §10).
package redact

import (
	"errors"
	"net/url"
	"strings"
)

const mask = "****"

// sensitiveParams are substrings of query parameter names whose value is masked.
var sensitiveParams = []string{"key", "token", "secret", "pass", "auth", "sig", "credential"}

// URL masks the userinfo password and the values of sensitive query parameters
// (apikey, token, …). An unparsable string is fully masked.
func URL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return mask
	}
	if u.User != nil {
		if _, has := u.User.Password(); has {
			u.User = url.UserPassword(u.User.Username(), mask)
		}
	}
	if u.RawQuery != "" {
		q := u.Query()
		for name, values := range q {
			if isSensitive(name) {
				for i := range values {
					values[i] = mask
				}
			}
		}
		u.RawQuery = q.Encode()
	}
	// Keep the mask readable instead of percent-encoded.
	return strings.ReplaceAll(u.String(), url.QueryEscape(mask), mask)
}

func isSensitive(param string) bool {
	p := strings.ToLower(param)
	for _, s := range sensitiveParams {
		if strings.Contains(p, s) {
			return true
		}
	}
	return false
}

// Error rewrites the URL of a *url.Error (as returned by http.Client) so the
// error message does not leak query secrets. Other errors are returned as is.
func Error(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		ue.URL = URL(ue.URL)
	}
	return err
}
