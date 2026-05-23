# Design: Public Docs on 143.dev with Fumadocs

> **Status:** Future design
> **Last reviewed:** 2026-05-23

143.dev should expose polished public documentation at `/docs` using repo-authored Markdown/MDX and a docs shell that feels native to the main website.

The recommended implementation is **Fumadocs inside the existing Next.js frontend**, using Fumadocs MDX/content loading for document structure and a 143-owned presentation layer for branding, navigation, and product-specific guide components.

## Goals

- Publish selected repo Markdown/MDX as first-party docs on `143.dev/docs`.
- Keep docs source-controlled, reviewable, and editable in normal PRs.
- Preserve the current Next.js/Tailwind/shadcn styling direction instead of shipping a visually separate docs property.
- Make the docs feel modern and AI-native: clean guide pages, strong code/image support, copyable Markdown, structured metadata, `llms.txt`, and page-level raw Markdown routes.
- Build a durable content organization that can grow from guides into self-hosting, configuration, API/reference, and integration docs.

## Non-goals

- Do not expose the entire internal `docs/` tree directly. Internal design docs, production-debugging docs, and implementation notes are not all product docs.
- Do not introduce a separate docs application unless the Fumadocs integration creates unacceptable complexity.
- Do not use a hosted docs SaaS for v1. SaaS products can be revisited later if hosted search analytics, non-engineer editing, or API playgrounds become more important than repo-native ownership.
- Do not build a custom CMS. Markdown/MDX in the repo is the authoring source of truth.

## Decision

Use Fumadocs because it gives 143 the standard docs infrastructure without giving up brand control:

- Fumadocs MDX transforms Markdown/MDX collections into typed data and React Server Components with table-of-contents metadata.
- Fumadocs Core exposes a loader API for page trees, slugs, static params, and content lookup.
- Fumadocs UI supports Tailwind v4, a shadcn color preset, light/dark mode via `next-themes`, and CSS/theme variable customization.
- Fumadocs supports progressive customization: first via props and CSS variables, then by installing/copying layout slots into the codebase when deeper control is needed.

Relevant upstream docs:

- Fumadocs MDX: <https://www.fumadocs.dev/docs/mdx>
- Fumadocs loader API: <https://www.fumadocs.dev/docs/headless/source-api>
- Fumadocs theme customization: <https://www.fumadocs.dev/docs/ui/theme>
- Fumadocs UI customization: <https://www.fumadocs.dev/docs/guides/customize-ui>

## Proposed documentation source tree

Create a curated public docs subtree rather than publishing from mixed internal folders:

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
│   │   ├── repo-config.mdx
│   │   ├── previews.mdx
│   │   └── linear-agent.mdx
│   ├── self-hosting/
│   │   ├── index.mdx
│   │   ├── github-app-setup.mdx
│   │   ├── platform-llm.mdx
│   │   ├── production-deployment-checklist.mdx
│   │   └── worker-capacity-tuning.mdx
│   ├── reference/
│   │   ├── repo-config-schema.mdx
│   │   ├── preview-config.mdx
│   │   └── environment-variables.mdx
│   └── images/
│       └── ...
└── design/
    └── future/
        └── 85-public-docs-fumadocs/
```

The existing `docs/guides` and `docs/self-hosting` files can remain as source material during migration, but published docs should live under `docs/public` so the boundary between public and internal documentation is explicit.

## Related design docs

- [content-model.md](content-model.md) defines what should be published and how it should be organized.
- [product-design.md](product-design.md) defines the desired docs experience and styling constraints.
- [engineering-implementation.md](engineering-implementation.md) defines implementation principles and a suggested build plan.
