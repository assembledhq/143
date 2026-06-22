"use client";

import type { ReactNode } from "react";
import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/utils";

interface SetupItemRowProps {
  // A lucide icon element shown in the leading tile. Sized by the tile.
  icon: ReactNode;
  title: string;
  description?: ReactNode;
  // descriptionTone keeps status copy calm by default; use "destructive" for
  // genuine errors (e.g. a failed sync).
  descriptionTone?: "muted" | "destructive";
  // action is the right-aligned control (a button or link). When omitted and
  // done is true, a "Connected" badge is shown instead.
  action?: ReactNode;
  done?: boolean;
}

// SetupItemRow is the shared presentation for a single setup step. It mirrors
// the onboarding integration/agent cards (leading rounded icon tile, title,
// muted description, trailing action) so the build-page SetupRequirementsCard
// and the /onboarding checklist read as one system rather than two.
export function SetupItemRow({
  icon,
  title,
  description,
  descriptionTone = "muted",
  action,
  done = false,
}: SetupItemRowProps) {
  return (
    <div className="flex items-center gap-3">
      <div className="flex h-9 w-9 shrink-0 items-center justify-center rounded-lg bg-muted/50 text-muted-foreground ring-1 ring-border/50 dark:bg-white/5">
        {icon}
      </div>
      <div className="min-w-0 flex-1">
        <p className="text-sm font-medium text-foreground">{title}</p>
        {description && (
          <p
            className={cn(
              "text-xs",
              descriptionTone === "destructive" ? "text-destructive" : "text-muted-foreground",
            )}
          >
            {description}
          </p>
        )}
      </div>
      {action ? (
        <div className="shrink-0">{action}</div>
      ) : done ? (
        <Badge variant="secondary" className="shrink-0">Connected</Badge>
      ) : null}
    </div>
  );
}
