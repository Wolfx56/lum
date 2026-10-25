# Checks, history and notifications

The server regularly checks the versions of all applications (the rules are described in [version-detection.md](version-detection.md)). Every result is recorded, and a notification is sent when a new version appears.

> Checks can be followed in the web UI (Dashboard, per-app history, System status page) and in the logs. The "Check now" button exists for all applications at once or for a single one (see [api.md](api.md)).

## When checks run

- **At server startup**: a full check runs immediately, so the Dashboard is populated right away.
- **According to `scheduler.check_cron`** (default `0 7 * * *`, every day at 7:00, server local time): each trigger first waits a random delay between 0 and `scheduler.jitter` (default 5 min), so that all APIs are not hit at a fixed time.
- **Never two at once**: if a scheduled check is still running when the next one is due, the next one is skipped.

The cron is built into the server; no system cron is needed.

## How a full check runs

- Applications are checked **in parallel**, at most `scheduler.concurrency` at a time (default 5).
- For each app, the installed version and the latest version are read simultaneously, with a limit of **60 s** per app.
- **One failure does not affect the others** (timeout, 401, GitHub unavailable): the affected app goes to `error`, with its message, and the others continue.
- Two checks of the same app (scheduled and manual, for example) never overlap: the second waits for the first to finish.

At the end, the server logs a summary:

```json
{"level":"INFO","msg":"check completed","apps":20,"duration":"3.2s","up_to_date":15,"update_available":3,"unknown":1,"error":1}
```

Failures are logged individually (`app check failed`, with `app_id` and the error) at `warn` level.

## What is recorded

The SQLite database `<data-dir>/lum.db` (flag `--data-dir`, default `/data`) contains, among other things:

| Table | Contents |
|---|---|
| `version_checks` | one row per check: date, versions, state, error, release link and notes. **Kept for 90 days**: purged at startup, then every night at 3:30. |
| `app_state` | the current state of each app: last state, versions, date, error, release link, last notified version. This is what the Dashboard shows. |
| `audit_log` | the log of write actions (see [api.md](api.md#audit-log)) |
| `agents`, `jobs`, `job_logs` | agents, update jobs and their logs (see [agent.md](agent.md) and [updates.md](updates.md)) |

The YAML configuration is never copied into the database. An app removed from `apps.yaml` keeps its rows but is no longer checked.

The database runs in WAL mode, so it can be read while the server is running:

```bash
sqlite3 /data/lum.db "SELECT app_id, status, current_version, latest_version FROM app_state"
```

## Notifications

An `update_available` notification is sent **only once per new version**:
- when an app moves to `update_available`;
- or, if the app is already in `update_available`, when an even newer version appears.

The last notified version is remembered (`app_state.last_notified_version`). A check that finds the same version again, or a server restart, does not notify again.

If a channel fails, the error is logged (`notification failed`) and the other channels still receive the notification. The version is still marked as notified, so that a broken channel does not cause the others to be notified again at every check.

Each channel only receives the events listed in its `on`. Emitted events: `update_available`, `agent_offline` (agent without a heartbeat for `agents.offline_after`, see [agent.md](agent.md)), `job_success` and `job_failed` (end of an update, see [updates.md](updates.md)). For `job_failed`, the message is the failure reason.

### ntfy

JSON publish to the ntfy server. The configured URL is the topic URL: `https://ntfy.example.com/<topic>`. Parameters such as `?auth=` are kept.

```yaml
notifications:
  - type: ntfy
    url: "https://ntfy.example.com/lum"
    on: [update_available, job_success, job_failed, agent_offline]
```

| ntfy field | Value |
|---|---|
| title | `Sonarr: update available` |
| message | `4.0.9.2244 → 4.0.10.2544` |
| tag | ⬆️ (`arrow_up`) |
| click | opens the release notes |

### Webhook

`POST` JSON to the configured URL (for example a Home Assistant webhook); any 2xx response is a success.

```json
{
  "event": "update_available",
  "app_id": "sonarr",
  "app_name": "Sonarr",
  "current_version": "4.0.9.2244",
  "latest_version": "4.0.10.2544",
  "release_url": "https://github.com/Sonarr/Sonarr/releases/tag/v4.0.10.2544",
  "title": "Sonarr: update available",
  "message": "4.0.9.2244 → 4.0.10.2544",
  "time": "2026-10-18T07:03:12Z"
}
```

### Telegram

Message sent by a bot through the `sendMessage` API: bold title, versions, then a link to the release notes.

```yaml
notifications:
  - type: telegram
    url: "https://api.telegram.org/bot${TELEGRAM_BOT_TOKEN}/sendMessage"
    chat_id: "123456789"
    on: [update_available, job_failed]
```

Setup:
1. Create the bot with [@BotFather](https://t.me/BotFather) (`/newbot`) and put its token in `TELEGRAM_BOT_TOKEN` (in the `.env` file).
2. Send any message to the bot (or add it to the target group), then open `https://api.telegram.org/bot<token>/getUpdates`: the `chat_id` is the `message.chat.id` value.
3. Set `chat_id`: a numeric ID, `-100…` for a group or channel, or `@channel_name` for a public channel where the bot is an administrator. Quote it.

If Telegram rejects the message, its reason is included in the log, for example `notifications[0] (telegram): unexpected status 400: Bad Request: chat not found`: most of the time, the bot has never received a message from that chat.

The `webhook` channel does not work for Telegram: the API expects `chat_id` and `text`, not LUM's JSON.

Notification URLs are secrets (the Telegram one contains the bot token): they never appear in logs or errors. A channel is referred to by its position, for example `notifications[1] (webhook): unexpected status 500`.
