import { FileCode2 } from "lucide-react";
import { cn } from "@/lib/utils";

interface DiffStatsBadgeProps {
  added: number;
  removed: number;
  filesChanged?: number;
  onClick?: () => void;
  className?: string;
}

export function DiffStatsBadge({ added, removed, filesChanged, onClick, className }: DiffStatsBadgeProps) {
  if (added === 0 && removed === 0) return null;

  const content = (
    <span className={cn("inline-flex items-center gap-1.5 text-[11px] font-mono", className)}>
      <FileCode2 className="h-3.5 w-3.5 text-muted-foreground shrink-0" />
      {filesChanged != null && filesChanged > 0 && (
        <span className="text-muted-foreground">{filesChanged} file{filesChanged !== 1 ? "s" : ""}</span>
      )}
      <span className="text-green-600 dark:text-green-400">+{added}</span>
      <span className="text-muted-foreground/40">/</span>
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
