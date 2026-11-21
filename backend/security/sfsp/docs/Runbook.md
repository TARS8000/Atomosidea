# SFSP Runbook

This document provides operational procedures for maintaining and troubleshooting the Secure File Scanning Platform (SFSP).

## 1. System Overview

Refer to the main `README.md`, `EditAI.md` (sections 3.2, 7, 9.8, and 12), and `backend/security/sfsp/docs/Threat_Model.md` for architectural details.

## 2. Deployment and Startup

### Initial Deployment

1.  **Clone the repository**: `git clone <repo-url>`
2.  **Copy environment file**: `cp .env.example .env` and fill in all required variables (PostgreSQL, MinIO, `REDIS_PASSWORD`, `SFSP_API_KEY`, `SFSP_WORKER_DB_PASSWORD`, `JWT_SECRET`, etc.). `docker compose` aborts if `REDIS_PASSWORD` or `SFSP_API_KEY` is unset, and `sfsp-api` refuses to start without `SFSP_API_KEY` (use a long random value of at least 32 characters). Replace every example value before production use.
3.  **Build and start services**: `docker compose build && docker compose up -d`
4.  **Verify services**: `docker compose ps` should show the SFSP services (`sfsp-api`, `sfsp-worker`, `sfsp-docker-proxy`, `sfsp-db`) and their dependencies (`minio`, `redis`) running. `sfsp-clamav-client` and `sfsp-yara-client` are build-only services that print a message and exit, and `minio-init` / `db-role-init` run once and exit; check them with `docker compose ps -a` and `docker images`.
5.  **Check API health**: `sfsp-api` is not published to the host (it listens on port 8080 inside the `internal` network `sfsp-isolated-net`; host port 8090 belongs to the monitoring dashboard). Run `docker compose exec sfsp-api wget -qO- http://localhost:8080/health`, which should return `{"status":"ok"}`. `/health` needs no key; every `/api/v1/*` request must carry the header `X-SFSP-API-Key` (the value of `SFSP_API_KEY`).

### Restarting Services

-   `docker compose restart sfsp-api sfsp-worker sfsp-docker-proxy sfsp-db minio`
-   Or for all services: `docker compose restart`
-   Restarting `sfsp-worker` is safe: jobs it had taken but not acknowledged stay in `sfsp:scan_queue:processing` and are moved back to `sfsp:scan_queue` at startup (`RecoverProcessing`).

## 3. Monitoring

### Key Metrics to Monitor

-   **`sfsp-api`**:
    -   HTTP request rates, latencies, error rates (5xx), and 401/413 responses.
    -   CPU/Memory usage.
-   **`sfsp-worker`**:
    -   Job processing rate.
    -   Number of jobs in the Redis queues: waiting (`sfsp:scan_queue`), in progress (`sfsp:scan_queue:processing`, normally at most one), and dead-lettered (`sfsp:scan_queue:dead`, should be empty).
    -   CPU/Memory usage.
    -   Errors in logs (failed scans, sandbox issues).
-   **`sfsp-docker-proxy`**:
    -   Container is running; errors or 403 responses from the proxy in the `sfsp-worker` logs.
-   **`sfsp-db`**:
    -   Connection count, query latency.
    -   Disk I/O, CPU, Memory usage.
    -   Table sizes (`files`, `scan_jobs`, `scan_results`).
-   **`minio`** (container `atmosidea-minio`):
    -   Storage usage.
    -   Object put/get rates, error rates.
-   **Docker Daemon (on worker host)**:
    -   Container startup/shutdown rates.
    -   Resource usage by sandbox containers (`sfsp-sandbox-*`).

### Logging

-   All SFSP services log to `stdout`/`stderr`. Use `docker compose logs -f <service_name>` to view logs.
-   Configure a centralized logging solution (e.g., ELK stack, Grafana Loki) for production.

## 4. Troubleshooting

### Common Issues and Solutions

#### A. `sfsp-api` returns 401/500 errors or fails to start

-   **Symptom**: The health check from step 5 fails, API calls return 401 or 500, or the container keeps restarting.
-   **Check**:
    1.  **`sfsp-api` logs**: `docker compose logs sfsp-api`. Look for `SFSP_API_KEY is not set` (the API refuses to start without it) and for connection errors to `sfsp-db`, `minio`, or `redis` (including Redis `NOAUTH` errors).
    2.  **Dependency status**: Ensure `sfsp-db`, `minio`, `redis` are running (`docker compose ps`).
    3.  **Configuration**: Verify the `.env` variables used by `sfsp-api` are set correctly, especially `SFSP_API_KEY`, `REDIS_PASSWORD`, `POSTGRES_USER` / `POSTGRES_PASSWORD` (compose builds `SFSP_DATABASE_URL` from them), and `SFSP_API_ACCESS_KEY` / `SFSP_API_SECRET_KEY` (compose maps them to `SFSP_RAW_MINIO_*` / `SFSP_CLEAN_MINIO_*`). `sfsp-api` does not read `.env` itself: like every service, it only receives the variables listed in its `environment` section of `docker-compose.yml` (no `env_file`, and `.env` is not copied into the image). `REDIS_URL` and `SFSP_API_URL` are fixed in `docker-compose.yml`.
    4.  **401 responses**: The caller does not send `X-SFSP-API-Key`, or its value differs from the `SFSP_API_KEY` of `sfsp-api`. **413 responses**: the file exceeds `SFSP_MAX_UPLOAD_BYTES` (default 2 GiB - 1).
-   **Solution**: Address connection issues, restart dependencies if necessary, or correct the `.env` configuration (then run `docker compose up -d` to recreate the affected services).

#### B. `sfsp-worker` is not processing jobs

-   **Symptom**: Jobs are stuck in "queued" status, `sfsp-worker` logs show no activity or errors.
-   **Check**:
    1.  **`sfsp-worker` logs**: `docker compose logs sfsp-worker`. Look for errors connecting to `sfsp-db`, `minio`, `redis`, or the Docker API (via `sfsp-docker-proxy`).
    2.  **Redis queue**: Redis requires a password, so run `docker compose exec redis sh -c 'REDISCLI_AUTH="$REDIS_PASSWORD" redis-cli LLEN sfsp:scan_queue'`. Do the same for `sfsp:scan_queue:processing` and `sfsp:scan_queue:dead`. If jobs are waiting but not processed, the worker might be stuck.
    3.  **Docker connectivity**: The worker does not mount `/var/run/docker.sock`; it reaches Docker through `sfsp-docker-proxy` (`DOCKER_HOST=tcp://sfsp-docker-proxy:2375`, network `sfsp-docker-net`). Ensure the proxy is running (`docker compose ps sfsp-docker-proxy`). A 403 from the proxy means the requested Docker API endpoint is not allowed by the proxy settings in `docker-compose.yml`.
    4.  **Sandbox image availability**: The worker never pulls images. Check that `sfsp-clamav-client` and `sfsp-yara-client` are built and available locally (`docker images`).
-   **Solution**: Resolve connectivity issues. If the worker is stuck, restart it (unacknowledged jobs are recovered automatically). If sandbox images are missing, rebuild them (`docker compose build sfsp-clamav-client sfsp-yara-client`).

#### C. Scan jobs fail with "error" or "failed" status

-   **Symptom**: `GET /api/v1/results/{job_id}` (internal only, requires `X-SFSP-API-Key`) shows `result: "error"` for a scanner, and the job ends as `failed` (a job is `clean` only if BOTH ClamAV and YARA report `clean`).
-   **Check**:
    1.  **`sfsp-worker` logs**: `docker compose logs sfsp-worker`. Look for specific errors from ClamAV or YARA scans, or sandbox execution failures.
    2.  **Sandbox output**: The `stdout`/`stderr` of each scanner run are stored in `scan_results.raw_output` in `sfsp-db`, and failures are also logged by `sfsp-worker`.
    3.  **Scanner image issues**: Ensure the `sfsp-clamav-client` and `sfsp-yara-client` images are functional. Try running them manually with a test file.
    4.  **YARA rules**: If YARA scans fail, verify `backend/security/sfsp/yara-rules/general.yar` exists and is valid. The sandbox reads the copy that was baked into the `sfsp-yara-client` image (`/etc/yara-rules`) at build time. `sfsp-worker` itself neither mounts nor reads the rules, and its image contains no `yara` or `docker` CLI.
    5.  **Not an error, but rejected**: A ZIP that violates the extraction limits (`SFSP_MAX_ZIP_ENTRIES`, `SFSP_MAX_EXTRACTED_BYTES`, `SFSP_MAX_ZIP_ENTRY_BYTES`, `SFSP_MAX_ZIP_RATIO`, `SFSP_MAX_ZIP_DEPTH`, Zip Slip, symlinks) or a file that exceeds a ClamAV limit ends as `suspicious` and goes to the `quarantine` bucket; see `scan_jobs.processing_details`.
-   **Solution**: Debug scanner issues based on logs. Update scanner images or YARA rules if necessary (see "Scanner Updates").

#### D. MinIO `403 Forbidden` or `404 Not Found` errors

-   **Symptom**: Files cannot be uploaded or downloaded from `minio`.
-   **Check**:
    1.  **`minio` logs**: `docker compose logs minio`.
    2.  **`minio-init` logs**: `docker compose logs minio-init`. Ensure buckets (`raw-files`, `clean-files`, `quarantine`) were created and the SFSP users and policies were set correctly.
    3.  **MinIO credentials**: Verify `SFSP_API_ACCESS_KEY` / `SFSP_API_SECRET_KEY` (`sfsp-api`), and `SFSP_WORKER_RAW_ACCESS_KEY` / `SFSP_WORKER_RAW_SECRET_KEY` and `SFSP_WORKER_CLEAN_ACCESS_KEY` / `SFSP_WORKER_CLEAN_SECRET_KEY` (`sfsp-worker`) in `.env`.
    4.  **Bucket policies**: Confirm `raw-files`, `clean-files`, and `quarantine` are all private (no anonymous access).
    5.  **Expected 404s**: The `raw-files` object is deleted by `sfsp-worker` after the final job status is recorded and the completion event is published, and `clean-files` objects are deleted by the cleanup watcher once the content is `public`.
-   **Solution**: Correct credentials, re-run `minio-init` (`docker compose up minio-init`) if buckets/policies are wrong.

#### E. Jobs are in the dead list or stay in `running`

-   **Symptom**: `sfsp:scan_queue:dead` is not empty, or a job stays in `queued` / `running` although the worker is up.
-   **Background**: If a job cannot reach a final status (for example, a temporary DB, Redis, or MinIO failure), the worker retries it after a 10 second backoff, up to 5 attempts. After that it first tries to mark the job `failed` and publish the completion event; only if that is impossible does it move the job ID to `sfsp:scan_queue:dead`.
-   **Check**: `LRANGE sfsp:scan_queue:dead 0 -1` (see B.2 for the `redis-cli` invocation), the `sfsp-worker` logs for the job ID, and `scan_jobs.status` / `scan_jobs.event_published_at` in `sfsp-db`.
-   **Solution**: Fix the root cause, then move a job back with `RPOPLPUSH sfsp:scan_queue:dead sfsp:scan_queue` (reprocessing is idempotent).

## 5. Maintenance

### Database Backups

-   Regularly back up the `sfsp-db` volume (`sfsp_db_data`).
-   Example: `docker exec sfsp-db sh -c 'pg_dump -U "$POSTGRES_USER" sfsp_db' > sfsp_db_backup.sql`

### MinIO Backups

-   Regularly back up the MinIO data directory on the host (`./backend/storage_data`, a bind mount shared by all buckets).

### Scanner Updates

-   **ClamAV**: Virus definitions are fetched with `freshclam` when the `sfsp-clamav-client` image is built. The sandbox has no network (`NetworkMode: none`), so definitions cannot update at run time. Rebuild periodically with `docker compose build --pull sfsp-clamav-client` to refresh both the definitions and the `clamscan` binary.
-   **YARA**: The rules in `backend/security/sfsp/yara-rules/` are copied into the `sfsp-yara-client` image at build time. After editing them, run `docker compose build sfsp-yara-client` (the rules are not loaded dynamically).

### Scan and Extraction Limits

-   Defaults apply unless overridden: `SFSP_MAX_UPLOAD_BYTES` (2 GiB - 1), `SFSP_MAX_EXTRACTED_BYTES` (4 GiB), `SFSP_MAX_ZIP_ENTRIES` (20000), `SFSP_MAX_ZIP_ENTRY_BYTES` (2 GiB - 1), `SFSP_MAX_ZIP_RATIO` (1000), `SFSP_MAX_ZIP_DEPTH` (32), and the `SFSP_CLAMAV_*` options. ClamAV cannot scan a file of 2 GiB or more, so the per-file limits are capped at 2 GiB - 1.
-   No SFSP service reads `.env` directly: `sfsp-api` and `sfsp-worker` receive only the variables listed in their `environment` sections of `docker-compose.yml`. `SFSP_MAX_UPLOAD_BYTES` (sfsp-api) and all `SFSP_CLAMAV_*` / `SFSP_MAX_EXTRACTED_BYTES` / `SFSP_MAX_ZIP_*` (sfsp-worker) are already listed there as `${VAR:-default}`, with the same defaults as `internal/config/config.go`. To change a limit, set the variable in `.env` (the commented-out lines in `.env.example` show every option) and run `docker compose up -d sfsp-api sfsp-worker`. The effective values are logged at worker startup (`SFSP worker limits`).

## 6. Security Procedures

-   **Incident Response**: Refer to `backend/security/sfsp/docs/Incident_Guide.md`.
-   **Vulnerability Management**: Regularly scan Docker images and dependencies for known vulnerabilities.
-   **Access Control**: Ensure only authorized personnel have access to the Docker host and `.env` files. Do not mount `/var/run/docker.sock` or `.env` into `sfsp-worker`; only `sfsp-docker-proxy` mounts the socket (read-only). Do not copy `.env` into any image or pass it with `env_file`; the root `.dockerignore` excludes `.env` / `.env.*` from the build context.
-   **Key Rotation**: To rotate `SFSP_API_KEY` or `REDIS_PASSWORD`, change it in `.env` and recreate every service that uses it (`docker compose up -d`). Callers of `sfsp-api` must send the new `X-SFSP-API-Key` value.

---
*This is a living document and should be updated as the system evolves.*
