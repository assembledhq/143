"use client";

import { AlertTriangle } from "lucide-react";
import { Button } from "@/components/ui/button";
import Link from "next/link";
import { AGENTS_BY_KEY } from "@/lib/agents";

export function AgentKeyRequiredBanner({ agentType }: { agentType: string }) {
  const agent = AGENTS_BY_KEY[agentType];
  const label = agent?.label ?? agentType;
  const href = !agent ? "/settings/agent" : agentType === "codex" ? "/settings/agent" : "/settings/account";

  return (
    <div className="flex items-center gap-3 rounded-lg border border-warning/20 bg-warning/5 px-4 py-3">
      <AlertTriangle className="h-4 w-4 shrink-0 text-warning" />
      <div className="flex-1 min-w-0">
        <p className="text-xs text-warning">
          No API key configured for {label}.{" "}
          <Link href={href} className="underline font-medium">Add one in Settings</Link>
          {" "}to use your own subscription. Org-wide credentials are used as a fallback.
        </p>
      </div>
      <Button size="sm" variant="outline" asChild className="shrink-0">
        <Link href={href}>Configure keys</Link>
      </Button>
    </div>
  );
}
