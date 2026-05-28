"use client";

import { useState } from "react";
import { ChevronDown, ChevronRight, Check, MessageSquare } from "lucide-react";
import { cn } from "@/lib/utils";
import type { SessionReviewComment } from "@/lib/types";

interface CommentsSummaryProps {
  comments: SessionReviewComment[];
  onCommentClick: (filePath: string) => void;
}

export function CommentsSummary({
  comments,
  onCommentClick,
}: CommentsSummaryProps) {
  const [expanded, setExpanded] = useState(true);

  if (comments.length === 0) return null;

  const resolvedCount = comments.reduce((n, c) => n + (c.resolved ? 1 : 0), 0);
  const openCount = comments.length - resolvedCount;

  return (
    <div className="mx-4 mt-3 mb-0 border border-border rounded-lg overflow-hidden bg-surface-raised">
      <div
        role="button"
        tabIndex={0}
        onClick={() => setExpanded(!expanded)}
        onKeyDown={(e) => {
          if (e.key === "Enter" || e.key === " ") {
            e.preventDefault();
            setExpanded(!expanded);
          }
        }}
        className="flex items-center justify-between w-full px-3 py-2 text-xs hover:bg-surface-pane transition-colors cursor-pointer"
      >
        <div className="flex items-center gap-2">
          {expanded ? (
            <ChevronDown className="h-3 w-3 text-muted-foreground" />
          ) : (
            <ChevronRight className="h-3 w-3 text-muted-foreground" />
          )}
          <MessageSquare className="h-3.5 w-3.5 text-muted-foreground" />
          <span className="font-medium">
            {comments.length} comment{comments.length > 1 ? "s" : ""}
          </span>
          <span className="text-muted-foreground">
            ({openCount} open{resolvedCount > 0 ? `, ${resolvedCount} resolved` : ""})
          </span>
        </div>
      </div>

      {expanded && (
        <div className="border-t border-border/50 px-3 py-1.5 space-y-0.5 max-h-[200px] overflow-y-auto">
          {comments.map((c) => {
            const fileName = c.file_path.split("/").pop() ?? c.file_path;
            return (
              <button
                key={c.id}
                onClick={() => onCommentClick(c.file_path)}
                className={cn(
                  "flex items-start gap-2 w-full text-left px-2 py-1 rounded text-xs hover:bg-surface-pane transition-colors",
                  c.resolved && "text-muted-foreground"
                )}
              >
                {c.resolved ? (
                  <Check className="h-3 w-3 shrink-0 mt-0.5 text-emerald-500" />
                ) : (
                  <span className="h-3 w-3 shrink-0 mt-0.5 flex items-center justify-center">
                    <span className="h-1.5 w-1.5 rounded-full bg-primary" />
                  </span>
                )}
                <div className="min-w-0 flex-1">
                  <span className="font-mono text-xs text-muted-foreground">
                    {fileName}:{c.line_number}
                  </span>
                  {c.pass_number > 1 && (
                    <span className="ml-1 inline-flex items-center rounded px-1 py-0 text-xs font-medium bg-muted text-muted-foreground">
                      P{c.pass_number}
                    </span>
                  )}
                  <span className="mx-1.5 text-muted-foreground/40">&mdash;</span>
                  <span className={cn("truncate", c.resolved && "line-through")}>
                    {c.body.length > 80 ? `${c.body.slice(0, 80)}...` : c.body}
                  </span>
                </div>
              </button>
            );
          })}
        </div>
      )}
    </div>
  );
}
