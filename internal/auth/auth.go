// Package auth authenticates users of the UI and API (spec §10).
//
// The UI uses the BFF pattern: the server runs the OIDC authorization code
// flow (confidential client, PKCE) against Keycloak and gives the browser an
// HttpOnly session cookie; tokens never reach the browser. The API also
// accepts "Authorization: Bearer <access token>" issued by the same provider
// (scripts, Prometheus). With auth.mode none every request is anonymous.
// See doc/authentication.md.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/gin-gonic/gin"
	"golang.org/x/oauth2"

	"github.com/Wolfx56/lum/internal/config"
	"github.com/Wolfx56/lum/internal/httperr"
	"github.com/Wolfx56/lum/internal/store"
)

// Cookie names.
const (
	SessionCookie = "lum_session"
	loginCookie   = "lum_login" // binds the login state to the browser
)

const loginTTL = 10 * time.Minute

// Authentication methods reported in User.Method.
const (
	MethodNone    = "none"
	MethodSession = "session"
	MethodBearer  = "bearer"
)

// User is the authenticated identity, recorded in the audit log.
type User struct {
	Subject  string `json:"subject,omitempty"`
	Username string `json:"username"`
	Name     string `json:"name,omitempty"`
	Email    string `json:"email,omitempty"`
	Method   string `json:"method"`
}

// Anonymous is the user of auth.mode none.
var Anonymous = User{Username: "anonymous", Method: MethodNone}

const userKey = "lum.user"

// UserFrom returns the user set by Middleware.
func UserFrom(c *gin.Context) User {
	if u, ok := c.Get(userKey); ok {
		return u.(User)
	}
	return User{}
}

// Options configures Auth.
type Options struct {
	Config  config.Auth
	BaseURL string // public URL: callback = BaseURL + /auth/callback
	Store   *store.Store
	Logger  *slog.Logger
	Client  *http.Client // for the OIDC provider; nil = default
	Now     func() time.Time
}

// Auth implements the login flow and the request middleware.
type Auth struct {
	opts Options

	mu       sync.Mutex
	provider *oidcProvider // discovered lazily, so the server starts even if Keycloak is down
	pending  map[string]pendingLogin
}

type oidcProvider struct {
	oauth2        oauth2.Config
	idVerifier    *oidc.IDTokenVerifier
	tokenVerifier *oidc.IDTokenVerifier // access tokens (aud must contain client_id)
	endSession    string
}

type pendingLogin struct {
	nonce    string
	verifier string
	redirect string
	expires  time.Time
}

// New returns an Auth.
func New(opts Options) *Auth {
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Client == nil {
		opts.Client = &http.Client{Timeout: 10 * time.Second}
	}
	opts.BaseURL = strings.TrimRight(opts.BaseURL, "/")
	return &Auth{opts: opts, pending: map[string]pendingLogin{}}
}

// Enabled reports whether authentication is required (auth.mode oidc).
func (a *Auth) Enabled() bool { return a.opts.Config.Mode == config.AuthOIDC }

// getProvider discovers the provider on first use and caches it. The discovery
// context must outlive the request: go-oidc keeps it to refresh the JWKS keys.
func (a *Auth) getProvider() (*oidcProvider, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.provider != nil {
		return a.provider, nil
	}
	cfg := a.opts.Config.OIDC
	p, err := oidc.NewProvider(oidc.ClientContext(context.Background(), a.opts.Client), cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("OIDC discovery of %s: %w", cfg.Issuer, err)
	}
	var meta struct {
		EndSession string `json:"end_session_endpoint"`
	}
	_ = p.Claims(&meta)
	verifierCfg := &oidc.Config{ClientID: cfg.ClientID, Now: a.opts.Now}
	a.provider = &oidcProvider{
		oauth2: oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret.Reveal(),
			Endpoint:     p.Endpoint(),
			RedirectURL:  a.opts.BaseURL + "/auth/callback",
			Scopes:       []string{oidc.ScopeOpenID, "profile", "email"},
		},
		idVerifier:    p.Verifier(verifierCfg),
		tokenVerifier: p.Verifier(verifierCfg),
		endSession:    meta.EndSession,
	}
	return a.provider, nil
}

// Routes registers /auth/login, /auth/callback and /auth/logout.
func (a *Auth) Routes(r gin.IRouter) {
	g := r.Group("/auth")
	g.GET("/login", a.login)
	g.GET("/callback", a.callback)
	g.POST("/logout", CSRF(a.opts.BaseURL), a.logout)
}

func (a *Auth) login(c *gin.Context) {
	redirect := safeRedirect(c.Query("redirect"))
	if !a.Enabled() {
		c.Redirect(http.StatusFound, redirect)
		return
	}
	p, err := a.getProvider()
	if err != nil {
		a.opts.Logger.Error("login unavailable", "error", err.Error())
		authPage(c, http.StatusBadGateway, "The identity provider is unreachable. Try again later.")
		return
	}
	state, nonce, verifier := randomToken(), randomToken(), oauth2.GenerateVerifier()

	a.mu.Lock()
	now := a.opts.Now()
	for k, v := range a.pending { // drop abandoned logins
		if now.After(v.expires) {
			delete(a.pending, k)
		}
	}
	a.pending[state] = pendingLogin{nonce: nonce, verifier: verifier, redirect: redirect, expires: now.Add(loginTTL)}
	a.mu.Unlock()

	a.setCookie(c, loginCookie, state, int(loginTTL.Seconds()), "/auth")
	c.Redirect(http.StatusFound, p.oauth2.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)))
}

func (a *Auth) callback(c *gin.Context) {
	if !a.Enabled() {
		c.Redirect(http.StatusFound, "/")
		return
	}
	if e := c.Query("error"); e != "" {
		a.opts.Logger.Warn("login refused by the identity provider", "error", e, "description", c.Query("error_description"))
		authPage(c, http.StatusUnauthorized, "Login refused by the identity provider.")
		return
	}
	state := c.Query("state")
	cookieState, _ := c.Cookie(loginCookie)
	a.setCookie(c, loginCookie, "", -1, "/auth")

	a.mu.Lock()
	pl, ok := a.pending[state]
	delete(a.pending, state)
	a.mu.Unlock()
	if state == "" || !ok || cookieState != state || a.opts.Now().After(pl.expires) {
		authPage(c, http.StatusBadRequest, "Login session invalid or expired. Please log in again.")
		return
	}

	ctx := oidc.ClientContext(c.Request.Context(), a.opts.Client)
	p, err := a.getProvider()
	if err != nil {
		authPage(c, http.StatusBadGateway, "The identity provider is unreachable.")
		return
	}
	tok, err := p.oauth2.Exchange(ctx, c.Query("code"), oauth2.VerifierOption(pl.verifier))
	if err != nil {
		a.opts.Logger.Warn("OIDC code exchange failed", "error", err.Error())
		authPage(c, http.StatusUnauthorized, "Login failed.")
		return
	}
	rawID, _ := tok.Extra("id_token").(string)
	idToken, err := p.idVerifier.Verify(ctx, rawID)
	if err != nil || idToken.Nonce != pl.nonce {
		a.opts.Logger.Warn("invalid ID token", "error", fmt.Sprint(err))
		authPage(c, http.StatusUnauthorized, "Login failed: invalid token.")
		return
	}
	user, err := userFromToken(idToken, MethodSession)
	if err != nil {
		authPage(c, http.StatusUnauthorized, "Login failed: incomplete identity.")
		return
	}

	sid := randomToken()
	now := a.opts.Now()
	if err := a.opts.Store.CreateSession(c.Request.Context(), store.Session{
		IDHash: hashID(sid), Subject: user.Subject, Username: user.Username, Name: user.Name, Email: user.Email,
		CreatedAt: now, ExpiresAt: now.Add(a.opts.Config.SessionTTL),
	}); err != nil {
		a.opts.Logger.Error("creating session failed", "error", err.Error())
		authPage(c, http.StatusInternalServerError, "Internal error.")
		return
	}
	a.setCookie(c, SessionCookie, sid, int(a.opts.Config.SessionTTL.Seconds()), "/")
	a.opts.Logger.Info("user logged in", "user", user.Username)
	c.Redirect(http.StatusFound, pl.redirect)
}

// logout deletes the session and returns where the browser should go next
// (the provider's end-session endpoint, to close the Keycloak session too).
func (a *Auth) logout(c *gin.Context) {
	if sid, err := c.Cookie(SessionCookie); err == nil && sid != "" {
		if err := a.opts.Store.DeleteSession(c.Request.Context(), hashID(sid)); err != nil {
			a.opts.Logger.Error("deleting session failed", "error", err.Error())
		}
	}
	a.setCookie(c, SessionCookie, "", -1, "/")

	redirect := "/"
	if a.Enabled() {
		if p, err := a.getProvider(); err == nil && p.endSession != "" {
			q := url.Values{"client_id": {a.opts.Config.OIDC.ClientID}, "post_logout_redirect_uri": {a.opts.BaseURL + "/"}}
			redirect = p.endSession + "?" + q.Encode()
		}
	}
	c.JSON(http.StatusOK, gin.H{"redirect": redirect})
}

// Middleware requires an authenticated user and stores it in the context.
func (a *Auth) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !a.Enabled() {
			c.Set(userKey, Anonymous)
			c.Next()
			return
		}
		user, err := a.authenticate(c)
		if err != nil {
			httperr.Abort(c, http.StatusUnauthorized, "unauthenticated", err.Error())
			return
		}
		c.Set(userKey, user)
		c.Next()
	}
}

var errNoCredentials = errors.New("authentication required")

func (a *Auth) authenticate(c *gin.Context) (User, error) {
	if h := c.GetHeader("Authorization"); h != "" {
		raw, ok := strings.CutPrefix(h, "Bearer ")
		if !ok {
			return User{}, errors.New("unsupported Authorization scheme (expected Bearer)")
		}
		ctx := oidc.ClientContext(c.Request.Context(), a.opts.Client)
		p, err := a.getProvider()
		if err != nil {
			a.opts.Logger.Error("bearer verification unavailable", "error", err.Error())
			return User{}, errors.New("identity provider unavailable")
		}
		tok, err := p.tokenVerifier.Verify(ctx, raw)
		if err != nil {
			return User{}, errors.New("invalid bearer token")
		}
		return userFromToken(tok, MethodBearer)
	}
	sid, err := c.Cookie(SessionCookie)
	if err != nil || sid == "" {
		return User{}, errNoCredentials
	}
	sess, err := a.opts.Store.ValidSession(c.Request.Context(), hashID(sid), a.opts.Now())
	if err != nil {
		return User{}, errors.New("session expired, log in again")
	}
	return User{Subject: sess.Subject, Username: sess.Username, Name: sess.Name, Email: sess.Email, Method: MethodSession}, nil
}

func userFromToken(tok *oidc.IDToken, method string) (User, error) {
	var claims struct {
		Username string `json:"preferred_username"`
		Name     string `json:"name"`
		Email    string `json:"email"`
		ClientID string `json:"client_id"` // service accounts (client credentials)
	}
	if err := tok.Claims(&claims); err != nil {
		return User{}, err
	}
	u := User{Subject: tok.Subject, Username: claims.Username, Name: claims.Name, Email: claims.Email, Method: method}
	if u.Username == "" {
		u.Username = cmpOr(claims.Email, claims.ClientID, tok.Subject)
	}
	if u.Subject == "" {
		return User{}, errors.New("token without subject")
	}
	return u, nil
}

func cmpOr(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func (a *Auth) setCookie(c *gin.Context, name, value string, maxAge int, path string) {
	secure := strings.HasPrefix(a.opts.BaseURL, "https://")
	http.SetCookie(c.Writer, &http.Cookie{
		Name: name, Value: value, Path: path, MaxAge: maxAge,
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode,
	})
}

// safeRedirect only allows local absolute paths ("/apps/x"), never "//host" or URLs.
func safeRedirect(p string) string {
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.HasPrefix(p, "/\\") {
		return "/"
	}
	return p
}

func randomToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func hashID(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:])
}

// authPage renders a minimal error page for the browser login flow.
func authPage(c *gin.Context, status int, message string) {
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(status, `<!doctype html><html lang="en"><meta charset="utf-8"><title>LUM — login</title>`+
		`<body style="font-family:system-ui;max-width:32rem;margin:4rem auto;padding:0 16px">`+
		`<h1>Login</h1><p>%s</p><p><a href="/auth/login">Log in</a></p></body></html>`, message)
}
