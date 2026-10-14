# Reduce GitHub requests during code review

Status: Implementation in progress; rollout pending.

Implementation started from current main after revalidating the source references.
Production configuration is prepared separately in private infrastructure.

## Goal

Reduce GitHub API requests per completed review and prevent quota exhaustion from
stalling active reviews, while preserving fresh checks before publication and merge.

## Plan

1. **Enforce the existing cooldown controller.** Set
   `GITHUB_RATE_LIMIT_MODE=enforce` with an explicit
   `GITHUB_RATE_LIMIT_INSTALLATION_ALLOWLIST` for a canary installation. Keep API
   and worker configuration consistent and use shared Redis state. Verify actual
   participation: requests defer during cooldown and one recovery probe resumes
   work after reset. Test empty and nonmatching allowlists, which enforce nothing;
   a mode label alone does not prove participation. Roll back to `observe` if
   recovery fails. This prevents futile retries after throttling, not initial
   quota exhaustion. Start in `internal/config/config.go` and
   `internal/services/github/ratelimit/controller.go`.

2. **Close request-attribution gaps.** Server telemetry already records
   installation, caller, route, resource, and quota headers. Add session attribution
   and instrumentation covering standard sandbox `gh` and `143-tools` GitHub API
   requests through the existing credential socket. GitHub CLI's supported Unix
   transport observes requests below its cache; credential issuance alone cannot
   count subsequent calls. Distinguish remote requests, cache hits,
   and deferrals in common counters, without credentials or request bodies. Track
   Git fetches separately from REST quota. Start in
   `internal/services/github/telemetry/transport.go` and sandbox authentication
   wiring. Reconcile observed requests with quota changes and keep unexplained
   usage explicit. Direct HTTP clients, explicit credentials/custom hosts, unsupported CLI commands,
   and Git transport remain visibility gaps. CLI cache hits are excluded from remote
   totals without inventing a cache-hit count. Establish reviewer traffic before
   expanding infrastructure; use the measurements to tune subsequent changes.

3. **Coalesce check-event bursts into fewer full PR refreshes.** Pending and
   running sync jobs already deduplicate by PR, but sync jobs have no run-at delay
   or chained successor for requests arriving during execution. Add a bounded
   delay for incomplete-check refreshes and one follow-up when needed, following
   the existing DB rebuild pattern. Preserve immediate local webhook updates.
   Tune the proposed 15–30 second window using step 2. Head/base changes, review
   and merge safety checks, repairs, and manual refreshes must bypass the delay;
   a pending delayed sync must never absorb urgent work. Extend
   `internal/services/github/pr_health_service.go`. Replay bursts to verify fewer
   remote requests, bounded delay, and no lost final check state.

4. **Prepare review inputs once and protect the remaining quota.** Reuse the
   existing shared checkout and captured assessment inputs. First, resolve exact
   base/head commits and compute the merge-base diff once in the shared workspace;
   pass that artifact to reviewers instead of URL-driven discovery. Keep immutable
   inputs scoped to tenant, repository, and revisions, with mutable metadata
   freshness tracked separately. Route missing-input fetches through the
   coordinator and fail explicitly if correct inputs are unavailable. Reviewers
   analyze the same read-only evidence independently; the coordinator retains final
   freshness checks and invalidates stale assessments. Measure reviewer REST calls
   before and after this change; further broker work depends on demonstrated need.
   As quota falls, slow background reconciliation before active reviews and final
   checks, with bounded eventual background progress. Start in
   `internal/services/agent/review_workspace.go`,
   `internal/worker/code_review_handler.go`, and
   `internal/prompts/templates/code_review_reviewer.template`.

## Validation and delivery

Deliver separate changes in this order: enforcement, attribution, coalescing, then
shared inputs and budgeting. Tune debounce and budget thresholds after attribution.
Before the canary, run `make test-github-rate-limit` and
`make test-github-rate-limit-redis`. Cover event bursts, updates during refresh,
urgent work bypassing a pending delayed sync, quota recovery, and background
progress. For shared inputs, add worker/agent tests for revision and tenant
isolation, missing commits, and stale assessments; run
`go test ./internal/worker/... ./internal/services/agent/...`.

Compare requests per completed review, refreshes per PR, quota-exhaustion time,
and review p50/p95 across comparable workloads. Confirm unchanged freshness
behavior before expanding rollout.

## Progress

- [x] Draft the four changes and acceptance checks.
- [x] Incorporate Fable's review and revised delivery order.
- [x] Prepare scoped enforcement configuration and validate cooldown/Redis behavior.
- [x] Implement and locally validate standard sandbox request attribution.
- [ ] Implement and validate coalescing.
- [ ] Implement and validate shared inputs and opt-in background budgeting.
- [ ] Complete deployment admission, rollout, and production impact measurements.

## Surprises & Discoveries

Reviewers already share a sandbox checkout and assessment inputs are captured
centrally. Repeated reviewer REST reads remain an unverified source of quota use;
shared-diff savings must be measured.

## Decision Log

Share evidence while keeping reviewer analysis independent. Instrument before
tuning or expanding infrastructure. Keep background quota reserve disabled by
default until attributed traffic supports an operator-selected threshold. Keep live installation identifiers and
production configuration in the private infrastructure repository.

## Outcomes & Retrospective

Enforcement configuration is prepared with an attended-rollout gate. Attribution
has local transport/cache/identity tests; deployment and measured savings remain
pending. Prepared code and configuration do not establish production impact.
