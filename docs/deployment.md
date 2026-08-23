# VPS deployment

This deployment targets one small Linux VPS. PostgreSQL and the API run in Docker; Caddy runs on the host and terminates TLS.

## DNS and firewall

1. Point an `A`/`AAAA` record such as `sync.example.com` to the VPS.
2. Allow inbound TCP 22, 80, and 443.
3. Do not expose PostgreSQL.
4. Set `API_BIND=127.0.0.1` so port 43781 is reachable only from the host.

Apple sign-in and secure mobile clients require a publicly trusted HTTPS certificate. A private IP or plain HTTP endpoint is suitable only for local development.

## Configuration

Copy `.env.example` to `.env`, then set at least:

```dotenv
APP_ENV=production
API_BIND=127.0.0.1
PUBLIC_URL=https://sync.example.com
CLIENT_URL=cubetimer://auth
POSTGRES_PASSWORD=<long-random-password>
JWT_SECRET=<output-of-openssl-rand-base64-48>
LOG_ONE_TIME_LINKS=false
ALLOWED_ORIGINS=https://timer.example.com
SMTP_HOST=smtp.example.com
SMTP_PORT=587
SMTP_USERNAME=<smtp-user>
SMTP_PASSWORD=<smtp-password>
SMTP_FROM=CubeTimer <noreply@example.com>
SMTP_STARTTLS=true
```

Add Google and Apple variables described in the README. Restrict `.env` to the deployment user:

```bash
chmod 600 .env
```

Changing `JWT_SECRET` immediately invalidates every access token. Refresh tokens remain valid and issue tokens with the new key, so rotate it during a controlled deployment.

## Start and upgrade

```bash
docker compose pull
docker compose build --pull
docker compose up -d
docker compose ps
curl http://127.0.0.1:43781/health/ready
```

The one-shot `migrate` service must finish successfully before the API starts. Migrations are forward-only during normal deployment. Back up before applying a new version.

For upgrades:

```bash
git pull --ff-only
docker compose build --pull
docker compose up -d
docker compose logs --since=10m api migrate
```

## HTTPS with Caddy

Install Caddy using its official package for the VPS distribution. Copy `deploy/Caddyfile` to `/etc/caddy/Caddyfile`, set the domain, validate, and reload:

```bash
sudo DOMAIN=sync.example.com caddy validate --config /etc/caddy/Caddyfile
sudo systemctl reload caddy
```

Persist `DOMAIN` in Caddy's systemd environment or replace the placeholder in the file. Confirm that `https://sync.example.com/health/ready` returns `{"status":"ready"}`.

Do not terminate TLS inside the Go process. The API trusts no forwarded client-IP header, which prevents clients from bypassing the in-process authentication rate limiter. On this simple deployment, requests arriving through Caddy share the proxy address for that limiter; use a proxy-level per-client rate limit if public traffic grows.

## Backups

Create a compressed logical backup:

```bash
docker compose exec -T postgres \
  pg_dump -U cubetimer -d cubetimer --format=custom \
  > "cubetimer-$(date -u +%Y%m%dT%H%M%SZ).dump"
```

Encrypt and copy backups off the VPS. Keep multiple generations.

Test restoration into an empty database:

```bash
docker compose exec -T postgres createdb -U cubetimer cubetimer_restore
docker compose exec -T postgres \
  pg_restore -U cubetimer -d cubetimer_restore --clean --if-exists \
  < cubetimer-backup.dump
```

Run integrity checks against the restored database before considering the backup process reliable. Remove the temporary database afterward.

For disaster recovery, restore PostgreSQL first, deploy the same or newer API version, run migrations, then check readiness.

## Logs and monitoring

The API emits structured JSON to standard output and intentionally omits passwords and tokens. Collect:

- container restart count;
- `/health/ready` availability;
- HTTP status and latency logs;
- disk usage for the PostgreSQL volume;
- backup completion and restore-test results.

Set Docker log rotation in `/etc/docker/daemon.json` or the Compose logging configuration so JSON logs cannot fill the VPS.

## Resource notes

The API is a single static Go binary. PostgreSQL normally determines the minimum useful memory. For a very small installation, start with conservative PostgreSQL connection/memory settings and measure rather than adding Redis or another identity service.
