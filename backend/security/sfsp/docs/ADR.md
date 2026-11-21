# SFSP Architecture Decision Records (ADR)

This directory contains Architecture Decision Records (ADRs) for the Secure File Scanning Platform (SFSP). ADRs are short, immutable documents that capture a significant architectural decision, its context, the options considered, and the chosen solution.

## ADR Template

```
# [ADR-XXXX] Title of the Architectural Decision

## Context

Describe the forces at play, including the technological, political, social, and project-related aspects. What is the problem we are trying to solve?

## Decision

State the decision made.

## Status

- Proposed
- Accepted
- Superseded by [ADR-YYYY]
- Deprecated

## Consequences

Describe the positive and negative impacts of the decision.

## Alternatives Considered

- Option 1: Description and why it was not chosen.
- Option 2: Description and why it was not chosen.
```

## Existing ADRs

### [ADR-0001] Isolation of Scanning Environment using Docker Sandboxing

#### Context

The core requirement of the SFSP is to scan untrusted files. Executing scanning tools directly on the worker host or in a shared environment poses a significant security risk. A malicious file could exploit vulnerabilities in the scanning software or the operating system, leading to compromise of the worker, other jobs, or the host system. We need a robust mechanism to ensure that each scan operation is performed in a highly isolated and ephemeral environment.

#### Decision

Each file scan (ClamAV, YARA) will be executed within a dedicated, ephemeral Docker container (sandbox). These sandbox containers will be launched by the `sfsp-worker` for each scan task. The containers will be configured with strict security policies to minimize their attack surface and prevent escape.

#### Status

Accepted

#### Consequences

##### Positive

- **Enhanced Security**: Provides strong isolation between scan jobs and the worker host, significantly reducing the risk of compromise from malicious files.
- **Reproducibility**: Each scan runs in a consistent environment.
- **Resource Control**: Allows fine-grained control over CPU, memory, and network access for each scan job.
- **Ephemeral Nature**: Containers are removed immediately after the scan, preventing persistence of any malicious artifacts.

##### Negative

- **Performance Overhead**: Launching a new Docker container for each scan introduces overhead compared to running tools directly.
- **Complexity**: Adds complexity to the worker implementation (Docker client integration, image management, volume mounting).
- **Docker Daemon Dependency**: The worker becomes dependent on a Docker daemon, and the security of the Docker daemon itself becomes critical.
- **Image Management**: Requires building and maintaining separate Docker images for each scanner client (e.g., `sfsp-clamav-client`, `sfsp-yara-client`).

#### Alternatives Considered

- **Running Scanners Directly on Worker**:
    - **Reason for not choosing**: High security risk. A single vulnerability could compromise the entire worker and potentially the host.
- **Virtual Machines (VMs) for Sandboxing**:
    - **Reason for not choosing**: Higher overhead and complexity than Docker for ephemeral, per-job isolation. Slower startup times.
- **Custom Linux Namespaces/cgroups**:
    - **Reason for not choosing**: Extremely complex to implement and maintain compared to using Docker, which provides a well-established abstraction for these technologies.

#### Update (2026-10-26)

The decision itself (one ephemeral, isolated Docker container per scanner run) is unchanged. The implementation was hardened as follows:

- **Docker access**: `sfsp-worker` no longer mounts `/var/run/docker.sock` or `.env`. It talks to Docker only through `sfsp-docker-proxy` (`DOCKER_HOST=tcp://sfsp-docker-proxy:2375`, network `sfsp-docker-net`), which allows only the container and image endpoints the sandbox needs. The proxy does not inspect request bodies, so this reduces but does not remove the "Docker Daemon Dependency" risk above.
- **Sandbox settings**: `NetworkMode: none`, `CapDrop: ALL`, `no-new-privileges`, 1.5 GiB memory, 1 CPU, 256 PIDs, a 30 minute timeout, the scan volume mounted read-only, a read-only root filesystem with only a 512 MiB `/tmp` tmpfs writable, and `sfsp-api` / `sfsp-worker` themselves running as the non-root user `10001:10001`. The scanner runs directly as the container entrypoint.
- **Images**: Scanner images are built locally by `docker compose build` and are never pulled at run time. Virus definitions are fetched when `sfsp-clamav-client` is built, because the sandbox has no network.

#### Update (2026-11-21)

- **Request-body inspection**: `sfsp-docker-proxy` filters only by URL prefix and HTTP method, and the sandbox needs `CONTAINERS=1` and `POST=1`. A compromised `sfsp-worker` could therefore create a privileged container or bind-mount the host root (`Binds: ["/:/host"]`), or use `/containers/{id}/exec`, and gain root on the host. `sfsp-docker-guard` (`backend/security/sfsp-docker-guard`, Go standard library only) now sits between the two: it allows only the exact requests `internal/sandbox/docker.go` sends (`/_ping`, inspect of the allowed scanner images, create / start / wait / logs / delete of `sfsp-sandbox-*` containers labelled `app=sfsp-sandbox`), rebuilds the container-create body from an allowlist, and returns 403 for everything else. `sfsp-worker` reaches it over the internal network `sfsp-docker-guard-net` (`DOCKER_HOST=tcp://sfsp-docker-guard:2375`) and is no longer on `sfsp-docker-net`. If `docker.go` changes the container settings or the Docker API calls it makes, update `sfsp-docker-guard` too.
- **Guard limits**: The guard accepts exactly the resource limits `docker.go` sends (1.5 GiB memory, 1 CPU, 256 PIDs; `TestLimitsMatchSandboxDockerGo` fails when they drift), always rewrites the `/tmp` tmpfs to `rw,noexec,nosuid,nodev,size=<= 512m>`, and refuses to create or start a sandbox when 2 or more `app=sfsp-sandbox` containers are running or 32 or more exist (`SFSP_GUARD_MAX_ACTIVE_SANDBOXES` / `SFSP_GUARD_MAX_SANDBOXES`). The worker runs one sandbox at a time, so this only bites a compromised worker.
- **Monitoring dashboard**: `mon-backend` no longer mounts `/var/run/docker.sock` (`:ro` only makes the socket file read-only; the API stays fully usable). It uses a read-only `mon-docker-proxy` (`CONTAINERS` / `VOLUMES` / `PING`, `POST=0`) on the internal network `mon-docker-net`. All `GET /containers/...` requests still pass (inspect including environment variables, logs, file reads via archive / export, and the `attach/ws` WebSocket, which could write to the stdin of a container started with `stdin_open`; none is today).
- **Verdict**: A file is `clean` only if BOTH ClamAV and YARA report `clean`. The ClamAV result is decided by its exit code, and files that exceed a ClamAV limit or a ZIP extraction limit are `suspicious`.

#### Update (2026-10-29)

Cleanup only; the decision is unchanged:

- **Worker image**: `sfsp-worker` starts the sandbox through the Docker SDK, so its image no longer installs `docker-cli` or `yara`, and the `yara-rules` directory is no longer mounted into it. The YARA rules exist only inside the `sfsp-yara-client` image. The builder services `sfsp-clamav-client` / `sfsp-yara-client` run with `network_mode: none`.
- **Configuration**: No SFSP service receives the whole `.env` (no `env_file`, `.env` is not copied into images). The ClamAV and ZIP extraction limits are passed to `sfsp-worker` explicitly with the code defaults, so they can be tuned from `.env`.
- **Shared code**: SFSP uses `github.com/atmosidea/shared` (`config`, `queue`, `model`, `event`); its own `internal/model` and unused queue/storage helpers were removed. The legacy `target_service` alias `thumbnail` was removed (only `site-thumbnail` is accepted).
