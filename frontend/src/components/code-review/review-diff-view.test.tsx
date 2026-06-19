import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, act } from "@testing-library/react";
import { forwardRef, useImperativeHandle, useState } from "react";
import { ReviewDiffView } from "./review-diff-view";
import type { DiffFile } from "@/lib/diff-parser";
import type { CommentLineKey } from "@/hooks/use-review-comments";
import type { SessionReviewComment } from "@/lib/types";

const mockDiffPaneRender = vi.fn();
const mockScrollToFile = vi.fn();
const mockScrollToNextHunk = vi.fn();
const mockScrollToPrevHunk = vi.fn();

// Mock child components to isolate unit under test
vi.mock("./diff-toolbar", () => ({
  DiffToolbar: (props: Record<string, unknown>) => (
    <div data-testid="diff-toolbar">
      <button onClick={props.onBack as () => void}>Back</button>
      <button onClick={() => (props.onViewModeChange as (m: string) => void)("split")}>
        ChangeView
      </button>
      {props.onPrevFile ? (
        <button onClick={props.onPrevFile as () => void}>PrevFile</button>
      ) : null}
      {props.onNextFile ? (
        <button onClick={props.onNextFile as () => void}>NextFile</button>
      ) : null}
      {props.onOpenFileList ? (
        <button onClick={props.onOpenFileList as () => void}>OpenFilesList</button>
      ) : null}
      {props.onBrowseRepo ? (
        <button onClick={props.onBrowseRepo as () => void}>Browse</button>
      ) : null}
      <span data-testid="view-mode">{String(props.viewMode)}</span>
      <span data-testid="toolbar-mobile-mode">{String(props.isMobile)}</span>
      <span data-testid="toolbar-file-path">{String(props.filePath ?? "")}</span>
      <span data-testid="toolbar-file-position">{String(props.filePositionLabel ?? "")}</span>
    </div>
  ),
}));

vi.mock("./diff-pane", () => ({
  DiffPane: forwardRef(function MockDiffPane(props: Record<string, unknown>, ref) {
    mockDiffPaneRender(props);
    useImperativeHandle(ref, () => ({
      scrollToFile: mockScrollToFile,
      scrollToNextHunk: mockScrollToNextHunk,
      scrollToPrevHunk: mockScrollToPrevHunk,
    }));

    return (
      <div data-testid="diff-pane">
        <span data-testid="pane-view-mode">{String(props.viewMode)}</span>
        <button onClick={() => (props.onBrowseFile as (p: string) => void)("test.ts")}>
          BrowseFile
        </button>
        <button onClick={() => (props.onActiveFileChange as (index: number) => void)(1)}>
          ReportActiveFile
        </button>
        <button onClick={() => (props.onActiveFileChange as (index: number) => void)(2)}>
          ReportTargetFile
        </button>
      </div>
    );
  }),
}));

vi.mock("./repo-explorer", () => ({
  RepoExplorer: (props: Record<string, unknown>) => (
    <div data-testid="repo-explorer">
      <button onClick={props.onBack as () => void}>BackFromExplorer</button>
      <span data-testid="explorer-initial-path">{String(props.initialPath ?? "")}</span>
    </div>
  ),
}));

vi.mock("./keyboard-help-overlay", () => ({
  KeyboardHelpOverlay: (props: Record<string, unknown>) => (
    <div data-testid="keyboard-help" data-open={String(props.open)}>
      <button onClick={props.onClose as () => void}>CloseHelp</button>
    </div>
  ),
}));

const mockUseDiffKeyboardNav = vi.fn();
vi.mock("@/hooks/use-diff-keyboard-nav", () => ({
  useDiffKeyboardNav: (...args: unknown[]) => mockUseDiffKeyboardNav(...args),
}));

const makeDiffFile = (path: string, lines?: DiffFile["hunks"][0]["lines"]): DiffFile => ({
  oldPath: path,
  newPath: path,
  hunks: [
    {
      oldStart: 1,
      oldCount: 3,
      newStart: 1,
      newCount: 4,
      header: "@@ -1,3 +1,4 @@",
      lines: lines ?? [
        { type: "context", content: " line1", oldLineNumber: 1, newLineNumber: 1 },
        { type: "add", content: "+added", oldLineNumber: null, newLineNumber: 2 },
        { type: "remove", content: "-removed", oldLineNumber: 2, newLineNumber: null },
      ],
    },
  ],
  stats: { added: 1, removed: 1 },
  language: "typescript",
});

function defaultProps(overrides: Partial<Parameters<typeof ReviewDiffView>[0]> = {}) {
  return {
    sessionId: "sess-1",
    files: [makeDiffFile("src/a.ts")],
    allFiles: [makeDiffFile("src/a.ts"), makeDiffFile("src/b.ts")],
    activeFileIndex: 0,
    onFileChange: vi.fn(),
    onBack: vi.fn(),
    commentsByLine: new Map<CommentLineKey, SessionReviewComment[]>(),
    activeCommentLine: null,
    onAddComment: vi.fn(),
    onSubmitComment: vi.fn(),
    onCancelComment: vi.fn(),
    onUpdateComment: vi.fn(),
    onDeleteComment: vi.fn(),
    diffSearchQuery: "",
    onDiffSearchChange: vi.fn(),
    ...overrides,
  };
}

describe("ReviewDiffView", () => {
  beforeEach(() => {
    localStorage.clear();
    mockScrollToFile.mockReset();
    mockScrollToNextHunk.mockReset();
    mockScrollToPrevHunk.mockReset();
    mockDiffPaneRender.mockReset();
    mockUseDiffKeyboardNav.mockReset();
  });

  it("renders DiffToolbar, DiffPane, and KeyboardHelpOverlay when files exist", () => {
    render(<ReviewDiffView {...defaultProps()} />);
    expect(screen.getByTestId("diff-toolbar")).toBeInTheDocument();
    expect(screen.getByTestId("diff-pane")).toBeInTheDocument();
    expect(screen.getByTestId("keyboard-help")).toBeInTheDocument();
  });

  it("does not rerender the diff pane when parent props are unchanged", () => {
    const props = defaultProps();
    const { rerender } = render(<ReviewDiffView {...props} />);
    const initialRenderCount = mockDiffPaneRender.mock.calls.length;

    rerender(<ReviewDiffView {...props} />);

    expect(mockDiffPaneRender).toHaveBeenCalledTimes(initialRenderCount);
  });

  it("passes an active file change handler to DiffPane", () => {
    const props = defaultProps();
    render(<ReviewDiffView {...props} />);

    const lastProps = mockDiffPaneRender.mock.calls.at(-1)?.[0] as Record<string, unknown>;

    expect(lastProps.onActiveFileChange).toEqual(expect.any(Function));
  });

  it("does not re-scroll the diff pane when the active file changes from passive scroll sync", () => {
    function Harness() {
      const [activeFileIndex, setActiveFileIndex] = useState(0);

      return (
        <ReviewDiffView
          {...defaultProps()}
          activeFileIndex={activeFileIndex}
          onFileChange={setActiveFileIndex}
        />
      );
    }

    render(<Harness />);
    expect(mockScrollToFile).toHaveBeenCalledWith(0);

    mockScrollToFile.mockClear();
    fireEvent.click(screen.getByText("ReportActiveFile"));

    expect(mockScrollToFile).not.toHaveBeenCalled();
  });

  it("ignores intermediate scroll sync while jumping to a selected file", () => {
    function Harness() {
      const [activeFileIndex, setActiveFileIndex] = useState(0);

      return (
        <>
          <button onClick={() => setActiveFileIndex(2)}>SelectFile</button>
          <ReviewDiffView
            {...defaultProps({
              files: [
                makeDiffFile("src/a.ts"),
                makeDiffFile("src/b.ts"),
                makeDiffFile("src/c.ts"),
              ],
              activeFileIndex,
              onFileChange: setActiveFileIndex,
            })}
          />
        </>
      );
    }

    render(<Harness />);
    expect(screen.getByTestId("toolbar-file-position")).toHaveTextContent("1 of 3");

    mockScrollToFile.mockClear();
    fireEvent.click(screen.getByText("SelectFile"));

    expect(mockScrollToFile).toHaveBeenCalledWith(2);
    expect(screen.getByTestId("toolbar-file-position")).toHaveTextContent("3 of 3");

    fireEvent.click(screen.getByText("ReportActiveFile"));
    expect(screen.getByTestId("toolbar-file-position")).toHaveTextContent("3 of 3");

    fireEvent.click(screen.getByText("ReportTargetFile"));
    expect(screen.getByTestId("toolbar-file-position")).toHaveTextContent("3 of 3");
  });

  it("does not leave a stale skip flag after confirming the scroll target", () => {
    // Regression: when confirming the pending scroll target, handleVisibleFileChange
    // called onFileChange(index) even though activeFileIndex was already at that value.
    // React bailed out (same state), so the effect never ran and skipNextScrollToFileRef
    // stayed true, causing the very next sidebar jump to silently skip scrollToFile.
    function Harness() {
      const [activeFileIndex, setActiveFileIndex] = useState(0);

      return (
        <>
          <button onClick={() => setActiveFileIndex(2)}>SelectFile2</button>
          <button onClick={() => setActiveFileIndex(0)}>SelectFile0</button>
          <ReviewDiffView
            {...defaultProps({
              files: [
                makeDiffFile("src/a.ts"),
                makeDiffFile("src/b.ts"),
                makeDiffFile("src/c.ts"),
              ],
              activeFileIndex,
              onFileChange: setActiveFileIndex,
            })}
          />
        </>
      );
    }

    render(<Harness />);

    // Jump to file 2 via sidebar
    mockScrollToFile.mockClear();
    fireEvent.click(screen.getByText("SelectFile2"));
    expect(mockScrollToFile).toHaveBeenCalledWith(2);
    expect(screen.getByTestId("toolbar-file-position")).toHaveTextContent("3 of 3");

    // Scroll animation completes — scroll pane confirms file 2 is visible
    fireEvent.click(screen.getByText("ReportTargetFile"));
    expect(screen.getByTestId("toolbar-file-position")).toHaveTextContent("3 of 3");

    // Immediately jump to file 0 via sidebar — must not be silently ignored
    mockScrollToFile.mockClear();
    fireEvent.click(screen.getByText("SelectFile0"));
    expect(mockScrollToFile).toHaveBeenCalledWith(0);
    expect(screen.getByTestId("toolbar-file-position")).toHaveTextContent("1 of 3");
  });

  it("renders empty state when files is empty", () => {
    render(<ReviewDiffView {...defaultProps({ files: [] })} />);
    expect(screen.getByText("No changes to display")).toBeInTheDocument();
    expect(screen.getByText("Try adjusting your search or pass filter.")).toBeInTheDocument();
    expect(screen.queryByTestId("diff-pane")).not.toBeInTheDocument();
  });

  it("renders DiffToolbar in empty state", () => {
    render(<ReviewDiffView {...defaultProps({ files: [] })} />);
    expect(screen.getByTestId("diff-toolbar")).toBeInTheDocument();
  });

  it("enters explorer mode when Browse is clicked", async () => {
    render(<ReviewDiffView {...defaultProps()} />);
    // Click "Browse" in the mocked DiffToolbar
    fireEvent.click(screen.getByText("Browse"));
    expect(screen.getByTestId("repo-explorer")).toBeInTheDocument();
    expect(screen.queryByTestId("diff-pane")).not.toBeInTheDocument();
  });

  it("exits explorer mode when BackFromExplorer is clicked", () => {
    render(<ReviewDiffView {...defaultProps()} />);
    fireEvent.click(screen.getByText("Browse"));
    expect(screen.getByTestId("repo-explorer")).toBeInTheDocument();

    fireEvent.click(screen.getByText("BackFromExplorer"));
    expect(screen.queryByTestId("repo-explorer")).not.toBeInTheDocument();
    expect(screen.getByTestId("diff-pane")).toBeInTheDocument();
  });

  it("enters explorer mode with initial path when BrowseFile is clicked", () => {
    render(<ReviewDiffView {...defaultProps()} />);
    fireEvent.click(screen.getByText("BrowseFile"));
    expect(screen.getByTestId("repo-explorer")).toBeInTheDocument();
    expect(screen.getByTestId("explorer-initial-path")).toHaveTextContent("test.ts");
  });

  it("renders a single-file mobile reader with file navigation metadata", () => {
    render(
      <ReviewDiffView
        {...defaultProps({
          files: [makeDiffFile("src/a.ts"), makeDiffFile("src/b.ts")],
          activeFileIndex: 1,
          isMobile: true,
          onOpenFileList: vi.fn(),
        })}
      />
    );

    const lastProps = mockDiffPaneRender.mock.calls.at(-1)?.[0] as Record<string, unknown>;

    expect((lastProps.files as DiffFile[]).map((file) => file.newPath)).toEqual(["src/b.ts"]);
    expect(screen.getByTestId("toolbar-mobile-mode")).toHaveTextContent("true");
    expect(screen.getByTestId("toolbar-file-path")).toHaveTextContent("src/b.ts");
    expect(screen.getByTestId("toolbar-file-position")).toHaveTextContent("2 of 2");
  });

  it("forces unified mode in mobile review", () => {
    localStorage.setItem("diff-view-mode", "split");

    render(
      <ReviewDiffView
        {...defaultProps({
          files: [makeDiffFile("src/a.ts"), makeDiffFile("src/b.ts")],
          activeFileIndex: 0,
          isMobile: true,
        })}
      />
    );

    const lastProps = mockDiffPaneRender.mock.calls.at(-1)?.[0] as Record<string, unknown>;
    expect(lastProps.viewMode).toBe("unified");
    expect(screen.getByTestId("view-mode")).toHaveTextContent("unified");
  });

  it("advances to the next file in mobile review when the toolbar requests it", () => {
    function Harness() {
      const [activeFileIndex, setActiveFileIndex] = useState(0);

      return (
        <ReviewDiffView
          {...defaultProps({
            files: [makeDiffFile("src/a.ts"), makeDiffFile("src/b.ts")],
            activeFileIndex,
            onFileChange: setActiveFileIndex,
            isMobile: true,
          })}
        />
      );
    }

    render(<Harness />);
    expect(screen.getByTestId("toolbar-file-position")).toHaveTextContent("1 of 2");

    fireEvent.click(screen.getByText("NextFile"));

    expect(screen.getByTestId("toolbar-file-path")).toHaveTextContent("src/b.ts");
    expect(screen.getByTestId("toolbar-file-position")).toHaveTextContent("2 of 2");
  });

  it("keeps repo browsing available in mobile review", () => {
    render(
      <ReviewDiffView
        {...defaultProps({
          files: [makeDiffFile("src/a.ts"), makeDiffFile("src/b.ts")],
          activeFileIndex: 0,
          isMobile: true,
        })}
      />
    );

    fireEvent.click(screen.getByText("Browse"));
    expect(screen.getByTestId("repo-explorer")).toBeInTheDocument();
  });

  it("calls onBack on Escape key when not in explorer/comment/help mode", () => {
    const props = defaultProps();
    render(<ReviewDiffView {...props} />);
    fireEvent.keyDown(document, { key: "Escape" });
    expect(props.onBack).toHaveBeenCalled();
  });

  it("exits full screen on Escape instead of leaving review mode", () => {
    const props = defaultProps({ isFullScreen: true, onToggleFullScreen: vi.fn() });
    render(<ReviewDiffView {...props} />);
    fireEvent.keyDown(document, { key: "Escape" });
    expect(props.onToggleFullScreen).toHaveBeenCalled();
    expect(props.onBack).not.toHaveBeenCalled();
  });

  it("does not call onBack on Escape when in explorer mode", () => {
    const props = defaultProps();
    render(<ReviewDiffView {...props} />);
    // Enter explorer mode
    fireEvent.click(screen.getByText("Browse"));
    (props.onBack as ReturnType<typeof vi.fn>).mockClear();
    fireEvent.keyDown(document, { key: "Escape" });
    expect(props.onBack).not.toHaveBeenCalled();
  });

  it("does not call onBack on Escape when activeCommentLine is set", () => {
    const props = defaultProps({
      activeCommentLine: { filePath: "a.ts", lineNumber: 1, side: "new" as const },
    });
    render(<ReviewDiffView {...props} />);
    fireEvent.keyDown(document, { key: "Escape" });
    expect(props.onBack).not.toHaveBeenCalled();
  });

  it("does not call onBack on Escape when target is INPUT", () => {
    const props = defaultProps();
    const { container } = render(
      <div>
        <ReviewDiffView {...props} />
        <input data-testid="test-input" />
      </div>
    );
    const input = container.querySelector("input")!;
    fireEvent.keyDown(input, { key: "Escape" });
    expect(props.onBack).not.toHaveBeenCalled();
  });

  it("does not call onBack on Escape when target is TEXTAREA", () => {
    const props = defaultProps();
    const { container } = render(
      <div>
        <ReviewDiffView {...props} />
        <textarea data-testid="test-textarea" />
      </div>
    );
    const textarea = container.querySelector("textarea")!;
    fireEvent.keyDown(textarea, { key: "Escape" });
    expect(props.onBack).not.toHaveBeenCalled();
  });

  it("does not fire onBack for non-Escape keys", () => {
    const props = defaultProps();
    render(<ReviewDiffView {...props} />);
    fireEvent.keyDown(document, { key: "Enter" });
    expect(props.onBack).not.toHaveBeenCalled();
  });

  it("reads viewMode from localStorage", () => {
    localStorage.setItem("diff-view-mode", "split");
    render(<ReviewDiffView {...defaultProps()} />);
    expect(screen.getByTestId("view-mode")).toHaveTextContent("split");
    expect(screen.getByTestId("pane-view-mode")).toHaveTextContent("split");
  });

  it("defaults to unified when localStorage has no value", () => {
    render(<ReviewDiffView {...defaultProps()} />);
    expect(screen.getByTestId("view-mode")).toHaveTextContent("unified");
  });

  it("persists viewMode to localStorage on change", () => {
    render(<ReviewDiffView {...defaultProps()} />);
    fireEvent.click(screen.getByText("ChangeView"));
    expect(localStorage.getItem("diff-view-mode")).toBe("split");
  });

  it("toggleExplorer callback toggles explorer mode", () => {
    render(<ReviewDiffView {...defaultProps()} />);
    const lastCall = mockUseDiffKeyboardNav.mock.calls.at(-1)![0] as Record<string, unknown>;
    const toggleExplorer = lastCall.onToggleExplorer as () => void;
    act(() => {
      toggleExplorer();
    });
    expect(screen.getByTestId("repo-explorer")).toBeInTheDocument();
  });

  it("handleAddCommentOnSelectedLine adds comment on first add line", () => {
    const props = defaultProps();
    render(<ReviewDiffView {...props} />);
    const lastCall = mockUseDiffKeyboardNav.mock.calls.at(-1)![0] as Record<string, unknown>;
    const addComment = lastCall.onAddCommentOnSelectedLine as () => void;
    addComment();
    // The first "add" line in the default file has newLineNumber=2
    expect(props.onAddComment).toHaveBeenCalledWith("src/a.ts", 2, "new");
  });

  it("handleAddCommentOnSelectedLine adds comment on remove line when no add lines", () => {
    const file = makeDiffFile("src/c.ts", [
      { type: "context", content: " ctx", oldLineNumber: 1, newLineNumber: 1 },
      { type: "remove", content: "-old", oldLineNumber: 2, newLineNumber: null },
    ]);
    const props = defaultProps({ files: [file] });
    render(<ReviewDiffView {...props} />);
    const lastCall = mockUseDiffKeyboardNav.mock.calls.at(-1)![0] as Record<string, unknown>;
    const addComment = lastCall.onAddCommentOnSelectedLine as () => void;
    addComment();
    expect(props.onAddComment).toHaveBeenCalledWith("src/c.ts", 2, "old");
  });

  it("handleAddCommentOnSelectedLine does nothing when activeFileIndex is out of bounds", () => {
    const props = defaultProps({ activeFileIndex: 99 });
    render(<ReviewDiffView {...props} />);
    const lastCall = mockUseDiffKeyboardNav.mock.calls.at(-1)![0] as Record<string, unknown>;
    const addComment = lastCall.onAddCommentOnSelectedLine as () => void;
    addComment();
    expect(props.onAddComment).not.toHaveBeenCalled();
  });

  it("handleAddCommentOnSelectedLine does nothing when hunks have only context lines", () => {
    const file = makeDiffFile("src/d.ts", [
      { type: "context", content: " ctx", oldLineNumber: 1, newLineNumber: 1 },
    ]);
    const props = defaultProps({ files: [file] });
    render(<ReviewDiffView {...props} />);
    const lastCall = mockUseDiffKeyboardNav.mock.calls.at(-1)![0] as Record<string, unknown>;
    const addComment = lastCall.onAddCommentOnSelectedLine as () => void;
    addComment();
    expect(props.onAddComment).not.toHaveBeenCalled();
  });

  it("keyboard nav callbacks (hunk nav, jump, help) are callable without error", () => {
    render(<ReviewDiffView {...defaultProps()} />);
    const lastCall = mockUseDiffKeyboardNav.mock.calls.at(-1)![0] as Record<string, () => void>;
    // These call diffPaneRef.current?.scrollToNextHunk() etc. which is null, so they no-op
    act(() => {
      lastCall.onNextHunk();
      lastCall.onPrevHunk();
      lastCall.onJumpToFile();
      lastCall.onShowHelp();
    });
    // After toggling help, the overlay should show open=true
    expect(screen.getByTestId("keyboard-help")).toHaveAttribute("data-open", "true");
  });

  it("toggleViewMode switches between unified and split", () => {
    render(<ReviewDiffView {...defaultProps()} />);
    const lastCall = mockUseDiffKeyboardNav.mock.calls.at(-1)![0] as Record<string, () => void>;
    act(() => {
      lastCall.onToggleViewMode();
    });
    // Default is unified, toggle should switch to split
    expect(screen.getByTestId("view-mode")).toHaveTextContent("split");
  });

  it("handleFileSelect calls onFileChange", () => {
    const props = defaultProps();
    render(<ReviewDiffView {...props} />);
    const lastCall = mockUseDiffKeyboardNav.mock.calls.at(-1)![0] as Record<string, unknown>;
    const onFileChange = lastCall.onFileChange as (index: number) => void;
    act(() => {
      onFileChange(1);
    });
    expect(props.onFileChange).toHaveBeenCalledWith(1);
  });

  it("does not call onBack when Escape event has defaultPrevented", () => {
    const props = defaultProps();
    render(<ReviewDiffView {...props} />);
    const event = new KeyboardEvent("keydown", {
      key: "Escape",
      bubbles: true,
      cancelable: true,
    });
    event.preventDefault(); // simulate defaultPrevented = true
    document.dispatchEvent(event);
    expect(props.onBack).not.toHaveBeenCalled();
  });
});
