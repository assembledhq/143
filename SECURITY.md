# Security Policy

## Supported Versions

143 currently supports:

- The hosted service at `143.dev`
- The latest code on the repository's default branch

The project does not currently publish long-term support branches. Older forks,
stale deployments, and heavily modified self-hosted instances may not receive
coordinated fixes.

## Reporting a Vulnerability

Please do not open a public GitHub issue for security vulnerabilities.

Preferred channel:

- Use GitHub's private security advisory reporting flow:
  `https://github.com/assembledhq/143/security/advisories/new`

Alternative channel:

- Email `security@assembled.com`

Include:

- A clear description of the issue and expected impact
- Reproduction steps or a proof of concept
- Affected versions, components, or deployment conditions
- Any mitigations or candidate fixes you already identified

## Safe Harbor

We support good-faith security research that:

- Avoids privacy violations, destructive testing, spam, social engineering, and denial of service
- Stops immediately if non-public customer or user data is accessed
- Does not establish persistence, modify data, or pivot to third-party systems

If a test could materially impact users or service availability, contact us
before proceeding.

## Response Targets

We aim to:

- Acknowledge reports within 3 business days
- Complete initial triage within 14 business days
- Remediate confirmed issues within 90 days when feasible

Actual remediation timing depends on severity, exploitability, dependency
constraints, user impact, and whether coordinated disclosure is needed.

## Scope Notes

- This policy covers the hosted service and the open-source codebase in this repository.
- Third-party dependency vulnerabilities should also be reported to the relevant maintainers.
- Self-hosted operators are responsible for securing their own infrastructure and secrets.

## Recognition

With your permission, we are happy to credit researchers in advisories or
release notes after a fix is available.
