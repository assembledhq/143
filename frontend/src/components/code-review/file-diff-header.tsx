import { Copy, Check, ExternalLink } from "lucide-react";
import { useState, useCallback, useEffect } from "react";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { DiffStatsBadge } from "./diff-stats-badge";

interface FileDiffHeaderProps {
  filePath: string;
  added: number;
  removed: number;
  className?: string;
  onBrowseFile?: (filePath: string) => void;
}

export function FileDiffHeader({ filePath, added, removed, className, onBrowseFile }: FileDiffHeaderProps) {
  const [copied, setCopied] = useState(false);

  // Clean up the "copied" feedback after 2s, safe on unmount
  useEffect(() => {
    if (!copied) return;
    const timer = setTimeout(() => setCopied(false), 2000);
    return () => clearTimeout(timer);
  }, [copied]);

  const copyPath = useCallback(() => {
    navigator.clipboard.writeText(filePath).catch(() => {
      // Clipboard API may fail in insecure contexts — silently ignore
    });
    setCopied(true);
  }, [filePath]);

  return (
    <div
      className={cn(
        "sticky top-0 z-10 flex items-center justify-between rounded-t-lg border-b border-border/70 bg-card/95 px-3 py-1.5 shadow-none backdrop-blur supports-[backdrop-filter]:bg-card/85",
        className
      )}
    >
      <div className="flex min-w-0 flex-1 items-center gap-2">
        <span className="min-w-0 flex-1 overflow-x-auto overflow-y-hidden whitespace-nowrap overscroll-x-contain scrollbar-hide font-mono text-xs font-medium text-foreground">
          {filePath}
        </span>
        <DiffStatsBadge added={added} removed={removed} className="shrink-0" />
      </div>
      <div className="flex items-center gap-0.5 shrink-0">
        {onBrowseFile && (
          <Button
            type="button"
            variant="ghost"
            size="icon"
            onClick={() => onBrowseFile(filePath)}
            className="h-7 w-7 shrink-0 text-muted-foreground hover:text-foreground"
            title="Browse in repository explorer"
          >
            <ExternalLink className="h-3.5 w-3.5" />
          </Button>
        )}
        <Button
          type="button"
          variant="ghost"
          size="icon"
          onClick={copyPath}
          className="h-7 w-7 shrink-0 text-muted-foreground hover:text-foreground"
          title="Copy file path"
        >
          {copied ? (
            <Check className="h-3.5 w-3.5 text-success" />
          ) : (
            <Copy className="h-3.5 w-3.5" />
          )}
        </Button>
      </div>
    </div>
  );
}
