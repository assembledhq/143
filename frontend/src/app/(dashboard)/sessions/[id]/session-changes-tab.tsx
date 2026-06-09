"use client";

import { memo, useCallback } from "react";
import { AlertTriangle, FileCode2 } from "lucide-react";

import { Button } from "@/components/ui/button";
import { CommentsSummary, FileTree, PassSelector, type DiffPassEntry, type PassRange } from "@/components/code-review";
import type { DiffFile } from "@/lib/diff-parser";
import type { SessionReviewComment } from "@/lib/types";

export const ChangesTab = memo(function ChangesTab({
  filteredFiles,
  activeFileIndex,
  onFileSelect,
  onOpenReview,
  comments,
  onCommentClick,
  passes,
  passRange,
  onPassRangeChange,
  emptyStatusText,
  isMobile,
  diffLoadErrorText,
  diffTruncationText,
  onRetryDiffLoad,
}: {
  filteredFiles: DiffFile[];
  activeFileIndex: number;
  onFileSelect: (index: number) => void;
  onOpenReview: (fileIndex?: number) => void;
  comments: SessionReviewComment[];
  onCommentClick: (filePath: string) => void;
  passes: DiffPassEntry[];
  passRange: PassRange | null;
  onPassRangeChange: (range: PassRange | null) => void;
  emptyStatusText: string;
  isMobile: boolean;
  diffLoadErrorText?: string;
  diffTruncationText?: string;
  onRetryDiffLoad?: () => void;
}) {
  const hasDiff = filteredFiles.length > 0;
  const hasDiffLoadError = !!diffLoadErrorText;

  const handleFileClick = useCallback(
    (index: number) => {
      onFileSelect(index);
      onOpenReview(index);
    },
    [onFileSelect, onOpenReview],
  );

  return (
    <div className="flex h-full flex-col">
      {passes.length >= 2 && (
        <div className="border-b border-border px-4 py-3">
          <PassSelector
            passes={passes}
            selectedRange={passRange}
            onRangeChange={onPassRangeChange}
          />
        </div>
      )}

      {comments.length > 0 && (
        <CommentsSummary
          comments={comments}
          onCommentClick={onCommentClick}
        />
      )}

      {hasDiff ? (
        <div className="flex min-h-0 flex-1 flex-col">
          {diffTruncationText ? (
            <div className="mx-4 mt-3 rounded-md border border-amber-200 bg-amber-50 px-3 py-2 text-xs text-amber-950 dark:border-amber-900/50 dark:bg-amber-950/20 dark:text-amber-100">
              <p className="font-medium">Large diff truncated</p>
              <p className="mt-1 text-amber-900/80 dark:text-amber-100/80">{diffTruncationText}</p>
            </div>
          ) : null}
          <div className="flex-1 overflow-hidden">
            <FileTree
              files={filteredFiles}
              activeFileIndex={activeFileIndex}
              onFileSelect={handleFileClick}
              variant={isMobile ? "sheet" : "sidebar"}
            />
          </div>
        </div>
      ) : (
        <div className="flex flex-1 items-center justify-center py-12">
          <div className="max-w-[280px] space-y-2 text-center">
            {hasDiffLoadError ? (
              <AlertTriangle className="mx-auto h-8 w-8 text-destructive/70" />
            ) : (
              <FileCode2 className="mx-auto h-8 w-8 text-muted-foreground/40" />
            )}
            <p className="text-xs font-medium text-muted-foreground">
              {hasDiffLoadError ? "Couldn't load changes" : "No changes yet"}
            </p>
            <p className="text-xs text-muted-foreground/60">
              {diffLoadErrorText ?? emptyStatusText}
            </p>
            {hasDiffLoadError && onRetryDiffLoad ? (
              <Button type="button" variant="outline" size="sm" className="mt-2" onClick={onRetryDiffLoad}>
                Retry
              </Button>
            ) : null}
          </div>
        </div>
      )}
    </div>
  );
});

ChangesTab.displayName = "ChangesTab";
