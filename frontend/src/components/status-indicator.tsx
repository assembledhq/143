"use client";

import { LoaderCircle } from "lucide-react";
import { useEffect, useRef } from "react";

import type { ActivityTreatment, OperationalTone } from "@/lib/operational-state";
import { cn } from "@/lib/utils";

const toneClasses: Record<OperationalTone, { dot: string; text: string }> = {
  neutral: { dot: "bg-muted-foreground/45", text: "text-muted-foreground" },
  primary: { dot: "bg-primary", text: "text-primary" },
  success: { dot: "bg-success", text: "text-success" },
  warning: { dot: "bg-warning", text: "text-warning" },
  attention: { dot: "bg-attention", text: "text-attention" },
  info: { dot: "bg-info", text: "text-info" },
  destructive: { dot: "bg-destructive", text: "text-destructive" },
};

const sizeClasses = {
  sm: { wrapper: "size-2", dot: "size-1.5", spinner: "size-3" },
  md: { wrapper: "size-2.5", dot: "size-2", spinner: "size-3.5" },
} as const;

export type StatusIndicatorProps = {
  tone?: OperationalTone;
  activity?: ActivityTreatment;
  size?: keyof typeof sizeClasses;
  /** Include a domain status key when multiple statuses share tone/activity. */
  stateKey?: string;
  className?: string;
};

export function StatusIndicator({
  tone = "neutral",
  activity = "none",
  size = "sm",
  stateKey,
  className,
}: StatusIndicatorProps) {
  const signature = `${stateKey ?? ""}:${tone}:${activity}`;
  const previousSignature = useRef(signature);
  const coreRef = useRef<HTMLSpanElement>(null);

  useEffect(() => {
    if (previousSignature.current === signature) return;
    previousSignature.current = signature;

    // Failures should land immediately. Other state changes receive one short
    // settling cue, while persistent activity is owned by its own treatment.
    if (tone === "destructive" || window.matchMedia?.("(prefers-reduced-motion: reduce)").matches) return;
    coreRef.current?.animate?.(
      [
        { transform: "scale(0.72)", opacity: 0.35 },
        { transform: "scale(1)", opacity: 1 },
      ],
      { duration: 180, easing: "cubic-bezier(0.16, 1, 0.3, 1)" },
    );
  }, [signature, tone]);

  const colors = toneClasses[tone];
  const dimensions = sizeClasses[size];

  if (activity === "indeterminate") {
    return (
      <LoaderCircle
        data-slot="status-indicator"
        data-activity={activity}
        aria-hidden="true"
        className={cn("status-indeterminate shrink-0", dimensions.spinner, colors.text, className)}
      />
    );
  }

  const showHalo = activity === "breathing";
  const showTransition = activity === "transitioning";

  return (
    <span
      data-slot="status-indicator"
      data-activity={activity}
      data-transitioning={showTransition ? "true" : undefined}
      aria-hidden="true"
      className={cn("relative inline-flex shrink-0 items-center justify-center", dimensions.wrapper, className)}
    >
      <span
        data-slot="status-indicator-halo"
        className={cn(
          "status-breathe-halo absolute rounded-full opacity-0",
          dimensions.dot,
          colors.dot,
          showHalo && "opacity-55",
        )}
      />
      <span
        ref={coreRef}
        data-slot="status-indicator-core"
        className={cn(
          "status-indicator-core relative rounded-full transition-[background-color,opacity,transform] duration-[var(--motion-state)] ease-[var(--motion-ease-out)]",
          dimensions.dot,
          colors.dot,
        )}
      />
    </span>
  );
}

export function statusToneTextClass(tone: OperationalTone): string {
  return toneClasses[tone].text;
}
