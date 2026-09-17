# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project

CubeSync is a self-hosted Go synchronization API for CubeTimer. It lets logged-in users keep solves and sessions consistent across Android, iOS, macOS, and web clients, with every client remaining fully usable offline. The server is a replication target and restore source — never a requirement for timing solves.

Module: `github.com/Maciek-Hetman/cubing-sync-backend`. Stack: Go + Chi, PostgreSQL via pgx/pgxpool, sqlc for typed queries, Goose for migrations, OpenAPI 3.1 with `oapi-codegen`-generated Go types. No Redis, no separate identity server — production only needs the API container and PostgreSQL.

## Commands

```bash
# Local dev (requires Go toolchain + PostgreSQL; .env populated from .env.example)
go run ./cmd/api migrate     # apply Goose migrations
go run ./cmd/api serve       # run the HTTP server
go run ./cmd/api create-admin  # interactive TTY prompt to provision an admin account

make build      # go build -o bin/api ./cmd/api
make run        # go run ./cmd/api serve
make test       # go test ./...
make test-integration  # go test -tags=integration ./...  (needs TEST_DATABASE_URL)
make lint       # gofmt -l . (must be empty) + go vet ./...
make generate   # regenerate sqlc DB code + OpenAPI Go types (see below)
make migrate-up
make compose-up / compose-down
```

Run a single test: `go test ./internal/sync/... -run TestName -v` (add `-tags=integration` for tests under `internal/integration`, which need `TEST_DATABASE_URL` and are skipped otherwise). CI also runs everything with `-race`.

`make generate` runs two generators and both outputs are committed and checked in CI (`make generate && git diff --exit-code`):
- sqlc (`sqlc.yaml`): `db/queries/*.sql` → `internal/store/db/` (package `db`)
- oapi-codegen (`api/oapi-codegen.yaml`): `api/openapi.yaml` → `internal/apicontract/types.gen.go`

After editing anything under `db/queries/` or `db/migrations/`, or `api/openapi.yaml`, run `make generate` and commit the regenerated files.

Docker: `docker compose up --build` (see `compose.yaml`); the release image is built from `Dockerfile` (distroless, non-root) and published to GHCR on `v*` tags.

## Architecture

**Layout**: `cmd/api` (CLI entrypoints: `serve`, `migrate`, `create-admin`, `healthcheck`) → `internal/httpapi` (Chi router, middleware, HTTP handlers/DTO boundary) → domain services (`internal/auth`, `internal/sync`, `internal/admin`) → `internal/store/db` (sqlc-generated queries) → PostgreSQL. `internal/apicontract` holds OpenAPI-generated types; `api/openapi.yaml` is the source of truth for the wire contract.

**`internal/httpapi`**: One `Handler` struct wires all domain services and is mounted by `NewRouter` in `router.go`. Middleware order matters: request ID → access log (also feeds admin telemetry) → recoverer → timeout → security headers → CORS → optional compression. Auth-sensitive routes (`/v1/auth/*` except `link/{provider}`) sit behind a token-bucket IP rate limiter (`ratelimit.go`) that only trusts `X-Forwarded-For`/`X-Real-IP` when the direct peer is in `TRUSTED_PROXIES` — otherwise it uses `RemoteAddr` verbatim to avoid spoofing and proxy-induced global lockout. Everything under the second route group requires `h.authenticate`; sync/snapshot/stats/history additionally require `h.requireVerified`; admin stats require `h.requireAdmin`. Request bodies are capped (2MB) and decoded with `DisallowUnknownFields`.

**`internal/auth`**: Email/password (Argon2id, PHC-format hash) plus Google-only federated OIDC login (`federated.go` — JWKS `KeySet`s are cached once in `NewOIDCVerifier`, never re-fetched per-request). Access tokens are short-lived HS256 JWTs (`tokens.go`); refresh tokens are opaque, stored only as SHA-256 hashes, and rotated per use within a `family_id` — presenting an already-revoked token triggers reuse detection and revokes the whole family. `SetPassword` (used by both forgot/reset and authenticated change-password flows) revokes all refresh tokens for the user. Federated identities are never auto-merged into an existing account by matching email; linking requires the authenticated `link/{provider}` endpoint. Apple sign-in / RS256 support has been removed — Google is the only provider and all locally-issued tokens are HS256.

**`internal/sync`**: The core mutation/replication engine (`service.go`). `POST /v1/sync` combines mutation upload and incremental download in one Postgres transaction, guarded per-user by a transaction-scoped Postgres advisory lock (`pg_advisory_xact_lock`, keyed by an FNV-64a hash of the user ID) so concurrent syncs from the same user serialize instead of racing. Mutations are sorted deterministically (sessions before solves, then by entity UUID) before applying, to avoid lock-ordering deadlocks. Every entity carries an optimistic-concurrency `version`; a mismatched `base_version` yields a `conflict` outcome (never a silent server-side resolution). Mutation idempotency is enforced via `processed_mutations` — replays of the same mutation ID return the previously recorded outcome. Supports protocol v2 (`X-Sync-Protocol: 2` header) which slims delete/conflict payloads (`DeleteStub`/`ConflictStub`) to reduce bandwidth. `POST /v1/snapshot` (`snapshot.go`) is the bootstrap/recovery path when a client has no cursor or its cursor has expired (`cursor_expired`, HTTP 409) — it paginates full current state per entity type instead of replaying change-log history. `retention.go` runs a background loop (`RetentionService`, started in `main.go`, gracefully drained via `sync.WaitGroup` on shutdown) that prunes change-log rows once every device has acknowledged them or they exceed `INACTIVE_DEVICE_WINDOW`; `checkCursorNotExpired` in `service.go` must use the same window or a valid cursor could be pruned out from under a client. See [docs/sync-protocol.md](docs/sync-protocol.md) for the full client-facing protocol (mutation shape, conflict handling, cursors, snapshot pagination, CubeTimer enum mapping).

**`internal/admin`**: Request/error telemetry. Both `RecordRequestAsync` and `RecordErrorAsync` push onto bounded in-memory channels drained by a background `flushLoop` batching worker — never spawn a naked goroutine per request here, that was a prior incident (unbounded goroutines exhausting the pgx pool under an error storm). `Service.Shutdown()` must be wired up wherever a `Service` is constructed so buffered telemetry flushes before the pool closes.

**Data model conventions**: every session/solve/device/mutation/change-log row is scoped by the authenticated user ID — there is no cross-user query path. Deletes are soft (tombstones with a bumped version, `deleted_at` set); a delete against an already-deleted entity at the same version is idempotent (returns `accepted`, not a conflict). `change_log` is the single append-only journal that both `/v1/sync` and retention pruning operate over.

**Config** (`internal/config/config.go`): all settings load from environment variables with validated defaults; production (`APP_ENV=production`) enforces additional invariants (non-default, ≥32-byte `JWT_SECRET`; `LOG_ONE_TIME_LINKS=false`). See `.env.example` for the full variable list and `README.md` for auth/SMTP/Google OAuth setup and production deployment steps (also see [docs/deployment.md](docs/deployment.md)).

## Testing conventions

- Files named `*_test.go` without a build tag are standard unit tests, including ones named `*_adversarial_test.go` (adversarial-style unit tests, not a separate build mode).
- Files under `internal/integration/` carry `//go:build integration` and talk to a real PostgreSQL instance via `TEST_DATABASE_URL`; they self-skip when that env var is unset. `runMigrations` applies Goose migrations and the suite truncates tables between runs.
- When changing sync/auth/admin behavior, check for a matching adversarial or integration test covering the exact failure mode before adding a new one — this codebase has been through a security/concurrency audit ([docs/AUDIT_REPORT.md](docs/AUDIT_REPORT.md)) and regressions in error wrapping, goroutine lifecycle, or advisory locking are the kind of thing that audit specifically hardened against.
