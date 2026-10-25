// Package metrics exposes Prometheus metrics on /metrics (spec §13).
// See doc/api.md.
package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"

	"github.com/Wolfx56/lum/internal/upstream"
	"github.com/Wolfx56/lum/internal/version"
)

// Source provides the values read at scrape time.
type Source interface {
	RateLimit() upstream.RateLimit
	AppStatuses() map[string]int   // status → number of configured apps
	AgentStatuses() map[string]int // status → number of enrolled agents
	ActiveJobs() int               // jobs running now
	ConnectedAgents() int          // agents with a live connection
}

// Metrics holds the LUM collectors in a dedicated registry.
type Metrics struct {
	Registry    *prometheus.Registry
	checks      *prometheus.CounterVec
	duration    prometheus.Histogram
	jobs        *prometheus.CounterVec
	jobDuration *prometheus.HistogramVec
}

// New registers the collectors.
func New(src Source) *Metrics {
	m := &Metrics{
		Registry: prometheus.NewRegistry(),
		checks: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "lum_checks_total",
			Help: "Version checks performed, by resulting status.",
		}, []string{"status"}),
		duration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "lum_check_duration_seconds",
			Help:    "Duration of the version check of one app.",
			Buckets: []float64{0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60},
		}),
		jobs: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "lum_jobs_total",
			Help: "Update jobs finished, by update type and final status.",
		}, []string{"type", "status"}),
		jobDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "lum_job_duration_seconds",
			Help:    "Duration of an update job, from its creation to its end.",
			Buckets: []float64{10, 30, 60, 120, 300, 600, 900, 1800, 3600},
		}, []string{"type"}),
	}
	m.Registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		m.checks,
		m.duration,
		m.jobs,
		m.jobDuration,
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name:        "lum_build_info",
			Help:        "Build information.",
			ConstLabels: prometheus.Labels{"version": version.Version},
		}, func() float64 { return 1 }),
		&scrapeCollector{src: src},
	)
	return m
}

// ObserveCheck records one app check.
func (m *Metrics) ObserveCheck(status string, d time.Duration) {
	m.checks.WithLabelValues(status).Inc()
	m.duration.Observe(d.Seconds())
}

// ObserveJob records the end of an update job.
func (m *Metrics) ObserveJob(typ, status string, d time.Duration) {
	m.jobs.WithLabelValues(typ, status).Inc()
	m.jobDuration.WithLabelValues(typ).Observe(d.Seconds())
}

var (
	activeJobsDesc   = prometheus.NewDesc("lum_jobs_active", "Update jobs running now.", nil, nil)
	connectedDesc    = prometheus.NewDesc("lum_agents_connected", "Agents with a live connection to the server.", nil, nil)
	appsDesc         = prometheus.NewDesc("lum_apps", "Configured apps, by current status.", []string{"status"}, nil)
	agentsDesc       = prometheus.NewDesc("lum_agents", "Enrolled agents, by status.", []string{"status"}, nil)
	agentStatuses    = []string{"online", "offline", "revoked"}
	rateRemaining    = prometheus.NewDesc("lum_github_rate_limit_remaining", "GitHub API requests remaining in the current window.", nil, nil)
	rateLimit        = prometheus.NewDesc("lum_github_rate_limit_limit", "GitHub API requests allowed per window.", nil, nil)
	rateReset        = prometheus.NewDesc("lum_github_rate_limit_reset_timestamp_seconds", "When the GitHub quota resets (Unix time).", nil, nil)
	knownStatusesFor = []string{"up_to_date", "update_available", "unknown", "error"}
)

// scrapeCollector reads app statuses and the GitHub quota at scrape time.
type scrapeCollector struct{ src Source }

func (c *scrapeCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- appsDesc
	ch <- agentsDesc
	ch <- activeJobsDesc
	ch <- connectedDesc
	ch <- rateRemaining
	ch <- rateLimit
	ch <- rateReset
}

func (c *scrapeCollector) Collect(ch chan<- prometheus.Metric) {
	statuses := c.src.AppStatuses()
	for _, s := range knownStatusesFor {
		ch <- prometheus.MustNewConstMetric(appsDesc, prometheus.GaugeValue, float64(statuses[s]), s)
	}
	agents := c.src.AgentStatuses()
	for _, s := range agentStatuses {
		ch <- prometheus.MustNewConstMetric(agentsDesc, prometheus.GaugeValue, float64(agents[s]), s)
	}
	ch <- prometheus.MustNewConstMetric(activeJobsDesc, prometheus.GaugeValue, float64(c.src.ActiveJobs()))
	ch <- prometheus.MustNewConstMetric(connectedDesc, prometheus.GaugeValue, float64(c.src.ConnectedAgents()))
	if rl := c.src.RateLimit(); rl.Known {
		ch <- prometheus.MustNewConstMetric(rateRemaining, prometheus.GaugeValue, float64(rl.Remaining))
		ch <- prometheus.MustNewConstMetric(rateLimit, prometheus.GaugeValue, float64(rl.Limit))
		ch <- prometheus.MustNewConstMetric(rateReset, prometheus.GaugeValue, float64(rl.Reset.Unix()))
	}
}
