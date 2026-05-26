# Public Docs Content Model

> **Status:** Future design
> **Last reviewed:** 2026-05-23

The public docs should be a curated product surface, not a mirror of the repository's internal documentation. The docs tree should answer user-facing questions first, then expose deeper reference material where it helps users configure or operate 143.

## Publishing rule

A page belongs under `docs/public` only if it helps one of these audiences:

- A team evaluating 143.
- An engineer setting up 143 for a repo.
- An operator self-hosting 143.
- A user operating sessions, previews, Linear integration, or review flows.
- An AI coding agent trying to understand how to use or configure 143 from public docs.

Internal implementation design, production debugging instructions, and historical decision records should stay under `docs/design`, `docs/contributing`, or other internal folders unless they are rewritten for public consumption.

## Source material to incorporate

### Incorporate first

These files are already close to public guide quality and should be migrated into `docs/public` early:

| Existing file | Target public page | Notes |
| --- | --- | --- |
| `docs/guides/repo-config.md` | `docs/public/guides/repo-config.mdx` | Keep the practical guide. Split dense field references into `reference/repo-config-schema.mdx` if the page gets too long. |
| `docs/guides/previews.md` | `docs/public/guides/previews.mdx` and `docs/public/reference/preview-config.mdx` | Keep the how-to content in Guides. Move detailed config tables and architecture caveats into Reference. |
| `docs/guides/linear-agent.md` | `docs/public/guides/linear-agent.mdx` | Strong guide candidate. Add screenshots or diagrams after the Fumadocs shell exists. |
| `docs/self-hosting/README.md` | `docs/public/self-hosting/index.mdx` | Rewrite intro for external operators. |
| `docs/self-hosting/github-app-setup.md` | `docs/public/self-hosting/github-app-setup.mdx` | High-value setup doc. |
| `docs/self-hosting/platform-llm.md` | `docs/public/self-hosting/platform-llm.mdx` | Public if it does not expose private deployment assumptions. |
| `docs/self-hosting/production-deployment-checklist.md` | `docs/public/self-hosting/production-deployment-checklist.mdx` | Public checklist for operators. |
| `docs/self-hosting/worker-capacity-tuning.md` | `docs/public/self-hosting/worker-capacity-tuning.mdx` | Public operator guide. |

### Add as new public guides

These are missing from the current docs and should be created for the first public docs release:

- `getting-started/index.mdx`: "Start here" page that explains what 143 is, what it needs access to, and what the first successful workflow looks like.
- `getting-started/connect-github.mdx`: GitHub App installation, repo import, org/repo ownership, and expected permissions.
- `getting-started/start-a-session.mdx`: Open a manual coding session, pick a repo/branch/agent, attach context, and send follow-ups.
- `getting-started/review-and-ship.mdx`: Review diffs, use previews, run review loops, open PRs, repair PR health, and merge.
- `guides/autopilot.mdx`: Explain the Autopilot queue, issue prioritization, auto-run gates, and when humans are asked to intervene.
- `guides/coding-agent-auth.mdx`: Explain Codex/Claude/Gemini auth expectations without leaking internal implementation details.
- `guides/using-previews-to-review-frontend-changes.mdx`: Higher-level workflow guide that links to preview config reference.
- `reference/environment-variables.mdx`: Public self-hosting env var reference with secret-handling warnings.
- `reference/webhooks.mdx`: Public inbound webhook overview once the API/webhook story is ready.

### Link, do not publish directly

These docs may be referenced from public docs after they are summarized, but should not be copied as-is into the public tree:

- `docs/design/implemented/44-sandbox-preview-server.md`
- `docs/design/implemented/78-review-agent-loops.md`
- `docs/design/implemented/75-autopilot-issue-and-run-queue.md`
- `docs/design/implemented/59-session-issue-decoupling-and-multi-issue-linking.md`
- `docs/design/implemented/20-security-architecture.md`

Reason: they are implementation/design records. Public docs should describe product behavior, guarantees, operator responsibilities, and user workflows.

## Proposed information architecture

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
│   ├── GitHub App setup
│   ├── Platform LLM
│   ├── Production deployment checklist
│   └── Worker capacity tuning
└── Reference
    ├── Repo config schema
    ├── Preview config
    ├── Environment variables
    └── Webhooks
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
- Use screenshots, diagrams, or UI callouts where visual state matters.
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
