"use client";

import { useQuery } from "@tanstack/react-query";
import { RefreshCw, Plus } from "lucide-react";
import Link from "next/link";
import { Button } from "@/components/ui/button";
import { DisabledTooltip } from "@/components/ui/disabled-tooltip";
import { ErrorNotice } from "@/components/ui/error-notice";
import { api } from "@/lib/api";
import { useAnalyze } from "@/hooks/use-analyze";
import { AgentStatusBar, deriveAgentStatus } from "@/components/autopilot/agent-status-bar";

interface PMStatusBannerProps {
  hasActivePlanSession: boolean;
  canMutate?: boolean;
}

export function PMStatusBanner({ hasActivePlanSession, canMutate = true }: PMStatusBannerProps) {
  const { data: statusData } = useQuery({
    queryKey: ["pm", "status"],
    queryFn: () => api.pm.status(),
    refetchInterval: 15000,
  });

  const pmStatus = statusData?.data;
  const { isAnalyzing, isPending, analyzeError, handleAnalyze, dismissError } = useAnalyze(hasActivePlanSession);

  const agentStatus = deriveAgentStatus(pmStatus, isAnalyzing);
  const runNowDisabled = isPending || isAnalyzing;
  const runNowDisabledReason = isPending
    ? "Wait for the PM agent run to start."
    : isAnalyzing
      ? "Wait for the current PM agent run to finish."
      : undefined;

  return (
    <div className="space-y-2">
      <AgentStatusBar
        label="PM Agent"
        pmStatus={pmStatus}
        agentStatus={agentStatus}
      >
        {canMutate && (
          <>
            <Button size="sm" variant="outline" className="h-7 text-xs" asChild>
              <Link href="/sessions/new">
                <Plus className="mr-1 h-3 w-3" />
                Manual Session
              </Link>
            </Button>
            <DisabledTooltip
              disabled={runNowDisabled}
              content={runNowDisabledReason}
            >
              <Button
                size="sm"
                className="h-7 text-xs"
                onClick={handleAnalyze}
                disabled={runNowDisabled}
                title="Run the PM agent now without waiting for the next scheduled run"
              >
                <RefreshCw className={`mr-1 h-3 w-3 ${runNowDisabled ? "animate-spin" : ""}`} />
                {isPending ? "Starting..." : isAnalyzing ? "Running..." : "Run now"}
              </Button>
            </DisabledTooltip>
          </>
        )}
      </AgentStatusBar>

      {analyzeError && (
        <ErrorNotice title={analyzeError} onDismiss={dismissError} />
      )}
    </div>
  );
}
