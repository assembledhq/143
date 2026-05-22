# Linear Agent

Assign a Linear issue to `@143` and the agent kicks off a coding
session, posts progress back to Linear, and ends with a PR linked to
the issue.

## Setup (admin, one-time)

1. **Connect Linear** in 143 (Settings → Integrations → Linear). The
   OAuth flow now requests the `actor=app` scopes Linear needs to
   provision a `@143` agent user in your workspace. If you connected
   Linear before this feature shipped, you'll see a **Re-authorize
   Linear** banner on Settings → Integrations — click it to grant the
   new scopes. **Re-authorization must be done by a Linear workspace
   admin**.

2. **Map your Linear teams to GitHub repos** at Settings →
   Integrations → Linear → Agent. Each row says "issues from team X
   (and optionally project Y) get worked on in repo Z". If your org
   has exactly one connected GitHub repo at re-authorize time it gets
   set as the org-default automatically, so single-repo setups need
   zero mapping configuration. Without any mapping or default, the
   agent posts a "configure a mapping" response and closes the
   AgentSession; nothing breaks, but no work happens.

3. **Agent toggle.** The agent is auto-enabled when a workspace admin
   completes the OAuth flow with agent scopes. If you ever turn it
   off explicitly, re-authorize will not retro-enable it — your
   choice is preserved.

## Using it

### Assignment

Open a Linear issue and assign it to **@143**. Within ~10 seconds the
issue gets a "Reading {KEY}…" thought from the agent, then a series
of progress activities as the run proceeds, then a final "Opened PR
#N" activity once the PR lands.

### @-mention

In an issue description or comment, @-mention **@143**. Same flow —
the agent picks up the issue context, runs in your mapped repo, and
posts back.

### Follow-up

Once a session is live, post a comment on the issue mentioning
**@143** with your follow-up instructions. The agent picks up the new
message as a turn on the existing 143 session and continues from
there. No need to start over.

## What the agent sees

When you assign or mention `@143`, the agent gets:

- the issue title and description
- the most recent comments
- linked attachments (URLs and titles)
- the Linear team and project context

It does *not* automatically have access to private 143 sessions, your
internal docs, or anything else outside the issue itself.

## Repo mapping rules (in priority order)

1. **`repo:<full-name>` label on the issue** — overrides everything.
   E.g. label `repo:acme/web` routes the issue to the `acme/web` repo
   regardless of team mapping.
2. **(team, project) exact match** — if your mapping table has an
   entry for the issue's team + project, that wins.
3. **Team default** — a mapping row for the team with no project set
   matches any issue in that team without a project-specific row.
4. **Org default** — `default_repo_id` in the agent settings catches
   issues whose team has no mapping at all.
5. **Otherwise** — the agent posts a clear "no repo configured for
   this team" response and closes the AgentSession. No retries; the
   issue stays where it is and an admin can fix the mapping.

## What "private session" / "don't auto-update Linear" do

These flags from design 62 still apply:

- **Private session**: suppresses *all* Linear writes (attachment,
  comment, state, agent activities). If a session was triggered by
  agent assignment, this flag isn't applicable — the AgentSession
  itself is the visibility surface and we can't quietly opt out of
  it.
- **Don't auto-update Linear**: leaves attachment + rolling comment +
  agent activities on, but suppresses workflow-state transitions
  (e.g. "In Progress" → "In Review" on PR open).

## Limits

- The agent always runs in **one repo per session**. If your issue
  spans multiple repos, the agent picks the mapped repo and the
  session stays single-repo. A multi-repo flow is a future
  enhancement.
- Linear **AgentSession state changes (close, etc.)** don't affect
  the running 143 session today. If you close the AgentSession in
  Linear, the 143 session keeps running and the eventual PR still
  opens.
- The agent **doesn't act on AppUserNotification webhooks** — only
  AgentSessionEvent. If your Linear webhook subscription is older,
  upgrade it via the Linear OAuth app dashboard to subscribe to
  Agent session events.

## Kill switch and rollout

The agent ships behind a process-wide kill switch in addition to the
per-org `enabled` toggle:

- `LINEAR_AGENT_ENABLED=true` (env var, default `false`) — must be true
  on API nodes that receive Linear webhooks for the feature to accept
  new inbound AgentSessions. Flip to `false` to **stop accepting new
  agent sessions** across every org in the deployment, without touching
  per-org state.
- **Drain semantics**: turning the kill switch off only gates new
  inbound webhooks at the dispatcher. AgentSessions that already have a
  `linear_agent_sessions` row continue to fan out milestone activities
  (`Started`, `PROpened`, `PRMerged`) until they reach a terminal state.
  This is deliberate — it lets you toggle the feature during an
  incident without stranding running coding work mid-PR.
- Per-org rollout is layered on top: `org_settings.linear_agent.enabled`
  (default `false`) must also be true for an org's webhooks to be
  dispatched. The per-team `enabled` map gates `created` events only;
  follow-up `prompted` events on an already-live session ignore the
  per-team gate so disabling a team mid-session doesn't strand work.

## Operator surfaces

- `GET /api/v1/integrations/linear/agent/sessions` — recent agent
  sessions for the org with state, linked 143 session id, and
  timestamps.
- `GET /api/v1/integrations/linear/agent/sessions/{id}` — full
  per-session detail including the activity log we sent to Linear.
- Metrics (OTel): `linear_agent.events`,
  `linear_agent.sessions_created`,
  `linear_agent.activities_emitted`,
  `linear_agent.activities_skipped_duplicate`,
  `linear_agent.bootstrap_emit_latency_ms`.
