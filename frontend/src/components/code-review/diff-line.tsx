import { useCallback } from "react";
import { Plus } from "lucide-react";
import { cn } from "@/lib/utils";
import type { DiffLine as DiffLineType } from "@/lib/diff-parser";
import {
  applyInlineDiffRangesToHtml,
  InlineHighlightedText,
  type InlineDiffRange,
} from "./inline-diff-highlight";

interface DiffLineProps {
  line: DiffLineType;
  filePath?: string;
  highlightedContent?: string;
  inlineHighlightRanges?: InlineDiffRange[];
  /** When set, a + icon appears on hover to add a comment */
  onAddComment?: () => void;
  hasComments?: boolean;
}

const lineTypeStyles: Record<DiffLineType["type"], string> = {
  add: "bg-green-50/60 dark:bg-green-950/20",
  remove: "bg-red-50/60 dark:bg-red-950/20",
  context: "",
};

const lineNumberStyles: Record<DiffLineType["type"], string> = {
  add: "bg-green-100/40 dark:bg-green-950/30",
  remove: "bg-red-100/40 dark:bg-red-950/30",
  context: "",
};

const linePrefix: Record<DiffLineType["type"], string> = {
  add: "+",
  remove: "-",
  context: " ",
};

export function DiffLineRow({
  line,
  filePath,
  highlightedContent,
  inlineHighlightRanges,
  onAddComment,
  hasComments,
}: DiffLineProps) {
  const handleLineNumberClick = useCallback(
    (lineNum: number | null, side: "L" | "R") => {
      if (lineNum == null) return;
      const anchor = filePath
        ? `${filePath.replace(/[^a-zA-Z0-9._/-]/g, "")}-${side}${lineNum}`
        : `${side}${lineNum}`;
      window.history.replaceState(null, "", `#${anchor}`);
    },
    [filePath]
  );

  const handleLineNumberButtonClick = useCallback(
    (e: React.MouseEvent<HTMLButtonElement>, lineNum: number | null, side: "L" | "R") => {
      e.stopPropagation();
      handleLineNumberClick(lineNum, side);
    },
    [handleLineNumberClick]
  );

  const handleLineNumberButtonKeyDown = useCallback(
    (e: React.KeyboardEvent<HTMLButtonElement>) => {
      e.stopPropagation();
    },
    []
  );

  const ariaLineNumber = line.type === "remove" ? line.oldLineNumber : line.newLineNumber;

  const handleKeyDown = onAddComment
    ? (e: React.KeyboardEvent<HTMLDivElement>) => {
        if (e.key === "Enter" || e.key === " ") {
          e.preventDefault();
          onAddComment();
        }
      }
    : undefined;

  return (
    <div
      id={
        filePath && line.newLineNumber != null
          ? `${filePath.replace(/[^a-zA-Z0-9._/-]/g, "")}-L${line.newLineNumber}`
          : undefined
      }
      role={onAddComment ? "button" : undefined}
      tabIndex={onAddComment ? 0 : undefined}
      aria-label={
        onAddComment ? `Add comment on line ${ariaLineNumber ?? ""}`.trim() : undefined
      }
      onClick={onAddComment}
      onKeyDown={handleKeyDown}
      className={cn(
        "flex text-xs font-mono leading-[20px] group relative [content-visibility:auto] [contain-intrinsic-size:auto_20px]",
        lineTypeStyles[line.type],
        onAddComment && "cursor-pointer"
      )}
    >
      {/* Comment add button — visual hover indicator; row handles the action so this is mouse-only */}
      {onAddComment && (
        <button
          type="button"
          tabIndex={-1}
          aria-hidden="true"
          onClick={(e) => {
            e.stopPropagation();
            onAddComment();
          }}
          className={cn(
            "absolute left-0 top-0 h-[20px] w-[20px] flex items-center justify-center z-10",
            "text-primary bg-primary/10 rounded-sm",
            hasComments
              ? "opacity-60 group-hover:opacity-100"
              : "opacity-0 group-hover:opacity-100",
            "transition-opacity"
          )}
          title="Add comment"
        >
          <Plus className="h-3 w-3" />
        </button>
      )}
      {/* Old line number gutter */}
      <button
        type="button"
        onClick={(e) => handleLineNumberButtonClick(e, line.oldLineNumber, "L")}
        onKeyDown={handleLineNumberButtonKeyDown}
        className={cn(
          "w-[42px] shrink-0 text-right pr-1 select-none text-xs text-muted-foreground/60 hover:text-primary cursor-pointer",
          lineNumberStyles[line.type]
        )}
      >
        {line.oldLineNumber ?? ""}
      </button>
      {/* New line number gutter */}
      <button
        type="button"
        onClick={(e) => handleLineNumberButtonClick(e, line.newLineNumber, "R")}
        onKeyDown={handleLineNumberButtonKeyDown}
        className={cn(
          "w-[42px] shrink-0 text-right pr-1 select-none text-xs text-muted-foreground/60 hover:text-primary cursor-pointer",
          lineNumberStyles[line.type]
        )}
      >
        {line.newLineNumber ?? ""}
      </button>
      {/* Prefix (+/-/space) */}
      <div className="w-[16px] shrink-0 text-center select-none text-muted-foreground/50">
        {linePrefix[line.type]}
      </div>
      {/* Content */}
      <div
        data-testid="diff-line-content"
        className="flex-1 min-w-0 pr-4 whitespace-pre-wrap break-words cursor-text"
      >
        {inlineHighlightRanges &&
        inlineHighlightRanges.length > 0 &&
        line.type !== "context" &&
        highlightedContent ? (
          // Safe: HTML generated by Shiki; inline diff spans use fixed class names and do not include user input
          <span
            dangerouslySetInnerHTML={{
              __html: applyInlineDiffRangesToHtml(
                highlightedContent,
                inlineHighlightRanges,
                line.type
              ),
            }}
          />
        ) : inlineHighlightRanges && inlineHighlightRanges.length > 0 && line.type !== "context" ? (
          <InlineHighlightedText
            content={line.content}
            ranges={inlineHighlightRanges}
            type={line.type}
          />
        ) : highlightedContent ? (
          // Safe: HTML generated by Shiki's codeToTokens — only produces <span style="color:..."> from grammar engine, not from user input
          <span dangerouslySetInnerHTML={{ __html: highlightedContent }} />
        ) : (
          <span>{line.content || "\u00A0"}</span>
        )}
      </div>
    </div>
  );
}
