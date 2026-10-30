# Roadmap

Concrete breakdown of the specification's milestones ([§14.2](specification.md)). Milestones are delivered **in order, without anticipating the next one**.

A milestone is done when:
- `make test` and `make lint` pass, as well as `make check-web` and `make test-web` once there is front-end code;
- the user documentation is written or updated in `doc/` and linked from `README.md`;
- deviations from the spec are recorded in [`DECISIONS.md`](../DECISIONS.md).

Method: one branch per milestone (`m1-config`, `m2-versions`…), merged once the milestone is done.

| Milestone | Content | Status |
|---|---|---|
| M0 | Foundation: Go/Gin, skeleton agent, SvelteKit web UI, Makefile | ✅ |
| M1 | YAML configuration | ✅ |
| M2 | Version collection (GitHub, application APIs) | ✅ |
| M3 | Persistence, scheduler, notifications | ✅ |
| M4 | REST API L1 + UI — **end of Deliverable 1** | ✅ |
| M5 | Connected agent (WebSocket, enrollment, scripts) | ✅ |
| M6 | Proxmox + `api` type jobs | ✅ |
| M7 | `script` type jobs, agent update | ✅ |
| M8 | REST API L2 + UI + audit — **end of Deliverable 2** | ✅ |

## M1 — Configuration (`internal/config`)
- [x] Models faithful to §4.1 and §4.2. Fields mentioned elsewhere in the spec are added with their default: `scheduler.concurrency` (5), `agents.enrollment` (`open`), `rollback_on_failure` (false), `retention` (2).
- [x] Strict parsing (`go.yaml.in/yaml/v3`, unknown fields rejected), `time.Duration` durations.
- [x] `${ENV}` substitution on values, after parsing. Sources: the environment, then the `.env` file. All missing variables are listed in a single error.
- [x] `Secret` type masked (`****`) in `fmt`, JSON and slog.
- [x] Exhaustive validation (all errors at once, with their path).
- [x] `Holder`: atomic active config; an invalid reload keeps the previous config.
- [x] Server flags `--config-dir` and `--env-file`, dev config `dev/config`, examples in `deploy/config`.
- [x] Docs: `doc/configuration.md`.

## M2 — Version collection
- [x] `internal/semver`: 2 to 4 segments (missing segment = 0, 2 segments since D21), comparison, computing the state `up_to_date | update_available | unknown | error`.
- [x] `internal/upstream`: hand-written GitHub client (D8):
  - ETag/304 cache with TTL;
  - `include_prerelease`;
  - fallback to `/tags`;
  - normalization through `tag_regex`;
  - `X-RateLimit-*` quota.
- [x] `internal/current`: `api` type (GET + auth header + JSONPath via `ohler55/ojg`); `agent` type as `unknown` until M5.
- [x] `httptest` tests: 200, 304, 401, timeout, no releases, prerelease.
- [x] Mask secrets passed in a URL (e.g. an `?apikey=` query parameter) in errors and logs.
- [x] Dev tools: `make check-versions` (`cmd/lum-check`), `make run-fakeapp` (`dev/fakeapp`).
- [x] Docs: `doc/version-detection.md`.

## M3 — Persistence, scheduler, notifications
- [x] `internal/store`: `modernc.org/sqlite`, WAL, embedded migrations. Tables `version_checks`, `app_state`, `audit_log`. 90-day retention.
- [x] `internal/checker`: parallel checks with a limit. An isolated failure does not stop the others. The transition to `update_available` is detected through `last_notified_version`.
- [x] `internal/scheduler`: `robfig/cron/v3` + jitter.
- [x] `internal/notify`: `ntfy` and `webhook`, filtering by event.
- [x] No token in error messages (dedicated test).
- [x] Full check at startup; `--data-dir` flag; fake webhook in `make run-fakeapp`.
- [x] Docs: `doc/checks-notifications.md`.

## M4 — REST API L1 + UI (end of Deliverable 1)
- [x] §11 L1 routes; reload on SIGHUP and via `POST /config/reload`. On reload, rebuild what depends on the config: the GitHub client (token) and the scheduler (cron, jitter).
- [x] Every config URL exposed by the API goes through `redact.URL` (keys in query parameters).
- [x] OIDC authentication (`coreos/go-oidc/v3`): BFF (session cookie) + Bearer (D12).
- [x] Prometheus `/metrics` (checks, GitHub quota).
- [x] UI:
  - dashboard;
  - "Check now", global and per app;
  - app detail with release notes (`marked` + `DOMPurify`);
  - System status page.
- [x] `deploy/`: systemd unit, config examples, backup and restore.
- [x] §6.2 acceptance criteria (integration tests `internal/server/api_test.go`, `internal/checker`).
- [x] Mobile rendering of the web UI checked (390 px iframe): hamburger menu, dashboard cards, detail page.
- [x] Docs: `doc/api.md`, `doc/deployment.md`, `doc/authentication.md`.

## M5 — Connected agent
- [x] WebSocket `github.com/coder/websocket` (D15), JSON envelope `{type, id, payload, error}` (`internal/agentproto`).
- [x] Enrollment (shared secret → individual token, only its hash is stored), heartbeat, `offline` state + `agent_offline` notification, backoff 1 s → 60 s.
- [x] `agents` and `agent_scripts` tables (migration `0003`).
- [x] `get_version` wired to `current.type: agent`.
- [x] `sync_scripts`: `fsnotify` index + SHA-256, targeted synchronization per agent (connection, change, reload, diverging heartbeat).
- [x] Local dev: `--token-file` and `--scripts-dir` (`make run-agent` → `tmp/agent/<id>/`), non-TLS WS limited to localhost.
- [x] `lum_agents{status}` metric.
- [x] Docs: `doc/agent.md` (protocol, lifecycle, manual installation until `install-agent.sh` in M7).

## M6 — Proxmox + `api` type jobs
- [x] `internal/proxmox`: Proxmox REST API with a least-privilege API token, certificate fingerprint pinning (D16, instead of SSH + `pct` wrapper).
- [x] `internal/jobs`:
  - §7.2 state machine, `jobs` and `job_logs` tables;
  - only one active job per app (unique index; the API returns the `409` in M8);
  - timeout; running jobs marked `server_restart` at shutdown and at restart;
  - snapshot, healthcheck, version check, snapshot retention, `rollback_on_failure`.
- [x] System status page: Proxmox card (API reachable, containers invisible to the token).
- [x] Docs: `doc/updates.md`, `doc/proxmox.md`.

## M7 — `script` type jobs
- [x] `run_update`: checksum verified on the server and agent sides, `LUM_*` variables, streamed logs (kept by the agent during a disconnection).
- [x] `cancel_job`: SIGTERM to the process group, then SIGKILL after 30 s (`Manager.Cancel`; the API route comes in M8).
- [x] Agent `self_update`: automatic when its binary differs from the one in `/opt/lum/agent` (`agents.self_update`).
- [x] `deploy/install-agent.sh` (served by the server on `/install-agent.sh`), `deploy/lum-agent.service`, `deploy/scripts/README.md` + `example.sh`, `make build-agents`.
- [x] Docs: `doc/scripts.md`, `doc/agent.md` (installation, self_update).

## M8 — REST API L2 + UI (end of Deliverable 2)
- [x] §11 L2 routes (asynchronous `202`, `409 job_active`), SSE logs with resume (`Last-Event-ID`). In addition: `DELETE /agents/{id}` (revoked agent) and `GET /audit` (D18).
- [x] Audit log with the OIDC identity for all writes, refusals included; shown on the System status page.
- [x] Manual rollback (`Manager.Rollback`): asynchronous, in the job's logs, checks that the snapshot exists.
- [x] UI: trigger (dashboard, app page), job page (live logs, cancellation, rollback), Jobs page (filters), Agents page (state, binary, script synchronization, revocation, deletion).
- [x] Metrics `lum_jobs_total`, `lum_job_duration_seconds`, `lum_jobs_active`, `lum_agents_connected`.
- [x] §7.4 acceptance criteria covered by tests: end-to-end `api` and `script` updates, live logs, failed healthcheck then rollback, offline agent refused before any snapshot, `409` on the second trigger.
- [x] Dev tools: the fake Sonarr installs a version (`FAKE_UPGRADE_TO`), the dev Vaultwarden script simulates an update.
- [x] Docs: `doc/api.md`, `doc/updates.md`, `doc/agent.md` (Agents page).

## Public release
Publication on GitHub (D20).
- [x] Anonymization (no real hosts, container IDs, app inventory or network layout).
- [x] English translation (docs, UI, log lines, notifications, API messages).
- [x] `LICENSE` (MIT), `SECURITY.md`, `CONTRIBUTING.md`.
- [x] GitHub Actions CI (`.github/workflows/ci.yml`) and release workflow.
- [x] Dependabot.
- [ ] First push to GitHub, `v0.1.0` tag and release check; repository settings (Dependabot alerts and security updates, secret scanning, branch protection).

## Open points
1. ~~**M4 — OIDC flow of the web UI.**~~ Settled: BFF + Bearer (D12).
2. ~~**M5 — Agent TLS.**~~ Settled: through HAProxy, `wss://lum.example.com/agent/ws` (D15).
3. ~~**M6 — Proxmox access: SSH or REST API.**~~ Settled: REST API (D16).
   - **SSH**, the spec's choice (§10): dedicated key, restricted by `command=` to a `pct snapshot|rollback|delsnapshot|status` wrapper.
   - **Proxmox REST API** (`https://<host>:8006/api2/json`): an API token (`PVEAPIToken`) whose role is limited to `VM.Snapshot`, `VM.Snapshot.Rollback` and `VM.Audit`, on the managed containers only. Pros: no key or wrapper to install on the host, and permissions restricted by Proxmox itself. Cons: Proxmox tasks are asynchronous (the UPID must be followed until completion), and the host certificate must be handled (CA or pinned fingerprint).
   - If the API is chosen, it is a deviation from §10 and §13 (`internal/proxmox` over SSH), to be recorded in `DECISIONS.md`.
