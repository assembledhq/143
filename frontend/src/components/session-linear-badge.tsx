import { cn } from "@/lib/utils";

export function SessionLinearBadge({
  label,
  className,
}: {
  label?: string | null;
  className?: string;
}) {
  const trimmed = label?.trim();
  if (!trimmed) return null;

  return (
    <span
      className={cn(
        "inline-flex shrink-0 rounded-md border border-border/60 bg-muted/50 px-1.5 py-0.5 text-xs text-muted-foreground",
        className
      )}
    >
      {trimmed}
    </span>
  );
}
