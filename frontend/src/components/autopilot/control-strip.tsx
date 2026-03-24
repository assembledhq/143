"use client";

import { RefreshCw } from "lucide-react";
import { Button } from "@/components/ui/button";
import { AgentStatusBar } from "@/components/autopilot/agent-status-bar";
import { deriveAgentStatus } from "@/components/autopilot/agent-status-bar";
import type { PMStatus } from "@/lib/types";

// Re-export for consumers that imported from here.
export { deriveAgentStatus } from "@/components/autopilot/agent-status-bar";

interface ControlStripProps {
  pmStatus: PMStatus | undefined;
  isAnalyzing: boolean;
  isPending: boolean;
  onAnalyze: () => void;
  analyzeError: string | null;
  dismissError: () => void;
}

export function ControlStrip({ pmStatus, isAnalyzing, isPending, onAnalyze, analyzeError, dismissError }: ControlStripProps) {
  const agentStatus = deriveAgentStatus(pmStatus, isAnalyzing);

  return (
    <div className="space-y-2">
      <AgentStatusBar
        label="Autopilot"
        pmStatus={pmStatus}
        agentStatus={agentStatus}
      >
        <Button
          size="sm"
          className="h-7 text-[12px]"
          onClick={onAnalyze}
          disabled={isPending || isAnalyzing}
          title="Run the PM agent now without waiting for the next scheduled run"
        >
          <RefreshCw className={`mr-1 h-3 w-3 ${isPending || isAnalyzing ? "animate-spin" : ""}`} />
          {isPending ? "Starting..." : isAnalyzing ? "Running..." : "Run now"}
        </Button>
      </AgentStatusBar>

      {analyzeError && (
        <div className="flex items-center justify-between rounded-md bg-red-50 dark:bg-red-950/30 px-3 py-2">
          <p className="text-xs text-red-700 dark:text-red-300">{analyzeError}</p>
          <button onClick={dismissError} className="text-xs text-red-500 hover:text-red-700 ml-2">dismiss</button>
        </div>
      )}
    </div>
  );
}
