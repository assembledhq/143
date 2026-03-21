import { useEffect } from "react";

interface UseDiffKeyboardNavOptions {
  fileCount: number;
  activeFileIndex: number;
  onFileChange: (index: number) => void;
  onToggleFileTree: () => void;
  onToggleViewMode: () => void;
  onToggleMaximize: () => void;
  onNextHunk: () => void;
  onPrevHunk: () => void;
  onJumpToFile: () => void;
  onShowHelp: () => void;
  onToggleExplorer?: () => void;
  onAddCommentOnSelectedLine?: () => void;
  onExpandContext?: () => void;
  enabled: boolean;
}

/**
 * Keyboard navigation hook for the code review / Changes tab.
 * Only active when `enabled` is true. Ignores events when focus is
 * inside a textarea or input.
 */
export function useDiffKeyboardNav({
  fileCount,
  activeFileIndex,
  onFileChange,
  onToggleFileTree,
  onToggleViewMode,
  onToggleMaximize,
  onNextHunk,
  onPrevHunk,
  onJumpToFile,
  onShowHelp,
  onToggleExplorer,
  onAddCommentOnSelectedLine,
  onExpandContext,
  enabled,
}: UseDiffKeyboardNavOptions) {
  useEffect(() => {
    if (!enabled) return;

    function handleKeyDown(e: KeyboardEvent) {
      // Don't capture when typing in an input/textarea
      const target = e.target as HTMLElement;
      if (
        target.tagName === "INPUT" ||
        target.tagName === "TEXTAREA" ||
        target.isContentEditable
      ) {
        return;
      }

      // Don't capture when modifier keys are held (avoid conflicts with browser/system shortcuts)
      if (e.ctrlKey || e.metaKey || e.altKey) {
        return;
      }

      switch (e.key) {
        case "j":
          e.preventDefault();
          if (activeFileIndex < fileCount - 1) {
            onFileChange(activeFileIndex + 1);
          }
          break;
        case "k":
          e.preventDefault();
          if (activeFileIndex > 0) {
            onFileChange(activeFileIndex - 1);
          }
          break;
        case "n":
          e.preventDefault();
          onNextHunk();
          break;
        case "p":
          e.preventDefault();
          onPrevHunk();
          break;
        case "Enter":
          e.preventDefault();
          onJumpToFile();
          break;
        case "f":
          e.preventDefault();
          onToggleFileTree();
          break;
        case "u":
        case "s":
          e.preventDefault();
          onToggleViewMode();
          break;
        case "m":
          e.preventDefault();
          onToggleMaximize();
          break;
        case "e":
          e.preventDefault();
          onToggleExplorer?.();
          break;
        case "c":
          e.preventDefault();
          onAddCommentOnSelectedLine?.();
          break;
        case "x":
          e.preventDefault();
          onExpandContext?.();
          break;
        case "?":
          e.preventDefault();
          onShowHelp();
          break;
      }
    }

    document.addEventListener("keydown", handleKeyDown);
    return () => document.removeEventListener("keydown", handleKeyDown);
  }, [
    enabled,
    fileCount,
    activeFileIndex,
    onFileChange,
    onToggleFileTree,
    onToggleViewMode,
    onToggleMaximize,
    onNextHunk,
    onPrevHunk,
    onJumpToFile,
    onShowHelp,
    onToggleExplorer,
    onAddCommentOnSelectedLine,
    onExpandContext,
  ]);
}
