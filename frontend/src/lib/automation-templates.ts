import type React from "react";
import {
  ClipboardList,
  FileCheck,
  FileText,
  FlaskConical,
  Gauge,
  Shield,
  TestTube2,
  Waypoints,
  Wrench,
} from "lucide-react";

export type AutomationTemplateCategoryID =
  | "reliability"
  | "security"
  | "maintenance"
  | "planning"
  | "documentation";

export interface AutomationTemplateCategory {
  id: AutomationTemplateCategoryID;
  name: string;
  description: string;
}

export interface AutomationTemplate {
  id: string;
  name: string;
  icon: React.ComponentType<{ className?: string }>;
  category: AutomationTemplateCategoryID;
  summary: string;
  goal: string;
  outcomes: string[];
  tags: string[];
  defaultInterval: number;
  defaultUnit: "hours" | "days" | "weeks";
  featured?: boolean;
}

export const automationTemplateCategories: AutomationTemplateCategory[] = [
  {
    id: "reliability",
    name: "Reliability",
    description: "Recurring sweeps that reduce operational risk, regressions, and production surprises.",
  },
  {
    id: "security",
    name: "Security",
    description: "Structured audits that look for concrete, exploitable issues instead of generic checklists.",
  },
  {
    id: "maintenance",
    name: "Maintenance",
    description: "Code health work that keeps the repo easier to change and less expensive to operate.",
  },
  {
    id: "planning",
    name: "Planning",
    description: "Templates for turning noisy inputs into prioritized, reviewable engineering work.",
  },
  {
    id: "documentation",
    name: "Documentation",
    description: "Prompts that keep specs, runbooks, and docs aligned with how the code actually works.",
  },
];

export const automationTemplates: AutomationTemplate[] = [
  {
    id: "flaky-tests",
    name: "Find flaky tests",
    icon: FlaskConical,
    category: "reliability",
    summary: "Investigate recent nondeterministic test failures, identify the real source of instability, and suggest the smallest durable fix.",
    goal: `What to do
- Start with CI/CD evidence before editing code when a CI provider exposes test metadata. Current GitHub PR tools can provide PR and review context, but do not expose flaky-test signals or check-run logs directly.
- If CircleCI evidence is available, prioritize its Test Insights flaky-test signal. CircleCI marks tests flaky when they both pass and fail on the same commit within a 14-day window; use that signal as evidence, then verify the likely root cause in the repo.
- Investigate recent test failures and identify the tests or suites that appear flaky rather than consistently broken.
- Reproduce the instability when possible, then trace it back to the underlying source: timing assumptions, shared state, ordering, random seeds, network dependency, or environment drift.
- Reuse existing testing patterns in this repository instead of introducing new abstractions unless they are clearly needed.

Output requirements
- Return a ranked list of the most credible flaky tests, including the evidence that made each one suspicious.
- Include the CI source for each candidate when available: provider, workflow/job/check name, PR or commit, failure signature, and whether the same test also passed on the same commit.
- For each item, explain the likely root cause, the smallest durable fix, and whether the fix belongs in test code, product code, or CI configuration.
- If nothing is convincingly flaky, say so explicitly and list the highest-signal follow-up checks worth scheduling next.

Verification
- Note which commands, suites, retries, or historical signals you used to validate the flake.
- Distinguish clearly between "reproduced", "high-confidence inference", and "possible but unconfirmed".
- Do not classify a test as flaky from one failed run alone. Look for same-commit pass/fail evidence, repeated nondeterministic signatures, or a successful local reproduction of the race/order dependency.
- Prefer fixes that make the test deterministic over increasing retries or widening timeouts.`,
    outcomes: [
      "Ranked flaky-test candidates with evidence",
      "Root-cause analysis instead of retry-based masking",
      "A small deterministic remediation plan",
    ],
    tags: ["tests", "ci", "reliability"],
    defaultInterval: 1,
    defaultUnit: "days",
    featured: true,
  },
  {
    id: "ci-failure-triage",
    name: "CI failure triage",
    icon: FileCheck,
    category: "reliability",
    summary: "Analyze failing pipelines and separate repo bugs, infra problems, and test instability so engineers can act quickly.",
    goal: `What to do
- Review the latest failing CI runs for this repository and identify the highest-signal failures blocking developer velocity.
- Separate deterministic code regressions from flaky tests, missing environment setup, dependency outages, or infra noise.
- Look for repeated patterns across jobs instead of treating every failure as isolated.

Output requirements
- Produce a short failure digest grouped by root cause category.
- For each group, list the failing step, probable cause, and the smallest next action to restore signal.
- Highlight anything that should be escalated outside the repo, such as shared CI infrastructure or third-party outages.

Verification
- Reference the exact failing job names, commands, or error signatures that support each conclusion.
- Avoid proposing code changes when the evidence points to infra or environment issues instead.
- Mark uncertain classifications clearly rather than forcing false precision.`,
    outcomes: [
      "Grouped CI failures by root cause",
      "Smallest next action for each failure class",
      "Clear separation between repo fixes and external issues",
    ],
    tags: ["ci", "build", "triage"],
    defaultInterval: 12,
    defaultUnit: "hours",
  },
  {
    id: "security-sweep",
    name: "Security sweep",
    icon: Shield,
    category: "security",
    summary: "Review the repo for concrete, exploitable security issues and propose minimal, evidence-backed remediations.",
    goal: `What to do
- Review the repository for concrete, actionable security risk introduced by recent changes or currently reachable in production-facing paths.
- Start with the highest-risk surfaces: authentication, authorization, secret handling, input validation, file access, network boundaries, unsafe parsing, and data exposure.
- Reuse the repository's existing security patterns where they exist; do not invent broad framework churn.

Output requirements
- Produce a ranked list of the highest-confidence findings with severity, affected files or flows, and a short exploit path.
- For each finding, propose the smallest credible remediation and note whether it can be addressed safely in a focused follow-up.
- If no real vulnerability is found, say that explicitly and instead list the most valuable hardening opportunities worth tracking.

Verification
- Verify each finding against actual code paths you inspected; do not infer vulnerabilities from naming alone.
- Reference the code, config, tests, or commands that support the finding.
- Prefer concrete evidence over generic checklist advice, and ignore theoretical issues without a plausible exploit path here.`,
    outcomes: [
      "Evidence-backed security findings",
      "Minimal remediation guidance for each issue",
      "A clean distinction between real bugs and hardening ideas",
    ],
    tags: ["security", "auth", "risk"],
    defaultInterval: 7,
    defaultUnit: "days",
    featured: true,
  },
  {
    id: "dependency-drift",
    name: "Dependency drift review",
    icon: Waypoints,
    category: "security",
    summary: "Check whether key dependencies are drifting into unsupported, vulnerable, or operationally risky states and suggest prioritized upgrades.",
    goal: `What to do
- Review the repository's important runtime and build dependencies for meaningful upgrade risk, security exposure, or support drift.
- Focus on dependencies that affect production correctness, security posture, or day-to-day developer workflow.
- Compare the repo's current versions against the surrounding code patterns so upgrade advice fits the actual implementation.

Output requirements
- Identify the most important dependencies to review first and explain why they matter.
- For each one, describe the likely benefit, upgrade risk, and whether the next step should be a patch bump, planned migration, or deliberate defer.
- Avoid noisy laundry lists; prioritize the few changes with the highest expected payoff.

Verification
- Use repository manifests, lockfiles, and code usage patterns as your primary evidence.
- If a recommendation depends on external release notes or advisories, state that clearly as outside context.
- Do not recommend upgrades that would obviously conflict with the current codebase without noting the migration cost.`,
    outcomes: [
      "Prioritized dependency risks",
      "Upgrade recommendations sized to the repo",
      "Clear defer-vs-upgrade tradeoffs",
    ],
    tags: ["dependencies", "upgrades", "security"],
    defaultInterval: 2,
    defaultUnit: "weeks",
  },
  {
    id: "codebase-maintenance",
    name: "Codebase maintenance",
    icon: Wrench,
    category: "maintenance",
    summary: "Look for small, high-leverage cleanup work that lowers future change cost without creating broad churn.",
    goal: `What to do
- Identify high-leverage maintenance opportunities that would make this repository safer and easier to change.
- Favor issues that have clear downstream impact: duplicated logic, brittle boundaries, stale abstractions, oversized modules, or poor local testability.
- Prefer small, reviewable improvements over broad rewrites or taste-based refactors.

Output requirements
- Return a short backlog of the best maintenance opportunities, ranked by leverage.
- For each item, explain the specific pain it reduces, the likely scope, and whether it is safe for an incremental change.
- Include one recommended candidate for the next automation run if a clear winner emerges.

Verification
- Anchor each recommendation in the codebase as it exists today, with file-level evidence where helpful.
- Avoid style-only or framework-fashion suggestions unless they remove a concrete source of risk.
- Keep the backlog specific enough that an engineer could turn each item into a scoped issue.`,
    outcomes: [
      "A ranked maintenance backlog",
      "Clear leverage and scope per item",
      "One recommended next task",
    ],
    tags: ["refactor", "cleanup", "maintainability"],
    defaultInterval: 3,
    defaultUnit: "days",
    featured: true,
  },
  {
    id: "dead-code-cleanup",
    name: "Dead code cleanup",
    icon: Wrench,
    category: "maintenance",
    summary: "Find unused code paths, stale feature flags, and obsolete helpers that now add confusion more than value.",
    goal: `What to do
- Search for code that is likely obsolete, unreachable, or retained only out of inertia.
- Focus on dead branches, stale feature flags, unused helpers, duplicate migration paths, and abandoned compatibility layers.
- Be conservative: removal candidates should be backed by strong evidence, not guesswork.

Output requirements
- List the strongest dead-code candidates and explain why each one appears safe to remove or simplify.
- Call out any dependencies, rollout concerns, or hidden references that should be checked before deletion.
- Suggest the smallest cleanup slice that could land safely in one follow-up change.

Verification
- Base your reasoning on search results, call sites, routing paths, configs, and tests in this repository.
- Clearly mark anything that still has ambiguous references.
- Do not recommend deleting code if the evidence for non-use is weak.`,
    outcomes: [
      "A conservative dead-code candidate list",
      "Risk notes for each removal",
      "A safe first cleanup slice",
    ],
    tags: ["cleanup", "deletion", "simplification"],
    defaultInterval: 2,
    defaultUnit: "weeks",
  },
  {
    id: "backlog-triage",
    name: "Backlog triage",
    icon: ClipboardList,
    category: "planning",
    summary: "Cluster active work, surface the highest-leverage items, and convert a noisy queue into an opinionated engineering order.",
    goal: `What to do
- Analyze the current backlog, open issues, and recently discussed engineering work for this repository.
- Group related items, remove obvious duplicates, and distinguish urgent reliability/security work from routine housekeeping.
- Optimize for what the team should actually do next, not for a perfectly labeled spreadsheet.

Output requirements
- Produce a prioritized list of issue clusters with a short rationale for each rank.
- For the top items, explain user impact, engineering urgency, and any dependency ordering that matters.
- Call out work that should be deferred, merged, or closed if it does not justify active attention.

Verification
- Use the issue descriptions, linked context, and repository state as your evidence base.
- Avoid fabricated precision in scoring; concise, defensible reasoning is better.
- Make sure the highest-ranked items are concrete enough to turn into execution work immediately.`,
    outcomes: [
      "A prioritized backlog by cluster",
      "De-duplication and defer guidance",
      "Immediate next work for the team",
    ],
    tags: ["planning", "triage", "prioritization"],
    defaultInterval: 1,
    defaultUnit: "days",
    featured: true,
  },
  {
    id: "documentation-freshness",
    name: "Documentation freshness",
    icon: FileText,
    category: "documentation",
    summary: "Find docs that drifted from the code and update the highest-value pages engineers actually rely on.",
    goal: `What to do
- Compare the most important developer-facing docs against the current code, workflows, and repository structure.
- Focus on docs people rely on to work safely: onboarding guides, architecture notes, operational runbooks, and setup instructions.
- Prefer updating or flagging the few highest-value stale docs over broad prose churn.

Output requirements
- Identify which docs are stale, missing, or misleading and explain the mismatch.
- Propose the exact updates needed, or note when a doc should be archived instead of expanded.
- Highlight one or two pages that deserve immediate attention because they directly affect engineering speed or correctness.

Verification
- Check the code, commands, paths, and current workflows before declaring a doc stale.
- Quote file paths, commands, or interfaces that changed when relevant.
- Avoid writing speculative documentation for systems you could not verify.`,
    outcomes: [
      "A shortlist of stale or missing docs",
      "Concrete update guidance per document",
      "One or two highest-priority doc fixes",
    ],
    tags: ["docs", "runbooks", "onboarding"],
    defaultInterval: 7,
    defaultUnit: "days",
    featured: true,
  },
  {
    id: "api-contract-audit",
    name: "API contract audit",
    icon: FileCheck,
    category: "documentation",
    summary: "Compare implemented API behavior against docs and typed clients so contract drift is visible before it breaks consumers.",
    goal: `What to do
- Audit the most important API surfaces in this repository for drift between implementation, docs, generated clients, and consumer expectations.
- Focus on response shapes, status codes, validation rules, pagination, auth requirements, and enum values.
- Prioritize mismatches that would break callers or mislead integrators.

Output requirements
- List the highest-signal contract mismatches and explain which source of truth appears correct.
- Propose the smallest fix path for each mismatch: server change, documentation update, client update, or explicit versioning note.
- Flag any gaps where the contract is ambiguous and should be clarified rather than guessed.

Verification
- Base findings on actual handlers, types, client code, and published docs in this repository.
- Distinguish clearly between current behavior and intended behavior when both are visible.
- Avoid speculative API design advice that is not tied to an observable mismatch.`,
    outcomes: [
      "A list of real contract mismatches",
      "A fix path for each mismatch",
      "Clarifications where the contract is ambiguous",
    ],
    tags: ["api", "contracts", "docs"],
    defaultInterval: 2,
    defaultUnit: "weeks",
  },
  {
    id: "test-coverage-gaps",
    name: "Test coverage for recent features",
    icon: TestTube2,
    category: "reliability",
    summary: "Identify recently shipped features that lack tests and add only the small number of tests that would actually catch real regressions.",
    goal: `What to do
- Look at features, endpoints, or modules added or substantially changed recently in this repository that do not yet have meaningful test coverage.
- Be extremely conservative about what deserves a new test. Only target code where a missing test would plausibly let a real, user-visible bug ship: core business logic, tricky conditionals, data transformations, auth boundaries, error handling, or known past regressions.
- Skip trivial wrappers, straightforward getters/setters, UI glue with no branching, generated code, and anything already covered indirectly by existing tests. When in doubt, leave it alone.
- Reuse the existing test style, frameworks, helpers, and fixtures in this repository instead of inventing new patterns. Match the surrounding testing conventions exactly.

Output requirements
- Return a short list of the highest-leverage coverage gaps, with the file or feature, what it does, and the specific risk a test would catch.
- Propose only the minimum number of tests worth writing. Prefer a few focused tests over broad coverage sweeps; do not generate tests just to raise coverage numbers.
- For each proposed test, give a concrete description (inputs, expected behavior, and why it matters). Draft the test if it is obviously safe and small; otherwise flag it for human review.
- If recent changes already appear adequately tested, say that plainly and add nothing.

Verification
- Confirm each gap against the current code and the existing test files before proposing anything new. Do not propose tests for behavior you did not actually read.
- Reject candidates whose "test" would just re-assert the implementation or lock in incidental details. Tests should describe behavior an engineer would care about preserving.
- Call out any proposed test that depends on external services, flaky timing, or unstable fixtures, and prefer to omit it rather than introduce a brittle test.`,
    outcomes: [
      "A short, high-signal list of real coverage gaps",
      "Only the tests worth writing, with clear justification",
      "Drafts that follow existing repo testing conventions",
    ],
    tags: ["tests", "coverage", "quality"],
    defaultInterval: 1,
    defaultUnit: "weeks",
  },
  {
    id: "performance-regression",
    name: "Performance regression sweep",
    icon: Gauge,
    category: "reliability",
    summary: "Look for newly expensive paths, repeated slow operations, or avoidable work that could explain recent latency or throughput drift.",
    goal: `What to do
- Review recent code paths and known hot spots for credible performance regressions or wasteful work.
- Focus on repeated database access, unnecessary network calls, expensive rendering, redundant parsing, and unbounded loops or scans.
- Optimize for issues with measurable impact rather than micro-optimizations.

Output requirements
- Identify the most likely performance regressions with the code path and the expected impact surface.
- For each one, propose the smallest practical optimization or measurement step.
- If the code alone is insufficient to prove a regression, say what telemetry or benchmark would close the gap.

Verification
- Anchor findings in actual code behavior and workload assumptions that fit this repository.
- Avoid generic performance advice without a concrete hot path.
- Separate confirmed inefficiencies from hypotheses that still need measurement.`,
    outcomes: [
      "Likely high-impact regressions",
      "Smallest optimization or measurement step",
      "Clear evidence vs hypothesis labeling",
    ],
    tags: ["performance", "latency", "profiling"],
    defaultInterval: 1,
    defaultUnit: "weeks",
  },
];

export const featuredAutomationTemplateIDs = automationTemplates
  .filter((template) => template.featured)
  .map((template) => template.id);

export function getAutomationTemplate(id: string): AutomationTemplate | undefined {
  return automationTemplates.find((template) => template.id === id);
}

export function getAutomationTemplatesByCategory(
  categoryID: AutomationTemplateCategoryID,
): AutomationTemplate[] {
  return automationTemplates.filter((template) => template.category === categoryID);
}
