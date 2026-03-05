export type IntegrationKey = "github" | "sentry" | "linear";

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
    description: "Sync repositories and open PRs.",
    logoSrc: "https://logo.clearbit.com/github.com",
  },
  {
    key: "sentry",
    name: "Sentry",
    description: "Pull errors and auto-generate fixes.",
    logoSrc: "https://logo.clearbit.com/sentry.io",
  },
  {
    key: "linear",
    name: "Linear",
    description: "Sync issues and auto-assign fixes.",
    logoSrc: "https://logo.clearbit.com/linear.app",
  },
];

export function getIntegrationByKey(key: IntegrationKey): IntegrationDefinition {
  const integration = INTEGRATIONS.find((integrationDefinition) => integrationDefinition.key === key);
  if (!integration) {
    throw new Error(`missing integration definition for key: ${key}`);
  }
  return integration;
}
