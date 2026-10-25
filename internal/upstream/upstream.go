// Package upstream finds the latest available version of an application
// (spec §5.2). Only GitHub is supported. See doc/version-detection.md.
package upstream

import (
	"context"
	"errors"
	"regexp"
	"time"

	"github.com/Wolfx56/lum/internal/config"
)

// Release is the latest version found upstream.
type Release struct {
	Version     string    `json:"version"`      // canonical version extracted by tag_regex
	Tag         string    `json:"tag"`          // raw git tag
	Name        string    `json:"name"`         // release title (empty for a plain tag)
	URL         string    `json:"url"`          // release notes page
	Body        string    `json:"body"`         // release notes, markdown (empty for a plain tag)
	PublishedAt time.Time `json:"published_at"` // zero for a plain tag
	Prerelease  bool      `json:"prerelease"`
}

// Source resolves the latest release of an app's upstream.
type Source interface {
	Latest(ctx context.Context, u config.Upstream) (Release, error)
}

// ErrNoMatchingVersion means no release or tag matches tag_regex.
var ErrNoMatchingVersion = errors.New("no release or tag matches tag_regex")

// ErrNotFound means the repository does not exist or is not accessible.
var ErrNotFound = errors.New("repository not found")

// normalize extracts the named group "version" of re from tag.
func normalize(re *regexp.Regexp, tag string) (string, bool) {
	m := re.FindStringSubmatch(tag)
	if m == nil {
		return "", false
	}
	v := m[re.SubexpIndex("version")]
	return v, v != ""
}
