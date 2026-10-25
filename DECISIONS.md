# Decisions

Deviations from, or clarifications of, [`doc/specification.md`](doc/specification.md) (§14.2).

## D1 — Gin instead of chi
The spec suggests `chi` (§14.1, a "suggested" library). The HTTP server uses **Gin** (`github.com/gin-gonic/gin`), out of the maintainer's habit. Gin's text logger is not used: a middleware logs requests through `slog` as JSON (§13).

## D2 — UI embedded via the `embedui` build tag
`internal/ui` only embeds the web UI build with `-tags embedui`. Without this tag (`go test ./...`, `go run`), `ui.FS()` returns `nil` and the server serves no UI. A fresh clone therefore compiles without a front-end build. In development, the UI runs on the Vite dev server (:5173) and proxies `/api` and `/healthz` to the server (:8080).

## D3 — Web UI sources in `web/`, build in `internal/ui/dist`
`go:embed` cannot reference a parent path. The build (`adapter-static`, `pages`/`assets` options) is therefore written to `internal/ui/dist`, which Git ignores. The project uses pnpm: its `node_modules` (symlinks + `.pnpm/`) is not walked by `go ... ./...`.

## D4 — SvelteKit (SPA) instead of Angular
The spec asks for Angular ≥ 19 (§14.1). The web UI uses **Svelte 5 + SvelteKit 3** as a static SPA: `adapter-static` with `fallback: 'index.html'`, `ssr = false` in `src/routes/+layout.ts`. The bundle is lighter, and the code (single-file components, `$state`/`$derived` runes, file-based routing in `src/routes/`) is simpler to read and debug, including for AIs. Deep linking relies on the Go server's SPA fallback (`internal/server`).

## D5 — Vitest 4 (not 5)
Vitest 5 excludes Node 25 from its `engines`, and `engine-strict=true` (`web/.npmrc`) then blocks the installation. We stay on Vitest 4.x as long as the development machine runs Node 25; Vitest 5 can be considered with Node 24 LTS or ≥ 26.

## D6 — Configuration: added fields and clarified rules (M1)
The spec mentions some fields outside the YAML examples. They are added with a default:
- `scheduler.concurrency` (5, §6.2);
- `agents.enrollment` (`open`, §10);
- `rollback_on_failure` (false, §7.1);
- `retention` (2, §7.1);
- `update.timeout` (15m).

Clarifications adopted (details in `doc/configuration.md`):
- **Strict parsing**: an unknown field is an error, to catch typos.
- **`${VAR}` substitution**: only in text fields, after YAML parsing, so that a value cannot inject structure. The environment takes precedence over `.env`.
- **`notifications[].url` is treated as a secret**, since a webhook URL often embeds a token.
- **Relative `scripts.dir`**: it is resolved relative to the configuration directory.
- **Defaults**: `auth.mode` defaults to `oidc` (secure by default); `tag_regex` has a default value covering `v1.2.3` and `1.2.3.4`.
- **`update` and `healthcheck` are optional**: an app can be view-only (Deliverable 1).
- **Script**: a plain `*.sh` name, without a path, present in `scripts.dir`.

## D7 — Gin always in release mode
Gin's debug mode writes text lines (`[GIN-debug]`) to the output, which breaks the 100% JSON logs requirement (§13). The server therefore forces `gin.ReleaseMode` regardless of `log_level`.

## D8 — Hand-written GitHub client rather than `go-github` (M2)
The spec suggests `google/go-github`. The need is limited to three calls (`/releases/latest`, `/releases`, `/tags`), but with:
- fine control of the cache (TTL + ETag/304);
- a fallback to tags;
- quota tracking.

A `net/http` client of about 250 lines (`internal/upstream/github.go`) is simpler to master and test than stacking a caching transport on top of `go-github`.

Clarifications adopted:
- **Cache**: during `cache_ttl`, no request is made; after that, the request is conditional (ETag).
- **`include_prerelease: true`**: we take the most recent release in the list (GitHub's order), excluding drafts, whose tag matches.
- **Fallback to tags**: we take the highest SemVer version, not the first tag in the list.

## D9 — Clarifications on comparison and the installed version (M2)
- **Comparison**: the spec only covers numeric segments. `-prerelease` suffixes follow SemVer rules and `+build` is ignored, so that `include_prerelease` is usable. A leading `v` is tolerated, since some APIs return `v2.15.3`.
- **Installed version ahead of the public release**: it is considered `up_to_date`.
- **`current.type: agent`**: it returns `ErrAgentUnavailable` until M5, and the state is then `unknown` (§5.1).
- **Secrets in URLs**: sensitive parameters (`apikey`, `token`…) and the `user:pass@` password are masked in all error messages (`internal/redact`).
- **Dev tools**: `cmd/lum-check` (one-shot check) and `dev/fakeapp` (fake Sonarr) are not part of the deliverables.

## D10 — Persistence, checks and notifications (M3)
- **`app_state`**: in addition to the §12 columns, the table has `release_url` and `error`, so the dashboard does not have to re-read the history.
- **Date format**: RFC 3339 UTC strings, readable with `sqlite3` and sortable.
- **SQLite transactions**: they are opened with `BEGIN IMMEDIATE` (`_txlock=immediate`). In WAL mode, concurrent "deferred" transactions fail with `SQLITE_BUSY_SNAPSHOT` when writing; this was observed with parallel checks.
- **Database location**: `--data-dir` flag (default `/data`, database `lum.db`), rather than a configuration field. The spec sets `/data/lum.db`, and it is deployment data.
- **Check at startup**, in addition to the cron, so the dashboard is filled right away (§6.2: "from the very first start").
- **`update_available` notification**: it is sent as soon as the latest version differs from `last_notified_version`, so also for an even newer version, without going through another state. The version is marked as notified even if a channel failed, to avoid spamming the others.
- **ntfy**: JSON publication on the server root (topic taken from the URL), which handles UTF-8 titles correctly.
- **History purge**: at startup and every night at 3:30.
- **Installed version of type `agent`**: the state stays `unknown` (without error), and the reason is kept in `error`.

## D11 — Configuration defaults and explicit zero (M1 fix)
The defaults of the global sections are set before YAML decoding, which only overwrites the fields present. An explicit zero value (`jitter: 0s`, `cache_ttl: 0s`) is therefore respected. App defaults are still applied to empty fields, since their zero value is never valid.

## D12 — BFF + Bearer authentication (M4)
The spec mentions both "OIDC (Bearer)" and a `client_secret`. Choice adopted:
- **Web UI**: the server performs the *authorization code* flow (confidential client + PKCE) and sets an opaque `HttpOnly` session cookie. Sessions are stored in the database as a SHA-256 hash. Tokens never reach the browser.
- **API and `/metrics`**: they also accept an `Authorization: Bearer`, whose audience must contain `client_id` (Keycloak mapper).

Three reasons for this choice: reduce the impact of an XSS on an application that has root access; let `EventSource` (SSE logs, M8) work without a token in the URL; make use of the `client_secret` provided for by the spec.

Additions:
- **Configuration**: `auth.session_ttl` (12h); `server.base_url` is required in `oidc` mode.
- **CSRF**: `Origin` header check on writes, in addition to `SameSite=Lax`, including in `none` mode.
- **Startup**: Keycloak is discovered on first use, so the server starts even if Keycloak is unavailable.

Details in [`doc/authentication.md`](doc/authentication.md).

## D13 — Deliverable 1 API: clarifications (M4)
- **`GET /me`**: route added for the web UI (current user, authentication mode).
- **Error format**: it gains an optional `details` field, used by `422 invalid_config` for configuration problems.
- **`POST /checks` and `/apps/{id}/check`**: synchronous (≤ 30 s for 20 apps, §6.2). Only one full check at a time, otherwise `409 check_running`.
- **Audit log from M4**: Deliverable 1 writes (checks, reload) are recorded, as §10 requires for the reload.
- **`/metrics`**: authenticated like the API, since §10 allows no other public route. Prometheus uses its `oauth2` block (client credentials).
- **Reload**: it rebuilds the GitHub client and the scheduler only if their settings changed, which keeps the ETag cache. `listen`, `log_level`, `base_url` and `auth.*` require a restart.
- **Exposed app definition**: without token or body, URLs masked by `redact.URL`.

## D14 — `telegram` notification channel
§4.1 only knows `ntfy` and `webhook`. The generic webhook cannot target Telegram, because the `sendMessage` API requires `chat_id` and `text`. §13 provides for other channels, so we add `type: telegram`.
- **`url`**: the bot's `sendMessage` endpoint, which contains the token. It is a secret, like the other notification URLs, and the token comes from `${TELEGRAM_BOT_TOKEN}`.
- **`chat_id`**: field added to `notifications[]`, required for `telegram` and rejected for the other types.
- **Message**: HTML (`parse_mode`), title and message escaped, without link preview.
- **Errors**: the `description` returned by Telegram is included in the error, without the token.

## D15 — Connected agent (M5)
- **`github.com/coder/websocket`** instead of `nhooyr.io/websocket` (§14.1): it is the same project, taken over and renamed by Coder.
- **TLS through HAProxy**: agents connect to `wss://lum.example.com/agent/ws`, HAProxy terminates TLS and the server stays on HTTP. The agent refuses `http://`/`ws://` except to a local address (development), which guarantees the "TLS mandatory" of §10.
- **Protocol**: `{type, id, payload, error}` envelope. The `error` field carries the failure of an order or the rejection of a `hello`. Messages added to §8.2: `hello`, `welcome`, `heartbeat`, `response`.
- **Authentication in the first message** (`hello` with token or secret), not in HTTP headers: the enrollment token is sent back on the same connection.
- **Re-enrollment**: possible with the shared secret as long as `enrollment: open`; it replaces the token. Refused for a revoked agent. An agent whose token is rejected does not fall back to the shared secret on its own.
- **Offline**: only the absence of a heartbeat for `offline_after` makes the agent `offline` and sends `agent_offline`, not a mere disconnection. A server restart therefore does not notify for every agent.
- **`get_version`**: the agent returns stdout, stderr and the exit code; the server applies `output_regex` to stdout, then to stderr if nothing matches. Command without a shell, 25 s timeout, outputs limited to 64 KiB.
- **`sync_scripts`**: the order contains the agent's full set of scripts, and the agent deletes the other `*.sh` in its directory. Permissions applied: those of the server, with at least `rwx` for the owner and at most `r-x` for the others. Maximum script size: 1 MiB.
- **`agent_scripts`** contains the scripts **reported by the agent** (hello, heartbeat, sync result); the `in_sync`/`stale`/`missing` state of §9 will be computed at display time (M8) by comparison with the index.
- **Agent secret**: read from `LUM_AGENT_SECRET` (environment) rather than as an option, so it does not show up in `ps`. `--token-file` and `--scripts-dir` options (defaults: `/opt/lum-agent/token`, `/opt/lum-agent/scripts`; see D17 for the token).

## D16 — Proxmox through the REST API, update jobs (M6)
- **REST API instead of SSH** (§10, §13): LUM calls the Proxmox API (port 8006) with an API token (`PVEAPIToken`). There is no SSH key or wrapper to install on the host, and Proxmox permissions limit the token: role `VM.Audit`, `VM.Snapshot`, `VM.Snapshot.Rollback`, `VM.PowerMgmt`, on the managed containers only. `VM.PowerMgmt` is used to restart the container after a rollback, since Proxmox stops it to restore it.
- **`proxmox` section** added to `server.yaml`: `url`, `token_id`, `token_secret` (secret), `fingerprint`. A node's self-signed certificate is verified by its SHA-256 fingerprint (pinning); without a fingerprint, the system's certificate authorities are used. The section is optional: without it, an update with `snapshot_before_update: true` is refused at pre-check, rather than blocking the startup of an existing configuration.
- **Node**: it is resolved on each operation through `/cluster/resources`, which follows migrations within a cluster.
- **Snapshot name**: Proxmox limits names to 40 characters. `lum-pre-update-<job_id>` (51 characters with a UUID) becomes `lum-pre-update-` followed by the first 12 hex digits of the job. The full identifier is put in the snapshot description.
- **Retention**: only `lum-pre-update-*` snapshots are counted and deleted, never manual snapshots. It applies after a success.
- **`jobs` table**: the `current_version` column (starting version) is added to those of §12. The uniqueness of the active job per app is enforced by a partial unique index.
- **Timeout**: `update.timeout` bounds the whole job (snapshot, request, waiting for the version, healthcheck). The rollback and retention that follow get 10 more minutes.
- **Version check**: the installed version must be **at least** the target. A newer version, published in the meantime, is accepted.
- **Server shutdown**: running jobs move to `failed` (`server_restart`), without rollback or notification. This is the same state as the one set at the next startup after a crash.
- **Snapshot failure**: the job ends as `cancelled` ("job cancelled", §7.1).
- **No trigger in M6**: the `POST /apps/{id}/update` API is an L2 route, delivered in M8 with the UI.

## D17 — `script` jobs, cancellation, agent distribution (M7)
- **Asynchronous `run_update`**: the agent acknowledges the order immediately, then sends `job_log` (one output line) and `job_done` (exit code, cancellation). These messages are added to those of §8.2. They are tied to the job by its identifier, not to the connection: during a disconnection, the agent queues them (20,000 lines at most) and sends them on reconnection.
- **Outputs cleaned by the agent**: ANSI sequences removed, `\r` resolved as in a terminal, lines limited to 8 KiB. `stdout`/`stderr` streams from §12; LUM's own messages stay in `system`.
- **Script environment**: the agent's, plus only the `LUM_*` variables of the order; working directory `/`. The script is started directly (executable bit, shebang), in its own process group, so that cancellation reaches its children.
- **Cancellation**: final state `cancelled`, without automatic rollback or notification; the snapshot is kept. `update.timeout` exceeded: the script is stopped the same way, and the job is `failed (timeout)`.
- **Server shutdown during a script**: the script is not killed (an update interrupted halfway is worse). The job is `failed (server_restart)` and the end of the script is ignored.
- **Automatic `self_update`**: the spec gives it no trigger. The server orders it when the hash of the agent's binary differs from that of `/opt/lum/agent/lum-agent-linux-<arch>`, never during a job, at most once every 10 min per agent. Option `agents.self_update: auto | off` (default `auto`). Comparing hashes rather than versions avoids loops when versions are badly set. After the replacement, the agent exits and systemd restarts it.
- **Agent binaries**: server option `--agent-dir` (default `/opt/lum/agent`), like `--data-dir`. Download at `/agent/download/lum-agent-linux-<arch>`, authenticated by an agent's token (`X-Agent-ID` + Bearer) or by the enrollment secret (open enrollment), with the hash in `X-Checksum-Sha256`.
- **Public `/install-agent.sh`**: it is the `curl … | bash` command of §8.4. The script contains no secret: the server URL is injected into it from `server.base_url`, or failing that from the request. The secret is passed as an option or through `LUM_AGENT_SECRET`, and removed from `/opt/lum-agent/env` after enrollment.
- **The whole agent in `/opt/lum-agent`**: binary, systemd unit (declared with `systemctl link`), `env` (`--id`, `--server` and enrollment secret), token and scripts. §8.1 put the token in `/etc/lum-agent/token`: it is in `/opt/lum-agent/token`, so that a single directory is enough to back up, inspect or remove the agent. `install-agent.sh` migrates an earlier installation (takes over the token from `/etc/lum-agent`).
- **Script conventions**: `deploy/scripts/README.md` (with the examples), rather than `scripts/README.md` at the root (§9), to keep all scripts in the same place.

## D18 — Deliverable 2 API and interface (M8)
- **Asynchronous responses**: `POST /apps/{id}/update`, `/jobs/{id}/cancel` and `/jobs/{id}/rollback` reply `202` with the job, without waiting for the end. Tracking goes through `GET /jobs/{id}` and the SSE stream.
- **SSE stream** (`GET /jobs/{id}/logs`): `log` events (with `id` = line number, for resuming through `Last-Event-ID`), `status` and `end`; the server closes the stream at the end of the job. A comment every 15 s prevents proxies from cutting the connection.
- **Manual rollback**: asynchronous, its steps are appended to the job's logs, and the job ends as `rolled_back` (original error kept). It is allowed for any finished job whose snapshot still exists, including `success`, and blocks updates of the app while it runs. If Proxmox fails, the job keeps its state.
- **Error codes**: the spec only gives `agent_offline`. The other codes (`job_active`, `no_update_available`, `snapshot_gone`…) and their HTTP statuses are listed in `doc/api.md`. `409 job_active` carries the id of the active job in `details.job_id`.
- **Audit of refusals**: refused write attempts are also recorded (`refused: <code>`), to keep track of who tried what.
- **Added routes**:
  - `DELETE /agents/{id}`, for a revoked agent only: without it, a revoked agent could never be reinstalled under the same identifier (§10 makes revocation final, with no way back).
  - `GET /audit`, to show the audit log on the System status page.
- **`GET /agents`** also lists the agents referenced in `apps.yaml` but never enrolled (`not_enrolled`), and the binary state (`self_update`). The `online` state is that of the WebSocket connection, not only that of the database.
- **Added metrics**: `lum_jobs_total{type,status}`, `lum_job_duration_seconds{type}`, `lum_jobs_active`, `lum_agents_connected`.
- **Agent**: a relative `--scripts-dir` is made absolute at startup, since scripts run in `/` (M7 fix, found with `make run-agent`).

## D19 — LXC resources during the update (`update.resources`)
- **Deviation from the §4 schema**: optional field `update.resources` (`cores`, `memory` in MB). Some updates (for example, scripts that rebuild an application from source) need far more than the usual usage. Some community install scripts even refuse to start on a smaller LXC.
- **Raise only, then restore**: LUM never lowers a value. An LXC without a CPU limit (`cores` absent) is left alone.
- **Order**: the raise happens after the snapshot, so that a rollback also brings back the original resources. The restore happens as soon as the script or request ends, before the healthcheck, so the application is checked in its usual configuration.
- **Failures**: if the raise fails, the job is cancelled before the update. If the restore fails, the job outcome does not change, and the `pct set` command to run is written to its logs.
- **Server shutdown**: the script keeps running on the agent, so the resources stay raised. They are not persisted for a restore at restart: the previous value is in the job's logs.
- **Additional token permissions**: `VM.Config.CPU` and `VM.Config.Memory`, to add only if an app definition uses `update.resources`.

## D20 — Public release on GitHub, English everywhere
- **Publication**: the project is published on GitHub as a public repository under the MIT license, with a fresh Git history (the private development history is not published). The Go module path is `github.com/Wolfx56/lum`.
- **Language**: documentation (including the spec and this file), UI text, log lines, notifications and API messages switched from French to English. This deviates from §13 ("UI: French").
- **Anonymization**: the real deployment configuration (hosts, container IDs, app inventory, network layout) is kept outside the repository. Examples use `example.com`, RFC 5737 addresses (`192.0.2.x`) and generic container IDs (101, 102…).
- **Agent script examples**: `deploy/scripts/` is reduced to `example.sh` (template) and `vaultwarden.sh`.
