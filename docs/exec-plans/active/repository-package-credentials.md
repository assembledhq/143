# Repository package credentials for dependency setup

Status: Implementation, independent review, and local validation complete.
Merge, deployment, and live credential configuration are outside the completed
work recorded here.

This repository has no `.agent/PLANS.md` or `docs/AGENTS.md`. This plan follows
the existing active ExecPlan convention and keeps its decisions, validation,
and remaining work explicit.

## Purpose and observable outcome

An administrator can configure an encrypted npm registry credential for one
repository in one organization. A declared dependency setup phase uses that
credential to install locked packages before ordinary bootstrap commands,
automation work, or code review preparation. Agent processes and arbitrary
repository commands do not receive the credential.

The feature must work for both npm workspaces and an independently locked
package nested inside the same repository. Public examples and tests use
synthetic registry hosts, scopes, repositories, and tokens.

## Context

`internal/services/agent/env.go` resolves known coding and integration
credentials. It does not copy arbitrary worker environment variables into
tenant sandboxes. Preview secret bundles apply to preview runtime after
installation; their existing delivery policy does not grant setup access.

Ordinary session bootstrap, automation turns, and built-in review preparation
have separate entry points. Reviews already separate private Git transport
from repository recipe execution. Package authentication must preserve that
boundary and must not rely on a temporary file readable by sibling agent
processes in a warm sandbox.

## Progress

- [x] Inspect the current runtime, preview secret boundary, and settings UI.
- [x] Implement dedicated encrypted storage, metadata-only admin API, and scoped
  revision-checked rotation/deletion.
- [x] Implement the package registries settings screen and focused UI tests.
- [x] Finish isolated npm installation and integration into sessions,
  automations, and review preparation.
- [x] Verify monorepo manifests, nested dependency outputs, lifecycle-script
  separation, and safe cleanup on success, failure, and cancellation.
- [x] Complete independent review and address material findings.
- [x] Run focused tests, vet, tenancy checks, and repository-required preflight.
- [x] Prepare the draft pull request with accurate validation and limitations.

## Decision log

1. Use a dedicated credential table and service. Existing preview bundles keep
   their runtime semantics. Package credential APIs never return plaintext or
   offer a reveal endpoint.
2. Scope every operation by both organization and repository. Bind identity,
   registry, npm scope, and revision inside the encrypted payload, and verify
   that binding after decryption. Require encryption; do not use a development
   plaintext fallback.
3. Keep only current ciphertext. Rotation and deletion require the caller's
   expected revision; stale requests cannot replace or delete a newer value.
   Audit events contain metadata only.
4. Support HTTPS registry origins and explicit npm scopes. Enforce one
   credential per registry origin and per package scope within a repository:
   npm selects authentication by registry origin rather than package scope.
5. Declare typed installs in `.143/config.json` as `setup.npm` entries with
   `directory` and optional `legacy_peer_deps`. Do not accept arbitrary
   credential-bearing shell commands or npm flags.
6. Run controlled `npm ci --ignore-scripts --no-audit --no-fund` in a distinct
   short-lived setup container. Stage validated manifests, use private
   temporary configuration/cache/log paths, pass private material through
   stdin, and never add it to the agent sandbox environment or Docker command
   arguments. Publish only validated dependency outputs after secret cleanup.
7. Include credential metadata revisions in setup reuse decisions. Resolve
   plaintext only for an authorized installation, after trust checks. PR setup
   must validate the authoritative head repository and exact head revision;
   forks, unknown trust, and stale heads fail closed.
8. Keep platform npm authentication out of arbitrary bootstrap/review recipes.
   Repository lifecycle scripts may run only after credential destruction.

## Delivery and ownership

The storage and API changes live in `internal/models`, `internal/db`,
`internal/services/packagecredentials`, `internal/api`, and a dedicated
migration. The settings page and typed client live under `frontend/src`.
Runtime work lives in `internal/services/agent`, its Docker provider,
`internal/repoconfig`, and process wiring. Public setup instructions live in
`docs/public/guides/private-packages.mdx`.

## Validation

Storage/API tests cover org and repository isolation, encryption context
binding, metadata-only responses and audit records, registry validation,
duplicate origin/scope rejection, stale revision handling, and rotation/deletion.
Frontend tests cover exact API requests, admin access, repository selection,
safe errors, and clearing secret inputs on success, cancellation, and navigation.
A deferred-response regression verifies that a stale save cannot dismiss a
newly opened credential dialog.

Runtime tests exercise a synthetic npm workspace plus a standalone nested
package, invalid paths and symlinks, malicious npm configuration and lifecycle
scripts, fork/stale-head denial, receipt invalidation, cleanup failure,
cancellation, and absence of tokens from commands, environment, logs, and
published outputs. The npm fixture uses real Node 24 and a synthetic TLS
registry behind a simulated Docker transport. Additional regressions cover
actual linked Git worktrees, first-install removal of stale nested outputs,
and cancellation of a real detached child process. Transport assertions check
the requested separate container, mounts, and environment. They do not prove
live Docker isolation.

The frontend production build and all 3,522 frontend tests passed. Affected
backend package suites, targeted runtime race tests, tenancy checks, and vet
passed. Full preflight passed after rebasing onto the current base branch.
Backend CI explicitly installs Node 24 so the real npm fixture executes there
as well. GNU-timeout-specific checks skipped locally because GNU timeout was
unavailable; extended security, database, and browser checks were not run.

Run focused Go tests and `go vet` for changed packages, the tenancy lints,
frontend checks, `make hooks-check`, and `PREFLIGHT_BASE=<base> make preflight`
before publication. A local pass does not establish hosted CI or deployment.

## Surprises and discoveries

- Automation preparation bypasses ordinary session bootstrap, so it requires
  its own setup integration and reuse fingerprint.
- A mode-0600 npm config in a warm agent sandbox does not isolate credentials
  from sibling processes with the same user identity.
- The motivating monorepo shape needs workspace manifests and nested
  `node_modules` outputs, not only a root manifest and lockfile.
- Independent review found preparation paths that bypassed setup in built-in
  reviews, resumed sessions, and PR repair. Runtime integration must cover
  those actual entry points, including fresh PR trust before decryption.
- Dependency publication must remove obsolete nested outputs when npm hoists
  a dependency, and setup receipts must support linked Git worktrees.
- Cancelling an attached Docker exec does not establish that the process
  exited. Publication requires explicit termination handling, and helper
  networking must preserve the sandbox's selected organization egress policy.
- Docker is not installed in the current local environment. Report any
  unavailable real-container test explicitly instead of treating mock provider
  tests as live isolation proof.

## Outcomes and retrospective

The implementation includes admin credential management, scoped encrypted
storage, controlled npm setup, and integration across session and review
lifecycle paths. Independent review findings were repaired and covered with
regressions. Local preflight passed; hosted CI remains required. Real-container
and database-backed extended verification require their respective test services.
No production credentials, deployment configuration, or live registry grants
have been changed by this implementation work.
