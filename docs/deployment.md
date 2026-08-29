# Production VPS Deployment Runbook

This guide is an end-to-end production deployment runbook for self-hosting **CubeSync** on a single Linux Virtual Private Server (VPS). 

The target topology runs PostgreSQL 18 and the CubeSync API as isolated Docker containers via Docker Compose, with Caddy running natively on the host to manage TLS certificates, terminate HTTPS, and reverse-proxy traffic to the API bound on the local loopback interface (`127.0.0.1:43781`).

```
                              +-------------------------------------------+
                              |              Linux VPS (Host)             |
                              |                                           |
[ Internet Clients ] -------->| :80, :443 (Caddy Reverse Proxy + AutoTLS) |
(Android / iOS / Web)  HTTPS  |                      |                    |
                              |                      v HTTP (Loopback)    |
                              |              127.0.0.1:43781              |
                              |                      |                    |
                              |  [ Docker Network ]  v                    |
                              |  +-------------------------------------+  |
                              |  | api (CubeSync Go Daemon)            |  |
                              |  +-------------------------------------+  |
                              |                      |                    |
                              |                      v :5432 (Internal)   |
                              |  +-------------------------------------+  |
                              |  | postgres:18-alpine (Persistent DB)  |  |
                              |  +-------------------------------------+  |
                              +-------------------------------------------+
```

---

## Hardware Requirements & Sizing

For a small to medium self-hosted instance (up to several thousand active users):

- **CPU**: 1 vCPU (x86_64 or ARM64)
- **RAM**: 1 GB minimum (2 GB recommended for comfortable PostgreSQL buffer caching)
- **Disk**: 15–25 GB SSD/NVMe (PostgreSQL WAL, solve history, and local backup snapshots)
- **OS**: Modern Linux distribution (Ubuntu 22.04/24.04 LTS, Debian 12, Rocky Linux 9, or AlmaLinux 9)

---

## Step 1: DNS and Host Firewall Hardening

### 1.1 DNS Records
Create an `A` (and optional `AAAA`) record pointing your sync domain to the public IP address of your VPS:

```text
sync.example.com.   IN  A     203.0.113.10
sync.example.com.   IN  AAAA  2001:db8::10
```

> [!IMPORTANT]
> Secure mobile clients and Apple Sign-In require a publicly trusted HTTPS certificate. Self-signed certificates or plain HTTP endpoints will be rejected by platform security policies.

### 1.2 Firewall Configuration (UFW)
Configure the Uncomplicated Firewall (UFW) to allow only necessary ingress traffic while strictly protecting PostgreSQL and internal API ports:

```bash
# Set default policies
sudo ufw default deny incoming
sudo ufw default allow outgoing

# Allow SSH (adjust port if running a custom SSH port)
sudo ufw allow 22/tcp comment "SSH"

# Allow HTTP and HTTPS for Caddy
sudo ufw allow 80/tcp comment "Caddy HTTP ACME Challenge"
sudo ufw allow 443/tcp comment "Caddy HTTPS"

# Enable firewall
sudo ufw enable
sudo ufw status verbose
```

Verify that port `5432` (PostgreSQL) and port `43781` (CubeSync API) are **never** opened in the public firewall.

---

## Step 2: Host Directory and Environment Setup

### 2.1 Directory Layout
Create a dedicated deployment directory for CubeSync:

```bash
sudo mkdir -p /opt/cubesync
sudo chown -R "$USER":"$USER" /opt/cubesync
cd /opt/cubesync
```

### 2.2 Download Docker Compose Configuration
Download the official `compose.yaml`:

```bash
curl -fsSL https://raw.githubusercontent.com/Maciek-Hetman/cubesync/main/compose.yaml -o compose.yaml
```

### 2.3 Configure Production Environment (`.env`)
Create `/opt/cubesync/.env` with production-grade settings:

```bash
cat << 'EOF' > /opt/cubesync/.env
# --- Application Environment ---
APP_ENV=production
CUBESYNC_IMAGE=ghcr.io/maciek-hetman/cubesync:0.1.0

# --- Network & URLs ---
API_BIND=127.0.0.1
API_PORT=43781
PUBLIC_URL=https://sync.example.com
CLIENT_URL=cubetimer://auth
ALLOWED_ORIGINS=https://timer.example.com

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
APPLE_CLIENT_IDS=
APPLE_TEAM_ID=
APPLE_KEY_ID=
APPLE_PRIVATE_KEY=

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

## Step 3: First Boot & Database Migration

### 3.1 Pull Container Images
Authenticate with GitHub Container Registry (if using private package builds) and pull the pinned image:

```bash
cd /opt/cubesync
docker compose pull
```

### 3.2 Launch the Service Stack
Start PostgreSQL, the database migration service, and the API daemon:

```bash
docker compose up -d
```

### 3.3 Verify Container Status & Migration Logs
Check that all containers are healthy and that the one-shot `migrate` container finished cleanly:

```bash
docker compose ps
docker compose logs migrate
```

Expected output for `migrate`:
```text
cubesync-migrate-1  | {"time":"...","level":"INFO","msg":"migration_complete"}
```

### 3.4 Verify Internal Loopback Health
Test the API's readiness endpoint directly through the local loopback port:

```bash
curl -s http://127.0.0.1:43781/health/ready
```

Expected response:
```json
{"status":"ready"}
```

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

## Step 5: Host HTTPS Reverse Proxy with Caddy

Caddy provides automated Let's Encrypt / ZeroSSL TLS certificate issuance, automatic renewals, HTTP-to-HTTPS redirects, and high-performance HTTP/2 and HTTP/3 support.

### 5.1 Install Caddy
Install Caddy on Debian/Ubuntu:

```bash
sudo apt-get install -y debian-keyring debian-archive-keyring apt-transport-https curl
curl -1sLF 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' | sudo gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
curl -1sLF 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' | sudo tee /etc/apt/sources.list.d/caddy-stable.list
sudo apt-get update
sudo apt-get install -y caddy
```

### 5.2 Configure `/etc/caddy/Caddyfile`
Create the hardened reverse proxy configuration:

```caddy
sync.example.com {
    # Compression for responses
    encode zstd gzip

    # Reverse proxy to the CubeSync API container loopback port
    reverse_proxy 127.0.0.1:43781 {
        header_up Host {host}
        header_up X-Real-IP {remote_host}
        header_up X-Forwarded-For {remote_host}
        header_up X-Forwarded-Proto {scheme}
    }

    # Security Response Headers
    header {
        Strict-Transport-Security "max-age=31536000; includeSubDomains; preload"
        X-Content-Type-Options "nosniff"
        X-Frame-Options "DENY"
        Referrer-Policy "strict-origin-when-cross-origin"
        -Server
    }
}
```

> [!NOTE]
> **Reverse Proxy & Rate Limiting Trust**:
> The CubeSync API inspects `X-Forwarded-For` and `X-Real-IP` headers only when requests originate from trusted loopback/private proxy addresses (such as Caddy connecting from `127.0.0.1`). Direct public connections cannot spoof client IP headers because `API_BIND=127.0.0.1` and the firewall block direct access.

### 5.3 Validate and Reload Caddy
Validate syntax and reload the systemd service:

```bash
sudo caddy validate --config /etc/caddy/Caddyfile
sudo systemctl reload caddy
```

### 5.4 Verify Public HTTPS Endpoint
Check public availability:

```bash
curl -i https://sync.example.com/health/ready
```

Expected HTTP status: `HTTP/2 200` with body `{"status":"ready"}`.

---

## Step 6: Automated Backups & Disaster Recovery

### 6.1 Automated Backup Script
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

### 6.2 Configure Automated Systemd Timer
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

### 6.3 Offsite Backup Replication (Recommended)
Sync `/var/backups/cubesync` offsite using `rclone`, AWS S3, or `rsync` over SSH:

```bash
# Example using rclone to sync to S3 or B2:
# rclone sync /var/backups/cubesync remote:cubesync-backups
```

### 6.4 Backup Restoration & Disaster Recovery Drill

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
1. Provision a new VPS following Steps 1–3.
2. Place the latest `.dump` file on the server.
3. Start only PostgreSQL: `docker compose up -d postgres`.
4. Restore data into the primary database:
   ```bash
   docker compose exec -T postgres pg_restore -U cubetimer -d cubetimer --clean --if-exists < latest.dump
   ```
5. Start the remaining services: `docker compose up -d`.

---

## Step 7: Upgrades, Rollbacks & Maintenance

### 7.1 Standard Application Upgrade
To upgrade to a new version of CubeSync:

1. Create an on-demand database backup:
   ```bash
   /opt/cubesync/backup.sh
   ```
2. Update the image tag in `/opt/cubesync/.env`:
   ```bash
   CUBESYNC_IMAGE=ghcr.io/maciek-hetman/cubesync:0.2.0
   ```
3. Pull new images and apply the rolling update:
   ```bash
   cd /opt/cubesync
   docker compose pull
   docker compose up -d
   ```
4. Verify migration completion and API health:
   ```bash
   docker compose logs --since=5m migrate api
   curl -f http://127.0.0.1:43781/health/ready
   ```

### 7.2 Secret Rotation Runbooks

#### Rotating `JWT_SECRET`
Changing `JWT_SECRET` immediately invalidates all existing short-lived access tokens (15-minute TTL). However, because CubeSync stores refresh tokens as cryptographically hashed records in PostgreSQL, clients will automatically exchange their existing refresh tokens on the next request and receive a new access token signed with the new secret—**without logging users out**.

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

### 7.3 Docker Log Rotation
Prevent Docker container JSON logs from consuming all VPS disk space by configuring log rotation in `/etc/docker/daemon.json`:

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

## Step 8: Troubleshooting & Diagnostic Runbook

### Common Issues and Resolutions

| Symptom | Probable Cause | Resolution |
| :--- | :--- | :--- |
| `migrate` container fails on startup | Database connection failure or invalid credentials | Check `docker compose logs postgres` and verify `POSTGRES_PASSWORD` in `.env`. |
| `api` container in `unhealthy` state | Database migrations have not finished or DB is unreachable | Inspect `docker compose logs migrate` to check for migration lock or syntax errors. |
| `429 Too Many Requests` on auth endpoints | In-process rate limiter triggered (burst limit exceeded) | Verify client isn't in a rapid retry loop. If behind Caddy, ensure `X-Forwarded-For` is being passed. |
| `502 Bad Gateway` from Caddy | CubeSync API is not running or bound to wrong port | Check `docker compose ps` and test `curl http://127.0.0.1:43781/health/ready` on the host. |
| Verification emails fail to send | Incorrect SMTP host, port, credentials, or STARTTLS mismatch | Test SMTP credentials independently and inspect API error logs with `docker compose logs api`. |

### Diagnostic Commands

```bash
# Check container status and health
docker compose ps

# Follow live structured JSON logs
docker compose logs -f --tail=100 api

# Check database connection pool status
docker compose exec postgres psql -U cubetimer -d cubetimer -c "SELECT count(*), state FROM pg_stat_activity GROUP BY state;"

# Check disk space utilization
df -h
docker system df
```

