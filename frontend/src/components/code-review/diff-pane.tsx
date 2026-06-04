"use client";

import { useRef, useCallback, useImperativeHandle, forwardRef, useEffect, useState } from "react";
import type { DiffFile } from "@/lib/diff-parser";
import type { SessionReviewComment } from "@/lib/types";
import type { CommentLineKey } from "@/hooks/use-review-comments";
import { FileDiffSection } from "./file-diff-section";
import type { ViewMode } from "./review-toolbar";

interface ActiveCommentLine {
  filePath: string;
  lineNumber: number;
  side: "old" | "new";
}

interface DiffPaneProps {
  files: DiffFile[];
  viewMode: ViewMode;
  sessionId?: string;
  activeFileIndex?: number;
  resetScrollKey?: string;
  onActiveFileChange?: (index: number) => void;
  onScrollMetricsChange?: (info: { scrollTop: number; direction: "up" | "down" | "idle" }) => void;
  commentsByLine?: Map<CommentLineKey, SessionReviewComment[]>;
  activeCommentLine?: ActiveCommentLine | null;
  onAddComment?: (filePath: string, lineNumber: number, side: "old" | "new") => void;
  onSubmitComment?: (body: string) => void;
  onCancelComment?: () => void;
  onUpdateComment?: (commentId: string, data: { body?: string; resolved?: boolean }) => void;
  onDeleteComment?: (commentId: string) => void;
  onBrowseFile?: (filePath: string) => void;
  showInlineCommentComposer?: boolean;
  onRequestEditComment?: (comment: SessionReviewComment) => void;
}

export interface DiffPaneHandle {
  scrollToFile: (index: number) => void;
  scrollToNextHunk: () => void;
  scrollToPrevHunk: () => void;
}

export const DiffPane = forwardRef<DiffPaneHandle, DiffPaneProps>(
  function DiffPane({
    files,
    viewMode,
    sessionId,
    activeFileIndex,
    resetScrollKey,
    onActiveFileChange,
    onScrollMetricsChange,
    commentsByLine,
    activeCommentLine,
    onAddComment,
    onSubmitComment,
    onCancelComment,
    onUpdateComment,
    onDeleteComment,
    onBrowseFile,
    showInlineCommentComposer = true,
    onRequestEditComment,
  }, ref) {
    const containerRef = useRef<HTMLDivElement>(null);
    const fileRefs = useRef<Map<number, HTMLDivElement>>(new Map());
    const lastReportedActiveFileIndexRef = useRef<number | null>(activeFileIndex ?? null);
    const lastScrollTopRef = useRef(0);
    const scrollFrameRef = useRef<number | null>(null);

    // Once any expander hits a NO_SANDBOX response, the session container is
    // gone for good — flip every expander on this pane into the disabled state
    // so the user gets clear feedback instead of repeating the failed click.
    // Reset on sessionId change using the "adjust state during render" pattern
    // (https://react.dev/reference/react/useState#storing-information-from-previous-renders).
    const [contextUnavailable, setContextUnavailable] = useState(false);
    const [trackedSessionId, setTrackedSessionId] = useState(sessionId);
    if (trackedSessionId !== sessionId) {
      setTrackedSessionId(sessionId);
      setContextUnavailable(false);
    }
    const handleContextUnavailable = useCallback(() => {
      setContextUnavailable(true);
    }, []);

    const setFileRef = useCallback(
      (index: number) => (el: HTMLDivElement | null) => {
        if (el) {
          fileRefs.current.set(index, el);
        } else {
          fileRefs.current.delete(index);
        }
      },
      []
    );

    const scrollToAdjacentHunk = useCallback(
      (direction: "next" | "prev") => {
        const container = containerRef.current;
        if (!container) return;

        const headers = Array.from(
          container.querySelectorAll<HTMLElement>("[data-hunk-header]")
        );
        if (headers.length === 0) return;

        const containerRect = container.getBoundingClientRect();
        const threshold = 4;

        if (direction === "next") {
          const next = headers.find((el) => {
            const rect = el.getBoundingClientRect();
            return rect.top - containerRect.top > threshold;
          });
          if (next) {
            container.scrollBy({
              top: next.getBoundingClientRect().top - containerRect.top,
              behavior: "smooth",
            });
          }
        } else {
          const prev = [...headers].reverse().find((el) => {
            const rect = el.getBoundingClientRect();
            return rect.top - containerRect.top < -threshold;
          });
          if (prev) {
            container.scrollBy({
              top: prev.getBoundingClientRect().top - containerRect.top,
              behavior: "smooth",
            });
          }
        }
      },
      []
    );

    const reportVisibleActiveFile = useCallback(() => {
      const container = containerRef.current;
      if (!container || !onActiveFileChange || fileRefs.current.size === 0) return;

      const containerTop = container.getBoundingClientRect().top;
      const activationOffset = 96;
      const sortedEntries = [...fileRefs.current.entries()].sort((a, b) => a[0] - b[0]);

      let nextActiveIndex = sortedEntries[0][0];

      for (const [index, element] of sortedEntries) {
        const topDelta = element.getBoundingClientRect().top - containerTop;
        if (topDelta <= activationOffset) {
          nextActiveIndex = index;
          continue;
        }
        break;
      }

      if (lastReportedActiveFileIndexRef.current === nextActiveIndex) return;

      lastReportedActiveFileIndexRef.current = nextActiveIndex;
      onActiveFileChange(nextActiveIndex);
    }, [onActiveFileChange]);

    const runScrollWork = useCallback(() => {
      scrollFrameRef.current = null;
      reportVisibleActiveFile();
      if (!onScrollMetricsChange || !containerRef.current) return;
      const scrollTop = containerRef.current.scrollTop;
      const delta = scrollTop - lastScrollTopRef.current;
      lastScrollTopRef.current = scrollTop;
      const direction =
        Math.abs(delta) < 4 ? "idle" : delta > 0 ? "down" : "up";
      onScrollMetricsChange({ scrollTop, direction });
    }, [onScrollMetricsChange, reportVisibleActiveFile]);

    const handleScroll = useCallback(() => {
      if (scrollFrameRef.current != null) return;
      scrollFrameRef.current = window.requestAnimationFrame(runScrollWork);
    }, [runScrollWork]);

    useEffect(() => {
      return () => {
        if (scrollFrameRef.current != null) {
          window.cancelAnimationFrame(scrollFrameRef.current);
        }
      };
    }, []);

    useEffect(() => {
      lastReportedActiveFileIndexRef.current = activeFileIndex ?? null;
    }, [activeFileIndex]);

    useEffect(() => {
      if (!resetScrollKey) return;
      const container = containerRef.current;
      if (!container) return;
      container.scrollTop = 0;
      lastScrollTopRef.current = 0;
    }, [resetScrollKey]);

    useImperativeHandle(ref, () => ({
      scrollToFile: (index: number) => {
        const el = fileRefs.current.get(index);
        if (el) {
          el.scrollIntoView({ behavior: "smooth", block: "start" });
        }
      },
      scrollToNextHunk: () => {
        scrollToAdjacentHunk("next");
      },
      scrollToPrevHunk: () => {
        scrollToAdjacentHunk("prev");
      },
    }), [scrollToAdjacentHunk]);

    if (files.length === 0) {
      return (
        <div className="flex-1 flex items-center justify-center py-12">
          <p className="text-sm text-muted-foreground">No diff available</p>
        </div>
      );
    }

    return (
      <div
        key={resetScrollKey ?? "all-files"}
        ref={containerRef}
        onScroll={handleScroll}
        className="flex-1 overflow-y-auto px-3 pb-3 pt-0 space-y-3 md:px-4 md:pb-4 md:pt-0 md:space-y-4"
      >
        {files.map((file, i) => (
          <FileDiffSection
            key={file.newPath}
            ref={setFileRef(i)}
            file={file}
            viewMode={viewMode}
            sessionId={sessionId}
            isActive
            commentsByLine={commentsByLine}
            activeCommentLine={activeCommentLine}
            onAddComment={onAddComment}
            onSubmitComment={onSubmitComment}
            onCancelComment={onCancelComment}
            onUpdateComment={onUpdateComment}
            onDeleteComment={onDeleteComment}
            onBrowseFile={onBrowseFile}
            contextUnavailable={contextUnavailable}
            onContextUnavailable={handleContextUnavailable}
            showInlineCommentComposer={showInlineCommentComposer}
            onRequestEditComment={onRequestEditComment}
          />
        ))}
      </div>
    );
  }
);
