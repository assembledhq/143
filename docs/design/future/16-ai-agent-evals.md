# Design: AI Agent Evaluation System

> **Status:** Not Started | **Last reviewed:** 2026-04-21

## Overview

This document defines the evaluation system for 143.dev. It replaces people/process guidance with a strict focus on measurable agent quality and automated improvement loops.

Goal: every change to prompts, models, context assembly, routing, validators, or tools is evaluated before rollout and monitored continuously after rollout.

## Core Principles

1. Eval-driven development: define tests before shipping behavior changes.
2. Use both offline and online evals; either one alone is insufficient.
3. Combine deterministic graders with LLM-as-judge graders.
4. Calibrate automated grading against periodic expert review.
5. Version datasets, graders, configs, and prompts for reproducibility.
6. Optimize for anti-gaming: passing evals must require solving the real task.
7. Evaluate traces, not just final outputs, to localize failures.

## Eval Taxonomy

Run evals at four levels:

1. **Task outcome evals**
- Did the issue get correctly fixed?
- Were required tests added/updated?
- Did CI pass under project constraints?

2. **Trace evals**
- Was the plan coherent?
- Were tool calls valid and efficient?
- Did the run loop or thrash?

3. **Policy evals**
- Were restricted paths/operations avoided?
- Were security and data-handling constraints respected?

4. **Impact evals**
- Did production outcomes improve after deploy?
- Did regressions/rollback rates stay within thresholds?

## Dataset Strategy

Build three dataset buckets per repo:

1. **Golden set (stable regression suite)**
- Curated tasks with known expected behavior.
- Includes easy, medium, and hard tasks.
- Never used for prompt/model tuning data generation.

2. **Shadow set (recent production-derived)**
- Sampled from recent real issue traffic.
- Updated weekly to track distribution shift.
- Used for pre-rollout checks and drift detection.

3. **Adversarial set**
- Prompt injection attempts, malformed tool outputs, flaky test environments, ambiguous issue descriptions, long-horizon tasks.
- Expanded from every escaped failure.

Dataset requirements:
- Tag by issue type, complexity, risk class, repo area, and expected tooling.
- Keep a strict holdout slice for final release gates.
- Record provenance for every example (source system and date).

Private data boundary:
- Keep real company eval inputs out of git.
- Store private eval examples in tenant-scoped database tables with encrypted payload fields.
- Keep only synthetic/public fixtures in the open-source repository.

## Grading Architecture

Use a mixed grader stack:

1. **Deterministic graders (first priority)**
- executable checks (tests, linters, compile)
- schema and format checks
- exact/constraint checks (required files touched, forbidden files untouched)

2. **LLM-as-judge graders (for nuanced criteria)**
- minimal diff quality
- adherence to style/conventions
- root-cause alignment

LLM grading best practices:
- Use dimension-specific rubrics, not one broad rubric.
- Allow explicit `unknown`/`insufficient_evidence` outputs.
- Calibrate with periodic human labels and track agreement.
- Use pairwise comparison when judging alternative outputs.

## Trace-Centric Evaluation

Store and grade full run traces:
- plan nodes
- tool calls and arguments
- retries and branch decisions
- validation failures and recoveries

Trace graders should emit structured failure codes, for example:
- `context_missing`
- `wrong_file_scope`
- `tool_arg_invalid`
- `test_strategy_insufficient`
- `policy_violation`

This enables targeted fixes (prompt, tool schema, context map, or validator rule) instead of generic reruns.

## Statistical Rigor

Agent outcomes are stochastic; single-run scores are misleading.

Required reporting:
- pass@1 and pass@k (k >= 3 for hard tasks)
- confidence intervals for key metrics
- per-slice metrics (by issue type/complexity/risk)
- change deltas versus last production baseline

Avoid:
- cherry-picking best-of-N without disclosure
- mixing pass@1 and pass@k in the same headline metric
- benchmark-only optimization with no production validation

## Release Gates and Rollout

Every meaningful config change (prompt/model/context/routing/validator) must pass:

1. Golden set gate (hard minimum thresholds)
2. Shadow set gate (no significant regression on recent traffic)
3. Adversarial gate (no new high-severity escapes)
4. Canary online gate (staged traffic rollout with rollback triggers)

Recommended staged rollout:
- 10% traffic -> 30% -> 100%
- automatic rollback if quality/reliability thresholds are violated

## Prompt Overrides and Tenant Configuration

Per-org prompt overrides are supported and evaluated with the same gate system.

Prompt layering:
1. `global_default` prompt (shipped by 143.dev, read-only)
2. `org_override` prompt (optional, tenant-specific)
3. `runtime_context` (issue/repo/run context)

Prompt scope and resolution:
- scopes: `global`, `repository`, `issue_type`, `phase`, and combinations
- deterministic precedence: most specific scope -> less specific -> global default
- every run stores `prompt_version_id` and `effective_prompt_hash`

Prompt lifecycle:
- states: `draft`, `candidate`, `active`, `archived`
- only `active` versions receive production traffic
- promotion to `active` requires passing release gates
- rollback is pointer-based and atomic

## Private Storage Model (No New Dependency)

Default storage is Postgres to avoid mandatory external services in v1.

What lives in OSS repo:
- harness code
- graders
- schema contracts
- synthetic/public fixtures

What lives in private tenant data:
- org eval datasets and examples from internal/company data
- encrypted payload fields (for example `input_encrypted`, `expected_output_encrypted`, `ground_truth_encrypted`)
- minimal plaintext metadata for indexing (`tags`, source refs, timestamps)

## API and UI Surface

API groups:
- `/api/v1/prompts/*` for template/version/submit/promote/rollback
- `/api/v1/evals/*` for datasets, examples, runs, results, and release gates

Settings UI sections:
- `Prompts`: override editor, scope controls, default-vs-override diff, version history
- `Evals`: dataset management, eval run history, failure drilldowns
- `Rollouts`: canary stages and gate thresholds

## CI Split for Privacy

- Public CI (OSS): runs only synthetic/public fixture evals.
- Internal/private runner: runs full org datasets and enforces promotion gates.
- Prompt promotions to `active` must be approved by internal gate outcomes.

## Continuous Online Evaluation

Run online evals on production traces continuously:
- score sampled runs automatically
- bucket failures by code and frequency
- detect drift in issue mix and outcome quality
- create remediation tasks automatically for recurrent failure modes

Track:
- first-pass PR acceptance rate
- rework-after-review rate
- post-merge defect/rollback rate
- time-to-fix and time-to-impact

## Anti-Overfitting and Benchmark Hygiene

Protect eval integrity:
- separate benchmark development data from final test sets
- rotate fresh production-derived tasks into shadow/adversarial sets
- periodically audit grader correctness and harness constraints
- verify that passing requires real task completion, not exploiting rubric loopholes

## Data Flywheel

Automatically convert failures into better eval coverage:

1. Production or review failure occurs.
2. Failure is classified into a structured code.
3. New/updated eval case is added to shadow or adversarial set.
4. Gates are rerun on candidate fixes.
5. Once stable, case can be promoted into golden set.

This creates compounding reliability over time.

## Implementation Order

1. Define eval schema (datasets, grader outputs, failure codes).
2. Launch golden/shadow/adversarial dataset pipeline.
3. Implement deterministic + LLM-judge grader stack with calibration jobs.
4. Add trace graders and failure taxonomy integration.
5. Add prompt layering + org override resolver.
6. Add private dataset ingestion and encrypted payload handling.
7. Enforce release gates and staged canary rollback automation.
8. Add automatic failure-to-eval ingestion for the flywheel.

## Connections to Existing Design Docs

- [06-agent-orchestrator.md](06-agent-orchestrator.md): trace capture and execution metadata.
- [07-validation.md](07-validation.md): deterministic executable checks.
- [09-observability.md](../backlog/09-observability.md): online outcomes and post-deploy impact.
- [11-review-feedback-loop.md](../backlog/11-review-feedback-loop.md): review failures as eval inputs.
- [12-smart-routing.md](../backlog/12-smart-routing.md): slice metrics by complexity and issue type.
- [14-codebase-context.md](14-codebase-context.md): context-quality failures and remediation.
- [01-database-schema.md](01-database-schema.md): prompt/eval tables and encrypted example fields.
- [02-api-server.md](02-api-server.md): prompt/eval endpoints, jobs, and scheduler hooks.
- [03-frontend.md](03-frontend.md): Prompts/Evals/Rollouts settings UX.
- [20-security-architecture.md](../implemented/20-security-architecture.md): tenant isolation, encryption, and redaction controls.

## External Practices Incorporated (Reviewed Feb 16, 2026)

- OpenAI, "Evaluation best practices":
  https://platform.openai.com/docs/guides/evaluation-best-practices
- OpenAI, "Eval Driven System Design - From Prototype to Production":
  https://cookbook.openai.com/examples/partners/eval_driven_system_design/receipt_inspection
- OpenAI, "Trace grading":
  https://platform.openai.com/docs/guides/trace-grading
- Anthropic Engineering, "Demystifying evals for AI agents":
  https://www.anthropic.com/engineering/demystifying-evals-for-ai-agents
- Anthropic Docs, "Using the Evaluation Tool":
  https://docs.anthropic.com/en/docs/test-and-evaluate/eval-tool
- LangSmith Docs, "Evaluation":
  https://docs.langchain.com/langsmith/evaluation
- SWE-bench Docs, "Evaluation Guide":
  https://www.swebench.com/SWE-bench/guides/evaluation/
