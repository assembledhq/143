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
    description: "Connect your GitHub account to sync repositories and open PRs.",
  },
  {
    key: "sentry",
    name: "Sentry",
    description: "Pull production errors and auto-generate fixes.",
  },
  {
    key: "linear",
    name: "Linear",
    description: "Sync issues from Linear and auto-assign fixes.",
  },
];
