# little garden.

**小小一朵，也是一个好开始。**

A shared garden for small, good things. Pick a color, find an empty spot, and leave a flower for the next person.

React + TypeScript / Canvas 2D / Go / SQLite. Frontend assets are embedded into a single Go executable. Production runs in Docker, behind your existing Caddy and a named Cloudflare Tunnel.

## What works

- A responsive, light paper-and-pastel garden: four palettes, deterministic flowers, gentle growth, optional sound, pan and zoom, keyboard planting, reduced motion.
- One shared active garden. Flowers survive refreshes and process restarts. Other visitors see committed plantings over WebSocket.
- Anonymous viewing; a 24-hour, signed, HttpOnly planting session after server-validated Turnstile verification.
- Per-session, per-IP and global request limits; bounded connections, queues, limiter storage, database size and container resources.
- Idempotent planting, consistent snapshot/subscription ordering, reconnect snapshots, slow-client disconnection.
- Admin commands through a private Unix socket; consistent SQLite backups; versioned image deployment and schema-checked image rollback.

The first garden starts with **16 actual, persisted flowers planted by the program**. No visitor counts or recent activity are simulated. The initial release has one active garden; earlier gardens remain in SQLite when the gardener starts a new one. An archive browsing UI is not implemented yet.

## Run locally

Requires Node.js 24, pnpm 11.25.0 and Go 1.26.10. Install Go from [go.dev](https://go.dev/dl/). The first build downloads Go modules.

```bash
git clone https://github.com/ieuforu/little-garden.git
cd little-garden
pnpm install --frozen-lockfile
pnpm dev
```

Open the Vite URL printed in the terminal. The development script builds and runs Go on `127.0.0.1:8081`, with Vite proxying `/api` and WebSocket traffic. Local data is stored in `.local/garden.db`. Restart `pnpm dev` after changing Go code. Vite updates frontend code automatically.

Development explicitly disables Turnstile and only permits Go to bind to a loopback IP. It is for a trusted local machine. Production is the default and refuses to start without the domain, signing secret, and real Turnstile keys.

## Check and build

```bash
pnpm test:web
pnpm test:deploy
pnpm build:web
cd server
go test -race ./...
go vet ./...
cd ..
pnpm build
```

`bin/garden` contains the frontend and backend. Run it with production configuration, or use the explicit loopback-only development environment from `scripts/dev.mjs`. In production there is no Node process.

## Deploy

[Deployment guide](docs/deployment.md) covers the one-time domain / Turnstile / Tunnel / Caddy setup, both host and container Caddy, offline image transfer, backups and recovery.

After that setup and after publishing a matching release tag:

```bash
./scripts/deploy.sh v0.1.0
./scripts/backup.sh
./scripts/deploy.sh --rollback
```

These examples do not imply that `v0.1.0` has already been released. To build and deploy directly from this checkout:

```bash
docker build -f deploy/Dockerfile -t ghcr.io/ieuforu/little-garden:v0.1.0 .
./scripts/deploy.sh --local v0.1.0
```

No script automatically installs system software, changes DNS or firewall rules, modifies an existing Caddyfile, or restores an older database. Those depend on the actual server and are configured once.

## Project map

```text
web/src/
  garden/          world coordinates, deterministic renderer, ordered state
  realtime/        HTTP client, anonymous session and WebSocket lifecycle
  components/      dialog and Turnstile widget
server/
  cmd/garden/      serve, backup, healthcheck and Unix-socket admin CLI
  internal/garden/ SQLite schema, transactions, garden coordinator
  internal/httpapi/ origin checks, cookies, verification, limits and HTTP/WS
  internal/assets/ embedded frontend output
deploy/           Dockerfile, Compose, Caddy and Tunnel examples
scripts/          local development, embedding, backup and deployment
docs/             architecture, security assumptions and operations
```

Read [architecture](docs/architecture.md) for the protocol and [security](docs/security.md) for the limits and remaining deployment checks.
