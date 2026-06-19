import { memo, useMemo } from "react";
import { Plus } from "lucide-react";
import { cn } from "@/lib/utils";
import type { DiffHunk as DiffHunkType, DiffLine } from "@/lib/diff-parser";
import type { SessionReviewComment } from "@/lib/types";
import type { CommentLineKey } from "@/hooks/use-review-comments";
import { makeCommentLineKey } from "@/hooks/use-review-comments";
import { CommentThread } from "./comment-thread";
import { CommentInput } from "./comment-input";

interface ActiveCommentLine {
  filePath: string;
  lineNumber: number;
  side: "old" | "new";
}

interface SplitDiffHunkProps {
  hunk: DiffHunkType;
  filePath: string;
  highlightedLines?: Map<number, string>;
  commentsByLine?: Map<CommentLineKey, SessionReviewComment[]>;
  activeCommentLine?: ActiveCommentLine | null;
  onAddComment?: (lineNumber: number, side: "old" | "new") => void;
  onSubmitComment?: (body: string) => void;
  onCancelComment?: () => void;
  onUpdateComment?: (commentId: string, data: { body?: string; resolved?: boolean }) => void;
  onDeleteComment?: (commentId: string) => void;
  showInlineCommentComposer?: boolean;
  onRequestEditComment?: (comment: SessionReviewComment) => void;
}

interface SplitRow {
  left: DiffLine | null;
  right: DiffLine | null;
  leftHighlighted?: string;
  rightHighlighted?: string;
}

/**
 * Pair up lines for side-by-side display:
 * - Context lines appear on both sides
 * - Remove/add pairs are aligned on the same row
 * - Unmatched removes fill the left, unmatched adds fill the right
 */
function buildSplitRows(
  lines: DiffLine[],
  highlighted?: Map<number, string>
): SplitRow[] {
  const rows: SplitRow[] = [];
  let i = 0;

  while (i < lines.length) {
    const line = lines[i];

    if (line.type === "context") {
      rows.push({
        left: line,
        right: line,
        leftHighlighted: highlighted?.get(i),
        rightHighlighted: highlighted?.get(i),
      });
      i++;
    } else if (line.type === "remove") {
      // Collect consecutive removes
      const removes: { line: DiffLine; idx: number }[] = [];
      while (i < lines.length && lines[i].type === "remove") {
        removes.push({ line: lines[i], idx: i });
        i++;
      }
      // Collect consecutive adds that follow
      const adds: { line: DiffLine; idx: number }[] = [];
      while (i < lines.length && lines[i].type === "add") {
        adds.push({ line: lines[i], idx: i });
        i++;
      }
      // Pair them up
      const maxLen = Math.max(removes.length, adds.length);
      for (let j = 0; j < maxLen; j++) {
        const rm = j < removes.length ? removes[j] : null;
        const ad = j < adds.length ? adds[j] : null;
        rows.push({
          left: rm?.line ?? null,
          right: ad?.line ?? null,
          leftHighlighted: rm ? highlighted?.get(rm.idx) : undefined,
          rightHighlighted: ad ? highlighted?.get(ad.idx) : undefined,
        });
      }
    } else if (line.type === "add") {
      // Standalone add (no preceding remove)
      rows.push({
        left: null,
        right: line,
        rightHighlighted: highlighted?.get(i),
      });
      i++;
    } else {
      i++;
    }
  }

  return rows;
}

const bgStyles = {
  add: "bg-green-50/60 dark:bg-green-950/20",
  remove: "bg-red-50/60 dark:bg-red-950/20",
  context: "",
  empty: "bg-muted/20",
};

const gutterBg = {
  add: "bg-green-100/40 dark:bg-green-950/30",
  remove: "bg-red-100/40 dark:bg-red-950/30",
  context: "",
  empty: "bg-muted/20",
};

const SplitLineCell = memo(function SplitLineCell({
  line,
  highlightedContent,
  side,
  onAddComment,
  hasComments,
}: {
  line: DiffLine | null;
  highlightedContent?: string;
  side: "left" | "right";
  onAddComment?: () => void;
  hasComments?: boolean;
}) {
  if (!line) {
    return (
      <div className={cn("flex text-xs font-mono leading-[20px] flex-1 min-w-0", bgStyles.empty)}>
        <div className={cn("w-[42px] shrink-0 select-none", gutterBg.empty)} />
        <div className="w-[16px] shrink-0" />
        <div className="flex-1 min-w-0">&nbsp;</div>
      </div>
    );
  }

  const type = line.type;
  const lineNum = side === "left" ? line.oldLineNumber : line.newLineNumber;
  const prefix = type === "add" ? "+" : type === "remove" ? "-" : " ";

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
      role={onAddComment ? "button" : undefined}
      tabIndex={onAddComment ? 0 : undefined}
      aria-label={
        onAddComment ? `Add comment on line ${lineNum ?? ""}`.trim() : undefined
      }
      onClick={onAddComment}
      onKeyDown={handleKeyDown}
      className={cn(
        "flex text-xs font-mono leading-[20px] flex-1 min-w-0 group/cell relative",
        bgStyles[type],
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
              ? "opacity-60 group-hover/cell:opacity-100"
              : "opacity-0 group-hover/cell:opacity-100",
            "transition-opacity"
          )}
          title="Add comment"
        >
          <Plus className="h-3 w-3" />
        </button>
      )}
      <div
        className={cn(
          "w-[42px] shrink-0 text-right pr-1 select-none text-xs text-muted-foreground/60",
          gutterBg[type]
        )}
      >
        {lineNum ?? ""}
      </div>
      <div className="w-[16px] shrink-0 text-center select-none text-muted-foreground/50 text-xs">
        {prefix}
      </div>
      <div
        data-testid="split-diff-line-content"
        className="flex-1 min-w-0 pr-2 whitespace-pre-wrap break-words cursor-text"
      >
        {highlightedContent ? (
          // Safe: HTML generated by Shiki's codeToTokens — only produces <span style="color:..."> from grammar engine, not from user input
          <span dangerouslySetInnerHTML={{ __html: highlightedContent }} />
        ) : (
          <span>{line.content || "\u00A0"}</span>
        )}
      </div>
    </div>
  );
});

export function SplitDiffHunk({
  hunk,
  filePath,
  highlightedLines,
  commentsByLine,
  activeCommentLine,
  onAddComment,
  onSubmitComment,
  onCancelComment,
  onUpdateComment,
  onDeleteComment,
  showInlineCommentComposer = true,
  onRequestEditComment,
}: SplitDiffHunkProps) {
  const rows = useMemo(
    () => buildSplitRows(hunk.lines, highlightedLines),
    [hunk.lines, highlightedLines]
  );

  return (
    <div>
      {/* Hunk header spans full width */}
      <div data-hunk-header className="bg-muted/30 border-y border-border/50 px-3 py-1 text-xs text-muted-foreground font-mono select-none">
        {hunk.header}
      </div>
      {/* Split rows */}
      {rows.map((row, i) => {
        // Determine comment keys for both sides
        // Left side always uses old line numbers; right side always uses new
        const leftLineNum = row.left?.oldLineNumber ?? row.left?.newLineNumber;
        const rightLineNum = row.right?.newLineNumber ?? row.right?.oldLineNumber;
        const leftSide: "old" | "new" = "old";
        const rightSide: "old" | "new" = "new";

        const leftKey = leftLineNum ? makeCommentLineKey(filePath, leftLineNum, leftSide) : null;
        const rightKey = rightLineNum ? makeCommentLineKey(filePath, rightLineNum, rightSide) : null;
        const leftComments = leftKey ? commentsByLine?.get(leftKey) : undefined;
        const rightComments = rightKey ? commentsByLine?.get(rightKey) : undefined;

        const isLeftActive =
          activeCommentLine &&
          activeCommentLine.filePath === filePath &&
          leftLineNum != null &&
          activeCommentLine.lineNumber === leftLineNum &&
          activeCommentLine.side === leftSide;

        const isRightActive =
          activeCommentLine &&
          activeCommentLine.filePath === filePath &&
          rightLineNum != null &&
          activeCommentLine.lineNumber === rightLineNum &&
          activeCommentLine.side === rightSide;

        const hasInlineContent =
          (leftComments && leftComments.length > 0) ||
          (rightComments && rightComments.length > 0) ||
          isLeftActive ||
          isRightActive;

        return (
          <div key={i}>
            <div className="flex divide-x divide-border/50 [content-visibility:auto] [contain-intrinsic-size:auto_20px]">
              <SplitLineCell
                line={row.left}
                highlightedContent={row.leftHighlighted}
                side="left"
                onAddComment={
                  onAddComment && leftLineNum != null
                    ? () => onAddComment(leftLineNum, leftSide)
                    : undefined
                }
                hasComments={leftComments != null && leftComments.length > 0}
              />
              <SplitLineCell
                line={row.right}
                highlightedContent={row.rightHighlighted}
                side="right"
                onAddComment={
                  onAddComment && rightLineNum != null
                    ? () => onAddComment(rightLineNum, rightSide)
                    : undefined
                }
                hasComments={rightComments != null && rightComments.length > 0}
              />
            </div>
            {/* Inline comments/input span full width below the row */}
            {hasInlineContent && (
              <div className="bg-muted/10 border-y border-border/30">
                {((leftComments && leftComments.length > 0) ||
                  (rightComments && rightComments.length > 0)) &&
                  onUpdateComment &&
                  onDeleteComment && (
                    <div className="flex divide-x divide-border/50">
                      <div
                        data-testid="left-comment-thread-slot"
                        className="flex-1 px-2"
                      >
                        {leftComments && leftComments.length > 0 ? (
                          <CommentThread
                            comments={leftComments}
                            onUpdate={onUpdateComment}
                            onDelete={onDeleteComment}
                            className="max-w-[min(36rem,calc(50cqw-1rem))]"
                            onRequestEdit={onRequestEditComment}
                          />
                        ) : null}
                      </div>
                      <div
                        data-testid="right-comment-thread-slot"
                        className="flex-1 px-2"
                      >
                        {rightComments && rightComments.length > 0 ? (
                          <CommentThread
                            comments={rightComments}
                            onUpdate={onUpdateComment}
                            onDelete={onDeleteComment}
                            className="max-w-[min(36rem,calc(50cqw-1rem))]"
                            onRequestEdit={onRequestEditComment}
                          />
                        ) : null}
                      </div>
                    </div>
                  )}
                {showInlineCommentComposer && (isLeftActive || isRightActive) && onSubmitComment && onCancelComment && (
                  <div className="flex divide-x divide-border/50">
                    <div
                      data-testid="left-comment-composer-slot"
                      className="flex-1 px-2"
                    >
                      {isLeftActive ? (
                        <CommentInput
                          className="max-w-[min(36rem,calc(50cqw-1rem))]"
                          onSubmit={onSubmitComment}
                          onCancel={onCancelComment}
                        />
                      ) : null}
                    </div>
                    <div
                      data-testid="right-comment-composer-slot"
                      className="flex-1 px-2"
                    >
                      {isRightActive ? (
                        <CommentInput
                          className="max-w-[min(36rem,calc(50cqw-1rem))]"
                          onSubmit={onSubmitComment}
                          onCancel={onCancelComment}
                        />
                      ) : null}
                    </div>
                  </div>
                )}
              </div>
            )}
          </div>
        );
      })}
    </div>
  );
}
