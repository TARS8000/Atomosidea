# SFSP Threat Model

This document outlines the threat model for the Secure File Scanning Platform (SFSP).

## 1. System Overview

- **Purpose**: To scan untrusted files in an isolated environment.
- **Key Components**: API (`sfsp-api`), Worker (`sfsp-worker`), Scanners (ClamAV, YARA), Sandbox (Docker), Docker API proxy (`sfsp-docker-proxy`), Queue (Redis), Database (PostgreSQL, `sfsp-db`), Storage (MinIO).

## 2. Security Objectives

- **Confidentiality**: Scan results and user data must be protected from unauthorized access.
- **Integrity**: Files and scan results must not be tampered with.
- **Availability**: The scanning service must be available and resilient to denial-of-service attacks.
- **Containment**: Malicious files must be contained within the sandbox and not affect the host system or other services.

## 3. Threat Analysis (STRIDE Model)

### Spoofing

- **Threat**: An attacker spoofs the identity of a legitimate user or service.
- **Mitigation**:
  - All `/api/v1/*` endpoints of `sfsp-api` require the `X-SFSP-API-Key` header (compared in constant time). `sfsp-api` fails closed: it does not start without `SFSP_API_KEY`. The API is internal only and is not reachable through Nginx. End-user authentication is done by the calling services, not by `sfsp-api`.
  - Redis requires a password (`requirepass`), so a container on the same network cannot forge scan completion events without it.
  - Internal service-to-service communication stays within Docker networks marked `internal: true` (`sfsp-isolated-net`, `sfsp-db-network`, `sfsp-docker-net`).

### Tampering

- **Threat**: An attacker modifies a file in transit or a scan result in the database.
- **Mitigation**:
  - The SHA256 hash of each upload is computed by `sfsp-api` while streaming the file to MinIO and is stored in `files.sha256` (it is not compared with a client-supplied hash).
  - Database access is restricted to authorized services (`sfsp-db` is reachable only from `sfsp-api` and `sfsp-worker`).
  - Communication with MinIO, PostgreSQL, and Redis uses credentials, and each MinIO user has a least-privilege bucket policy.

### Repudiation

- **Threat**: A user denies having uploaded a malicious file.
- **Mitigation**:
  - Log all file uploads with user identifiers (to be implemented; currently `sfsp-api` logs only the file ID, job ID, size, and target service, and the calling service is identified by the shared API key).
  - Maintain audit trails for all scan jobs (`files`, `scan_jobs`, and `scan_results` in `sfsp-db`, including the raw scanner output).

### Information Disclosure

- **Threat**: An attacker gains access to sensitive information, such as scan results, other users' files, or system configuration.
- **Mitigation**:
  - The MinIO buckets `raw-files`, `clean-files`, and `quarantine` are private (no anonymous access).
  - Database credentials and other secrets are managed via environment variables, not hardcoded. No service receives the whole `.env` file (there is no `env_file`, and `.env` is excluded from every image by `.dockerignore`): each container gets only the variables its code reads, and credentials are redacted from the logs. Known gap: `sfsp-api` / `sfsp-worker` connect to `sfsp-db` with the shared PostgreSQL superuser (`POSTGRES_USER`), and the `sfsp-api` (`raw-files`) and `sfsp-worker` clean (`clean-files` / `quarantine` / `raw-files`) MinIO policies grant `s3:*` on their buckets.
  - API endpoints require the API key to access job/result details.
  - The raw upload is deleted from `raw-files` after the final status is recorded and the completion event is published.

### Denial of Service (DoS)

- **Threat**: An attacker overwhelms the system with large files, numerous requests, or "zip bombs".
- **Mitigation**:
  - Rate limiting on the API gateway (to be implemented).
  - File size limits on upload (`SFSP_MAX_UPLOAD_BYTES`, default 2 GiB - 1; larger uploads get HTTP 413).
  - ZIP extraction limits: entry count, size per entry, total extracted size, compression ratio, and directory depth (`SFSP_MAX_ZIP_*`, `SFSP_MAX_EXTRACTED_BYTES`). Exceeding a limit marks the job `suspicious` and the file is quarantined, never `clean`.
  - Resource limits on sandbox containers (1.5 GiB memory, 1 CPU, 256 PIDs).
  - Timeouts for scan operations (30 minutes per sandbox run; ClamAV `--max-scantime` 25 minutes).
  - A job that repeatedly fails is retried at most 5 times and then marked `failed` or moved to the dead list `sfsp:scan_queue:dead`, so it cannot block the queue forever.

### Elevation of Privilege

- **Threat**: A malicious file exploits a vulnerability in a scanner or the sandbox to gain control of the worker or host system. **(This is the most critical threat)**.
- **Mitigation**:
  - **Principle of Least Privilege**: The `sfsp-worker` container does not mount `/var/run/docker.sock` or `.env`. It reaches Docker only through `sfsp-docker-proxy` (`DOCKER_HOST=tcp://sfsp-docker-proxy:2375`), which allows only the container and image API endpoints needed by the sandbox. `sfsp-api` and `sfsp-worker` run as the non-root user `10001:10001`. Known gap: the proxy does not inspect request bodies (a compromised worker could still ask for a privileged container or host mounts).
  - **Sandbox Isolation** (`internal/sandbox/docker.go`):
    - Containers are ephemeral: each one is removed explicitly after the scan (`AutoRemove` is off so that the logs can be read first).
    - `NetworkMode: none`: No network access from the sandbox.
    - `CapDrop: ALL` and `SecurityOpt: no-new-privileges`.
    - Memory limit 1.5 GiB, 1 CPU, 256 PIDs, and a 30 minute timeout.
    - The scan volume (`sfsp_temp_scan`) is mounted at `/scan_target` as read-only. It holds the temporary directories of the jobs being processed (a single consumer, so normally one job). The root filesystem of the container is read-only (`ReadonlyRootfs: true`); the only writable path is a 512 MiB `/tmp` tmpfs (`noexec,nosuid,nodev`), which ClamAV uses for temporary files. If a scan needs more than that it ends with an error and the job becomes `failed`, never `clean`.
    - No `Privileged` flag is set, and host PID, IPC, and network namespaces are not shared. No other host paths are mounted.
    - Scanner images (`sfsp-clamav-client`, `sfsp-yara-client`) are built locally and are never pulled at run time.
  - **Safe Verdicts**: ClamAV is given explicit limits and `--alert-exceeds-max=yes`, so files that were not fully scanned are `suspicious` instead of `clean`. The ClamAV verdict is decided by the exit code. A job is `clean` only if BOTH ClamAV and YARA report `clean`; a scanner error makes the job `failed`.
  - **Scanner Hardening**: Keep scanner tools (ClamAV, YARA) and their base Docker images updated to patch vulnerabilities (rebuild with `docker compose build --pull sfsp-clamav-client`).

## 4. Security Assumptions

- The Docker daemon and the host OS are secure and properly configured.
- The underlying infrastructure (network, hardware) is secure.
- Secrets (DB passwords, MinIO keys, `SFSP_API_KEY`, `REDIS_PASSWORD`) are managed securely and not exposed.
- Each queue has a single consumer (one `sfsp-worker`), as assumed by the startup recovery of unacknowledged jobs.

---
*This is a living document and should be updated as the system evolves.*
