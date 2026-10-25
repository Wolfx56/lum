# Authentication

This page explains how users and tools log in to LUM, and how to configure the OIDC provider. Read it before exposing LUM to anyone: LUM can trigger updates and snapshot rollbacks in your containers, so access to it must be controlled.

LUM effectively has root access to the containers and to the Proxmox host, so the web UI and the API are protected by **OIDC**. Any standard OpenID Connect provider should work (authorization code flow with PKCE, discovery document, JWKS); **Keycloak** is the one LUM is tested with, and the setup below uses it as the example. The only routes without authentication are:
- `/healthz`;
- the login flow `/auth/*`;
- the static files of the web UI;
- the agent endpoints (`/agent/ws`, `/agent/download/`, `/install-agent.sh`), which use their own authentication (see [agent.md](agent.md)).

The web UI itself shows nothing without a session, since all data goes through the API.

| `auth.mode` | Effect |
|---|---|
| `oidc` (default) | login through the OIDC provider is required |
| `none` | no authentication: everyone is `anonymous`. Only for a trusted network or development. A warning is logged at startup. |

## Two ways to authenticate

### 1. The web UI: cookie session (BFF)

The server talks to the OIDC provider, not the browser. The OIDC tokens never leave the server: the browser only holds an opaque session cookie that JavaScript cannot read.

```
Browser ──GET /auth/login──▶ LUM ──redirect──▶ OIDC provider (login)
OIDC provider ──/auth/callback?code──▶ LUM ──code exchange (client_secret + PKCE)──▶ OIDC provider
LUM: verifies the ID token, creates the session ──▶ lum_session cookie ──▶ back to the requested page
```

**`lum_session` cookie properties**:
- `HttpOnly`, `SameSite=Lax`, and `Secure` if `base_url` is https;
- it only contains a random ID; the database only stores its SHA-256 hash (`sessions` table).

**Session lifetime**: `auth.session_ttl` (12 h by default), absolute. When it expires, the web UI sends you to the login page, then back to the page you were on.

**Login flow security**:
- `state` bound to the browser by a temporary cookie, which protects against "login CSRF";
- `nonce` checked in the ID token;
- PKCE (S256) on top of the `client_secret`.

**Logout**: the logout button deletes the session on the server, then redirects to the provider's logout endpoint, to close the SSO session too.

**CSRF protection**: on top of `SameSite=Lax`, every write request (POST…) from a browser must carry an `Origin` header equal to the server host or to `base_url`. Otherwise the answer is `403 csrf`. This protection also applies in `none` mode.

Any proxy in front of LUM must therefore **keep the `Host` header**: HAProxy does by default. A proxy that rewrites `Host` causes `403 csrf` on every action button.

### 2. Scripts and tools: Bearer token

The API and `/metrics` also accept `Authorization: Bearer <access token>`, issued by the same OIDC provider (the same Keycloak realm). This is useful for `curl`, Home Assistant or Prometheus.

```bash
TOKEN=$(curl -s -d grant_type=client_credentials -d client_id=lum-automation -d client_secret=… \
  https://auth.example.com/realms/example/protocol/openid-connect/token | jq -r .access_token)
curl -H "Authorization: Bearer $TOKEN" https://lum.example.com/api/v1/apps
```

The token must have **`lum` in its audience** (`aud`). Without this check, any token from the realm would be accepted (see the Keycloak configuration below). Bearer requests are not subject to the `Origin` check, since no credential is sent automatically.

## Configuring Keycloak

1. **Client `lum`**: OpenID Connect, *Client authentication*: **On** (confidential client), *Standard flow*: On.
   - *Valid redirect URIs*: `https://lum.example.com/auth/callback`
   - *Valid post logout redirect URIs*: `https://lum.example.com/`
   - *Web origins*: `https://lum.example.com`
   - *Credentials* tab: copy the secret to `OIDC_CLIENT_SECRET` (`.env` file).
2. **Audience for Bearer tokens**: in *Client scopes* → `lum-dedicated` → *Add mapper* → *Audience*, *Included Client Audience* = `lum`, *Add to access token* = On.
3. **Automation (optional)**: a `lum-automation` client with *Service accounts* enabled, and the same audience mapper. Use it for `curl`, Prometheus, etc.

With another provider, create the equivalent: a confidential client with the callback URL above, and, for Bearer tokens, a way to put `lum` in the access token audience.

```yaml
# server.yaml
server:
  base_url: "https://lum.example.com"     # required: callback URL = base_url + /auth/callback
auth:
  mode: oidc
  oidc:
    issuer: "https://auth.example.com/realms/example"
    client_id: "lum"
    client_secret: "${OIDC_CLIENT_SECRET}"
  session_ttl: 12h
```

The provider is contacted on the first login, not at startup: LUM starts even if the provider is temporarily unreachable. Logging in then shows an error page, and you can try again later.

The `auth.*` settings and `server.base_url` are **only read at startup**: changing them needs a restart, not just a reload.

## Identity and audit

The identity used is `preferred_username`, otherwise the email, otherwise the client ID for a service account. It is recorded in the audit log (`audit_log`) for every write action: check, configuration reload, update, cancellation, rollback, agent actions (see [api.md](api.md)). `GET /api/v1/me` returns the current user.
