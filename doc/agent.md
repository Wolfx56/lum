# Agent (`lum-agent`)

The agent is a small program you install in a container when LUM must read the installed version with a command (`current.type: agent`) or update the app with a script (`update.type: script`). Apps that expose their version and update themselves through an HTTP API (the \*arr apps, for example) do not need one.

The agent is a static Go binary. It opens **no port**: it opens an outbound WebSocket connection to the server itself, keeps it open and reconnects on its own.

What the agent does:
- enrolls, then connects with its own token;
- sends heartbeats, so the server can detect offline agents;
- reads the installed version (`get_version`);
- receives the update scripts (`sync_scripts`);
- runs scripts, streams their output live, and cancels them (`run_update`, `cancel_job`, see [scripts.md](scripts.md));
- updates itself (`self_update`).

## Connection and TLS

```
lum-agent ──wss://lum.example.com/agent/ws──▶ reverse proxy (TLS) ──ws://──▶ lum-server :8080
```

- **TLS is mandatory**: the agent refuses an `http://` URL, except to `localhost` (development). It checks the server certificate against the system CAs, so you need a publicly trusted certificate (Let's Encrypt, for example).
- **The reverse proxy** terminates TLS; the example in [deployment.md](deployment.md#reverse-proxy-haproxy-example) works as is (`timeout tunnel 1h`). The agents' containers must be able to reach `lum.example.com`: if the proxy filters by source IP, add their subnets.
- **`/agent/ws`** is not behind the OIDC login: the agent authenticates itself in its first message (see below).

## Enrollment and token

1. **First start**: the agent has no token. It presents its ID and the shared secret (`agents.shared_secret` on the server, `LUM_AGENT_SECRET` on the agent).
2. **The server enrolls it**: it generates a random per-agent token, sends it to the agent and only keeps its SHA-256 hash (`agents` table).
3. **The agent stores the token** in `/opt/lum-agent/token` (mode `0600`) and stops using the shared secret.
4. **Later connections** use the token only.

Rules:
- **`agents.enrollment: closed`** refuses any new enrollment; already enrolled agents keep connecting with their token. Close it once all agents are installed.
- **Enrolling again**: if the token file is deleted and `LUM_AGENT_SECRET` is set, the agent enrolls again (only while enrollment is open). Its old token then stops working.
- **Token refused** (revoked or unknown): the agent does **not** fall back to the secret; it retries less and less often (up to every 60 s) and says so in its logs (`refused by the server`). A revoked agent cannot enroll again until it has been deleted (see [Agents page](#agents-page)).
- **ID**: kebab-case (`vaultwarden-lxc`). It is the value of the `agent` field of the app definitions in `apps.yaml`.
- **One connection per agent**: a new connection with the same ID replaces the old one.

## Heartbeat and offline state

Every `agents.heartbeat_interval` (60 s by default, sent by the server on connection), the agent sends its version, host name, container uptime and the checksums of its scripts.

With no heartbeat for `agents.offline_after` (5 min by default), the server marks the agent `offline` and sends the `agent_offline` notification (once, until the agent comes back). A short disconnection followed by a quick reconnection, for example when the server restarts, does not trigger a notification.

**Reconnection**: after a disconnection, the agent retries after 1 s, then 2 s, 4 s… up to 60 s between attempts, with ±20 % jitter so that all agents do not come back at the same time. The delay goes back to 1 s as soon as a connection is accepted.

## Installed version (`get_version`)

For an app definition with `current.type: agent`, on every check, the server sends `current.command` to the app's agent:

```yaml
    current:
      type: agent
      command: ["/opt/vaultwarden/bin/vaultwarden", "--version"]
      output_regex: "(?P<version>\\d+\\.\\d+\\.\\d+)"
```

- The command runs **without a shell** (no `|`, `$VAR` or `~`), as root, with a 25 s timeout. For a pipeline: `["sh", "-c", "…"]`.
- The server applies `output_regex` to standard output, then to standard error if nothing matches (some tools print their version there).
- A non-zero exit code is an error, shown with the first line of standard error.
- **Agent not connected**: the installed version is unknown; the app is in state `unknown` (not `error`) with the reason "no connected agent".

## Scripts (`sync_scripts`)

The source of truth is `scripts.dir` on the server (`/data/scripts`). The server indexes every `*.sh` (SHA-256 checksum, 1 MiB max) and watches the directory (inotify): a change is picked up in less than a second.

**Targeted sync**: an agent only receives the scripts referenced by the `update.type: script` definitions of its apps. It writes them to `/opt/lum-agent/scripts/` and **deletes any other `*.sh`** in that directory.

Sync happens:
- when the agent connects;
- when a script changes on the server;
- when the configuration is reloaded (an app definition may have changed);
- when a heartbeat reports a diverging copy (a file changed or deleted by hand in the container).

Nothing is sent if the agent already has the right versions. The agent checks the checksum of every script it receives: if a single one does not match, it writes nothing. Each file is replaced atomically. The state is recorded in the `agent_scripts` table.

## Installation

Server-side prerequisites: the agent binaries in `/opt/lum/agent` (see [deployment.md](deployment.md#agent-binaries)), and `agents.enrollment: open`.

In the container, as root (`curl` and systemd required):

```bash
curl -fsSL https://lum.example.com/install-agent.sh | bash -s -- --id vaultwarden-lxc --secret <AGENT_BOOTSTRAP_SECRET>
```

To keep the secret out of `ps` and the shell history, pass it through the environment instead:

```bash
read -rs LUM_AGENT_SECRET && export LUM_AGENT_SECRET
curl -fsSL https://lum.example.com/install-agent.sh | bash -s -- --id vaultwarden-lxc
```

The script, served by LUM with the server URL already filled in:
1. detects the architecture (`amd64`, `arm64`), downloads the binary from the server, authenticating with the secret, and checks its SHA-256 checksum;
2. installs it as `/opt/lum-agent/lum-agent`;
3. writes `/opt/lum-agent/env` (`0600`: ID, server URL, secret) and the unit `/opt/lum-agent/lum-agent.service` ([`deploy/lum-agent.service`](../deploy/lum-agent.service)), registered with systemd by `systemctl link` (a plain symlink in `/etc/systemd/system`);
4. starts the agent, waits for enrollment, then **removes the secret** from `/opt/lum-agent/env`.

The whole agent lives in `/opt/lum-agent`:

| File | Mode | Contents |
|---|---|---|
| `lum-agent` | 755 | the binary |
| `lum-agent.service` | 644 | the systemd unit |
| `env` | 600 | `LUM_AGENT_ID`, `LUM_SERVER_URL` (and `LUM_AGENT_SECRET` until enrollment) |
| `token` | 600 | the agent token |
| `scripts/` | 700 | copies of the update scripts |

Uninstall: `systemctl disable --now lum-agent && rm -rf /opt/lum-agent`.

Running the script again on an enrolled agent reinstalls it without the secret: the download then authenticates with the agent token. This is also how to migrate an older install (unit copied to `/etc/systemd/system`, token in `/etc/lum-agent`): the token is moved to `/opt/lum-agent`, the old unit is replaced by the symlink, and `/etc/lum-agent` is removed.

The ID (`--id`) must match the `agent` field of the app definitions. Otherwise the agent connects but receives no script: the server only sends an agent the scripts of the app definitions whose `agent` field is exactly its ID. On the Agents page, the agent then shows online with no app, and the ID expected by the app definition stays "not enrolled".

**Changing the ID.** Run the script again with the new `--id` and the secret. The token is bound to the old ID: the script detects it (from `LUM_AGENT_ID` in `/opt/lum-agent/env`), moves it to `/opt/lum-agent/token.<old-id>.old` and enrolls the agent again. Then revoke and delete the old agent on the Agents page.

`lum-agent` options (set by the unit):

| Option | Default | Purpose |
|---|---|---|
| `--id` | — | agent ID (`agent` field of the app definitions) |
| `--server` | — | server URL, `https://…` |
| `--token-file` | `/opt/lum-agent/token` | token, written on enrollment |
| `--scripts-dir` | `/opt/lum-agent/scripts` | local copies of the scripts |
| `--log-level` | `info` | `debug`, `info`, `warn`, `error` |
| `--version` | — | print the version and exit |
| `LUM_AGENT_SECRET` (env) | — | enrollment secret, used while there is no token |

The secret is read from the environment rather than from an option, so that it does not show in `ps`.

## Installation troubleshooting

When it fails, `install-agent.sh` prints the HTTP status, the server's answer and the likely cause.

| Symptom | Cause and fix |
|---|---|
| `server unreachable: check DNS and firewall` | The container cannot reach the server URL. Run `curl https://lum.example.com/healthz` from the container, then see "Containers on an isolated network" below. |
| `download refused (HTTP 401: …)` | Secret refused: it differs from `AGENT_BOOTSTRAP_SECRET`, or `agents.enrollment` is `closed`. For an already enrolled agent: its token was revoked (see "Delete" on the Agents page). Older versions of the script also got a 401 after an `--id` change: delete `/opt/lum-agent/token` and run again with the secret. |
| `download refused (HTTP 404: no agent binary for amd64)` | The secret is accepted, but the server cannot find `/opt/lum/agent/lum-agent-linux-amd64`. The server logs the reason (`agent binary unavailable`, in `journalctl -u lum-server`). |
| `download refused (HTTP 404: …)` with another message | The request does not reach LUM: check the virtual host routing in the reverse proxy. |
| `the agent did not enroll` | The binary is installed, but the WebSocket connection fails. The last 20 lines of `journalctl -u lum-agent` are printed. Check the proxy's tunnel timeout (`timeout tunnel` in HAProxy, see [deployment.md](deployment.md#reverse-proxy-haproxy-example)). |

**Binary present but unreadable.** The server runs as the `lum` user, which must be able to traverse `/opt/lum` and `/opt/lum/agent`. A directory created with mode `700`, for example by `scp` or by `mkdir` with a strict umask, hides the binary. The server then answers 404 and logs `permission denied`. To check, then fix, on the server:

```bash
namei -l /opt/lum/agent/lum-agent-linux-amd64
chmod 755 /opt/lum /opt/lum/agent
sudo -u lum test -r /opt/lum/agent/lum-agent-linux-amd64 && echo readable
```

The directory is read again on every request: no server restart needed.

**Containers on an isolated network.** The agent requires TLS to the server (`https://`); plain `http://` is only accepted to localhost. So it always goes through the reverse proxy, even when the LUM server is on the same network. If your containers sit on an isolated bridge that cannot reach the network where DNS resolves `lum.example.com`, there are two options:

- **The reverse proxy has an address on that bridge** (`<proxy-ip-on-isolated-bridge>`), and listens on it. In each container, before installing:
  ```bash
  echo "<proxy-ip-on-isolated-bridge> lum.example.com" >> /etc/hosts
  curl -fsS https://lum.example.com/healthz
  ```
  The certificate stays valid, since the name is unchanged, and no firewall change is needed. Proxmox only rewrites the `# --- BEGIN PVE ---` block of `/etc/hosts`, so the added line is kept. Do not make this change in your main DNS: machines that cannot reach the isolated bridge would lose access.
- **Otherwise**, allow on your router or firewall only the flow from `<isolated-bridge>` to the reverse proxy on TCP 443, placed before the rule that blocks the rest. Keep in mind that every isolated container then reaches every virtual host of the proxy, and that if this traffic is masqueraded, they all show up with the same source address.

If the proxy filters access to the LUM virtual host by source address, include these networks.

## Agent self-update (`self_update`)

The server compares the checksum of each agent's binary (reported on connection and with every heartbeat) with that of the binary for its architecture in `/opt/lum/agent`. If they differ, and no script is running on the agent, it sends `self_update`:
1. the agent downloads the new binary (authenticated with its token) and checks its checksum and size;
2. it replaces its executable atomically;
3. it exits: systemd (`Restart=always`) restarts it right away, with the new version.

Updating the agents therefore just means putting the new binaries on the server (in `/opt/lum/agent`, see [deployment.md](deployment.md#agent-binaries)): they update at the latest on their next heartbeat. After a failure, the next attempt happens 10 min later at the earliest. `agents.self_update: off` disables this mechanism.

## Monitoring

- **Logs**: JSON on standard output (`journalctl -u lum-agent`); on the server, with the `agent_id` field.
- **Metric** `lum_agents{status="online|offline|revoked"}` on `/metrics`.
- **Metric** `lum_agents_connected`: agents connected right now.

## Agents page

The **Agents** page of the web UI (and `GET /api/v1/agents`) lists the enrolled agents, and the ones named in `apps.yaml` that never connected ("not enrolled", with the install command to copy). For each one:
- **state**: online, offline, revoked or not enrolled; last contact, host name, version;
- **binary**: up to date, or different from the server's (`self_update` pending or disabled);
- **apps** from `apps.yaml` that use the agent;
- **scripts** it must hold, with their sync state: up to date (`in_sync`), stale (`stale`, checksum differs) or missing (`missing`).

Actions, all recorded in the audit log:
- **Sync**: pushes the agent's scripts now, even if they look up to date. Sync is automatic anyway (connection, heartbeat, script change); the button is for checking or forcing.
- **Revoke**: the agent token is refused from now on, and its connection is closed. Use it when a container is compromised or deleted. A script running on that agent is no longer followed: its job fails after `update.timeout`.
- **Delete** (revoked agents only): LUM forgets the agent. It can then enroll again with the shared secret, if `agents.enrollment: open`: delete `/opt/lum-agent/token` in the container, then run `install-agent.sh` again with the secret.
