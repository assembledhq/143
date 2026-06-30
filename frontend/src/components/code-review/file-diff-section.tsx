"use client";

import { forwardRef, useMemo, useCallback, useState, type ReactNode } from "react";
import type { DiffFile, DiffLine } from "@/lib/diff-parser";
import type { SessionReviewComment, FileLine } from "@/lib/types";
import type { CommentLineKey } from "@/hooks/use-review-comments";
import { Button } from "@/components/ui/button";
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
  hiddenEnd?: number;
  lineDelta: number;
  lines: DiffLine[];
}

const INITIAL_RENDERED_DIFF_LINES = 800;
const RENDERED_DIFF_LINE_INCREMENT = 800;

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

function getDiffLineNumber(line: DiffLine): number {
  return line.newLineNumber ?? line.oldLineNumber ?? 0;
}

function mergeContextLines(existingLines: DiffLine[], newLines: DiffLine[]): DiffLine[] {
  const byLineNumber = new Map<number, DiffLine>();
  for (const line of existingLines) {
    byLineNumber.set(getDiffLineNumber(line), line);
  }
  for (const line of newLines) {
    byLineNumber.set(getDiffLineNumber(line), line);
  }
  return Array.from(byLineNumber.values()).sort(
    (a, b) => getDiffLineNumber(a) - getDiffLineNumber(b)
  );
}

function makeContextHunk(lines: DiffLine[]): DiffFile["hunks"][number] | null {
  if (lines.length === 0) return null;
  return {
    oldStart: lines[0].oldLineNumber ?? 0,
    oldCount: lines.length,
    newStart: lines[0].newLineNumber ?? 0,
    newCount: lines.length,
    header: "",
    lines,
  };
}

function splitExpandedGapLines(gap: ContextGapState): {
  sortedLines: DiffLine[];
  lowerLines: DiffLine[];
  upperLines: DiffLine[];
  lowerEnd?: number;
  upperStart?: number;
  visibleStart?: number;
  visibleEnd?: number;
  remainingLineCount: number;
} {
  const sortedLines = mergeContextLines([], gap.lines);
  const visibleNumbers = sortedLines.map(getDiffLineNumber);
  const visibleStart = visibleNumbers.length > 0 ? visibleNumbers[0] : undefined;
  const visibleEnd = visibleNumbers.length > 0 ? visibleNumbers[visibleNumbers.length - 1] : undefined;

  if (gap.hiddenEnd == null) {
    return {
      sortedLines,
      lowerLines: sortedLines,
      upperLines: [],
      lowerEnd: visibleEnd,
      visibleStart,
      visibleEnd,
      remainingLineCount: 1,
    };
  }

  const linesByNumber = new Map(sortedLines.map((line) => [getDiffLineNumber(line), line]));
  const lowerLines: DiffLine[] = [];
  for (let lineNumber = gap.hiddenStart; lineNumber <= gap.hiddenEnd; lineNumber++) {
    const line = linesByNumber.get(lineNumber);
    if (!line) break;
    lowerLines.push(line);
  }

  const lowerNumbers = new Set(lowerLines.map(getDiffLineNumber));
  const upperLinesReversed: DiffLine[] = [];
  for (let lineNumber = gap.hiddenEnd; lineNumber >= gap.hiddenStart; lineNumber--) {
    if (lowerNumbers.has(lineNumber)) break;
    const line = linesByNumber.get(lineNumber);
    if (!line) break;
    upperLinesReversed.push(line);
  }
  const upperLines = upperLinesReversed.reverse();

  const lowerEnd = lowerLines.length > 0
    ? getDiffLineNumber(lowerLines[lowerLines.length - 1])
    : undefined;
  const upperStart = upperLines.length > 0
    ? getDiffLineNumber(upperLines[0])
    : undefined;
  const remainingStart = lowerEnd != null ? lowerEnd + 1 : gap.hiddenStart;
  const remainingEnd = upperStart != null ? upperStart - 1 : gap.hiddenEnd;
  const remainingLineCount = Math.max(0, remainingEnd - remainingStart + 1);

  return {
    sortedLines,
    lowerLines,
    upperLines,
    lowerEnd,
    upperStart,
    visibleStart,
    visibleEnd,
    remainingLineCount,
  };
}

function countRenderableLines(hunk: DiffFile["hunks"][number]): number {
  return hunk.lines.length;
}

function sliceHunk(hunk: DiffFile["hunks"][number], lineLimit: number): DiffFile["hunks"][number] {
  const lines = hunk.lines.slice(0, lineLimit);
  return {
    ...hunk,
    lines,
    oldCount: lines.filter((line) => line.type !== "add").length,
    newCount: lines.filter((line) => line.type !== "remove").length,
  };
}

function diffFileContentKey(file: DiffFile): string {
  let hash = 0;
  const append = (value: string | number | null | undefined) => {
    const text = value == null ? "null" : String(value);
    for (let i = 0; i < text.length; i++) {
      hash = ((hash << 5) - hash + text.charCodeAt(i)) | 0;
    }
    hash = ((hash << 5) - hash + 31) | 0;
  };

  append(file.oldPath);
  append(file.newPath);
  append(file.stats.added);
  append(file.stats.removed);
  for (const hunk of file.hunks) {
    append(hunk.oldStart);
    append(hunk.oldCount);
    append(hunk.newStart);
    append(hunk.newCount);
    append(hunk.header);
    for (const line of hunk.lines) {
      append(line.type);
      append(line.content);
      append(line.oldLineNumber);
      append(line.newLineNumber);
    }
  }
  return `${file.newPath}:${file.hunks.length}:${hash}`;
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
    const fileContentKey = useMemo(() => diffFileContentKey(file), [file]);
    const [gapStates, setGapStates] = useState<Map<string, ContextGapState>>(new Map());
    const [visibleLineState, setVisibleLineState] = useState({
      fileContentKey,
      count: INITIAL_RENDERED_DIFF_LINES,
    });
    let visibleLineLimit = visibleLineState.count;
    if (visibleLineState.fileContentKey !== fileContentKey) {
      visibleLineLimit = INITIAL_RENDERED_DIFF_LINES;
      setVisibleLineState({ fileContentKey, count: INITIAL_RENDERED_DIFF_LINES });
      setGapStates(new Map());
    }

    const buildGapState = useCallback((kind: GapKind, key: string, hiddenStart: number, hiddenEnd: number | undefined, lineDelta: number) => {
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
          const mergedLines =
            direction === "all" ? mergeContextLines([], diffLines) : mergeContextLines(existing.lines, diffLines);

          next.set(gap.key, {
            ...existing,
            lines: mergedLines,
            hiddenEnd:
              existing.hiddenEnd ??
              (meta.totalLines >= existing.hiddenStart
                ? meta.totalLines
                : existing.hiddenStart - 1),
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
      const expanded = splitExpandedGapLines(gapState);
      const hiddenLineCount = expanded.remainingLineCount;
      if (hiddenLineCount <= 0 && expanded.sortedLines.length === 0) return null;

      const renderContextLines = (key: string, lines: DiffLine[]) => {
        const expandedHunk = makeContextHunk(lines);
        if (!expandedHunk) return null;
        return viewMode === "split" ? (
          <SplitDiffHunk key={key} hunk={expandedHunk} {...commonHunkProps} />
        ) : (
          <DiffHunk key={key} hunk={expandedHunk} {...commonHunkProps} />
        );
      };

      const expander = hiddenLineCount > 0 ? (
        <ContextExpander
          key="expander"
          kind={gap.kind}
          viewMode={viewMode}
          hiddenLineCount={hiddenLineCount}
          sessionId={sessionId}
          filePath={file.newPath}
          hiddenStart={gap.hiddenStart}
          hiddenEnd={gapState.hiddenEnd}
          visibleStart={expanded.visibleStart}
          visibleEnd={expanded.visibleEnd}
          aboveVisibleStart={expanded.upperStart}
          belowVisibleEnd={expanded.lowerEnd}
          onExpand={makeHandleContextExpand(gapState)}
          contextUnavailable={contextUnavailable}
          onContextUnavailable={onContextUnavailable}
        />
      ) : null;

      let children: ReactNode[];
      if (gap.kind === "bottom") {
        children = [renderContextLines("lower", expanded.sortedLines), expander];
      } else if (gap.kind === "middle") {
        children = [
          renderContextLines("lower", expanded.lowerLines),
          expander,
          renderContextLines("upper", expanded.upperLines),
        ];
      } else {
        children = [expander, renderContextLines("upper", expanded.sortedLines)];
      }

      return (
        <div key={gap.key}>
          {children}
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
      } else if (totalLines == null && sessionId && lastEnd != null && lastNewEnd != null) {
        items.push({
          type: "gap",
          gap: buildGapState("bottom", `${file.newPath}:bottom`, lastNewEnd + 1, undefined, lastNewEnd - lastEnd),
        });
      }
      return items;
    }, [buildGapState, file.hunks, file.newPath, fileContextMeta, sessionId]);
    const totalRenderableLines = useMemo(
      () => file.hunks.reduce((sum, hunk) => sum + countRenderableLines(hunk), 0),
      [file.hunks],
    );
    const visibleSections = useMemo(() => {
      if (totalRenderableLines <= visibleLineLimit) return sections;

      const items: typeof sections = [];
      let remaining = visibleLineLimit;

      for (const section of sections) {
        if (section.type === "gap") {
          if (remaining > 0) items.push(section);
          continue;
        }

        const lineCount = countRenderableLines(section.hunk);
        if (lineCount <= remaining) {
          items.push(section);
          remaining -= lineCount;
          continue;
        }

        if (remaining > 0) {
          items.push({
            ...section,
            hunk: sliceHunk(section.hunk, remaining),
          });
        }
        break;
      }

      return items;
    }, [sections, totalRenderableLines, visibleLineLimit]);
    const renderedLineCount = Math.min(totalRenderableLines, visibleLineLimit);
    const hasMoreDiffLines = renderedLineCount < totalRenderableLines;
    const visibleHunkSections = useMemo(
      () => visibleSections.filter((section): section is Extract<(typeof visibleSections)[number], { type: "hunk" }> => section.type === "hunk"),
      [visibleSections],
    );

    // Collect visible line contents across hunks for a single batch highlight call.
    const allLineContents = useMemo(() => {
      const contents: string[] = [];
      for (const section of visibleHunkSections) {
        for (const line of section.hunk.lines) {
          contents.push(line.content);
        }
      }
      return contents;
    }, [visibleHunkSections]);

    const highlighted = useFileHighlighting(allLineContents, file.language, undefined, isActive);

    // Build per-visible-hunk Maps of line index -> highlighted HTML.
    const hunkHighlightMaps = useMemo(() => {
      if (!highlighted) return null;
      const maps = new Map<number, Map<number, string>>();
      let offset = 0;
      for (const section of visibleHunkSections) {
        const map = new Map<number, string>();
        for (let i = 0; i < section.hunk.lines.length; i++) {
          map.set(i, highlighted[offset + i]);
        }
        maps.set(section.index, map);
        offset += section.hunk.lines.length;
      }
      return maps;
    }, [highlighted, visibleHunkSections]);

    return (
      <div ref={ref} className="min-w-0 max-w-full rounded-lg border border-border">
        <FileDiffHeader
          filePath={file.newPath}
          added={file.stats.added}
          removed={file.stats.removed}
          onBrowseFile={onBrowseFile}
        />
        <div className="min-w-0 max-w-full overflow-x-auto overscroll-x-contain [container-type:inline-size]">
          <div className="min-w-fit">
            {visibleSections.map((section) => {
              if (section.type === "gap") {
                return renderGap(section.gap);
              }

              const hunkEl =
                viewMode === "split" ? (
                  <SplitDiffHunk
                    key={section.index}
                    hunk={section.hunk}
                    highlightedLines={hunkHighlightMaps?.get(section.index)}
                    {...commonHunkProps}
                  />
                ) : (
                  <DiffHunk
                    key={section.index}
                    hunk={section.hunk}
                    highlightedLines={hunkHighlightMaps?.get(section.index)}
                    {...commonHunkProps}
                  />
                );
              return <div key={section.index}>{hunkEl}</div>;
            })}
            {hasMoreDiffLines ? (
              <div className="sticky bottom-0 border-t border-border bg-background/95 px-3 py-3 text-center backdrop-blur">
                <p className="mb-2 text-xs text-muted-foreground">
                  Showing first {renderedLineCount} of {totalRenderableLines} diff lines in this file
                </p>
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  className="text-xs"
                  onClick={() => setVisibleLineState((state) => ({
                    fileContentKey,
                    count: state.count + RENDERED_DIFF_LINE_INCREMENT,
                  }))}
                >
                  Show more diff lines
                </Button>
              </div>
            ) : null}
          </div>
        </div>
      </div>
    );
  }
);
