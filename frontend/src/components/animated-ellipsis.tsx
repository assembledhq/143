import { cn } from "@/lib/utils";

/**
 * Three dots that fade in one at a time, then fade out together.
 * Inherits its color from the surrounding text via `currentColor`.
 * Honors `prefers-reduced-motion` (renders a static "...").
 */
export function AnimatedEllipsis({ className }: { className?: string }) {
  return (
    <span aria-hidden="true" className={cn("inline-flex", className)}>
      <span className="ellipsis-dot ellipsis-dot-1">.</span>
      <span className="ellipsis-dot ellipsis-dot-2">.</span>
      <span className="ellipsis-dot ellipsis-dot-3">.</span>
    </span>
  );
}
