# Update scripts

Scripts for `update.type: script`, run by `lum-agent` as root inside the app's LXC. Copy them to the server's `scripts.dir` (`/data/scripts`): the server distributes them to the agents that need them. Full explanation: [doc/scripts.md](../../doc/scripts.md).

| Script | App |
|---|---|
| [`example.sh`](example.sh) | commented template to copy |
| [`vaultwarden.sh`](vaultwarden.sh) | Vaultwarden installed by the community-scripts ProxmoxVE helper |

## Conventions

- **Header**: `#!/usr/bin/env bash` then `set -euo pipefail`. The file must be executable (`chmod 755`).
- **Name**: `<app>.sh`, no spaces, no subdirectory.
- **Idempotent**: run again while the app is already up to date, the script breaks nothing (and succeeds).
- **Log to stdout**: every line shows up in the job logs; errors go to stderr. ANSI colors are stripped on display.
- **Exit code**: `0` = success, any other code = job failure.
- **Verified downloads**: check the checksum (`sha256sum -c`) when upstream publishes one; avoid `curl … | bash` on a moving branch.
- **Services**: stop and start them with `systemctl`. Add a `trap` that restarts the service if the script fails halfway.
- **No interaction**: no prompts (there is no terminal); the run time is bounded by the app definition's `update.timeout`.
- **Clean shutdown**: a cancellation sends SIGTERM to the script and its child processes, then SIGKILL 30 s later.

Variables provided: `LUM_APP_ID`, `LUM_APP_NAME`, `LUM_CURRENT_VERSION`, `LUM_TARGET_VERSION`, `LUM_JOB_ID`.
