# Design: Fumadocs Engineering Implementation

> **Status:** Implemented | **Last reviewed:** 2026-06-30

This document records the implemented public docs architecture. It exists so future changes preserve the boundary between public docs, internal design records, and product UI.

## Architecture

Docs live inside the existing `frontend/` Next.js app rather than a separate app.

Public routes:

```text
https://143.dev/docs
https://143.dev/docs/getting-started
https://143.dev/docs/guides/repo-config
https://143.dev/docs/self-hosting
https://143.dev/docs/reference/repo-config-schema
```

Target source directory:

```text
docs/public/
```

Fumadocs reads from `../docs/public` relative to the `frontend/` package. Public docs live entirely in `docs/public`; there is no synced copy under `frontend/content/docs`.

## Implemented Frontend Shape

```text
frontend/
├── source.config.ts
├── next.config.ts
├── src/
│   ├── app/
│   │   ├── (landing)/
│   │   │   └── docs/
│   │   │       ├── layout.tsx
│   │   │       ├── [[...slug]]/
│   │   │       │   └── page.tsx
│   │   └── api/
│   │       └── docs/
│   │           └── raw/
│   │               ├── route.ts        # raw docs home
│   │               └── [...slug]/
│   │                   └── route.ts    # raw Markdown pages
│   ├── components/
│   │   └── docs/
│   │       ├── docs-mdx-components.tsx
│   │       └── docs-theme-switch.tsx
│   └── lib/
│       └── docs/
│           ├── layout.ts
│           ├── public-docs.ts
│           ├── raw-markdown.ts
│           ├── raw-docs-route.ts
│           └── sidebar-labels.ts
└── public/
    └── product/
        └── ...                         # docs screenshots
```

Keep docs-specific UI under `src/components/docs` so Fumadocs customization does not leak into unrelated product UI.

## Package direction

Packages:

- `fumadocs-core`
- `fumadocs-ui`
- `fumadocs-mdx`
- `next-mdx-remote-client`

Use versions compatible with the existing stack:

- Next.js 16+
- React 19+
- Tailwind v4
- `next-themes`
- shadcn-style theme tokens

## Implementation principles

### 1. Public docs are curated, not mirrored

Only `docs/public` is routed publicly. Do not create a catch-all route that can read arbitrary files from `docs/`.

Reason: `docs/design`, production-debugging notes, and internal architecture records may contain implementation detail that is not intended as user-facing product documentation.

### 2. Keep docs build-time where possible

Use static generation for docs pages. Docs content changes only at deploy time, so runtime database/API dependencies are unnecessary.

Requirements:

- `generateStaticParams()` should use the Fumadocs source.
- Missing pages should return `notFound()`.
- Metadata should be generated from page frontmatter.
- The docs route should work in the existing static/standalone deployment model.

### 3. Use typed frontmatter

Validate frontmatter with the Fumadocs schema option or an equivalent typed layer.

Required fields:

- `title`
- `description`
- `section`
- `order`
- `status`
- `audience`
- `tags`
- `llm_summary`

Invalid frontmatter should fail the build.

### 4. Keep the visual system first-party

Use the Fumadocs shadcn preset and map Fumadocs colors to existing theme tokens.

Requirements:

- No hardcoded docs color palette.
- No separate docs-only typography stack.
- No marketing-style hero layout for docs home.
- Docs controls should use existing button/input/icon patterns where practical.
- If Fumadocs default UI conflicts with 143's style, wrap or copy that component rather than fighting it with brittle CSS.

### 5. Customize progressively

Start with Fumadocs props, theme variables, and MDX component mappings. Only install/copy Fumadocs layout code with the CLI for specific parts that cannot be styled cleanly.

When copying Fumadocs components:

- Put copied code under `src/components/docs/vendor/` or a clearly named docs component folder.
- Add a short file header noting the upstream component and version.
- Treat copied UI as owned code afterward.
- Avoid mixing copied and package-imported types accidentally.

### 6. Preserve raw Markdown access

Each public docs page has a raw Markdown representation for AI agents and power users.

Implemented path shape:

- `/api/docs/raw/guides/repo-config`

`/llms.txt` is generated from the docs index. It lists canonical docs URLs, short summaries, and raw Markdown URLs.

### 7. Separate guides from references

Do not let long config tables dominate task pages. For example:

- `guides/previews.mdx`: how to configure a working preview.
- `reference/preview-config.mdx`: exhaustive config fields and constraints.

This keeps Stripe-like guides scannable while preserving detail for operators.

### 8. Images are first-class content

Docs pages support product screenshots and a small set of reusable diagrams.

Engineering requirements:

- Use `frontend/public/product` for product screenshots used by public docs.
- Support stable dimensions/aspect ratios.
- Use Next image optimization only if it works with the deployment mode and source location.
- Add a reusable `Screenshot` MDX component with caption and alt text.
- Do not allow images to overflow article width on mobile.

### 9. Search must be keyboard-friendly

Use Fumadocs search initially unless it conflicts with the desired UX.

Requirements:

- Search is available from the docs shell.
- `Cmd+K` can remain product-global where appropriate, but docs search should not conflict with authenticated app command-palette behavior.
- Results should include title, description, headings, and page URLs.

### 10. Maintain existing frontend quality gates

Docs changes should run from `frontend/`:

```bash
npm run typecheck
npm run lint
npm run build
```

Add focused tests for:

- Docs route renders a known page.
- Missing docs route returns 404.
- Frontmatter metadata appears in generated page metadata.
- MDX component mapping renders code blocks, callouts, tables, and images.
- Raw Markdown or `llms.txt` generation includes expected pages.

## Delivered Build Plan

### Phase 1: Skeleton

- Added Fumadocs packages and config.
- Added `docs/public/index.mdx` and section `meta.json` files.
- Added `/docs/[[...slug]]` route.
- Rendered page title, content, sidebar, TOC, and next/previous links.
- Wired the existing website nav around the docs layout.

### Phase 2: Styling and components

- Applied shadcn/Fumadocs theme integration.
- Added `src/components/docs/docs-mdx-components.tsx`.
- Implemented/wrapped Callout, Steps, CodeBlock, Screenshot, ConfigField, FlowDiagram, BoundaryDiagram, and AgentNote.
- Added responsive behavior for mobile sidebar/search/TOC.

### Phase 3: Content migration

- Added getting-started pages.
- Rewrote repo config, preview, Linear, Autopilot, and coding-agent auth guides for public use.
- Split task guides from reference pages.
- Added self-hosting pages safe for public operators.

### Phase 4: AI-native surfaces

- Generated `/llms.txt`.
- Added raw Markdown routes.
- Added page-level `llm_summary` to public docs.
- Added docs source tests for public page metadata and raw surfaces.

### Phase 5: Polish

- Added real product screenshots for core getting-started and preview pages.
- Added restrained reusable diagrams for sequence and boundary explanations.
- Still open: docs search refinements, endpoint-level API polish, and contribution instructions.

## Risks and mitigations

| Risk | Mitigation |
| --- | --- |
| Fumadocs default UI feels generic | Use shadcn preset first, then replace docs-specific components under `src/components/docs`. |
| Publishing internal docs by accident | Route only `docs/public`; make raw Markdown routes read from the same source only. |
| MDX outside `frontend/` is hard to compile | Configure Fumadocs/build tooling to read `docs/public` directly. Do not create a second synced docs tree under `frontend/content/docs`. |
| Docs become too reference-heavy | Split task guides from `reference/*` pages before launch. |
| Custom CSS becomes brittle | Prefer props, components, and copied layout slots over undocumented DOM selectors. |
| AI docs surfaces leak internal content | Generate `/llms.txt` only from the public docs source. |

## Resolved decisions

- Public docs live entirely in `docs/public`.
- `/docs` should use a docs-specific top nav with the same visual language as the public website.
- Raw Markdown should be available through stable URL routes and through page-level copy/download actions.

## Open Questions

- Should docs search be local/static only for v1, or should it use a hosted/indexed search later?
- Which API/reference pages need generated endpoint examples instead of hand-authored prose?
