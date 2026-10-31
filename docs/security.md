# Abuse prevention

HTTPS/WSS protects the transport. JSON is the protocol. A browser-side shared secret, a binary encoding or a custom encryption layer does not stop automated clients, so none is used as an anti-abuse boundary.

## Edge and origin boundary

The recommended ingress is a **named Cloudflare Tunnel → dedicated private Caddy listener → Go**. The app is only published on host loopback; the tunnel listener must not be published to the internet. Do not add a second public Caddy route to the same garden API. Keep other existing sites' configuration intact.

Enable WAF and route-specific rate rules within the Cloudflare account's actual plan. Apply stricter protection to session creation and planting, and keep viewing lightweight. Cloudflare only inspects the initial WebSocket upgrade, so Go owns connection budgets, message-size limits and slow-client handling after upgrade.

The supplied Caddy snippet overwrites `X-Real-IP` from Cloudflare's connecting-IP header on the **tunnel-only listener**. Go trusts only `X-Real-IP`, and only when its socket peer falls in an explicitly configured proxy CIDR. It ignores client-supplied `X-Forwarded-For` and `CF-Connecting-IP` directly. In host-Caddy mode, confirm the actual peer address Docker presents, then configure its exact CIDR. Never use `0.0.0.0/0` or `::/0`.

Any local process/container attached to that trusted network is within the origin trust boundary. Keep unrelated and untrusted workloads off the dedicated network. A tunnel does not prevent network attacks against an already-known server IP used by other public services.

## Participation

Viewing does not require a cookie or challenge. The first planting asks for a Turnstile token. Go calls Siteverify with a 5-second timeout and checks success, the exact hostname and action `plant`. Tokens are single-use; the app then issues its own 24-hour signed anonymous session. Production refuses test site keys and missing credentials. Verification failures never silently enable writes.

The cookie is Secure, HttpOnly, SameSite=Strict, host-only and uses the `__Host-` prefix. POST and WebSocket requests require an exact allowed Origin. An anonymous session is not a unique human: IP and global budgets also apply, including when a client discards cookies. IPv6 clients are grouped by /64.

## Initial resource budgets

These are conservative starting settings, **not measured server capacity**.

| Resource | Initial budget |
| --- | --- |
| Planting per session | 20/minute on average, burst 3 |
| Planting per IP | 120/minute on average, burst 10 |
| Global planting | 10/second, burst 20 |
| New sessions per IP | 1/minute on average, burst 3 |
| Global new sessions | 1/second, burst 5; at most 8 concurrent verification calls |
| General API requests | 2/second per IP, burst 60; 100/second global, burst 200 |
| Non-WebSocket API concurrency | 64 requests |
| Waiting/active plant writes | 16 |
| Plant JSON / verification JSON | 2 KiB / 4 KiB |
| HTTP headers / header deadline | 8 KiB / 5 seconds |
| WebSockets | 200 global; 8/IP; 2/signed session |
| Upgrade rate | 1 per 5 seconds per IP, burst 5; 5/second global, burst 10 |
| Per-subscriber event queue | 32; disconnect on overflow |
| Application messages received on WS | Rejected; read limit 1 KiB |
| TCP connections | Configured WS budget + 128 |
| Limiter map | 20,000 keys; inactivity expiry; unknown keys fail closed when full |
| Garden storage | 800 plants/garden, up to 30 gardens, SQLite main-file ceiling 64 MiB |
| App container | 256 MiB, 1 CPU, 96 processes/threads; Go soft memory limit 192 MiB |
| App logs | 3 × 5 MiB, error reporting rate-limited |

SQLite's main-file limit does not include WAL, filesystem overhead or backups. Monitor volume free space; move verified backups off the VPS and apply a deliberate retention policy. Backups are not deleted by the deployment script. Server outbound bandwidth also needs an alert: broadcasting one planting to many viewers has a real cost even when the event is small.

Users behind a shared NAT can hit the IP limits together. Tune against the actual VPS and representative traffic. Slowdowns, proxy floods and upstream DDoS are handled in different layers; these application controls do not promise complete bot or DDoS prevention.

## Operator controls

The admin API listens on a mode-0600 Unix socket in the private data volume. There is no public administration route.

```bash
# Once scripts/deploy.sh has saved the current image:
export GARDEN_IMAGE=$(cat .local/deploy/current)
docker compose --project-directory deploy -f deploy/compose.yaml exec -T garden /app/garden admin pause
docker compose --project-directory deploy -f deploy/compose.yaml exec -T garden /app/garden admin resume
docker compose --project-directory deploy -f deploy/compose.yaml exec -T garden /app/garden admin hide FLOWER_ID
docker compose --project-directory deploy -f deploy/compose.yaml exec -T garden /app/garden admin show FLOWER_ID
docker compose --project-directory deploy -f deploy/compose.yaml exec -T garden /app/garden admin ban-flower FLOWER_ID
docker compose --project-directory deploy -f deploy/compose.yaml exec -T garden /app/garden admin new-garden
```

Hiding is reversible. `ban-flower` bans that anonymous session and hides its flowers; creating a different session may bypass that individual ban, so retain the IP/global/edge controls. The CLI currently has no unban command. `new-garden` retains prior data and switches the shared view to a new, empty garden.

## Before the first public launch

Verify the live domain's Siteverify hostname/action, actual trusted-proxy IP handling, no direct public origin route, container restart/persistence, backup restoration, and traffic budgets under load on the real VPS. Check the existing Caddy routes and Alibaba security group without disturbing unrelated services. Configure edge rules and bandwidth/disk alerts in the user's accounts.

No Worker is required for v1. A Worker can later add an edge policy if measured traffic justifies it; the Go coordinator remains authoritative for garden mutations and global write limits.

Primary references: [Turnstile validation](https://developers.cloudflare.com/turnstile/get-started/server-side-validation/), [Cloudflare WebSockets](https://developers.cloudflare.com/network/websockets/), [OWASP WebSocket Security](https://cheatsheetseries.owasp.org/cheatsheets/WebSocket_Security_Cheat_Sheet.html), [SQLite WAL](https://sqlite.org/wal.html).
