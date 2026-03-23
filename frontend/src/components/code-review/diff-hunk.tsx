import type { DiffHunk as DiffHunkType } from "@/lib/diff-parser";
import type { SessionReviewComment } from "@/lib/types";
import type { CommentLineKey } from "@/hooks/use-review-comments";
import { makeCommentLineKey } from "@/hooks/use-review-comments";
import { DiffLineRow } from "./diff-line";
import { CommentThread } from "./comment-thread";
import { CommentInput } from "./comment-input";

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
}: DiffHunkProps) {
  return (
    <div>
      {/* Hunk header */}
      <div
        data-hunk-header
        className="bg-muted/30 border-y border-border/50 px-3 py-1 text-[11px] text-muted-foreground font-mono select-none"
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
              onAddComment={
                onAddComment && commentLineNumber != null
                  ? () => onAddComment(commentLineNumber, commentSide)
                  : undefined
              }
              hasComments={lineComments != null && lineComments.length > 0}
            />
            {/* Existing comments */}
            {lineComments && lineComments.length > 0 && onUpdateComment && onDeleteComment && (
              <CommentThread
                comments={lineComments}
                onUpdate={onUpdateComment}
                onDelete={onDeleteComment}
              />
            )}
            {/* New comment input */}
            {isActiveCommentLine && onSubmitComment && onCancelComment && (
              <CommentInput onSubmit={onSubmitComment} onCancel={onCancelComment} />
            )}
          </div>
        );
      })}
    </div>
  );
}
