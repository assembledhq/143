"use client";

import { forwardRef, useMemo, useCallback, useState } from "react";
import type { DiffFile, DiffLine } from "@/lib/diff-parser";
import type { SessionReviewComment, FileLine } from "@/lib/types";
import type { CommentLineKey } from "@/hooks/use-review-comments";
import { useFileHighlighting } from "@/lib/syntax-highlighter";
import { FileDiffHeader } from "./file-diff-header";
import { DiffHunk } from "./diff-hunk";
import { SplitDiffHunk } from "./split-diff-hunk";
import { ContextExpander, type ContextExpandResult } from "./context-expander";
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
  fileContextMeta?: Record<string, { totalLines: number }>;
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
  contextUnavailable?: boolean;
  onContextUnavailable?: () => void;
  showInlineCommentComposer?: boolean;
  onRequestEditComment?: (comment: SessionReviewComment) => void;
}

type GapKind = "top" | "middle" | "bottom";

interface ContextGapState {
  kind: GapKind;
  key: string;
  hiddenStart: number;
  hiddenEnd: number;
  lineDelta: number;
  visibleStart?: number;
  visibleEnd?: number;
  lines: DiffLine[];
}

function getFirstVisibleOldLine(hunk: DiffFile["hunks"][number]): number | null {
  for (const line of hunk.lines) {
    if (line.oldLineNumber != null) return line.oldLineNumber;
  }
  return null;
}

function getLastVisibleOldLine(hunk: DiffFile["hunks"][number]): number | null {
  let lineNumber: number | null = null;
  for (const line of hunk.lines) {
    if (line.oldLineNumber != null) lineNumber = line.oldLineNumber;
  }
  return lineNumber;
}

function getFirstVisibleNewLine(hunk: DiffFile["hunks"][number]): number | null {
  for (const line of hunk.lines) {
    if (line.newLineNumber != null) return line.newLineNumber;
  }
  return null;
}

function getLastVisibleNewLine(hunk: DiffFile["hunks"][number]): number | null {
  let lineNumber: number | null = null;
  for (const line of hunk.lines) {
    if (line.newLineNumber != null) lineNumber = line.newLineNumber;
  }
  return lineNumber;
}

function toDiffLines(lines: FileLine[], lineDelta: number): DiffLine[] {
  return lines.map((line) => ({
    type: "context" as const,
    content: line.content,
    oldLineNumber: line.number - lineDelta,
    newLineNumber: line.number,
  }));
}

export const FileDiffSection = forwardRef<HTMLDivElement, FileDiffSectionProps>(
  function FileDiffSection({
    file,
    viewMode,
    sessionId,
    fileContextMeta,
    isActive = true,
    commentsByLine,
    activeCommentLine,
    onAddComment,
    onSubmitComment,
    onCancelComment,
    onUpdateComment,
    onDeleteComment,
    onBrowseFile,
    contextUnavailable,
    onContextUnavailable,
    showInlineCommentComposer = true,
    onRequestEditComment,
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

    const [gapStates, setGapStates] = useState<Map<string, ContextGapState>>(new Map());

    const buildGapState = useCallback((kind: GapKind, key: string, hiddenStart: number, hiddenEnd: number, lineDelta: number) => {
      const existing = gapStates.get(key);
      if (existing) return existing;
      return { kind, key, hiddenStart, hiddenEnd, lineDelta, lines: [] } as ContextGapState;
    }, [gapStates]);

    const makeHandleContextExpand = useCallback((gap: ContextGapState) => {
      return (direction: "above" | "below" | "all", lines: FileLine[], meta: ContextExpandResult) => {
        const diffLines = toDiffLines(lines, gap.lineDelta);
        setGapStates((prev) => {
          const next = new Map(prev);
          const existing = next.get(gap.key) ?? gap;
          let mergedLines = existing.lines;
          if (direction === "above") {
            mergedLines = [...diffLines, ...existing.lines];
          } else if (direction === "below") {
            mergedLines = [...existing.lines, ...diffLines];
          } else {
            mergedLines = diffLines;
          }

          next.set(gap.key, {
            ...existing,
            lines: mergedLines,
            visibleStart:
              existing.visibleStart != null
                ? Math.min(existing.visibleStart, meta.startLine)
                : meta.startLine,
            visibleEnd:
              existing.visibleEnd != null
                ? Math.max(existing.visibleEnd, meta.endLine)
                : meta.endLine,
          });
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
      showInlineCommentComposer,
      onRequestEditComment,
    }), [file.newPath, commentsByLine, activeCommentLine, onAddComment, handleAddComment, onSubmitComment, onCancelComment, onUpdateComment, onDeleteComment, showInlineCommentComposer, onRequestEditComment]);

    const renderGap = useCallback((gap: ContextGapState) => {
      const gapState = gapStates.get(gap.key) ?? gap;
      const hiddenLineCount = gapState.hiddenEnd - gapState.hiddenStart + 1;
      if (hiddenLineCount <= 0) return null;

      const expandedHunk = gapState.lines.length > 0 ? {
        oldStart: gapState.lines[0].oldLineNumber ?? 0,
        oldCount: gapState.lines.length,
        newStart: gapState.lines[0].newLineNumber ?? 0,
        newCount: gapState.lines.length,
        header: "",
        lines: gapState.lines,
      } : null;

      return (
        <div key={gap.key}>
          <ContextExpander
            kind={gap.kind}
            hiddenLineCount={hiddenLineCount}
            sessionId={sessionId}
            filePath={file.newPath}
            hiddenStart={gap.hiddenStart}
            hiddenEnd={gap.hiddenEnd}
            visibleStart={gapState.visibleStart}
            visibleEnd={gapState.visibleEnd}
            onExpand={makeHandleContextExpand(gapState)}
            contextUnavailable={contextUnavailable}
            onContextUnavailable={onContextUnavailable}
          />
          {expandedHunk ? (
            viewMode === "split" ? (
              <SplitDiffHunk hunk={expandedHunk} {...commonHunkProps} />
            ) : (
              <DiffHunk hunk={expandedHunk} {...commonHunkProps} />
            )
          ) : null}
        </div>
      );
    }, [commonHunkProps, file.newPath, gapStates, makeHandleContextExpand, sessionId, viewMode, contextUnavailable, onContextUnavailable]);

    const sections = useMemo(() => {
      const items: Array<{ type: "gap"; gap: ContextGapState } | { type: "hunk"; index: number; hunk: DiffFile["hunks"][number] }> = [];
      if (file.hunks.length === 0) return items;

      const firstStart = getFirstVisibleOldLine(file.hunks[0]);
      const firstNewStart = getFirstVisibleNewLine(file.hunks[0]);
      if (firstStart != null && firstNewStart != null && firstNewStart > 1) {
        items.push({
          type: "gap",
          gap: buildGapState("top", `${file.newPath}:top`, 1, firstNewStart - 1, firstNewStart - firstStart),
        });
      }

      file.hunks.forEach((hunk, i) => {
        items.push({ type: "hunk", index: i, hunk });
        if (i === file.hunks.length - 1) return;
        const currentEnd = getLastVisibleOldLine(hunk);
        const currentNewEnd = getLastVisibleNewLine(hunk);
        const nextStart = getFirstVisibleOldLine(file.hunks[i + 1]);
        const nextNewStart = getFirstVisibleNewLine(file.hunks[i + 1]);
        if (
          currentEnd != null &&
          currentNewEnd != null &&
          nextStart != null &&
          nextNewStart != null &&
          nextNewStart - currentNewEnd > 1
        ) {
          items.push({
            type: "gap",
            gap: buildGapState(
              "middle",
              `${file.newPath}:middle:${i}`,
              currentNewEnd + 1,
              nextNewStart - 1,
              nextNewStart - nextStart,
            ),
          });
        }
      });

      const totalLines = fileContextMeta?.[file.newPath]?.totalLines;
      const lastEnd = getLastVisibleOldLine(file.hunks[file.hunks.length - 1]);
      const lastNewEnd = getLastVisibleNewLine(file.hunks[file.hunks.length - 1]);
      if (totalLines != null && lastEnd != null && lastNewEnd != null && totalLines > lastNewEnd) {
        items.push({
          type: "gap",
          gap: buildGapState("bottom", `${file.newPath}:bottom`, lastNewEnd + 1, totalLines, lastNewEnd - lastEnd),
        });
      }
      return items;
    }, [buildGapState, file.hunks, file.newPath, fileContextMeta]);

    return (
      <div ref={ref} className="border border-border rounded-lg">
        <FileDiffHeader
          filePath={file.newPath}
          added={file.stats.added}
          removed={file.stats.removed}
          onBrowseFile={onBrowseFile}
        />
        <div className="overflow-x-auto [container-type:inline-size]">
        <div className="min-w-fit">
        {sections.map((section) => {
          if (section.type === "gap") {
            return renderGap(section.gap);
          }

          const hunkEl =
            viewMode === "split" ? (
              <SplitDiffHunk
                key={section.index}
                hunk={section.hunk}
                highlightedLines={hunkHighlightMaps?.[section.index]}
                {...commonHunkProps}
              />
            ) : (
              <DiffHunk
                key={section.index}
                hunk={section.hunk}
                highlightedLines={hunkHighlightMaps?.[section.index]}
                {...commonHunkProps}
              />
            );
          return <div key={section.index}>{hunkEl}</div>;
        })}
        </div>
        </div>
      </div>
    );
  }
);
