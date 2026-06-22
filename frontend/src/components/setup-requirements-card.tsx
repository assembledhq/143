"use client";

import { Rocket } from "lucide-react";
import { NoReposWarning } from "@/components/no-repos-warning";
import { AgentKeyRequiredBanner } from "@/components/agent-key-required-banner";

interface SetupRequirementsCardProps {
  // The composer already computes both conditions from the resolved-credential
  // and repository queries, so this card stays purely presentational.
  showAgentRow: boolean;
  agentType: string;
  showRepoRow: boolean;
}

// SetupRequirementsCard collapses the previously separate "no API key" and
// "no repositories" warnings into a single calm card. Both items are advisory
// (a manual session can still be submitted without a repo, and the agent row
// only appears when no credential resolves), so the container uses neutral
// styling rather than stacking two alarm-colored banners on the build page.
export function SetupRequirementsCard({
  showAgentRow,
  agentType,
  showRepoRow,
}: SetupRequirementsCardProps) {
  if (!showAgentRow && !showRepoRow) return null;

  return (
    <div
      className="rounded-lg border border-border bg-muted/40 px-4 py-3"
      data-testid="setup-requirements-card"
    >
      <div className="flex items-center gap-2 text-xs font-medium text-muted-foreground">
        <Rocket className="h-3.5 w-3.5 shrink-0" />
        <span>Finish setting up</span>
      </div>
      <div className="mt-2 divide-y divide-border/60">
        {showAgentRow && (
          <div className="py-2 first:pt-0 last:pb-0">
            <AgentKeyRequiredBanner agentType={agentType} asRow />
          </div>
        )}
        {showRepoRow && (
          <div className="py-2 first:pt-0 last:pb-0">
            <NoReposWarning asRow />
          </div>
        )}
      </div>
    </div>
  );
}
