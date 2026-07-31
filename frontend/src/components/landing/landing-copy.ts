export const codeReviewSummary = {
  step: "01",
  kicker: "Code review",
  heading: "Code review that approves the pull requests it should.",
  body: "Your team can use anything to open a pull request. Reviewing it is the bottleneck. Request 143 Code Reviewer and several coding agents review the PR in parallel. If the change passes your policy, 143 approves it on GitHub with the evidence attached. If not, it leaves inline findings and the reason a human is needed.",
};

export const codeReviewControls = [
  "Tune thresholds, sensitive paths, and required checks",
  "Raise the auto-approval rate by tightening policy",
  "Choose reviewer models: Codex, Claude Code, OpenCode",
  "Set reasoning depth per reviewer to control cost",
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
    body: "Codex, Claude Code, OpenCode, Amp, and Pi each run in an isolated cloud sandbox. Connect an agent once and anyone on the team can start a run from the web, Slack, Linear, or Sentry.",
    components: [
      "One isolated sandbox per run",
      "Team-visible sessions and transcripts",
      "Cheaper models for routine work",
      "Existing subscriptions used before metered billing",
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
