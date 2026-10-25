# REST API

Prefix `/api/v1`, JSON. Every route requires authentication: a web UI session or `Authorization: Bearer` (see [authentication.md](authentication.md)).

Responses **never contain secrets**:
- tokens are omitted;
- URLs go through masking of sensitive parameters (`apikey=****`).

A test checks this on every route.

The API has two levels: the **read API** (L1: consult apps, run checks, reload the configuration) and the **write API** (L2: trigger updates, cancel, roll back, manage agents).

## Error format

All errors have the same shape:

```json
{"error": {"code": "app_not_found", "message": "unknown app: foo"}}
```

`details` is present when an error carries structured information (for example configuration problems).

| HTTP status | `code` | Case |
|---|---|---|
| 400 | `invalid_limit` | `limit` parameter out of range |
| 401 | `unauthenticated` | no session, expired session, invalid token |
| 403 | `csrf` | write request coming from another origin |
| 404 | `app_not_found`, `job_not_found`, `agent_not_found`, `not_found` | unknown application, job, agent or route |
| 409 | `check_running` | a full check is already running |
| 409 | `job_active` | a job is already running for the app (`details.job_id` says which) |
| 409 | `rollback_running` | a manual rollback is running for the app |
| 409 | `no_update_available` | the app is not in the `update_available` state |
| 409 | `job_not_running`, `job_not_finished` | cancelling a finished job, rolling back a running job |
| 409 | `no_snapshot`, `snapshot_gone`, `already_rolled_back` | rollback impossible: no snapshot, snapshot deleted, already done |
| 409 | `agent_revoked`, `agent_not_enrolled`, `agent_not_revoked` | action incompatible with the agent's state |
| 422 | `invalid_config` | reload rejected (the previous config stays active) |
| 422 | `no_update_recipe`, `unsupported_update`, `proxmox_not_configured` | the app definition or `server.yaml` does not allow the action |
| 500 | `internal` | internal error (details in the logs) |
| 502 | `proxmox_error`, `agent_error` | Proxmox or the agent returned an error, or did not respond |
| 503 | `agent_offline`, `script_not_synced` | agent not connected, script could not be synchronized: nothing was done |

## Read API routes

| Method | Route | Purpose |
|---|---|---|
| GET | `/me` | current user and authentication mode |
| GET | `/apps` | list of applications with their state |
| GET | `/apps/{id}` | details: app definition, release notes, history |
| POST | `/apps/{id}/check` | check one application now |
| POST | `/checks` | check all applications now |
| GET | `/system/status` | config, GitHub rate limit, schedule, errors |
| POST | `/config/reload` | reload `server.yaml` and `apps.yaml` |

## Write API routes

| Method | Route | Purpose |
|---|---|---|
| POST | `/apps/{id}/update` | trigger an update (`202`, `409` if a job is active) |
| GET | `/jobs?app=&status=&limit=` | job history, newest first |
| GET | `/jobs/{id}` | job details, whether cancel and rollback are possible |
| GET | `/jobs/{id}/logs` | job logs as an SSE stream |
| POST | `/jobs/{id}/cancel` | cancel a running job |
| POST | `/jobs/{id}/rollback` | restore the job's snapshot |
| GET | `/agents` | agents: state, version, scripts and their synchronization |
| POST | `/agents/{id}/sync-scripts` | force script synchronization |
| POST | `/agents/{id}/revoke` | revoke an agent's token |
| DELETE | `/agents/{id}` | forget a revoked agent, which can then enroll again |
| GET | `/audit?limit=` | latest audit log entries |

How a job runs is described in [updates.md](updates.md), agents in [agent.md](agent.md).

### `POST /apps/{id}/update`

Runs the pre-checks, creates the job and responds immediately, without waiting for the end:

```json
202 {"job": {"id": "c9be6a94-…", "app_id": "sonarr", "app_name": "Sonarr", "lxc_id": 101, "type": "api",
             "status": "pending", "current_version": "4.0.9.2244", "target_version": "4.0.10.2544",
             "created_by": "alice", "created_at": "…"}}
```

Possible refusals, with no job created: `404 app_not_found`, `422 no_update_recipe` / `proxmox_not_configured`, `409 no_update_available`, `409 job_active` (with `details.job_id`), `409 rollback_running`, `503 agent_offline` / `script_not_synced`. The `agent_offline` message states that nothing was done.

### `GET /jobs`

`{"jobs": [...]}`, 50 jobs by default (`limit`: 1 to 500). Filters:
- `app`: application ID;
- `status`: one or more comma-separated states (`failed,rolled_back`), or `active` for running jobs.

Each job carries `app_name` and `lxc_id`, taken from `apps.yaml` (`lxc_id` is absent if the app is no longer there), as well as `snapshot_name`, `started_at`, `finished_at` and `error` when they exist.

### `GET /jobs/{id}`

```json
{"job": {...}, "running": true, "rollback": {"available": false, "reason": "no_snapshot", "message": "the job has no snapshot"}}
```

- `running`: the job runs in this server process and can be cancelled.
- `rollback.available`: `POST /jobs/{id}/rollback` would be accepted. Otherwise, `reason` gives the error code it would return. For a finished job with a snapshot, LUM checks with Proxmox that the snapshot still exists.

### `GET /jobs/{id}/logs` (Server-Sent Events)

A `text/event-stream` stream that sends the lines already written, then new ones live:

```
id: 12
event: log
data: {"seq":12,"ts":"…","stream":"stdout","line":"[build] 3/5"}

event: status
data: {"id":"…","status":"healthchecking",…}

event: end
data: {"id":"…","status":"success",…}
```

- **`log`**: one line, with `stream` = `system` (LUM messages), `stdout` or `stderr` (script). `id` is its number.
- **`status`**: the job, at the start and then at every state change.
- **`end`**: the finished job; the server then closes the stream. For a job that is already finished, the stream replays the logs then sends `end`.
- **Resume**: after a disconnection, `EventSource` reconnects with the `Last-Event-ID` header, and the stream resumes after that line. `?after=<seq>` does the same.
- A comment line (`: keep-alive`) is sent every 15 s so that proxies do not cut a silent stream. The server sends `X-Accel-Buffering: no`.
- The stream uses the browser session (cookie): `EventSource` needs no token.

### `POST /jobs/{id}/cancel`

`202 {"job": …}`. Cancellation is asynchronous: the job moves to `cancelled` once the script is stopped (SIGTERM, then SIGKILL after 30 s) or, for an `api` job, immediately. `409 job_not_running` if the job is finished, `502 agent_error` if the order could not be delivered to the agent (the job continues).

### `POST /jobs/{id}/rollback`

Restores the job's snapshot in the background: `202 {"job": …}`. The steps are appended to the job's logs (the SSE stream reopens and shows them), and the job ends in `rolled_back`, with its original error kept. If Proxmox fails, the job keeps its state and the failure is in its logs. Conditions and refusals: see [updates.md](updates.md).

### `GET /agents`

```json
{"agents": [{
  "id": "vaultwarden-lxc", "status": "online", "agent_version": "1.0.0", "hostname": "vaultwarden",
  "last_seen_at": "…", "enrolled_at": "…", "binary": "up_to_date", "apps": ["vaultwarden"],
  "scripts": [{"name": "vaultwarden.sh", "state": "in_sync", "server_sha256": "…", "agent_sha256": "…"}]
}]}
```

- `status`: `online` (connected), `offline`, `revoked`, or `not_enrolled` for an agent referenced in `apps.yaml` that has never connected.
- `binary`: `up_to_date` if the connected agent's executable is the one the server distributes, `outdated` if it differs (`self_update` pending or disabled), `unknown` otherwise.
- `scripts`: the scripts the agent must hold (those of its app definitions), with their state: `in_sync`, `stale` (different checksum) or `missing`.

### `POST /agents/{id}/sync-scripts`, `POST /agents/{id}/revoke`, `DELETE /agents/{id}`

- **Sync**: returns `{"agent": …}` once the scripts have been pushed, even if they already looked up to date. `503 agent_offline` if the agent is not connected, `409 agent_revoked` if it is revoked.
- **Revoke**: the token is rejected from now on, and the current connection is closed. Returns `{"agent": …}`. `409 agent_not_enrolled` for an agent that never enrolled.
- **Delete**: only for a revoked agent (`409 agent_not_revoked` otherwise). The agent is forgotten: it can enroll again with the shared secret as long as `agents.enrollment: open`. Returns `{"deleted": "<id>"}`.

### `GET /audit`

`{"entries": [{"id", "ts", "user", "action", "target", "details"}]}`, newest first, 100 by default (`limit`: 1 to 1000). Shown on the System status page.

## Read API details

### `GET /apps`

```json
{
  "apps": [{
    "id": "sonarr", "name": "Sonarr", "lxc_id": 101,
    "status": "update_available",
    "current_version": "4.0.9.2244", "latest_version": "4.0.10.2544",
    "last_check_at": "2026-10-18T07:03:12Z",
    "release_url": "https://github.com/Sonarr/Sonarr/releases/tag/v4.0.10.2544",
    "repo": "Sonarr/Sonarr", "current_type": "api", "update_type": "api"
  }],
  "check_running": false
}
```

- **All apps listed**: the list follows `apps.yaml`. An app that has never been checked appears as `unknown`, with `last_check_at: null`.
- **Errors**: `error` gives the collection message. For an `unknown` app, it gives the reason, for example an agent that is not connected.
- **Running job**: `active_job` is `{"id", "status"}` during an update, `null` otherwise.

### `GET /apps/{id}`

Contains the fields of `/apps`, plus:
- `recipe`: the app definition without secrets (masked URLs, no token or request body);
- `release_notes`: the markdown notes of the latest version;
- `checks`: the last 50 checks, newest first.

### `POST /apps/{id}/check` and `POST /checks`

- **Synchronous**: the response arrives once the check is done (60 s maximum per app). `/apps/{id}/check` returns `{"check": {...}}`, `/checks` returns `{"checks": [...]}`.
- **Exclusive full check**: `/checks` responds `409 check_running` if a full check is already running (scheduled or manual).
- **Notifications**: they follow the same rules as scheduled checks (see [checks-notifications.md](checks-notifications.md)).

### `GET /system/status`

```json
{
  "version": "1.0.0", "auth_mode": "oidc",
  "config": {"valid": true, "loaded_at": "…"},
  "github": {"known": true, "limit": 5000, "remaining": 4987, "reset": "…", "token_configured": true},
  "scheduler": {"check_cron": "0 7 * * *", "jitter": "5m0s", "concurrency": 5,
                "next_check": "…", "last_check": "…", "check_running": false},
  "apps": {"up_to_date": 15, "update_available": 3, "error": 1, "unknown": 1},
  "recent_errors": [{"app_id": "radarr", "name": "Radarr", "error": "…", "at": "…"}]
}
```

After a failed reload, `config.valid` is `false`, and `config.last_reload_error` gives the details: `kind` (`validation`, `missing_env` or `read`), `problems` and `missing_vars`.

### `POST /config/reload`

Reloads both YAML files. `kill -HUP <pid>` or `systemctl reload lum-server` have the same effect.

- **Success**: `200 {"loaded_at": "…", "apps": 20}`. The check schedule and the GitHub client are rebuilt if their settings changed.
- **Failure**: `422 invalid_config`, with `details` (same format as `last_reload_error`). **The previous configuration stays active.**
- **What a reload does not cover**: `server.listen`, `server.log_level`, `server.base_url` and `auth.*` require a restart.

## Audit log

Every write action is recorded in `audit_log` with the user's identity, and logged (`"msg":"audit"`). Refused attempts are recorded too, with the detail `refused: <code>`. The latest entries are shown on the System status page (`GET /audit`).

| Action | Target | Detail |
|---|---|---|
| `app.check` | app ID | resulting state |
| `checks.run` | — | — |
| `config.reload` | — | `failed` on failure |
| `app.update` | app ID | `job <id>`, or `refused: <code>` |
| `job.cancel` | job ID | app ID |
| `job.rollback` | job ID | app ID and snapshot |
| `agent.sync_scripts` | agent ID | `failed` if the agent returned an error |
| `agent.revoke` | agent ID | — |
| `agent.delete` | agent ID | — |

## Prometheus metrics: `GET /metrics`

`/metrics` is protected like the API. Prometheus can obtain a Keycloak token on its own (`oauth2`, client credentials flow):

```yaml
scrape_configs:
  - job_name: lum
    scheme: https
    static_configs: [{targets: ["lum.example.com"]}]
    oauth2:
      client_id: lum-automation
      client_secret_file: /etc/prometheus/lum-secret
      token_url: https://auth.example.com/realms/example/protocol/openid-connect/token
```

| Metric | Type | Description |
|---|---|---|
| `lum_apps{status}` | gauge | configured applications by state |
| `lum_agents{status}` | gauge | enrolled agents: `online`, `offline`, `revoked` |
| `lum_agents_connected` | gauge | agents currently connected |
| `lum_jobs_total{type,status}` | counter | finished jobs, by type (`api`, `script`) and final state |
| `lum_job_duration_seconds{type}` | histogram | job duration, from creation to end |
| `lum_jobs_active` | gauge | running jobs |
| `lum_checks_total{status}` | counter | checks performed, by resulting state |
| `lum_check_duration_seconds` | histogram | duration of one app check |
| `lum_github_rate_limit_remaining` / `_limit` | gauge | GitHub rate limit |
| `lum_github_rate_limit_reset_timestamp_seconds` | gauge | rate limit reset (Unix timestamp) |
| `lum_build_info{version}` | gauge | server version |

Standard Go and process metrics (`go_*`, `process_*`) are exposed as well.
