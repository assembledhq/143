export const codeReviewSummary = {
  step: "01",
  kicker: "Code review",
  heading: "Code review that approves the pull requests it should.",
  body: "Request 143 Code Reviewer on a pull request and several coding agents review it in parallel. When the change clears your policy and no blocking finding remains, 143 submits a real GitHub approval with the evidence attached. When it does not, the pull request comes back with inline findings and the specific reason a human is still needed.",
};

export const codeReviewOutcomes = [
  {
    title: "Reviews finish in minutes",
    body: "Assessment starts the moment 143 is requested as a reviewer, and every new commit is reviewed again automatically until the pull request is approved. Nobody has to schedule a review or chase one down.",
  },
  {
    title: "Policy is enforced, not remembered",
    body: "Size thresholds, sensitive paths, migrations, auth, billing, dependency changes, required checks, and description requirements are evaluated on every pull request the same way — instead of depending on who happens to be reviewing that day.",
  },
  {
    title: "Review stops blocking the merge",
    body: "Acceptable-risk changes are approved on the spot instead of waiting in someone's queue, so finished work reaches main while reviewers keep their attention for the changes that need judgment.",
  },
];

export const codeReviewCapabilities = [
  "Requested like any other GitHub reviewer",
  "Reviewer agents run their native /review in parallel",
  "Approval needs evidence, not a model's opinion",
  "P0 and P1 findings block approval and post inline",
  "One versioned policy across every repository",
  "Every decision links to its session and commit",
];

export const codeReviewApproval = {
  title: "143 Code Reviewer approved this PR",
  decision: "Approved",
  evidence: [
    { label: "Risk", value: "Acceptable" },
    { label: "Description", value: "Passed" },
    { label: "Review agents", value: "Codex clean, Claude Code clean" },
    { label: "Required checks", value: "CI green" },
    { label: "Changed", value: "4 files, 96 lines" },
    { label: "Sensitive paths", value: "None touched" },
  ],
  footer: "Policy v12 · reviewed a91f3c2 · evidence kept in the review session",
};

export const codeReviewEscalation = {
  title: "143 Code Reviewer did not approve this PR",
  decision: "Needs human review",
  reasons: [
    "Auth-sensitive paths changed",
    "Description is missing a testing strategy",
    "P1 finding posted inline on src/auth/session.go:88",
  ],
};

export const platformLayers = [
  {
    step: "03",
    kicker: "Context",
    title: "Team context",
    heading: "Shared context for every run.",
    body: "Repos, issues, docs, prompts, automations, sessions, and outcomes live in one team workspace. Every agent starts from the same context.",
    components: [
      "Shared prompts and automations",
      "Team-visible sessions and history",
      "One integration setup per organization",
      "Builder and engineer roles",
    ],
  },
  {
    step: "04",
    kicker: "Execution",
    title: "Cloud execution",
    heading: "Run agents from anywhere.",
    body: "Start Codex, Claude Code, OpenCode, and other coding agents from web, mobile, Slack, Linear, or Sentry. Runs happen in cloud sandboxes your team can follow.",
    components: [
      "Codex, Claude Code, OpenCode, and more",
      "Cloud sandboxes with previews",
      "Mobile-friendly job controls",
      "Sessions from issues and errors",
    ],
  },
  {
    step: "05",
    kicker: "Control",
    title: "Repair loops",
    heading: "Arrive at review already clean.",
    body: "Before a reviewer is ever requested, agents repair failing tests, respond to feedback, and iterate inside guardrails, so the pull request that reaches code review is one that can pass it.",
    components: [
      "PR review and repair loops",
      "Usage and cost analytics",
      "Audit logs for sensitive changes",
      "Safeguards for builder workflows",
    ],
  },
  {
    step: "06",
    kicker: "Previews",
    title: "Cloud previews",
    heading: "Preview every change in the cloud.",
    body: "Every agent change can launch a browser preview directly from its cloud sandbox, so teammates can inspect behavior before code reaches a PR.",
    components: [
      "Shareable preview links",
      "Preview status in the session",
      "Browser checks before review",
      "No local setup required",
    ],
  },
];

export const codingAgents = [
  {
    name: "Codex",
    logo: "/agents/codex.svg",
  },
  {
    name: "Claude Code",
    logo: "/agents/claude_code.svg",
  },
  {
    name: "OpenCode",
    logo: "/agents/opencode.svg",
  },
  {
    name: "Amp",
    logo: "/agents/amp.svg",
  },
  {
    name: "Pi",
    logo: "/agents/pi.svg",
  },
];

export const agentChoiceHighlights = [
  {
    title: "Use the best agent for the job",
    body: "Run top-tier tools like Codex, Claude Code, and OpenCode when the task needs maximum capability.",
  },
  {
    title: "Keep routine work economical",
    body: "Route lighter jobs through OpenCode and open-source models when cost matters more than peak reasoning.",
  },
  {
    title: "Stack subscriptions before metered spend",
    body: "Layer personal, team, and bundled coding-agent subscriptions so available seats are used before extra usage piles up.",
  },
];

export const integrations = [
  {
    name: "GitHub",
    logo: "/integrations/github.svg",
    description: "Repos, branches, pull requests, and review state.",
  },
  {
    name: "Linear",
    logo: "/integrations/linear.svg",
    description: "Issues, projects, priorities, and automation triggers.",
  },
  {
    name: "Slack",
    logo: "/integrations/slack.svg",
    description: "Team notifications and job kickoff from conversation.",
  },
  {
    name: "Sentry",
    logo: "/integrations/sentry.svg",
    description: "Errors, traces, stack context, and production signals.",
  },
  {
    name: "PagerDuty",
    logo: "/integrations/pagerduty.svg",
    description: "Incidents and on-call alerts that kick off a fix.",
  },
  {
    name: "Notion",
    logo: "/integrations/notion.svg",
    description: "Product notes, specs, runbooks, and team knowledge.",
  },
  {
    name: "CircleCI",
    logo: "/integrations/circleci.svg",
    description: "Build status, failing jobs, and repair-loop feedback.",
  },
  {
    name: "Mezmo",
    logo: "/integrations/mezmo.svg",
    description: "Production log search and signals from Mezmo.",
  },
];
