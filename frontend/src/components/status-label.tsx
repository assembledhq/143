import type { ReactNode } from "react";
import { LoaderCircle } from "lucide-react";

import { cn } from "@/lib/utils";

export type StatusTone = "neutral" | "primary" | "success" | "warning" | "attention" | "info" | "destructive";

const toneClasses: Record<StatusTone, { dot: string; text: string }> = {
  neutral: { dot: "bg-muted-foreground/45", text: "text-muted-foreground" },
  primary: { dot: "bg-primary", text: "text-primary" },
  success: { dot: "bg-success", text: "text-success" },
  warning: { dot: "bg-warning", text: "text-warning" },
  attention: { dot: "bg-attention", text: "text-attention" },
  info: { dot: "bg-info", text: "text-info" },
  destructive: { dot: "bg-destructive", text: "text-destructive" },
};

type StatusLabelProps = {
  label: ReactNode;
  tone?: StatusTone;
  detail?: ReactNode;
  active?: boolean;
  className?: string;
};

export function StatusLabel({ label, tone = "neutral", detail, active = false, className }: StatusLabelProps) {
  const colors = toneClasses[tone];
  return (
    <span data-slot="status-label" className={cn("inline-flex min-w-0 items-center gap-1.5 type-dense", className)}>
      {active ? (
        <LoaderCircle data-slot="status-spinner" aria-hidden="true" className={cn("size-3.5 shrink-0 animate-spin", colors.text)} />
      ) : (
        <span aria-hidden="true" className={cn("size-1.5 shrink-0 rounded-full", colors.dot)} />
      )}
      <span className={cn("font-medium", colors.text)}>{label}</span>
      {detail ? <span className="truncate text-muted-foreground">{detail}</span> : null}
    </span>
  );
}
