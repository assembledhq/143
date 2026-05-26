# Public Docs Product and Visual Design

> **Status:** Future design
> **Last reviewed:** 2026-05-23

The docs should feel like part of 143.dev, not a separate documentation vendor theme. The user should land in a calm, dense, modern technical reference that helps them complete real setup and review workflows.

## Experience principles

- **Guide-first, reference-second.** Most users arrive with a task: connect a repo, start a session, configure previews, assign Linear issues, self-host. The first-level IA should reflect those tasks.
- **Product behavior over implementation history.** Public docs should describe what users can rely on. Link internal design docs only when the reader explicitly needs architecture context.
- **Dense but readable.** Match the product's compact rhythm while preserving prose readability. Use generous line height for article bodies, but avoid marketing-style oversized sections.
- **No detached docs brand.** The docs top nav, footer, type scale, color tokens, and interaction states should align with the public website and app shell.
- **AI-readable by default.** Pages should be useful to both humans and agents. Raw Markdown, stable headings, examples, and metadata are product features.

## Layout

Desktop docs pages should use a three-column reading layout:

```text
┌─────────────────────────────────────────────────────────────────────┐
│ Public website nav                                                   │
├───────────────┬───────────────────────────────────────┬─────────────┤
│ Section nav   │ Article content                       │ On this page│
│               │                                       │ + page tools│
└───────────────┴───────────────────────────────────────┴─────────────┘
```

Mobile should collapse to:

- Docs-specific top nav with the same visual language as the public website.
- Docs section selector / search.
- Article body.
- Inline page tools.
- Previous/next links.

Use Fumadocs layout primitives initially, but customize enough that the surface follows the 143 product design:

- Neutral-first background.
- Subtle borders/separators instead of heavy cards.
- Compact nav rows.
- Restrained selected states.
- Consistent focus rings.
- No decorative gradient/orb backgrounds.

## Navigation

Top-level docs groups:

- Get started
- Guides
- Self-hosting
- Reference

The sidebar should show only public docs pages. Internal design docs should never appear in this tree.

The sidebar should keep the top-level docs groups visible while browsing inside a group. Section folders should stay in a single Fumadocs tree instead of using separate root navigation scopes.

The docs home page should be a functional index, not a marketing page. It should show:

- A short description of what the docs cover.
- Four entry cards for Get started, Guides, Self-hosting, and Reference.
- A "popular guides" list.
- A "for AI agents" block linking to `/llms.txt` and raw Markdown access.

## Page components

The first implementation should support these MDX components:

- `Callout`: info, warning, success, danger.
- `Steps`: numbered workflow steps.
- `CodeBlock`: syntax highlighted, filename/title, copy action.
- `Tabs`: package-manager or platform variants.
- `Cards`: compact links to related pages.
- `Screenshot`: responsive image with caption and optional lightbox.
- `ConfigField`: field name, type, required flag, default, description.
- `Endpoint`: method/path summary for future API docs.
- `AgentNote`: special note for AI/automation readers, visually subtle.

Prefer Fumadocs-provided components where they are close enough. Wrap or replace them when the visual treatment does not match 143.

## Visual style

Use the existing frontend design direction:

- Tailwind/shadcn semantic tokens, not hardcoded hex colors.
- `text-foreground`, `text-muted-foreground`, `bg-background`, `bg-card`, `border-border`, and product `primary` tokens.
- Geist font via the existing app layout.
- Lucide icons for search, copy, external links, section icons, and page tools.
- Compact button sizes matching the website's existing primitives.

Article typography:

- Body width should optimize reading, not full-screen density.
- Headings should be clearly stepped but not hero-scale.
- Code blocks should be high contrast enough for long commands and JSON.
- Tables should support horizontal overflow on mobile.
- Images should have stable dimensions or aspect ratios to avoid layout shift.

## Customization stance

Customization should happen in this order:

1. Fumadocs props and config.
2. Theme variables and shadcn preset.
3. MDX component mapping/wrappers.
4. Scoped CSS selectors that target documented `id`/`data-*` hooks.
5. Fumadocs CLI-installed layout slots only when the above cannot produce the desired UX.

Do not deeply target undocumented DOM structure. Fumadocs explicitly warns that invasive DOM selectors are brittle across UI updates.

## Search

Docs search should be present in the first public release. It does not need to search authenticated product data.

Search requirements:

- Keyboard accessible.
- Search from docs nav.
- Include page title, description, headings, and structured content.
- Return stable URLs with heading anchors when possible.
- Keep the result design aligned with command-palette density.

Implementation can start with Fumadocs search defaults and evolve to a custom search UI later.

## Images and media

Store docs images under `docs/public/images` or `frontend/public/docs` after final implementation choice. The selected path should be documented in the contribution guide.

Image requirements:

- Use real product screenshots where the user needs to recognize UI.
- Use diagrams for architecture only when they clarify setup or operator decisions.
- Avoid stock/atmospheric imagery.
- Captions should explain the action/state shown.
- Every image needs useful alt text.

## Stripe-like guide quality

"Similar to Stripe's guides" means the following for 143:

- Guides are task-based, not feature dumps.
- Each guide starts with prerequisites and the expected final state.
- Code/config examples are copyable and realistic.
- Deep explanation exists, but after the path to success.
- Reference pages are separated from narrative guides.
- Related links are curated, not autogenerated noise.
- The docs visually feel first-party and polished.
