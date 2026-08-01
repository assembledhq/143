import type { ReactNode } from "react";

import { StatusIndicator, statusToneTextClass } from "@/components/status-indicator";
import type { ActivityTreatment, OperationalTone } from "@/lib/operational-state";
import { cn } from "@/lib/utils";

export type StatusTone = OperationalTone;

type StatusLabelProps = {
  label: ReactNode;
  tone?: StatusTone;
  detail?: ReactNode;
  activity?: ActivityTreatment;
  size?: "sm" | "md";
  /** Boolean values are retained temporarily for existing call sites. */
  indicator?: "dot" | "icon" | "none" | boolean;
  icon?: ReactNode;
  announcement?: "none" | "polite";
  stateKey?: string;
  /** @deprecated Use activity="indeterminate" for short-lived operations. */
  active?: boolean;
  className?: string;
};

export function StatusLabel({
  label,
  tone = "neutral",
  detail,
  activity = "none",
  size = "sm",
  indicator = "dot",
  icon,
  announcement = "none",
  stateKey,
  active = false,
  className,
}: StatusLabelProps) {
  const resolvedActivity = active ? "indeterminate" : activity;
  const resolvedIndicator = indicator === true ? "dot" : indicator === false ? "none" : indicator;
  return (
    <span
      data-slot="status-label"
      role={announcement === "polite" ? "status" : undefined}
      aria-live={announcement === "polite" ? "polite" : undefined}
      className={cn(
        "inline-flex min-w-0 items-center gap-1.5",
        size === "sm" ? "type-dense" : "text-sm",
        className,
      )}
    >
      {resolvedIndicator === "icon" && icon ? (
        <span aria-hidden="true" className="inline-flex shrink-0">{icon}</span>
      ) : resolvedIndicator === "dot" ? (
        <StatusIndicator tone={tone} activity={resolvedActivity} size={size} stateKey={stateKey} />
      ) : null}
      <span className={cn("font-medium", statusToneTextClass(tone))}>{label}</span>
      {detail ? <span className="truncate text-muted-foreground">{detail}</span> : null}
    </span>
  );
}
