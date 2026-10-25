package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"

	"github.com/Wolfx56/lum/internal/config"
	"github.com/Wolfx56/lum/internal/store"
)

func init() { gin.SetMode(gin.TestMode) }

// fakeProvider is a minimal OIDC provider: discovery, JWKS, authorize (not
// called: the test follows the redirect itself) and token endpoints.
type fakeProvider struct {
	t      *testing.T
	srv    *httptest.Server
	key    *rsa.PrivateKey
	mu     sync.Mutex
	nonce  string // nonce of the pending authorization
	verify string // expected PKCE challenge
}

func newFakeProvider(t *testing.T) *fakeProvider {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := &fakeProvider{t: t, key: key}
	mux := http.NewServeMux()
	p.srv = httptest.NewServer(mux)
	t.Cleanup(p.srv.Close)

	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                p.srv.URL,
			"authorization_endpoint":                p.srv.URL + "/authorize",
			"token_endpoint":                        p.srv.URL + "/token",
			"jwks_uri":                              p.srv.URL + "/jwks",
			"end_session_endpoint":                  p.srv.URL + "/logout",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"}}})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		user, pass, _ := r.BasicAuth()
		sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		p.mu.Lock()
		challenge, nonce := p.verify, p.nonce
		p.mu.Unlock()
		if r.Form.Get("code") != "good-code" || user != "lum" || pass != "test-client-secret" ||
			base64.RawURLEncoding.EncodeToString(sum[:]) != challenge {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": p.token(t, "lum", nil), "token_type": "Bearer", "expires_in": 300,
			"id_token": p.token(t, "lum", map[string]any{"nonce": nonce}),
		})
	})
	return p
}

// token signs a JWT for the given audience with alice's claims.
func (p *fakeProvider) token(t *testing.T, aud string, extra map[string]any) string {
	t.Helper()
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: p.key}, (&jose.SignerOptions{}).WithHeader("kid", "k1"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	claims := map[string]any{
		"iss": p.srv.URL, "sub": "sub-alice", "aud": aud,
		"iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(),
		"preferred_username": "alice", "name": "Alice Martin", "email": "alice@example.com",
	}
	for k, v := range extra {
		claims[k] = v
	}
	raw, err := jwt.Signed(signer).Claims(claims).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

type harness struct {
	auth     *Auth
	srv      *httptest.Server
	provider *fakeProvider
	client   *http.Client // keeps cookies, does not follow redirects
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	p := newFakeProvider(t)
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "lum.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	h := &harness{provider: p}
	r := gin.New()
	h.srv = httptest.NewServer(r)
	t.Cleanup(h.srv.Close)

	h.auth = New(Options{
		Config: config.Auth{Mode: config.AuthOIDC, SessionTTL: time.Hour, OIDC: config.OIDC{
			Issuer: p.srv.URL, ClientID: "lum", ClientSecret: "test-client-secret",
		}},
		BaseURL: h.srv.URL,
		Store:   st,
	})
	h.auth.Routes(r)
	api := r.Group("/api", h.auth.Middleware())
	api.GET("/me", func(c *gin.Context) { c.JSON(http.StatusOK, UserFrom(c)) })

	jar, _ := cookiejar.New(nil)
	h.client = &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return h
}

// login runs the whole browser flow and returns the final redirect target.
func (h *harness) login(t *testing.T, redirect string) string {
	t.Helper()
	resp, err := h.client.Get(h.srv.URL + "/auth/login?redirect=" + url.QueryEscape(redirect))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	authURL, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || !strings.HasPrefix(authURL.String(), h.provider.srv.URL+"/authorize") {
		t.Fatalf("login must redirect to the provider, got %q", resp.Header.Get("Location"))
	}
	q := authURL.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("redirect_uri") != h.srv.URL+"/auth/callback" {
		t.Fatalf("authorize URL misses PKCE or callback: %s", authURL)
	}
	h.provider.mu.Lock()
	h.provider.nonce, h.provider.verify = q.Get("nonce"), q.Get("code_challenge")
	h.provider.mu.Unlock()

	resp, err = h.client.Get(h.srv.URL + "/auth/callback?code=good-code&state=" + url.QueryEscape(q.Get("state")))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("callback status = %d", resp.StatusCode)
	}
	return resp.Header.Get("Location")
}

func (h *harness) me(t *testing.T, bearer string) (int, User) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, h.srv.URL+"/api/me", nil)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var u User
	_ = json.NewDecoder(resp.Body).Decode(&u)
	return resp.StatusCode, u
}

func TestLoginFlowCreatesSession(t *testing.T) {
	h := newHarness(t)

	if status, _ := h.me(t, ""); status != http.StatusUnauthorized {
		t.Fatalf("before login: status = %d, want 401", status)
	}
	if got := h.login(t, "/apps/sonarr"); got != "/apps/sonarr" {
		t.Errorf("redirect after login = %q, want /apps/sonarr", got)
	}
	status, u := h.me(t, "")
	if status != http.StatusOK || u.Username != "alice" || u.Name != "Alice Martin" || u.Method != MethodSession {
		t.Fatalf("after login: status=%d user=%+v", status, u)
	}

	// The cookie is HttpOnly and the token never reaches the browser.
	srvURL, _ := url.Parse(h.srv.URL)
	cookies := h.client.Jar.Cookies(srvURL)
	if len(cookies) != 1 || cookies[0].Name != SessionCookie || strings.Count(cookies[0].Value, ".") == 2 {
		t.Errorf("unexpected cookies: %+v", cookies)
	}
}

func TestLogout(t *testing.T) {
	h := newHarness(t)
	h.login(t, "/")
	resp, err := h.client.Post(h.srv.URL+"/auth/logout", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	var body struct{ Redirect string }
	_ = json.NewDecoder(resp.Body).Decode(&body)
	resp.Body.Close()
	if !strings.HasPrefix(body.Redirect, h.provider.srv.URL+"/logout?") || !strings.Contains(body.Redirect, "client_id=lum") {
		t.Errorf("logout redirect = %q, want the provider end-session endpoint", body.Redirect)
	}
	if status, _ := h.me(t, ""); status != http.StatusUnauthorized {
		t.Errorf("after logout: status = %d, want 401", status)
	}
}

func TestCallbackRejectsForgedState(t *testing.T) {
	h := newHarness(t)
	resp, err := h.client.Get(h.srv.URL + "/auth/login")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	// A state the browser did not start (login CSRF) is refused.
	resp, err = h.client.Get(h.srv.URL + "/auth/callback?code=good-code&state=forged")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("forged state: status = %d, want 400", resp.StatusCode)
	}
}

func TestBearerTokens(t *testing.T) {
	h := newHarness(t)
	status, u := h.me(t, h.provider.token(t, "lum", nil))
	if status != http.StatusOK || u.Username != "alice" || u.Method != MethodBearer {
		t.Fatalf("valid bearer: status=%d user=%+v", status, u)
	}
	if status, _ := h.me(t, h.provider.token(t, "account", nil)); status != http.StatusUnauthorized {
		t.Errorf("token for another audience: status = %d, want 401", status)
	}
	if status, _ := h.me(t, "not-a-jwt"); status != http.StatusUnauthorized {
		t.Errorf("garbage token: status = %d, want 401", status)
	}
}

func TestSafeRedirect(t *testing.T) {
	cases := map[string]string{
		"/apps/sonarr":         "/apps/sonarr",
		"":                     "/",
		"//evil.example.com":   "/",
		"/\\evil.example.com":  "/",
		"https://evil.example": "/",
	}
	for in, want := range cases {
		if got := safeRedirect(in); got != want {
			t.Errorf("safeRedirect(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestModeNoneIsAnonymous(t *testing.T) {
	a := New(Options{Config: config.Auth{Mode: config.AuthNone}})
	r := gin.New()
	r.GET("/me", a.Middleware(), func(c *gin.Context) { c.JSON(http.StatusOK, UserFrom(c)) })
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/me", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"username":"anonymous"`) {
		t.Errorf("mode none: %d %s", w.Code, w.Body)
	}
}

func TestCSRF(t *testing.T) {
	r := gin.New()
	r.POST("/x", CSRF("https://lum.example.com"), func(c *gin.Context) { c.Status(http.StatusNoContent) })
	cases := []struct {
		origin, auth string
		want         int
	}{
		{"", "", http.StatusNoContent},                                 // curl
		{"https://lum.example.com", "", http.StatusNoContent},          // the web UI behind the proxy
		{"http://example.test", "", http.StatusNoContent},              // same host as the request
		{"https://evil.example.com", "", http.StatusForbidden},         // cross-site
		{"https://evil.example.com", "Bearer x", http.StatusNoContent}, // bearer: no ambient credentials
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodPost, "http://example.test/x", nil)
		if tc.origin != "" {
			req.Header.Set("Origin", tc.origin)
		}
		if tc.auth != "" {
			req.Header.Set("Authorization", tc.auth)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != tc.want {
			t.Errorf("origin=%q auth=%q: status %d, want %d", tc.origin, tc.auth, w.Code, tc.want)
		}
	}
}
