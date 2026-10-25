# Updates (jobs)

This page explains what happens when you click Update in LUM: the steps of an update job, what can make it fail, and how to roll back. Read it before enabling updates for an app, to choose its snapshot and rollback settings.

An update is a **job**: it runs in the background on the server, and each of its steps is recorded in the database (`jobs` and `job_logs` tables).

There are two kinds of update:
- **`update.type: api`**: the app updates itself on request (the \*arr apps, for example);
- **`update.type: script`**: the container's agent runs a script from the server, whose output feeds the job logs (see [scripts.md](scripts.md)).

## From the web UI

- **Start**: the Update button on the Dashboard or on the app page. It only shows if the app has an `update` block, is in state `update_available` and has no job in progress. A confirmation reminds you of the target version and of the snapshot, if any. The UI then opens the job page.
- **Follow**: the job page shows the state, the duration and the logs **live** (SSE stream): LUM messages in blue, script output, `stderr` in red. The view follows the end of the logs; scrolling up pauses it (a Follow button resumes). After a network outage, the stream resumes where it stopped.
- **During the job**: the Dashboard shows the job state instead of the button, with a link to its logs.
- **Cancel**: the cancel button on the job page, while it runs.
- **Rollback**: the Rollback button on the page of a finished job that has a snapshot (see [Manual rollback](#manual-rollback)).
- **History**: the **Jobs** page (filters by app and by state), and the last 10 jobs on each app page.

The same actions exist in the API (see [api.md](api.md)). Each one is recorded in the audit log with the user's identity, refusals included.

## Steps

```
pending → snapshotting → running → healthchecking → success
                                                  ↘ failed | rolled_back | cancelled
```

1. **Pre-checks**, before any job is created. The job is refused if:
   - the app does not exist or has no `update` block;
   - it is not in state `update_available` (a recent check must have found a new version);
   - `snapshot_before_update: true` or `update.resources` is set but `server.yaml` has no `proxmox` section;
   - for a script: the agent is not connected (`agent offline`), or its copy of the script cannot be synced;
   - a job, or a manual rollback, is already running for this app: only one active job per app, the API answers `409 job_active`.

   The target version is the latest known version (`latest_version`); the starting version is the installed version read at the last check.
2. **Snapshot** (`snapshotting`), if `snapshot_before_update: true`: `lum-pre-update-…` snapshot of the container (see [proxmox.md](proxmox.md)). **If it fails, the job is cancelled (`cancelled`) and the update does not start.**
3. **Resources**, if the app definition has `update.resources`: LUM raises the container's CPUs and memory that are below the request (see [proxmox.md](proxmox.md#resources-during-the-update)). This step comes after the snapshot, so a rollback also restores the usual resources. **If it fails, the job is cancelled (`cancelled`) and the update does not start.**
4. **Update** (`running`).
   - **Script**: the checksum of the agent's script is checked again, then the agent runs it with the `LUM_*` variables; each output line is added to the job logs. The exit code must be `0`.
   - **API**: the `update` request is sent (method, URL, authentication header, body). Any 2xx answer is accepted. LUM then reads the installed version (`current`) every 10 s until it changes. While the app restarts, read errors are expected: they are written to the job logs without failing it.

   Once the script or the request has finished, LUM restores the original resources, whether the update succeeded or not.
5. **Health check** (`healthchecking`), if the app definition has one: `healthcheck.url` is polled every 5 s until it returns `expect_status`, for at most `healthcheck.timeout`.
6. **Version check**: the installed version must be at least the target version. A version that changed but is still lower (partial update) fails the job.
7. **Outcome**:
   - **success**: `job_success` notification, then old snapshots beyond `retention` are deleted;
   - **failure**: `job_failed` notification. The snapshot is kept. If `rollback_on_failure: true`, LUM restores the snapshot automatically and the job ends as `rolled_back`; otherwise it ends as `failed`, and you can still roll back by hand.

   In every case, the app is checked again at the end of the job, so that the Dashboard shows the new version.

**Time limit**: the whole job must fit in `update.timeout` (15 min by default). Beyond that, it fails with the reason `timeout`, for example when the installed version never changes. The rollback, if any, and the snapshot cleanup then get 10 more minutes.

**Cancellation**: a cancelled job ends as `cancelled`, with no automatic rollback and with its snapshot kept. For a script, the agent sends SIGTERM to the script and its child processes, then SIGKILL 30 s later. For an API update, LUM stops waiting (the app may still finish updating). A script still running when `update.timeout` expires is stopped the same way.

**Server restart**: a running job cannot resume. When the server stops, or at the next startup after a crash, it becomes `failed` with the reason `server_restart`. No rollback is attempted, and a running script goes on to the end in the container. Resources raised for that script stay in place: the command to restore them (`pct set …`) is in the job logs.

## Manual rollback

When an update failed without automatic rollback (`rollback_on_failure: false`), its snapshot is kept and the Rollback button on the job page restores it: `POST /jobs/{id}/rollback`.

- **The whole container** goes back to the snapshot state, data included: anything written since is lost. Proxmox stops the container to restore it, then LUM starts it again if it was running. The confirmation reminds you of this.
- **In the background**: the steps are added to the logs of the same job, visible live. The job ends as `rolled_back`, with the update error kept. If Proxmox fails, the job keeps its state and the error is in its logs; you can try again.
- **Afterwards**: the app is checked again, as after every job. During the rollback, no update can start on the app.
- **Possible for** a finished job (`failed`, `cancelled`, or even `success`) that has a snapshot, as long as that snapshot still exists on Proxmox and no job is running on the app.
- **Refusals**:
  - `no_snapshot`: no snapshot (`snapshot_before_update: false`, or failure before the snapshot);
  - `snapshot_gone`: the snapshot was deleted, by the retention of a more recent successful job or by hand;
  - `already_rolled_back`, `job_not_finished`, `job_active`, `rollback_running`;
  - `proxmox_not_configured` (`proxmox` section removed), `proxmox_error` (Proxmox unreachable).

  The job page shows the reason when the button is not offered.

## Example: Sonarr

```yaml
  - id: sonarr
    lxc_id: 101
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

The \*arr `ApplicationUpdate` command only installs a version if the app sees one on its own update branch: if Sonarr finds nothing, the version does not change and the job fails with `timeout`.

## Job log

Each step writes a line to `job_logs` (`system` stream): snapshot, request and HTTP status, detected version, health check, rollback, snapshot cleanup. URLs go through the same masking as check errors: no API key ever shows there. Logs are kept for 90 days, like the check history.

The server's JSON logs carry `job_id` and `app_id`: `journalctl -u lum-server | grep <job_id>`.
