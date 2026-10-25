package semver

import "testing"

func TestParse(t *testing.T) {
	valid := map[string][4]uint64{
		"1.2.3":            {1, 2, 3, 0},
		"v4.0.1.2":         {4, 0, 1, 2},
		"V2.15.3":          {2, 15, 3, 0},
		" 1.0.0 ":          {1, 0, 0, 0},
		"1.2.3-beta.1":     {1, 2, 3, 0},
		"1.2.3+build.5":    {1, 2, 3, 0},
		"2024.10.1":        {2024, 10, 1, 0},
		"4.0.9.2244":       {4, 0, 9, 2244},
		"1.2.3-rc.1+build": {1, 2, 3, 0},
	}
	for in, want := range valid {
		v, err := Parse(in)
		if err != nil {
			t.Errorf("Parse(%q): %v", in, err)
			continue
		}
		if v.Segments != want {
			t.Errorf("Parse(%q) = %v, want %v", in, v.Segments, want)
		}
	}
	for _, in := range []string{"", "1.2", "1.2.3.4.5", "1.x.3", "1..3", "latest", "1.2.3-", "1.2.3-a..b", "-1.2.3"} {
		if _, err := Parse(in); err == nil {
			t.Errorf("Parse(%q): expected an error", in)
		}
	}
}

func TestCompare(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.2.3", "1.2.3", 0},
		{"1.2.3", "1.2.3.0", 0}, // missing segment = 0
		{"v1.2.3", "1.2.3", 0},
		{"1.2.3", "1.2.4", -1},
		{"1.10.0", "1.9.9", 1}, // numeric, not lexicographic
		{"4.0.1.2", "4.0.1.10", -1},
		{"4.0.1.1", "4.0.1", 1},
		{"2.0.0", "1.99.99.99", 1},
		{"1.0.0-beta", "1.0.0", -1}, // prerelease < release
		{"1.0.0-alpha", "1.0.0-alpha.1", -1},
		{"1.0.0-alpha.1", "1.0.0-alpha.beta", -1}, // numeric < alphanumeric
		{"1.0.0-beta.2", "1.0.0-beta.11", -1},
		{"1.0.0-rc.1", "1.0.0-beta.11", 1},
		{"1.0.0+build.1", "1.0.0+build.2", 0}, // build metadata ignored
	}
	for _, tc := range cases {
		a, _ := Parse(tc.a)
		b, _ := Parse(tc.b)
		if got := Compare(a, b); got != tc.want {
			t.Errorf("Compare(%s, %s) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
		if got := Compare(b, a); got != -tc.want {
			t.Errorf("Compare(%s, %s) = %d, want %d (antisymmetry)", tc.b, tc.a, got, -tc.want)
		}
	}
}

func TestEvaluate(t *testing.T) {
	cases := []struct {
		current, latest string
		want            Status
	}{
		{"4.0.9.2244", "4.0.10.2544", UpdateAvailable},
		{"1.32.0", "1.32.0", UpToDate},
		{"1.33.0", "1.32.0", UpToDate}, // installed ahead of upstream
		{"", "1.2.3", Unknown},
		{"1.2.3", "", Unknown},
		{"nightly", "1.2.3", Unknown},
	}
	for _, tc := range cases {
		if got := Evaluate(tc.current, tc.latest); got != tc.want {
			t.Errorf("Evaluate(%q, %q) = %s, want %s", tc.current, tc.latest, got, tc.want)
		}
	}
}
