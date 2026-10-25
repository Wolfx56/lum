# Getting started

This guide gets a LUM server running in about ten minutes, monitoring one application. At the end you will see, on the Dashboard, whether your app is up to date with its latest GitHub release.

The scenario below uses no agent, no Proxmox access and no authentication. It is meant for a first look; the [next steps](#next-steps) section shows how to go further.

## What you need

- A Linux machine (or LXC container) to run the server. It needs outbound HTTPS access to `api.github.com` and HTTP access to the application you want to monitor.
- An application that exposes its version over HTTP. This guide uses [Sonarr](https://sonarr.tv), whose API returns its version at `/api/v3/system/status`. You need its API key (in Sonarr: *Settings → General → Security → API Key*).

## 1. Get the server binary

**Option A: download a release.** Download `lum-server-linux-amd64` (or `-arm64`) and `SHA256SUMS` from the [releases page](https://github.com/Wolfx56/lum/releases), check it and make it executable:

```bash
sha256sum --ignore-missing -c SHA256SUMS
mv lum-server-linux-amd64 lum-server && chmod +x lum-server
```

**Option B: build from source.** You need Go (the version listed in `go.mod`), Node.js LTS, pnpm and `make`:

```bash
git clone https://github.com/Wolfx56/lum.git
cd lum
make deps     # Go modules and web UI dependencies
make build    # bin/lum-server (web UI embedded) and bin/lum-agent
```

To build Linux binaries from another OS, use `make build-linux LINUX_ARCH=amd64` (or `arm64`): the binaries land in `bin/linux-amd64/`.

The binaries are static: copy `lum-server` to the target machine, nothing else is required.

> Always build with `make build`. A plain `go build ./cmd/server` produces a server without the web UI.

## 2. Write a minimal configuration

Create a working directory with a `config` folder:

```bash
mkdir -p ~/lum/config
cd ~/lum
```

`config/server.yaml`:

```yaml
server:
  listen: ":8080"

auth:
  mode: none
```

> **`auth.mode: none` disables authentication entirely.** Anyone who can reach port 8080 can see the Dashboard and trigger actions. Use it only on a trusted network, for this first test. For any real deployment, switch to OIDC (see [authentication.md](authentication.md)).

`config/apps.yaml` (replace `192.0.2.15` with your Sonarr address, and `101` with the Proxmox ID of the container it runs in):

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

- `upstream` tells LUM where to find the latest version: the latest release of the `Sonarr/Sonarr` GitHub repository.
- `current` tells LUM how to read the installed version: call Sonarr's API with the key in the `X-Api-Key` header, and take the `version` field of the JSON response.
- The API key is not written in the file: `${SONARR_API_KEY}` is resolved from the environment or from the `.env` file.

`config/.env`:

```bash
SONARR_API_KEY=paste-your-sonarr-api-key-here
```

Protect it: `chmod 600 config/.env`.

## 3. Run the server

```bash
./lum-server --config-dir ./config --env-file ./config/.env --data-dir ./data
```

- `--data-dir` is where the SQLite database (`lum.db`) is created. The default is `/data`, which is why this example points it to a local folder.
- Logs are JSON on stdout. You should see `configuration loaded`, a warning that authentication is disabled, `server listening`, then a `check completed` summary: the server checks every app as soon as it starts.

If the configuration has a problem, the server refuses to start and lists every problem with its path, for example:

```
missing environment variables: SONARR_API_KEY (apps[0].current.token)
```

## 4. Open the Dashboard

Open `http://<server-address>:8080` in a browser. The Dashboard lists Sonarr with its installed version, the latest available version and its state:

| State | Meaning |
|---|---|
| `up_to_date` | the installed version is the latest (or newer) |
| `update_available` | a newer release exists on GitHub; the link opens its release notes |
| `unknown` | one of the two versions could not be read |
| `error` | the check failed; the message says why (wrong API key, unreachable app, GitHub rate limit…) |

The "Check now" button runs a new check immediately. Otherwise, LUM checks every day at 7:00 (`scheduler.check_cron`).

To add more applications, add entries to `apps.yaml`, then reload with the "Reload" button on the System status page (or restart the server).

## Next steps

- **Run it as a service**: systemd unit, reverse proxy, backups: [deployment.md](deployment.md).
- **Configuration reference**: every key of `server.yaml` and `apps.yaml`: [configuration.md](configuration.md).
- **Get notified**: ntfy, webhook or Telegram when a new version appears: [checks-notifications.md](checks-notifications.md).
- **Avoid the GitHub rate limit**: without a token, GitHub allows 60 requests per hour; set `github.token` to get 5000 (see [version-detection.md](version-detection.md)).
- **Snapshots before updates**: give LUM a Proxmox API token so it can snapshot a container before an update and roll it back on failure: [proxmox.md](proxmox.md).
- **Apps without an HTTP version endpoint, and script updates**: install the agent in the container: [agent.md](agent.md) and [scripts.md](scripts.md).
- **Trigger updates from the web UI**: [updates.md](updates.md).
- **Secure access**: OIDC login (Keycloak or another provider): [authentication.md](authentication.md).
