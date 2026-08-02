# Design: Session Artifacts Product and UX

> **Status:** Not Started | **Last reviewed:** 2026-08-02
>
> **Depends on:** [overall](../overall.md), [frontend](../03-frontend.md),
> [visual system](../implemented/117-visual-system-and-product-polish.md),
> [transcript refactor](../implemented/101-session-transcript-refactor.md),
> [mobile top bar](../implemented/70-mobile-session-top-bar-consolidation.md),
> [agent run capabilities](../implemented/102-agent-run-capabilities.md),
> [audit logs](../implemented/34-audit-logs.md),
> [slackbot](92-slackbot-product-surface.md),
> [preview verification](115-agent-native-preview-verification.md)

## Product Bet

**An artifact is how a session's output leaves the session.**

143 sessions run unattended, and the people who care about the result are often
not in 143 at all. Today only a PR and a Slack notification escape a session;
everything else the agent produced dies with the sandbox.

Every decision below resolves against that sentence: it is why sharing is P0,
why a lone terminal artifact is promoted at delivery, why a long run can keep
one page updating, and why identity must be stable enough that republishing
versions instead of duplicating. The bet also sets the limit — an artifact is a
*capture of work*, not an application and not a workspace browser.

## Decision

Artifacts are durable outputs of a session, not a new navigation destination.
They have three surfaces:

1. **Transcript — primary discovery.** A compact publication row at the turn
   that created or updated the artifact. This is where users actually find
   artifacts: at the moment of production.
2. **Overview — durable index.** A compact `Artifacts` section near the top of
   Overview, rendered only when artifacts exist, so an output stays findable
   after its turn scrolls away.
3. **Durable org link — export.** A stable URL any org member can open, which
   unfurls in Slack and outlives the sandbox.

Selecting an artifact opens it in the session's center workspace. No new detail
tab, no permanent shelf, no featured artifact in any list. Overview renders a
quiet borderless resource list with **at most two artifacts at rest**; larger
collections use one `View all` disclosure. That hard space budget stops the
detail rail from growing in proportion to agent output.

```text
agent runs `143-tools artifacts publish`
   +--> compact event in the originating transcript turn
   +--> compact row in Overview > Artifacts
   +--> durable org link (Slack unfurl, deep link)
            +--> selected artifact opens in center workspace
```

Character: content before containers; one obvious action per object; detail only
after intent; repeated objects form a list, not a stack of cards; previews
identify without competing with the work; the interface is absent when it has
nothing to say.

Restraint governs *lists*, not the *moment of delivery* — see
[Delivery Moment](#delivery-moment).

## Goals

1. Publish through one `143-tools` command, with identity stable enough that
   republishing versions rather than duplicates.
2. Discoverable from the transcript at production time and Overview afterwards,
   without adding a tab.
3. Preserve the turn and version that produced each artifact.
4. Render images, HTML, PDFs, and text in an appropriate viewer.
5. Give every artifact a durable org-scoped link that survives the sandbox.
6. Zero artifact chrome for sessions with no artifacts.
7. Reuse 143's resource-row, semantic-token, selected-state, and mobile details
   patterns.
8. Durability independent of the live sandbox and preview runtime.
9. Govern publication with the existing capability and org policy model, with
   audit coverage and a stated retention default.

## Non-Goals

Collaborative editing; comments or annotations; a spatial canvas; a global
cross-session library (deferred — URL space reserved below); platform-generated
artifacts without an explicit agent publication; a replacement for source
control, the Changes tab, or the live Preview tab; the final schema, storage
layout, or API implementation.

**Public, unauthenticated sharing is an explicit decision, not a deferral.** 143
artifacts are built by an agent with read access to a private repository; a link
that works without sign-in is a data-exfiltration surface we do not want on by
default. Org-scoped sharing is P0; public sharing is later work that must ship
behind an org owner toggle if at all.

Deletion and retention are **not** non-goals — see [Governance](#governance).

## Naming

### Decision: keep `Artifacts`, enforce one meaning

`Artifact` means exactly one thing in every user-facing surface: **a durable
output intentionally published by an agent during a session.** CLI namespace is
`artifacts`.

Two of three major agent platforms use exactly this word, and Antigravity's
definition — *tangible deliverables in formats easier to validate than raw tool
calls* — is almost precisely 143's intent.

| Product | Name | Scope of the noun |
| --- | --- | --- |
| Claude Code / claude.ai | **Artifacts** | Published live page, versioned, shareable |
| Google Antigravity | **Artifacts** | Task lists, plans, walkthroughs, screenshots, recordings |
| Cursor | **Canvases** | Interactive visual output in the chat panel |
| Codex | "artifact previews" | Secondary capability, not a headline noun |
| Manus | "deliverables" (prose) | Doc, report, deck, small site |

Rejected: **Canvases** (scoped to interactive output; reads wrong on a PDF or
CSV, most of P0), **Outputs** (too close to log and tool output in the same
transcript), **Deliverables** (implies a stakeholder commitment a mid-run
screenshot lacks), **Results** (Overview already leads with `Result`), **Files**
(implies a browsable workspace — the wrong mental model), **Evidence** (right
for captures, wrong for a prototype).

### Clearing the word

`Artifact` currently means five things in 143, two of them on screen. Shipping
without resolving this gives you one word labelling a prototype, a prompt log,
and an eval count on three different screens.

| Existing use | Where | Visible | Action | Priority |
| --- | --- | --- | --- | --- |
| Eval run output count | `settings/evals/[id]/page.tsx:272` | **Yes** | Rename to `Outputs` | P0 |
| Prompt/response records | `code-reviews/page.tsx:3314` | **Yes** | Rename to "Prompt records" | P0 |
| Preview screenshot blob | `PreviewArtifact`, `internal/models/preview.go:841` | Indirect | Fold into the artifact model as an evidence kind | P0 |
| Code-review context bundle | `ReviewArtifactKey`, `models.go:700`; `internal/services/reviewartifact` | No | Rename to `ReviewContextBundle*` (82 non-test refs) | Deferred |
| Dependency/build caches | `install_artifact`, `build_artifact`, `preview.go:663` | No | Keep; CI sense is idiomatic and never surfaced | None |

The `ReviewArtifact` rename is expensive and entirely internal; it does not
block this feature. The rule that does: **no new user-facing string uses
"artifact" for anything other than a session artifact.**

### Preview evidence is an artifact kind, not a separate object

[115](115-agent-native-preview-verification.md) already produces durable
screenshot evidence, and Antigravity treats screenshots and recordings as
artifacts alongside plans. A second parallel durable-output store is a mistake
we would spend a year undoing. One model, discriminated by kind:

- `kind: published` — an agent ran `artifacts publish`. Appears in the
  transcript, Overview, and the collection. This document's subject.
- `kind: evidence` — a verification capture from preview tooling. Durable and
  linkable, surfaced in the verification summary, **not** listed as a session
  output.

Promotion is then a state change rather than a copy, which is what "transient
unless intentionally published" should have meant.

## Terminology

**Artifact:** a durable output intentionally published by an agent — an image, a
self-contained HTML prototype or static bundle, a PDF or Markdown report, an SVG
or diagram, a structured data file, or a verification report preserved as
evidence. One logical artifact may have many immutable versions; Overview shows
one row and opens the latest.

**Not an artifact:** a *user attachment* (input to the agent); a *workspace
file* (mutable sandbox state, not durable merely because it exists); a *Preview*
(live runtime); a *Change* (repo source or diff); a *PR or branch* (code
publication); an unpromoted *verification capture* (evidence kind — durable and
linkable, but not a listed session output).

## Information Architecture

### Overview placement and space contract

Order is state-aware, but artifacts sit between outcome and ordinary metadata:
blocking notice → result or failure summary → **`Artifacts`, when ≥1 exists** →
session vitals, origin, repo, branch, timing, audit → execution context.

Artifacts remain useful in failed and cancelled sessions; partial outputs must
not disappear because the run did not complete. The section does not render at
zero — no empty state, placeholder, disabled heading, or explanation of an
unused capability.

| Artifact count | At-rest treatment | Approximate height |
| ---: | --- | ---: |
| 0 | Nothing rendered | `0px` |
| 1 | Heading plus one row | `72-80px` |
| 2 | Heading plus two rows | `116-128px` |
| 3+ | Heading, two rows, `View all N` | maximum `148px` |

Dimensions may adjust in visual QA; the maximum at-rest height is a product
constraint. The section must never grow one row per artifact. The contract is
only as strong as [artifact identity](#artifact-identity) — if republishing
creates duplicates, the two visible rows become the two least informative things
in the session.

```text
Artifacts                                           6
──────────────────────────────────────────────────────
[preview] Checkout prototype
          HTML · v3 · Updated 2m ago                  ›
──────────────────────────────────────────────────────
[ PDF  ] Accessibility report
          PDF · 218 KB · Created 8m ago               ›
──────────────────────────────────────────────────────
View all 6
```

No outer card and no card per row — a heading, a continuous list rhythm, and
hairline dividers. The Overview panel is already a durable boundary; nested
containers would announce implementation structure instead of hierarchy.

### Row anatomy

```text
[32px preview/icon]  Title, one line                         [open cue]
                     Type · version/size · relative time
```

The entire row is the primary `Open artifact` target, `44-48px` on desktop and
at least `44px` on mobile. Title truncates to one line with the full title in
the accessible name; metadata stays one line in the existing metadata type role.
No default shadow, pill, badge collection, or visible button. Hover and focus
use a quiet accent surface; selection uses the visual system's one
selected-state signal — soft accent plus optional leading indicator, not an
added border and shadow. Secondary actions live in the viewer; no persistent
overflow control on every narrow Overview row in v1. Reuse or specialize
`ResourceRow` rather than introducing a parallel card language.

The leading visual exists for recognition, not content consumption: raster
images use a cropped thumbnail preserving the focal center; HTML and static
sites a generated screenshot; PDFs a first-page thumbnail; SVGs a safely
rendered thumbnail; video a poster frame with a quiet play glyph. Everything
else uses a type icon — document for Markdown and text, data-file for JSON and
CSV, generic for archives and unknowns. All share one box and corner radius and
must not grow because an artifact is more colorful or newer. Preview-generation
failure falls back to the type icon without making the artifact unavailable.

### Ordering

Order by **most recently created**, not most recently updated. An artifact
republishing on a timer would otherwise hold the top row for an entire session
and push every other output behind `View all`. Version updates change row
metadata in place; they do not reorder. Ordering is recency, not featured
status — all rows keep identical styling.

Live publication must be non-disruptive: a new or updated row may enter with one
quiet `160-200ms` transition, but must not open the artifact, switch tabs, move
transcript scroll, or steal focus.

### Complete collection

`View all N` opens the collection **in the center workspace**, as the list state
of the surface that renders the viewer — not a modal. A dialog cannot be
deep-linked, cannot be read alongside the transcript, and dead-ends when a user
wants to compare an artifact against the conversation that produced it. As a
center-mode state it adds one mode rather than a mode plus a modal, and gives
the later cross-session library a surface to grow into.

Contents: the same compact row component, all `published`-kind artifacts in the
session ordered by creation, and type filtering or search only above eight
artifacts. Closing returns to the previous center mode and returns focus to
`View all N`; selecting a row switches the same surface to the viewer. On mobile
it is a full-height sheet, since the mobile center workspace is already full
screen.

### URL and identity space

Reserved now so the deferred library is not a link migration later:

- `/artifacts/:artifactId` — canonical durable link, resolving regardless of
  originating session. This is the link that goes into Slack.
- `/artifacts/:artifactId?v=:version` — a specific immutable version.
- `/sessions/:sessionId?artifact=:artifactId` — in-session viewing state.

Artifact IDs are globally unique and org-scoped, never session-scoped. A future
`/artifacts` index needs new UI, not new identifiers or redirects.

## Delivery Moment

Uniform treatment is a rule about lists, not about the end of a run. When a
session finishes and its result references artifacts, the final transcript turn
promotes them:

- **Exactly one artifact:** render it inline at recognition size — a real
  thumbnail or first page, not a 44px row — beneath the result summary. When a
  session's entire deliverable is one report, that artifact *is* the answer, and
  a 44px row in a 384px rail buries it.
- **Two or more:** the existing grouped compact rows, unchanged.

This is deliberately the only promotion in the product. Overview and the
collection stay uniform, nothing is ever marked "featured," and the promotion is
derived from state — no `--featured` flag, no agent decision, no configuration.
Claude Code makes the stronger version of this bet by opening your browser on
publish; that is right for an attended terminal and wrong for 143, where the tab
would open into an empty room.

## Progress Artifacts

A long unattended run can keep one artifact current as it works: a migration
checklist that ticks off, an investigation timeline that fills in, a status board
a PM leaves open. This is worth more in 143 than in an attended terminal, because
"what is my agent actually doing" is a question users cannot currently answer
without reading a transcript. Republishing already creates a version, so this
needs one behavioral rule:

- A viewer on the **latest** version updates in place — no reload, no
  interstitial, no scroll jump, using the same quiet transition as preview
  replacement.
- A viewer on an **older** version does not move. It keeps the version under
  inspection and offers a subdued `New version available` action.

Someone with the link open watches the work happen; someone reading history is
never yanked out of it. Publishing frequency is the agent's judgment, steered
toward meaningful checkpoints by the tool docs, with a republish rate limit
defined in implementation.

## Sharing and Access

Every artifact has a durable link at `/artifacts/:artifactId`. Sharing is not a
later feature; it is why artifacts exist.

- **Org-scoped by default — decided.** Any member of the owning organization can
  open the link. This matches how 143 already treats sessions and needs no share
  dialog, invite flow, or per-artifact permission UI in v1.
- The link works after the sandbox is gone, the session is archived, and the
  preview runtime is reclaimed.
- No unauthenticated access. See [Non-Goals](#non-goals).

Claude Code defaults to private-to-author with an explicit share step, which fits
a product with individual accounts. 143 is already an org-scoped team product
where sessions, PRs, and reviews are team-visible, so private-by-default would be
inconsistent with everything around it and would add a share step to the most
common case.

**Keeping the default narrowable.** Org-scoping is also the kind of default that
is painful to tighten once links circulate. Two requirements make a future
narrowing a configuration change rather than a breaking one:

1. **Visibility is an explicit stored field**, set to `org` at creation, not
   inferred from session ownership at read time. Adding `private` or
   `session-participants` later is then a new value on an existing column, not a
   migration that reinterprets every link already in a Slack thread.
2. **One authorization point.** Every read path — viewer, collection, thumbnail,
   raw content on the isolated origin, Slack unfurl — resolves through a single
   check reading that field. The isolated origin is easy to get wrong, because it
   serves bytes outside the normal application request path.

**Slack.** P0 includes an unfurl for `/artifacts/:artifactId` showing thumbnail,
title, type, version, and originating session. Unfurls resolve per-viewer and
render nothing outside the owning org. `Copy link` copies the canonical durable
URL, not the in-session viewing URL.

## Artifact Viewer

Opening an artifact preserves the mounted transcript, scroll position, and
composer draft; switches the center workspace to artifact mode; keeps Overview
visible on desktop with the row marked selected; updates URL state for deep
linking; and defaults to the latest version unless a transcript event explicitly
opens an older one.

The viewer header contains only: `Back to conversation`, the title, a version
selector when more than one version exists, one context menu (copy link,
download, view source where supported), and full-screen where the format
benefits. It must not restate title, metadata, and actions in stacked headers.
Format-specific controls appear only when useful.

The artifact center mode has two states — viewer and collection — sharing one
header pattern and one back affordance. It composes with the existing `review`
center mode the same way `review` composes with the transcript: entering artifact
mode from a review context returns to that context on back.

## Isolation and Network Policy

The rendering boundary is a product constraint, not an implementation detail: it
bounds what agents can usefully build. Artifact content is written by an agent
that just read a private repository. The threat is not a broken page; it is a
one-pixel image beacon carrying source code to an attacker-controlled host.

In v1:

1. **HTML and SVG render on an isolated artifact origin**, never the 143
   application origin, under a restrictive CSP.
2. **Artifact content makes no outbound network requests.** Scripts,
   stylesheets, fonts, images, `fetch`, `XHR`, and WebSocket calls to other hosts
   are blocked. Pages inline CSS and JS and embed images as data URIs.
3. Interactive HTML runs sandboxed and cannot capture application keyboard
   shortcuts outside explicit focus.
4. The isolated origin is a single documented hostname, so customers with egress
   restrictions can allowlist it alongside the 143 application.

Claude Code reached the same total block after shipping at scale. The capability
given up — a page fetching live data when viewed — is the natural place for a
later explicitly-declared connector model, and is not worth an exfiltration
surface in v1. The viewer should make blocked capabilities understandable when a
page visibly fails, without turning routine viewing into a warning screen.

## Transcript Design

The transcript explains where an artifact came from and is where most users
first encounter one. It is not a second gallery.

```text
I created a prototype of the revised checkout flow.

Artifact published
[preview] Checkout prototype
          HTML · v1                                      ›
```

The row shares the Overview anatomy — no large bordered card. The transcript can
afford a slightly larger leading preview than the narrow Overview but remains a
reference, not an embedded renderer, except at the
[delivery moment](#delivery-moment).

**Updates and versions.** An update produces `Artifact updated` with
`HTML · v3 · Latest`. Each event stays bound to the version published at that
moment; when no longer latest it reads `v1 · Latest v3` without warning color.
Selecting it opens that historical version; the viewer offers the latest.
Consecutive updates to the same artifact within one turn collapse into a single
event showing the latest version, so a progress artifact cannot flood the
transcript.

**Multiple artifacts in one turn** group under one `Artifacts` label with shared
dividers — no repeated headings, no separate cards — and may collapse after
three rows with `Show 2 more`, because the turn's prose remains the primary
reading experience.

**Publication behavior.** The platform creates the event automatically once the
publish is durable. The agent may refer to the artifact naturally but need not
paste a URL or duplicate attachment markup. Events participate in transcript
pagination and keep their place when switching threads. Overview collects
artifacts from all threads; transcript events stay in the thread and turn that
produced them.

## Agent Publishing Experience

```bash
143-tools artifacts publish --path ./artifacts/report.pdf --title "Accessibility report"

# static bundle (P0b); republishing is the same command with the same path
143-tools artifacts publish --path ./artifacts/checkout-prototype \
  --entry index.html --title "Checkout prototype"
```

### Artifact identity

The highest-risk detail in the feature. Requiring `--artifact-id <uuid>` on every
update forces the agent to thread an opaque identifier across turns, compactions,
and threads — Claude Code hit this and only partly solved it with a URL. In 143
the failure does not degrade gracefully: an agent republishing a progress page
across a forty-minute run produces N logical artifacts instead of N versions, and
the two-row cap stops protecting the rail and starts hiding the session's real
output behind `View all 23`.

Identity is therefore derived, not remembered:

1. **Default:** within one session, the same `--path` publishes a new version of
   the same logical artifact. Re-running the identical command is an update.
2. `--key <slug>` sets a stable identity independent of path, for agents that
   move or regenerate files.
3. `--artifact-id <id>` updates an artifact from a *different* session, matching
   the durable link the agent was given. The only case needing an identifier.
4. `--new` forces a distinct logical artifact at the same path.

Publishing an identical payload to an unchanged artifact is a no-op returning the
existing version rather than creating an empty one.

### Command requirements

- Infer org, session, thread, and turn from the sandbox capability; ordinary
  agents supply none of those.
- Infer format from the file or bundle; reject ambiguous unsupported content with
  an actionable error.
- Default the title from the filename, while encouraging a short readable title.
- Enforce a per-artifact size ceiling and per-session count ceiling, with errors
  naming the limit and the offending file, creating nothing.
- Return artifact ID, version, and durable `/artifacts/:id` link.
- Complete only once publication is durable enough for Overview and the
  transcript to converge, within a stated latency budget.
- Never require a `--featured` flag.

**Agent-side cost guidance** belongs in help text and size errors, where the
model will read it: prefer SVG/HTML/CSS over embedded raster for diagrams; omit
unneeded interactivity; summarize large datasets rather than inlining them;
publish at meaningful checkpoints, not after every step.

## Governance

Artifacts are durable customer content — source code, reports about a private
codebase, screenshots of authenticated application state — created autonomously.
143 already has the machinery to govern that and must use it rather than
inventing a parallel path.

- **Capability.** Publication is a named agent run capability filtered through
  `mcp.NewCapabilityFilteredToolSource` like other `143-tools` namespaces. An
  agent without it does not see the tool.
- **Org policy.** An `OrgSettings` toggle enables or disables publication for the
  organization. Default on.
- **Audit.** Publication, version creation, and deletion each emit an audit event
  with org, session, actor agent, artifact ID, version, and size.

143 does not prompt a human before publication as Claude Code does; that prompt
exists because the terminal user is present. 143's user is not, and a blocking
prompt would stall unattended runs. Capability plus policy plus audit is the
equivalent control for an autonomous product.

**Retention and deletion.** Durable storage of customer code with no delete path
and no stated retention will not survive a first enterprise security review.
Claude Code ships retention, audit events, and list/delete APIs on day one; 143
already has audit logs and `AuditRetentionDays`. v1 requires a **stated retention
default**, configurable per org and applied to the artifact rather than the
session; an **admin delete path** for a logical artifact and all versions,
emitting an audit event; and **graceful tombstones**, so deletion never turns
historical transcript events into broken or, worse, silently unrelated links —
deleted artifacts render a plain `Deleted` state and their URL resolves to a
tombstone, not a 404 or a reused ID. User-initiated and per-version deletion
remain later work; the storage lifecycle, admin surface, and tombstone contract
do not.

## States and Feedback

| State | Treatment |
| --- | --- |
| Ready | Thumbnail/icon, title, metadata; selecting opens the viewer |
| Preview processing | Stable file-type icon, never an animated full-row skeleton; the preview replaces the icon in place without moving the row |
| Preview unavailable | Type icon. No error badge merely because a thumbnail failed |
| Artifact unavailable | Compact row, plain `Unavailable`, retry in the viewer. No destructive red unless user action or data loss demands attention |
| Deleted | Compact row, plain `Deleted`, no thumbnail, no open action; transcript event matches |
| Publishing failure | No ghost row in Overview. The agent gets a clear tool error and may retry. If metadata committed but post-processing failed, the artifact lists with its readable fallback |
| Size or quota rejection | Publish fails naming the limit and file, creating nothing |

## Responsive Behavior

**Desktop.** The detail panel stays resizable with its existing tab strip. The
artifact section is one column at all panel widths. Narrow widths reduce metadata
before the title or touch target. Opening uses the center workspace; the Overview
list is never asked to become a rich renderer.

**Mobile.** Artifacts appear in Overview inside the existing session-details
sheet, with the same two-row cap. `View all N` opens a full-height sheet.
Selecting dismisses details and opens a full-screen viewer; back returns to the
transcript, and reopening details restores prior scroll where practical. No
artifact affordance is added to the persistent mobile top bar. The mobile
transcript keeps the publication row at full conversation width, at least `44px`
tall, with no horizontal scrolling.

## Visual Design Contract

Warm semantic surfaces and hairlines; Geist dense-UI and metadata roles;
`SectionGroup` hierarchy before borders; `ResourceRow` alignment and interaction;
one selected-state signal; saturation reserved for meaningful state and primary
actions; no gradients, glow, sparkles, or special AI styling; no hover on static
elements; motion limited to quiet selection/view transitions and preview
replacement. Previews should feel like small windows into the work, not
promotional tiles — the title carries identity, the preview only shortens
recognition time.

## Accessibility

- The whole row is one keyboard-focusable open action; `Enter` and `Space` open.
- The accessible name includes full title, type, and version where useful.
- Decorative thumbnails use empty alt text because the adjacent title supplies
  identity; meaningful preview content is described in the viewer.
- Focus stays visible at every panel width, in light and dark themes.
- Selected, unavailable, processing, and deleted states are never communicated by
  color alone. Touch targets stay ≥`44px` on mobile.
- `View all N` announces the complete count and returns focus when closed.
- Viewer focus moves to its heading on open and returns to the originating row on
  close when that row is still mounted.
- A progress artifact updating in place announces politely without moving focus.
- HTML cannot escape its frame or capture application shortcuts outside focus.

## Product Reference Lessons

| | Claude Code | Antigravity | Cursor | 143 (this doc) |
| --- | --- | --- | --- | --- |
| Name | Artifacts | Artifacts | Canvases | Artifacts |
| Formats | `.html` / `.md` only | Plans, walkthroughs, screenshots | Interactive pages | 8 types, P0a/P0b |
| Multi-file | No, by design | n/a | No | P0b only |
| Default audience | Private to author | Workspace | Thread | Org |
| Sharing | Org, public, viewer/editor | Agent Manager review | Live snapshot link | Org link + Slack unfurl |
| Cross-session index | Gallery, day one | Artifacts view | — | Deferred, URLs reserved |
| Live update | Yes, in place | Task list progress | — | Yes, latest-version viewers |
| Network from content | Blocked; connectors excepted | n/a | n/a | Blocked |
| Retention / delete | Policy, audit, API | — | — | P0 |

**Taken:** the live-updating page and day-one retention/audit/delete from
[Claude Code](https://code.claude.com/docs/en/artifacts); artifacts as
deliverables spanning plans, walkthroughs, and screenshots from
[Antigravity](https://antigravity.google/docs/walkthrough), which is why 143
folds verification evidence into one model; a shareable link to the output alone
without exposing the thread from
[Cursor](https://sutopo.com/cursor-21-rewrites-the-agentic-coding-loop-2026-dev-tool/),
the strongest argument that sharing is the feature and not a phase two;
summary-level discovery with richer preview on selection from
[Codex](https://openai.com/index/codex-for-almost-everything/); and separating
live environment activity from durable outputs from
[Devin](https://docs.devin.ai/work-with-devin/devin-session-tools).

**Declined:** a dedicated artifact tab (143 has three — Overview, Changes,
Preview; the problem is not tab budget but that a tab is a permanent destination
for something most sessions never produce); auto-opening a browser on publish;
private-by-default with a share step; public links in v1.

## Initial Product Scope

### P0a — single file

Covers the large majority of real publishes: screenshots, reports, diagrams,
exports.

1. Publication command for one file, with derived identity and versioning.
2. Durable logical artifacts and immutable versions.
3. Image, PDF, SVG, and single-page HTML, plus one text viewer covering
   Markdown, plain text, JSON, and CSV.
4. Compact Overview section, maximum two visible rows.
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
highlighting, not interaction model; four handlers would earn nothing.

### P0b — static bundle

13. Multi-file bundles via `--entry`, with packaging, relative-path serving on
    the isolated origin, and lifecycle cleanup.

Split out because bundles are the expensive half — packaging, path resolution,
origin routing, and cleanup are most of the storage work — and Anthropic shipped
artifacts at scale while deliberately refusing multi-file pages. P0a should be
production-usable alone; P0b ships once P0a's storage and isolation are proven.

### Later

Video and richer media; user-authored uploads; annotations and comments; version
comparison; an explicitly-declared connector model for live data; promotion to
projects or other sessions; public sharing behind an owner toggle; cross-session
search or a `/artifacts` library.

## Success Criteria

1. A user identifies and opens a newly published artifact from the transcript or
   Overview without inspecting the workspace filesystem.
2. A user sends a colleague a link without sending the session, and it resolves
   after the sandbox is reclaimed.
3. A session with no artifacts gains no new visible chrome.
4. A session with twenty artifacts consumes no more at-rest Overview height than
   one with three.
5. All artifacts have equal treatment in every list; the only promotion is a
   single-artifact session's final turn.
6. An agent republishing the same output twenty times produces one artifact,
   twenty versions, one Overview row, and one transcript event per turn.
7. The full collection is reachable in one action from Overview.
8. Opening and closing an artifact loses no transcript position or draft.
9. Historical transcript events resolve to their published version and expose a
   newer one when it exists.
10. Mobile retains one persistent top bar and adds no permanent navigation.
11. Rows are usable at minimum panel width, `200%` zoom, via keyboard, in both
    themes.
12. Untrusted HTML and SVG never execute on the application origin and make no
    outbound network requests.
13. Every read path enforces the same stored visibility check, including raw
    content on the isolated origin. No path serves bytes on an ID alone.
14. `Artifact` labels exactly one concept in every user-facing surface.

### Measures

Criteria above are pass/fail. These say whether the feature is worth having:

| Measure | Target |
| --- | --- |
| Publish call to visible in Overview and transcript | p95 under 3s |
| Sessions with ≥1 artifact where an artifact is opened | above 60% |
| Median publish to first open, completed sessions | under 24h |
| Links opened by someone other than the session owner | tracked; the sharing bet is wrong if this stays near zero |
| Duplicate logical artifacts per session | under 1.05 mean |

## Design Validation Before Implementation

Prototype: Overview at zero, one, two, six, and twenty artifacts; a turn
publishing one, updating one, and publishing five; a completed single-artifact
session's final turn against the same session with three; viewing at `1440x900`,
`1024x768`, and `390x844`; the collection as a center-mode state at each width;
long titles, missing thumbnails, multiple versions, unavailable and deleted
artifacts, and dark theme; keyboard focus from Overview to viewer, to collection,
and back.

**Decisive review one:** the six-artifact Overview at the default `384px` panel
width. If artifacts read as a stack of cards, crowd out status metadata, or make
the panel feel like a file browser before the user asks for one, the design has
failed its primary constraint.

**Decisive review two:** the single-artifact completed session. If the session's
only deliverable is not immediately recognizable in the final turn, the design
has failed its product bet instead.

## Technical Contracts

Per `docs/design/AGENTS.md`, stated explicitly:

- **Database schema: deferred** to the implementation design. This document
  constrains it four ways: one durable-output model rather than two; globally
  unique org-scoped artifact IDs; identity derivable from `(session, path)` or
  `(session, key)` without an agent-supplied UUID; and an explicit stored
  visibility field rather than access inferred from session ownership.
- **API contract: deferred** to the same design, constrained by the reserved
  [URL space](#url-and-identity-space), org-scoped authorization, and publish
  returning an ID, version, and durable link only once the record is durable.

## Implementation Design Follow-Up

A separate phase should define: tenancy-safe data models and the
`published`/`evidence` discriminator; object storage, retention enforcement,
size limits, MIME validation, tombstones, and cleanup; identity resolution and
republish rate limiting; capability, policy, audit shapes, and `143-tools`
registration; bundle packaging and isolated-origin serving with CSP enforcement;
link resolution, Slack unfurls, and the single authorization check; transcript
event representation and live-update delivery; API pagination and caching;
viewer components and safe format handlers; the [naming](#naming) cleanups; and
tests, migration sequencing, observability for the measures above, and rollout
controls.

Those decisions must preserve this product contract, especially the zero-state
absence, the two-row Overview cap, derived artifact identity, equal treatment in
lists, transcript provenance, durable org-scoped links, and the isolated
no-network rendering boundary.
