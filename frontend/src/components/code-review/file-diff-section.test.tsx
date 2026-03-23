import { describe, it, expect, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { FileDiffSection } from "./file-diff-section";
import type { DiffFile, DiffLine, DiffHunk } from "@/lib/diff-parser";

// Mock syntax highlighting to return null (no highlighting)
vi.mock("@/lib/syntax-highlighter", () => ({
  useFileHighlighting: () => null,
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

  it("renders line content in unified mode", () => {
    render(<FileDiffSection file={makeDiffFile()} viewMode="unified" />);
    expect(screen.getByText("line 1")).toBeInTheDocument();
    expect(screen.getByText("old line 2")).toBeInTheDocument();
    expect(screen.getByText("new line 2")).toBeInTheDocument();
  });

  it("renders line content in split mode", () => {
    render(<FileDiffSection file={makeDiffFile()} viewMode="split" />);
    expect(screen.getByText("old line 2")).toBeInTheDocument();
    expect(screen.getByText("new line 2")).toBeInTheDocument();
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
    expect(screen.getByText("first hunk line")).toBeInTheDocument();
    expect(screen.getByText("second hunk line")).toBeInTheDocument();
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
