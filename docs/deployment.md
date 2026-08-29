# Production VPS Deployment Runbook

This guide is an end-to-end production deployment runbook for self-hosting **CubeSync** on a single Linux Virtual Private Server (VPS). 

The entire production stack—PostgreSQL 18, the CubeSync Go API daemon, database migration engine, and Caddy reverse proxy (with automated TLS termination)—runs in isolated containers managed by **Docker Compose**.

```
                              +-------------------------------------------------+
                              |                 Linux VPS (Host)                |
                              |                                                 |
[ Internet Clients ] -------->| :80, :443, :443/udp (Public Ingress)            |
(Android / iOS / Web)  HTTPS  |                      |                          |
                              |  [ Docker Bridge Network ]                      |
                              |  +-------------------------------------------+  |
                              |  | caddy:2-alpine (Auto Let's Encrypt / TLS) |  |
                              |  +-------------------------------------------+  |
                              |                      |                          |
                              |                      v HTTP (Internal Bridge)   |
                              |  +-------------------------------------------+  |
                              |  | api:43781 (CubeSync Go Daemon)            |  |
                              |  +-------------------------------------------+  |
                              |                      |                          |
                              |                      v :5432 (Internal Bridge)  |
                              |  +-------------------------------------------+  |
                              |  | postgres:18-alpine (Persistent Volume)    |  |
                              |  +-------------------------------------------+  |
                              +-------------------------------------------------+
```

---

## Hardware Requirements & Sizing

For a small to medium self-hosted instance (up to several thousand active users):

- **CPU**: 1 vCPU (x86_64 or ARM64)
- **RAM**: 1 GB minimum (2 GB recommended for comfortable PostgreSQL buffer caching)
- **Disk**: 15–25 GB SSD/NVMe (PostgreSQL WAL, solve history, and local backup snapshots)
- **OS**: Modern Linux distribution with Docker Engine & Compose v2 installed (Ubuntu 22.04/24.04 LTS, Debian 12, Rocky Linux 9, or AlmaLinux 9)

---

## Step 1: DNS and Host Firewall Hardening

### 1.1 DNS Records
Create an `A` (and optional `AAAA`) record pointing your sync domain to the public IP address of your VPS:

```text
sync.example.com.   IN  A     203.0.113.10
sync.example.com.   IN  AAAA  2001:db8::10
```

> [!IMPORTANT]
> Secure mobile clients require a publicly trusted HTTPS certificate. Self-signed certificates or plain HTTP endpoints will be rejected by platform security policies.

### 1.2 Firewall Configuration (UFW)
Configure the Uncomplicated Firewall (UFW) to allow only necessary ingress traffic while strictly protecting internal container ports:

```bash
# Set default policies
sudo ufw default deny incoming
sudo ufw default allow outgoing

# Allow SSH (adjust port if running a custom SSH port)
sudo ufw allow 22/tcp comment "SSH"

# Allow HTTP and HTTPS (TCP and UDP for HTTP/3) for the Caddy container
sudo ufw allow 80/tcp comment "Caddy HTTP ACME Challenge"
sudo ufw allow 443/tcp comment "Caddy HTTPS"
sudo ufw allow 443/udp comment "Caddy HTTP/3 (QUIC)"

# Enable firewall
sudo ufw enable
sudo ufw status verbose
```

Verify that port `5432` (PostgreSQL) and port `43781` (CubeSync API) are **never** opened in the public firewall.

---

## Step 2: Host Directory and Environment Setup

### 2.1 Directory Layout
Create a dedicated deployment directory for CubeSync and its configuration files:

```bash
sudo mkdir -p /opt/cubesync/deploy
sudo chown -R "$USER":"$USER" /opt/cubesync
cd /opt/cubesync
```

### 2.2 Download Deployment Files
Download `compose.yaml` and `deploy/Caddyfile`:

```bash
curl -fsSL https://raw.githubusercontent.com/Maciek-Hetman/cubesync/main/compose.yaml -o compose.yaml
curl -fsSL https://raw.githubusercontent.com/Maciek-Hetman/cubesync/main/deploy/Caddyfile -o deploy/Caddyfile
```

### 2.3 Configure Production Environment (`.env`)
Create `/opt/cubesync/.env` with production-grade settings:

```bash
cat << 'EOF' > /opt/cubesync/.env
# --- Application Environment ---
APP_ENV=production
CUBESYNC_IMAGE=ghcr.io/maciek-hetman/cubesync:0.1.0

# --- Domain & Reverse Proxy ---
DOMAIN=sync.example.com
PUBLIC_URL=https://sync.example.com
CLIENT_URL=cubetimer://auth
ALLOWED_ORIGINS=https://timer.example.com

# --- Internal Networking ---
API_BIND=127.0.0.1
API_PORT=43781

# --- Database & Secrets ---
# Replace with a strong random string (e.g. openssl rand -hex 24)
POSTGRES_PASSWORD=replace-with-strong-db-password-min-24-chars

# Must be >= 32 bytes in production (generate with: openssl rand -base64 48)
JWT_SECRET=replace-with-openssl-rand-base64-48-output

# --- Token Lifetimes ---
ACCESS_TOKEN_TTL=15m
REFRESH_TOKEN_TTL=720h

# --- SMTP Email Configuration ---
SMTP_HOST=smtp.mailgun.org
SMTP_PORT=587
SMTP_USERNAME=postmaster@sync.example.com
SMTP_PASSWORD=your-smtp-password
SMTP_FROM=CubeTimer <noreply@sync.example.com>
SMTP_STARTTLS=true
# MANDATORY: Must be false in production to prevent secret leakage in logs
LOG_ONE_TIME_LINKS=false

# --- OAuth Providers (Optional) ---
GOOGLE_CLIENT_IDS=
GOOGLE_CLIENT_SECRET=

# --- Performance & Retention ---
MAX_SYNC_MUTATIONS=500
MAX_SYNC_CHANGES=1000
INACTIVE_DEVICE_WINDOW=2160h
RETENTION_RUN_INTERVAL=1h
EOF
```

### 2.4 Secure File Permissions
Restrict read and write access on `.env` to the operating system deployment user:

```bash
chmod 600 /opt/cubesync/.env
```

---

## Step 3: First Boot & Service Verification

### 3.1 Pull Container Images
Authenticate with GitHub Container Registry (if using private package builds) and pull the pinned images:

```bash
cd /opt/cubesync
docker compose pull
```

### 3.2 Launch the Complete Stack
Start PostgreSQL, the migration runner, the API daemon, and the Caddy reverse proxy:

```bash
docker compose up -d
```

### 3.3 Verify Container Status & Migration Logs
Check that all containers are running and that the one-shot `migrate` container completed successfully:

```bash
docker compose ps
docker compose logs migrate
```

Expected output for `migrate`:
```text
cubesync-migrate-1  | {"time":"...","level":"INFO","msg":"migration_complete"}
```

### 3.4 Verify Public HTTPS Availability
Caddy automatically contacts Let's Encrypt / ZeroSSL, solves the ACME HTTP-01/TLS-ALPN-01 challenge, acquires a certificate, and begins serving HTTPS.

Test the public health endpoint:

```bash
curl -i https://sync.example.com/health/ready
```

Expected HTTP status: `HTTP/2 200` with body:
```json
{"status":"ready"}
```

> [!NOTE]
> **Rate Limiting & Trusted Reverse Proxying**:
> In `deploy/Caddyfile`, Caddy forwards `X-Forwarded-For` and `X-Real-IP` to `reverse_proxy api:43781`. The Go backend's `clientIP()` middleware recognizes Docker's private bridge subnet (`172.16.0.0/12`) as a trusted proxy, extracting the genuine public client IP to enforce per-IP authentication rate limits (10 req/min with burst of 5) without allowing direct header spoofing.

---

## Step 4: Initial Admin Account Bootstrapping

CubeSync separates administrative telemetry (`/v1/admin/stats/*`) from regular users. Admin privileges cannot be granted through the public API or environment variables.

To provision the initial verified administrator, execute the interactive `create-admin` CLI inside the running API container:

```bash
docker compose exec -it api /usr/local/bin/cubing-sync create-admin
```

Follow the interactive prompts:
```text
Enter admin email: admin@example.com
Enter admin password: [hidden]
Confirm admin password: [hidden]
Admin account created successfully: admin@example.com
```

The newly created admin can immediately authenticate via `POST /v1/auth/login`.

---

## Step 5: Automated Backups & Disaster Recovery

### 5.1 Automated Backup Script
Create an automated backup script at `/opt/cubesync/backup.sh`:

```bash
cat << 'EOF' > /opt/cubesync/backup.sh
#!/usr/bin/env bash
set -euo pipefail

BACKUP_DIR="/var/backups/cubesync"
COMPOSE_DIR="/opt/cubesync"
TIMESTAMP="$(date -u +%Y%m%dT%H%M%SZ)"
BACKUP_FILE="${BACKUP_DIR}/cubesync-${TIMESTAMP}.dump"
RETENTION_DAYS=14

mkdir -p "${BACKUP_DIR}"

# Execute custom-format pg_dump inside the postgres container
docker compose --project-directory "${COMPOSE_DIR}" exec -T postgres \
  pg_dump -U cubetimer -d cubetimer --format=custom --compress=9 \
  > "${BACKUP_FILE}"

chmod 600 "${BACKUP_FILE}"

# Prune local backups older than RETENTION_DAYS
find "${BACKUP_DIR}" -type f -name "cubesync-*.dump" -mtime +"${RETENTION_DAYS}" -delete

echo "[$(date -u)] Backup completed successfully: ${BACKUP_FILE}"
EOF

chmod 700 /opt/cubesync/backup.sh
```

### 5.2 Configure Automated Systemd Timer
Set up a daily systemd service and timer to execute the backup at 03:00 UTC:

1. Create `/etc/systemd/system/cubesync-backup.service`:
```ini
[Unit]
Description=CubeSync PostgreSQL Automated Backup
After=docker.service

[Service]
Type=oneshot
ExecStart=/opt/cubesync/backup.sh
StandardOutput=journal
StandardError=journal
```

2. Create `/etc/systemd/system/cubesync-backup.timer`:
```ini
[Unit]
Description=Run CubeSync backup daily at 03:00 UTC

[Timer]
OnCalendar=*-*-* 03:00:00 UTC
Persistent=true

[Install]
WantedBy=timers.target
```

3. Enable and start the timer:
```bash
sudo systemctl daemon-reload
sudo systemctl enable --now cubesync-backup.timer
sudo systemctl list-timers cubesync-backup.timer
```

### 5.3 Offsite Backup Replication (Recommended)
Sync `/var/backups/cubesync` offsite using `rclone`, AWS S3, or `rsync` over SSH:

```bash
# Example using rclone to sync to S3 or B2:
# rclone sync /var/backups/cubesync remote:cubesync-backups
```

### 5.4 Backup Restoration & Disaster Recovery Drill

Test backup restoration non-destructively into a temporary database:

```bash
cd /opt/cubesync

# 1. Create a temporary database
docker compose exec -T postgres createdb -U cubetimer cubetimer_test_restore

# 2. Restore the dump file
docker compose exec -T postgres \
  pg_restore -U cubetimer -d cubetimer_test_restore --clean --if-exists \
  < /var/backups/cubesync/cubesync-<TIMESTAMP>.dump

# 3. Verify data integrity in the temporary database
docker compose exec -T postgres psql -U cubetimer -d cubetimer_test_restore -c "SELECT count(*) FROM users;"

# 4. Clean up temporary database
docker compose exec -T postgres dropdb -U cubetimer cubetimer_test_restore
```

In a total server loss disaster scenario:
1. Provision a new VPS following Steps 1–2.
2. Place the latest `.dump` file on the server.
3. Start only PostgreSQL: `docker compose up -d postgres`.
4. Restore data into the primary database:
   ```bash
   docker compose exec -T postgres pg_restore -U cubetimer -d cubetimer --clean --if-exists < latest.dump
   ```
5. Start the full stack: `docker compose up -d`.

---

## Step 6: Upgrades, Rollbacks & Maintenance

### 6.1 Standard Application Upgrade
To upgrade to a new version of CubeSync:

1. Create an on-demand database backup:
   ```bash
   /opt/cubesync/backup.sh
   ```
2. Update the image tag in `/opt/cubesync/.env`:
   ```bash
   CUBESYNC_IMAGE=ghcr.io/maciek-hetman/cubesync:0.2.0
   ```
3. Pull new images and apply the update:
   ```bash
   cd /opt/cubesync
   docker compose pull
   docker compose up -d
   ```
4. Verify migration completion and service health:
   ```bash
   docker compose logs --since=5m migrate api caddy
   curl -f https://sync.example.com/health/ready
   ```

### 6.2 Secret Rotation Runbooks

#### Rotating `JWT_SECRET`
Changing `JWT_SECRET` immediately invalidates all existing short-lived access tokens (15-minute TTL). Because CubeSync stores refresh tokens as cryptographically hashed records in PostgreSQL, clients will automatically exchange their existing refresh tokens on the next request and receive a new access token signed with the new secret—**without logging users out**.

1. Generate a new secret: `openssl rand -base64 48`.
2. Update `JWT_SECRET` in `/opt/cubesync/.env`.
3. Restart the API: `docker compose up -d api`.

#### Rotating `POSTGRES_PASSWORD`
1. Update password in PostgreSQL:
   ```bash
   docker compose exec -T postgres psql -U cubetimer -d cubetimer -c "ALTER USER cubetimer WITH PASSWORD 'new-strong-password';"
   ```
2. Update `POSTGRES_PASSWORD` in `/opt/cubesync/.env`.
3. Recreate the containers:
   ```bash
   docker compose up -d
   ```

### 6.3 Docker Log Rotation
Prevent Docker container JSON logs from consuming VPS disk space by configuring log rotation in `/etc/docker/daemon.json`:

```json
{
  "log-driver": "json-file",
  "log-opts": {
    "max-size": "50m",
    "max-file": "3"
  }
}
```

Reload Docker daemon to apply:
```bash
sudo systemctl reload docker
```

---

## Step 7: Troubleshooting & Diagnostic Runbook

### Common Issues and Resolutions

| Symptom | Probable Cause | Resolution |
| :--- | :--- | :--- |
| `migrate` container fails on startup | Database connection failure or invalid credentials | Check `docker compose logs postgres` and verify `POSTGRES_PASSWORD` in `.env`. |
| `api` container in `unhealthy` state | Database migrations have not finished or DB is unreachable | Inspect `docker compose logs migrate` to check for migration lock or syntax errors. |
| `caddy` fails to obtain SSL certificate | Port 80/443 blocked by firewall or DNS not pointing to VPS | Ensure DNS `A` record matches VPS IP and run `sudo ufw status` to confirm ports 80/443 are open. Check `docker compose logs caddy`. |
| `429 Too Many Requests` on auth endpoints | In-process rate limiter triggered (burst limit exceeded) | Verify client isn't in a rapid retry loop. Check that `deploy/Caddyfile` includes `X-Forwarded-For`. |
| `502 Bad Gateway` from Caddy | CubeSync API is not running or still initializing | Check `docker compose ps` and `docker compose logs api`. |
| Verification emails fail to send | Incorrect SMTP host, port, credentials, or STARTTLS mismatch | Test SMTP credentials independently and inspect API error logs with `docker compose logs api`. |

### Diagnostic Commands

```bash
# Check status and health across all containers (caddy, api, postgres)
docker compose ps

# Follow live structured logs
docker compose logs -f --tail=100 api caddy

# Check database connection pool status
docker compose exec postgres psql -U cubetimer -d cubetimer -c "SELECT count(*), state FROM pg_stat_activity GROUP BY state;"

# Check disk space utilization
df -h
docker system df
```


