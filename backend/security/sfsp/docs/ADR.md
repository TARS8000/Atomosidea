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
- **Verdict**: A file is `clean` only if BOTH ClamAV and YARA report `clean`. The ClamAV result is decided by its exit code, and files that exceed a ClamAV limit or a ZIP extraction limit are `suspicious`.
