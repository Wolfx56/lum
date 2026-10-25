// Package semver compares application versions using extended SemVer (spec §5.3):
// 3 or 4 numeric segments (a missing segment counts as 0), an optional leading
// "v", an optional -prerelease (SemVer precedence rules) and an ignored +build.
// See doc/version-detection.md.
package semver

import (
	"fmt"
	"strconv"
	"strings"
)

// Version is a parsed version.
type Version struct {
	Segments   [4]uint64
	Prerelease []string // dot-separated identifiers; empty for a release
	raw        string
}

// String returns the version as it was parsed.
func (v Version) String() string { return v.raw }

// IsPrerelease reports whether the version has a -prerelease suffix.
func (v Version) IsPrerelease() bool { return len(v.Prerelease) > 0 }

// Parse parses "1.2.3", "v4.0.1.2", "1.2.3-beta.1" or "1.2.3+build".
func Parse(s string) (Version, error) {
	raw := strings.TrimSpace(s)
	rest := strings.TrimPrefix(strings.TrimPrefix(raw, "v"), "V")
	if i := strings.IndexByte(rest, '+'); i >= 0 {
		rest = rest[:i]
	}
	core, pre, hasPre := strings.Cut(rest, "-")

	v := Version{raw: raw}
	parts := strings.Split(core, ".")
	if len(parts) < 3 || len(parts) > 4 {
		return Version{}, fmt.Errorf("invalid version %q: expected 3 or 4 numeric segments", s)
	}
	for i, p := range parts {
		n, err := strconv.ParseUint(p, 10, 64)
		if err != nil || p == "" {
			return Version{}, fmt.Errorf("invalid version %q: segment %q is not a number", s, p)
		}
		v.Segments[i] = n
	}
	if hasPre {
		if pre == "" {
			return Version{}, fmt.Errorf("invalid version %q: empty prerelease", s)
		}
		v.Prerelease = strings.Split(pre, ".")
		for _, id := range v.Prerelease {
			if id == "" {
				return Version{}, fmt.Errorf("invalid version %q: empty prerelease identifier", s)
			}
		}
	}
	return v, nil
}

// Compare returns -1, 0 or +1. A prerelease is lower than the same release.
func Compare(a, b Version) int {
	for i := range a.Segments {
		if c := cmpUint(a.Segments[i], b.Segments[i]); c != 0 {
			return c
		}
	}
	switch {
	case !a.IsPrerelease() && !b.IsPrerelease():
		return 0
	case !a.IsPrerelease():
		return 1
	case !b.IsPrerelease():
		return -1
	}
	for i := 0; i < len(a.Prerelease) && i < len(b.Prerelease); i++ {
		if c := comparePrereleaseID(a.Prerelease[i], b.Prerelease[i]); c != 0 {
			return c
		}
	}
	return cmpInt(len(a.Prerelease), len(b.Prerelease))
}

// comparePrereleaseID: numeric identifiers compare numerically and are lower than alphanumeric ones.
func comparePrereleaseID(a, b string) int {
	na, errA := strconv.ParseUint(a, 10, 64)
	nb, errB := strconv.ParseUint(b, 10, 64)
	switch {
	case errA == nil && errB == nil:
		return cmpUint(na, nb)
	case errA == nil:
		return -1
	case errB == nil:
		return 1
	}
	return strings.Compare(a, b)
}

func cmpUint(a, b uint64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func cmpInt(a, b int) int { return cmpUint(uint64(a), uint64(b)) }
