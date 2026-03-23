import { describe, it, expect, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { DiffPane } from "./diff-pane";
import type { DiffFile, DiffLine, DiffHunk } from "@/lib/diff-parser";

// Mock FileDiffSection to avoid deep rendering
vi.mock("./file-diff-section", () => ({
  FileDiffSection: vi.fn().mockImplementation(({ file }: { file: DiffFile }) => (
    <div data-testid={`file-${file.newPath}`}>{file.newPath}</div>
  )),
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

  it("renders a single file", () => {
    render(<DiffPane files={[makeDiffFile("index.ts")]} viewMode="split" />);
    expect(screen.getByTestId("file-index.ts")).toBeInTheDocument();
  });
});
