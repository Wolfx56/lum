#!/usr/bin/env bash
# LUM — Vaultwarden update (LXC installed by the community-scripts ProxmoxVE helper).
# Copy it to scripts.dir (/data/scripts); lum-agent runs it as root inside the LXC.
# Variables provided by LUM: LUM_APP_ID, LUM_APP_NAME, LUM_CURRENT_VERSION,
# LUM_TARGET_VERSION, LUM_JOB_ID. Documentation: doc/scripts.md.
set -euo pipefail

SERVICE=vaultwarden
UPDATE_URL="https://raw.githubusercontent.com/community-scripts/ProxmoxVE/main/ct/vaultwarden.sh"

# Resources announced to the community script: they must be <= those of the LXC,
# otherwise it refuses to update in non-interactive mode (exit 113, "under-provisioned").
export var_cpu="2" var_ram="2048" var_disk="8"
# Non-interactive mode: menus take their default choice ("Update VaultWarden + Web-Vault").
export PHS_SILENT=1 TERM="${TERM:-dumb}"
# Under systemd there is no terminal: `clear` (called by the community script)
# fails with TERM=dumb and aborts the update. Make it a no-op.
clear() { :; }
export -f clear

log() { echo "[$(date -Iseconds)] $*"; }

installed_version() {
	local bin
	for bin in /opt/vaultwarden/bin/vaultwarden /usr/bin/vaultwarden; do
		if [[ -x "$bin" ]]; then
			"$bin" --version 2>/dev/null | grep -oE '[0-9]+\.[0-9]+\.[0-9]+' | head -n1 || true
			return
		fi
	done
}

# Whatever happens (failure, cancellation), the service is restarted on exit.
restart_on_exit() {
	local rc=$?
	if ! systemctl is-active --quiet "$SERVICE"; then
		log "Starting $SERVICE"
		systemctl start "$SERVICE" || rc=1
	fi
	exit "$rc"
}
trap restart_on_exit EXIT

before="$(installed_version)"
log "Updating ${LUM_APP_NAME:-Vaultwarden}: ${before:-?} -> ${LUM_TARGET_VERSION:-latest version}"

# The community script builds Vaultwarden from source (cargo): expect several tens of minutes.
# The vault stays online during the build: the community script only stops the
# service while it replaces the binary, then starts it again.
bash -c "$(curl -fsSL "$UPDATE_URL")"

if ! systemctl is-active --quiet "$SERVICE"; then
	log "Starting $SERVICE"
	systemctl start "$SERVICE"
fi
sleep 5
if ! systemctl is-active --quiet "$SERVICE"; then
	log "ERROR: $SERVICE does not start"
	journalctl -u "$SERVICE" -n 30 --no-pager || true
	exit 1
fi

after="$(installed_version)"
log "Installed version: ${after:-?}"
# The community script exits with code 0 even when it did nothing (installation not found...).
if [[ -z "$after" ]]; then
	log "ERROR: cannot read the Vaultwarden version after the update"
	exit 1
fi
if [[ -n "${LUM_TARGET_VERSION:-}" && "$after" == "$before" && "$after" != "$LUM_TARGET_VERSION" ]]; then
	log "ERROR: version unchanged ($after), expected $LUM_TARGET_VERSION"
	exit 1
fi
log "Update finished"
