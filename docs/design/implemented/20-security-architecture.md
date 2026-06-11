# Design: Security Architecture

> **Status:** Implemented | **Last reviewed:** 2026-04-21
>
> **Implementation notes:** The baseline production security model is live: sandbox isolation with gVisor-by-default workers, prompt-input sanitization, webhook signature verification, RBAC, request body limits, rate limiting, credential encryption at rest, audit-log immutability, and retention cleanup jobs. The remaining hardening work is tracked in the future sections below.

This document describes the security controls that are part of the product today, followed by security work that remains intentionally deferred.

## Threat Model

143 accepts untrusted external inputs, feeds some of them to LLMs, runs agent-produced code in sandboxes, and stores credentials for repo and integration access. The main risks are:

1. Prompt injection through issues, stack traces, and review comments
2. Data exfiltration through generated code, logs, or outbound network access
3. Sandbox escape or abuse of host-level container privileges
4. Credential compromise
5. Cross-org data access
6. Privilege escalation inside the app

## Implemented Controls

### 1. Sandbox Isolation

The worker runtime treats the sandbox as the main security boundary for agent execution.

- gVisor (`runsc`) is checked on worker startup by launching a small configurable health-check image under the runtime; worker deployments set `SANDBOX_REQUIRE_GVISOR=true`
- sandbox containers run with dropped Linux capabilities
- sandbox processes run as a non-root user
- PID and resource limits are enforced
- host-level egress controls are applied through the worker firewall scripts and deployment automation

This is the implemented baseline. The code is the source of truth in:

- [internal/services/agent/providers/docker.go](../../../internal/services/agent/providers/docker.go)
- [docker-compose.worker.yml](../../../docker-compose.worker.yml)
- [deploy/scripts/sandbox-firewall.sh](../../../deploy/scripts/sandbox-firewall.sh)

### 2. Prompt Injection Defense

Untrusted text is sanitized before it is embedded into prompts.

- issue and review-comment sanitization strips markdown fence and XML-like prompt-shaping content
- validation prompts wrap diffs in explicit delimiters
- review feedback processing uses the same sanitization layer

Primary implementation:

- [internal/sanitize/sanitize.go](../../../internal/sanitize/sanitize.go)
- [internal/services/validation/service.go](../../../internal/services/validation/service.go)

### 3. Authentication, Authorization, and Request Guards

The application enforces the main app-layer controls expected for a multi-tenant control plane.

- role-based access control via `RequireRole`
- request body size caps via `MaxBodySize`
- per-org and per-IP rate limiting via `RateLimit` and related specialized limiters
- CSRF protection for authenticated browser traffic
- webhook HMAC verification for GitHub and ingestion providers

Primary implementation:

- [internal/api/router.go](../../../internal/api/router.go)
- [internal/api/middleware/rbac.go](../../../internal/api/middleware/rbac.go)
- [internal/api/middleware/ratelimit.go](../../../internal/api/middleware/ratelimit.go)
- [internal/api/middleware/body_limit.go](../../../internal/api/middleware/body_limit.go)
- [internal/api/handlers/webhooks.go](../../../internal/api/handlers/webhooks.go)
- [internal/api/handlers/ingestion_webhooks.go](../../../internal/api/handlers/ingestion_webhooks.go)

### 4. Secrets and Credential Storage

Stored credentials are encrypted at rest using envelope encryption.

- `ENCRYPTION_MASTER_KEY` is required in production
- credential blobs are encrypted before database write
- startup config validation rejects missing or obviously unsafe production secrets

Primary implementation:

- [internal/crypto/encryption.go](../../../internal/crypto/encryption.go)
- [internal/config/config.go](../../../internal/config/config.go)

### 5. Audit and Retention

Security-sensitive metadata is protected and old raw data is cleaned up.

- `audit_log` is append-only via a database trigger
- retention cleanup jobs exist for raw payloads and logs
- metadata is retained longer than bulky raw content

Primary implementation:

- [migrations/000001_init.up.sql](../../../migrations/000001_init.up.sql)
- [internal/worker/handlers.go](../../../internal/worker/handlers.go)

## Future Hardening

These are desirable additions, but they are not required to describe the currently implemented security baseline.

### Future: Docker Socket Proxy

The design originally called for a Docker socket proxy so the API process would not have broad direct Docker control. That is still worth doing for stricter host isolation, but it is not the current deployment model.

### Future: Read-Only Root Filesystem for Sandboxes

The original design assumed a read-only root filesystem. The current sandbox bootstrap flow still needs a writable rootfs for package/tool setup. If bootstrap is refactored so all mutable setup happens elsewhere, this can be tightened.

### Future: Full Security Scan Stage in Validation

The design called for an explicit security-scan stage with tools such as secret scanning and SAST. Validation already has security-oriented checks, but the full standalone stage described in the original design is not yet implemented as written.

### Future: Postgres RLS as Defense in Depth

The product relies primarily on application-layer `org_id` filtering plus tenancy lints. Row-Level Security is still a possible second layer, but it is not the current enforcement model.

### Future: API Key Scoping

The design included scoped API keys for non-browser clients. That full system is not currently live and should remain future work until there is a real product need for machine-to-machine auth beyond the existing flows.

### Future: Broader PII Redaction Policy

We already limit raw-data retention, but the stricter design goal is consistent redaction across logs, traces, eval payloads, and operator-facing diagnostics. That should be implemented as a dedicated cross-cutting pass, not piecemeal.

## Relationship to Other Docs

- Validation-specific checks live in [07-validation.md](../07-validation.md)
- infrastructure deployment assumptions live in [10-infrastructure.md](../10-infrastructure.md)
- audit-log behavior is also described in [34-audit-logs.md](34-audit-logs.md)
