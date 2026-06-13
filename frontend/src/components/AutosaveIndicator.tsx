"use client";

import { AlertCircle, Check, Loader2 } from "lucide-react";
import { cn } from "@/lib/utils";
import type { AutosaveStatus } from "@/hooks/useAutosave";

interface AutosaveIndicatorProps {
  status: AutosaveStatus;
  className?: string;
}

const STATUS_COPY: Record<Exclude<AutosaveStatus, "idle">, string> = {
  saving: "Saving…",
  saved: "Saved",
  error: "Couldn't save",
};

/**
 * Small status chip for autosave surfaces. Render one per page (or per
 * logically distinct save scope) next to the heading it describes.
 *
 * Reserves both vertical and horizontal space even when idle so adjacent
 * content does not reflow on status transitions. `min-w` is sized to fit
 * the longest copy variant ("Couldn't save" + icon). Uses `aria-live="polite"`
 * so screen readers announce transitions without interrupting the user.
 */
export function AutosaveIndicator({ status, className }: AutosaveIndicatorProps) {
  return (
    <span
      role="status"
      aria-live="polite"
      className={cn(
        "inline-flex min-h-5 min-w-[6.5rem] items-center gap-1.5 text-xs text-muted-foreground",
        className,
      )}
    >
      {status === "saving" && (
        <>
          <Loader2 className="size-3 animate-spin" aria-hidden="true" />
          <span>{STATUS_COPY.saving}</span>
        </>
      )}
      {status === "saved" && (
        <>
          <Check className="size-3 text-success" aria-hidden="true" />
          <span>{STATUS_COPY.saved}</span>
        </>
      )}
      {status === "error" && (
        <>
          <AlertCircle className="size-3 text-destructive" aria-hidden="true" />
          <span className="text-destructive">{STATUS_COPY.error}</span>
        </>
      )}
    </span>
  );
}
