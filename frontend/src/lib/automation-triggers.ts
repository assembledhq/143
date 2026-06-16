import type { AutomationGitHubEvent } from "./types";

export type AutomationProductTrigger =
  | "github.pr.opened"
  | "github.pr.updated"
  | "github.pr.feedback"
  | "github.checks.completed"
  | "github.pr.merged";

export const prFeedbackGitHubEvents: AutomationGitHubEvent[] = [
  "github.issue_comment.created",
  "github.pull_request_review.submitted",
  "github.pull_request_review_comment.created",
];

const productTriggerEvents: Record<AutomationProductTrigger, AutomationGitHubEvent[]> = {
  "github.pr.opened": ["github.pull_request.opened"],
  "github.pr.updated": ["github.pull_request.updated"],
  "github.pr.feedback": prFeedbackGitHubEvents,
  "github.checks.completed": ["github.check_suite.completed"],
  "github.pr.merged": ["github.pull_request.merged"],
};

export const automationProductTriggerOptions: {
  value: AutomationProductTrigger;
  label: string;
}[] = [
  { value: "github.pr.opened", label: "When a PR is opened" },
  { value: "github.pr.updated", label: "When a PR is updated" },
  { value: "github.pr.feedback", label: "When there is new PR feedback" },
  { value: "github.checks.completed", label: "When checks finish" },
  { value: "github.pr.merged", label: "When a PR is merged" },
];

export function automationProductTriggersToGitHubEvents(triggers: AutomationProductTrigger[]): AutomationGitHubEvent[] {
  const seen = new Set<AutomationGitHubEvent>();
  const events: AutomationGitHubEvent[] = [];

  for (const trigger of triggers) {
    for (const event of productTriggerEvents[trigger]) {
      if (!seen.has(event)) {
        seen.add(event);
        events.push(event);
      }
    }
  }

  return events;
}

export function githubEventsToAutomationProductTriggers(events: AutomationGitHubEvent[]): AutomationProductTrigger[] {
  const eventSet = new Set(events);
  const triggers: AutomationProductTrigger[] = [];

  if (eventSet.has("github.pull_request.opened")) {
    triggers.push("github.pr.opened");
  }
  if (eventSet.has("github.pull_request.updated")) {
    triggers.push("github.pr.updated");
  }
  if (prFeedbackGitHubEvents.some((event) => eventSet.has(event))) {
    triggers.push("github.pr.feedback");
  }
  if (eventSet.has("github.check_suite.completed") || eventSet.has("github.check_run.completed")) {
    triggers.push("github.checks.completed");
  }
  if (eventSet.has("github.pull_request.merged")) {
    triggers.push("github.pr.merged");
  }

  return triggers;
}
