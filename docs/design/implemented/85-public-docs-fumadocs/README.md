# Design: Public Docs on 143.dev with Fumadocs

> **Status:** Implemented | **Last reviewed:** 2026-06-30

143.dev exposes public documentation at `/docs` from repo-authored MDX in `docs/public`.

The implementation uses Fumadocs inside the existing Next.js frontend. Fumadocs owns the document tree, MDX loading, table of contents, and route generation. 143 owns the information architecture, page copy, MDX components, screenshots, and visual fit with the product.

## Current Shape

- Public pages are routed from `frontend/src/app/(landing)/docs`.
- Public source lives only in `docs/public`.
- Raw Markdown routes are generated from the same public source.
- `/llms.txt` is generated from public docs metadata.
- Docs-specific UI lives under `frontend/src/components/docs`.
- Product screenshots live under `frontend/public/product`.

## Invariants

- Publish selected repo Markdown/MDX as first-party docs on `143.dev/docs`.
- Keep docs source-controlled, reviewable, and editable in normal PRs.
- Preserve the current Next.js/Tailwind/shadcn styling direction instead of shipping a detached docs property.
- Keep public docs curated. Do not expose `docs/design`, production-debugging notes, or internal implementation plans through public routes.
- Keep pages useful to both humans and agents: stable headings, concise summaries, real examples, raw Markdown, `llms.txt`, and useful alt/caption text.
- Prefer task guides before exhaustive reference. Move field tables and API details into `reference/*` pages.
- Use screenshots and diagrams only when they clarify real product state or setup decisions.

## Non-goals

- Do not expose the entire internal `docs/` tree directly. Internal design docs, production-debugging docs, and implementation notes are not all product docs.
- Do not introduce a separate docs application unless the Fumadocs integration creates unacceptable complexity.
- Do not use a hosted docs SaaS for v1. SaaS products can be revisited later if hosted search analytics, non-engineer editing, or API playgrounds become more important than repo-native ownership.
- Do not build a custom CMS. Markdown/MDX in the repo is the authoring source of truth.

## Decision Record

Fumadocs gives 143 standard docs infrastructure without giving up brand control:

- Fumadocs MDX transforms Markdown/MDX collections into typed data and React Server Components with table-of-contents metadata.
- Fumadocs Core exposes a loader API for page trees, slugs, static params, and content lookup.
- Fumadocs UI supports Tailwind v4, a shadcn color preset, light/dark mode via `next-themes`, and CSS/theme variable customization.
- Fumadocs supports progressive customization: first via props and CSS variables, then by installing/copying layout slots into the codebase when deeper control is needed.

## Public Source Tree

The public docs tree is deliberately separate from internal design docs:

```text
docs/
├── public/
│   ├── index.mdx
│   ├── meta.json
│   ├── getting-started/
│   │   ├── index.mdx
│   │   ├── connect-github.mdx
│   │   ├── start-a-session.mdx
│   │   └── review-and-ship.mdx
│   ├── guides/
│   │   ├── index.mdx
│   │   ├── repo-config.mdx
│   │   ├── previews.mdx
│   │   ├── linear-agent.mdx
│   │   ├── autopilot.mdx
│   │   └── coding-agent-auth.mdx
│   ├── self-hosting/
│   │   ├── index.mdx
│   │   ├── single-node.mdx
│   │   ├── github-app-setup.mdx
│   │   ├── platform-llm.mdx
│   │   ├── production-deployment-checklist.mdx
│   │   └── worker-capacity-tuning.mdx
│   └── reference/
│       ├── index.mdx
│       ├── repo-config-schema.mdx
│       ├── preview-config.mdx
│       ├── environment-variables.mdx
│       ├── agent-tools.mdx
│       └── external-api.mdx
└── design/
    └── implemented/
        └── 85-public-docs-fumadocs/
```

## Remaining Gaps

- Docs search should be revisited after the guide/reference split settles.
- API reference pages should grow toward endpoint-level examples and token-scope examples.
- Public docs need a lightweight contribution checklist for screenshots, frontmatter, and raw-Markdown quality.
- Some product areas still need public guides before launch maturity: Slack, code reviews, automations, and PR previews.

## Related Design Docs

- [content-model.md](content-model.md) defines what belongs in public docs.
- [product-design.md](product-design.md) defines the desired docs experience and styling constraints.
- [engineering-implementation.md](engineering-implementation.md) records the implemented build shape and quality gates.
