# Design: Session Artifacts Product and UX

> **Status:** Not Started | **Last reviewed:** 2026-08-02
>
> **Depends on:** [overall architecture](../overall.md),
> [frontend architecture](../03-frontend.md),
> [visual system](../implemented/117-visual-system-and-product-polish.md),
> [session transcript refactor](../implemented/101-session-transcript-refactor.md),
> [mobile session top bar](../implemented/70-mobile-session-top-bar-consolidation.md),
> [agent run capabilities](../implemented/102-agent-run-capabilities.md),
> [audit logs](../implemented/34-audit-logs.md),
> [slackbot product surface](92-slackbot-product-surface.md),
> and [agent-native preview verification](115-agent-native-preview-verification.md)

## Product Bet

**An artifact is how a session's output leaves the session.**

143 sessions run unattended. The supervising engineer is usually not watching,
and the people who care about the result — a designer, a PM, an on-call lead —
are frequently not in 143 at all. Today the only things that escape a session
are a PR and a Slack notification. Everything else the agent produced dies with
the sandbox.

Every open question in this document resolves against that sentence. It is the
reason sharing is P0 rather than a later phase, the reason a lone terminal
artifact is promoted at the moment of delivery, the reason a long run can keep
one page updating while it works, and the reason artifact identity has to be
stable enough that republishing produces a version instead of a duplicate.

The bet also sets the limit. An artifact is a *capture of work*, not an
application and not a workspace browser. 143 does not become an asset manager
because an agent wrote some files.

## Decision

143 will treat artifacts as durable outputs of a session, not as another session
navigation destination.

Artifacts have three first-class product surfaces:

1. **The transcript is the primary discovery surface.** A compact publication
   row appears at the turn that created or updated an artifact. This is where
   users actually find artifacts — at the moment of production.
2. **Overview is the durable index.** A compact `Artifacts` section appears near
   the top of the existing session Overview only when artifacts exist, so an
   output stays findable long after its turn scrolls away.
3. **A durable org-scoped link is the export surface.** Every artifact has a
   stable URL that any member of the organization can open, unfurls in Slack,
   and outlives the sandbox.

Selecting an artifact opens it in the session's center workspace. There is no
new detail tab, no permanent artifact shelf, and no featured artifact in the
list. Every artifact uses the same visual hierarchy wherever artifacts are
listed.

The Overview does not render one card per artifact. It renders a quiet,
borderless resource list with at most two artifacts visible at rest. Larger
collections use one `View all` disclosure. This gives artifacts a hard space
budget and prevents the detail rail from growing in proportion to agent output.

```text
agent publishes with `143-tools artifacts publish`
          |
          +--> compact event in the originating transcript turn
          |
          +--> compact row in Overview > Artifacts
          |
          +--> durable org link (Slack unfurl, deep link)
                         |
                         +--> selected artifact opens in center workspace
```

## Product Intent

Coding agents can already create useful files, screenshots, HTML prototypes,
reports, and diagrams in their workspace. Those outputs are difficult to
discover after the turn that created them and may disappear with the runtime.
The user should be able to recognize, open, revisit, and forward a meaningful
output without browsing a sandbox filesystem or reading a long transcript.

The feature must not make every session look like an asset manager. Most of the
session remains conversation, code review, and operational state. Artifacts
earn a small, predictable place within that hierarchy only after they exist.

The desired character is restrained and self-evident:

- content before containers;
- one obvious action per object;
- detail appears only after intent;
- repeated objects form a list, not a stack of decorative cards;
- visual previews identify an artifact but do not compete with the work;
- the interface is absent when it has nothing useful to say.

Restraint governs the *list*. It does not govern the *moment of delivery*: a
session whose entire deliverable is one report is allowed to show that report.
See [Delivery Moment](#delivery-moment).

## Goals

1. Let an agent publish a workspace output through one `143-tools` command,
   with identity stable enough that republishing versions rather than
   duplicates.
2. Make all session artifacts discoverable from the transcript at production
   time and from Overview afterwards, without adding a tab.
3. Preserve the turn and version that produced each artifact in the transcript.
4. Render images, HTML, PDFs, text, and later formats in an appropriate viewer.
5. Give every artifact a durable org-scoped link that survives the sandbox and
   unfurls in Slack.
6. Keep the Overview calm and bounded with zero artifact chrome for sessions
   that have no artifacts.
7. Reuse 143's dense resource-row, semantic-token, selected-state, and mobile
   details patterns.
8. Make artifacts durable independently of the live sandbox and preview runtime.
9. Govern publication with the existing agent capability and org policy model,
   with audit coverage and a stated retention default.

## Non-Goals

This product/design phase does not define:

- collaborative artifact editing;
- comments or annotations on artifacts;
- a spatial canvas or mood board;
- a global cross-session artifact library (deferred, but its URL space is
  reserved — see [URL and identity space](#url-and-identity-space));
- **public, unauthenticated sharing.** This is an explicit product decision, not
  a deferral. 143 artifacts are built by an agent with read access to a private
  repository; a link that works without sign-in is a data-exfiltration surface
  we do not want on by default. Org-scoped sharing is P0; public sharing is a
  later feature that must ship behind an org owner toggle if it ships at all.
- artifact generation by the platform without an explicit agent publication;
- a replacement for source control, the Changes tab, or the live Preview tab;
- the final database schema, object-storage layout, or API implementation.

Deletion and retention are **not** non-goals. See
[Retention, Deletion, and Audit](#retention-deletion-and-audit).

## Naming

### The word is already taken twice

`Artifact` is not a free name in 143. It currently means three unrelated
things, two of them on screen today:

| Existing use | Where | User-visible |
| --- | --- | --- |
| Preview tooling screenshot blob | `PreviewArtifact`, `internal/models/preview.go:841` | Indirectly |
| Code-review context bundle | `ReviewArtifactKey`, `internal/models/models.go:700`; `internal/services/reviewartifact` | No |
| Stored LLM prompt/response records | "Prompt artifacts" heading, `frontend/src/app/(dashboard)/code-reviews/page.tsx:3314` | **Yes** |
| Eval run output count | "Artifacts" column, `frontend/src/app/(dashboard)/settings/evals/[id]/page.tsx:272` | **Yes** |
| Dependency/build cache blobs | `install_artifact`, `build_artifact` cache kinds, `internal/models/preview.go:663` | No |

Shipping a user-facing `Artifacts` section without resolving this produces a
product where the same word labels a prototype, a prompt log, and an eval
output count on three different screens.

### How the market named it

| Product | Name | Scope of the noun |
| --- | --- | --- |
| Claude Code / claude.ai | **Artifacts** | Published live page, versioned, shareable |
| Google Antigravity | **Artifacts** | Task lists, plans, walkthroughs, screenshots, recordings |
| Cursor | **Canvases** | Interactive visual output rendered in the chat panel |
| Codex | "artifact previews" | Secondary capability, not a headline noun |
| Manus | "deliverables" (prose) | Doc, report, deck, small site |

Two of the three major agent platforms landed on exactly `Artifacts`, and
Antigravity's definition — *tangible deliverables in formats easier to validate
than raw tool calls* — is almost precisely 143's intent. The word is becoming
the category standard, and users arriving from Claude Code or Antigravity will
already know what it means.

Alternatives considered and rejected:

- **Canvases** (Cursor) — evocative, but scoped to interactive rendered output.
  It reads wrong on a PDF, a screenshot, or a CSV, which are most of 143's P0.
- **Outputs** — accurate and plain, but too close to log output and tool output,
  both of which appear in the same transcript.
- **Deliverables** — implies a commitment to a stakeholder that a mid-run
  screenshot does not carry. Also enterprise-flavored in a way the rest of the
  product is not.
- **Results** — already load-bearing: the Overview leads with the session
  `Result` summary.
- **Files** — implies a browsable workspace, which is the exact wrong mental
  model.
- **Evidence** — right for verification captures, wrong for a prototype.

### Decision: keep `Artifacts`, enforce one meaning

`Artifact` means exactly one thing in every user-facing 143 surface: **a durable
output intentionally published by an agent during a session.** The CLI namespace
is `artifacts`. `Outputs`, `Files`, `Attachments`, and `Deliverables` are not
introduced as competing names for the same object.

Enforcing that requires clearing the existing uses, in cost order:

| Action | Scope | Priority |
| --- | --- | --- |
| Rename the eval runs `Artifacts` column to `Outputs` | 1 string | P0 |
| Rename "Prompt artifacts" to "Prompt records" in code review evidence | ~3 strings | P0 |
| Fold `PreviewArtifact` into the session artifact model as an evidence-kind artifact | 16 non-test refs | P0 — see below |
| Keep `install_artifact` / `build_artifact` cache kinds | enum values, infra-only | No change; never surfaced |
| Rename `ReviewArtifact*` to `ReviewContextBundle*` | 82 non-test refs plus a package | Deferred; freeze from new user-facing strings |

The `ReviewArtifact` rename is real but expensive and entirely internal. It does
not block this feature. The rule that does block it: **no new user-facing string
may use "artifact" for anything other than a session artifact.**

### Preview evidence is an artifact kind, not a separate object

[115-agent-native-preview-verification](115-agent-native-preview-verification.md)
already produces durable, user-readable screenshot evidence and describes
"durable artifact references" as authoritative. Antigravity treats screenshots
and browser recordings as artifacts alongside walkthroughs and plans. Building a
second parallel durable-output store would be a mistake we would spend a year
undoing.

One model, discriminated by kind:

- `kind: published` — an agent ran `artifacts publish`. Appears in Overview, the
  transcript, and the collection. This document's subject.
- `kind: evidence` — a verification capture written by preview tooling. Durable
  and linkable, surfaced in the verification summary, **not** listed in Overview
  or the artifact collection.

Promoting an evidence capture is then a state change rather than a copy, which
is what "transient unless an agent intentionally publishes one as evidence"
should have meant all along. The implementation phase owns the schema; this
document owns the constraint that there is exactly one durable-output model.

## Product Boundaries and Terminology

### Artifact

An **artifact** is a durable output intentionally published by an agent during
a session. Examples include:

- an image or set of images;
- a self-contained HTML prototype or static-site bundle;
- a PDF or Markdown report;
- an SVG or diagram;
- a structured data file;
- a test or verification report intentionally preserved as evidence.

One logical artifact may have multiple immutable versions. Overview shows one
row for the logical artifact and opens the latest version by default. The
transcript records the specific version published by that turn.

### Not an artifact

- A **user attachment** is input supplied to the agent.
- A **workspace file** is ordinary mutable sandbox state and is not durable or
  visible merely because it exists.
- A **Preview** is a live application runtime.
- A **Change** is repository source or diff state.
- A **PR or branch** is code publication, not a session artifact in this UI.
- An unpromoted **verification capture** is an `evidence`-kind artifact: durable
  and linkable, but not listed as a session output.

## Information Architecture

### Overview placement

The Overview order is state-aware, but artifacts consistently sit between the
session outcome and ordinary metadata:

1. blocking session, runtime, or PR notice;
2. result or failure summary;
3. `Artifacts`, when at least one exists;
4. session vitals, origin, repository, branch, timing, and audit access;
5. execution context and other supporting detail.

Artifacts remain useful in failed and cancelled sessions. Partial outputs must
not disappear simply because the run did not complete.

The section does not render while the artifact count is zero. There is no empty
state, placeholder, disabled heading, or explanation of a capability that has
not been used.

### At-rest space contract

The section has a strict density budget:

| Artifact count | At-rest treatment | Approximate height |
| ---: | --- | ---: |
| 0 | Nothing rendered | `0px` |
| 1 | Heading plus one row | `72-80px` |
| 2 | Heading plus two rows | `116-128px` |
| 3+ | Heading, two rows, and `View all N` | maximum `148px` |

Exact implementation dimensions may adjust during visual QA, but the maximum
at-rest height is a product constraint. The section must never grow one row per
artifact by default.

This contract is only as strong as artifact identity. If republishing creates
duplicates instead of versions, a long run fills the collection with near-copies
and the two visible rows become the two least informative things in the session.
See [Artifact identity](#artifact-identity-and-idempotency).

### Overview wireframe

```text
Overview

Result
Implemented and verified the checkout flow.

Artifacts                                           6
──────────────────────────────────────────────────────
[preview] Checkout prototype
          HTML · v3 · Updated 2m ago                  ›
──────────────────────────────────────────────────────
[ PDF  ] Accessibility report
          PDF · 218 KB · Created 8m ago               ›
──────────────────────────────────────────────────────
View all 6

Running · Codex · Jane
assembledhq/storefront · feature/checkout
Completed 3m ago · Activity
```

There is no outer artifact card and no card around each row. The section is a
heading, a continuous list rhythm, and hairline dividers where needed. The
Overview panel is already a durable boundary; nested containers would announce
implementation structure instead of information hierarchy.

### Row anatomy

Every artifact uses the same compact `ArtifactRow` composition:

```text
[32px preview/icon]  Title, one line                         [open cue]
                     Type · version/size · relative time
```

Requirements:

- The entire row is the primary `Open artifact` target.
- Desktop row height targets `44-48px`; mobile preserves at least a `44px`
  touch target.
- Title truncates to one line. The accessible name exposes the full title.
- Metadata remains one line and uses the existing metadata type role.
- The row has no default shadow, pill, badge collection, or visible button.
- Hover and focus use a quiet accent surface. Selection uses the visual
  system's one selected-state signal: a soft accent plus an optional leading
  indicator, not an additional border and shadow.
- Secondary actions live in the artifact viewer. Do not add a persistent
  overflow control to every narrow Overview row in v1.

The row should reuse or specialize `ResourceRow` rather than introduce a
parallel card language. A specialization may reduce padding and own preview
geometry, keyboard semantics, and selection behavior.

### Preview and icon treatment

The leading visual exists for recognition, not for content consumption.

| Artifact type | Leading treatment |
| --- | --- |
| Raster image | Cropped thumbnail preserving the focal center |
| HTML/static site | Generated screenshot when available |
| PDF | First-page thumbnail when available; otherwise PDF icon |
| SVG | Safely rendered thumbnail |
| Video | Poster frame with a quiet play glyph |
| Markdown/text | Document icon |
| JSON/CSV | Data-file icon |
| Archive/unknown | Generic file icon |

All leading visuals occupy the same box and corner radius. Thumbnails must not
grow because an artifact is more colorful or newer. A preview-generation
failure falls back to the type icon without making the artifact unavailable.

### Ordering

Overview orders logical artifacts by **most recently created**, not most
recently updated. An artifact that republishes on a timer — a progress page, a
running checklist — would otherwise hold the top row for an entire session and
push every other output behind `View all`. Version updates change the row's
metadata in place; they do not reorder the list.

Ordering is recency, not featured status; all rows retain identical styling.

Only the first two rows appear at rest. `View all N` opens the complete
collection rather than expanding an unbounded list inside Overview.

Live publication must remain non-disruptive. A newly published or updated row
may enter with one quiet `160-200ms` transition, but it must not open the
artifact, switch tabs, move transcript scroll, or steal keyboard focus. If the
user is inspecting an older artifact version, a new version adds a subdued
`New version available` action in the viewer instead of replacing the content
under inspection.

### Complete collection

`View all N` opens the collection **in the center workspace**, as the list state
of the same surface that renders the viewer. It is not a modal dialog.

A dialog was the obvious choice and is the wrong one: it cannot be deep-linked,
it cannot be read alongside the transcript, and it becomes a dead end the moment
a user wants to compare an artifact against the conversation that produced it.
Making the collection a state of the artifact center mode means the feature adds
one center mode rather than a mode plus a modal, and it gives the later
cross-session library an existing surface to grow into.

The collection shows:

- the same compact row component as Overview;
- all `published`-kind artifacts in the session, ordered by creation;
- type filtering or search only when the collection is large enough to need it,
  initially at more than eight artifacts.

Closing the collection returns to the previous center mode and returns focus to
`View all N`. Selecting a row switches the same surface to the viewer.

On mobile, `View all N` opens a full-height sheet, because the mobile center
workspace is already the full screen.

### URL and identity space

Artifact URLs are reserved now so the deferred cross-session library does not
become a link migration later:

- `/artifacts/:artifactId` — canonical durable link. Resolves regardless of
  which session produced it. This is the link that goes into Slack.
- `/artifacts/:artifactId?v=:version` — a specific immutable version.
- `/sessions/:sessionId?artifact=:artifactId` — in-session viewing state, used
  when navigating within a session.

Artifact IDs are globally unique and org-scoped, never session-scoped. A future
`/artifacts` index requires new UI, not new identifiers or redirects.

## Delivery Moment

Uniform treatment is a rule about lists. It is not a rule about the end of a
run.

When a session finishes and its result summary references artifacts, the final
transcript turn promotes them:

- **Exactly one artifact:** render it inline at recognition size — a real
  thumbnail or first page, not a 44px row — directly beneath the result summary.
  For a session whose entire deliverable is one report or one prototype, that
  artifact *is* the answer, and a 44px row in a 384px rail buries it.
- **Two or more artifacts:** the existing grouped compact rows, unchanged.

This is deliberately the only promotion in the product. Overview stays uniform,
the collection stays uniform, and no artifact is ever marked "featured." The
promotion is a property of *the final turn*, derived from state, and requires no
`--featured` flag, no agent decision, and no user configuration.

Claude Code makes the stronger version of this bet — it opens your browser when
an artifact publishes. That is right for an interactive terminal session and
wrong for 143, where runs are unattended and a browser tab would open into an
empty room. Promotion at the final turn is the same instinct sized for a product
where the user arrives after the work is done.

## Progress Artifacts

A long unattended run can keep one artifact current as it works: a migration
checklist that ticks off, an investigation timeline that fills in, a status
board a PM can leave open. Claude Code names this pattern explicitly, and it is
worth more in 143 than in an attended terminal session, because "what is my
agent actually doing" is a question 143 users ask constantly and cannot
currently answer without reading a transcript.

The mechanics are already present — republishing creates a version — so this
requires one behavioral rule:

- A viewer displaying the **latest** version updates in place when a new version
  publishes. No reload, no interstitial, no scroll jump. Rendered content swaps
  with the same quiet transition used for preview replacement.
- A viewer displaying an **older** version does not move. It keeps showing the
  version under inspection and offers the subdued `New version available`
  action.

That distinction is the whole feature. Someone with the link open watches the
work happen; someone reading history is never yanked out of it.

Publishing frequency is the agent's judgment, but the tool documentation should
steer toward meaningful checkpoints rather than per-step churn, and the
implementation phase should define a republish rate limit.

## Sharing and Access

Every artifact has a durable link at `/artifacts/:artifactId`. Sharing is not a
feature to build later; it is why artifacts exist.

### Access model

- Artifacts are **org-scoped by default.** Any member of the organization that
  owns the session can open the link. This matches how 143 already treats
  sessions, and it needs no share dialog, no invite flow, and no per-artifact
  permission UI in v1.
- The link works after the sandbox is gone, after the session is archived, and
  after the preview runtime is reclaimed.
- No unauthenticated access. See [Non-Goals](#non-goals).

Claude Code defaults artifacts private-to-author and requires an explicit share
step; that fits a consumer-to-prosumer product with individual accounts. 143 is
already an org-scoped team product where sessions, PRs, and reviews are visible
to the team, so private-by-default would be inconsistent with everything around
it and would add a share step to the most common case.

### Slack

143 already has a Slack surface, and Slack is where a link to a prototype
actually goes. P0 includes:

- an unfurl for `/artifacts/:artifactId` links posted in Slack, showing the
  thumbnail, title, type, version, and originating session;
- unfurls resolve per-viewer and render nothing for users outside the owning
  org.

`Copy link` in the viewer copies the canonical durable URL, not the in-session
viewing URL.

## Artifact Viewer

Overview is the index; the center workspace is the viewer.

```text
+---------------- Artifact viewer ----------------+-- Overview -----------+
| <- Conversation                                 | Artifacts          6 |
|                                                 |                     |
| Checkout prototype             Version 3 v     | > Checkout prototype|
| ----------------------------------------------- |   Accessibility    |
|                                                 |   View all 6        |
|             rendered artifact                   |                     |
|                                                 | Session details     |
+-------------------------------------------------+---------------------+
```

Opening an artifact:

1. preserves the mounted transcript, scroll position, and composer draft;
2. switches the center workspace to artifact mode;
3. keeps Overview visible on desktop and marks the selected artifact row;
4. updates URL state so the artifact can be deep-linked;
5. defaults to the latest version unless the originating transcript event
   explicitly opens an older version.

The viewer header contains only:

- `Back to conversation`;
- artifact title;
- version selector when more than one version exists;
- one context menu for copy link, download, view source when supported, and
  other secondary actions;
- full-screen when the format benefits from it.

The viewer must not reproduce the artifact's metadata, action menu, and title
in multiple stacked headers. Format-specific controls appear only when useful.

The artifact center mode has two states — viewer and collection — sharing one
header pattern and one back affordance. It composes with the existing
`review` center mode the same way `review` composes with the transcript: entering
artifact mode from a review context returns to that context on back.

## Isolation and Network Policy

The rendering boundary is a product constraint, not an implementation detail. It
determines what agents can usefully build, so it is decided here.

Artifact content is written by an agent that has just read a private repository.
The threat is not a broken page; it is a one-pixel image beacon that carries
source code to an attacker-controlled host.

Therefore, in v1:

1. **HTML and SVG render on an isolated artifact origin**, never the 143
   application origin, under a restrictive Content Security Policy.
2. **Artifact content makes no outbound network requests.** Scripts, stylesheets,
   fonts, images, `fetch`, `XHR`, and WebSocket calls to any other host are
   blocked by CSP. Self-contained pages inline their CSS and JavaScript and embed
   images as data URIs.
3. Interactive HTML runs in a sandboxed frame and cannot capture application
   keyboard shortcuts outside explicit focus.
4. The isolated origin is a single documented hostname, so customers with
   egress restrictions can allowlist it alongside the 143 application.

Claude Code arrived at the same total network block after shipping artifacts at
scale, which is a strong signal from a team with an identical threat model. The
capability we are giving up — a page that fetches live data when viewed — is
real, and is the natural place for a later, explicitly-declared connector model.
It is not worth an exfiltration surface in v1.

The viewer must make blocked capabilities understandable when a page visibly
fails, without turning routine viewing into a security warning screen.

## Transcript Design

The transcript explains where an artifact came from and is where most users will
first encounter one. It is not a second artifact gallery.

### First publication

The artifact appears beneath the relevant agent message as a compact event row:

```text
I created a prototype of the revised checkout flow.

Artifact published
[preview] Checkout prototype
          HTML · v1                                      ›
```

The row shares the Overview anatomy and does not use a large bordered card.
The transcript can afford a slightly larger leading preview than the narrow
Overview, but it should remain a reference, not an embedded full renderer —
except at the [delivery moment](#delivery-moment).

### Updates and versions

An update produces a concise version event:

```text
Artifact updated
[preview] Checkout prototype
          HTML · v3 · Latest                              ›
```

Each transcript event remains bound to the version published at that moment.
If it is no longer latest, metadata reads `v1 · Latest v3` without warning
color. Selecting the event opens its historical version; the viewer offers the
latest version.

A progress artifact republishing many times must not produce many transcript
events. Consecutive updates to the same artifact within one turn collapse into a
single event showing the latest version.

### Multiple artifacts in one turn

When one turn publishes multiple artifacts, group them under one `Artifacts`
label with shared dividers. Do not create repeated headings or separate cards:

```text
Artifacts
[image] Desktop screenshot     PNG
-----------------------------------
[image] Mobile screenshot      PNG
-----------------------------------
[ PDF ] Accessibility report   PDF
```

The transcript may collapse after three rows with `Show 2 more`, because the
turn's prose remains the primary reading experience.

### Transcript publication behavior

- The platform creates the publication event automatically after the publish
  operation is durable.
- The agent may refer to the artifact naturally, but does not need to paste a
  raw URL or duplicate attachment markup.
- Artifact events participate in transcript pagination and retain their place
  when switching threads.
- A session-level Overview collection includes artifacts from all threads.
  Transcript events remain in the thread and turn that produced them.

## Agent Publishing Experience

The primary command is intentionally small:

```bash
143-tools artifacts publish --path ./artifacts/report.pdf --title "Accessibility report"
```

For a static bundle (P0b):

```bash
143-tools artifacts publish \
  --path ./artifacts/checkout-prototype \
  --entry index.html \
  --title "Checkout prototype"
```

Publishing a new version uses the same command with the same path:

```bash
143-tools artifacts publish \
  --path ./artifacts/checkout-prototype \
  --entry index.html
```

### Artifact identity and idempotency

This is the highest-risk detail in the feature. An explicit
`--artifact-id <uuid>` on every update requires the agent to thread an opaque
identifier across turns, compactions, and threads. Claude Code hit exactly this
and only partly solved it with a URL, documenting that a new session still
creates a new artifact rather than updating an existing one.

In 143, ID-threading failure does not degrade gracefully. An agent republishing
a progress page across a forty-minute run produces N logical artifacts instead
of N versions, and the two-row Overview cap stops protecting the rail and starts
hiding the session's real output behind `View all 23`.

Identity is therefore derived, not remembered:

1. **Default:** within one session, the same `--path` publishes a new version of
   the same logical artifact. Re-running the identical command is an update.
2. **Explicit key:** `--key <slug>` sets a stable identity independent of path,
   for agents that move or regenerate files. Same key in the same session means
   same artifact.
3. **Cross-session:** `--artifact-id <id>` remains available to update an
   artifact from a different session, matching the durable link the agent was
   given. This is the only case that requires an identifier.
4. `--new` forces a distinct logical artifact when the agent genuinely wants a
   second one at the same path.

Publishing an identical payload to an unchanged artifact is a no-op that returns
the existing version rather than creating an empty one.

### Product requirements for the command

- infer the current organization, session, thread, and turn from the sandbox
  capability; ordinary agents do not supply those identifiers;
- infer the format from the file or bundle and reject ambiguous unsupported
  content with an actionable error;
- default the title from the filename when omitted, while encouraging a short
  human-readable title;
- enforce a per-artifact size ceiling with a clear error naming the offending
  file, and a per-session artifact count ceiling;
- return the artifact ID, version, and durable `/artifacts/:id` link;
- complete only after publication is durable enough for Overview and the
  transcript event to converge, within a stated latency budget;
- never require a `--featured` flag.

### Agent-side cost guidance

Building a rich page is token-expensive, and the failure mode is a model that
inlines a megabyte of base64 into a report. The tool's help text and size errors
should carry the guidance directly, where the model will actually read it:

- prefer SVG, HTML, and CSS over embedded raster images for diagrams;
- omit interactivity that the output does not need;
- summarize large datasets rather than inlining them in full;
- publish at meaningful checkpoints, not after every step.

The detailed API, authorization, upload, and idempotency contracts belong in the
implementation design that follows this product/design phase.

## Capability, Policy, and Audit

Artifacts are durable customer content — potentially source code, reports about a
private codebase, and screenshots of authenticated application state — created
autonomously by an agent. 143 already has the machinery to govern that, and this
feature must use it rather than inventing a parallel path.

- **Capability.** Artifact publication is a named agent run capability, filtered
  through `mcp.NewCapabilityFilteredToolSource` like other `143-tools`
  namespaces. An agent without the capability does not see the tool.
- **Org policy.** An org setting enables or disables artifact publication for the
  organization, alongside the existing preview and automation settings in
  `OrgSettings`. Default on.
- **Audit.** Publication, version creation, and deletion each emit an audit event
  carrying org, session, actor agent, artifact ID, version, and size. Artifact
  access is covered by existing session authorization; publication is not.

143 does not prompt a human before an agent publishes, as Claude Code does. That
prompt exists because the terminal user is present; 143's user is not, and a
blocking prompt would stall unattended runs. Capability plus org policy plus
audit is the equivalent control for an autonomous product.

## Retention, Deletion, and Audit

Durable object storage holding customer code artifacts with no delete path and
no stated retention will not survive a first enterprise security review. Claude
Code ships a retention policy, audit events, and a Compliance API with list and
delete on day one. 143 already has audit logs and `AuditRetentionDays`.

v1 requires:

1. **A stated retention default**, configurable per org, after which artifact
   content is deleted. Retention runs on the artifact, not the session.
2. **An admin delete path** for a logical artifact and all its versions, emitting
   an audit event.
3. **Graceful tombstones.** Deleting an artifact must not turn historical
   transcript events into broken or, worse, silently unrelated links. A deleted
   artifact's transcript event and Overview row render a plain `Deleted` state,
   and its URL resolves to a tombstone rather than a 404 or a reused ID.

User-initiated deletion UX and per-version deletion remain later work. The
storage lifecycle, the admin surface, and the tombstone contract do not.

## States and Feedback

### Ready

Shows the normal thumbnail/icon, title, and metadata. Selecting it opens the
viewer.

### Preview processing

The artifact is durable and openable, but its small visual preview is being
prepared. Show the stable file-type icon, not an animated full-row skeleton.
Preview generation may replace the icon in place without moving the row.

### Preview unavailable

Use the type icon. Do not show an error badge merely because a thumbnail could
not be generated.

### Artifact unavailable

If the durable artifact itself cannot be read, retain a compact row with a
plain `Unavailable` status and a retry action in the viewer. Avoid destructive
red styling unless user action or data loss requires immediate attention.

### Deleted

A compact row with a plain `Deleted` status, no thumbnail, and no open action.
The transcript event renders the same way. See
[Retention, Deletion, and Audit](#retention-deletion-and-audit).

### Publishing failure

A failed `143-tools artifacts publish` call does not create a ghost artifact in
Overview. The agent receives a clear tool error and may retry. If durable
metadata was committed but post-processing failed, the artifact remains listed
with the appropriate readable fallback.

### Size or quota rejection

The publish call fails with an error naming the limit and the offending file,
and creates nothing. The agent is expected to reduce and retry, guided by the
cost guidance in the tool's help text.

## Responsive Behavior

### Desktop and compact desktop

- The current detail panel remains resizable and keeps its existing tab strip.
- The artifact section uses one column at all supported panel widths.
- Narrow widths reduce metadata before reducing the title or touch target.
- Opening an artifact uses the center workspace; the Overview list is not asked
  to become a rich renderer.

### Mobile

- Artifacts appear inside Overview in the existing session-details sheet.
- The same two-row at-rest cap applies.
- `View all N` opens a full-height collection sheet.
- Selecting an artifact dismisses details and opens a full-screen viewer.
- Back returns to the transcript. Reopening details restores its prior Overview
  scroll position where practical.
- No artifact affordance is added to the persistent mobile top bar.

The mobile transcript keeps the compact publication row at full conversation
width. A row remains at least `44px` tall and must not introduce horizontal
scrolling.

## Visual Design Contract

This feature follows the current 143 visual system:

- warm semantic surfaces and hairlines;
- Geist dense-UI and metadata roles;
- `SectionGroup` hierarchy before borders;
- `ResourceRow` alignment and interaction behavior;
- one selected-state signal;
- saturation reserved for meaningful state and primary actions;
- no gradients, glow, decorative sparkles, or special AI styling;
- no hover behavior on static elements;
- motion limited to quiet selection/view transitions and preview replacement.

Artifact previews should feel like small windows into the work, not promotional
tiles. Their job is recognition. The title carries identity; the preview merely
shortens recognition time.

## Accessibility

- The whole artifact row is one keyboard-focusable open action.
- `Enter` and `Space` open the selected artifact.
- The accessible name includes the full title, type, and version where useful.
- Decorative thumbnail images use empty alt text because the adjacent title
  supplies identity. Meaningful preview content is described in the viewer.
- Focus remains visible at every panel width and in light and dark themes.
- Selected, unavailable, processing, and deleted states are never communicated
  by color alone.
- Touch targets remain at least `44px` on mobile.
- `View all N` announces the complete count and returns focus when closed.
- Viewer focus moves to its heading on open and returns to the originating row
  on close/back when that row is still mounted.
- A progress artifact updating in place announces the new version politely and
  does not move focus.
- HTML content cannot escape its frame or capture application keyboard
  shortcuts outside explicit focus.

## Product Reference Lessons

The category converged fast, and on more than the noun.

| | Claude Code | Antigravity | Cursor | 143 (this doc) |
| --- | --- | --- | --- | --- |
| Name | Artifacts | Artifacts | Canvases | Artifacts |
| Formats | `.html` / `.md` only | Plans, walkthroughs, screenshots, recordings | Interactive pages | 8 types, P0a/P0b |
| Multi-file | No, by design | n/a | No | P0b only |
| Default audience | Private to author | Workspace | Thread | Org |
| Sharing | Org, public, viewer/editor | Agent Manager review | Live snapshot link | Org link + Slack unfurl |
| Cross-session index | Gallery, day one | Artifacts view | — | Deferred, URLs reserved |
| Live update | Yes, in place | Task list progress | — | Yes, latest-version viewers |
| Network from content | Blocked; connectors excepted | n/a | n/a | Blocked |
| Retention / delete | Policy, audit, Compliance API | — | — | P0 |

What we took:

- [Claude Artifacts](https://code.claude.com/docs/en/artifacts) — durable
  versioned object adjacent to the conversation; the live-updating page during a
  long task; total network block on page content; retention, audit, and delete
  as day-one obligations rather than follow-ups.
- [Google Antigravity](https://antigravity.google/docs/walkthrough) — artifacts
  as *deliverables easier to validate than raw tool calls*, deliberately spanning
  plans, walkthroughs, and screenshots. This is why 143 folds preview
  verification evidence into one artifact model rather than two.
- [Cursor canvases](https://sutopo.com/cursor-21-rewrites-the-agentic-coding-loop-2026-dev-tool/)
  — a shareable link to the output alone, without exposing the whole thread. The
  strongest argument that sharing is the feature, not a phase two.
- [Codex](https://openai.com/index/codex-for-almost-everything/) — summary-level
  discovery plus richer preview on selection.
- [Devin session tools](https://docs.devin.ai/work-with-devin/devin-session-tools)
  — separating live environment activity from durable outputs.

What we deliberately did not take:

- **A dedicated artifact tab or permanent adjacent canvas.** 143's session detail
  has three tabs — Overview, Changes, Preview. The problem is not tab budget; it
  is that a tab is a permanent destination for something most sessions will never
  produce. Overview-plus-transcript pays no navigation cost in the common case.
- **Auto-opening a browser on publish.** Correct for an attended terminal,
  pointless for an unattended run. Replaced by
  [delivery-moment promotion](#delivery-moment).
- **Private-by-default with a share step.** Inconsistent with an org-scoped team
  product where sessions and PRs are already team-visible.
- **Public links in v1.** See [Non-Goals](#non-goals).

## Initial Product Scope

### P0a — single file

The scope that covers the large majority of real publishes: screenshots,
reports, diagrams, exports.

1. Agent publication command for one file, with derived identity and versioning.
2. Durable logical artifacts and immutable versions.
3. Image, PDF, SVG, and single-page HTML support, plus one text viewer covering
   Markdown, plain text, JSON, and CSV.
4. Compact Overview section with a maximum of two visible rows.
5. Collection as the list state of the artifact center mode.
6. Automatic transcript publication and update events, with same-turn collapse.
7. Center-workspace viewer with download, copy link, and version access.
8. Durable org-scoped `/artifacts/:id` links, deep links, and Slack unfurl.
9. Isolated origin and total network block for HTML and SVG.
10. Capability gating, org policy, audit events, retention default, admin delete,
    and tombstones.
11. Delivery-moment promotion for a single-artifact session.
12. Live update in place for latest-version viewers.

Five viewers cover eight formats. Markdown, text, JSON, and CSV differ in syntax
highlighting, not in interaction model, and four separate handlers would earn
nothing.

### P0b — static bundle

13. Multi-file static-site bundles via `--entry`, with packaging, relative-path
    serving on the isolated origin, and lifecycle cleanup.

Bundles are split out because they are the expensive half: packaging, path
resolution, origin routing, and cleanup are most of the storage work in this
feature, and Anthropic shipped artifacts at scale while deliberately refusing
multi-file pages. P0a should be usable in production on its own; P0b ships when
P0a's storage and isolation are proven.

### Later

- video and richer media;
- user-authored artifact uploads;
- annotations and comments;
- comparison between versions;
- an explicitly-declared connector model for pages that need live data;
- artifact promotion to projects or other sessions;
- public or externally scoped sharing, behind an org owner toggle;
- cross-session search or a `/artifacts` library.

## Success Criteria

The product should be considered successful when:

1. A user can identify and open a newly published artifact from either the
   transcript or Overview without inspecting the workspace filesystem.
2. A user can send a colleague a link to an artifact without sending them the
   session, and that link still resolves after the sandbox is reclaimed.
3. A session with no artifacts gains no new visible chrome.
4. A session with twenty artifacts consumes no more at-rest Overview height
   than a session with three artifacts.
5. All artifacts have equal row treatment in every list; the only promotion in
   the product is a single-artifact session's final turn.
6. An agent that republishes the same output twenty times produces one artifact
   with twenty versions, one Overview row, and one transcript event per turn.
7. The full artifact collection remains reachable in one action from Overview.
8. Opening and closing an artifact does not lose transcript position or a
   composer draft.
9. Historical transcript events resolve to the published version and clearly
   expose a newer version when one exists.
10. The mobile session retains one persistent top bar and adds no new permanent
    navigation control.
11. Artifact rows are usable at the detail panel's minimum width, at `200%`
    zoom, with keyboard navigation, and in both themes.
12. Untrusted HTML and SVG never execute on the 143 application origin and make
    no outbound network requests.
13. `Artifact` labels exactly one concept in every user-facing surface.

### Measures

Criteria above are pass/fail. These are the numbers that say whether the feature
is worth having:

| Measure | Target |
| --- | --- |
| Publish call to visible in Overview and transcript | p95 under 3s |
| Sessions with ≥1 artifact where an artifact is opened | above 60% |
| Median time from publish to first open, completed sessions | under 24h |
| Artifact links opened by someone other than the session owner | tracked; the sharing bet is wrong if this stays near zero |
| Duplicate logical artifacts per session (identity failure) | under 1.05 mean |

## Design Validation Before Implementation

Create high-fidelity prototypes for:

1. Overview with zero, one, two, six, and twenty artifacts.
2. A transcript turn publishing one artifact, updating one artifact, and
   publishing five artifacts.
3. A completed single-artifact session's final turn, showing delivery-moment
   promotion against the same session with three artifacts.
4. Artifact viewing at `1440x900`, `1024x768`, and `390x844`.
5. The collection as a center-mode state at each of those widths.
6. Long titles, missing thumbnails, multiple versions, unavailable artifacts,
   deleted artifacts, and dark theme.
7. Keyboard focus from Overview to viewer, to collection, and back.

The decisive visual review is the six-artifact Overview at the default `384px`
panel width. If artifacts read as a stack of cards, crowd out status metadata,
or make the panel feel like a file browser before the user asks for one, the
design has failed its primary constraint.

The second decisive review is the single-artifact completed session. If the
session's only deliverable is not immediately recognizable in the final turn,
the design has failed its product bet instead.

## Technical Contracts

Per `docs/design/AGENTS.md`, stated explicitly rather than left implicit:

- **Database schema:** deferred. This is a product/design phase. The tables,
  columns, indexes, and tenancy scope for logical artifacts, immutable versions,
  and the `published`/`evidence` kind discriminator are defined in the
  implementation design that follows. This document constrains that schema in
  three ways: one durable-output model rather than two, globally unique
  org-scoped artifact IDs, and identity derivable from `(session, path)` or
  `(session, key)` without an agent-supplied UUID.
- **API contract:** deferred to the same implementation design, constrained by
  the reserved URL space in
  [URL and identity space](#url-and-identity-space), org-scoped authorization,
  and the requirement that publish returns an artifact ID, version, and durable
  link only after the record is durable.

## Implementation Design Follow-Up

A separate implementation phase should define:

- tenancy-safe data models, the `published`/`evidence` kind discriminator, and
  immutable version relationships;
- object storage, retention enforcement, size limits, MIME validation, tombstone
  representation, and cleanup;
- path- and key-derived identity resolution and republish rate limiting;
- session-token capability, org policy plumbing, audit event shapes, and
  `143-tools` registration;
- bundle packaging and isolated artifact-origin serving with CSP enforcement;
- durable link resolution, Slack unfurl handling, and cross-session access
  authorization;
- transcript event representation, same-turn collapse, and live-update delivery;
- API pagination and cache behavior;
- viewer components and safe format handlers;
- the naming cleanups in [Naming](#naming);
- focused backend/frontend tests, migration sequencing, observability for the
  measures above, and rollout controls.

Those decisions must preserve this product contract, especially the zero-state
absence, two-row Overview cap, derived artifact identity, equal treatment in
lists, transcript provenance, durable org-scoped links, and the isolated
no-network rendering boundary.
