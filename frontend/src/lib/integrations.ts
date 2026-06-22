export type IntegrationKey = "github" | "sentry" | "linear" | "slack" | "notion" | "circleci" | "mezmo" | "pagerduty";

export type IntegrationDefinition = {
  key: IntegrationKey;
  name: string;
  description: string;
  logoSrc: string;
};

export const INTEGRATIONS: IntegrationDefinition[] = [
  {
    key: "github",
    name: "GitHub",
    description: "Install the 143 app so it can access your repositories and open PRs.",
    logoSrc: "/integrations/github.svg",
  },
  {
    key: "sentry",
    name: "Sentry",
    description: "Pull errors and auto-generate fixes.",
    logoSrc: "/integrations/sentry.svg",
  },
  {
    key: "linear",
    name: "Linear",
    description: "Sync issues and auto-assign fixes.",
    logoSrc: "/integrations/linear.svg",
  },
  {
    key: "slack",
    name: "Slack",
    description: "Monitor channels for actionable conversations.",
    logoSrc: "/integrations/slack.svg",
  },
  {
    key: "notion",
    name: "Notion",
    description: "Sync product docs, roadmaps, and knowledge base.",
    logoSrc: "/integrations/notion.svg",
  },
  {
    key: "circleci",
    name: "CircleCI",
    description: "Surface flaky tests so agents can investigate and fix them.",
    logoSrc: "/integrations/circleci.svg",
  },
  {
    key: "mezmo",
    name: "Mezmo",
    description: "Query and search log data from Mezmo.",
    logoSrc: "/integrations/mezmo.svg",
  },
  {
    key: "pagerduty",
    name: "PagerDuty",
    description: "Trigger incident response automation from PagerDuty events.",
    logoSrc: "/integrations/pagerduty.svg",
  },
];

export function getIntegrationByKey(key: IntegrationKey): IntegrationDefinition {
  const integration = INTEGRATIONS.find((integrationDefinition) => integrationDefinition.key === key);
  if (!integration) {
    throw new Error(`missing integration definition for key: ${key}`);
  }
  return integration;
}
