# AGENTS.md

Guidance for AI coding agents working in this repository.

## Project status

LXC Update Manager (LUM) is being built milestone by milestone (see `doc/ROADMAP.md`). The source of truth is [`doc/specification.md`](doc/specification.md). Read the relevant sections before you implement anything. If something is ambiguous, follow what the spec says. If the spec doesn't cover it, the choice is open, but document it.

Hard rules from the spec (§14.2):
- Follow the contracts exactly: YAML schema (§4), agent orders (§8.2), REST API (§11), SQLite tables (§12).
- Record any necessary deviation in `DECISIONS.md` at the repo root.
- Implement milestones **in order (M1 → M8) without anticipating the next one**. Each milestone must end with passing `go test ./...`, a clean `golangci-lint run`, and an updated `README.md`.
- Never hardcode secrets, including in tests (use `${ENV_VAR}` substitution / test env).
- **Every new feature ships with user-facing documentation** of how it works, in English, in `doc/` (one file per area, e.g. `doc/configuration.md`), linked from `README.md`. Update it whenever the behavior changes.
- Track milestone progress in `doc/ROADMAP.md` (check off items as they are done; record decisions taken on its open points).

## Repository

- Public on GitHub (MIT). Go module path: `github.com/Wolfx56/lum`.
- Contributor guide (setup, workflow, conventions): `CONTRIBUTING.md`.
- CI runs on GitHub Actions (`.github/workflows/ci.yml`): Go tests, lint and front-end checks must pass. Dependabot (`.github/dependabot.yml`) keeps dependencies up to date.

## Milestones

1. **M1** `internal/config`: models, YAML parsing, validation, `${ENV}` substitution.
2. **M2** `internal/upstream` (GitHub) + `internal/current` (type `api`) + version comparator.
3. **M3** `internal/store` (SQLite) + scheduler + notifications.
4. **M4** REST API L1 + Svelte UI. *End of Deliverable 1 (read-only consultation).*
5. **M5** Agent: WebSocket, enrollment, heartbeat, `get_version`, `sync_scripts`.
6. **M6** `internal/proxmox` (Proxmox REST API with an API token, chosen over the spec's SSH, see `DECISIONS.md` D16) + job orchestrator for `api` updates.
7. **M7** `script` jobs: `run_update`, log streaming, cancel, agent `self_update`.
8. **M8** REST API L2 + UI (trigger, SSE logs, agents, rollback) + audit. *End of Deliverable 2.*

## Commands

```bash
make dev                          # Go server (:8080, dev/config, no embedded UI) + Vite dev server (:5173, proxies /api & /healthz)
make run-agent AGENT_ID=x         # dev agent → SERVER_URL (default http://localhost:8080), token + scripts in tmp/agent/<id>
make run-fakeapp                  # fake Sonarr on :8989 (FAKE_VERSION=…)
make check-versions               # one-shot version check of CONFIG_DIR apps against real GitHub
make test                         # go test ./...
go test ./internal/server -run TestHealthz   # single Go test
make test-web                     # front-end unit tests (vitest, src/**/*.test.ts)
make check-web                    # svelte-check type checking
make lint                         # golangci-lint (v2 config in .golangci.yml)
make build                        # pnpm build + static bin/lum-server (-tags embedui) and bin/lum-agent
```

UI embedding (see `DECISIONS.md`): SvelteKit sources live in `web/` (pnpm), and the build output goes to `internal/ui/dist` (gitignored). The UI is embedded only with `-tags embedui`. Without the tag, `ui.FS()` is nil, so `go test ./...` works on a fresh clone without building the front end.

## Stack

- Go (version in `go.mod`), static binaries (`CGO_ENABLED=0`). SQLite via `modernc.org/sqlite` (pure Go, so no `mattn/go-sqlite3`).
- HTTP: **Gin** (chosen over the spec's suggested `chi`, see `DECISIONS.md`; request logs via a slog middleware, not Gin's logger). Other libs: `github.com/coder/websocket` (successor of the suggested `nhooyr.io/websocket`), `fsnotify`, `robfig/cron`. GitHub and Proxmox clients are hand-written `net/http` (D8, D16).
- Frontend: **SvelteKit 3 + Svelte 5 as a static SPA** (chosen over the spec's Angular, see `DECISIONS.md`). Use `adapter-static` with the `index.html` fallback and `ssr = false`. Write runes only (`$state`, `$derived`, `$effect`, `$props`), and use file-based routes in `web/src/routes/`. Shared code lives in `web/src/lib/`, imported as `#lib/...`. SvelteKit config sits inline in `vite.config.ts` (there is no `svelte.config.js`). REST + SSE. UI text is **English**, responsive, light/dark theme follows the system.

## Architecture (big picture)

Two binaries in one monorepo:

- **Server** (`cmd/server` → `internal/service`, which wires config holder, store, GitHub client, checker, scheduler, agent hub (`internal/agenthub`), scripts index (`internal/scripts`), Proxmox client and job manager (`internal/jobs`) and metrics and applies reloads; HTTP in `internal/server` + `internal/api` + `internal/auth`): the only component users can reach, behind HAProxy + Keycloak OIDC. It contains the REST API + embedded UI, a cron scheduler for version checks, the job orchestrator (L2), and the agent hub (WebSocket). It calls the GitHub API (with ETag conditional requests, falling back to `/tags`) and the app APIs, and drives LXC snapshots (and the temporary CPU/memory raise of `update.resources`) through the Proxmox REST API with a least-privilege API token (`VM.Audit`, `VM.Snapshot`, `VM.Snapshot.Rollback`, `VM.PowerMgmt`, plus `VM.Config.CPU`/`VM.Config.Memory` for `update.resources`; certificate fingerprint pinning).
- **Agent** (`cmd/agent`, `lum-agent`): one per LXC, run by systemd, installed by `deploy/install-agent.sh` (served on `/install-agent.sh`), self-updated from the server's `--agent-dir` binaries. It opens **no inbound ports**. It keeps an outbound TLS WebSocket to the server (`coder/websocket`, `wss://…/agent/ws` through HAProxy; plain `ws://` only to localhost; messages in `internal/agentproto`), reconnects with exponential backoff (1s → 60s), enrolls with a shared bootstrap secret, then uses a per-agent token stored in `/opt/lum-agent/token` (everything of the agent lives in `/opt/lum-agent`: binary, unit, env, token, scripts).

Key concepts:
- **Config is YAML only** (`config/server.yaml`, `config/apps.yaml`) and is never copied into the DB. The DB stores state and history only, keyed by `app_id` from the YAML. Hot reload runs on `SIGHUP` or `POST /api/v1/config/reload`. An invalid reload keeps the old config active. An unresolved `${VAR}` blocks startup.
- **App recipe** = `current` (type `api` via JSONPath, or type `agent` via command + regex) + `upstream` (`github`, tag normalized by the `tag_regex` named group `version`) + `update` (type `api`: the app updates itself, or type `script`: run by the agent) + `healthcheck`.
- **Version states**: `up_to_date | update_available | unknown | error`. Comparison is extended SemVer (2–4 numeric segments, a missing segment counts as 0; DECISIONS.md D21). Notifications fire only on the *transition* to `update_available`, tracked through `app_state.last_notified_version`.
- **Jobs** (L2): async and persisted, with one active job per app (otherwise `409`). States: `pending → snapshotting → running → healthchecking → success | failed | rolled_back | cancelled`. Auto-rollback only happens if `rollback_on_failure: true` (default false). On server restart, in-flight jobs become `failed` (`server_restart`).
- **Scripts**: the server's `scripts.dir` is the source of truth. Files are indexed by SHA-256 and watched with inotify. Each agent receives only the scripts its recipes reference. The agent refuses to run a script whose checksum doesn't match. The API must never allow arbitrary commands on agents. Scripts receive `LUM_APP_ID`, `LUM_APP_NAME`, `LUM_CURRENT_VERSION`, `LUM_TARGET_VERSION`, `LUM_JOB_ID`.

## Security invariants

- Secrets come only from env vars. They are masked (`****`) in logs, UI and API responses, and never sent to agents.
- The only unauthenticated routes are `/healthz` and the agent WebSocket endpoint.
- Every write action is recorded in `audit_log` with the OIDC user identity (`auth.UserFrom(c)`, see `API.audit` in `internal/api`).
- Auth is BFF (HttpOnly session cookie) for the UI plus Bearer for scripts (`internal/auth`); write routes also pass the `auth.CSRF` Origin check. API responses must never expose tokens; pass URLs through `redact.URL`.
- Logs are structured JSON on stdout, correlated by `job_id` / `app_id`. Prometheus metrics are exposed at `/metrics`.
