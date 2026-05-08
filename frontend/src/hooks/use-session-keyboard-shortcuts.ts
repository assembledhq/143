import { useEffect, useRef } from "react";

export type SessionDetailTab = "overview" | "changes" | "preview";

type TranscriptControls = {
  focus: () => void;
  scrollByStep: (direction: 1 | -1) => void;
  scrollByPage: (direction: 1 | -1) => void;
  scrollToTop: () => void;
  scrollToLatest: () => void;
};

type AgentTabControls = {
  activeIndex: number;
  count: number;
  onChange: (index: number) => void;
  onAdd: () => void;
};

type DetailControls = {
  open: boolean;
  required: boolean;
  activeTab: SessionDetailTab;
  availableTabs: readonly SessionDetailTab[];
  onToggle: () => void;
  onClose: () => void;
  onTabChange: (tab: SessionDetailTab) => void;
  onOpenReview: () => void;
  onExitReview: () => void;
};

type PullRequestControls = {
  canCreate: boolean;
  canView: boolean;
  canPush: boolean;
  canFixTests: boolean;
  canResolveConflicts: boolean;
  canMerge: boolean;
  onCreate: () => void;
  onView: () => void;
  onPush: () => void;
  onFixTests: () => void;
  onResolveConflicts: () => void;
  onMerge: () => void;
};

export type UseSessionKeyboardShortcutsOptions = {
  enabled: boolean;
  reviewMode?: boolean;
  onShowHelp: () => void;
  onFocusComposer: () => void;
  transcript?: TranscriptControls | null;
  agentTabs?: AgentTabControls | null;
  details?: DetailControls | null;
  pr?: PullRequestControls | null;
};

const PR_SEQUENCE_TIMEOUT_MS = 1200;

export function isSessionKeyboardTextEntryTarget(target: EventTarget | null): boolean {
  if (!(target instanceof HTMLElement)) {
    return false;
  }
  if (target.isContentEditable) {
    return true;
  }
  return !!target.closest("input, textarea, select, [contenteditable='true'], [cmdk-input]");
}

export function hasSessionKeyboardTransientSurface(): boolean {
  return !!document.querySelector("[role='dialog'], [role='menu']");
}

function isFocusedTranscript(target: EventTarget | null): boolean {
  return target instanceof HTMLElement && !!target.closest("[data-session-transcript-scroll='true']");
}

function shouldPreserveFocusedControl(event: KeyboardEvent): boolean {
  const target = event.target;
  if (!(target instanceof HTMLElement)) {
    return false;
  }
  if (isFocusedTranscript(target)) {
    return false;
  }
  if (event.key === " " && target.closest("button, a, [role='button'], [role='menuitem'], [role='tab']")) {
    return true;
  }
  if (
    (event.key === "ArrowLeft" ||
      event.key === "ArrowRight" ||
      event.key === "ArrowUp" ||
      event.key === "ArrowDown" ||
      event.key === "Home" ||
      event.key === "End") &&
    target.closest("button, a, [role='tab'], [role='tablist'], [role='menuitem'], [role='list'], [role='listbox'], [role='tree'], [role='grid']")
  ) {
    return true;
  }
  return false;
}

function wrapIndex(index: number, count: number): number {
  if (count <= 0) {
    return 0;
  }
  return (index + count) % count;
}

function nextDetailTab(details: DetailControls, direction: 1 | -1): SessionDetailTab {
  const currentIndex = Math.max(0, details.availableTabs.indexOf(details.activeTab));
  return details.availableTabs[wrapIndex(currentIndex + direction, details.availableTabs.length)] ?? details.activeTab;
}

export function useSessionKeyboardShortcuts(options: UseSessionKeyboardShortcutsOptions) {
  const optionsRef = useRef(options);
  const prSequenceTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const pendingPRSequenceRef = useRef(false);

  useEffect(() => {
    optionsRef.current = options;
  });

  useEffect(() => {
    if (!options.enabled) {
      return;
    }

    function clearPRSequence() {
      pendingPRSequenceRef.current = false;
      if (prSequenceTimerRef.current) {
        clearTimeout(prSequenceTimerRef.current);
        prSequenceTimerRef.current = null;
      }
    }

    function beginPRSequence() {
      clearPRSequence();
      pendingPRSequenceRef.current = true;
      prSequenceTimerRef.current = setTimeout(clearPRSequence, PR_SEQUENCE_TIMEOUT_MS);
    }

    function runPRSequence(key: string): boolean {
      const pr = optionsRef.current.pr;
      if (!pr) {
        return false;
      }
      if (key === "c" && pr.canCreate) {
        pr.onCreate();
        return true;
      }
      if (key === "v" && pr.canView) {
        pr.onView();
        return true;
      }
      if (key === "p" && pr.canPush) {
        pr.onPush();
        return true;
      }
      if (key === "t" && pr.canFixTests) {
        pr.onFixTests();
        return true;
      }
      if (key === "r" && pr.canResolveConflicts) {
        pr.onResolveConflicts();
        return true;
      }
      if (key === "m" && pr.canMerge) {
        pr.onMerge();
        return true;
      }
      return false;
    }

    function handleKeyDown(event: KeyboardEvent) {
      const opts = optionsRef.current;
      if (!opts.enabled || isSessionKeyboardTextEntryTarget(event.target)) {
        return;
      }
      if (hasSessionKeyboardTransientSurface()) {
        clearPRSequence();
        return;
      }

      if (pendingPRSequenceRef.current) {
        if (event.metaKey || event.ctrlKey || event.altKey || event.shiftKey) {
          clearPRSequence();
          return;
        }
        const handled = runPRSequence(event.key);
        clearPRSequence();
        if (handled) {
          event.preventDefault();
        }
        return;
      }

      if (event.key === "?" && !event.metaKey && !event.ctrlKey && !event.altKey) {
        event.preventDefault();
        opts.onShowHelp();
        return;
      }

      if (opts.reviewMode) {
        if (event.key === "i" && !event.metaKey && !event.ctrlKey && !event.altKey) {
          event.preventDefault();
          opts.onFocusComposer();
        }
        return;
      }

      if (shouldPreserveFocusedControl(event)) {
        return;
      }

      if (event.key === "i" && !event.metaKey && !event.ctrlKey && !event.altKey) {
        event.preventDefault();
        opts.onFocusComposer();
        return;
      }

      if (event.key === "p" && !event.metaKey && !event.ctrlKey && !event.altKey && !event.shiftKey) {
        event.preventDefault();
        beginPRSequence();
        return;
      }

      const transcript = opts.transcript;
      if (transcript && !event.metaKey && !event.ctrlKey && !event.altKey) {
        if (event.key === "ArrowDown") {
          event.preventDefault();
          transcript.scrollByStep(1);
          return;
        }
        if (event.key === "ArrowUp") {
          event.preventDefault();
          transcript.scrollByStep(-1);
          return;
        }
        if (event.key === "PageDown") {
          event.preventDefault();
          transcript.scrollByPage(1);
          return;
        }
        if (event.key === "PageUp") {
          event.preventDefault();
          transcript.scrollByPage(-1);
          return;
        }
        if (event.key === "Home") {
          event.preventDefault();
          transcript.scrollToTop();
          return;
        }
        if (event.key === "End" || event.key === ".") {
          event.preventDefault();
          transcript.scrollToLatest();
          return;
        }
        if (event.key === " ") {
          event.preventDefault();
          transcript.scrollByPage(event.shiftKey ? -1 : 1);
          return;
        }
      }

      const agentTabs = opts.agentTabs;
      if (agentTabs && agentTabs.count > 1) {
        if ((event.key === "]" && !event.shiftKey) || (event.key === "Tab" && event.ctrlKey && !event.shiftKey)) {
          event.preventDefault();
          agentTabs.onChange(wrapIndex(agentTabs.activeIndex + 1, agentTabs.count));
          return;
        }
        if ((event.key === "[" && !event.shiftKey) || (event.key === "Tab" && event.ctrlKey && event.shiftKey)) {
          event.preventDefault();
          agentTabs.onChange(wrapIndex(agentTabs.activeIndex - 1, agentTabs.count));
          return;
        }
      }
      if (agentTabs && event.key === "t" && !event.metaKey && !event.ctrlKey && !event.altKey && !event.shiftKey) {
        event.preventDefault();
        agentTabs.onAdd();
        return;
      }

      const details = opts.details;
      if (details) {
        if (event.key === "d" && !event.metaKey && !event.ctrlKey && !event.altKey && !event.shiftKey) {
          event.preventDefault();
          details.onToggle();
          return;
        }
        if (event.key === "]" && event.shiftKey && !event.metaKey && !event.ctrlKey && !event.altKey) {
          event.preventDefault();
          details.onTabChange(nextDetailTab(details, 1));
          return;
        }
        if (event.key === "[" && event.shiftKey && !event.metaKey && !event.ctrlKey && !event.altKey) {
          event.preventDefault();
          details.onTabChange(nextDetailTab(details, -1));
          return;
        }
        if (event.key === "r" && !event.metaKey && !event.ctrlKey && !event.altKey && !event.shiftKey) {
          event.preventDefault();
          details.onOpenReview();
          return;
        }
        if (event.key === "Escape" && details.open && !details.required) {
          event.preventDefault();
          details.onClose();
        }
      }
    }

    document.addEventListener("keydown", handleKeyDown);
    return () => {
      document.removeEventListener("keydown", handleKeyDown);
      clearPRSequence();
    };
  }, [options.enabled]);
}
