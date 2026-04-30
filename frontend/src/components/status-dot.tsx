import { cn } from "@/lib/utils";

type StatusDotProps = {
  /** Tailwind color class for the dot (e.g. "bg-blue-500", "bg-primary") */
  color: string;
  /** Additional classes on the outer wrapper */
  className?: string;
} & (
  | { animate?: false; pingColor?: never }
  | { animate: true; pingColor: string }
);

export function StatusDot({ animate, color, pingColor, className }: StatusDotProps) {
  if (animate) {
    return (
      <span className={cn("relative flex h-2 w-2", className)}>
        {/* Soft breathing halo — slower and softer than animate-ping so it
            reads as ongoing thought rather than a network heartbeat. */}
        <span
          className={cn(
            "ai-pulse-halo absolute inline-flex h-full w-full rounded-full",
            pingColor,
          )}
        />
        {/* Solid base dot in the requested color, with a slow gradient
            shimmer overlay for the "AI thinking" feel. */}
        <span className={cn("relative inline-flex h-2 w-2 overflow-hidden rounded-full", color)}>
          <span className="ai-shimmer absolute inset-0 rounded-full" />
        </span>
      </span>
    );
  }

  return <span className={cn("inline-flex rounded-full h-2 w-2", color, className)} />;
}
