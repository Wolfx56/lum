# Configuration

All of LUM's configuration lives in two YAML files in the same directory:

| File | Contents |
|---|---|
| `server.yaml` | global settings: HTTP server, authentication, GitHub, scheduling, notifications, agents, scripts, Proxmox |
| `apps.yaml` | one **app definition** per application: how to read the installed version, where to find the latest version, how to update |

The database never holds configuration, only state and history.

Commented examples are in [`deploy/config/`](../deploy/config). For a first run, see [getting-started.md](getting-started.md).

## Starting the server

```bash
lum-server --config-dir /opt/lum/config --env-file /opt/lum/config/.env
```

| Flag | Default | Purpose |
|---|---|---|
| `--config-dir` | `config` | directory containing `server.yaml` and `apps.yaml` |
| `--env-file` | `.env` | optional variables file (ignored if missing) |
| `--listen` | `server.listen` | overrides the listen address |
| `--log-level` | `server.log_level` | overrides the log level |
| `--data-dir` | `/data` | state directory: SQLite database `lum.db` (see [checks-notifications.md](checks-notifications.md)) |
| `--agent-dir` | `/opt/lum/agent` | `lum-agent-linux-<arch>` binaries served to agents for installation and self-update (see [agent.md](agent.md)) |

An invalid configuration **prevents startup**. The message lists every problem at once, each with its path:

```
lum-server: loading configuration from config: invalid configuration (2 problems):
  - apps[0].id: must be kebab-case (got "Sonarr")
  - apps[1].update.script: script "missing.sh" not found in scripts.dir (/data/scripts)
```

Unknown keys are errors too, so a typo is caught instead of being silently ignored.

## Secrets and `${…}` variables

Never write secrets in the YAML files. Any text field can contain one or more `${VARIABLE_NAME}` references, including in the middle of a value (`"https://${HOST}/hook"`).

- **Resolution**: first the process environment, then the `--env-file` file. The environment wins.
- **Missing variable**: a blocking error that lists every missing variable and the fields that use it:
  ```
  missing environment variables: SONARR_API_KEY (apps[0].current.token, apps[0].update.token)
  ```
- **Text fields only**: substitution happens after the YAML is parsed, so a variable's value cannot change the structure of the file. Numbers and durations do not accept `${…}`.
- **`.env` format**: one `KEY=value` line per variable. Blank lines and `#` comments are allowed, an `export ` prefix is tolerated, and single or double quotes are stripped. No interpolation.

Sensitive fields are **masked** (`****`) everywhere they could leak: logs, API responses, the web UI. An empty value stays empty, so you can see that it is not set.

The sensitive fields are:
- `auth.oidc.client_secret`
- `github.token`
- `agents.shared_secret`
- `proxmox.token_secret`
- `notifications[].url`, because a webhook URL often contains a token
- `current.token` and `update.token`

Other URLs (`current.url`, `update.url`, `healthcheck.url`) are not treated as secrets. If an application requires its key in the URL (for example `?apikey=…`), use a `${…}` variable: sensitive query parameters are masked in error messages (see [version-detection.md](version-detection.md#secrets-in-urls)).

## `server.yaml`

The smallest valid `server.yaml` is:

```yaml
auth:
  mode: none   # trusted network only, see authentication.md
```

Everything else takes its default value. A typical production file:

```yaml
server:
  listen: ":8080"
  base_url: "https://lum.example.com"

auth:
  mode: oidc
  oidc:
    issuer: "https://auth.example.com/realms/example"
    client_id: "lum"
    client_secret: "${OIDC_CLIENT_SECRET}"

github:
  token: "${GITHUB_TOKEN}"

scheduler:
  check_cron: "0 7 * * *"

notifications:
  - type: ntfy
    url: "https://ntfy.example.com/lum"
    on: [update_available, job_failed]

agents:
  shared_secret: "${AGENT_BOOTSTRAP_SECRET}"

scripts:
  dir: "/data/scripts"

proxmox:
  url: "https://pve.example.com:8006"
  token_id: "lum@pve!lum"
  token_secret: "${PROXMOX_TOKEN_SECRET}"
```

| Field | Default | Rules |
|---|---|---|
| `server.listen` | `:8080` | HTTP listen address |
| `server.base_url` | — | absolute http(s) public URL; **required in `oidc` mode** (callback URL `<base_url>/auth/callback`) |
| `server.log_level` | `info` | `debug`, `info`, `warn`, `error` |
| `auth.mode` | `oidc` | `oidc` or `none` (trusted network only; a warning is logged) |
| `auth.oidc.issuer` | — | required with `oidc`, http(s) URL |
| `auth.oidc.client_id` | — | required with `oidc` |
| `auth.oidc.client_secret` | — | required with `oidc`, secret |
| `auth.session_ttl` | `12h` | absolute lifetime of a web UI session (see [authentication.md](authentication.md)) |
| `github.token` | — | optional, secret (60 requests/hour without, 5000 with) |
| `github.cache_ttl` | `15m` | how long a GitHub response is reused without any request; `0s` = revalidate (ETag) every time |
| `scheduler.check_cron` | `0 7 * * *` | standard 5-field cron; shortcuts such as `@daily` are accepted |
| `scheduler.jitter` | `5m` | maximum random delay before scheduled checks; `0s` disables it |
| `scheduler.concurrency` | `5` | simultaneous checks, ≥ 1 |
| `notifications[].type` | — | `ntfy`, `webhook` or `telegram` |
| `notifications[].url` | — | http(s) URL, secret |
| `notifications[].chat_id` | — | Telegram recipient, required for `telegram` and forbidden otherwise (see [checks-notifications.md](checks-notifications.md#telegram)) |
| `notifications[].on` | — | at least one event among `update_available`, `job_success`, `job_failed`, `agent_offline` |
| `agents.shared_secret` | — | agent enrollment secret; without it, no agent can enroll |
| `agents.heartbeat_interval` | `60s` | > 0 |
| `agents.offline_after` | `5m` | must be > `heartbeat_interval` |
| `agents.enrollment` | `open` | `open` or `closed` (rejects any new enrollment) |
| `agents.self_update` | `auto` | `auto` (an agent whose binary differs from the one served by the server is updated) or `off` (see [agent.md](agent.md)) |
| `scripts.dir` | `/data/scripts` | central script repository; a relative path is **relative to the config directory** |
| `proxmox.url` | — | optional: Proxmox API for snapshots, http(s) URL (see [proxmox.md](proxmox.md)) |
| `proxmox.token_id` | — | required with `proxmox.url`, in the form `user@realm!name` |
| `proxmox.token_secret` | — | required with `proxmox.url`, secret |
| `proxmox.fingerprint` | — | SHA-256 fingerprint of the node certificate (`AB:CD:…`, 32 bytes); empty = system certificate authorities |

Defining `apps:` in `server.yaml` is an error. An explicit value, even zero (`jitter: 0s`), always overrides the default.

## `apps.yaml`

A minimal, working app definition: Sonarr, whose installed version is read from its own API and compared with the latest GitHub release.

```yaml
apps:
  - id: sonarr
    name: "Sonarr"
    lxc_id: 101
    upstream:
      type: github
      repo: "Sonarr/Sonarr"
    current:
      type: api
      url: "http://192.0.2.15:8989/api/v3/system/status"
      auth_header: "X-Api-Key"
      token: "${SONARR_API_KEY}"
      json_path: "$.version"
```

The same app with every optional section: one-click updates through Sonarr's own API, a healthcheck, and a Proxmox snapshot before each update.

```yaml
apps:
  - id: sonarr
    name: "Sonarr"
    lxc_id: 101
    upstream:
      type: github
      repo: "Sonarr/Sonarr"
      include_prerelease: false
    current:
      type: api
      url: "http://192.0.2.15:8989/api/v3/system/status"
      auth_header: "X-Api-Key"
      token: "${SONARR_API_KEY}"
      json_path: "$.version"
    update:
      type: api
      method: POST
      url: "http://192.0.2.15:8989/api/v3/command"
      auth_header: "X-Api-Key"
      token: "${SONARR_API_KEY}"
      body: '{"name": "ApplicationUpdate"}'
      timeout: 15m
    healthcheck:
      url: "http://192.0.2.15:8989/ping"
      expect_status: 200
      timeout: 120s
    snapshot_before_update: true
    rollback_on_failure: false
    retention: 2
```

### App fields

| Field | Default | Rules |
|---|---|---|
| `id` | — | required, unique, kebab-case; used as the key for history in the database |
| `name` | the `id` | display name |
| `lxc_id` | — | required, integer > 0 (Proxmox container ID) |
| `agent` | — | agent ID, kebab-case; required for `current.type: agent` and `update.type: script` |
| `upstream` | — | required: where the latest version comes from |
| `current` | — | required: how to read the installed version |
| `update` | — | optional: how to update (without it, the app is monitored only) |
| `healthcheck` | — | optional: checked after an update |
| `snapshot_before_update` | `false` | take a Proxmox snapshot before updating (requires the `proxmox` section) |
| `rollback_on_failure` | `false` | restore the snapshot automatically if the update fails; a rollback restores the whole container |
| `retention` | `2` | snapshots kept after successful updates, ≥ 1 |

### `upstream`

| Field | Default | Rules |
|---|---|---|
| `type` | — | `github` |
| `repo` | — | `owner/name` |
| `include_prerelease` | `false` | include pre-releases |
| `tag_regex` | `^v?(?P<version>\d+\.\d+(\.\d+){0,2})$` | must contain the named group `(?P<version>…)`, which yields the canonical version |

In YAML double-quoted strings, backslashes must be doubled: `tag_regex: "^(?P<version>\\d+\\.\\d+\\.\\d+)$"`.

### `current`

The `type` field determines which fields are allowed. A field belonging to the other type is rejected.

| Type | Fields | Rules |
|---|---|---|
| `api` | `url`, `json_path`, `auth_header`, `token` | `url` http(s); valid `json_path` (e.g. `$.version`); `auth_header` and `token` go together |
| `agent` | `command`, `output_regex` | `command` = non-empty argv array; `output_regex` with the `version` group; the app must have an `agent` |

Prefer `api` when the application exposes its version over HTTP: no agent is needed. The `current` type is independent of the `update` type: an app can read its version through `api` and update through `script`. Known endpoints:

| Application | `url` | `json_path` | Auth |
|---|---|---|---|
| Sonarr, Radarr | `http://<ip>:8989/api/v3/system/status` (Radarr: `:7878`) | `$.version` | `X-Api-Key` |
| Prowlarr | `http://<ip>:9696/api/v1/system/status` (API **v1**) | `$.version` | `X-Api-Key` |
| Vaultwarden | `https://<domain>/api/version` (response: `"1.34.3"`) | `$` | none |

For an application that does not expose its version over HTTP, use an agent:

```yaml
    agent: vaultwarden-lxc
    current:
      type: agent
      command: ["/opt/vaultwarden/bin/vaultwarden", "--version"]
      output_regex: "(?P<version>\\d+\\.\\d+\\.\\d+)"
```

For Vaultwarden installed by community-scripts, if you prefer the `api` type, go through your reverse proxy: the service itself serves https with a self-signed certificate on `:8000`, which LUM rejects.

### `update`

| Type | Fields | Rules |
|---|---|---|
| `api` | `url`, `method` (default `POST`), `auth_header`, `token`, `body` | `method` ∈ GET, POST, PUT, PATCH; `auth_header` and `token` go together |
| `script` | `script` | plain `*.sh` file name (no path) present in `scripts.dir`; the app must have an `agent` |

In both cases, `timeout` defaults to `15m` and must be > 0.

`resources` (optional) gives the container more CPU and memory for the duration of the update, for example for an application that recompiles itself:

```yaml
    update:
      type: script
      script: "myapp.sh"
      timeout: 60m
      resources:
        cores: 4        # number of cores, ≥ 1
        memory: 4096    # MiB, ≥ 16
```

At least one of the two fields is required. LUM only raises values that are below the target, then restores the original values. This requires the `proxmox` section of `server.yaml` and two more permissions on the token: see [proxmox.md](proxmox.md).

### `healthcheck`

| Field | Default | Rules |
|---|---|---|
| `url` | — | required, http(s) URL |
| `expect_status` | `200` | HTTP status code (100–599) |
| `timeout` | `120s` | > 0 |

## Hot reload

A reload re-reads both files:
- if it succeeds, the new configuration atomically replaces the old one;
- if it fails, **the old configuration stays active**, and the error is shown in the web UI (System status page).

There are three triggers: `systemctl reload lum-server` (SIGHUP), `POST /api/v1/config/reload`, or the "Reload" button on the System status page. The check schedule and the GitHub client are rebuilt if their settings changed. `server.listen`, `server.log_level`, `server.base_url` and `auth.*` are read only at startup and require a restart.
