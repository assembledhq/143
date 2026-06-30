# Security Policy

> **Version:** 1.0 | **Effective date:** 2026-03-20 | **Last updated:** 2026-03-20

## Supported Versions

143 currently supports:

- The hosted service at `143.dev`
- The latest code on the repository's default branch

The project does not currently publish long-term support branches. Older forks,
stale deployments, and heavily modified self-hosted instances may not receive
coordinated fixes.

Self-hosted operators are responsible for monitoring this repository's security
advisories and applying patches in a timely manner.

## Scope

This policy covers:

- **Hosted service** — the infrastructure, application, and APIs operated at `143.dev`
- **Open-source codebase** — the code in this repository, including sandbox
  images, configuration defaults, and documentation

Out of scope:

- Third-party dependencies — report these to the relevant upstream maintainers
  first, then notify us if the vulnerability materially affects 143
- Self-hosted infrastructure — operators are responsible for securing their own
  deployments, secrets, networks, and host operating systems
- Social engineering, physical attacks, or denial-of-service testing against the
  hosted service

## Reporting a Vulnerability

Please do **not** open a public GitHub issue for security vulnerabilities.

**Preferred open-source channel:**

- Use GitHub's private security advisory reporting flow:
  [`github.com/assembledhq/143/security/advisories/new`](https://github.com/assembledhq/143/security/advisories/new)

This is the best path for vulnerabilities in the open-source codebase because
it keeps the report private while preserving the advisory, patch, credit, and
release workflow in GitHub.

**Hosted-service fallback:**

- Email [`security@assembled.com`](mailto:security@assembled.com)

If your report contains highly sensitive details (exploit code, credentials,
customer data), please request our PGP key before transmitting.

**Include in your report:**

- A clear description of the issue and its expected impact
- Reproduction steps or a proof of concept (minimized where possible)
- Affected versions, components, or deployment conditions
- Severity assessment (see [Severity Classification](#severity-classification) below)
- Any mitigations or candidate fixes you have already identified

We accept reports in English.

## Severity Classification

We use [CVSS v3.1](https://www.first.org/cvss/calculator/3.1) as a common
reference when discussing severity. For internal prioritization, we map to the
following tiers:

| Tier | CVSS Range | Description | Target Remediation |
|------|-----------|-------------|-------------------|
| **Critical** | 9.0 – 10.0 | Sandbox escape, RCE, credential exfiltration, cross-org data access | 90 days |
| **High** | 7.0 – 8.9 | Privilege escalation, auth bypass, prompt injection with demonstrated impact | 120 days |
| **Medium** | 4.0 – 6.9 | Information disclosure, CSRF, limited-scope injection | 180 days |
| **Low** | 0.1 – 3.9 | Minor information leaks, best-practice deviations | 365 days |

143 is maintained by a small team. These targets are best-effort goals, not
guarantees. We will prioritize based on severity and exploitability, but actual
timelines may vary based on complexity, contributor availability, dependency
constraints, and coordinated disclosure requirements. We may publish
mitigations or workarounds while a full fix is in progress.

## Response Process

| Step | Target |
|------|--------|
| **Acknowledge receipt** | 14 calendar days |
| **Initial triage and severity assessment** | 30 calendar days |
| **Investigation complete; remediation plan shared with reporter** | 60 calendar days |
| **Remediation deployed (hosted) / patch released (open source)** | Per severity tier above |
| **Public advisory published** | Within 30 days of fix availability |

143 is maintained by a small team alongside other responsibilities. We will do
our best to meet these targets but cannot guarantee them. If we cannot meet a
target, we will communicate the revised timeline to the reporter and explain
the delay.

## Coordinated Disclosure

We practice coordinated disclosure:

1. **During investigation**: The vulnerability details remain confidential
   between the reporter and the 143.dev security team.
2. **After remediation**: We publish a GitHub Security Advisory (GHSA), request
   a CVE identifier where appropriate, and release a patched version.
3. **Reporter publication**: Reporters may publish their findings **after** the
   public advisory is issued or **180 days after the initial report**, whichever
   comes first, regardless of fix status.
4. **Self-hosted notification**: Security advisories are published via GitHub's
   advisory system. Self-hosted operators should watch this repository or
   subscribe to advisory notifications.

We will not request indefinite embargo. If we are unable to remediate within 180
days, we will work with the reporter on a disclosure timeline that balances user
safety with transparency.

## Safe Harbor

143.dev supports good-faith security research. We commit to the following for
researchers who comply with this policy:

- **We will not initiate legal action** (civil or criminal, including under the
  CFAA or equivalent statutes) against researchers acting in good faith.
- **We will not pursue claims** that good-faith research constitutes
  unauthorized access.
- **We will work with researchers** to understand and validate reports before
  taking any enforcement action related to their testing.
- **If a third party initiates legal action** against a researcher for activity
  conducted in accordance with this policy, we will make clear that the
  research was authorized.

Good-faith research must:

- Avoid privacy violations, data destruction, spam, social engineering, and
  denial of service
- Stop immediately if non-public customer or user data is accessed, and report
  the access in the vulnerability report
- Not establish persistence, modify production data, or pivot to third-party
  systems
- Not disrupt service availability for other users of the hosted service

If a test could materially impact users or service availability, contact us
**before** proceeding. We can provide a staging environment for high-risk
testing.

## Bug Bounty

We do not currently operate a formal bug bounty program. We offer public
recognition (with your permission) and are open to providing goodwill rewards
for exceptional reports at our discretion. This position may change in the
future — check this document for updates.

## Recognition

With your permission, we are happy to credit researchers in security advisories,
release notes, and a SECURITY_ACKNOWLEDGMENTS file after a fix is available.

## Contact

| Channel | Address |
|---------|---------|
| **Open-source security advisories** (preferred) | [`github.com/assembledhq/143/security/advisories/new`](https://github.com/assembledhq/143/security/advisories/new) |
| **Hosted-service fallback email** | [`security@assembled.com`](mailto:security@assembled.com) |

For non-security bugs, please open a regular [GitHub issue](https://github.com/assembledhq/143/issues).

## Policy Changes

Material changes to this policy will be communicated via a commit to this file.
The version number and effective date at the top of this document reflect the
current revision.
