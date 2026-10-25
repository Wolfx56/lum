package auth

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Wolfx56/lum/internal/httperr"
)

// CSRF rejects state-changing browser requests coming from another origin.
// The session cookie is SameSite=Lax (not sent on cross-site POSTs); this
// Origin check is the second layer, and also protects auth.mode none.
// Requests without Origin (curl, scripts) and Bearer requests are allowed:
// browsers always send Origin on cross-origin POST/PUT/PATCH/DELETE.
func CSRF(baseURL string) gin.HandlerFunc {
	var baseHost string
	if u, err := url.Parse(baseURL); err == nil {
		baseHost = u.Host
	}
	return func(c *gin.Context) {
		switch c.Request.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			c.Next()
			return
		}
		if strings.HasPrefix(c.GetHeader("Authorization"), "Bearer ") {
			c.Next()
			return
		}
		origin := c.GetHeader("Origin")
		if origin == "" {
			c.Next()
			return
		}
		u, err := url.Parse(origin)
		if err != nil || (u.Host != c.Request.Host && (baseHost == "" || u.Host != baseHost)) {
			httperr.Abort(c, http.StatusForbidden, "csrf", "cross-origin request refused")
			return
		}
		c.Next()
	}
}
