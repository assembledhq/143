"use client";

import { useRef, useCallback, useImperativeHandle, forwardRef } from "react";
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
  commentsByLine?: Map<CommentLineKey, SessionReviewComment[]>;
  activeCommentLine?: ActiveCommentLine | null;
  onAddComment?: (filePath: string, lineNumber: number, side: "old" | "new") => void;
  onSubmitComment?: (body: string) => void;
  onCancelComment?: () => void;
  onUpdateComment?: (commentId: string, data: { body?: string; resolved?: boolean }) => void;
  onDeleteComment?: (commentId: string) => void;
  onBrowseFile?: (filePath: string) => void;
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
    commentsByLine,
    activeCommentLine,
    onAddComment,
    onSubmitComment,
    onCancelComment,
    onUpdateComment,
    onDeleteComment,
    onBrowseFile,
  }, ref) {
    const containerRef = useRef<HTMLDivElement>(null);
    const fileRefs = useRef<Map<number, HTMLDivElement>>(new Map());

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
      <div ref={containerRef} className="flex-1 overflow-y-auto p-4 space-y-4">
        {files.map((file, i) => (
          <FileDiffSection
            key={file.newPath}
            ref={setFileRef(i)}
            file={file}
            viewMode={viewMode}
            sessionId={sessionId}
            isActive={activeFileIndex === undefined || activeFileIndex === i}
            commentsByLine={commentsByLine}
            activeCommentLine={activeCommentLine}
            onAddComment={onAddComment}
            onSubmitComment={onSubmitComment}
            onCancelComment={onCancelComment}
            onUpdateComment={onUpdateComment}
            onDeleteComment={onDeleteComment}
            onBrowseFile={onBrowseFile}
          />
        ))}
      </div>
    );
  }
);
