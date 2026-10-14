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
   Start with a fixed 15-second deadline and tune it from attributed production traffic. Head/base changes, review
   and merge safety checks, repairs, and manual refreshes must bypass the delay;
   a pending delayed sync must never absorb urgent work. Extend
   `internal/services/github/pr_health_service.go`. Replay bursts to verify fewer
   remote requests, bounded delay, and no lost final check state. Keep initial
   viewer reads shared and detached from browser cancellation. Persist check
   projection, health summary, and CI status atomically so parallel lanes cannot
   replace a newer revision with an older completed read. Track authoritative
   GitHub reads separately from webhook projection versions, so a racing webhook
   cannot discard fresh mergeability data; overlay its check state afterward.

4. **Prepare review inputs once and protect the remaining quota.** Reuse the
   existing shared checkout and captured assessment inputs. First, resolve exact
   base/head commits and compute the merge-base diff once in the shared workspace;
   pass that artifact to reviewers instead of URL-driven discovery. Keep immutable
   inputs scoped to tenant, repository, and revisions, with mutable metadata
   freshness tracked separately. Route missing-input fetches through the
   coordinator and fail explicitly if correct inputs are unavailable. Repository
   preparation must finish before reviewer execution. Recover an interrupted owner
   only after proving its durable job and executor leases are gone; reclaim the
   incomplete workspace with existing generation and live-holder guards before
   retrying preparation. Healthy-owner polls stay read-only; recovery uses indexed
   ownership checks, bounded locks, and the existing runtime inbox-loss transition.
   A timeout alone does not establish owner death. Reused bundles tolerate reviewer
   workspace edits while validating captured commits and immutable evidence. Use a
   valid shell environment name for ephemeral fetch credentials and retry temporary
   readiness failures without consuming reviewer attempts.
   Validate reused inputs before claiming a thread runtime or seed inbox messages,
   and persist a failed reviewer result when immutable evidence is invalid.
   Start the shared eight-minute deadline at workspace setup after capacity
   admission, excluding queue age. Carry it, including earlier caller
   deadlines, through input generation and hydration. After that deadline, allow
   one minute only to verify an existing bundle using local objects; do not fetch,
   repair, generate inputs, or renew preparation. Earlier caller deadlines still
   apply. Give input-generation and hydration Git commands the
   remaining budget and use managed Docker process cleanup before removing the
   preparation-owner marker. Canceling a waiting sibling must not cancel the
   owner; canceling the owner must wait for bounded cleanup. Hydrate missing
   captured tree objects through the coordinator before computing the patch.
   Verify existing bundles once per turn using local captured objects and digests,
   without full-history integrity scans or credential-oriented configuration
   guards. Keep strict trust and integrity checks for creation and hydration.
   Pin a ready container with a distinct short setup lease until its thread
   runtime holder exists, retaining the owner-recovery barrier before acquisition.
   Reviewers analyze the same read-only evidence independently; the coordinator retains final
   freshness checks and invalidates stale assessments. Measure reviewer REST calls
   before and after this change; further broker work depends on demonstrated need.
   As quota falls, slow background reconciliation before active reviews and final
   checks, with bounded eventual background progress. Start in
   `internal/services/agent/review_workspace.go`,
   `internal/worker/code_review_handler.go`, and
   `internal/prompts/templates/code_review_reviewer.template`.

   Default the background reserve to zero until attributed traffic supports an
   operator-selected threshold. Observation records decisions without suppressing
   work; enforcement requires an explicit matching installation allowlist. Positive
   reserves gate only scoped stale reconciliation sweeps, with shared admission and expiring fair
   candidates. Ordinary queued refreshes, active/final reviews, database recovery,
   and publication reconciliation bypass budgeting. Unknown quota or unavailable
   Redis permits work and exposes the optimization's limits. Admission intervals do
   not change the ten-minute sweep cadence; absent queue heads can delay successors
   until their retention expires.
   Deduplicate sweep admission before joining the ordinary PR read flight, so an
   admitted sweep reuses in-flight reads without allowing a suppressed sweep to
   delay ordinary refreshes.

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
- [x] Implement and validate coalescing, including concurrent PostgreSQL scheduling.
- [x] Implement and validate shared inputs and opt-in background budgeting.
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

Preserve captured assessment identity when a base commit is unavailable; fail
explicitly and require recapture or retry instead of silently substituting a new
base. Automatic recovery remains limited to proven unstarted platform reviewers;
other concurrent waiters retain the bounded workspace retry and restart path.
Fencing an incomplete workspace clears its snapshot reference and requires a cold
clone because recovery cannot establish that the checkpoint contains valid inputs.
Keep complete binary patches for integrity and permit reviewers to skip encoded
payloads while identifying any missing visual evidence.

## Outcomes & Retrospective

Enforcement configuration is prepared with an attended-rollout gate. Attribution
and coalescing are merged, with their deployments checked healthy. Shared inputs
have real Git and recovery tests; budgeting has fair-admission and
standalone/Cluster Redis tests. Shared-input deployment and measured savings
remain pending. The background reserve remains disabled by default; prepared
code and configuration do not establish production impact.
