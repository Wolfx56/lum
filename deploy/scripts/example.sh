#!/usr/bin/env bash
# LUM — update script template (conventions: deploy/scripts/README.md).
# Example: an app shipped as a GitHub release archive with a SHA-256 checksum file.
# Copy it to scripts.dir (/data/scripts), rename it to <app>.sh and adapt the variables.
set -euo pipefail

SERVICE=myapp
INSTALL_DIR=/opt/myapp
REPO=owner/myapp
VERSION="${LUM_TARGET_VERSION:?LUM_TARGET_VERSION is missing}"
ASSET="myapp-${VERSION}-linux-amd64.tar.gz"
BASE="https://github.com/${REPO}/releases/download/v${VERSION}"

log() { echo "[$(date -Iseconds)] $*"; }

# Whatever happens, the temporary directory is removed and the service restarted on exit.
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"; systemctl is-active --quiet "$SERVICE" || systemctl start "$SERVICE"' EXIT

log "Updating ${LUM_APP_NAME:-$SERVICE}: ${LUM_CURRENT_VERSION:-?} -> $VERSION"
curl -fsSL -o "$TMP/$ASSET" "$BASE/$ASSET"
curl -fsSL -o "$TMP/SHA256SUMS" "$BASE/SHA256SUMS"
(cd "$TMP" && grep " ${ASSET}\$" SHA256SUMS | sha256sum -c -)

log "Stopping $SERVICE"
systemctl stop "$SERVICE"
tar -xzf "$TMP/$ASSET" -C "$INSTALL_DIR"
log "Starting $SERVICE"
systemctl start "$SERVICE"
log "Update finished"
