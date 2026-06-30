# Design: Public Docs Content Model

> **Status:** Implemented | **Last reviewed:** 2026-06-30

The public docs are a curated product surface, not a mirror of the repository's internal documentation. They answer user-facing questions first, then expose deeper reference material where it helps users configure or operate 143.

## Publishing rule

A page belongs under `docs/public` only if it helps one of these readers:

- A team evaluating 143.
- An engineer setting up 143 for a repo.
- An operator self-hosting 143.
- A user operating sessions, previews, Linear integration, or review flows.
- An AI coding agent trying to understand how to use or configure 143 from public docs.

Internal implementation design, production debugging instructions, and historical decision records should stay under `docs/design`, `docs/contributing`, or other internal folders unless they are rewritten for public consumption.

## Current Public Tree

The current public IA is intentionally small:

| Section | Purpose | Current pages |
| --- | --- | --- |
| Docs home | Explain what 143 docs cover and route readers to the right section. | `docs/public/index.mdx` |
| Get started | Short path from GitHub connection to first PR. | Overview, connect GitHub, start a session, review and ship |
| Guides | Task workflows for repo config, previews, Linear, Autopilot, and coding-agent auth. | `guides/*.mdx` |
| Self-hosting | Operator setup and production checks. | Overview, single-node, GitHub App, platform LLM, production checklist, worker capacity |
| Reference | Compact lookup material. | Repo config schema, preview config, environment variables, agent tools, external API |

### Link, do not publish directly

These docs may be referenced from public docs after they are summarized, but should not be copied as-is into the public tree:

- `docs/design/implemented/44-sandbox-preview-server.md`
- `docs/design/implemented/78-review-agent-loops.md`
- `docs/design/implemented/75-autopilot-issue-and-run-queue.md`
- `docs/design/implemented/59-session-issue-decoupling-and-multi-issue-linking.md`
- `docs/design/implemented/20-security-architecture.md`

Reason: they are implementation/design records. Public docs should describe product behavior, guarantees, operator responsibilities, and user workflows.

## Information Architecture

```text
Docs home
├── Get started
│   ├── Overview
│   ├── Connect GitHub
│   ├── Start a session
│   └── Review and ship
├── Guides
│   ├── Repo config
│   ├── Preview environments
│   ├── Linear agent
│   ├── Autopilot
│   └── Coding agent auth
├── Self-hosting
│   ├── Overview
│   ├── Quickstart: single-node
│   ├── GitHub App setup
│   ├── Platform LLM
│   ├── Production deployment checklist
│   └── Worker capacity tuning
└── Reference
    ├── Overview
    ├── Repo config schema
    ├── Preview config
    ├── Environment variables
    ├── Agent tools CLI
    └── External API
```

## Frontmatter contract

Every public page should define frontmatter that is useful to Fumadocs, search, SEO, and AI surfaces:

```yaml
---
title: Preview environments
description: Configure live previews for frontend and full-stack sessions.
section: Guides
order: 20
status: stable
audience: engineer
tags:
  - previews
  - repo-config
  - frontend
llm_summary: Learn how 143 starts an app preview from `.143/config.json`, checks readiness, and exposes the result in a session.
---
```

Required fields:

- `title`
- `description`
- `section`
- `order`
- `status`: `draft`, `beta`, or `stable`
- `audience`: `evaluator`, `engineer`, `operator`, or `agent`
- `tags`
- `llm_summary`

## Page-quality standards

- Start with the user outcome, not platform internals.
- Put the shortest successful path before configuration reference.
- Use screenshots, diagrams, or UI callouts where visual state matters. Avoid adding a diagram just because a page needs rhythm.
- Keep "how it works" sections below the primary workflow.
- Split large config references out of task guides.
- Every page should have clear next links.
- Every page should be understandable from raw Markdown, because agents and search systems may consume that version.

## AI-native docs requirements

- Generate `/llms.txt` from the docs index.
- Expose a raw Markdown version for every public docs page, either at `/docs/... .md` or via a `?format=md` route.
- Add "Open as Markdown" and "Copy Markdown" commands to each docs page.
- Include stable headings and short descriptive link text so AI tools can cite sections reliably.
- Avoid image-only explanations. Every diagram or screenshot needs adjacent explanatory text.
- Prefer structured examples with copy buttons over prose-only setup instructions.

## Next Content Gaps

- Slack: start sessions, answer human-input requests, and manage notifications from Slack.
- Code reviews: request the reviewer bot, understand findings, and handle acceptable-risk decisions.
- Automations: create scheduled/event-triggered work and interpret automation runs.
- PR previews: use branch and PR previews from GitHub review flows.
- Webhooks/API: expand reference docs when endpoint-level public contracts are ready.
