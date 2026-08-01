import { cn } from "@/lib/utils";

/** @deprecated Migrate feature code to StatusIndicator with a semantic tone. */
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
        {/* Compatibility adapter for older feature call sites. New code uses
            StatusIndicator with a semantic tone instead of color classes. */}
        <span
          className={cn(
            "status-breathe-halo absolute inline-flex h-full w-full rounded-full opacity-55",
            pingColor,
          )}
        />
        <span className={cn("status-indicator-core relative inline-flex h-2 w-2 rounded-full", color)} />
      </span>
    );
  }

  return <span className={cn("inline-flex rounded-full h-2 w-2", color, className)} />;
}
