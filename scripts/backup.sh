#!/usr/bin/env bash
set -euo pipefail
root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
state="$root/.local/deploy"
if [[ -z ${GARDEN_IMAGE:-} ]]; then
  [[ -f "$state/current" ]] || { echo 'No recorded deployment.' >&2; exit 1; }
  GARDEN_IMAGE=$(cat "$state/current")
fi
export GARDEN_IMAGE
compose=(docker compose --project-directory "$root/deploy" -f "$root/deploy/compose.yaml")
mkdir -p -- "$state"
exec 8>"$state/backup.lock"
flock -n 8 || { echo 'Another backup is running.' >&2; exit 1; }
destination="$root/backups"
mkdir -p -- "$destination"
chmod 700 "$destination"
name="garden-$(date -u +%Y%m%dT%H%M%SZ)-${RANDOM}.db"
job="little-garden-backup-$$-${RANDOM}"
trap 'docker rm -f "$job" >/dev/null 2>&1 || true' EXIT
# A one-off process can back up SQLite even when the app container is stopped.
"${compose[@]}" run -T --name "$job" --no-deps garden backup --output "/backups/$name"
docker cp "$job:/backups/$name" "$destination/$name"
chmod 600 "$destination/$name"
echo "Backup saved: $destination/$name"
# Backups are never restored automatically. Retention is an explicit operator policy.
