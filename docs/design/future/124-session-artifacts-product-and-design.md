# Design: Session Artifacts Product and UX

> **Status:** Not Started | **Last reviewed:** 2026-08-04
>
> **Depends on:** [overall](../overall.md), [frontend](../03-frontend.md),
> [visual system](../implemented/117-visual-system-and-product-polish.md),
> [transcript refactor](../implemented/101-session-transcript-refactor.md),
> [mobile top bar](../implemented/70-mobile-session-top-bar-consolidation.md),
> [agent run capabilities](../implemented/102-agent-run-capabilities.md),
> [audit logs](../implemented/34-audit-logs.md),
> [slackbot](92-slackbot-product-surface.md), and
> [preview verification](115-agent-native-preview-verification.md)

## Product Bet

**An artifact is how a session's output leaves the session.**

143 sessions often run unattended, and the people who care about the result may not be in 143. Today only a PR and a Slack notification reliably
escape a session; other useful output dies with the sandbox.

An artifact is a durable capture of work, not an application or workspace browser. Sharing, stable identity, versions, retention, and a calm
delivery moment are therefore part of the initial product rather than later polish.

## Decision

Artifacts are durable session outputs, not a new navigation destination. They have three surfaces:

1. **Transcript — primary discovery.** A compact row appears at the turn that published or updated the artifact.
2. **Overview — durable index.** A compact `Artifacts` section appears only when artifacts exist, keeping outputs findable after their turns
   scroll away.
3. **Durable org link — export.** A stable URL survives the sandbox, opens for authorized org members, and unfurls in Slack.

Selecting an artifact opens it in the session's center workspace. There is:

- no new detail tab;
- no permanent shelf;
- no featured artifact in any list;
- no card per artifact;
- no artifact UI when the session has none.

Overview shows at most two equal, borderless rows at rest. `View all N` opens the collection in the center workspace, giving the side panel a
hard space budget regardless of artifact count.

```text
agent runs `143-tools artifacts publish`
   +--> transcript publication event
   +--> Overview > Artifacts row
   +--> durable org link and Slack unfurl
            +--> center-workspace collection or viewer
```

The principle is restraint: content before containers, one obvious action per object, details after intent, and previews used for recognition
rather than decoration. Lists remain uniform; only the final delivery moment may give a lone terminal artifact more room.

## Goals

1. Publish with one `143-tools` command and derive stable identity so republishes create versions rather than duplicates.
2. Discover artifacts at creation in the transcript and afterward in Overview, preserving the session, thread, turn, and immutable version.
3. Render supported formats appropriately and provide an org-scoped durable URL.
4. Add zero chrome when empty and bounded chrome otherwise, using existing 143 resource-row, semantic-token, selected-state, and mobile patterns.
5. Govern publication through capabilities, org policy, audit, and retention.
6. Keep artifacts independent of live sandboxes and preview runtimes.

## Non-Goals

Non-goals are collaborative editing, annotations, a spatial canvas, a P0 cross-session library, implicit platform publication, replacement of
source control/Changes/Preview, and final schema, storage, API, or rollout details.

**Public unauthenticated sharing is explicitly excluded.** An agent can read a private repository; an anonymous link would be a data-exfiltration
surface. P0 links are org-scoped. Public sharing, if ever added, requires an org-owner toggle and a separate security design.

Deletion and retention are P0 governance requirements, not non-goals.

## Language and Product Model

### One user-facing meaning

`Artifact` means exactly one thing: **a durable output intentionally published by an agent during a session.** The CLI namespace is `artifacts`.

| Rejected name | Why |
| --- | --- |
| Canvases | Implies interactive output; wrong for PDF, CSV, and screenshots |
| Outputs | Conflicts with logs and tool output in the transcript |
| Deliverables | Overstates mid-run evidence as a commitment |
| Results | Overview already uses `Result` |
| Files | Implies a browsable workspace |
| Evidence | Correct for captures, wrong for prototypes and reports |

Before launch, existing visible uses must be disambiguated:

| Existing visible use | Required label |
| --- | --- |
| Eval run artifact count | `Outputs` |
| Code-review prompt/response artifacts | `Prompt records` |

Internal review-context and dependency/build-cache uses may keep idiomatic names until separate cleanup. No new user-facing string may call
anything other than a session artifact an artifact.

### Artifact and version

A logical artifact may have many immutable versions. Overview and the collection show one row per logical artifact and open the latest by
default. Transcript events stay bound to the version published by that turn.

Examples include images, HTML prototypes or static bundles, PDF or Markdown reports, SVG diagrams, structured data, and intentionally published
verification reports.

These are not published artifacts:

- **User attachment:** input supplied to the agent.
- **Workspace file:** mutable sandbox state, not durable because it exists.
- **Preview:** a live runtime.
- **Change:** repository source or diff state.
- **PR or branch:** code publication.
- **Verification capture:** durable evidence that has not been promoted.

### One durable-output model

Preview verification already creates durable screenshots. A second storage model would drift, so one model is discriminated by kind:

- `published`: intentionally published, listed in transcript and Overview.
- `evidence`: verification capture, linkable from verification UI but not listed as a session output.

Promotion changes kind or publication state rather than copying bytes.

## Information Architecture

### Overview placement

Artifacts sit between outcome and ordinary metadata:

1. Blocking session, runtime, or PR notice.
2. Result or failure summary.
3. `Artifacts`, only when one or more published artifacts exist.
4. Session vitals, origin, repository, branch, timing, and audit access.
5. Execution context and other supporting detail.

Failed and cancelled sessions retain partial artifacts. At zero artifacts the section renders nothing: no heading, placeholder, explanation, or
empty state.

### At-rest space contract

| Artifact count | Treatment | Approximate height |
| ---: | --- | ---: |
| 0 | Nothing | `0px` |
| 1 | Heading plus one row | `72-80px` |
| 2 | Heading plus two rows | `116-128px` |
| 3+ | Heading, two rows, `View all N` | maximum `148px` |

Exact dimensions may shift during visual QA; maximum height is a product constraint. Twenty artifacts must consume no more at-rest space than
three.

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

There is no outer artifact card and no card per row. The existing Overview panel is already a boundary; the section uses a heading, list rhythm,
and hairline dividers.

### Artifact row

```text
[32px preview/icon]  Title, one line                         [open cue]
                     Type · version/size · relative time
```

- The whole row is the primary `Open artifact` target.
- Target height is `44-48px` desktop and at least `44px` mobile.
- Title and metadata each occupy one line; accessible text exposes full values.
- There is no shadow, pill collection, visible button, or persistent overflow.
- Hover/focus uses a quiet accent surface.
- Selection uses soft accent plus an optional leading indicator.
- Download, copy, source, and version actions live in the viewer.
- Reuse or specialize `ResourceRow`; do not create a parallel card language.

The leading visual identifies rather than renders:

| Type | Leading treatment |
| --- | --- |
| Raster image | Cropped thumbnail preserving focal center |
| HTML/static site | Generated screenshot |
| PDF | First page when available, otherwise PDF icon |
| SVG | Safely rendered thumbnail |
| Video later | Poster frame with quiet play glyph |
| Markdown/text | Document icon |
| JSON/CSV | Data-file icon |
| Archive/unknown | Generic file icon |

All use one box and radius. Preview failure falls back to an icon without making the artifact unavailable or showing an error badge.

### Ordering and live arrival

Order logical artifacts by creation time. Updating versions changes metadata in place rather than pinning a frequently updated progress artifact
to the top. Ordering is not featuring; all rows remain visually equal.

A new row may enter with one quiet `160-200ms` transition. Publication must never open the artifact, switch tabs, move transcript scroll, or
steal focus.

### Complete collection

`View all N` opens the list state of artifact center mode, not a modal. This state is deep-linkable, can be viewed alongside Overview, and can
later grow into a cross-session library without a second interaction model.

The collection uses the same rows, shows every `published` artifact ordered by creation, and adds search/type filtering only above eight items.
Back restores the previous center mode and focus; selecting a row switches to the viewer.

On mobile the collection is a full-height sheet because the center workspace is already full screen.

### Durable URLs

Reserve the long-term URL and identity space:

- `/artifacts/:artifactId`: canonical org-scoped link and Slack URL.
- `/artifacts/:artifactId?v=:version`: immutable version.
- `/sessions/:sessionId?artifact=:artifactId`: in-session viewer state.

Artifact IDs are globally unique and org-scoped, never session-scoped. A future `/artifacts` index requires new UI, not link migration.

## Delivery and Viewing

### Final delivery moment

Uniform treatment governs lists, not the terminal answer. When a completed session's final result references:

- **one artifact:** show a recognition-size thumbnail or first page beneath the result summary;
- **two or more:** use the normal compact grouped rows.

This is the only promotion. It is derived from terminal state, not stored as featured. There is no `--featured` option, and the product never
auto-opens a browser when an unattended run finishes.

### Progress artifacts

A long run may republish one evolving artifact at meaningful checkpoints, such as a migration checklist or investigation timeline.

- A latest-version viewer updates in place without reload, interstitial, focus movement, or scroll jump.
- An older-version viewer remains fixed and offers `New version available`.
- Same-turn updates collapse in the transcript.
- Implementation defines a republish rate limit.

### Viewer

Opening preserves the mounted transcript, scroll position, and draft; switches the center workspace to artifact mode; keeps Overview visible with
selection on desktop; updates URL state; and opens latest unless the link names a version.

The header contains only `Back to conversation`, title, version selector when needed, one menu for copy/download/source, and full-screen when
useful.

Viewer and collection share one center mode, header pattern, and back behavior. Entering from review returns to review. Avoid duplicated title,
metadata, or actions in stacked headers.

## Sharing, Security, and Governance

### Org-scoped access

Every artifact has a durable `/artifacts/:artifactId` link. Any owning-org member can open it after sandbox deletion, session archival, or
preview reclamation; it never grants anonymous access. `Copy link` copies this canonical URL rather than session viewer state.

Visibility is an explicit stored field, initially `org`, rather than inferred at read time. That permits future `private` or
`session-participants` values without reinterpreting existing links.

Every read path—viewer, collection, thumbnail, raw isolated-origin content, and Slack unfurl—uses one authorization decision that reads
visibility. No endpoint may serve bytes on possession of an ID alone.

### Slack

P0 unfurls show thumbnail, title, type, version, and originating session. An unfurl resolves for its viewer and renders nothing outside the
owning org.

### Isolation and network policy

Agent-authored content may contain private source. The rendering boundary is a product constraint:

1. HTML and SVG render on one documented isolated origin, never the app origin.
2. CSP blocks outbound scripts, styles, fonts, images, `fetch`, XHR, and WebSockets. Pages inline CSS/JS and embed images.
3. Interactive HTML is sandboxed and cannot capture application shortcuts outside explicit focus.
4. Explain blocked capabilities only when they visibly affect the page.

Live data may later use declared connectors. Arbitrary artifact network access is not part of P0.

### Publication control

- **Capability:** publication is a named agent-run capability; agents without it do not see the tool.
- **Org policy:** an org setting enables publication and defaults on.
- **Audit:** publish, version, and deletion events record org, session, actor, artifact ID, version, and size.

143 does not block unattended work with a human confirmation prompt. Capability, policy, and audit are the appropriate controls for an autonomous
product.

### Retention and deletion

P0 requires an org-configurable retention default, an admin action deleting one logical artifact and all versions, a deletion audit event, and a
durable tombstone at the original URL and transcript location.

A deleted artifact shows plain `Deleted` state. IDs are never reused, and historical events never become unrelated links. User deletion and
per-version deletion are later work.

## Transcript Design

The transcript is primary discovery and explains provenance; it is not a second gallery.

```text
I created a prototype of the revised checkout flow.

Artifact published
[preview] Checkout prototype
          HTML · v1                                      ›
```

The row shares Overview anatomy without a large bordered card. It may use a slightly larger preview but remains a reference, except at final
delivery.

An update is labeled `Artifact updated`, stays bound to the turn's version, quietly shows `v1 · Latest v3` when stale, opens that historical
version with a path to latest, and collapses consecutive same-artifact updates within one turn.

Multiple artifacts in one turn share one `Artifacts` heading and dividers. After three rows, collapse the remainder behind `Show N more`.

The platform creates events only after publication is durable. Agents need not paste raw URLs. Events participate in pagination and remain in the
producing thread and turn; Overview collects published artifacts across all threads.

## Agent Publishing Experience

```bash
143-tools artifacts publish \
  --path ./artifacts/report.pdf \
  --title "Accessibility report"

# P0b static bundle
143-tools artifacts publish \
  --path ./artifacts/checkout-prototype \
  --entry index.html \
  --title "Checkout prototype"
```

### Stable identity

Agents should not remember opaque IDs across turns:

Within a session, the same `--path` updates the same artifact. `--key <slug>` preserves identity across path changes; `--artifact-id <id>`
updates an artifact from another session; `--new` forces a distinct object; identical bytes are a no-op returning the existing version.

This prevents twenty progress publishes from becoming twenty Overview objects.

### Command requirements

- Infer org/session/thread/turn and format; reject unsupported content clearly.
- Default title from filename and encourage short readable names.
- Enforce per-artifact size and per-session count ceilings atomically.
- Return ID, version, and canonical link only when UI can converge, p95 under 3s.
- Never expose or require featured state.

Help and size errors guide agents to prefer SVG/HTML/CSS over embedded raster, omit needless interactivity, summarize large data, and publish at
meaningful checkpoints.

## States

| State | Treatment |
| --- | --- |
| Ready | Thumbnail/icon, title, metadata; row opens viewer |
| Preview processing | Stable type icon; preview replaces it without moving row |
| Preview unavailable | Type icon, no error badge |
| Unavailable | Plain status; retry in viewer; red only for urgent data loss |
| Deleted | Plain tombstone, no thumbnail or open action |
| Publish failure | No ghost row; actionable tool error |
| Size/quota rejection | Name limit and file; create nothing |

If durable metadata exists but post-processing fails, list the artifact with a readable fallback.

## Responsive and Visual Behavior

### Desktop

Keep the resizable panel and existing tabs, use one artifact column at every width, remove secondary metadata before title or touch target, and
render rich content only in the center workspace.

### Mobile

Show the same two-row Overview section in the existing details sheet. Collection uses a full-height sheet and artifacts a full-screen viewer;
back returns to the transcript and restores Overview scroll where practical. Add nothing to the persistent top bar; transcript rows remain at
least `44px` without overflow.

### Visual contract

Use warm semantic surfaces, hairlines, Geist dense roles, `SectionGroup`, `ResourceRow`, and one selected-state signal. Reserve saturation for
state and primary actions. No gradients, glow, sparkles, special AI styling, or hover on static elements. Motion is limited to selection, viewer
transitions, and preview replacement.

Previews are small windows for recognition, not promotional tiles. Titles carry identity; previews only shorten recognition time.

## Accessibility

- One focusable row action opens with `Enter` or `Space`; its accessible name includes full title, type, and version.
- Decorative thumbnails use empty alt text; meaningful content is described in the viewer.
- Focus is visible at every width/theme, state is not color-only, and mobile targets remain at least `44px`.
- `View all N` announces count and restores focus; the viewer focuses its heading and returns to origin where possible.
- Live updates announce without moving focus; sandbox content cannot capture app shortcuts while unfocused.

## Product Reference Lessons

| | Claude Code | Antigravity | Cursor | 143 |
| --- | --- | --- | --- | --- |
| Name | Artifacts | Artifacts | Canvases | Artifacts |
| Formats | HTML/Markdown | Plans, walkthroughs, captures | Interactive pages | Eight P0 types |
| Audience | Author | Workspace | Thread | Org |
| Sharing | Org/public | Agent Manager | Snapshot | Org link + Slack |
| Cross-session index | Yes | Yes | — | Deferred; URLs reserved |
| Live update | Yes | Progress lists | — | Latest viewer |
| Content network | Blocked | n/a | n/a | Blocked |
| Retention/delete | Yes | — | — | P0 |

Taken:

- versioning, live updates, retention, audit, and delete from [Claude Code](https://code.claude.com/docs/en/artifacts);
- plans, walkthroughs, and captures as artifacts from [Antigravity](https://antigravity.google/docs/walkthrough);
- output-only links from [Cursor](https://sutopo.com/cursor-21-rewrites-the-agentic-coding-loop-2026-dev-tool/);
- summary discovery with rich selection previews from [Codex](https://openai.com/index/codex-for-almost-everything/);
- separation of live environments and durable outputs from [Devin](https://docs.devin.ai/work-with-devin/devin-session-tools).

Declined: dedicated tab, auto-open browser, private-by-default share step, and public links in P0.

## Initial Scope

### P0a — single file

1. Publish one file with derived identity and immutable versions.
2. Support image, PDF, SVG, single-page HTML, Markdown, text, JSON, and CSV through five viewers.
3. Add bounded Overview, transcript events, and center collection/viewer with download, copy, version access, deep links, org links, and Slack
   unfurls.
4. Isolate HTML/SVG and block outbound network.
5. Add capability/policy, audit, retention, admin delete, and tombstones.
6. Promote one terminal artifact and live-update latest-version viewers.

### P0b — static bundle

Add multi-file bundles via `--entry`, including packaging, relative-path serving on the isolated origin, and cleanup. This follows P0a because
bundles carry most storage and routing complexity; P0a is independently useful.

### Later

Video; user uploads; annotations; version comparison; declared live-data connectors; project/session promotion; public sharing behind owner
policy; and a cross-session `/artifacts` library.

## Acceptance and Measures

### Acceptance

1. Users open artifacts from transcript/Overview and share durable links that survive sandbox reclamation.
2. Zero artifacts add zero chrome; twenty use the same height as three; every list is equal except the single terminal-artifact delivery moment.
3. Twenty republishes create one artifact, twenty versions, and one Overview row.
4. Collection is one action away; viewer round-trips preserve transcript/draft; historical events open their version and expose latest.
5. Mobile adds no navigation; rows work at minimum width, `200%` zoom, keyboard-only, and in both themes.
6. HTML/SVG use an isolated no-network origin and every read applies the same stored visibility check.
7. `Artifact` has one user-facing meaning.

### Measures

| Measure | Target |
| --- | --- |
| Publish to visible in Overview/transcript | p95 under 3s |
| Artifact-producing sessions with an open | above 60% |
| Median publish to first open, completed sessions | under 24h |
| Links opened by another org member | Track; near zero invalidates sharing bet |
| Mean duplicate logical artifacts per session | under 1.05 |

## Design Validation

Prototype:

- Overview with zero, one, two, six, and twenty artifacts.
- Turns publishing one, updating one, and publishing five.
- Final turns for one-artifact and three-artifact completed sessions.
- Viewer/collection at `1440x900`, `1024x768`, and `390x844`.
- Long titles, missing previews, versions, unavailable/deleted states, dark mode.
- Keyboard movement Overview → viewer → collection → return.

Two decisive reviews:

1. At default `384px` detail width with six artifacts, the section must not resemble cards, crowd metadata, or become a file browser before
   intent.
2. In a completed single-artifact session, the sole deliverable must be immediately recognizable in the final turn.

## Technical Contracts and Follow-Up

Implementation design must define the tenancy-safe `published`/`evidence` schema, org-scoped IDs and versions, derived identity, stored
visibility and one authorization check, storage/MIME/limits/retention/tombstones, capabilities and audit, CLI and rate limits, bundle
isolation/CSP, links and Slack unfurls, transcript/live updates, APIs/caching/viewers, naming cleanup, tests, observability, and rollout.

Schema and API are deferred to that design, constrained by reserved URLs, org-scoped authorization, and publish returning ID, version, and link
only after commit.

Implementation must preserve zero-state absence, the two-row/`148px` cap, derived identity, equal list treatment, transcript provenance, durable
org links, retention/tombstones, and isolated no-network rendering.
