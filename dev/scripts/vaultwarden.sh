#!/usr/bin/env bash
# Demo script (dev): simulates an update that takes ~20 s, with continuous
# output, then "installs" the target version in a file read by
# current.command (dev/config/apps.yaml). Nothing else is changed.
set -euo pipefail
echo "Simulated update of ${LUM_APP_NAME:-vaultwarden}: ${LUM_CURRENT_VERSION:-?} → ${LUM_TARGET_VERSION:-?}"
trap 'echo "Interrupted (SIGTERM): nothing was installed" >&2; exit 143' TERM
for step in download verify build install; do
	for i in 1 2 3 4 5; do
		echo "[${step}] ${i}/5"
		sleep 1
	done
done
echo "a demo warning" >&2
echo "Vaultwarden ${LUM_TARGET_VERSION}" > /tmp/lum-dev-vaultwarden-version
echo "Done"
