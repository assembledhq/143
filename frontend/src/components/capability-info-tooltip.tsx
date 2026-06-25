"use client";

import { CircleHelp } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from "@/components/ui/tooltip";
import type { AgentCapabilityAccessLevel, AgentCapabilityDefinition, AgentCapabilityID, AgentCapabilityRisk } from "@/lib/types";

// Richer, plain-language explanations surfaced in the info tooltip beside each
// capability. These replace the inline risk/category badges with context that
// actually helps an admin decide whether to enable the capability.
const CAPABILITY_DETAILS: Record<AgentCapabilityID, string> = {
  repo_context:
    "Reads your repository's code, docs, and configuration so the agent's work is grounded in how your codebase actually behaves.",
  pr_history:
    "Looks at recent pull requests, review threads, and merge conventions so the agent follows your team's established patterns.",
  session_history:
    "Lets the agent learn from prior 143 sessions in this org and repository instead of starting every task from scratch.",
  review_feedback:
    "Surfaces past review comments and learned feedback so the agent avoids repeating mistakes your team has already flagged.",
  ci_history:
    "Gives the agent recent test failures and flaky-test evidence to help it diagnose and fix CI breakages.",
  issue_sources:
    "Pulls in Linear, Sentry, and support context for the issue being worked on so the agent understands what it's solving.",
  team_docs:
    "Reads Notion, Slack, architecture, and product docs for context that lives outside the codebase.",
  production_diagnostics:
    "Allows bounded, read-only access to production logs and error-tracker data when debugging live issues. Enable only when needed.",
  external_comments:
    "Lets the agent post comments and status updates to Linear and Slack on your behalf.",
  slack_notifications:
    "Lets the agent send Slack completion and status notifications through the connected 143 Slack app.",
  automation_management:
    "Lets the agent create, update, pause, resume, and run repo-scoped automations. Enable only for trusted setup or maintenance work.",
  project_proposals:
    "Lets the agent draft and create planning documents and project proposals.",
  eval_authoring:
    "Lets the agent create eval candidates that can influence how future agents are graded. High-impact — keep off unless you trust the source.",
  publishing:
    "Lets the agent open branches and pull requests through 143 workflows — the main output of most coding sessions.",
};

const RISK_LABEL: Record<AgentCapabilityRisk, string> = {
  low: "Low risk",
  medium: "Medium risk",
  high: "High risk",
};

const ACCESS_LABEL: Record<AgentCapabilityAccessLevel, string> = {
  read: "Read-only",
  write: "Can make changes",
  publish: "Can publish branches & PRs",
};

export function CapabilityInfoTooltip({ definition }: { definition: AgentCapabilityDefinition }) {
  const detail = CAPABILITY_DETAILS[definition.id] ?? definition.description;
  return (
    <TooltipProvider delayDuration={150}>
      <Tooltip>
        <TooltipTrigger asChild>
          <Button
            type="button"
            variant="ghost"
            size="icon"
            className="h-5 w-5 rounded-full text-muted-foreground hover:text-foreground"
            aria-label={`About ${definition.display_name}`}
          >
            <CircleHelp className="h-3.5 w-3.5" />
          </Button>
        </TooltipTrigger>
        <TooltipContent side="top" sideOffset={6} className="max-w-72 space-y-1.5">
          <p>{detail}</p>
          <p className="text-xs text-background/70">
            {RISK_LABEL[definition.risk]} · {ACCESS_LABEL[definition.max_access_level]}
          </p>
        </TooltipContent>
      </Tooltip>
    </TooltipProvider>
  );
}
