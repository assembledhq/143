import { describe, it, expect, vi } from "vitest";
import { act, render, screen, fireEvent } from "@testing-library/react";
import { createRef, forwardRef } from "react";
import { DiffPane, type DiffPaneHandle } from "./diff-pane";
import type { DiffFile, DiffLine, DiffHunk } from "@/lib/diff-parser";
import type { SessionReviewComment } from "@/lib/types";
import type { CommentLineKey } from "@/hooks/use-review-comments";

// Mock FileDiffSection to avoid deep rendering
vi.mock("./file-diff-section", () => ({
  FileDiffSection: forwardRef<HTMLDivElement, {
    file: DiffFile;
    isActive: boolean;
    sessionId?: string;
    onBrowseFile?: (p: string) => void;
  }>(function MockFileDiffSection({ file, isActive, sessionId, onBrowseFile }, ref) {
    return (
      <div ref={ref} data-testid={`file-${file.newPath}`} data-active={isActive} data-session={sessionId}>
        {file.newPath}
        {onBrowseFile && <button data-testid={`browse-${file.newPath}`} onClick={() => onBrowseFile(file.newPath)}>Browse</button>}
      </div>
    );
  }),
}));

function makeLine(type: DiffLine["type"], content: string, oldLn: number | null, newLn: number | null): DiffLine {
  return { type, content, oldLineNumber: oldLn, newLineNumber: newLn };
}

function makeHunk(lines: DiffLine[]): DiffHunk {
  return {
    oldStart: 1,
    oldCount: lines.filter((l) => l.type !== "add").length,
    newStart: 1,
    newCount: lines.filter((l) => l.type !== "remove").length,
    header: "@@ -1,3 +1,4 @@",
    lines,
  };
}

function makeDiffFile(newPath: string): DiffFile {
  return {
    oldPath: newPath,
    newPath,
    hunks: [makeHunk([makeLine("context", "line 1", 1, 1)])],
    stats: { added: 1, removed: 0 },
    language: "typescript",
  };
}

describe("DiffPane", () => {
  it("shows empty state when no files", () => {
    render(<DiffPane files={[]} viewMode="unified" />);
    expect(screen.getByText("No diff available")).toBeInTheDocument();
  });

  it("renders files when provided", () => {
    const files = [makeDiffFile("src/a.ts"), makeDiffFile("src/b.ts")];
    render(<DiffPane files={files} viewMode="unified" />);
    expect(screen.getByText("src/a.ts")).toBeInTheDocument();
    expect(screen.getByText("src/b.ts")).toBeInTheDocument();
  });

  it("keeps the first file header flush with the toolbar by removing top padding from the scroll pane", () => {
    const { container } = render(<DiffPane files={[makeDiffFile("src/a.ts")]} viewMode="unified" />);
    const scrollContainer = container.firstElementChild;

    expect(scrollContainer).toHaveClass("pt-0");
    expect(scrollContainer).not.toHaveClass("p-3");
  });

  it("prevents horizontal diff overflow from propagating outside the pane", () => {
    const { container } = render(<DiffPane files={[makeDiffFile("src/a.ts")]} viewMode="unified" />);
    const scrollContainer = container.firstElementChild;

    expect(scrollContainer).toHaveClass("min-w-0", "max-w-full", "overflow-x-hidden");
  });

  it("renders a single file", () => {
    render(<DiffPane files={[makeDiffFile("index.ts")]} viewMode="split" />);
    expect(screen.getByTestId("file-index.ts")).toBeInTheDocument();
  });

  it("passes sessionId and callbacks to FileDiffSection", () => {
    const onBrowseFile = vi.fn();
    const onAddComment = vi.fn();
    const onSubmitComment = vi.fn();
    const onCancelComment = vi.fn();
    const onUpdateComment = vi.fn();
    const onDeleteComment = vi.fn();

    render(
      <DiffPane
        files={[makeDiffFile("src/main.ts")]}
        viewMode="unified"
        sessionId="sess-1"
        onBrowseFile={onBrowseFile}
        onAddComment={onAddComment}
        onSubmitComment={onSubmitComment}
        onCancelComment={onCancelComment}
        onUpdateComment={onUpdateComment}
        onDeleteComment={onDeleteComment}
      />
    );
    expect(screen.getByTestId("file-src/main.ts")).toHaveAttribute("data-session", "sess-1");
  });

  it("passes commentsByLine and activeCommentLine to children", () => {
    const commentsByLine = new Map<CommentLineKey, SessionReviewComment[]>();

    render(
      <DiffPane
        files={[makeDiffFile("src/test.ts")]}
        viewMode="split"
        commentsByLine={commentsByLine}
        activeCommentLine={{ filePath: "src/test.ts", lineNumber: 5, side: "new" }}
      />
    );
    expect(screen.getByTestId("file-src/test.ts")).toBeInTheDocument();
  });

  it("marks all files as active when activeFileIndex is undefined", () => {
    const files = [makeDiffFile("a.ts"), makeDiffFile("b.ts")];
    render(<DiffPane files={files} viewMode="unified" />);
    expect(screen.getByTestId("file-a.ts")).toHaveAttribute("data-active", "true");
    expect(screen.getByTestId("file-b.ts")).toHaveAttribute("data-active", "true");
  });

  it("keeps syntax highlighting enabled for every file when activeFileIndex is set", () => {
    const files = [makeDiffFile("a.ts"), makeDiffFile("b.ts")];
    render(<DiffPane files={files} viewMode="unified" activeFileIndex={1} />);
    expect(screen.getByTestId("file-a.ts")).toHaveAttribute("data-active", "true");
    expect(screen.getByTestId("file-b.ts")).toHaveAttribute("data-active", "true");
  });

  it("exposes scrollToFile via ref", () => {
    const ref = createRef<DiffPaneHandle>();
    const files = [makeDiffFile("a.ts"), makeDiffFile("b.ts")];
    const scrollIntoView = vi.fn();
    vi.spyOn(HTMLElement.prototype, "scrollIntoView").mockImplementation(scrollIntoView);
    render(<DiffPane ref={ref} files={files} viewMode="unified" />);

    // scrollToFile should not throw even if element is not found
    expect(() => ref.current?.scrollToFile(99)).not.toThrow();
    // scrollToFile with valid index should also not throw
    expect(() => ref.current?.scrollToFile(0)).not.toThrow();
    expect(scrollIntoView).toHaveBeenCalled();
  });

  it("exposes scrollToNextHunk and scrollToPrevHunk via ref", () => {
    const ref = createRef<DiffPaneHandle>();
    const files = [makeDiffFile("a.ts")];
    render(<DiffPane ref={ref} files={files} viewMode="unified" />);

    // These should not throw even with no hunk headers in the DOM
    expect(() => ref.current?.scrollToNextHunk()).not.toThrow();
    expect(() => ref.current?.scrollToPrevHunk()).not.toThrow();
  });

  it("scrollToNextHunk/scrollToPrevHunk handle hunk headers in DOM", () => {
    const ref = createRef<DiffPaneHandle>();
    const files = [makeDiffFile("a.ts")];

    const { container } = render(
      <DiffPane ref={ref} files={files} viewMode="unified" />
    );

    // Add fake hunk-header elements inside the container
    const scrollContainer = container.firstElementChild as HTMLDivElement;
    const header1 = document.createElement("div");
    header1.setAttribute("data-hunk-header", "true");
    const header2 = document.createElement("div");
    header2.setAttribute("data-hunk-header", "true");
    scrollContainer.appendChild(header1);
    scrollContainer.appendChild(header2);

    // Should not throw with headers present
    expect(() => ref.current?.scrollToNextHunk()).not.toThrow();
    expect(() => ref.current?.scrollToPrevHunk()).not.toThrow();
  });

  it("does not render empty state with ref on empty files", () => {
    const ref = createRef<DiffPaneHandle>();
    render(<DiffPane ref={ref} files={[]} viewMode="unified" />);
    expect(screen.getByText("No diff available")).toBeInTheDocument();
    // ref methods should still be available on empty files
    expect(() => ref.current?.scrollToFile(0)).not.toThrow();
    expect(() => ref.current?.scrollToNextHunk()).not.toThrow();
    expect(() => ref.current?.scrollToPrevHunk()).not.toThrow();
  });

  it("reports the topmost visible file when the diff pane scroll position changes", () => {
    const onActiveFileChange = vi.fn();
    const files = [makeDiffFile("a.ts"), makeDiffFile("b.ts")];
    let rafCallback: FrameRequestCallback | null = null;
    const requestAnimationFrameSpy = vi
      .spyOn(window, "requestAnimationFrame")
      .mockImplementation((callback) => {
        rafCallback = callback;
        return 1;
      });
    const cancelAnimationFrameSpy = vi
      .spyOn(window, "cancelAnimationFrame")
      .mockImplementation(() => {});
    const { container } = render(
      <DiffPane
        files={files}
        viewMode="unified"
        onActiveFileChange={onActiveFileChange}
      />
    );

    const scrollContainer = container.firstElementChild as HTMLDivElement;
    const firstFile = screen.getByTestId("file-a.ts");
    const secondFile = screen.getByTestId("file-b.ts");

    vi.spyOn(scrollContainer, "getBoundingClientRect").mockReturnValue({
      top: 0,
      bottom: 600,
      left: 0,
      right: 800,
      width: 800,
      height: 600,
      x: 0,
      y: 0,
      toJSON: () => ({}),
    });

    vi.spyOn(firstFile, "getBoundingClientRect").mockReturnValue({
      top: -220,
      bottom: 80,
      left: 0,
      right: 800,
      width: 800,
      height: 300,
      x: 0,
      y: -220,
      toJSON: () => ({}),
    });

    vi.spyOn(secondFile, "getBoundingClientRect").mockReturnValue({
      top: 24,
      bottom: 324,
      left: 0,
      right: 800,
      width: 800,
      height: 300,
      x: 0,
      y: 24,
      toJSON: () => ({}),
    });

    fireEvent.scroll(scrollContainer);
    act(() => {
      rafCallback?.(0);
    });

    expect(onActiveFileChange).toHaveBeenCalledWith(1);

    requestAnimationFrameSpy.mockRestore();
    cancelAnimationFrameSpy.mockRestore();
  });

  it("defers scroll visibility work to animation frames", () => {
    const onActiveFileChange = vi.fn();
    const files = [makeDiffFile("a.ts"), makeDiffFile("b.ts")];
    let rafCallback: FrameRequestCallback | null = null;
    const requestAnimationFrameSpy = vi
      .spyOn(window, "requestAnimationFrame")
      .mockImplementation((callback) => {
        rafCallback = callback;
        return 1;
      });
    const cancelAnimationFrameSpy = vi
      .spyOn(window, "cancelAnimationFrame")
      .mockImplementation(() => {});

    const { container } = render(
      <DiffPane
        files={files}
        viewMode="unified"
        onActiveFileChange={onActiveFileChange}
      />
    );

    const scrollContainer = container.firstElementChild as HTMLDivElement;
    const firstFile = screen.getByTestId("file-a.ts");
    const secondFile = screen.getByTestId("file-b.ts");

    vi.spyOn(scrollContainer, "getBoundingClientRect").mockReturnValue({
      top: 0,
      bottom: 600,
      left: 0,
      right: 800,
      width: 800,
      height: 600,
      x: 0,
      y: 0,
      toJSON: () => ({}),
    });

    vi.spyOn(firstFile, "getBoundingClientRect").mockReturnValue({
      top: -220,
      bottom: 80,
      left: 0,
      right: 800,
      width: 800,
      height: 300,
      x: 0,
      y: -220,
      toJSON: () => ({}),
    });

    vi.spyOn(secondFile, "getBoundingClientRect").mockReturnValue({
      top: 24,
      bottom: 324,
      left: 0,
      right: 800,
      width: 800,
      height: 300,
      x: 0,
      y: 24,
      toJSON: () => ({}),
    });

    fireEvent.scroll(scrollContainer);

    expect(onActiveFileChange).not.toHaveBeenCalled();
    expect(requestAnimationFrameSpy).toHaveBeenCalledTimes(1);

    act(() => {
      rafCallback?.(0);
    });

    expect(onActiveFileChange).toHaveBeenCalledWith(1);

    requestAnimationFrameSpy.mockRestore();
    cancelAnimationFrameSpy.mockRestore();
  });

  it("replaces the scroll container when resetScrollKey changes so mobile file switches do not keep a stale offset", () => {
    const { container, rerender } = render(
      <DiffPane
        files={[makeDiffFile("a.ts")]}
        viewMode="unified"
        resetScrollKey="a.ts"
      />
    );

    const firstScrollContainer = container.firstElementChild as HTMLDivElement;
    firstScrollContainer.scrollTop = 480;

    rerender(
      <DiffPane
        files={[makeDiffFile("b.ts")]}
        viewMode="unified"
        resetScrollKey="b.ts"
      />
    );

    const secondScrollContainer = container.firstElementChild as HTMLDivElement;

    expect(secondScrollContainer).not.toBe(firstScrollContainer);
    expect(secondScrollContainer.scrollTop).toBe(0);
  });
});
