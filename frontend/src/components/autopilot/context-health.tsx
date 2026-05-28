"use client";

import { useState } from "react";
import { CheckCircle2, AlertTriangle, Circle } from "lucide-react";
import type { ProductContext, PMDocument } from "@/lib/types";

interface ContextHealthProps {
  productContext?: ProductContext;
  settingsUpdatedAt?: string;
  documents: PMDocument[];
}

function HealthIndicator({ set, label, detail }: { set: boolean; label: string; detail?: string }) {
  return (
    <div className="flex items-center gap-2 text-xs">
      {set ? (
        <CheckCircle2 className="h-3.5 w-3.5 text-emerald-600 dark:text-emerald-400 shrink-0" />
      ) : (
        <Circle className="h-3.5 w-3.5 text-muted-foreground/40 shrink-0" />
      )}
      <span className={set ? "text-foreground" : "text-muted-foreground"}>{label}</span>
      {detail && <span className="text-muted-foreground/60">{detail}</span>}
    </div>
  );
}

export function ContextHealth({ productContext, settingsUpdatedAt, documents }: ContextHealthProps) {
  const philosophySet = Boolean(productContext?.philosophy?.trim());
  const directionSet = Boolean(productContext?.direction?.trim());
  const focusCount = productContext?.focus_areas?.length ?? 0;
  const avoidCount = productContext?.avoid_areas?.length ?? 0;
  const docCount = documents.length;

  // Compute direction age — note: settingsUpdatedAt tracks any settings change,
  // not just direction updates, so this is an approximation.
  const [now] = useState(() => Date.now());
  let directionAge: string | undefined;
  if (settingsUpdatedAt) {
    const days = Math.floor(
      (now - new Date(settingsUpdatedAt).getTime()) / (1000 * 60 * 60 * 24)
    );
    if (days > 30) {
      directionAge = `${days}d ago`;
    }
  }

  // Compute overall score (0-1)
  let score = 0;
  if (philosophySet) score += 0.25;
  if (directionSet) score += 0.25;
  if (focusCount > 0) score += 0.25;
  if (docCount > 0) score += 0.25;

  const scoreLabel =
    score >= 0.75 ? "Healthy" : score >= 0.5 ? "Moderate" : "Needs attention";
  const scoreColor =
    score >= 0.75
      ? "text-emerald-600 dark:text-emerald-400"
      : score >= 0.5
        ? "text-amber-600 dark:text-amber-400"
        : "text-muted-foreground";

  return (
    <div className="rounded-md border border-border bg-surface-pane px-4 py-3 space-y-2">
      <div className="flex items-center justify-between">
        <span className="text-xs font-medium text-muted-foreground uppercase tracking-wider">Context health</span>
        <span className={`text-xs font-medium ${scoreColor}`}>{scoreLabel}</span>
      </div>
      <div className="space-y-1.5">
        <HealthIndicator set={philosophySet} label="Philosophy" detail={philosophySet ? "Active" : "Not set"} />
        <HealthIndicator
          set={directionSet}
          label="Direction"
          detail={
            !directionSet
              ? "Not set"
              : directionAge
                ? directionAge
                : "Active"
          }
        />
        {directionSet && directionAge && (
          <div className="flex items-center gap-2 text-xs ml-6">
            <AlertTriangle className="h-3 w-3 text-amber-500 shrink-0" />
            <span className="text-amber-600 dark:text-amber-400">Consider refreshing your direction</span>
          </div>
        )}
        <HealthIndicator
          set={focusCount > 0}
          label="Focus areas"
          detail={focusCount > 0 ? `${focusCount} set` : "None"}
        />
        <HealthIndicator
          set={avoidCount > 0}
          label="Avoid areas"
          detail={avoidCount > 0 ? `${avoidCount} set` : "None"}
        />
        <HealthIndicator
          set={docCount > 0}
          label="Documents"
          detail={docCount > 0 ? `${docCount} uploaded` : "Add more"}
        />
      </div>
    </div>
  );
}
