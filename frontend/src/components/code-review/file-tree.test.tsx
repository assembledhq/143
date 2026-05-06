import { describe, it, expect, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { FileTree } from "./file-tree";
import type { DiffFile } from "@/lib/diff-parser";

function makeDiffFile(path: string, added = 1, removed = 0): DiffFile {
  return {
    oldPath: path,
    newPath: path,
    hunks: [],
    stats: { added, removed },
    language: "typescript",
  };
}

const files: DiffFile[] = [
  makeDiffFile("src/app.ts", 5, 2),
  makeDiffFile("src/helpers.ts", 3, 0),
  makeDiffFile("README.md", 1, 1),
];

describe("FileTree", () => {
  it("preserves exact incoming file order when directory entries are interleaved with root files", () => {
    const orderedFiles: DiffFile[] = [
      makeDiffFile("src/first.ts", 1, 0),
      makeDiffFile("README.md", 1, 0),
      makeDiffFile("src/second.ts", 1, 0),
    ];

    render(
      <FileTree files={orderedFiles} activeFileIndex={0} onFileSelect={vi.fn()} />
    );

    const firstFile = screen.getByText(/first\.ts$/);
    const readmeFile = screen.getByText("README.md");
    const secondFile = screen.getByText(/second\.ts$/);

    expect(
      firstFile.compareDocumentPosition(readmeFile) & Node.DOCUMENT_POSITION_FOLLOWING
    ).toBeTruthy();
    expect(
      readmeFile.compareDocumentPosition(secondFile) & Node.DOCUMENT_POSITION_FOLLOWING
    ).toBeTruthy();
  });

  it("keeps root files and directories in the incoming order", () => {
    const orderedFiles: DiffFile[] = [
      makeDiffFile("README.md", 1, 0),
      makeDiffFile("src/first.ts", 1, 0),
      makeDiffFile("docs/guide.md", 1, 0),
    ];

    render(
      <FileTree files={orderedFiles} activeFileIndex={0} onFileSelect={vi.fn()} />
    );

    const readmeFile = screen.getByText("README.md");
    const srcDirectory = screen.getByText("src/");
    const docsDirectory = screen.getByText("docs/");

    expect(
      readmeFile.compareDocumentPosition(srcDirectory) & Node.DOCUMENT_POSITION_FOLLOWING
    ).toBeTruthy();
    expect(
      srcDirectory.compareDocumentPosition(docsDirectory) & Node.DOCUMENT_POSITION_FOLLOWING
    ).toBeTruthy();
  });

  it("preserves incoming file order so the sidebar matches the diff detail view", () => {
    const orderedFiles: DiffFile[] = [
      makeDiffFile("src/first.ts", 1, 0),
      makeDiffFile("src/second.ts", 10, 0),
      makeDiffFile("src/third.ts", 5, 0),
    ];

    render(
      <FileTree files={orderedFiles} activeFileIndex={0} onFileSelect={vi.fn()} />
    );

    const firstFile = screen.getByText("first.ts");
    const secondFile = screen.getByText("second.ts");
    const thirdFile = screen.getByText("third.ts");

    expect(
      firstFile.compareDocumentPosition(secondFile) & Node.DOCUMENT_POSITION_FOLLOWING
    ).toBeTruthy();
    expect(
      secondFile.compareDocumentPosition(thirdFile) & Node.DOCUMENT_POSITION_FOLLOWING
    ).toBeTruthy();
  });

  it("renders file names", () => {
    render(
      <FileTree files={files} activeFileIndex={0} onFileSelect={vi.fn()} />
    );
    expect(screen.getByText("app.ts")).toBeInTheDocument();
    expect(screen.getByText("helpers.ts")).toBeInTheDocument();
    expect(screen.getByText("README.md")).toBeInTheDocument();
  });

  it('shows "N files changed" count', () => {
    render(
      <FileTree files={files} activeFileIndex={0} onFileSelect={vi.fn()} />
    );
    expect(screen.getByText("3 files changed")).toBeInTheDocument();
  });

  it("calls onFileSelect when a file is clicked", async () => {
    const onFileSelect = vi.fn();
    const user = userEvent.setup();
    render(
      <FileTree files={files} activeFileIndex={0} onFileSelect={onFileSelect} />
    );
    await user.click(screen.getByText("helpers.ts"));
    expect(onFileSelect).toHaveBeenCalled();
  });

  it("filters files by search input", async () => {
    const user = userEvent.setup();
    render(
      <FileTree files={files} activeFileIndex={0} onFileSelect={vi.fn()} />
    );
    const input = screen.getByPlaceholderText("Filter files...");
    await user.type(input, "README");
    expect(screen.getByText("README.md")).toBeInTheDocument();
    expect(screen.queryByText("app.ts")).not.toBeInTheDocument();
  });
});
