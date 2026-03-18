import { cn } from "@/lib/utils";

interface StatusDotProps {
  /** Whether to show the animated ping effect */
  animate?: boolean;
  /** Tailwind color class for the dot (e.g. "bg-blue-500", "bg-primary") */
  color: string;
  /** Tailwind color class for the ping ring; defaults to `color` with /60 opacity */
  pingColor?: string;
  /** Additional classes on the outer wrapper */
  className?: string;
}

export function StatusDot({ animate, color, pingColor, className }: StatusDotProps) {
  if (animate) {
    return (
      <span className={cn("relative flex h-2 w-2", className)}>
        <span className={cn("animate-ping absolute inline-flex h-full w-full rounded-full opacity-75", pingColor ?? `${color}/60`)} />
        <span className={cn("relative inline-flex rounded-full h-2 w-2", color)} />
      </span>
    );
  }

  return <span className={cn("inline-flex rounded-full h-2 w-2", color, className)} />;
}
