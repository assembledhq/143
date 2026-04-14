import { Copy, Check, ExternalLink } from "lucide-react";
import { useState, useCallback, useEffect } from "react";
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
        "sticky top-0 z-10 flex items-center justify-between px-3 py-2 bg-muted/60 backdrop-blur-sm border-b border-border",
        className
      )}
    >
      <div className="flex items-center gap-2 min-w-0">
        <span className="text-xs font-medium font-mono text-foreground truncate">
          {filePath}
        </span>
        <DiffStatsBadge added={added} removed={removed} />
      </div>
      <div className="flex items-center gap-0.5 shrink-0">
        {onBrowseFile && (
          <button
            onClick={() => onBrowseFile(filePath)}
            className="p-1 rounded hover:bg-muted transition-colors text-muted-foreground hover:text-foreground"
            title="Browse in repository explorer"
          >
            <ExternalLink className="h-3.5 w-3.5" />
          </button>
        )}
        <button
          onClick={copyPath}
          className="p-1 rounded hover:bg-muted transition-colors text-muted-foreground hover:text-foreground"
          title="Copy file path"
        >
          {copied ? (
            <Check className="h-3.5 w-3.5 text-emerald-500" />
          ) : (
            <Copy className="h-3.5 w-3.5" />
          )}
        </button>
      </div>
    </div>
  );
}
