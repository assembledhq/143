import { describe, it, expect, vi } from "vitest";
import { renderHook } from "@testing-library/react";
import { useDiffKeyboardNav } from "./use-diff-keyboard-nav";

function makeOptions(overrides = {}) {
  return {
    fileCount: 5,
    activeFileIndex: 2,
    onFileChange: vi.fn(),
    onToggleFileTree: vi.fn(),
    onToggleViewMode: vi.fn(),
    onSetViewMode: vi.fn(),
    onToggleMaximize: vi.fn(),
    onNextHunk: vi.fn(),
    onPrevHunk: vi.fn(),
    onJumpToFile: vi.fn(),
    onShowHelp: vi.fn(),
    onToggleExplorer: vi.fn(),
    onAddCommentOnSelectedLine: vi.fn(),
    onExpandContext: vi.fn(),
    enabled: true,
    ...overrides,
  };
}

function pressKey(key: string, extras: Partial<KeyboardEventInit> = {}) {
  document.dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true, ...extras }));
}

describe("useDiffKeyboardNav", () => {
  it("j moves to next file", () => {
    const opts = makeOptions();
    renderHook(() => useDiffKeyboardNav(opts));
    pressKey("j");
    expect(opts.onFileChange).toHaveBeenCalledWith(3);
  });

  it("k moves to previous file", () => {
    const opts = makeOptions();
    renderHook(() => useDiffKeyboardNav(opts));
    pressKey("k");
    expect(opts.onFileChange).toHaveBeenCalledWith(1);
  });

  it("j does not exceed file count", () => {
    const opts = makeOptions({ activeFileIndex: 4, fileCount: 5 });
    renderHook(() => useDiffKeyboardNav(opts));
    pressKey("j");
    expect(opts.onFileChange).not.toHaveBeenCalled();
  });

  it("k does not go below 0", () => {
    const opts = makeOptions({ activeFileIndex: 0 });
    renderHook(() => useDiffKeyboardNav(opts));
    pressKey("k");
    expect(opts.onFileChange).not.toHaveBeenCalled();
  });

  it("n triggers onNextHunk", () => {
    const opts = makeOptions();
    renderHook(() => useDiffKeyboardNav(opts));
    pressKey("n");
    expect(opts.onNextHunk).toHaveBeenCalled();
  });

  it("p triggers onPrevHunk", () => {
    const opts = makeOptions();
    renderHook(() => useDiffKeyboardNav(opts));
    pressKey("p");
    expect(opts.onPrevHunk).toHaveBeenCalled();
  });

  it("Enter triggers onJumpToFile", () => {
    const opts = makeOptions();
    renderHook(() => useDiffKeyboardNav(opts));
    pressKey("Enter");
    expect(opts.onJumpToFile).toHaveBeenCalled();
  });

  it("f triggers onToggleFileTree", () => {
    const opts = makeOptions();
    renderHook(() => useDiffKeyboardNav(opts));
    pressKey("f");
    expect(opts.onToggleFileTree).toHaveBeenCalled();
  });

  it("u calls onSetViewMode with unified", () => {
    const opts = makeOptions();
    renderHook(() => useDiffKeyboardNav(opts));
    pressKey("u");
    expect(opts.onSetViewMode).toHaveBeenCalledWith("unified");
  });

  it("s calls onSetViewMode with split", () => {
    const opts = makeOptions();
    renderHook(() => useDiffKeyboardNav(opts));
    pressKey("s");
    expect(opts.onSetViewMode).toHaveBeenCalledWith("split");
  });

  it("u falls back to onToggleViewMode when onSetViewMode is undefined", () => {
    const opts = makeOptions({ onSetViewMode: undefined });
    renderHook(() => useDiffKeyboardNav(opts));
    pressKey("u");
    expect(opts.onToggleViewMode).toHaveBeenCalled();
  });

  it("m triggers onToggleMaximize", () => {
    const opts = makeOptions();
    renderHook(() => useDiffKeyboardNav(opts));
    pressKey("m");
    expect(opts.onToggleMaximize).toHaveBeenCalled();
  });

  it("e triggers onToggleExplorer", () => {
    const opts = makeOptions();
    renderHook(() => useDiffKeyboardNav(opts));
    pressKey("e");
    expect(opts.onToggleExplorer).toHaveBeenCalled();
  });

  it("c triggers onAddCommentOnSelectedLine", () => {
    const opts = makeOptions();
    renderHook(() => useDiffKeyboardNav(opts));
    pressKey("c");
    expect(opts.onAddCommentOnSelectedLine).toHaveBeenCalled();
  });

  it("x triggers onExpandContext", () => {
    const opts = makeOptions();
    renderHook(() => useDiffKeyboardNav(opts));
    pressKey("x");
    expect(opts.onExpandContext).toHaveBeenCalled();
  });

  it("? triggers onShowHelp", () => {
    const opts = makeOptions();
    renderHook(() => useDiffKeyboardNav(opts));
    pressKey("?");
    expect(opts.onShowHelp).toHaveBeenCalled();
  });

  it("does nothing when disabled", () => {
    const opts = makeOptions({ enabled: false });
    renderHook(() => useDiffKeyboardNav(opts));
    pressKey("j");
    pressKey("n");
    expect(opts.onFileChange).not.toHaveBeenCalled();
    expect(opts.onNextHunk).not.toHaveBeenCalled();
  });

  it("ignores keys with modifier keys held", () => {
    const opts = makeOptions();
    renderHook(() => useDiffKeyboardNav(opts));
    pressKey("j", { ctrlKey: true });
    pressKey("j", { metaKey: true });
    pressKey("j", { altKey: true });
    expect(opts.onFileChange).not.toHaveBeenCalled();
  });

  it("ignores keys when target is an input element", () => {
    const opts = makeOptions();
    renderHook(() => useDiffKeyboardNav(opts));
    const input = document.createElement("input");
    document.body.appendChild(input);
    input.dispatchEvent(new KeyboardEvent("keydown", { key: "j", bubbles: true }));
    expect(opts.onFileChange).not.toHaveBeenCalled();
    document.body.removeChild(input);
  });

  it("ignores keys when target is a textarea", () => {
    const opts = makeOptions();
    renderHook(() => useDiffKeyboardNav(opts));
    const textarea = document.createElement("textarea");
    document.body.appendChild(textarea);
    textarea.dispatchEvent(new KeyboardEvent("keydown", { key: "j", bubbles: true }));
    expect(opts.onFileChange).not.toHaveBeenCalled();
    document.body.removeChild(textarea);
  });
});
