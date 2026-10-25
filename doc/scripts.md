# Update scripts

This page explains how LUM updates an app with a shell script run inside its container. You need it for apps that cannot update themselves through an HTTP API, for example apps installed with the Proxmox community-scripts. It requires an [agent](agent.md) in the container.

The app definition delegates the update to a script, run by the agent in the container:

```yaml
    agent: vaultwarden-lxc
    update:
      type: script
      script: "vaultwarden.sh"
      timeout: 60m
```

## Where scripts live

**The server is the source of truth**: `scripts.dir` (`/data/scripts`), ideally a Git clone to keep its history. Agents only hold copies, synced automatically (see [agent.md](agent.md#scripts-sync_scripts)): each agent only receives the scripts of its app definitions, in `/opt/lum-agent/scripts/`.

To add or change a script, put it in `/data/scripts` (executable, `chmod 755`). The change is pushed to the agents in less than a second. Examples and template: [`deploy/scripts/`](../deploy/scripts/README.md).

## Execution (`run_update`)

When an update starts (see [updates.md](updates.md)):

1. **Pre-checks**: the agent is connected and its copy of the script is up to date, otherwise it is synced. Agent offline: the job is refused before any snapshot.
2. Snapshot of the container if the app definition asks for one.
3. Right before running, the server checks the SHA-256 checksum of the agent's copy again, and sends it with the order. **The agent refuses to run a script whose local checksum does not match.**
4. The agent runs the script as root, with no intermediate shell and no terminal, in the `/` directory, with the agent's environment plus these variables:

   | Variable | Example |
   |---|---|
   | `LUM_APP_ID` | `vaultwarden` |
   | `LUM_APP_NAME` | `Vaultwarden` |
   | `LUM_CURRENT_VERSION` | `1.34.3` (version installed before the update) |
   | `LUM_TARGET_VERSION` | `1.35.0` (latest available version) |
   | `LUM_JOB_ID` | job ID |

5. **Output**: every line of stdout and stderr is sent to the server in real time and added to the job logs (`stdout` / `stderr` streams). For readability, ANSI color sequences are removed, a line rewritten with `\r` (progress bar) only keeps its last state, and a line longer than 8 KiB is truncated.
6. **Exit code**: `0` = success, followed by the health check and the version check. Any other code fails the job (`script vaultwarden.sh exited with code 3`).

**Network outage while the script runs**: the script keeps running. The agent buffers its output in memory (up to 20,000 lines) and sends it, along with the script's end, as soon as it reconnects. The job is only lost if `update.timeout` expires in the meantime.

**Server restart while the script runs**: the job becomes `failed (server_restart)`. The script itself runs to the end in the container: stopping it in the middle of an update would be worse. The new server ignores its end. Check the state of the app (a version check is enough).

## Cancellation (`cancel_job`)

Cancelling a job sends **SIGTERM** to the script and all its child processes (`cargo`, `curl`…), then **SIGKILL** 30 s later if they are still running. The job ends as `cancelled`. The snapshot is kept, with no automatic rollback: the app may be in an intermediate state; check it before deciding to roll back.

From the web UI: the cancel button on the job page (`POST /jobs/{id}/cancel`). The same happens automatically when `update.timeout` expires.

## Writing a script

Conventions, detailed in [`deploy/scripts/README.md`](../deploy/scripts/README.md):
- `#!/usr/bin/env bash` and `set -euo pipefail`;
- idempotent, non-interactive, logging to stdout;
- downloads verified by checksum;
- services stopped and started with `systemctl`, with a `trap` that restarts the service on failure;
- handle SIGTERM cleanly (cancellation).

To test a script by hand in the container, the way the agent runs it:

```bash
LUM_APP_ID=vaultwarden LUM_APP_NAME=Vaultwarden LUM_CURRENT_VERSION=1.34.3 LUM_TARGET_VERSION=1.35.0 LUM_JOB_ID=test \
  /opt/lum-agent/scripts/vaultwarden.sh
```
