# Deploying the server

This page explains how to install the LUM server on a Linux host, put it behind a TLS reverse proxy, run it day to day, and back it up. Read it when you set up LUM for the first time or move it to another machine.

The LUM server is a **single static binary**: it contains the API, the web UI and the database migrations. It usually runs in a dedicated LXC container, behind a reverse proxy, and is only exposed on the internal network.

## Prerequisites

- **A Linux host or LXC container** for the server, amd64 or arm64. Debian 12 or later is a good choice; any distribution with systemd works. 1 vCPU and 256–512 MB of RAM are enough: the binary has no system dependencies (no libc, no installed SQLite).
- **The binaries**, either:
  - downloaded from [GitHub Releases](https://github.com/Wolfx56/lum/releases) (`lum-server-linux-<arch>`, `lum-agent-linux-<arch>` and a `SHA256SUMS` file), or
  - built from source, which needs Go (the version in `go.mod`), Node.js and pnpm (see [Building from source](#building-from-source)).
- **Authentication**, one of:
  - an **OIDC provider** such as Keycloak (see [authentication.md](authentication.md)), or
  - **`auth.mode: none`** in `server.yaml`: no login at all. Only acceptable on a trusted network, or for a first try.
- **A TLS reverse proxy with a publicly trusted certificate** (Let's Encrypt, for example) if you use agents: agents only connect over `wss://` and check the certificate against the system CAs. HAProxy is used in the example below; any reverse proxy that supports WebSocket works.
- **Optional:**
  - a **GitHub token** (`GITHUB_TOKEN`): without it, GitHub allows 60 API requests per hour, which is enough for a few apps checked once a day; with it, 5,000 per hour;
  - a **Proxmox API token**, for snapshots before updates (see [proxmox.md](proxmox.md)).

## Getting the binaries

### From GitHub Releases

```bash
ARCH=amd64   # or arm64
VERSION=vX.Y.Z
BASE=https://github.com/Wolfx56/lum/releases/download/$VERSION
curl -fL -O "$BASE/lum-server-linux-$ARCH" -O "$BASE/SHA256SUMS"
sha256sum --check --ignore-missing SHA256SUMS
mv lum-server-linux-$ARCH lum-server
```

Download the agent binaries (`lum-agent-linux-amd64`, `lum-agent-linux-arm64`) the same way if you use agents (see [Agent binaries](#agent-binaries)).

### Building from source

```bash
make build-linux                    # bin/linux-amd64/lum-server (web UI embedded) and lum-agent
make build-linux LINUX_ARCH=arm64   # for an ARM host
make build-agents                   # bin/agent/lum-agent-linux-amd64 and -arm64, served to agents
```

`make build` builds for the current machine (macOS on a Mac): for a Linux server, use `make build-linux`.

## File layout

| Location | Owner | Contents |
|---|---|---|
| `/opt/lum/lum-server` | `root`, 755 | the binary |
| `/opt/lum/config/` | `root:lum`, 750 | `server.yaml`, `apps.yaml` (640) and `.env` (640, secrets) |
| `/opt/lum/agent/` | `root`, 755 | agent binaries `lum-agent-linux-<arch>` (644), served to the containers |
| `/data/` | `lum:lum`, 750 | `lum.db` (state, history, audit, sessions) and `scripts/` |

The binary and the configuration belong to `root`: the service runs as the `lum` user and can read them but not change them. Only `/data` is writable by the service.

## Installing

Copy the binary and the files from `deploy/` to the server:

```bash
scp lum-server deploy/lum-server.service deploy/config/*.yaml root@<server>:/tmp/
```

Then, on the server, as root:

```bash
useradd --system --home /data --shell /usr/sbin/nologin lum
install -d -o lum -g lum -m 750 /data /data/scripts
install -d -m 755 /opt/lum
install -d -m 750 -g lum /opt/lum/config
install -m 755 /tmp/lum-server /opt/lum/lum-server

install -m 640 -g lum /tmp/server.yaml /tmp/apps.yaml /opt/lum/config/   # then edit them
install -m 640 -g lum /dev/null /opt/lum/config/.env                      # secrets, see below
install -m 644 /tmp/lum-server.service /etc/systemd/system/
systemctl daemon-reload && systemctl enable --now lum-server
journalctl -u lum-server -f     # JSON logs
```

`/opt/lum/config/.env` holds the secrets referenced by `${…}` in the YAML files (see [configuration.md](configuration.md)). Only list the ones your configuration uses:

```
OIDC_CLIENT_SECRET=…
GITHUB_TOKEN=…
SONARR_API_KEY=…
HA_WEBHOOK_URL=…
TELEGRAM_BOT_TOKEN=…
PROXMOX_TOKEN_SECRET=…
AGENT_BOOTSTRAP_SECRET=…
```

Edit the examples from `deploy/config/` before the first start:

- remove the notifications and apps you do not need, and any `${…}` you have no value for (for example the `github.token` line if you have no GitHub token): **every unresolved `${…}` blocks startup**;
- for an app definition with `update.type: script`, put the script in `/data/scripts` (example for a community-scripts container: [`deploy/scripts/vaultwarden.sh`](../deploy/scripts/vaultwarden.sh)), otherwise validation rejects the configuration;
- for a first try without an OIDC provider, set `auth.mode: none` in `server.yaml` (trusted network only).

The systemd unit runs `lum-server --config-dir /opt/lum/config --env-file /opt/lum/config/.env --data-dir /data` as the `lum` user. The file system is protected (`ProtectSystem=strict`): `/opt/lum` is read-only and only `/data` is writable.

**Upgrading the binary**:

```bash
install -m 755 /tmp/lum-server /opt/lum/lum-server && systemctl restart lum-server
```

Database migrations run at startup.

## Agent binaries

The server distributes the agent itself: `install-agent.sh` and the agents' self-update (`self_update`, see [agent.md](agent.md)) download the binary for their architecture from `/opt/lum/agent` (`--agent-dir` option).

```bash
# from the release downloads, or from bin/agent/ after make build-agents
scp lum-agent-linux-* root@<server>:/tmp/
# on the server
install -d -m 755 /opt/lum/agent
install -m 644 /tmp/lum-agent-linux-* /opt/lum/agent/
```

Replacing these files updates every connected agent, at the latest on its next heartbeat (unless `agents.self_update: off`). The server does not need a restart. Keep the server and the agents on the same release (or build them together with `make build-linux build-agents`).

The download requires the token of an enrolled agent, or the enrollment secret while `agents.enrollment: open`: the binaries are not public. Only `install-agent.sh`, which contains no secret, is served without authentication.

## Day-to-day operations

| Action | Command |
|---|---|
| Reload the configuration after editing `apps.yaml` | `systemctl reload lum-server` (or the Reload button on the System status page) |
| Restart (after changing `auth`, `listen`, `base_url`) | `systemctl restart lum-server` |
| Follow the logs | `journalctl -u lum-server -f` |
| Check health | `curl http://127.0.0.1:8080/healthz` |

An invalid reload breaks nothing: the previous configuration stays active, and the error shows in the logs and on the System status page.

## Reverse proxy (HAProxy example)

LUM listens on plain HTTP (`server.listen`); the reverse proxy terminates TLS. The following is an **example** HAProxy configuration to adapt (host name, certificate path, and the LUM address, here the documentation address `192.0.2.10`):

```
frontend https
    bind :443 ssl crt /etc/haproxy/certs/ alpn h2,http/1.1
    http-request redirect scheme https unless { ssl_fc }
    use_backend lum if { req.hdr(host) -i lum.example.com }

backend lum
    mode http
    option httpchk GET /healthz
    http-check expect status 200
    option forwardfor                            # client IP in LUM logs (remote field)
    http-request set-header X-Forwarded-Proto https
    http-response set-header Strict-Transport-Security "max-age=31536000"
    timeout server 60s                           # "Check now" can take several seconds
    timeout tunnel 1h                            # agent WebSocket (/agent/ws) and job log SSE streams
    server lum1 192.0.2.10:8080 check inter 10s
```

Whatever proxy you use:

- **Keep the `Host` header**: HAProxy forwards the original `Host` by default; do not rewrite it. The CSRF check compares `Origin` with `Host` and with `server.base_url`.
- **Live logs (SSE)**: `GET /api/v1/jobs/{id}/logs` is a long-lived response. LUM sends a line every 15 s, which is enough with `timeout server 60s`; do not enable compression or response buffering on this path.
- **`/healthz`** needs no authentication: use it for the proxy health check.
- **`/agent/ws`** (agent WebSocket), **`/agent/download/`** (binaries) and **`/install-agent.sh`** must not be put behind the OIDC login: agents authenticate with their token or the enrollment secret, and the install script contains no secret. The agents' containers must be able to reach the proxy: if it filters by source IP, include their subnets. See [agent.md](agent.md).
- **`server.base_url`** must be the public URL (`https://lum.example.com`): it is used for the OIDC callback URL and enables the `Secure` cookie flag.
- **Direct access**: LUM should only be reachable through the proxy. Restrict port 8080 to the internal network, for example with the Proxmox firewall of the container.

For the OIDC provider side, see [authentication.md](authentication.md).

## Backup and restore

All LUM state lives in two places:

| Location | Contents | Note |
|---|---|---|
| `/opt/lum/config/` | `server.yaml`, `apps.yaml`, `.env` | `.env` contains the secrets: back it up encrypted |
| `/data/` | `lum.db` (history, state, audit, sessions), `scripts/` | `scripts/` is ideally a Git clone |

**Hot backup** (without stopping the server), with a consistent copy of the database:

```bash
sqlite3 /data/lum.db ".backup '/backup/lum-$(date +%F).db'"
tar czf /backup/lum-config-$(date +%F).tgz -C /opt/lum config
```

A plain copy of `lum.db` could be incomplete, because WAL mode also uses `lum.db-wal` and `lum.db-shm`. Use `.backup`, or stop the server before copying. A Proxmox snapshot of the server container works too.

**Restore**:

```bash
systemctl stop lum-server
cp /backup/lum-2026-10-18.db /data/lum.db && rm -f /data/lum.db-wal /data/lum.db-shm
chown lum:lum /data/lum.db
tar xzf /backup/lum-config-2026-10-18.tgz -C /opt/lum
systemctl start lum-server
```

Schema migrations run automatically at startup: a backup older than the binary is upgraded. Restored sessions stay valid until they expire; to force everyone to log in again: `sqlite3 /data/lum.db "DELETE FROM sessions"`.

Without a backup of `lum.db`, you only lose the history: the server creates an empty database and the first check rebuilds the state. The versions already notified are forgotten, so you may get one new notification per app with a pending update.

## Troubleshooting

**The server does not start.** `journalctl -u lum-server` shows the reason on a `lum-server: loading configuration from /opt/lum/config: …` line:

- `missing environment variables: VAR (path), …`: a `${VAR}` has no value in the environment or in `.env`. All missing variables are listed at once, with where each is used. Add them to `/opt/lum/config/.env`, or remove the setting that uses them (an optional `github.token`, an unused notification).
- `invalid configuration (N problems):` followed by one line per problem (`path: message`): fix each one in `server.yaml` or `apps.yaml`. A common one is an app definition that references a script missing from `scripts.dir`.
- `reading env file: …`: `.env` exists but cannot be read (check owner `root:lum` and mode 640), or a line is not `KEY=VALUE`.

**A reload has no effect.** If `systemctl reload lum-server` (or the Reload button) fails validation, LUM logs `configuration reload failed, previous configuration kept` with the error, and the System status page shows it. The previous configuration keeps running until you fix the files and reload again. Also note that `auth.*`, `server.listen` and `server.base_url` are only read at startup: changing them needs a restart.

**Apps are in `error` with `rate limit exceeded`.** The check error reads `github <path>: rate limit exceeded, resets at <time>`. Without a token, GitHub allows 60 requests per hour per IP address. Add a token (`github.token: "${GITHUB_TOKEN}"`, no scope needed for public repositories), or space out checks. The System status page shows the remaining quota and whether a token is configured. `authentication failed (401), check github.token` means the token itself is invalid or expired.

**Snapshots fail with `certificate fingerprint mismatch (got …)`.** The Proxmox node certificate changed (renewal, reinstallation) and no longer matches `proxmox.fingerprint`. Check that the new fingerprint really belongs to your node, update `fingerprint` and reload. See [proxmox.md](proxmox.md#finding-the-certificate-fingerprint).

**Agents cannot install or connect.** See the troubleshooting section of [agent.md](agent.md#installation-troubleshooting).
