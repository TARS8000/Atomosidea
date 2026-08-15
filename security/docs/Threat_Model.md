# SFSP Threat Model

This document outlines the threat model for the Secure File Scanning Platform (SFSP).

## 1. System Overview

- **Purpose**: To scan untrusted files in an isolated environment.
- **Key Components**: API, Worker, Scanners (ClamAV, YARA), Sandbox (Docker), Database (PostgreSQL), Storage (MinIO).

## 2. Security Objectives

- **Confidentiality**: Scan results and user data must be protected from unauthorized access.
- **Integrity**: Files and scan results must not be tampered with.
- **Availability**: The scanning service must be available and resilient to denial-of-service attacks.
- **Containment**: Malicious files must be contained within the sandbox and not affect the host system or other services.

## 3. Threat Analysis (STRIDE Model)

### Spoofing

- **Threat**: An attacker spoofs the identity of a legitimate user or service.
- **Mitigation**:
  - API endpoints are protected by authentication/authorization mechanisms (to be implemented).
  - Internal service-to-service communication is within a trusted Docker network.

### Tampering

- **Threat**: An attacker modifies a file in transit or a scan result in the database.
- **Mitigation**:
  - Files are verified by SHA256 hash upon upload.
  - Database access is restricted to authorized services.
  - Communication with MinIO and PostgreSQL uses credentials.

### Repudiation

- **Threat**: A user denies having uploaded a malicious file.
- **Mitigation**:
  - Log all file uploads with user identifiers (to be implemented).
  - Maintain audit trails for all scan jobs.

### Information Disclosure

- **Threat**: An attacker gains access to sensitive information, such as scan results, other users' files, or system configuration.
- **Mitigation**:
  - `sfsp-minio` buckets for raw and quarantined files are private.
  - Database credentials and other secrets are managed via environment variables, not hardcoded.
  - API endpoints require authorization to access job/result details.

### Denial of Service (DoS)

- **Threat**: An attacker overwhelms the system with large files, numerous requests, or "zip bombs".
- **Mitigation**:
  - Rate limiting on the API gateway (to be implemented).
  - File size limits on upload.
  - Resource limits (CPU, memory) on sandbox containers.
  - Timeouts for scan operations.

### Elevation of Privilege

- **Threat**: A malicious file exploits a vulnerability in a scanner or the sandbox to gain control of the worker or host system. **(This is the most critical threat)**.
- **Mitigation**:
  - **Principle of Least Privilege**: The worker process does not run as root.
  - **Sandbox Isolation**:
    - `docker run --rm`: Containers are ephemeral.
    - `--network none`: No network access from the sandbox.
    - `--memory=512m --cpus=1`: Resource constraints.
    - `--read-only`: The container's root filesystem is read-only. The target file is mounted as read-only.
    - `--privileged` is forbidden.
    - Host PID, IPC, and network namespaces are not shared.
    - No host volumes are mounted other than the specific target file's directory (as read-only).
  - **Scanner Hardening**: Keep scanner tools (ClamAV, YARA) and their base Docker images updated to patch vulnerabilities.

## 4. Security Assumptions

- The Docker daemon and the host OS are secure and properly configured.
- The underlying infrastructure (network, hardware) is secure.
- Secrets (DB passwords, MinIO keys) are managed securely and not exposed.

---
*This is a living document and should be updated as the system evolves.*
