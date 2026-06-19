import { cn } from "@/lib/utils";

interface DiffStatsBadgeProps {
  added: number;
  removed: number;
  onClick?: () => void;
  className?: string;
}

export function DiffStatsBadge({ added, removed, onClick, className }: DiffStatsBadgeProps) {
  if (added === 0 && removed === 0) return null;

  const content = (
    <span className={cn("inline-flex items-center gap-1 text-xs font-mono", className)}>
      <span className="text-success">+{added}</span>
      <span className="text-red-600 dark:text-red-400">-{removed}</span>
    </span>
  );

  if (onClick) {
    return (
      <button
        onClick={onClick}
        className={cn(
          "inline-flex items-center rounded-md border border-border px-2 py-1 transition-colors",
          "hover:bg-muted/60 hover:border-muted-foreground/30",
          "focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
        )}
        title="View changes"
      >
        {content}
      </button>
    );
  }

  return content;
}
