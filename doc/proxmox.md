# Proxmox: snapshots and resources during updates

This page explains how to connect LUM to Proxmox VE. You need it if you want a snapshot of the container before each update (and the option to roll back), or a temporary CPU/memory raise during updates. Without Proxmox access, LUM still checks versions and runs updates, just without snapshots.

Before an update, LUM takes a snapshot of the container (`snapshot_before_update: true`). If the update fails, the snapshot lets you go back: automatically if `rollback_on_failure: true`, otherwise by hand, with the Rollback button on the job page (see [updates.md](updates.md#manual-rollback)).

LUM can also raise the container's CPUs and memory for the duration of the update (`update.resources`, see [below](#resources-during-the-update)).

LUM drives Proxmox through its **REST API** (port 8006) with an **API token**. Nothing is installed on the host, and Proxmox itself limits what the token can do.

## Configuration

```yaml
# server.yaml
proxmox:
  url: "https://pve.example.com:8006"        # one node of the cluster
  token_id: "lum@pve!lum"                     # user@realm!token_name
  token_secret: "${PROXMOX_TOKEN_SECRET}"     # in .env
  fingerprint: "AB:CD:…:EF"                   # SHA-256 fingerprint of the node certificate (optional)
```

| Field | Purpose |
|---|---|
| `url` | API address of a node. In a cluster, any node: LUM finds the node of each container, even after a migration. |
| `token_id` | token ID, in the form `user@realm!name`. |
| `token_secret` | token secret, always through a variable (`${PROXMOX_TOKEN_SECRET}`). It never shows in the logs or the UI. |
| `fingerprint` | SHA-256 fingerprint of the node certificate. **Set it when the node uses Proxmox's original self-signed certificate**: only the fingerprint is then checked, not the CA. Without it, the certificate must be trusted by the system (Let's Encrypt, for example). |

The section is optional: without it, LUM works, but any update of an app with `snapshot_before_update: true` or with `update.resources` is refused. A hot reload (`systemctl reload lum-server`) picks up changes to this section.

## Creating the token with minimal privileges

On a Proxmox node, as root:

```bash
# Role limited to snapshots
pveum role add LUMSnapshot --privs "VM.Audit,VM.Snapshot,VM.Snapshot.Rollback,VM.PowerMgmt"
# With update.resources, add CPU and memory changes:
# pveum role modify LUMSnapshot --privs "VM.Config.CPU,VM.Config.Memory" --append 1

# Dedicated user, without a password (it only exists for the token)
pveum user add lum@pve --comment "LXC Update Manager"

# Privileges on the managed containers only, one by one…
pveum acl modify /vms/101 --users lum@pve --roles LUMSnapshot
pveum acl modify /vms/102 --users lum@pve --roles LUMSnapshot
# …or through a pool grouping these containers
# pveum pool add lum && pveum pool modify lum --vms 101,102
# pveum acl modify /pool/lum --users lum@pve --roles LUMSnapshot

# Token without privilege separation: it has exactly the privileges of lum@pve
pveum user token add lum@pve lum --privsep 0
```

The last command prints the secret **only once**: copy it to `PROXMOX_TOKEN_SECRET` (LUM's `.env` file). The token ID is `lum@pve!lum`.

| Privilege | Used for |
|---|---|
| `VM.Audit` | finding the container's node, reading its state and its list of snapshots |
| `VM.Snapshot` | creating and deleting snapshots |
| `VM.Snapshot.Rollback` | rolling back to a snapshot |
| `VM.PowerMgmt` | starting the container again after a rollback (Proxmox stops it to restore it) |
| `VM.Config.CPU`, `VM.Config.Memory` | only with `update.resources`: changing the container's CPUs and memory |

The token cannot open a console or change anything else. `VM.Audit` lets it read the containers' configuration.

## Finding the certificate fingerprint

In the Proxmox web UI: *Node → System → Certificates*, *Fingerprint* column of the `pveproxy-ssl.pem` certificate if it exists, otherwise `pve-ssl.pem`. On the command line, on the node:

```bash
f=/etc/pve/local/pveproxy-ssl.pem; [ -f "$f" ] || f=/etc/pve/local/pve-ssl.pem
openssl x509 -in "$f" -noout -fingerprint -sha256 | cut -d= -f2
```

If the node certificate changes (renewal, reinstallation), calls fail with `certificate fingerprint mismatch (got …)`: update `fingerprint` with the fingerprint shown, after checking that it really is your node's.

## Checking the setup

- **At startup**, LUM logs `proxmox API reachable` with the Proxmox version, or the reason for the failure.
- **System status page**: "Proxmox" card (API reachable, version, containers not visible with this token).
- **A container that is not visible** either does not exist, or the token lacks `VM.Audit` on it: add the matching ACL.

**No container visible at all**, while the Proxmox version shows: the token is accepted but has no privileges. This is almost always **privilege separation**. It is on by default, including through the "Privilege Separation" checkbox in the web UI when creating the token. The token then does not inherit the privileges of `lum@pve`. To check, then fix:

```bash
pveum user token list lum@pve                              # privsep = 1: this is the problem
pveum user token modify lum@pve lum --privsep 0            # the token gets the privileges of lum@pve
```

If the containers are still not visible, `lum@pve` itself has no privileges on them. `pveum user permissions lum@pve --path /vms/<vmid>` must list `VM.Audit`. Otherwise, add the ACL (`pveum acl modify /vms/<vmid> --users lum@pve --roles LUMSnapshot`), or check that the container is in the pool targeted by the ACL (`pveum pool list`).

The "Proxmox" card is computed each time the page is shown: no need to restart LUM after the fix.

## Snapshots created by LUM

- **Name**: `lum-pre-update-` followed by the first 12 characters of the job ID, for example `lum-pre-update-1a2b3c4d5e6f`. Proxmox limits names to 40 characters, hence the shortened ID.
- **Description**: full job ID, app, versions (`LUM job <id>: sonarr 4.0.9 -> 4.0.10`).
- **Retention**: after a successful update, LUM only keeps the last `retention` `lum-pre-update-*` snapshots of the container (2 by default, `retention` field of the app definition). Snapshots created by hand are never touched.
- **After a failure**, the snapshot is kept.

A rollback restores **the whole container**, data included: anything written since the snapshot is lost. That is why `rollback_on_failure` defaults to `false`.

**Storage**: snapshots depend on the container's storage (ZFS, LVM-thin, Ceph… but not a directory storage with `raw` volumes). If the storage does not support them, the job is cancelled before any update, with the Proxmox error, for example `snapshot feature is not available`.

## Resources during the update

Some updates need more than the app needs day to day: for example, an update script that rebuilds the app from source and refuses to start below 4 CPUs and 4 GB. Rather than sizing the container for that peak, the app definition can request minimum resources for the duration of the update:

```yaml
    update:
      type: script
      script: "myapp.sh"
      resources: { cores: 4, memory: 4096 }   # memory in MiB
```

How it works:

1. After the snapshot, LUM reads the container's CPUs and memory. It only raises what is below the request, and never lowers anything. A container without a CPU limit keeps all the host's cores. Proxmox applies the change live, without restarting the container.
2. Once the script or the request has finished, LUM restores the original values, before the health check: the app is therefore checked with its usual resources. It also restores them after a failure, a cancellation or a `timeout`.
3. Both operations show in the job logs, for example `Raising LXC 102 resources for the update: 4 CPU, 4096 MiB (instead of 1 CPU, 512 MiB)…`.

**When something goes wrong**:

- **Raise refused**, for example for lack of `VM.Config.CPU`/`VM.Config.Memory`: the job is cancelled before the update, with the Proxmox error.
- **Restore refused**: the job keeps its outcome. The logs give the command to run on the host, for example `pct set 102 --cores 1 --memory 512`. Lowering the memory of a container that is still using it can fail.
- **Server stopped during the update**: the script keeps running in the container, and LUM does not lower its resources under it. The job logs give the command to restore them once the script has finished.

With a snapshot, a rollback also restores the original resources, since the snapshot was taken before the raise.
