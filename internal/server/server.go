// Package server builds the LUM HTTP router: health endpoint, authentication,
// REST API, Prometheus metrics and the embedded UI.
package server

import (
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/Wolfx56/lum/internal/agentproto"
	"github.com/Wolfx56/lum/internal/api"
	"github.com/Wolfx56/lum/internal/auth"
	"github.com/Wolfx56/lum/internal/httperr"
	"github.com/Wolfx56/lum/internal/service"
	"github.com/Wolfx56/lum/internal/version"
)

// Options configures the router. Auth and Service may be nil (UI-only tests).
type Options struct {
	Logger  *slog.Logger
	UI      fs.FS // nil: no UI served (dev mode, the UI runs on the Vite dev server)
	Auth    *auth.Auth
	Service *service.Service
	BaseURL string
}

// New returns the HTTP handler. The only routes without user authentication are
// /healthz, the agent endpoints (WebSocket and binaries, authenticated by the
// agent token or bootstrap secret), /install-agent.sh, the login flow (/auth/*)
// and the static UI (spec §10).
func New(opts Options) *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery(), requestLogger(opts.Logger))

	r.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok", "version": version.Version})
	})

	if opts.Service != nil {
		// Agents authenticate with their own token in the hello message (spec §10).
		hub := opts.Service.Hub()
		r.GET(agentproto.Path, gin.WrapH(hub))
		// Binaries: agent token or bootstrap secret. The install script itself
		// holds no secret and is fetched by curl (spec §8.4).
		r.GET(agentproto.DownloadPath+":file", gin.WrapF(hub.Download))
		r.GET("/install-agent.sh", gin.WrapF(hub.InstallScript(opts.BaseURL)))
	}
	if opts.Auth != nil && opts.Service != nil {
		opts.Auth.Routes(r)
		protected := r.Group("", auth.CSRF(opts.BaseURL), opts.Auth.Middleware())
		api.Register(protected.Group("/api/v1"), opts.Service, opts.Logger)
		protected.GET("/metrics", gin.WrapH(promhttp.HandlerFor(opts.Service.Metrics().Registry, promhttp.HandlerOpts{})))
	}

	r.NoRoute(noRoute(opts.UI))
	return r
}

func noRoute(ui fs.FS) gin.HandlerFunc {
	// Missing from Go's built-in table and from most /etc/mime.types.
	_ = mime.AddExtensionType(".webmanifest", "application/manifest+json")
	var files http.Handler
	if ui != nil {
		files = http.FileServerFS(ui)
	}
	return func(c *gin.Context) {
		p := c.Request.URL.Path
		if files == nil || p == "/api" || strings.HasPrefix(p, "/api/") || c.Request.Method != http.MethodGet {
			httperr.Abort(c, http.StatusNotFound, "not_found", "route not found: "+c.Request.Method+" "+p)
			return
		}
		// Serve the static file if it exists, otherwise fall back to index.html (SPA routing).
		name := strings.TrimPrefix(path.Clean(p), "/")
		if name == "" {
			name = "index.html"
		}
		if _, err := fs.Stat(ui, name); err != nil {
			c.FileFromFS("/", http.FS(ui))
			return
		}
		files.ServeHTTP(c.Writer, c.Request)
	}
}

func requestLogger(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		logger.LogAttrs(c.Request.Context(), slog.LevelDebug, "http request",
			slog.String("method", c.Request.Method),
			slog.String("path", c.Request.URL.Path),
			slog.Int("status", c.Writer.Status()),
			slog.Duration("duration", time.Since(start)),
			slog.String("remote", c.ClientIP()),
		)
	}
}
