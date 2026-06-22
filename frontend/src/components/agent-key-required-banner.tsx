"use client";

import { AlertTriangle, Bot } from "lucide-react";
import { Button } from "@/components/ui/button";
import Link from "next/link";
import { AGENTS_BY_KEY } from "@/lib/agents";
import { SetupItemRow } from "@/components/setup-item-row";

export function AgentKeyRequiredBanner({
  agentType,
  // asRow renders the warning as a SetupItemRow inside SetupRequirementsCard so
  // it shares the onboarding cards' visual language. The default standalone
  // rendering is the inline warning banner.
  asRow = false,
}: {
  agentType: string;
  asRow?: boolean;
}) {
  const agent = AGENTS_BY_KEY[agentType];
  const label = agent?.label ?? agentType;
  const href = !agent ? "/settings/agent" : agentType === "codex" ? "/settings/agent" : "/settings/account";

  if (asRow) {
    return (
      <SetupItemRow
        icon={<Bot className="h-5 w-5" />}
        title="Coding agent"
        description={`${label} isn't connected yet — add a key or sign in so your sessions can run.`}
        action={
          <Button size="sm" variant="outline" asChild>
            <Link href={href}>Configure keys</Link>
          </Button>
        }
      />
    );
  }

  return (
    <div className="flex items-center gap-3 rounded-lg border border-warning/20 bg-warning/5 px-4 py-3">
      <AlertTriangle className="h-4 w-4 shrink-0 text-warning" />
      <div className="flex-1 min-w-0">
        <p className="text-xs text-warning">
          {label} isn&apos;t connected yet.{" "}
          <Link href={href} className="underline font-medium">Add a key or sign in</Link>
          {" "}so your sessions can run.
        </p>
      </div>
      <Button size="sm" variant="outline" asChild className="shrink-0">
        <Link href={href}>Configure keys</Link>
      </Button>
    </div>
  );
}
