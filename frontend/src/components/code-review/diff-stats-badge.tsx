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
    <span className={cn("inline-flex items-center gap-1 text-[11px] font-mono", className)}>
      <span className="text-green-600 dark:text-green-400">+{added}</span>
      <span className="text-muted-foreground/40">/</span>
      <span className="text-red-600 dark:text-red-400">-{removed}</span>
    </span>
  );

  if (onClick) {
    return (
      <button
        onClick={onClick}
        className="hover:bg-muted/50 rounded px-1.5 py-0.5 transition-colors"
      >
        {content}
      </button>
    );
  }

  return content;
}
