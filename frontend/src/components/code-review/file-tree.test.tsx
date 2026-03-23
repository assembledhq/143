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

  it("shows reviewed checkmark for reviewed files", () => {
    const reviewedFiles = new Set(["src/app.ts"]);
    render(
      <FileTree
        files={files}
        activeFileIndex={0}
        onFileSelect={vi.fn()}
        reviewedFiles={reviewedFiles}
        onToggleReviewed={vi.fn()}
      />
    );
    const checkbox = screen.getByRole("checkbox", {
      name: /Unmark app\.ts as reviewed/,
    });
    expect(checkbox).toHaveAttribute("aria-checked", "true");
  });

  it("shows unchecked state for non-reviewed files", () => {
    render(
      <FileTree
        files={files}
        activeFileIndex={0}
        onFileSelect={vi.fn()}
        reviewedFiles={new Set()}
        onToggleReviewed={vi.fn()}
      />
    );
    const checkbox = screen.getByRole("checkbox", {
      name: /Mark app\.ts as reviewed/,
    });
    expect(checkbox).toHaveAttribute("aria-checked", "false");
  });

  it("calls onToggleReviewed when checkbox is clicked", async () => {
    const onToggleReviewed = vi.fn();
    const user = userEvent.setup();
    render(
      <FileTree
        files={files}
        activeFileIndex={0}
        onFileSelect={vi.fn()}
        reviewedFiles={new Set()}
        onToggleReviewed={onToggleReviewed}
      />
    );
    const checkbox = screen.getByRole("checkbox", {
      name: /Mark app\.ts as reviewed/,
    });
    await user.click(checkbox);
    expect(onToggleReviewed).toHaveBeenCalledWith("src/app.ts");
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
