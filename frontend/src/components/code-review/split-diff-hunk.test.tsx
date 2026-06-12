import { describe, it, expect, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { SplitDiffHunk } from "./split-diff-hunk";
import type { DiffHunk as DiffHunkType, DiffLine } from "@/lib/diff-parser";
import type { SessionReviewComment } from "@/lib/types";
import { makeCommentLineKey } from "@/hooks/use-review-comments";

function makeHunk(lines: DiffLine[], header = "@@ -1,3 +1,4 @@"): DiffHunkType {
  return {
    oldStart: 1,
    oldCount: 3,
    newStart: 1,
    newCount: 4,
    header,
    lines,
  };
}

const contextLine: DiffLine = {
  type: "context",
  content: "const x = 1;",
  oldLineNumber: 1,
  newLineNumber: 1,
};

const addLine: DiffLine = {
  type: "add",
  content: "const y = 2;",
  oldLineNumber: null,
  newLineNumber: 2,
};

const removeLine: DiffLine = {
  type: "remove",
  content: "const z = 3;",
  oldLineNumber: 2,
  newLineNumber: null,
};

describe("SplitDiffHunk", () => {
  it("renders the hunk header", () => {
    const hunk = makeHunk([contextLine]);
    render(<SplitDiffHunk hunk={hunk} filePath="src/app.ts" />);
    expect(screen.getByText("@@ -1,3 +1,4 @@")).toBeInTheDocument();
  });

  it("renders context lines on both sides", () => {
    const hunk = makeHunk([contextLine]);
    render(<SplitDiffHunk hunk={hunk} filePath="src/app.ts" />);
    // Context line content appears twice (left + right)
    const elements = screen.getAllByText("const x = 1;");
    expect(elements.length).toBe(2);
  });

  it("uses compact line-number gutters so split panes keep more code visible", () => {
    const hunk = makeHunk([contextLine]);
    render(<SplitDiffHunk hunk={hunk} filePath="src/app.ts" />);
    const lineNumbers = screen.getAllByText("1");
    expect(lineNumbers[0]).toHaveClass("w-[42px]", "pr-1");
    expect(lineNumbers[1]).toHaveClass("w-[42px]", "pr-1");
  });

  it("renders remove+add pair side by side", () => {
    const hunk = makeHunk([removeLine, addLine]);
    render(<SplitDiffHunk hunk={hunk} filePath="src/app.ts" />);
    expect(screen.getByText("const z = 3;")).toBeInTheDocument();
    expect(screen.getByText("const y = 2;")).toBeInTheDocument();
  });

  it("renders standalone add line with empty left cell", () => {
    const hunk = makeHunk([addLine]);
    render(<SplitDiffHunk hunk={hunk} filePath="src/app.ts" />);
    expect(screen.getByText("const y = 2;")).toBeInTheDocument();
  });

  it("shows add comment buttons when onAddComment is provided", () => {
    const hunk = makeHunk([addLine]);
    render(
      <SplitDiffHunk hunk={hunk} filePath="src/app.ts" onAddComment={vi.fn()} />
    );
    // At least one add comment button for the right side
    expect(screen.getAllByTitle("Add comment").length).toBeGreaterThanOrEqual(1);
  });

  it("calls onAddComment with line number and side", async () => {
    const user = userEvent.setup();
    const onAddComment = vi.fn();
    const hunk = makeHunk([contextLine]);
    render(
      <SplitDiffHunk hunk={hunk} filePath="src/app.ts" onAddComment={onAddComment} />
    );
    const buttons = screen.getAllByTitle("Add comment");
    // Click the first button (left side = old)
    await user.click(buttons[0]);
    expect(onAddComment).toHaveBeenCalledWith(1, "old");
  });

  it("calls onAddComment exactly once when + button is clicked (no double-fire via bubble)", async () => {
    const user = userEvent.setup();
    const onAddComment = vi.fn();
    const hunk = makeHunk([contextLine]);
    render(
      <SplitDiffHunk hunk={hunk} filePath="src/app.ts" onAddComment={onAddComment} />
    );
    const buttons = screen.getAllByTitle("Add comment");
    await user.click(buttons[0]);
    expect(onAddComment).toHaveBeenCalledTimes(1);
  });

  it("calls onAddComment when clicking anywhere on the line content", async () => {
    const user = userEvent.setup();
    const onAddComment = vi.fn();
    const hunk = makeHunk([contextLine]);
    render(
      <SplitDiffHunk hunk={hunk} filePath="src/app.ts" onAddComment={onAddComment} />
    );
    // Context lines render content twice (left + right). Click the first.
    const contentEls = screen.getAllByText("const x = 1;");
    await user.click(contentEls[0]);
    expect(onAddComment).toHaveBeenCalledWith(1, "old");
  });

  it("invokes onAddComment via Enter on the focused row", async () => {
    const user = userEvent.setup();
    const onAddComment = vi.fn();
    const hunk = makeHunk([contextLine]);
    render(
      <SplitDiffHunk hunk={hunk} filePath="src/app.ts" onAddComment={onAddComment} />
    );
    const rows = screen.getAllByRole("button", { name: /add comment on line/i });
    rows[0].focus();
    await user.keyboard("{Enter}");
    expect(onAddComment).toHaveBeenCalledWith(1, "old");
  });

  it("renders highlighted content via dangerouslySetInnerHTML", () => {
    const hunk = makeHunk([contextLine]);
    const highlightedLines = new Map([[0, '<span style="color:red">const x = 1;</span>']]);
    const { container } = render(
      <SplitDiffHunk hunk={hunk} filePath="src/app.ts" highlightedLines={highlightedLines} />
    );
    const spans = container.querySelectorAll('span[style="color:red"]');
    expect(spans.length).toBeGreaterThanOrEqual(1);
  });

  it("allows long split line content to wrap while preserving whitespace", () => {
    const hunk = makeHunk([
      {
        type: "add",
        content: "const token = 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa';",
        oldLineNumber: null,
        newLineNumber: 12,
      },
    ]);
    const { container } = render(<SplitDiffHunk hunk={hunk} filePath="src/app.ts" />);
    const content = container.querySelector('[data-testid="split-diff-line-content"]');
    expect(content).toHaveClass("whitespace-pre-wrap");
    expect(content).toHaveClass("break-words");
  });

  it("contains split row layout and paint work so wrapped offscreen lines stay cheap to scroll", () => {
    const hunk = makeHunk([contextLine]);
    const { container } = render(<SplitDiffHunk hunk={hunk} filePath="src/app.ts" />);
    const row = container.querySelector(".flex.divide-x");
    expect(row).toHaveClass("[content-visibility:auto]");
    expect(row).toHaveClass("[contain-intrinsic-size:auto_20px]");
  });

  it("renders comment thread when comments exist", () => {
    const comment: SessionReviewComment = {
      id: "c-1",
      session_id: "s-1",
      org_id: "org-1",
      user_id: "user-1",
      file_path: "src/app.ts",
      line_number: 2,
      diff_side: "new",
      body: "Nice change",
      resolved: false,
      pass_number: 1,
      created_at: new Date().toISOString(),
      updated_at: new Date().toISOString(),
    };
    const commentsByLine = new Map([
      [makeCommentLineKey("src/app.ts", 2, "new"), [comment]],
    ]);
    const hunk = makeHunk([addLine]);
    render(
      <SplitDiffHunk
        hunk={hunk}
        filePath="src/app.ts"
        commentsByLine={commentsByLine}
        onUpdateComment={vi.fn()}
        onDeleteComment={vi.fn()}
      />
    );
    expect(screen.getByText("Nice change")).toBeInTheDocument();
  });

  it("renders comment input when activeCommentLine matches", () => {
    const hunk = makeHunk([addLine]);
    const { container } = render(
      <SplitDiffHunk
        hunk={hunk}
        filePath="src/app.ts"
        activeCommentLine={{ filePath: "src/app.ts", lineNumber: 2, side: "new" }}
        onSubmitComment={vi.fn()}
        onCancelComment={vi.fn()}
      />
    );
    expect(screen.getByRole("button", { name: /submit|comment/i })).toBeInTheDocument();
    expect(container.querySelector('[data-testid="right-comment-composer-slot"]')).not.toHaveClass(
      "sticky"
    );
    expect(container.querySelector('[data-testid="inline-comment-composer"]')).toHaveClass(
      "max-w-[min(36rem,calc(50cqw-1rem))]"
    );
  });

  it("handles multiple removes followed by multiple adds", () => {
    const remove1: DiffLine = { type: "remove", content: "old line 1", oldLineNumber: 1, newLineNumber: null };
    const remove2: DiffLine = { type: "remove", content: "old line 2", oldLineNumber: 2, newLineNumber: null };
    const add1: DiffLine = { type: "add", content: "new line 1", oldLineNumber: null, newLineNumber: 1 };
    const add2: DiffLine = { type: "add", content: "new line 2", oldLineNumber: null, newLineNumber: 2 };
    const add3: DiffLine = { type: "add", content: "new line 3", oldLineNumber: null, newLineNumber: 3 };
    const hunk = makeHunk([remove1, remove2, add1, add2, add3]);
    render(<SplitDiffHunk hunk={hunk} filePath="src/app.ts" />);
    expect(screen.getByText("old line 1")).toBeInTheDocument();
    expect(screen.getByText("old line 2")).toBeInTheDocument();
    expect(screen.getByText("new line 1")).toBeInTheDocument();
    expect(screen.getByText("new line 2")).toBeInTheDocument();
    expect(screen.getByText("new line 3")).toBeInTheDocument();
  });
});
