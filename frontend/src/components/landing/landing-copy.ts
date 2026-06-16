export const platformLayers = [
  {
    step: "02",
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
    step: "03",
    kicker: "Execution",
    title: "Cloud execution",
    heading: "Run agents from anywhere.",
    body: "Start Codex, Claude Code, and other coding agents from web, mobile, Slack, Linear, or Sentry. Runs happen in cloud sandboxes your team can follow.",
    components: [
      "Codex, Claude Code, OpenCode, and more",
      "Cloud sandboxes with previews",
      "Mobile-friendly job controls",
      "Autopilot from issues and errors",
    ],
  },
  {
    step: "04",
    kicker: "Control",
    title: "Review control",
    heading: "Review loops before human review.",
    body: "Agents can repair failing tests, respond to review feedback, and iterate inside guardrails before a teammate has to step in.",
    components: [
      "PR review and repair loops",
      "Usage and cost analytics",
      "Audit logs for sensitive changes",
      "Safeguards for builder workflows",
    ],
  },
  {
    step: "05",
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

export const integrations = [
  {
    name: "GitHub",
    logo: "/integrations/github.svg",
    description: "Repos, branches, pull requests, and review state.",
  },
  {
    name: "Linear",
    logo: "/integrations/linear.svg",
    description: "Issues, projects, priorities, and autopilot triggers.",
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
    name: "Notion",
    logo: "/integrations/notion.svg",
    description: "Product notes, specs, runbooks, and team knowledge.",
  },
  {
    name: "CircleCI",
    logo: "/integrations/circleci.svg",
    description: "Build status, failing jobs, and repair-loop feedback.",
  },
];
