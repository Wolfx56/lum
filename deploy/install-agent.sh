#!/usr/bin/env bash
# LUM: installs (or updates) lum-agent in an LXC container.
# Served by the LUM server on /install-agent.sh, with the server URL already filled in:
#   curl -fsSL https://lum.example.com/install-agent.sh | bash -s -- --id sonarr-lxc --secret <secret>
# The secret can also come from the LUM_AGENT_SECRET variable (kept out of ps and the shell history):
#   curl -fsSL https://lum.example.com/install-agent.sh | LUM_AGENT_SECRET=<secret> bash -s -- --id sonarr-lxc
# Documentation: doc/agent.md.
set -euo pipefail

SERVER='__LUM_SERVER_URL__'
ID=""
SECRET="${LUM_AGENT_SECRET:-}"
# Everything of the agent lives in /opt/lum-agent: binary, systemd unit, environment, token, scripts.
AGENT_DIR=/opt/lum-agent
UNIT="$AGENT_DIR/lum-agent.service"
OLD_STATE_DIR=/etc/lum-agent # location used by older installs

die() { echo "install-agent: $*" >&2; exit 1; }
usage() { echo "usage: install-agent.sh --id <agent-id> [--secret <secret>] [--server <url>]"; }

while [[ $# -gt 0 ]]; do
	case "$1" in
	--id) ID="${2:-}"; shift 2 ;;
	--secret) SECRET="${2:-}"; shift 2 ;;
	--server) SERVER="${2:-}"; shift 2 ;;
	-h | --help) usage; exit 0 ;;
	*) usage >&2; die "unknown option: $1" ;;
	esac
done

[[ $EUID -eq 0 ]] || die "must be run as root"
[[ "$ID" =~ ^[a-z0-9]+(-[a-z0-9]+)*$ ]] || die "--id is required, in kebab-case (the agent field of the app definitions)"
[[ "$SERVER" == https://* || "$SERVER" == http://localhost* || "$SERVER" == http://127.0.0.1* ]] || die "--server must be an https:// URL"
command -v curl >/dev/null || die "curl is required"
command -v sha256sum >/dev/null || die "sha256sum is required"
command -v systemctl >/dev/null || die "systemd is required"

case "$(uname -m)" in
x86_64 | amd64) ARCH=amd64 ;;
aarch64 | arm64) ARCH=arm64 ;;
*) die "unsupported architecture: $(uname -m)" ;;
esac

install -d -m 755 "$AGENT_DIR"
# Older install: reuse its token so the agent does not have to enroll again.
if [[ -s "$OLD_STATE_DIR/token" && ! -s "$AGENT_DIR/token" ]]; then
	install -m 600 "$OLD_STATE_DIR/token" "$AGENT_DIR/token"
	echo "Token reused from $OLD_STATE_DIR/token"
fi
# ID change: the token is bound to the old ID and would be refused (401).
# Set it aside; the agent enrolls again with the secret.
OLD_ID="$(sed -n 's/^LUM_AGENT_ID=//p' "$AGENT_DIR/env" 2>/dev/null || true)"
if [[ -s "$AGENT_DIR/token" && -n "$OLD_ID" && "$OLD_ID" != "$ID" ]]; then
	[[ -n "$SECRET" ]] || die "the agent was enrolled as $OLD_ID: --secret is required to enroll it again as $ID"
	mv "$AGENT_DIR/token" "$AGENT_DIR/token.$OLD_ID.old"
	echo "Agent ID changed ($OLD_ID -> $ID): old token moved to $AGENT_DIR/token.$OLD_ID.old, enrolling again with the secret"
fi

# Download authentication: the agent token if the agent is already enrolled,
# otherwise the enrollment secret.
AUTH=()
if [[ -s "$AGENT_DIR/token" ]]; then
	AUTH=(-H "Authorization: Bearer $(tr -d '[:space:]' <"$AGENT_DIR/token")" -H "X-Agent-ID: $ID")
elif [[ -n "$SECRET" ]]; then
	AUTH=(-H "Authorization: Bearer $SECRET")
else
	die "--secret is required for a first enrollment"
fi

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
echo "Downloading lum-agent ($ARCH) from $SERVER..."
code="$(curl -sS "${AUTH[@]}" -D "$TMP/headers" -o "$TMP/lum-agent" -w '%{http_code}' "$SERVER/agent/download/lum-agent-linux-$ARCH")" ||
	die "server unreachable: check DNS and firewall (curl $SERVER/healthz must answer ok)"
if [[ "$code" != 200 ]]; then
	# On error, the body is a short text message from the server (or the reverse proxy).
	body="$(head -c 200 "$TMP/lum-agent" 2>/dev/null | tr -d '\0' | tr -s '\r\n' ' ' | sed 's/ *$//' || true)"
	case "$code" in
	401) hint="secret or token refused: the secret differs from AGENT_BOOTSTRAP_SECRET, agents.enrollment is closed, or the agent was revoked" ;;
	404)
		if [[ "$body" == "no agent binary"* ]]; then
			hint="lum-agent-linux-$ARCH is missing from /opt/lum/agent on the server, or the lum user cannot read it (see journalctl -u lum-server)"
		else
			hint="the request does not reach LUM: check the reverse proxy routing"
		fi
		;;
	*) hint="unexpected server response" ;;
	esac
	die "download refused (HTTP $code: ${body:-empty response}). Likely cause: $hint"
fi
expected="$(awk 'tolower($1) == "x-checksum-sha256:" { print $2 }' "$TMP/headers" | tr -d '\r')"
actual="$(sha256sum "$TMP/lum-agent" | cut -d' ' -f1)"
[[ -n "$expected" && "$expected" == "$actual" ]] || die "SHA-256 checksum mismatch (got $actual, expected ${expected:-?})"

install -m 755 "$TMP/lum-agent" "$AGENT_DIR/lum-agent"
(
	umask 077
	{
		echo "LUM_AGENT_ID=$ID"
		echo "LUM_SERVER_URL=$SERVER"
		if [[ ! -s "$AGENT_DIR/token" && -n "$SECRET" ]]; then echo "LUM_AGENT_SECRET=$SECRET"; fi
	} >"$AGENT_DIR/env"
)

cat >"$UNIT" <<'UNIT'
__LUM_AGENT_SERVICE__
UNIT
chmod 644 "$UNIT"
# A unit copied into /etc/systemd/system by a manual install would
# prevent linking the one in /opt/lum-agent.
SYSTEM_UNIT=/etc/systemd/system/lum-agent.service
if [[ -e "$SYSTEM_UNIT" && ! -L "$SYSTEM_UNIT" ]]; then
	systemctl disable -q lum-agent 2>/dev/null || true
	rm -f "$SYSTEM_UNIT"
fi
systemctl link -q "$UNIT" 2>/dev/null || [[ -L "$SYSTEM_UNIT" ]] || die "systemctl link $UNIT failed"
systemctl daemon-reload
systemctl enable -q lum-agent
systemctl restart lum-agent
rm -rf "$OLD_STATE_DIR"

# Wait for enrollment, then remove the secret, which is no longer needed.
for _ in $(seq 1 20); do
	if [[ -s "$AGENT_DIR/token" ]] && systemctl is-active --quiet lum-agent; then
		sed -i '/^LUM_AGENT_SECRET=/d' "$AGENT_DIR/env"
		echo "lum-agent installed and connected (agent $ID, $("$AGENT_DIR/lum-agent" --version))."
		exit 0
	fi
	sleep 1
done
journalctl -u lum-agent -n 20 --no-pager || true
die "the agent did not enroll: see the logs above (journalctl -u lum-agent)"
