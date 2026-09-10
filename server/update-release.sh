#!/usr/bin/env bash
# Installed root-owned by an administrator. Release downloads contain data only.
set -euo pipefail
umask 077
[[ $EUID == 0 ]] || { echo 'Run the installed updater as root.' >&2; exit 1; }
repo=Derek-Sykes/dylans-lawn-care-demo
root=/opt/dylan-demo
state=/var/lib/dylan-demo-deploy
mkdir -p "$state"
exec 9>"$state/update.lock"
flock -n 9 || exit 0
work=$(mktemp -d "$state/check.XXXXXX")
candidate="dylan-demo-candidate-$$"
deployment_started=false
committed=false
had_previous=false
had_current=false
compose=(docker compose --project-name dylan-demo --project-directory "$root" --env-file "$root/release.env" -f "$root/compose.yaml")
next_compose=(docker compose --project-name dylan-demo --project-directory "$root" --env-file "$work/next.env" -f "$work/next-compose.yaml")
rollback() {
  if [[ "$had_previous" == true ]]; then
    echo 'New release failed; restoring the previous demo container definition.' >&2
    cp "$work/previous-compose.yaml" "$root/compose.yaml" || return 1
    cp "$work/previous.env" "$root/release.env" || return 1
    "${compose[@]}" up -d --no-build --wait --wait-timeout 90 || return 1
  else
    echo 'First release failed; removing only its unsuccessful demo container.' >&2
    "${next_compose[@]}" rm --stop --force web || return 1
    rm -f -- "$root/compose.yaml" "$root/release.env" || return 1
  fi
  if [[ "$had_current" == true ]]; then
    cp "$work/previous-current.json" "$state/current.json.new" || return 1
    mv "$state/current.json.new" "$state/current.json" || return 1
  else
    rm -f -- "$state/current.json" "$state/current.json.new" || return 1
  fi
}
cleanup() {
  status=$?
  trap - EXIT
  trap '' TERM INT
  recovered=true
  if [[ "$deployment_started" == true && "$committed" != true ]]; then
    if ! rollback; then
      recovered=false
      status=1
      echo "Automatic recovery failed; recovery files retained in $work. Inspect dylan-demo-update.service immediately." >&2
    fi
  fi
  docker rm -f -v "$candidate" >/dev/null 2>&1 || true
  if [[ "$recovered" == true ]]; then rm -rf -- "$work"; fi
  exit "$status"
}
trap cleanup EXIT
trap 'exit 143' TERM
trap 'exit 130' INT
current_main() { timeout 30 git ls-remote "https://github.com/$repo.git" refs/heads/main | cut -f1; }
main=$(current_main)
[[ "$main" =~ ^[0-9a-f]{40}$ ]] || { echo 'Cannot determine current main revision.' >&2; exit 1; }
if [[ -f "$state/current.json" ]] && python3 -c 'import json,sys; sys.exit(json.load(open(sys.argv[1]))["revision"] != sys.argv[2])' "$state/current.json" "$main"; then exit 0; fi
status=$(curl -LsS --connect-timeout 15 --max-time 60 --retry 2 -w '%{http_code}' \
  "https://github.com/$repo/releases/download/release-$main/deployment.json?check=$(date +%s)" -o "$work/deployment.json")
if [[ "$status" == 404 ]]; then echo 'Current main is still building; keeping the existing demo.'; exit 0; fi
[[ "$status" == 200 ]] || { echo "Release download failed (HTTP $status)." >&2; exit 1; }
mapfile -t release < <(python3 - "$work/deployment.json" <<'PY'
import json,re,sys
from pathlib import Path
p=Path(sys.argv[1])
assert p.stat().st_size < 4096, 'Oversized release manifest'
d=json.loads(p.read_text())
assert isinstance(d,dict) and set(d) == {'revision','image'}, 'Invalid manifest fields'
assert re.fullmatch(r'[0-9a-f]{40}',d['revision']), 'Invalid revision'
assert re.fullmatch(r'ghcr.io/derek-sykes/dylans-lawn-care-demo@sha256:[0-9a-f]{64}',d['image']), 'Invalid image'
print(d['revision']); print(d['image'])
PY
)
[[ ${#release[@]} == 2 ]] || { echo 'Invalid release manifest.' >&2; exit 1; }
revision=${release[0]}
image=${release[1]}
[[ "$revision" == "$main" ]] || { echo 'Waiting for a release of the current main revision.'; exit 0; }
docker network inspect xsolutions-proxy >/dev/null
docker pull "$image"
[[ $(docker image inspect "$image" --format '{{.Architecture}}') == arm64 ]]
[[ $(docker image inspect "$image" --format '{{index .Config.Labels "org.opencontainers.image.revision"}}') == "$revision" ]]
[[ $(docker image inspect "$image" --format '{{index .Config.Labels "org.opencontainers.image.source"}}') == "https://github.com/$repo" ]]
docker run --rm --tmpfs /data --tmpfs /config --entrypoint caddy "$image" validate --config /etc/caddy/Caddyfile --adapter caddyfile
# The candidate uses a random loopback port and no public proxy network.
docker run -d --name "$candidate" --read-only --tmpfs /tmp --tmpfs /data --tmpfs /config -p 127.0.0.1::8080 "$image"
port=$(docker port "$candidate" 8080/tcp | awk -F: '{print $NF}')
[[ "$port" =~ ^[0-9]+$ ]]
ready=false
for attempt in $(seq 1 30); do
  if [[ $(docker inspect "$candidate" --format '{{.State.Health.Status}}') == healthy ]]; then ready=true; break; fi
  sleep 1
done
[[ "$ready" == true ]]
actual=$(curl -fsS --max-time 10 "http://127.0.0.1:$port/version.json" | python3 -c 'import json,sys; print(json.load(sys.stdin)["revision"])')
[[ "$actual" == "$revision" ]]
curl -fsS --max-time 10 "http://127.0.0.1:$port/" -D "$work/candidate.headers" -o "$work/candidate.html"
test -s "$work/candidate.html"
grep -Eiq '^x-robots-tag:.*noindex' "$work/candidate.headers"
docker rm -f -v "$candidate" >/dev/null
[[ "$revision" == "$(current_main)" ]] || { echo 'A newer main commit arrived; keeping the existing demo.'; exit 0; }
if [[ -f "$root/compose.yaml" && -f "$root/release.env" ]]; then
  had_previous=true
  cp "$root/compose.yaml" "$work/previous-compose.yaml"
  cp "$root/release.env" "$work/previous.env"
elif [[ -e "$root/compose.yaml" || -e "$root/release.env" ]]; then
  echo 'Incomplete prior deployment configuration; repair before updating.' >&2
  exit 1
elif [[ -n $(docker ps -aq --filter label=com.docker.compose.project=dylan-demo) ]]; then
  echo 'A demo container exists without recovery files; repair before updating.' >&2
  exit 1
fi
if [[ -f "$state/current.json" ]]; then
  cp "$state/current.json" "$work/previous-current.json"
  had_current=true
fi
cp /usr/local/share/dylan-demo/compose.yaml "$work/next-compose.yaml"
printf 'DYLAN_IMAGE=%s\n' "$image" > "$work/next.env"
"${next_compose[@]}" config --quiet
deployment_started=true
cp "$work/next-compose.yaml" "$root/compose.yaml"
cp "$work/next.env" "$root/release.env"
"${compose[@]}" up -d --no-build --wait --wait-timeout 90
verified=false
for attempt in $(seq 1 30); do
  actual=$(curl -fsS --max-time 10 --resolve demo.xsolutionsmd.com:443:127.0.0.1 \
    https://demo.xsolutionsmd.com/version.json | python3 -c 'import json,sys; print(json.load(sys.stdin)["revision"])') || actual=''
  if [[ "$actual" == "$revision" ]] \
    && curl -fsS --max-time 10 --resolve demo.xsolutionsmd.com:443:127.0.0.1 https://demo.xsolutionsmd.com/ -D "$work/live.headers" -o "$work/live.html" \
    && cmp -s "$work/candidate.html" "$work/live.html" \
    && grep -Eiq '^x-robots-tag:.*noindex' "$work/live.headers"; then verified=true; break; fi
  sleep 2
done
[[ "$verified" == true ]]
if [[ "$had_previous" == true ]]; then
  cp "$work/previous-compose.yaml" "$state/previous-compose.yaml"
  cp "$work/previous.env" "$state/previous.env"
fi
cp "$work/deployment.json" "$state/current.json.new"
mv "$state/current.json.new" "$state/current.json"
committed=true
echo "Deployed and verified $revision ($image)."
