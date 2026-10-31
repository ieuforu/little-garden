# Deploy to Alibaba Cloud with existing Caddy

The deliverable is one Go app image, with SQLite on a persistent Docker volume. The frontend is embedded. Compose also provides bounded logs, CPU and memory, a read-only root filesystem, non-root execution and health checks.

## One-time preparation

1. Confirm the VPS region, Linux version, CPU architecture, available memory and disk, and whether Caddy runs on the host or in Docker. Install Docker Engine and Compose v2 from the vendor's instructions if needed. The deploy script also uses Bash and `flock` (util-linux).
2. Clone this repository into a suitable server directory such as `/opt/little-garden`. Do not replace an existing Caddy deployment or other applications.
3. Choose the garden subdomain and add a Turnstile widget restricted to that hostname. Use real production keys. Configure a named Cloudflare Tunnel with that subdomain; a temporary quick tunnel is not the production configuration.
4. Copy `deploy/.env.example` to `deploy/.env`, fill in `PUBLIC_ORIGIN`, `SESSION_SECRET`, `TURNSTILE_SITE_KEY`, `TURNSTILE_SECRET_KEY`, and the exact trusted proxy network. Generate the session secret **on your computer or server** with `openssl rand -hex 32`. Keep the real file mode 0600.
5. Check that `172.30.21.0/28` does not overlap an existing Docker/VPN network; change both `GARDEN_SUBNET` and `TRUSTED_PROXIES` if needed. The default app port is `127.0.0.1:18080`. Bindings on all public interfaces are not used.

Do not paste real tokens into issues, commits or pull requests. `.gitignore` and `.dockerignore` exclude actual environment files, data and backups. Public Turnstile site keys are intentionally exposed through the API; its secret stays server-side.

### Caddy on the host

Add the block in `deploy/Caddyfile.host.example` to the existing Caddy configuration. It listens only on `127.0.0.1:18180` and proxies the loopback-published app at `127.0.0.1:18080`.

Run cloudflared on the host and configure the named tunnel's service as `http://127.0.0.1:18180`. Validate Caddy's full existing configuration before reloading it. Confirm the app sees the expected Docker proxy peer and restrict `TRUSTED_PROXIES` accordingly.

### Caddy in Docker

Deploy the app to create `little-garden-ingress`. Attach the **existing Caddy container** to that dedicated network, for example:

```bash
docker network connect little-garden-ingress YOUR_EXISTING_CADDY_CONTAINER
```

Persist that network attachment in the existing Caddy Compose project. Add `deploy/Caddyfile.docker.example` to its configuration, validate it, and reload. Inside Docker, Caddy proxies `garden:8080`, not `127.0.0.1:18080`.

Run cloudflared on the same dedicated network, and configure its service as `http://YOUR_CADDY_CONTAINER_NAME:18180`. An optional overlay is included as `deploy/compose.tunnel.yaml`; it needs a pinned `CLOUDFLARED_IMAGE` and a real token file at `/opt/little-garden-secrets/tunnel-token`, owned by UID 65532 and mode 0400 so the non-root connector can read it. Keep the containing host directory private. Use a named tunnel, not a quick tunnel. Start/manage this connector separately from app updates:

```bash
export GARDEN_IMAGE=$(cat .local/deploy/current)
export CLOUDFLARED_IMAGE=cloudflare/cloudflared:YOUR_VERIFIED_VERSION
docker compose --project-directory deploy -f deploy/compose.yaml -f deploy/compose.tunnel.yaml up -d cloudflared
```

Do not publish the tunnel-only Caddy port. The optional connector does not change or install Caddy for you. Keep access to that Docker network limited to these trusted services.

### Edge setup

Protect `/api/v1/session` and `/api/v1/flowers` with rate/WAF rules supported by the actual Cloudflare account. Bypass caching for `/api/*`, `/healthz`, `/readyz` and WebSocket upgrades. Hashed `/assets/*` files can be cached. Ensure no existing public Caddy listener or server security-group rule exposes an alternate garden origin path.

## Versioned updates

The `Release image` GitHub Actions workflow runs on `v*` tags, validates the source and builds `linux/amd64` and `linux/arm64` images in GHCR. The first image package may need its visibility configured in GitHub package settings; a public source repository does not automatically guarantee an anonymously pullable package.

After a release exists:

```bash
cd /opt/little-garden
./scripts/deploy.sh v0.1.0
```

The script takes a deployment lock, pulls the image, records its immutable image ID, validates Compose, makes a verified backup of existing data, starts the app and waits for health. It keeps the prior successful image ID. Updates briefly disconnect WebSockets; clients reconnect with snapshots.

The script assumes you have deliberately updated this checkout's deployment files to the intended release. It does not run a destructive `git reset` or modify the existing Caddy configuration.

### GHCR is unreachable from the server

On a machine that can pull the correct architecture's image:

```bash
docker pull --platform linux/amd64 ghcr.io/ieuforu/little-garden:v0.1.0
docker save -o little-garden-v0.1.0.tar ghcr.io/ieuforu/little-garden:v0.1.0
sha256sum little-garden-v0.1.0.tar > little-garden-v0.1.0.tar.sha256
```

Use `linux/arm64` instead for an ARM server. Transfer both files over your normal SSH connection. On the VPS, verify and load them:

```bash
sha256sum -c little-garden-v0.1.0.tar.sha256
docker load -i little-garden-v0.1.0.tar
./scripts/deploy.sh --local v0.1.0
```

No image download is attempted in `--local` mode. You can also build the image locally from the checked-out source with the command in the README.

## Backups

```bash
cd /opt/little-garden
./scripts/backup.sh
```

The backup uses SQLite `VACUUM INTO` and an integrity check, including committed WAL data. A one-off container can back up the volume even if the main app is stopped. The verified file is copied into `backups/` on the host, with mode 0600, and also remains in the backup volume. Do not copy a live `.db` file without its WAL or substitute a raw filesystem copy for this command.

Schedule this command daily using the server's own scheduler and transfer backups to a separate destination such as an OSS bucket. **Scheduling, offsite storage and retention have not been configured by this repository.** Decide retention and disk alerts during first deployment. Keep the image ID/release alongside a backup so its schema can be restored by a compatible binary.

## Rollback and restore

```bash
./scripts/deploy.sh --rollback
```

Rollback checks the database's actual schema version against the older image's supported schema. It never automatically restores an old database, so newly planted flowers are not silently discarded. A failed update attempts the same compatible-image rollback. Future schema changes must bump the schema number when they break older images.

For a deliberate database restore, stop the app, preserve the current data volume, restore a verified snapshot into a **new volume**, and point a compatible image at that volume. Do not overwrite a running database or leave old `-wal`/`-shm` files alongside a restored snapshot. Confirm the restored flower count before reopening ingress. The Go integration suite verifies backup recovery into a separate database; the real server volume/ownership restore must be rehearsed before launch.

Never run `docker compose down -v` as an update or rollback step: it removes the persistent volumes.

## Remaining launch inputs

- Actual domain/subdomain, VPS region, OS, architecture and capacity.
- Existing Caddy location and network configuration.
- Cloudflare account access for named Tunnel, hostname, Turnstile and edge rules.
- Server-side secret files and a verified backup destination/retention policy.

No deployment has occurred merely by creating or merging this repository. Live domain verification, direct-origin checks, VPS load tests and browser checks are separate acceptance steps.
