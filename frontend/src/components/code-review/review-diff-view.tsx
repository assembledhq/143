"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { FileCode2 } from "lucide-react";
import type { DiffFile } from "@/lib/diff-parser";
import type { SessionReviewComment } from "@/lib/types";
import type { CommentLineKey } from "@/hooks/use-review-comments";
import { Sheet, SheetContent, SheetDescription, SheetTitle } from "@/components/ui/sheet";
import { DiffToolbar } from "./diff-toolbar";
import { DiffPane, type DiffPaneHandle } from "./diff-pane";
import { RepoExplorer } from "./repo-explorer";
import { KeyboardHelpOverlay } from "./keyboard-help-overlay";
import { useDiffKeyboardNav } from "@/hooks/use-diff-keyboard-nav";
import type { ViewMode } from "./review-toolbar";
import { CommentInput } from "./comment-input";

interface ReviewDiffViewProps {
  sessionId: string;
  files: DiffFile[];
  allFiles: DiffFile[];
  activeFileIndex: number;
  onFileChange: (index: number) => void;
  onBack: () => void;
  isMobile?: boolean;
  onOpenFileList?: () => void;
  onOpenComposer?: () => void;
  commentsByLine: Map<CommentLineKey, SessionReviewComment[]>;
  activeCommentLine: {
    filePath: string;
    lineNumber: number;
    side: "old" | "new";
  } | null;
  onAddComment: (filePath: string, lineNumber: number, side: "old" | "new") => void;
  onSubmitComment: (body: string) => void;
  onCancelComment: () => void;
  onUpdateComment: (commentId: string, data: { body?: string; resolved?: boolean }) => void;
  onDeleteComment: (commentId: string) => void;
  /** Search query for filtering diff content */
  diffSearchQuery: string;
  onDiffSearchChange: (q: string) => void;
}

export function ReviewDiffView({
  sessionId,
  files,
  allFiles,
  activeFileIndex,
  onFileChange,
  onBack,
  commentsByLine,
  activeCommentLine,
  onAddComment,
  onSubmitComment,
  onCancelComment,
  onUpdateComment,
  onDeleteComment,
  diffSearchQuery,
  onDiffSearchChange,
  isMobile = false,
  onOpenFileList,
  onOpenComposer,
}: ReviewDiffViewProps) {
  const diffPaneRef = useRef<DiffPaneHandle>(null);
  const skipNextScrollToFileRef = useRef(false);
  const [showKeyboardHelp, setShowKeyboardHelp] = useState(false);
  const [explorerMode, setExplorerMode] = useState(false);
  const [explorerInitialPath, setExplorerInitialPath] = useState<string | undefined>(undefined);
  const [mobileChromeCollapsed, setMobileChromeCollapsed] = useState(false);
  const [editingComment, setEditingComment] = useState<SessionReviewComment | null>(null);
  const [viewMode, setViewMode] = useState<ViewMode>(() => {
    if (typeof window !== "undefined") {
      return (localStorage.getItem("diff-view-mode") as ViewMode) || "unified";
    }
    return "unified";
  });
  const activeFile = files[activeFileIndex] ?? files[0] ?? null;
  const displayFiles = isMobile ? (activeFile ? [activeFile] : []) : files;
  const filePositionLabel =
    files.length > 0 && activeFile
      ? `${activeFileIndex + 1} of ${files.length}`
      : "";
  const activeCommentLabel = activeCommentLine
    ? `${activeCommentLine.filePath}:${activeCommentLine.lineNumber}`
    : editingComment
      ? `${editingComment.file_path}:${editingComment.line_number}`
      : null;

  // Escape key exits review mode (when not in an input, comment, or explorer).
  // Check e.defaultPrevented to avoid conflicts with KeyboardHelpOverlay's own
  // Escape handler, which calls e.preventDefault() when closing the overlay.
  useEffect(() => {
    function handleEscape(e: KeyboardEvent) {
      if (e.key !== "Escape" || e.defaultPrevented) return;
      const target = e.target as HTMLElement;
      if (
        target.tagName === "INPUT" ||
        target.tagName === "TEXTAREA" ||
        target.isContentEditable
      ) {
        return;
      }
      if (!explorerMode && !activeCommentLine && !showKeyboardHelp) {
        e.preventDefault();
        onBack();
      }
    }
    document.addEventListener("keydown", handleEscape);
    return () => document.removeEventListener("keydown", handleEscape);
  }, [explorerMode, activeCommentLine, showKeyboardHelp, onBack]);

  const handleViewModeChange = useCallback((mode: ViewMode) => {
    if (isMobile) return;
    setViewMode(mode);
    if (typeof window !== "undefined") {
      localStorage.setItem("diff-view-mode", mode);
    }
  }, [isMobile]);

  const handleFileSelect = useCallback(
    (index: number) => {
      onFileChange(index);
    },
    [onFileChange]
  );

  // activeFileIndex can change from outside (sidebar file-tree click), so scroll via effect rather than only inside handleFileSelect.
  useEffect(() => {
    if (skipNextScrollToFileRef.current) {
      skipNextScrollToFileRef.current = false;
      return;
    }
    diffPaneRef.current?.scrollToFile(activeFileIndex);
  }, [activeFileIndex]);

  const handleVisibleFileChange = useCallback(
    (index: number) => {
      skipNextScrollToFileRef.current = true;
      onFileChange(index);
    },
    [onFileChange]
  );

  const toggleViewMode = useCallback(() => {
    handleViewModeChange(viewMode === "unified" ? "split" : "unified");
  }, [viewMode, handleViewModeChange]);

  const handleNextHunk = useCallback(() => {
    diffPaneRef.current?.scrollToNextHunk();
  }, []);

  const handlePrevHunk = useCallback(() => {
    diffPaneRef.current?.scrollToPrevHunk();
  }, []);

  const handleJumpToFile = useCallback(() => {
    diffPaneRef.current?.scrollToFile(activeFileIndex);
  }, [activeFileIndex]);

  const handlePrevFile = useCallback(() => {
    if (activeFileIndex <= 0) return;
    onFileChange(activeFileIndex - 1);
  }, [activeFileIndex, onFileChange]);

  const handleNextFile = useCallback(() => {
    if (activeFileIndex >= files.length - 1) return;
    onFileChange(activeFileIndex + 1);
  }, [activeFileIndex, files.length, onFileChange]);

  const toggleShowHelp = useCallback(() => {
    setShowKeyboardHelp((v) => !v);
  }, []);

  const handleBrowseRepo = useCallback(() => {
    setExplorerMode(true);
    setExplorerInitialPath(undefined);
  }, []);

  const handleBackFromExplorer = useCallback(() => {
    setExplorerMode(false);
    setExplorerInitialPath(undefined);
  }, []);

  const handleBrowseFile = useCallback((filePath: string) => {
    setExplorerInitialPath(filePath);
    setExplorerMode(true);
  }, []);

  const toggleExplorer = useCallback(() => {
    setExplorerMode((v) => !v);
    setExplorerInitialPath(undefined);
  }, []);

  const handleAddCommentOnSelectedLine = useCallback(() => {
    const activeFile = files[activeFileIndex];
    if (!activeFile) return;
    for (const hunk of activeFile.hunks) {
      for (const line of hunk.lines) {
        if (line.type === "add" && line.newLineNumber != null) {
          onAddComment(activeFile.newPath, line.newLineNumber, "new");
          return;
        }
        if (line.type === "remove" && line.oldLineNumber != null) {
          onAddComment(activeFile.newPath, line.oldLineNumber, "old");
          return;
        }
      }
    }
  }, [files, activeFileIndex, onAddComment]);

  // Keyboard navigation — "f" toggles file tree in sidebar, "m" goes back to chat
  useDiffKeyboardNav({
    fileCount: files.length,
    activeFileIndex,
    onFileChange: handleFileSelect,
    onToggleFileTree: () => {}, // File tree lives in sidebar, no-op in center
    onToggleViewMode: toggleViewMode,
    onSetViewMode: handleViewModeChange,
    onToggleMaximize: onBack, // "m" key exits review mode
    onNextHunk: handleNextHunk,
    onPrevHunk: handlePrevHunk,
    onJumpToFile: handleJumpToFile,
    onShowHelp: toggleShowHelp,
    onToggleExplorer: toggleExplorer,
    onAddCommentOnSelectedLine: handleAddCommentOnSelectedLine,
    enabled: activeCommentLine === null && !explorerMode,
  });

  const effectiveViewMode: ViewMode = isMobile ? "unified" : viewMode;
  const handleRequestEditComment = useCallback((comment: SessionReviewComment) => {
    setEditingComment(comment);
  }, []);
  const handleScrollMetricsChange = useCallback(
    ({ scrollTop, direction }: { scrollTop: number; direction: "up" | "down" | "idle" }) => {
      if (!isMobile) return;
      if (activeCommentLine || editingComment || showKeyboardHelp || scrollTop < 32) {
        setMobileChromeCollapsed(false);
        return;
      }
      if (direction === "down") {
        setMobileChromeCollapsed(true);
      } else if (direction === "up") {
        setMobileChromeCollapsed(false);
      }
    },
    [activeCommentLine, editingComment, isMobile, showKeyboardHelp]
  );

  // Explorer mode takes over
  if (explorerMode) {
    return (
      <div className="flex flex-col h-full">
        <RepoExplorer
          sessionId={sessionId}
          diffFiles={allFiles}
          onBack={handleBackFromExplorer}
          initialPath={explorerInitialPath}
        />
      </div>
    );
  }

  if (files.length === 0) {
    return (
      <div className="flex flex-col h-full">
        <DiffToolbar
          onBack={onBack}
          viewMode={effectiveViewMode}
          onViewModeChange={handleViewModeChange}
          isMobile={isMobile}
          mobileChromeCollapsed={mobileChromeCollapsed}
        />
        <div className="flex-1 flex items-center justify-center py-12">
          <div className="text-center space-y-2 max-w-[280px]">
            <FileCode2 className="h-8 w-8 text-muted-foreground/40 mx-auto" />
            <p className="text-sm font-medium text-muted-foreground">
              No changes to display
            </p>
            <p className="text-xs text-muted-foreground/60">
              Try adjusting your search or pass filter.
            </p>
          </div>
        </div>
      </div>
    );
  }

  return (
      <div className="flex flex-col h-full">
      <DiffToolbar
        onBack={onBack}
        viewMode={effectiveViewMode}
        onViewModeChange={handleViewModeChange}
        onBrowseRepo={handleBrowseRepo}
        searchQuery={diffSearchQuery}
        onSearchChange={onDiffSearchChange}
        isMobile={isMobile}
        filePath={activeFile?.newPath}
        filePositionLabel={filePositionLabel}
        onOpenFileList={isMobile ? onOpenFileList : undefined}
        onOpenComposer={isMobile ? onOpenComposer : undefined}
        onPrevFile={isMobile ? handlePrevFile : undefined}
        onNextFile={isMobile ? handleNextFile : undefined}
        canGoPrev={isMobile && activeFileIndex > 0}
        canGoNext={isMobile && activeFileIndex < files.length - 1}
        mobileChromeCollapsed={mobileChromeCollapsed}
      />
      <DiffPane
        ref={diffPaneRef}
        files={displayFiles}
        viewMode={effectiveViewMode}
        sessionId={sessionId}
        activeFileIndex={isMobile ? undefined : activeFileIndex}
        onActiveFileChange={isMobile ? undefined : handleVisibleFileChange}
        onScrollMetricsChange={handleScrollMetricsChange}
        commentsByLine={commentsByLine}
        activeCommentLine={activeCommentLine}
        onAddComment={onAddComment}
        onSubmitComment={onSubmitComment}
        onCancelComment={onCancelComment}
        onUpdateComment={onUpdateComment}
        onDeleteComment={onDeleteComment}
        onBrowseFile={handleBrowseFile}
        resetScrollKey={isMobile ? activeFile?.newPath : undefined}
        showInlineCommentComposer={!isMobile}
        onRequestEditComment={isMobile ? handleRequestEditComment : undefined}
      />
      {isMobile && (activeCommentLine || editingComment) ? (
        <Sheet
          open
          onOpenChange={(open) => {
            if (!open) {
              if (editingComment) {
                setEditingComment(null);
              } else {
                onCancelComment();
              }
            }
          }}
        >
          <SheetContent side="bottom" className="rounded-t-2xl px-0 pb-0 pt-4">
            <SheetTitle className="px-4 text-sm">
              {editingComment ? "Edit review comment" : "Add review comment"}
            </SheetTitle>
            <SheetDescription className="px-4 pb-3 text-xs">
              {activeCommentLabel ? `Comment on ${activeCommentLabel}` : "Comment on the selected diff line."}
            </SheetDescription>
            <div className="border-t border-border/60 bg-muted/10 px-3 py-3">
              <CommentInput
                className="mx-0"
                initialValue={editingComment?.body ?? ""}
                submitLabel={editingComment ? "Save" : "Add comment"}
                onSubmit={(body) => {
                  if (editingComment) {
                    onUpdateComment(editingComment.id, { body });
                    setEditingComment(null);
                    return;
                  }
                  onSubmitComment(body);
                }}
                onCancel={() => {
                  if (editingComment) {
                    setEditingComment(null);
                    return;
                  }
                  onCancelComment();
                }}
              />
            </div>
          </SheetContent>
        </Sheet>
      ) : null}
      <KeyboardHelpOverlay open={showKeyboardHelp} onClose={toggleShowHelp} />
    </div>
  );
}
