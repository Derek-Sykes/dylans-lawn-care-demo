#!/usr/bin/env bash
# Only installs the separately reviewed dev deployment runtime; never dispatches a release.
set -euo pipefail
[[ $EUID == 0 ]] || { echo 'Run with sudo on the prepared server.' >&2; exit 1; }
cd "$(dirname "$0")/../.."
for command in docker curl python3 systemctl; do command -v "$command" >/dev/null; done
docker compose version >/dev/null
docker network inspect xsolutions-proxy >/dev/null
[[ $(uname -m) == aarch64 ]] || { echo 'This installation expects the ARM64 server.' >&2; exit 1; }
for path in /opt/dylan-dev /var/lib/dylan-dev-deploy /usr/local/share/dylan-dev; do
  [[ ! -L "$path" ]] || { echo 'Installation directories must not be symlinks.' >&2; exit 1; }
done
was_active=false
if systemctl is-active --quiet dylan-dev-update.timer; then was_active=true; fi
if systemctl cat dylan-dev-update.timer >/dev/null 2>&1; then systemctl stop dylan-dev-update.timer; fi
if systemctl cat dylan-dev-update.service >/dev/null 2>&1; then systemctl stop dylan-dev-update.service; fi
install -d -o root -g root -m 700 /opt/dylan-dev /var/lib/dylan-dev-deploy
install -d -o root -g root -m 755 /usr/local/share/dylan-dev
install -o root -g root -m 644 compose.dev-server.yaml /usr/local/share/dylan-dev/compose.yaml
install -o root -g root -m 644 Caddyfile.local /usr/local/share/dylan-dev/Caddyfile.local
install -o root -g root -m 755 server/dev/update-release.py /usr/local/sbin/dylan-dev-update
install -o root -g root -m 644 server/dev/dylan-dev-update.service /etc/systemd/system/dylan-dev-update.service
install -o root -g root -m 644 server/dev/dylan-dev-update.timer /etc/systemd/system/dylan-dev-update.timer
systemctl daemon-reload
if [[ "$was_active" == true ]]; then systemctl start dylan-dev-update.timer; fi
echo 'Dev deployment runtime installed. Existing runtime.env and database were preserved.'
echo 'For a new installation: prepare the private runtime.env and HTTPS route, then enable --now dylan-dev-update.timer.'
