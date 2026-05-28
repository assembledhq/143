import { describe, it, expect, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { FileDiffSection } from "./file-diff-section";
import type { DiffFile, DiffLine, DiffHunk } from "@/lib/diff-parser";
import type { FileLine } from "@/lib/types";

// Mock syntax highlighting to return null (no highlighting)
vi.mock("@/lib/syntax-highlighter", () => ({
  useFileHighlighting: () => null,
}));

vi.mock("./context-expander", () => ({
  ContextExpander: ({
    kind,
    hiddenStart,
    hiddenEnd,
    visibleStart,
    visibleEnd,
    onExpand,
  }: {
    kind: string;
    hiddenStart: number;
    hiddenEnd?: number;
    visibleStart?: number;
    visibleEnd?: number;
    onExpand?: (direction: "above" | "below" | "all", lines: FileLine[], meta: {
      startLine: number;
      endLine: number;
      hasMoreAbove: boolean;
      hasMoreBelow: boolean;
      totalLines: number;
    }) => void;
  }) => (
    <div>
      <div data-testid={`gap-${kind}`}>{`${kind}:${hiddenStart}-${hiddenEnd ?? "open"}`}</div>
      <div data-testid={`gap-visible-${kind}`}>{`${visibleStart ?? "none"}-${visibleEnd ?? "none"}`}</div>
      <button
        data-testid={`expand-${kind}-above`}
        onClick={() =>
          onExpand?.(
            "above",
            [{ number: hiddenStart, content: `line ${hiddenStart}` }],
            {
              startLine: hiddenStart,
              endLine: hiddenStart,
              hasMoreAbove: hiddenStart > 1,
              hasMoreBelow: hiddenEnd == null || hiddenStart < hiddenEnd,
              totalLines: hiddenEnd ?? hiddenStart,
            },
          )
        }
      >
        expand above
      </button>
      <button
        data-testid={`expand-${kind}-below`}
        onClick={() =>
          onExpand?.(
            "below",
            [{ number: hiddenEnd ?? hiddenStart, content: `line ${hiddenEnd ?? hiddenStart}` }],
            {
              startLine: hiddenEnd ?? hiddenStart,
              endLine: hiddenEnd ?? hiddenStart,
              hasMoreAbove: hiddenEnd == null ? false : hiddenStart < hiddenEnd,
              hasMoreBelow: false,
              totalLines: hiddenEnd ?? hiddenStart,
            },
          )
        }
      >
        expand below
      </button>
      <button
        data-testid={`expand-${kind}-all`}
        onClick={() =>
          onExpand?.(
            "all",
            Array.from({ length: (hiddenEnd ?? hiddenStart) - hiddenStart + 1 }, (_, index) => ({
              number: hiddenStart + index,
              content: `line ${hiddenStart + index}`,
            })),
            {
              startLine: hiddenStart,
              endLine: hiddenEnd ?? hiddenStart,
              hasMoreAbove: hiddenStart > 1,
              hasMoreBelow: false,
              totalLines: hiddenEnd ?? hiddenStart,
            },
          )
        }
      >
        expand all
      </button>
    </div>
  ),
}));

vi.mock("./diff-hunk", () => ({
  DiffHunk: ({ hunk, onAddComment }: { hunk: DiffHunk; onAddComment?: (lineNumber: number, side: "old" | "new") => void }) => (
    <div data-testid="mock-diff-hunk">
      <div>{hunk.header}</div>
      {hunk.lines.map((line, index) => (
        <div key={index} data-testid={`mock-line-${index}`}>
          {`${line.content}|old:${line.oldLineNumber ?? "null"}|new:${line.newLineNumber ?? "null"}`}
        </div>
      ))}
      {onAddComment ? <button title="Add comment" onClick={() => onAddComment(1, "new")}>add comment</button> : null}
    </div>
  ),
}));

vi.mock("./split-diff-hunk", () => ({
  SplitDiffHunk: ({ hunk, onAddComment }: { hunk: DiffHunk; onAddComment?: (lineNumber: number, side: "old" | "new") => void }) => (
    <div data-testid="mock-split-diff-hunk">
      <div>{hunk.header}</div>
      {hunk.lines.map((line, index) => (
        <div key={index} data-testid={`mock-split-line-${index}`}>
          {`${line.content}|old:${line.oldLineNumber ?? "null"}|new:${line.newLineNumber ?? "null"}`}
        </div>
      ))}
      {onAddComment ? <button title="Add comment" onClick={() => onAddComment(1, "new")}>add comment</button> : null}
    </div>
  ),
}));

function makeLine(type: DiffLine["type"], content: string, oldLn: number | null, newLn: number | null): DiffLine {
  return { type, content, oldLineNumber: oldLn, newLineNumber: newLn };
}

function makeHunk(lines: DiffLine[], oldStart = 1, newStart = 1): DiffHunk {
  return {
    oldStart,
    oldCount: lines.filter((l) => l.type !== "add").length,
    newStart,
    newCount: lines.filter((l) => l.type !== "remove").length,
    header: `@@ -${oldStart},3 +${newStart},4 @@`,
    lines,
  };
}

function makeDiffFile(overrides: Partial<DiffFile> = {}): DiffFile {
  return {
    oldPath: "src/app.ts",
    newPath: "src/app.ts",
    hunks: [
      makeHunk([
        makeLine("context", "line 1", 1, 1),
        makeLine("remove", "old line 2", 2, null),
        makeLine("add", "new line 2", null, 2),
        makeLine("context", "line 3", 3, 3),
      ]),
    ],
    stats: { added: 1, removed: 1 },
    language: "typescript",
    ...overrides,
  };
}

describe("FileDiffSection", () => {
  it("renders the file header with path", () => {
    render(<FileDiffSection file={makeDiffFile()} viewMode="unified" />);
    expect(screen.getByText("src/app.ts")).toBeInTheDocument();
  });

  it("renders hunk header in unified mode", () => {
    render(<FileDiffSection file={makeDiffFile()} viewMode="unified" />);
    expect(screen.getByText("@@ -1,3 +1,4 @@")).toBeInTheDocument();
  });

  it("renders hunk header in split mode", () => {
    render(<FileDiffSection file={makeDiffFile()} viewMode="split" />);
    expect(screen.getByText("@@ -1,3 +1,4 @@")).toBeInTheDocument();
  });

  it("marks the horizontal diff viewport as a size container for inline comments", () => {
    const { container } = render(<FileDiffSection file={makeDiffFile()} viewMode="unified" />);
    expect(container.querySelector(".overflow-x-auto")).toHaveClass("[container-type:inline-size]");
  });

  it("renders line content in unified mode", () => {
    render(<FileDiffSection file={makeDiffFile()} viewMode="unified" />);
    expect(screen.getByText(/line 1\|old:1\|new:1/)).toBeInTheDocument();
    expect(screen.getByText(/old line 2\|old:2\|new:null/)).toBeInTheDocument();
    expect(screen.getByText(/new line 2\|old:null\|new:2/)).toBeInTheDocument();
  });

  it("renders line content in split mode", () => {
    render(<FileDiffSection file={makeDiffFile()} viewMode="split" />);
    expect(screen.getByText(/old line 2\|old:2\|new:null/)).toBeInTheDocument();
    expect(screen.getByText(/new line 2\|old:null\|new:2/)).toBeInTheDocument();
  });

  it("bounds the initial line render for a very large single-file diff", async () => {
    const user = userEvent.setup();
    const lines = Array.from({ length: 901 }, (_, index) =>
      makeLine("add", `added line ${index}`, null, index + 1)
    );
    const file = makeDiffFile({ hunks: [makeHunk(lines)] });

    render(<FileDiffSection file={file} viewMode="unified" />);

    expect(screen.getByText(/added line 799\|old:null\|new:800/)).toBeInTheDocument();
    expect(screen.queryByText(/added line 800\|old:null\|new:801/)).not.toBeInTheDocument();
    expect(screen.getByText("Showing first 800 of 901 diff lines in this file")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Show more diff lines" }));

    expect(screen.getByText(/added line 800\|old:null\|new:801/)).toBeInTheDocument();
  });

  it("renders multiple hunks with context expanders between them", () => {
    const hunk1 = makeHunk(
      [
        makeLine("context", "first hunk line", 1, 1),
        makeLine("add", "added in first", null, 2),
      ],
      1,
      1,
    );
    const hunk2 = makeHunk(
      [
        makeLine("context", "second hunk line", 20, 21),
        makeLine("remove", "removed in second", 21, null),
      ],
      20,
      21,
    );
    const file = makeDiffFile({ hunks: [hunk1, hunk2] });
    render(<FileDiffSection file={file} viewMode="unified" />);
    expect(screen.getByText(/first hunk line\|old:1\|new:1/)).toBeInTheDocument();
    expect(screen.getByText(/second hunk line\|old:20\|new:21/)).toBeInTheDocument();
    expect(screen.getByTestId("gap-middle")).toHaveTextContent("middle:3-20");
  });

  it("renders a top gap before the first hunk", () => {
    const file = makeDiffFile({
      hunks: [
        makeHunk(
          [
            makeLine("context", "line 10", 10, 10),
            makeLine("add", "line 11", null, 11),
          ],
          10,
          10,
        ),
      ],
    });

    render(<FileDiffSection file={file} viewMode="unified" />);

    expect(screen.getByTestId("gap-top")).toHaveTextContent("top:1-9");
  });

  it("renders a bottom gap after the last hunk when file context metadata is available", () => {
    const file = makeDiffFile({
      hunks: [
        makeHunk(
          [
            makeLine("context", "line 10", 10, 10),
            makeLine("add", "line 11", null, 11),
            makeLine("context", "line 12", 12, 12),
          ],
          10,
          10,
        ),
      ],
    });

    render(
      <FileDiffSection
        file={file}
        viewMode="unified"
        fileContextMeta={{ "src/app.ts": { totalLines: 20 } }}
      />
    );

    expect(screen.getByTestId("gap-bottom")).toHaveTextContent("bottom:13-20");
  });

  it("renders a bottom boundary after the last hunk for sessions before total line metadata is known", () => {
    const file = makeDiffFile({
      hunks: [
        makeHunk(
          [
            makeLine("context", "line 10", 10, 10),
            makeLine("add", "line 11", null, 11),
            makeLine("context", "line 12", 12, 12),
          ],
          10,
          10,
        ),
      ],
    });

    render(
      <FileDiffSection
        file={file}
        viewMode="unified"
        sessionId="session-1"
      />
    );

    expect(screen.getByTestId("gap-bottom")).toHaveTextContent("bottom:13-open");
  });

  it("maps expanded context lines onto the correct old and new numbers", async () => {
    const user = userEvent.setup();
    const file = makeDiffFile({
      hunks: [
        makeHunk(
          [
            makeLine("context", "line 1", 1, 1),
            makeLine("add", "inserted line 2", null, 2),
            makeLine("context", "line 2", 2, 3),
          ],
          1,
          1,
        ),
        makeHunk(
          [
            makeLine("context", "line 6", 6, 7),
            makeLine("remove", "removed line 7", 7, null),
          ],
          6,
          7,
        ),
      ],
    });

    render(<FileDiffSection file={file} viewMode="unified" />);

    await user.click(screen.getByTestId("expand-middle-all"));

    expect(screen.getByText("line 4|old:3|new:4")).toBeInTheDocument();
    expect(screen.getByText("line 6|old:5|new:6")).toBeInTheDocument();
  });

  it("merges visible range metadata across above and below expansions", async () => {
    const user = userEvent.setup();
    const file = makeDiffFile({
      hunks: [
        makeHunk(
          [
            makeLine("context", "line 1", 1, 1),
            makeLine("context", "line 2", 2, 2),
          ],
          1,
          1,
        ),
        makeHunk(
          [
            makeLine("context", "line 8", 8, 8),
            makeLine("context", "line 9", 9, 9),
          ],
          8,
          8,
        ),
      ],
    });

    render(<FileDiffSection file={file} viewMode="unified" />);

    expect(screen.getByTestId("gap-visible-middle")).toHaveTextContent("none-none");

    await user.click(screen.getByTestId("expand-middle-above"));
    expect(screen.getByTestId("gap-visible-middle")).toHaveTextContent("3-3");

    await user.click(screen.getByTestId("expand-middle-below"));
    expect(screen.getByTestId("gap-visible-middle")).toHaveTextContent("3-7");
  });

  it("passes onAddComment to hunks", () => {
    const onAddComment = vi.fn();
    render(
      <FileDiffSection
        file={makeDiffFile()}
        viewMode="unified"
        onAddComment={onAddComment}
      />
    );
    // Should render add comment buttons
    const buttons = screen.getAllByTitle("Add comment");
    expect(buttons.length).toBeGreaterThan(0);
  });

  it("renders with isActive=false without crashing", () => {
    render(
      <FileDiffSection file={makeDiffFile()} viewMode="unified" isActive={false} />
    );
    expect(screen.getByText("src/app.ts")).toBeInTheDocument();
  });
});
