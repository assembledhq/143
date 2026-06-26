import { useMemo } from "react";
import type { DiffHunk as DiffHunkType } from "@/lib/diff-parser";
import type { SessionReviewComment } from "@/lib/types";
import type { CommentLineKey } from "@/hooks/use-review-comments";
import { makeCommentLineKey } from "@/hooks/use-review-comments";
import { DiffLineRow } from "./diff-line";
import { CommentThread } from "./comment-thread";
import { CommentInput } from "./comment-input";
import { buildInlineDiffRanges } from "./inline-diff-highlight";

interface ActiveCommentLine {
  filePath: string;
  lineNumber: number;
  side: "old" | "new";
}

interface DiffHunkProps {
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

export function DiffHunk({
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
}: DiffHunkProps) {
  const inlineDiffRanges = useMemo(
    () => buildInlineDiffRanges(hunk.lines),
    [hunk.lines]
  );

  return (
    <div>
      {/* Hunk header */}
      <div
        data-hunk-header
        className="bg-muted/30 border-y border-border/50 px-3 py-1 text-xs text-muted-foreground font-mono select-none"
      >
        {hunk.header}
      </div>
      {/* Lines with inline comments */}
      {hunk.lines.map((line, i) => {
        // Use new line number for adds/context, old line number for removes
        const commentLineNumber =
          line.type === "remove" ? line.oldLineNumber : line.newLineNumber;
        const commentSide: "old" | "new" = line.type === "remove" ? "old" : "new";
        const lineKey = commentLineNumber
          ? makeCommentLineKey(filePath, commentLineNumber, commentSide)
          : null;
        const lineComments = lineKey ? commentsByLine?.get(lineKey) : undefined;

        const isActiveCommentLine =
          activeCommentLine &&
          activeCommentLine.filePath === filePath &&
          commentLineNumber != null &&
          activeCommentLine.lineNumber === commentLineNumber &&
          activeCommentLine.side === commentSide;

        return (
          <div key={i}>
            <DiffLineRow
              line={line}
              filePath={filePath}
              highlightedContent={highlightedLines?.get(i)}
              inlineHighlightRanges={inlineDiffRanges.get(i)}
              onAddComment={
                onAddComment && commentLineNumber != null
                  ? () => onAddComment(commentLineNumber, commentSide)
                  : undefined
              }
              hasComments={lineComments != null && lineComments.length > 0}
            />
            {/* Existing comments */}
            {lineComments && lineComments.length > 0 && onUpdateComment && onDeleteComment && (
              <div
                data-testid="inline-comment-thread-anchor"
                className="sticky left-0 pl-[100px] pr-2"
              >
                <CommentThread
                  comments={lineComments}
                  onUpdate={onUpdateComment}
                  onDelete={onDeleteComment}
                  className="max-w-[min(42rem,calc(100cqw-10rem))]"
                  onRequestEdit={onRequestEditComment}
                />
              </div>
            )}
            {/* New comment input */}
            {showInlineCommentComposer && isActiveCommentLine && onSubmitComment && onCancelComment && (
              <div
                data-testid="inline-comment-composer-anchor"
                className="sticky left-0 pl-[100px] pr-2"
              >
                <CommentInput
                  className="max-w-[min(42rem,calc(100cqw-10rem))]"
                  onSubmit={onSubmitComment}
                  onCancel={onCancelComment}
                />
              </div>
            )}
          </div>
        );
      })}
    </div>
  );
}
