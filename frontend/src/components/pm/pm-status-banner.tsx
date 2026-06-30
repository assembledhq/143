"use client";

import { useQuery } from "@tanstack/react-query";
import { RefreshCw, Plus } from "lucide-react";
import Link from "next/link";
import { Button } from "@/components/ui/button";
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
            <Button
              size="sm"
              className="h-7 text-xs"
              onClick={handleAnalyze}
              disabled={isPending || isAnalyzing}
              title="Run the PM agent now without waiting for the next scheduled run"
            >
              <RefreshCw className={`mr-1 h-3 w-3 ${isPending || isAnalyzing ? "animate-spin" : ""}`} />
              {isPending ? "Starting..." : isAnalyzing ? "Running..." : "Run now"}
            </Button>
          </>
        )}
      </AgentStatusBar>

      {analyzeError && (
        <ErrorNotice title={analyzeError} onDismiss={dismissError} />
      )}
    </div>
  );
}
