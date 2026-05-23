# Fumadocs Engineering Implementation

> **Status:** Future design
> **Last reviewed:** 2026-05-23

This document is the implementation brief for adding public docs to 143.dev with Fumadocs. It is written for the engineer who will build the feature.

## Architecture

Add docs to the existing `frontend/` Next.js app rather than creating a separate app.

Target route:

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

Fumadocs should read directly from `../docs/public` relative to the `frontend/` package. Public docs should live entirely in `docs/public`; do not sync or copy them into `frontend/content/docs` as a second content tree.

## Suggested frontend file shape

```text
frontend/
├── source.config.ts
├── next.config.ts
├── src/
│   ├── app/
│   │   └── (landing)/
│   │       └── docs/
│   │           ├── layout.tsx
│   │           ├── [[...slug]]/
│   │           │   └── page.tsx
│   │           └── [...slug]/
│   │               └── route.ts        # raw Markdown route
│   ├── components/
│   │   └── docs/
│   │       ├── docs-layout.tsx
│   │       ├── docs-mdx-components.tsx
│   │       ├── docs-page-tools.tsx
│   │       ├── config-field.tsx
│   │       ├── screenshot.tsx
│   │       └── agent-note.tsx
│   └── lib/
│       └── docs/
│           ├── source.ts
│           ├── navigation.ts
│           ├── raw-markdown.ts
│           └── llms.ts
└── public/
    └── docs/
        └── ...                         # built/static assets if needed
```

Exact file names can vary, but keep docs-specific UI under `src/components/docs` so Fumadocs customization does not leak into unrelated product UI.

## Package direction

Expected packages:

- `fumadocs-core`
- `fumadocs-ui`
- `fumadocs-mdx`
- Any required Fumadocs Next.js integration package for the current version.

Use versions compatible with the existing stack:

- Next.js 16+
- React 19+
- Tailwind v4
- `next-themes`
- shadcn-style theme tokens

Before implementation, check current Fumadocs installation docs because package names and Next integration details may change.

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

Each public docs page should have a raw Markdown representation for AI agents and power users.

Acceptable implementations:

- `/docs/guides/repo-config.md`
- `/docs/guides/repo-config?format=md`

Every page should also include a page-level "Copy Markdown" action backed by the same raw source. URL-based raw Markdown access and copy/download affordances are complementary, not alternatives.

Also generate `/llms.txt` from the docs index. It should list canonical docs URLs, short summaries, and raw Markdown URLs.

### 7. Separate guides from references

Do not let long config tables dominate task pages. For example:

- `guides/previews.mdx`: how to configure a working preview.
- `reference/preview-config.mdx`: exhaustive config fields and constraints.

This keeps Stripe-like guides scannable while preserving detail for operators.

### 8. Images are first-class content

Docs pages should support product screenshots and diagrams from the start.

Engineering requirements:

- Define one blessed image directory.
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

This is a frontend change. After implementation, run from `frontend/`:

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

## Build plan

### Phase 1: Skeleton

- Add Fumadocs packages and config.
- Add `docs/public/index.mdx` and minimal `meta.json`.
- Add `/docs/[[...slug]]` route.
- Render page title, content, sidebar, TOC, and next/previous links.
- Wire existing website nav around the docs layout.

### Phase 2: Styling and components

- Apply shadcn/Fumadocs theme integration.
- Add `src/components/docs/docs-mdx-components.tsx`.
- Implement/wrap Callout, Steps, CodeBlock, Screenshot, ConfigField, and AgentNote.
- Add responsive behavior for mobile sidebar/search/TOC.

### Phase 3: Content migration

- Move and rewrite `docs/guides/repo-config.md`.
- Move and split `docs/guides/previews.md`.
- Move and rewrite `docs/guides/linear-agent.md`.
- Move self-hosting docs that are safe for public use.
- Add the missing getting-started flow.

### Phase 4: AI-native surfaces

- Generate `/llms.txt`.
- Add raw Markdown routes and copy/download actions.
- Add page-level `llm_summary` to all public docs.
- Add link validation for public docs.

### Phase 5: Polish

- Add screenshots and diagrams.
- Add docs search refinements.
- Add SEO metadata and Open Graph handling.
- Add redirects if old public docs URLs exist.
- Add contribution instructions for public docs authoring.

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

## Open questions

- Should docs search be local/static only for v1, or should it use a hosted/indexed search later?
- Which screenshots should be created before launch, and which guides can ship text-first?
