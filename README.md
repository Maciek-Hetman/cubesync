# CubeTimer Sync Backend

A small, self-hosted synchronization API for CubeTimer. Logged-in users can keep solves and client-managed sessions consistent across Android, iOS, macOS, and web clients while every client remains usable offline.

## Stack

- Go and Chi
- PostgreSQL, pgx, and sqlc
- Goose SQL migrations
- OpenAPI 3.1 with generated Go contract types
- Email/password, Google, and Apple authentication
- Docker Compose for local or single-VPS deployment

The service does not need Redis or a separate identity server. The required production containers are the API and PostgreSQL.

## Quick start with Docker

Requirements: Docker Engine with Compose v2.

```bash
cp .env.example .env
docker compose up --build
```

The API listens on `http://127.0.0.1:43781` when `API_BIND=127.0.0.1`. Readiness is available at:

```bash
curl http://127.0.0.1:43781/health/ready
```

For a local SMTP inbox:

```bash
docker compose --profile dev-mail up --build
```

Then set `SMTP_HOST=mailpit`, `SMTP_PORT=1025`, and `SMTP_STARTTLS=false`. Mailpit is available at `http://127.0.0.1:8025`.

## Local Go development

Use a supported Go toolchain and a PostgreSQL database:

```bash
cp .env.example .env
set -a; . ./.env; set +a
go run ./cmd/api migrate
go run ./cmd/api serve
```

Common commands:

```bash
make test
make lint
make generate
```

`make generate` reproduces sqlc queries under `internal/store/db` and OpenAPI models under `internal/apicontract`. Generated files are committed and checked in CI.

## API

The source of truth is [`api/openapi.yaml`](api/openapi.yaml). Main endpoints:

- `POST /v1/auth/register`
- `POST /v1/auth/email/verify`
- `POST /v1/auth/login`
- `POST /v1/auth/refresh`
- `POST /v1/auth/federated/{google|apple}`
- `POST /v1/auth/link/{google|apple}`
- `GET /v1/me`
- `POST /v1/sync`
- `GET /v1/admin/stats/overview`
- `GET /v1/admin/stats/requests`
- `GET /v1/admin/stats/errors`

Access tokens are short-lived HMAC-signed JWTs. Refresh tokens are opaque, stored only as hashes, rotated on every use, and revoked as a family when reuse is detected.

Admin statistics endpoints require an account with `user_role = admin`. There is no public role-management API. After migrations have been applied, create a new verified admin from a host with Docker access:

```bash
docker compose exec api /usr/local/bin/cubing-sync create-admin
docker exec -it <api-container> /usr/local/bin/cubing-sync create-admin
```

The command prompts for an email, a password, and a password confirmation on a TTY. It does not accept credentials as flags or environment variables, does not create HTTP routes, and does not promote or modify an existing account. The new admin can sign in immediately with `POST /v1/auth/login`.

Overview totals come from account and sync tables. Request and error series are hourly aggregates of completed HTTP traffic, excluding health checks and the statistics endpoints themselves. Query `from` and `to` as RFC3339 timestamps and `interval` as `hour` or `day`; the default window is the last 24 hours.

See [`docs/sync-protocol.md`](docs/sync-protocol.md) for the client algorithm, retries, conflicts, and CubeTimer enum mapping.

## Authentication configuration

### Email and password

Passwords use Argon2id. New email accounts must verify their address before synchronization. Configure:

- `SMTP_HOST`, `SMTP_PORT`, `SMTP_USERNAME`, `SMTP_PASSWORD`
- `SMTP_FROM`
- `SMTP_STARTTLS=true` in production
- `CLIENT_URL`, the deep-link or web-app base receiving verification and reset tokens

The client extracts a token from the link and submits it to the appropriate API endpoint. The API deliberately does not consume secrets from a `GET` request, which avoids email-link prefetchers creating sessions.

In development only, `LOG_ONE_TIME_LINKS=true` writes one-time URLs to structured logs. Production configuration rejects that setting.

### Google

Create the appropriate Google OAuth clients and put every accepted token audience in the comma-separated `GOOGLE_CLIENT_IDS`. Native apps normally request an ID token for a server/web client ID. Set `GOOGLE_CLIENT_SECRET` when clients send authorization codes for server-side exchange.

### Apple

Set:

- `APPLE_CLIENT_IDS` to accepted bundle IDs and Services IDs
- `APPLE_TEAM_ID`
- `APPLE_KEY_ID`
- `APPLE_PRIVATE_KEY` to the `.p8` private key

The API generates Apple's short-lived ES256 client secret when exchanging an authorization code.

Both providers require a nonce. The client must send the same value found in the provider token's `nonce` claim. Verify tokens in the platform SDK first, keep refresh tokens in Keychain/Keystore, and send API access tokens only in the `Authorization: Bearer` header.

Accounts are never merged merely because two providers report the same email. Sign in to the existing account and call the authenticated linking endpoint.

## Production deployment

See [`docs/deployment.md`](docs/deployment.md) for VPS setup, HTTPS, secrets, upgrades, and PostgreSQL backup/restore. A host-level Caddy example is provided in [`deploy/Caddyfile`](deploy/Caddyfile).

Minimum operational requirements:

1. Set `APP_ENV=production`.
2. Generate `JWT_SECRET` with `openssl rand -base64 48`.
3. Set `LOG_ONE_TIME_LINKS=false` and configure SMTP.
4. Bind the API to loopback with `API_BIND=127.0.0.1`.
5. Put Caddy or another trusted reverse proxy in front and expose only HTTPS.
6. Back up the PostgreSQL volume regularly and test restoration.

## Data ownership and privacy

Every session, solve, device, mutation, and change-log query is scoped by the authenticated user ID. The backend stores solve history and account email addresses; operators should publish a privacy policy before distributing clients.
