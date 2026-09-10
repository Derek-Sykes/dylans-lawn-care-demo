#!/usr/bin/env bash
set -euo pipefail
[[ $EUID == 0 ]] || { echo 'Run with sudo on the prepared Oracle server.' >&2; exit 1; }
cd "$(dirname "$0")/.."
for command in docker curl git python3 flock timeout; do command -v "$command" >/dev/null; done
docker compose version >/dev/null
docker network inspect xsolutions-proxy >/dev/null
install -d -o root -g root -m 755 /opt/dylan-demo /usr/local/share/dylan-demo
install -d -o root -g root -m 700 /var/lib/dylan-demo-deploy
install -o root -g root -m 644 compose.production.yaml /usr/local/share/dylan-demo/compose.yaml
install -o root -g root -m 755 server/update-release.sh /usr/local/sbin/dylan-demo-update
install -o root -g root -m 644 server/dylan-demo-update.service /etc/systemd/system/dylan-demo-update.service
install -o root -g root -m 644 server/dylan-demo-update.timer /etc/systemd/system/dylan-demo-update.timer
systemctl daemon-reload
systemctl enable --now dylan-demo-update.timer
echo 'Dylan demo updater installed. The prepared shared proxy must route demo.xsolutionsmd.com to dylan-demo:8080.'
