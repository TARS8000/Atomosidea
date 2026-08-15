# SFSP Runbook

This document provides operational procedures for maintaining and troubleshooting the Secure File Scanning Platform (SFSP).

## 1. System Overview

Refer to the main `README.md` and `security/docs/Threat_Model.md` for architectural details.

## 2. Deployment and Startup

### Initial Deployment

1.  **Clone the repository**: `git clone <repo-url>`
2.  **Copy environment file**: `cp .env.example .env` and fill in all required variables (PostgreSQL, MinIO, JWT_SECRET, etc.).
3.  **Build and start services**: `docker compose build && docker compose up -d`
4.  **Verify services**: `docker compose ps` should show all SFSP services (`sfsp-api`, `sfsp-worker`, `sfsp-db`, `sfsp-minio`, `sfsp-clamav-client`, `sfsp-yara-client`) running.
5.  **Check API health**: `curl http://localhost:8090/health` should return `{"status":"ok"}`.

### Restarting Services

-   `docker compose restart sfsp-api sfsp-worker sfsp-db sfsp-minio`
-   Or for all services: `docker compose restart`

## 3. Monitoring

### Key Metrics to Monitor

-   **`sfsp-api`**:
    -   HTTP request rates, latencies, error rates (5xx).
    -   CPU/Memory usage.
-   **`sfsp-worker`**:
    -   Job processing rate.
    -   Number of jobs in Redis queue (`sfsp:scan_queue`).
    -   CPU/Memory usage.
    -   Errors in logs (failed scans, sandbox issues).
-   **`sfsp-db`**:
    -   Connection count, query latency.
    -   Disk I/O, CPU, Memory usage.
    -   Table sizes (`files`, `scan_jobs`, `scan_results`).
-   **`sfsp-minio`**:
    -   Storage usage.
    -   Object put/get rates, error rates.
-   **Docker Daemon (on worker host)**:
    -   Container startup/shutdown rates.
    -   Resource usage by sandbox containers.

### Logging

-   All SFSP services log to `stdout`/`stderr`. Use `docker compose logs -f <service_name>` to view logs.
-   Configure a centralized logging solution (e.g., ELK stack, Grafana Loki) for production.

## 4. Troubleshooting

### Common Issues and Solutions

#### A. `sfsp-api` returns 500 errors or fails to start

-   **Symptom**: `curl http://localhost:8090/health` fails or API endpoints return 500.
-   **Check**:
    1.  **`sfsp-api` logs**: `docker compose logs sfsp-api`. Look for connection errors to `sfsp-db`, `sfsp-minio`, or `redis`.
    2.  **Dependency status**: Ensure `sfsp-db`, `sfsp-minio`, `redis` are running (`docker compose ps`).
    3.  **Configuration**: Verify `.env` variables are correctly set, especially `SFSP_DATABASE_URL`, `SFSP_MINIO_ENDPOINT`, `REDIS_ADDR`.
-   **Solution**: Address connection issues, restart dependencies if necessary, or correct `.env` configuration.

#### B. `sfsp-worker` is not processing jobs

-   **Symptom**: Jobs are stuck in "queued" status, `sfsp-worker` logs show no activity or errors.
-   **Check**:
    1.  **`sfsp-worker` logs**: `docker compose logs sfsp-worker`. Look for errors connecting to `sfsp-db`, `sfsp-minio`, `redis`, or the Docker daemon.
    2.  **Redis queue**: Connect to Redis (`docker exec -it atmosidea-redis redis-cli`) and check `LLEN sfsp:scan_queue`. If jobs are present but not processed, the worker might be stuck.
    3.  **Docker daemon connectivity**: Ensure `/var/run/docker.sock` is correctly mounted and the worker has permissions to access it.
    4.  **Sandbox image availability**: Check if `sfsp-clamav-client` and `sfsp-yara-client` images are built and available (`docker images`).
-   **Solution**: Resolve connectivity issues. If worker is stuck, restart it. If sandbox images are missing, rebuild them (`docker compose build sfsp-clamav-client sfsp-yara-client`).

#### C. Scan jobs fail with "error" status

-   **Symptom**: `GET /api/v1/results/{job_id}` shows `result: "error"` for scanners.
-   **Check**:
    1.  **`sfsp-worker` logs**: `docker compose logs sfsp-worker`. Look for specific errors from ClamAV or YARA scans, or sandbox execution failures.
    2.  **Sandbox logs**: If a sandbox container failed to start or execute, the `sfsp-worker` logs should contain `stdout`/`stderr` from the sandbox.
    3.  **Scanner image issues**: Ensure the `sfsp-clamav-client` and `sfsp-yara-client` images are functional. Try running them manually with a test file.
    4.  **YARA rules**: If YARA scans fail, verify `security/yara-rules/general.yar` exists and is valid.
-   **Solution**: Debug scanner issues based on logs. Update scanner images or YARA rules if necessary.

#### D. MinIO `403 Forbidden` or `404 Not Found` errors

-   **Symptom**: Files cannot be uploaded or downloaded from `sfsp-minio`.
-   **Check**:
    1.  **`sfsp-minio` logs**: `docker compose logs sfsp-minio`.
    2.  **`minio-init` logs**: `docker compose logs minio-init`. Ensure buckets (`raw-files`, `clean-files`, `quarantine`) were created and policies set correctly.
    3.  **MinIO credentials**: Verify `SFSP_MINIO_ACCESS_KEY_ID` and `SFSP_MINIO_SECRET_ACCESS_KEY` in `.env`.
    4.  **Bucket policies**: Confirm `raw-files` and `quarantine` are private, `clean-files` is public read.
-   **Solution**: Correct credentials, restart `minio-init` if buckets/policies are wrong.

## 5. Maintenance

### Database Backups

-   Regularly back up the `sfsp-db` volume (`sfsp_db_data`).
-   Example: `docker exec atmosidea-sfsp-db pg_dump -U user sfsp_db > sfsp_db_backup.sql`

### MinIO Backups

-   Regularly back up the `sfsp-minio` volume (`sfsp_minio_data`).

### Scanner Updates

-   **ClamAV**: The `clamav/clamav:latest` image automatically updates virus definitions. Rebuilding `sfsp-clamav-client` periodically will ensure the `clamscan` binary itself is up-to-date.
-   **YARA**: Update YARA rules in `security/yara-rules/`. Worker will pick up changes on restart or if rules are dynamically loaded (not currently implemented).

## 6. Security Procedures

-   **Incident Response**: Refer to `security/docs/Incident_Guide.md`.
-   **Vulnerability Management**: Regularly scan Docker images and dependencies for known vulnerabilities.
-   **Access Control**: Ensure only authorized personnel have access to the Docker host and `.env` files.

---
*This is a living document and should be updated as the system evolves.*
