"use client";

import { AlertTriangle } from "lucide-react";
import { Button } from "@/components/ui/button";
import Link from "next/link";
import { AGENTS_BY_KEY } from "@/lib/agents";

export function AgentKeyRequiredBanner({ agentType }: { agentType: string }) {
  const agent = AGENTS_BY_KEY[agentType];
  const label = agent?.label ?? agentType;
  const href = !agent ? "/settings/agent" : agentType === "codex" ? "/settings/agent" : "/settings/account";
  const message = `No API key configured for ${label}. Add your key in Settings to start sessions.`;

  return (
    <div className="flex items-center gap-3 rounded-lg border border-amber-500/20 bg-amber-500/5 px-4 py-3">
      <AlertTriangle className="h-4 w-4 shrink-0 text-amber-600 dark:text-amber-400" />
      <div className="flex-1 min-w-0">
        <p className="text-xs text-amber-700 dark:text-amber-300">{message}</p>
      </div>
      <Button size="sm" variant="outline" asChild className="shrink-0">
        <Link href={href}>Configure keys</Link>
      </Button>
    </div>
  );
}
