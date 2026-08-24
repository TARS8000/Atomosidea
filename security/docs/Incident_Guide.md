# SFSP Incident Response Guide

This guide provides a high-level framework for responding to security incidents related to the Secure File Scanning Platform (SFSP).

## 1. Roles and Responsibilities

-   **Incident Commander (IC)**: The person responsible for leading the incident response.
-   **Security Team**: Responsible for technical investigation and containment.
-   **Operations Team**: Responsible for system-level actions (e.g., taking services offline, restoring from backup).

## 2. Incident Classification

| Severity | Description                                                                                             | Example                                                                                             |
| :------- | :------------------------------------------------------------------------------------------------------ | :-------------------------------------------------------------------------------------------------- |
| **SEV-1 (Critical)** | Active compromise of the host system, data breach, or service-wide outage. Immediate action required. | A sandbox escape is confirmed. Sensitive data from the database has been exfiltrated.               |
| **SEV-2 (High)**     | A single service is compromised, potential for data exposure, or significant service degradation.       | The `sfsp-api` is compromised, but the host and other services appear unaffected. A DoS attack is successful. |
| **SEV-3 (Medium)**   | A vulnerability is discovered but not yet exploited. Minor service issues.                              | A new critical vulnerability is announced for a dependency (e.g., Docker, Gin, PostgreSQL).         |
| **SEV-4 (Low)**      | Minor security misconfiguration or bug with no immediate impact.                                        | A MinIO bucket has overly permissive policies but contains no sensitive data.                       |

## 3. Incident Response Phases (PICERL)

### 3.1. Preparation

-   **This guide is part of the preparation phase.**
-   Ensure all team members are familiar with this guide.
-   Ensure access to logs, monitoring dashboards, and system consoles is readily available.
-   Conduct regular drills and tabletop exercises.

### 3.2. Identification

-   **How are incidents detected?**
    -   Alerts from monitoring systems (e.g., high CPU, unusual network traffic).
    -   Anomalies in application logs (e.g., repeated errors, suspicious API calls).
    -   External reports (e.g., from users, security researchers).
    -   Findings from vulnerability scans.
-   **Initial Actions**:
    1.  Acknowledge the alert/report.
    2.  Declare an incident and assign an Incident Commander.
    3.  Create a dedicated communication channel (e.g., Slack channel, conference call).
    4.  Start a timeline document to log all actions, observations, and decisions.

### 3.3. Containment

The goal is to stop the bleeding and prevent further damage.

-   **Short-term Containment (Example Actions)**:
    -   **Sandbox Escape / Host Compromise (SEV-1)**:
        -   Immediately isolate the affected host from the network.
        -   Shut down the `sfsp-worker` service to prevent new jobs from running.
        -   Consider taking the entire SFSP offline.
    -   **API Compromise (SEV-2)**:
        -   Block the attacker's IP address at the firewall/API gateway.
        -   Rotate credentials and API keys associated with the `sfsp-api` service.
        -   Restart the `sfsp-api` service from a known-good image.
    -   **DoS Attack (SEV-2)**:
        -   Implement rate limiting or IP blocking at a higher level (e.g., Cloudflare, AWS WAF).
        -   Temporarily scale down services to reduce impact.

### 3.4. Eradication

The goal is to remove the root cause of the incident.

-   **Actions**:
    -   Identify the vulnerability that was exploited.
    -   Patch the vulnerability (e.g., update a dependency, fix a code flaw, correct a misconfiguration).
    -   Deploy the fix.
    -   Scan the entire system for any backdoors or persistence mechanisms left by the attacker.
    -   If a host was compromised, it should be rebuilt from a trusted base image/snapshot. **Do not trust a compromised system.**

### 3.5. Recovery

The goal is to restore services to normal operation.

-   **Actions**:
    -   Bring services back online in a controlled manner.
    -   Monitor closely for any signs of recurrence.
    -   Restore data from backups if necessary.
    -   Confirm with stakeholders that the service is operating normally.

### 3.6. Lessons Learned (Post-Mortem)

This is the most important phase for long-term improvement.

-   **Actions**:
    -   Conduct a blameless post-mortem meeting.
    -   Analyze the timeline of the incident.
    -   Identify the root cause(s).
    -   What went well? What didn't go well? Where did we get lucky?
    -   Create actionable follow-up items to improve defenses, detection, and response processes.
    -   Update this guide and other relevant documentation.

## 4. Playbooks (Specific Scenarios)

### Playbook: Sandbox Escape

-   **Identification**: A file scan results in unexpected activity on the worker host (e.g., new processes, network connections, file modifications).
-   **Containment**:
    1.  Immediately execute `docker compose stop sfsp-worker`.
    2.  Isolate the host from the network.
    3.  Preserve the malicious file from MinIO for analysis.
-   **Eradication**:
    1.  Analyze the file and the scanner vulnerability that was exploited.
    2.  The host must be considered fully compromised. Rebuild it from scratch.
    3.  Patch the scanner or its base image.
-   **Recovery**:
    1.  Deploy the patched worker and scanner images.
    2.  Bring the new host online.
    3.  Resume worker service.
-   **Lessons Learned**: Was the sandbox configuration adequate? Was the vulnerability in a known CVE? How can we improve our detection of sandbox escapes?

---
*This is a living document and should be updated as the system evolves.*
