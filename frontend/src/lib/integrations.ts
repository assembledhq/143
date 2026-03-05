export type IntegrationKey = "github" | "sentry" | "linear";

export type IntegrationDefinition = {
  key: IntegrationKey;
  name: string;
  description: string;
};

export const INTEGRATIONS: IntegrationDefinition[] = [
  {
    key: "github",
    name: "GitHub",
    description: "Sync repositories and open PRs.",
  },
  {
    key: "sentry",
    name: "Sentry",
    description: "Pull errors and auto-generate fixes.",
  },
  {
    key: "linear",
    name: "Linear",
    description: "Sync issues and auto-assign fixes.",
  },
];
