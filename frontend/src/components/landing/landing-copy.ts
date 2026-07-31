export const codeReviewSummary = {
  step: "01",
  kicker: "Code review",
  heading: "Code review that approves the pull requests it should.",
  body: "Your team can use anything to open a pull request — coding agents made that the easy part. Review is now the bottleneck. Request 143 Code Reviewer and several agents review the PR in parallel: when the change clears your policy, 143 submits a real GitHub approval with the evidence attached; when it does not, it comes back with inline findings and the specific reason a human is still needed.",
};

export const codeReviewControls = [
  "Approval thresholds, sensitive paths, and required checks are yours to tune",
  "Raise the auto-approval rate by tightening policy, not by trusting the model more",
  "Run one reviewer agent or several in parallel — Codex, Claude Code, OpenCode",
  "Set each reviewer's model and reasoning depth to balance cost and quality",
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
    step: "02",
    kicker: "Any agent",
    title: "Cloud agents",
    heading: "Run any coding agent.",
    body: "Codex, Claude Code, OpenCode, Amp, and Pi all run in isolated cloud sandboxes. Connect an agent's auth once for the organization, start runs from web, mobile, Slack, Linear, or Sentry, and match the agent to the task — top-tier capability where it matters, economical models for routine work.",
    components: [
      "One isolated cloud sandbox per run",
      "Team-visible sessions, transcripts, and history",
      "Start from web, mobile, Slack, Linear, or Sentry",
      "Stack subscriptions before metered spend",
    ],
  },
  {
    step: "03",
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
