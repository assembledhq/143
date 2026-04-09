"use client";

import { forwardRef, useMemo, useCallback, useState } from "react";
import type { DiffFile, DiffLine } from "@/lib/diff-parser";
import type { SessionReviewComment, FileLine } from "@/lib/types";
import type { CommentLineKey } from "@/hooks/use-review-comments";
import { useFileHighlighting } from "@/lib/syntax-highlighter";
import { FileDiffHeader } from "./file-diff-header";
import { DiffHunk } from "./diff-hunk";
import { SplitDiffHunk } from "./split-diff-hunk";
import { ContextExpander } from "./context-expander";
import type { ViewMode } from "./review-toolbar";

interface ActiveCommentLine {
  filePath: string;
  lineNumber: number;
  side: "old" | "new";
}

interface FileDiffSectionProps {
  file: DiffFile;
  viewMode: ViewMode;
  sessionId?: string;
  /** When true, syntax highlighting is enabled for this file. Defaults to true. */
  isActive?: boolean;
  commentsByLine?: Map<CommentLineKey, SessionReviewComment[]>;
  activeCommentLine?: ActiveCommentLine | null;
  onAddComment?: (filePath: string, lineNumber: number, side: "old" | "new") => void;
  onSubmitComment?: (body: string) => void;
  onCancelComment?: () => void;
  onUpdateComment?: (commentId: string, data: { body?: string; resolved?: boolean }) => void;
  onDeleteComment?: (commentId: string) => void;
  onBrowseFile?: (filePath: string) => void;
}

/**
 * Compute hidden line count between two consecutive hunks.
 */
function hiddenLinesBetweenHunks(
  prevHunk: DiffFile["hunks"][number],
  nextHunk: DiffFile["hunks"][number]
): number {
  let prevEnd = 0;
  for (const line of prevHunk.lines) {
    if (line.oldLineNumber != null) {
      prevEnd = line.oldLineNumber;
    }
  }
  let nextStart = 0;
  for (const line of nextHunk.lines) {
    if (line.oldLineNumber != null) {
      nextStart = line.oldLineNumber;
      break;
    }
  }
  return nextStart > prevEnd ? nextStart - prevEnd - 1 : 0;
}

export const FileDiffSection = forwardRef<HTMLDivElement, FileDiffSectionProps>(
  function FileDiffSection({
    file,
    viewMode,
    sessionId,
    isActive = true,
    commentsByLine,
    activeCommentLine,
    onAddComment,
    onSubmitComment,
    onCancelComment,
    onUpdateComment,
    onDeleteComment,
    onBrowseFile,
  }, ref) {
    // Collect all line contents across hunks for a single batch highlight call
    const allLineContents = useMemo(() => {
      const contents: string[] = [];
      for (const hunk of file.hunks) {
        for (const line of hunk.lines) {
          contents.push(line.content);
        }
      }
      return contents;
    }, [file.hunks]);

    const highlighted = useFileHighlighting(allLineContents, file.language, undefined, isActive);

    // Build per-hunk Maps of line index → highlighted HTML
    const hunkHighlightMaps = useMemo(() => {
      if (!highlighted) return null;
      const maps: Map<number, string>[] = [];
      let offset = 0;
      for (const hunk of file.hunks) {
        const map = new Map<number, string>();
        for (let i = 0; i < hunk.lines.length; i++) {
          map.set(i, highlighted[offset + i]);
        }
        maps.push(map);
        offset += hunk.lines.length;
      }
      return maps;
    }, [highlighted, file.hunks]);

    // Track expanded context lines per gap (keyed by hunk index).
    const [expandedGaps, setExpandedGaps] = useState<Map<number, DiffLine[]>>(new Map());

    // Factory that returns a callback for a specific gap index.
    const makeHandleContextExpand = useCallback((gapIndex: number) => {
      return (lines: FileLine[]) => {
        // Convert FileLine[] to DiffLine[] (context lines with both old and new line numbers).
        const diffLines: DiffLine[] = lines.map((l) => ({
          type: "context" as const,
          content: l.content,
          oldLineNumber: l.number,
          newLineNumber: l.number,
        }));
        setExpandedGaps((prev) => {
          const next = new Map(prev);
          next.set(gapIndex, diffLines);
          return next;
        });
      };
    }, []);

    const handleAddComment = useCallback(
      (lineNumber: number, side: "old" | "new") => {
        if (onAddComment) {
          onAddComment(file.newPath, lineNumber, side);
        }
      },
      [onAddComment, file.newPath]
    );

    const commonHunkProps = useMemo(() => ({
      filePath: file.newPath,
      commentsByLine,
      activeCommentLine,
      onAddComment: onAddComment ? handleAddComment : undefined,
      onSubmitComment,
      onCancelComment,
      onUpdateComment,
      onDeleteComment,
    }), [file.newPath, commentsByLine, activeCommentLine, onAddComment, handleAddComment, onSubmitComment, onCancelComment, onUpdateComment, onDeleteComment]);

    return (
      <div ref={ref} className="border border-border rounded-lg">
        <FileDiffHeader
          filePath={file.newPath}
          added={file.stats.added}
          removed={file.stats.removed}
          onBrowseFile={onBrowseFile}
        />
        <div className="overflow-x-auto">
        {file.hunks.map((hunk, i) => {
          const hunkEl =
            viewMode === "split" ? (
              <SplitDiffHunk
                key={i}
                hunk={hunk}
                highlightedLines={hunkHighlightMaps?.[i]}
                {...commonHunkProps}
              />
            ) : (
              <DiffHunk
                key={i}
                hunk={hunk}
                highlightedLines={hunkHighlightMaps?.[i]}
                {...commonHunkProps}
              />
            );

          if (i === 0) return hunkEl;

          const hidden = hiddenLinesBetweenHunks(file.hunks[i - 1], hunk);
          // Compute the start line for context expansion (last old line of previous hunk)
          const prevHunkLines = file.hunks[i - 1].lines;
          let expandStartLine = 0;
          for (const l of prevHunkLines) {
            if (l.oldLineNumber != null) expandStartLine = l.oldLineNumber;
          }

          const expandedLines = expandedGaps.get(i);

          return (
            <div key={i}>
              {hidden > 0 && !expandedLines && (
                <ContextExpander
                  hiddenLineCount={hidden}
                  sessionId={sessionId}
                  filePath={file.newPath}
                  startLine={expandStartLine}
                  onExpand={makeHandleContextExpand(i)}
                />
              )}
              {expandedLines && expandedLines.length > 0 && (
                viewMode === "split" ? (
                  <SplitDiffHunk
                    hunk={{ oldStart: expandedLines[0].oldLineNumber ?? 0, oldCount: expandedLines.length, newStart: expandedLines[0].newLineNumber ?? 0, newCount: expandedLines.length, header: "", lines: expandedLines }}
                    {...commonHunkProps}
                  />
                ) : (
                  <DiffHunk
                    hunk={{ oldStart: expandedLines[0].oldLineNumber ?? 0, oldCount: expandedLines.length, newStart: expandedLines[0].newLineNumber ?? 0, newCount: expandedLines.length, header: "", lines: expandedLines }}
                    {...commonHunkProps}
                  />
                )
              )}
              {hunkEl}
            </div>
          );
        })}
        </div>
      </div>
    );
  }
);
