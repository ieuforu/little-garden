#!/usr/bin/env bash
set -euo pipefail
root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
state="$root/.local/deploy"
mkdir -p -- "$state"
command -v flock >/dev/null || { echo 'Install util-linux (flock) first.' >&2; exit 1; }
exec 9>"$state/lock"
flock -n 9 || { echo 'Another deployment is running.' >&2; exit 1; }
docker compose version >/dev/null
[[ -f "$root/deploy/.env" ]] || { echo 'Copy deploy/.env.example to deploy/.env and fill in the domain and keys.' >&2; exit 1; }
chmod 600 "$root/deploy/.env"
compose=(docker compose --project-directory "$root/deploy" -f "$root/deploy/compose.yaml")
old=''
[[ ! -f "$state/current" ]] || old=$(cat "$state/current")
rollback=false
local_image=false
if [[ ${1:-} == --rollback ]]; then
  [[ -f "$state/previous" ]] || { echo 'No previous image recorded.' >&2; exit 1; }
  target=$(cat "$state/previous")
  rollback=true
else
  if [[ ${1:-} == --local ]]; then local_image=true; shift; fi
  version=${1:-}
  [[ $version =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[a-zA-Z0-9.-]+)?$ ]] || { echo 'Usage: scripts/deploy.sh [--local] v0.1.0 | --rollback' >&2; exit 1; }
  target="ghcr.io/ieuforu/little-garden:$version"
  if ! $local_image; then docker pull "$target"; fi
  # Record the actual image ID, so a moved tag cannot change a rollback target.
  target=$(docker image inspect --format '{{.Id}}' "$target")
fi
export GARDEN_IMAGE="$target"
"${compose[@]}" config --quiet
if [[ -n "$old" ]]; then
  GARDEN_IMAGE="$old" "$root/scripts/backup.sh"
fi
schema_compatible() {
  local image=$1 supported actual
  supported=$(docker run --rm --network none --read-only --cap-drop ALL "$image" schema)
  actual=$(GARDEN_IMAGE="$target" "${compose[@]}" run --rm --no-deps garden db-version)
  [[ $supported =~ ^[0-9]+$ && $actual =~ ^[0-9]+$ && $supported == "$actual" ]]
}
if $rollback && ! schema_compatible "$target"; then
  echo 'Rollback stopped: the previous image does not support the current database schema.' >&2; exit 1
fi
healthy() {
  local id
  id=$("${compose[@]}" ps -q garden)
  [[ -n "$id" ]] && docker exec "$id" /app/garden healthcheck >/dev/null 2>&1
}
"${compose[@]}" up -d --no-build --wait --wait-timeout 60 garden && started=true || started=false
if ! $started || ! healthy; then
  echo 'New image is not healthy.' >&2
  if [[ -n "$old" ]] && schema_compatible "$old"; then
    export GARDEN_IMAGE="$old"
    "${compose[@]}" up -d --no-build --wait --wait-timeout 60 garden
    echo 'Restored the previous compatible image; database was not restored.' >&2
  else
    echo 'Automatic image rollback is unavailable or schema-incompatible. Inspect logs and docs/deployment.md.' >&2
  fi
  exit 1
fi
if [[ -n "$old" && "$old" != "$target" ]]; then
  printf '%s\n' "$old" > "$state/previous.tmp"
  mv -- "$state/previous.tmp" "$state/previous"
fi
printf '%s\n' "$target" > "$state/current.tmp"
mv -- "$state/current.tmp" "$state/current"
echo "Garden deployed: $target"
