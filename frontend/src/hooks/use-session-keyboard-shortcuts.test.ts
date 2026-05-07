import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { renderHook } from "@testing-library/react";
import { useSessionKeyboardShortcuts } from "./use-session-keyboard-shortcuts";

function makeOptions(overrides = {}) {
  return {
    enabled: true,
    reviewMode: false,
    onShowHelp: vi.fn(),
    onFocusComposer: vi.fn(),
    transcript: {
      focus: vi.fn(),
      scrollByStep: vi.fn(),
      scrollByPage: vi.fn(),
      scrollToTop: vi.fn(),
      scrollToLatest: vi.fn(),
    },
    agentTabs: {
      activeIndex: 0,
      count: 3,
      onChange: vi.fn(),
      onAdd: vi.fn(),
    },
    details: {
      open: true,
      required: false,
      activeTab: "overview" as const,
      availableTabs: ["overview", "changes", "validation", "preview"] as const,
      onToggle: vi.fn(),
      onClose: vi.fn(),
      onTabChange: vi.fn(),
      onOpenReview: vi.fn(),
      onExitReview: vi.fn(),
    },
    pr: {
      canCreate: true,
      canView: true,
      canPush: true,
      canFixTests: true,
      canResolveConflicts: true,
      canMerge: true,
      onCreate: vi.fn(),
      onView: vi.fn(),
      onPush: vi.fn(),
      onFixTests: vi.fn(),
      onResolveConflicts: vi.fn(),
      onMerge: vi.fn(),
    },
    ...overrides,
  };
}

function pressKey(key: string, extras: Partial<KeyboardEventInit> = {}) {
  document.dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true, ...extras }));
}

describe("useSessionKeyboardShortcuts", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("opens help and focuses the composer with single-key shortcuts", () => {
    const opts = makeOptions();
    renderHook(() => useSessionKeyboardShortcuts(opts));

    pressKey("?");
    pressKey("i");

    expect(opts.onShowHelp).toHaveBeenCalledTimes(1);
    expect(opts.onFocusComposer).toHaveBeenCalledTimes(1);
  });

  it("scrolls the transcript with standard reading keys", () => {
    const opts = makeOptions();
    renderHook(() => useSessionKeyboardShortcuts(opts));

    pressKey("ArrowDown");
    pressKey("ArrowUp");
    pressKey("PageDown");
    pressKey("PageUp");
    pressKey("Home");
    pressKey("End");
    pressKey(".");
    pressKey(" ");
    pressKey(" ", { shiftKey: true });

    expect(opts.transcript.scrollByStep).toHaveBeenCalledWith(1);
    expect(opts.transcript.scrollByStep).toHaveBeenCalledWith(-1);
    expect(opts.transcript.scrollByPage).toHaveBeenCalledWith(1);
    expect(opts.transcript.scrollByPage).toHaveBeenCalledWith(-1);
    expect(opts.transcript.scrollToTop).toHaveBeenCalledTimes(1);
    expect(opts.transcript.scrollToLatest).toHaveBeenCalledTimes(2);
    expect(opts.transcript.scrollByPage).toHaveBeenCalledWith(-1);
  });

  it("cycles agent tabs and adds a tab", () => {
    const opts = makeOptions();
    renderHook(() => useSessionKeyboardShortcuts(opts));

    pressKey("]");
    pressKey("[");
    pressKey("Tab", { ctrlKey: true });
    pressKey("Tab", { ctrlKey: true, shiftKey: true });
    pressKey("t");

    expect(opts.agentTabs.onChange).toHaveBeenNthCalledWith(1, 1);
    expect(opts.agentTabs.onChange).toHaveBeenNthCalledWith(2, 2);
    expect(opts.agentTabs.onChange).toHaveBeenNthCalledWith(3, 1);
    expect(opts.agentTabs.onChange).toHaveBeenNthCalledWith(4, 2);
    expect(opts.agentTabs.onAdd).toHaveBeenCalledTimes(1);
  });

  it("toggles details, cycles detail tabs, and exits details with Escape", () => {
    const opts = makeOptions();
    renderHook(() => useSessionKeyboardShortcuts(opts));

    pressKey("d");
    pressKey("]", { shiftKey: true });
    pressKey("[", { shiftKey: true });
    pressKey("r");
    pressKey("Escape");

    expect(opts.details.onToggle).toHaveBeenCalledTimes(1);
    expect(opts.details.onTabChange).toHaveBeenNthCalledWith(1, "changes");
    expect(opts.details.onTabChange).toHaveBeenNthCalledWith(2, "preview");
    expect(opts.details.onOpenReview).toHaveBeenCalledTimes(1);
    expect(opts.details.onClose).toHaveBeenCalledTimes(1);
  });

  it("routes PR sequences through the provided action callbacks", () => {
    const opts = makeOptions();
    renderHook(() => useSessionKeyboardShortcuts(opts));

    pressKey("p");
    pressKey("c");
    pressKey("p");
    pressKey("v");
    pressKey("p");
    pressKey("p");
    pressKey("p");
    pressKey("t");
    pressKey("p");
    pressKey("r");
    pressKey("p");
    pressKey("m");

    expect(opts.pr.onCreate).toHaveBeenCalledTimes(1);
    expect(opts.pr.onView).toHaveBeenCalledTimes(1);
    expect(opts.pr.onPush).toHaveBeenCalledTimes(1);
    expect(opts.pr.onFixTests).toHaveBeenCalledTimes(1);
    expect(opts.pr.onResolveConflicts).toHaveBeenCalledTimes(1);
    expect(opts.pr.onMerge).toHaveBeenCalledTimes(1);
  });

  it("does not steal keys from text entry or transient surfaces", () => {
    const opts = makeOptions();
    renderHook(() => useSessionKeyboardShortcuts(opts));
    const input = document.createElement("input");
    const dialog = document.createElement("div");
    dialog.setAttribute("role", "dialog");
    document.body.append(input, dialog);

    input.dispatchEvent(new KeyboardEvent("keydown", { key: "i", bubbles: true }));
    pressKey("d");

    expect(opts.onFocusComposer).not.toHaveBeenCalled();
    expect(opts.details.onToggle).not.toHaveBeenCalled();

    input.remove();
    dialog.remove();
  });

  it("does not steal arrow keys from focusable list navigation", () => {
    const opts = makeOptions();
    renderHook(() => useSessionKeyboardShortcuts(opts));
    const list = document.createElement("div");
    list.setAttribute("role", "list");
    list.tabIndex = 0;
    document.body.append(list);

    list.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowDown", bubbles: true }));

    expect(opts.transcript.scrollByStep).not.toHaveBeenCalled();

    list.remove();
  });

  it("keeps diff review shortcuts authoritative while still allowing composer focus", () => {
    const opts = makeOptions({ reviewMode: true });
    renderHook(() => useSessionKeyboardShortcuts(opts));

    pressKey("?");
    pressKey("]");
    pressKey("d");
    pressKey("p");
    pressKey("c");
    pressKey("i");

    expect(opts.onShowHelp).toHaveBeenCalledTimes(1);
    expect(opts.agentTabs.onChange).not.toHaveBeenCalled();
    expect(opts.details.onToggle).not.toHaveBeenCalled();
    expect(opts.pr.onCreate).not.toHaveBeenCalled();
    expect(opts.onFocusComposer).toHaveBeenCalledTimes(1);
  });

  it("clears an incomplete PR sequence after a short timeout", () => {
    const opts = makeOptions();
    renderHook(() => useSessionKeyboardShortcuts(opts));

    pressKey("p");
    vi.advanceTimersByTime(1300);
    pressKey("c");

    expect(opts.pr.onCreate).not.toHaveBeenCalled();
  });
});
