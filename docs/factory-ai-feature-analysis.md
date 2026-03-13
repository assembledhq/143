# Factory AI Feature Analysis — Opportunities for 143

_Date: 2026-03-13_

## Overview

Factory AI is an **agent-native software development platform** built by The San Francisco
AI Factory Inc. (founded 2023 by Matan Grinberg and Eno Reyes). It uses autonomous AI
agents called "Droids" to automate the entire software development lifecycle. Funded ~$70M
(Sequoia, NEA, NVIDIA, J.P. Morgan). Valued at $300M as of September 2025.

Enterprise customers include MongoDB, Ernst & Young (5,000+ engineers), Zapier, Bayer, and
Bilt Rewards. Reported 200% quarter-over-quarter growth throughout 2025.

---

## Factory AI Key Features

### 1. Specialized Droid Agents

Factory uses pre-built, purpose-specific agents rather than a general-purpose coding assistant:

| Droid | Purpose |
|---|---|
| **Code Droid** | Feature development, refactoring, bug fixes, migrations |
| **Reliability Droid** | On-call triage, production alert handling, root cause analysis, incident documentation |
| **Product Droid** | Backlog management, ticket prioritization, Slack threads → product specs |
| **Knowledge Droid** | Engineering research, technical docs, onboarding guides, codebase Q&A |
| **Tutorial Droid** | Onboarding new users to Factory itself |
| **Custom Droids** | User-defined subagents with custom prompts, tool access, and model selection |

### 2. Custom Droids (Subagent Framework)

Users can create their own Droids with:
- **Custom prompts** — encode complex checklists once, reuse with a single command
- **Scoped tool access** — read-only, edit-only, or curated tool sets per agent
- **Context isolation** — each subagent gets a fresh context window, avoiding prompt bloat
- **Repeatable workflows** — team-specific review, testing, or release gates as versionable code

### 3. Persistent Memory (Three Layers)

Factory's memory system propagates knowledge across sessions and teams:

1. **Personal Memory** (`~/.factory/memories.md`) — individual preferences, style, tool choices
2. **Project Memory** (`.factory/memories.md`) — architecture decisions, history, codebase context
3. **Org Memory** — team-wide conventions that propagate to every developer's Droid automatically
4. **Rules & Conventions** (`.factory/rules/`) — codified standards and patterns
5. **AGENTS.md** — orchestrator file connecting all memory layers

### 4. Multi-Model Architecture

- **LLM-agnostic**: GPT-5, Claude Opus/Sonnet, Gemini 2.5 Pro, o3 — all under one subscription
- **Model routing**: Different models dispatched to different subtasks based on task characteristics
- **Key insight**: Their agent framework enables cheaper models (Sonnet) to outperform expensive
  models (Opus) on competing platforms — framework > model

### 5. HyperCode & ByteRank (Context Engineering)

- **HyperCode**: Codebase understanding system
- **ByteRank**: Code-specific RAG with multi-resolution representations (architecture-level
  down to implementation-level)
- **Lazy context loading**: Only pulls context when necessary to avoid prompt bloat
- Build outputs, test results, and execution feedback captured as additional context

### 6. Multi-Interface Access

Same Droids accessible from:
- VS Code / JetBrains / Vim (IDE extensions)
- Factory CLI (terminal)
- Factory App (web)
- Slack (conversational)
- Linear (ticket-native)
- Programmatic scripts (CI/CD)

### 7. Enterprise Integrations

Native connections to: GitHub, GitLab, Jira, Slack, PagerDuty, Sentry, Notion, Google Drive,
Datadog. Droids see the same data human developers see.

### 8. Multi-Trajectory Planning

- Generates **multiple solution trajectories** for a given task
- Validates trajectories using existing AND **self-generated tests**
- Selects optimal solution from the ensemble
- Explicit plan-and-track tool that marks completed steps (leverages LLM attention recency bias)

### 9. Scripted Parallelization

Run Droids at scale: batch migrations, CI/CD automation, maintenance tasks across repos.
Parallel execution with orchestration.

### 10. Fail-Fast Architecture

Short default timeouts that fail fast on unproductive paths, with explicit opt-in for longer
timeouts. Counterintuitively improves average performance by cutting wasted compute.

---

## Feature Comparison: 143 vs. Factory AI

| Feature Area | 143 | Factory AI | Assessment |
|---|---|---|---|
| Issue ingestion (Sentry, Linear) | Yes | Via integrations (Sentry, PagerDuty) | **143 ahead** — deeper, native ingestion pipeline |
| AI PM agent / batch planning | Yes (holistic, scheduled) | Product Droid (lighter) | **143 ahead** — more sophisticated analysis |
| Composite prioritization scoring | Yes (multi-factor algorithm) | No (delegated to Product Droid) | **143 ahead** — algorithmic, not just LLM |
| Multi-stage validation gates | Yes (6-stage pipeline) | Basic (PR review) | **143 ahead** — significantly more rigorous |
| Feedback loop / learned conventions | Yes (PR review → learned conventions) | Yes (3-layer persistent memory) | **Factory ahead** — more structured memory |
| Deployment observation | Yes | No | **143 ahead** |
| Multi-agent provider support | Yes (Claude, Codex, Gemini) | Yes (GPT-5, Claude, Gemini, o3) | Comparable |
| Complexity estimation | Yes (5-tier LLM-based) | No explicit system | **143 ahead** |
| Direction alignment checking | Yes (LLM-based product fit) | No | **143 ahead** |
| Specialized non-coding agents | PM agent only | Yes (Reliability, Product, Knowledge) | **Gap** |
| Custom/user-defined agents | No | Yes (Custom Droids) | **Gap** |
| Multi-layer persistent memory | Partial (learned conventions) | Yes (Personal/Project/Org) | **Gap** |
| On-call / incident response | No | Yes (Reliability Droid) | **Gap** |
| Multi-trajectory planning | No | Yes (ensemble selection) | **Gap** |
| Code-specific RAG (ByteRank) | No | Yes (multi-resolution) | **Gap** |
| Multi-interface (IDE, CLI, Web, Slack) | Web UI only | All surfaces | **Gap** |
| Fail-fast architecture | No explicit system | Yes (short timeouts) | **Gap** |
| Custom agent tool scoping | No | Yes (per-Droid permissions) | **Gap** |
| Scripted parallelization / batch ops | Partial (PM batch scheduling) | Yes (full orchestration) | **Gap** |

---

## Recommended Features to Add to 143

### Tier 1 — High Impact, Strong Fit

#### 1. Reliability Droid / On-Call Agent
Factory's Reliability Droid handles production alerts, root cause analysis, and incident
documentation. 143 already ingests Sentry errors — extending this to handle PagerDuty/Datadog
alerts and produce incident reports (not just code fixes) would be a natural evolution. Think:
Sentry error → root cause analysis → incident doc + code fix PR.

**Why it fits**: 143 already has the issue ingestion pipeline. Adding incident response
capabilities would expand from "fix the bug" to "understand the incident, document it, AND
fix the bug."

#### 2. Custom Agent Templates (Custom Droids)
Factory lets users define their own agents with scoped tools and prompts. 143 could let
users define reusable "agent profiles" with:
- Custom system prompts (e.g., "You are a security-focused reviewer")
- Scoped tool access (read-only for review agents, full access for coding agents)
- Model selection per profile
- Saved as versionable config files in the repo

**Why it fits**: This is the natural evolution of 143's existing multi-agent support. Instead
of just choosing between Claude/Codex/Gemini, users define *specialized personas*.

#### 3. Multi-Layer Persistent Memory
Factory's 3-layer memory (Personal → Project → Org) is more structured than 143's learned
conventions. 143 should formalize:
- **Project memory**: Architecture decisions, tech stack, key patterns (auto-generated + editable)
- **Org memory**: Team conventions that apply to all repos (naming conventions, review standards)
- **Session memory**: What the agent learned in this specific run (already partially exists)

**Why it fits**: 143 already has learned conventions from PR feedback. Structuring this into
layers would make the feedback loop significantly more powerful and debuggable.

#### 4. Multi-Trajectory Planning (Ensemble Selection)
Factory generates multiple solution approaches, validates each, and picks the best. 143
currently generates one plan. Adding:
- Generate 2-3 candidate approaches (e.g., "quick fix" vs. "proper refactor")
- Run validation on each candidate
- Select the one that passes the most gates / has highest confidence
- Present alternatives to user for manual override

**Why it fits**: 143 already has a robust validation pipeline. Running it against multiple
candidates instead of one dramatically increases success rates with modest compute overhead.

### Tier 2 — Medium Impact, Worth Exploring

#### 5. Fail-Fast Timeout Architecture
Factory uses short default timeouts that cut unproductive paths early. 143 could add:
- Per-phase timeouts (planning phase: 2 min, coding phase: 10 min, validation: 5 min)
- Automatic re-planning if a phase exceeds timeout
- Configurable per complexity tier (trivial gets shorter timeouts)

**Why it fits**: Simple to implement, immediate impact on efficiency. Prevents the agent
from spending 30 minutes on a dead-end approach.

#### 6. Code-Specific RAG (ByteRank-style)
Factory's ByteRank provides multi-resolution code retrieval. 143 could build:
- Architecture-level summaries (module relationships, data flow)
- File-level summaries (purpose, key functions, dependencies)
- Implementation-level details (function signatures, logic patterns)
- Indexed and searchable across the codebase

**Why it fits**: 143 already generates "context packages" for repos. Adding structured
multi-resolution indexing would dramatically improve the quality of context fed to agents.

#### 7. Multi-Interface Support (CLI + Slack)
Factory's agents are accessible from IDE, CLI, Web, Slack, and Linear. 143 is currently
web-only. Adding:
- **CLI**: `143 run --issue SENTRY-1234` for scriptable automation
- **Slack**: `@143 fix this` in a thread with an error screenshot

**Why it fits**: CLI unlocks CI/CD integration and developer workflow automation. Slack
unlocks adoption by meeting developers where they already work.

#### 8. Knowledge Droid Equivalent
Factory's Knowledge Droid generates docs, onboarding guides, and answers codebase questions.
143 could add a "documentation mode" that:
- Auto-generates architecture docs from codebase analysis
- Answers natural language questions about the codebase
- Produces onboarding guides for new team members

**Why it fits**: Adjacent to the codebase understanding 143 already builds for context packages.

### Tier 3 — Nice to Have

#### 9. Agent Tool Scoping / Permissions
Letting admins restrict what tools specific agent runs can access (read-only, no git push,
no external API calls). Useful for high-security environments.

#### 10. Scripted Batch Operations
Running the same agent task across multiple repos or multiple issues in parallel with
orchestration. E.g., "update this dependency across all 20 microservices."

---

## Pricing Comparison

| | Factory AI | Devin AI | 143 |
|---|---|---|---|
| Free tier | Yes (20M tokens) | No | Open source |
| Entry price | $20/month (Pro) | $20/month (Core) | Self-hosted (free) |
| Premium | $200/month (Max) | $500/month (Teams) | N/A |
| Enterprise | Custom | Custom | N/A |
| Model | Per-seat + usage | Per-seat + usage | BYOK (bring your own keys) |

143's open-source, BYOK model is a significant advantage for cost-conscious teams and
enterprises that want data sovereignty.

---

## Strategic Summary

Factory AI's strength is **breadth across the SDLC** — specialized agents for coding,
reliability, product, and knowledge, accessible from every surface (IDE, CLI, Web, Slack).
Their Custom Droids framework and multi-layer memory system enable teams to codify workflows.

143's strength is **depth in the issue-to-PR pipeline** — sophisticated ingestion, multi-factor
prioritization, PM analysis, 6-stage validation, and learning feedback loops. This is deeper
than anything Factory offers for the specific workflow of "production issue → validated fix."

The biggest gaps are:
1. **Custom agent templates** — letting users define specialized agent personas
2. **Structured persistent memory** — formalizing the learning loop into Personal/Project/Org layers
3. **Multi-trajectory planning** — generating and validating multiple approaches
4. **Reliability/incident response** — expanding beyond "fix the code" to "understand the incident"
5. **Multi-interface access** — CLI and Slack for scriptability and adoption

---

## Sources

- [Factory AI - Official Site](https://factory.ai)
- [Droid: #1 on Terminal-Bench](https://factory.ai/news/terminal-bench)
- [Factory is GA: Droids for the Entire SDLC](https://factory.ai/news/factory-is-ga)
- [Factory.ai Guide (Sid Bharath)](https://www.siddharthbharath.com/factory-ai-guide/)
- [Factory.ai: Autonomous Software Development (ZenML)](https://www.zenml.io/llmops-database/autonomous-software-development-using-multi-model-llm-system-with-advanced-planning-and-tool-integration)
- [Factory AI Review (eesel.ai)](https://www.eesel.ai/blog/factory-ai)
- [Factory.ai Review 2026 (Fritz AI)](https://fritz.ai/factory-ai-review/)
- [Factory + OpenAI (OpenAI)](https://openai.com/index/factory/)
- [Vibe Check: Factory's Coding Agent Droid (Every)](https://every.to/vibe-check/vibe-check-i-canceled-two-ai-max-plans-for-factory-s-coding-agent-droid)
- [Memory Management - Factory Docs](https://docs.factory.ai/guides/power-user/memory-management)
- [Custom Droids - Factory Docs](https://docs.factory.ai/cli/configuration/custom-droids)
- [Factory Droids AI Agents (Developer Tech)](https://www.developer-tech.com/news/factory-droids-ai-agents-tackle-entire-development-lifecycle/)
- [Factory.ai: The A-SWE Droid Army (Latent Space)](https://www.latent.space/p/factory)
