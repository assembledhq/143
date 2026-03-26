"use client";

import { Button } from "@/components/ui/button";

interface AutopilotControlStripProps {
  autonomyLabel: string;
  secondaryText: string;
  primaryActionLabel: string;
  onPrimaryAction: () => void;
}

export function AutopilotControlStrip({
  autonomyLabel,
  secondaryText,
  primaryActionLabel,
  onPrimaryAction,
}: AutopilotControlStripProps) {
  return (
    <div className="flex flex-col gap-4 rounded-2xl border border-border/70 bg-card/60 px-5 py-4 sm:flex-row sm:items-center sm:justify-between">
      <div className="space-y-1">
        <p className="text-sm font-medium text-foreground">{autonomyLabel}</p>
        <p className="text-sm text-muted-foreground">{secondaryText}</p>
      </div>
      <Button onClick={onPrimaryAction}>{primaryActionLabel}</Button>
    </div>
  );
}
