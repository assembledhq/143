# Homepage Positioning Refresh

> **Status:** Implemented | **Last reviewed:** 2026-05-19

The homepage should position 143 as shared coding-agent infrastructure for engineering teams, not as an individual coding assistant. The core message is that team context, cloud execution, review controls, integrations, auditability, and usage analytics belong in one shared workspace.

## Design Direction

- Use an editorial landing-page rhythm: centered declarative hero, numbered sections, layered platform explanation, integrations, and CTA.
- Keep the existing custom agent visuals, but make the copy clearer about what the product does and why team defaults matter.
- Lead with "Open source coding agents for teams" and keep the hero description short, with Codex and Claude Code as the primary named agents.
- Explain the platform in focused sections, numbered after the opening "Why this matters" section:
  - Team context: shared setup, sessions, automations, prompts, integrations, and roles.
  - Cloud execution: Codex, Claude Code, Gemini CLI, and future agents running in observable sandboxes with previews.
  - Review control: fix loops, audit logs, usage analytics, and builder safeguards before code reaches human review.
- Give cloud previews their own section between review control and the broader workspace view.
- Keep integrations in their own section, using the real logo assets already bundled with the frontend.
- Animate supporting bullet lists with staggered scroll-in motion so dense details feel progressive rather than dumped onto the page at once.
- Keep homepage product copy on a small shared type scale: hero title, hero body, section title, feature title, body text, eyebrow labels, buttons, and footer links. Compact mock-interface text can stay on `text-xs`/`text-sm` so the product visuals read like application chrome.
- Do not describe commercial terms until the product has a finalized model.

## Copy Principles

- Lead with the team problem: individual coding tools fragment setup, history, and context.
- Use concrete nouns from the product: sessions, automations, autopilot, previews, PRs, audit logs, usage analytics, integrations.
- Avoid vague leverage claims unless they are tied to a visible product mechanism.
