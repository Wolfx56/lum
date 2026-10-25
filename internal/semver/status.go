package semver

// Status is the computed state of an application (spec §5.3).
type Status string

// Application states.
const (
	UpToDate        Status = "up_to_date"
	UpdateAvailable Status = "update_available"
	Unknown         Status = "unknown" // one of the two versions is unreadable
	Error           Status = "error"   // collection failed (set by the checker)
)

// Evaluate compares the installed and latest versions. An empty or unparsable
// version gives Unknown. An installed version newer than the latest one is UpToDate.
func Evaluate(current, latest string) Status {
	cur, err := Parse(current)
	if err != nil {
		return Unknown
	}
	lat, err := Parse(latest)
	if err != nil {
		return Unknown
	}
	if Compare(cur, lat) < 0 {
		return UpdateAvailable
	}
	return UpToDate
}
