# Contributing to LUM

Thanks for your interest! Bug reports, documentation fixes and pull requests are welcome.
For security issues, do **not** open an issue: see [SECURITY.md](SECURITY.md).

## Workflow

1. Open an issue first for anything bigger than a small fix, so we can agree on the approach.
2. Fork, create a branch, and keep commits focused. Commit messages follow [Conventional Commits](https://www.conventionalcommits.org/) (`feat(jobs): …`, `fix(agent): …`, `docs: …`).
3. Before pushing, run `make test test-web check-web lint`. CI (`.github/workflows/ci.yml`) runs the same checks plus `govulncheck` and a full build.
4. Every user-visible change updates the matching page in `doc/` (English). A change to a contract (YAML schema, REST API, agent protocol, SQLite tables) is recorded in [DECISIONS.md](DECISIONS.md).
5. Never commit secrets, real hostnames or IPs, even in tests: use `${ENV_VAR}` placeholders, `example.com` and `192.0.2.x` addresses.

The design documents are [doc/specification.md](doc/specification.md), [doc/ROADMAP.md](doc/ROADMAP.md) and [DECISIONS.md](DECISIONS.md). Guidance for AI coding agents is in [AGENTS.md](AGENTS.md).

## Prerequisites

- Go (version from `go.mod`; currently `go 1.26.2`)
- Node.js LTS + pnpm
- golangci-lint, optional: if it is not installed, `make lint` runs a pinned version through `go run`

## Local development

```bash
make deps        # Go modules + web UI dependencies
make dev         # Go server (:8080) + Svelte web UI dev server (http://localhost:5173)
make run-agent   # in another terminal: dev agent (enrolls, reads the version of the dev vaultwarden app)
```

`make run-server` uses the dev config `dev/config` and the database `tmp/data/lum.db`, and creates `.env` from `.env.example` if it does not exist. It checks versions at startup; with `make run-fakeapp` running, the update notification (webhook) is printed in the fake Sonarr's terminal.

Useful variables: `make run-agent AGENT_ID=sonarr-lxc`, `make run-server LISTEN=:9090 CONFIG_DIR=path/to/config`.

## Make targets

| Command | Purpose |
|---|---|
| `make deps` | install Go and web UI dependencies |
| `make dev` | server + web UI dev server in parallel |
| `make run-server` | dev server (dev/config, database in tmp/data, no embedded UI) |
| `make run-web` | Svelte web UI (Vite :5173, proxies /api and /healthz to :8080) |
| `make run-agent` | dev agent (`AGENT_ID`, `SERVER_URL`; token and scripts in `tmp/agent/<AGENT_ID>`) |
| `make test` | Go tests |
| `make test-web` | web UI tests (vitest) |
| `make lint` | golangci-lint |
| `make check-web` | svelte-check (TS/Svelte types) |
| `make build` | `bin/lum-server` (embedded UI) and `bin/lum-agent`, static binaries |
| `make build-linux` | same binaries for Linux (`LINUX_ARCH=amd64` by default) in `bin/linux-amd64/` |
| `make build-agents` | `lum-agent` Linux amd64 and arm64 in `bin/agent/`, to copy into the server's `/opt/lum/agent` |
| `make clean` | removes build artifacts and dev data (`tmp/`) |
| `make run-fakeapp` | fake Sonarr on :8989 to test detection and updates (`FAKE_VERSION=…`, `FAKE_UPGRADE_TO=…`) |
| `make check-versions` | one-shot version check of the apps in `CONFIG_DIR` (dev tool) |

## Repository layout

```
cmd/server, cmd/agent   entry points (cmd/lum-check: dev tool)
internal/semver         version comparison and states
internal/upstream       latest available version (GitHub client)
internal/current        installed version (app API, or command through the agent)
internal/redact         secret masking in URLs and errors
internal/service        component wiring and configuration reload
internal/api            REST API /api/v1 (read API, write API, SSE job log stream)
internal/auth           OIDC authentication (BFF session, Bearer), CSRF
internal/metrics        Prometheus metrics
internal/httperr        uniform error format
internal/store          SQLite: migrations, check history, app state, agents, jobs, audit
internal/checker        parallel checks, persistence, notification triggering
internal/scheduler      internal cron (checks + purge)
internal/notify         ntfy, webhook and Telegram channels
internal/agentproto     server <-> agent protocol messages
internal/agenthub       server side of agents: WebSocket, enrollment, orders, script sync
internal/scripts        index of scripts.dir (SHA-256, inotify)
internal/proxmox        Proxmox API client: snapshots, rollback, resources
internal/jobs           update jobs: snapshot, API request or script, healthcheck, verification, rollback, cancellation
internal/config         loading, validation and reload of the YAML configuration
internal/server         Gin router (/healthz, /agent/ws, /auth, /api/v1, /metrics, UI + SPA fallback)
internal/agent          lum-agent: connection, heartbeat, get_version, sync_scripts, run_update, cancel_job, self_update
internal/logging        slog JSON logger
internal/version        build version (set by -ldflags)
internal/ui             embedded web UI build (embedui build tag)
web/                    SvelteKit web UI as a static SPA (pnpm)
dev/                    dev config, scripts and fake Sonarr (dev/fakeapp)
deploy/                 systemd units, install-agent.sh, example production configs and scripts
doc/                    specification, roadmap, user documentation
```

## Package notes

### Configuration package (`internal/config`)

- `Load`: read, defaults, substitution, validation.
- `Holder`: active configuration and reload.
- `Secret`: masking.
- Typed errors:
  - `*MissingEnvError`, with `Vars()`;
  - `*ValidationError`, whose `Problems` (`path`, `message`) is serializable for the API;
  - other read or parse errors name the file.
- The files in `dev/config` and `deploy/config` are validated by `TestRepoConfigs`: a change that breaks them makes `make test` fail.

### Version detection packages

| Package | Role |
|---|---|
| `internal/semver` | `Parse`, `Compare`, `Evaluate` → `Status` |
| `internal/upstream` | `Source` interface; `GitHub` (cache, rate limit via `RateLimit()`); errors `ErrNotFound`, `ErrNoMatchingVersion` |
| `internal/current` | `Reader.Current`; `agent` type via `AgentRunner` (the hub), `ErrAgentUnavailable` when the agent is not connected |
| `internal/redact` | `URL` and `Error`: cleaning of URLs and `*url.Error` errors |

`cmd/lum-check` (dev tool) and `dev/fakeapp` (fake Sonarr) are not deployed.

#### Testing detection locally

```bash
make run-fakeapp      # fake Sonarr on :8989, version 4.0.0.700 (FAKE_VERSION=… to change it)
make check-versions   # one-shot check of the dev/config apps against the real GitHub
```

```
APP          INSTALLED  LATEST       STATUS            DETAIL
sonarr       4.0.0.700  4.0.20.3014  update_available  https://github.com/Sonarr/Sonarr/releases/tag/v4.0.20.3014
vaultwarden  -          1.37.4       unknown           installed version unavailable: no connected agent

GitHub quota: 58/60 remaining, resets at 12:52:03
```

`make check-versions CONFIG_DIR=…` checks another configuration; `go run ./cmd/lum-check --app sonarr` checks a single app.

### Checks and notifications packages

| Package | Role |
|---|---|
| `internal/store` | `Open` (WAL, embedded `migrations/NNNN_*.sql`), `RecordCheck` (returns the previous state), `MarkNotified`, `AppState(s)`, `Checks`, `PurgeChecks`, `Audit` |
| `internal/checker` | `Collect` (no side effects), `CheckApp`, `CheckAll` |
| `internal/scheduler` | cron (`robfig/cron/v3`), jitter, daily purge |
| `internal/notify` | `Dispatcher` (filtering by `on`), `Ntfy`, `Webhook`, `Telegram` |

To add a migration, create `internal/store/migrations/NNNN_<name>.sql` with the next number. It is applied once, at startup, in a transaction.

#### Testing notifications locally

```bash
make run-fakeapp   # fake Sonarr (4.0.0.700) that also prints the webhooks it receives
make run-server    # checks at startup → update_available → webhook received by the fake Sonarr
```

Restarting `make run-server` sends no new notification. To start from scratch: `make clean` (removes `tmp/`, including the dev database).

### Running an agent locally

```bash
make run-server                  # dev/config: the vaultwarden app uses the test-agent agent
make run-agent                   # test-agent agent, token and scripts in tmp/agent/test-agent/
make run-agent AGENT_ID=other    # a second agent
```

In development, the agent connects to `ws://localhost:8080` with the secret from `.env` (`AGENT_BOOTSTRAP_SECRET`). The Vaultwarden app definition in `dev/config` reads a simulated version (1.30.0), so it shows as `update_available`; its update runs `dev/scripts/vaultwarden.sh`, which writes the target version to `/tmp/lum-dev-vaultwarden-version`. Delete that file to go back to 1.30.0.

### Agent protocol and code

- **Protocol** (`internal/agentproto`): JSON messages `{type, id, payload, error}`.
  - Agent → server: `hello`, `heartbeat`, `response`, `job_log`, `job_done`.
  - Server → agent: `welcome`, `get_version`, `sync_scripts`, `run_update`, `cancel_job`, `self_update`.
  - An order and its response share the same `id`; a failed response carries `error`. Response timeout: 30 s (5 min for `self_update`).
  - `run_update` is acknowledged right away; the output (`job_log`) and the end (`job_done`) follow, tied to the job by its ID, even across a reconnection.
- **Server**: `internal/agenthub` (endpoint, enrollment, orders, sync, offline detection, binaries and `install-agent.sh`) and `internal/scripts` (index + inotify).
- **Agent**: `internal/agent`.
- **Tests**: `internal/agenthub/hub_test.go` runs a real WebSocket server, real agents and a SQLite database. They cover enrollment, token reconnection, refusals, `get_version`, every sync trigger and offline detection.

### Authentication

- Code: `internal/auth`.
  - `Auth.Routes`: `/auth/login`, `/auth/callback`, `/auth/logout`.
  - `Auth.Middleware`: session or Bearer, then `auth.UserFrom(c)`.
  - `CSRF`.
- The Vite dev proxy keeps the `Host` header (`changeOrigin: false` in `web/vite.config.ts`); otherwise the CSRF check would answer `403 csrf` on every action.
- The tests (`internal/auth/auth_test.go`) use a complete fake OIDC provider: discovery, JWKS, a token endpoint that checks PKCE. They cover the full flow, logout, a forged `state`, the Bearer audience and open redirects.

### Proxmox

- LUM drives Proxmox through its REST API with an API token rather than over SSH as the specification originally planned (see `DECISIONS.md`, D16).
- `internal/proxmox`: API client (`Snapshot`, `Rollback`, `DeleteSnapshot`, `Snapshots`, `Resources`, `SetResources`, `Version`, `Nodes`). Proxmox tasks are asynchronous: each call follows the returned UPID until the task ends.
- Tests: `internal/proxmox/proxmox_test.go` simulates the API over self-signed TLS (pinning, tasks, parameter errors, container not found).

### Update scripts

- Agent: `internal/agent/jobs.go` (execution, process group, send queue that survives reconnections, line cleanup).
- Server: `internal/agenthub/jobs.go` (`EnsureScript`, `RunUpdate`, `CancelJob`) and `internal/jobs` (`running` step of `script` jobs).
- End-to-end tests (`internal/jobs/script_test.go`) with a real hub, a real agent and real bash scripts: success and variables, failure, cancellation, reconnection during the script, agent offline.

### Jobs

- `internal/jobs`: `Manager.Start` (pre-checks, creation, launch), `Cancel`, `Rollback` and `RollbackCheck` (manual rollback), `Recover` (jobs interrupted at startup), `Stop`. `Watch` wakes up the SSE stream on each line or state change of a running job.
- `internal/api/jobs.go`: job routes and SSE stream.
- Local try: `make run-fakeapp FAKE_UPGRADE_TO=4.99.0.0` (the fake Sonarr "installs" that version 5 s after the request), `make dev`, then Update on Sonarr. Without `FAKE_UPGRADE_TO`, the version does not change: useful to try cancellation or `timeout`. With `make run-agent`, the dev Vaultwarden app runs `dev/scripts/vaultwarden.sh`, which simulates 20 s of output then "installs" the target version.
- The single active job per app is enforced by the database: partial unique index on `jobs(app_id)` for the active states.
- Tests (`internal/jobs/jobs_test.go`) with a fake Sonarr and a fake Proxmox: success and retention, failing health check, automatic rollback, failing snapshot, timeout, unexpected version, refused request, concurrent job, pre-checks, server stop and restart. Manual rollback: `internal/jobs/rollback_test.go`. End to end through the API (trigger, `409`, SSE, cancellation, agent offline, audit): `internal/server/api_l2_test.go`. `script` jobs: `internal/jobs/script_test.go`.

### Deployment

- `make build-linux build-agents` builds the server and agents from the same tree, which keeps their versions aligned.
