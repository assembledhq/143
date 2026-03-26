"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { FileCode2 } from "lucide-react";
import type { DiffFile } from "@/lib/diff-parser";
import type { SessionReviewComment } from "@/lib/types";
import type { CommentLineKey } from "@/hooks/use-review-comments";
import { DiffToolbar } from "./diff-toolbar";
import { DiffPane, type DiffPaneHandle } from "./diff-pane";
import { RepoExplorer } from "./repo-explorer";
import { KeyboardHelpOverlay } from "./keyboard-help-overlay";
import { useDiffKeyboardNav } from "@/hooks/use-diff-keyboard-nav";
import type { ViewMode } from "./review-toolbar";

interface ReviewDiffViewProps {
  sessionId: string;
  files: DiffFile[];
  allFiles: DiffFile[];
  activeFileIndex: number;
  onFileChange: (index: number) => void;
  onBack: () => void;
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
}: ReviewDiffViewProps) {
  const diffPaneRef = useRef<DiffPaneHandle>(null);
  const [showKeyboardHelp, setShowKeyboardHelp] = useState(false);
  const [explorerMode, setExplorerMode] = useState(false);
  const [explorerInitialPath, setExplorerInitialPath] = useState<string | undefined>(undefined);
  const [viewMode, setViewMode] = useState<ViewMode>(() => {
    if (typeof window !== "undefined") {
      return (localStorage.getItem("diff-view-mode") as ViewMode) || "unified";
    }
    return "unified";
  });

  // Escape key exits review mode (when not in an input, comment, or explorer)
  useEffect(() => {
    function handleEscape(e: KeyboardEvent) {
      if (e.key !== "Escape") return;
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
    setViewMode(mode);
    if (typeof window !== "undefined") {
      localStorage.setItem("diff-view-mode", mode);
    }
  }, []);

  const handleFileSelect = useCallback(
    (index: number) => {
      onFileChange(index);
      diffPaneRef.current?.scrollToFile(index);
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
          viewMode={viewMode}
          onViewModeChange={handleViewModeChange}
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
        viewMode={viewMode}
        onViewModeChange={handleViewModeChange}
        onBrowseRepo={handleBrowseRepo}
        searchQuery={diffSearchQuery}
        onSearchChange={onDiffSearchChange}
      />
      <DiffPane
        ref={diffPaneRef}
        files={files}
        viewMode={viewMode}
        sessionId={sessionId}
        activeFileIndex={activeFileIndex}
        commentsByLine={commentsByLine}
        activeCommentLine={activeCommentLine}
        onAddComment={onAddComment}
        onSubmitComment={onSubmitComment}
        onCancelComment={onCancelComment}
        onUpdateComment={onUpdateComment}
        onDeleteComment={onDeleteComment}
        onBrowseFile={handleBrowseFile}
      />
      <KeyboardHelpOverlay open={showKeyboardHelp} onClose={toggleShowHelp} />
    </div>
  );
}
