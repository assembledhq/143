import { useEffect, useRef } from "react";
import type { ViewMode } from "@/components/code-review/review-toolbar";

interface UseDiffKeyboardNavOptions {
  fileCount: number;
  activeFileIndex: number;
  onFileChange: (index: number) => void;
  onToggleFileTree: () => void;
  onToggleViewMode: () => void;
  onSetViewMode?: (mode: ViewMode) => void;
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
 *
 * Uses a ref to hold the latest options so the keydown listener is
 * attached only once (when enabled changes), avoiding re-registration
 * on every parent render.
 */
export function useDiffKeyboardNav(options: UseDiffKeyboardNavOptions) {
  const optionsRef = useRef(options);
  useEffect(() => {
    optionsRef.current = options;
  });

  useEffect(() => {
    if (!options.enabled) return;

    function handleKeyDown(e: KeyboardEvent) {
      const opts = optionsRef.current;

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

      function setViewModeOrToggle(mode: ViewMode) {
        if (opts.onSetViewMode) {
          opts.onSetViewMode(mode);
        } else {
          opts.onToggleViewMode();
        }
      }

      switch (e.key) {
        case "j":
          e.preventDefault();
          if (opts.activeFileIndex < opts.fileCount - 1) {
            opts.onFileChange(opts.activeFileIndex + 1);
          }
          break;
        case "k":
          e.preventDefault();
          if (opts.activeFileIndex > 0) {
            opts.onFileChange(opts.activeFileIndex - 1);
          }
          break;
        case "n":
          e.preventDefault();
          opts.onNextHunk();
          break;
        case "p":
          e.preventDefault();
          opts.onPrevHunk();
          break;
        case "Enter":
          e.preventDefault();
          opts.onJumpToFile();
          break;
        case "f":
          e.preventDefault();
          opts.onToggleFileTree();
          break;
        case "u":
          e.preventDefault();
          setViewModeOrToggle("unified");
          break;
        case "s":
          e.preventDefault();
          setViewModeOrToggle("split");
          break;
        case "m":
          e.preventDefault();
          opts.onToggleMaximize();
          break;
        case "e":
          e.preventDefault();
          opts.onToggleExplorer?.();
          break;
        case "c":
          e.preventDefault();
          opts.onAddCommentOnSelectedLine?.();
          break;
        case "x":
          e.preventDefault();
          opts.onExpandContext?.();
          break;
        case "?":
          e.preventDefault();
          opts.onShowHelp();
          break;
      }
    }

    document.addEventListener("keydown", handleKeyDown);
    return () => document.removeEventListener("keydown", handleKeyDown);
  }, [options.enabled]);
}
