import { describe, it, expect, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { DiffHunk } from "./diff-hunk";
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

describe("DiffHunk", () => {
  it("renders the hunk header", () => {
    const hunk = makeHunk([contextLine]);
    render(<DiffHunk hunk={hunk} filePath="src/app.ts" />);
    expect(screen.getByText("@@ -1,3 +1,4 @@")).toBeInTheDocument();
  });

  it("renders context, add, and remove lines", () => {
    const hunk = makeHunk([contextLine, removeLine, addLine]);
    render(<DiffHunk hunk={hunk} filePath="src/app.ts" />);
    expect(screen.getByText("const x = 1;")).toBeInTheDocument();
    expect(screen.getByText("const y = 2;")).toBeInTheDocument();
    expect(screen.getByText("const z = 3;")).toBeInTheDocument();
  });

  it("shows add comment buttons when onAddComment is provided", () => {
    const hunk = makeHunk([addLine]);
    render(
      <DiffHunk hunk={hunk} filePath="src/app.ts" onAddComment={vi.fn()} />
    );
    expect(screen.getByTitle("Add comment")).toBeInTheDocument();
  });

  it("calls onAddComment with line number and side", async () => {
    const user = userEvent.setup();
    const onAddComment = vi.fn();
    const hunk = makeHunk([addLine]);
    render(
      <DiffHunk hunk={hunk} filePath="src/app.ts" onAddComment={onAddComment} />
    );
    await user.click(screen.getByTitle("Add comment"));
    expect(onAddComment).toHaveBeenCalledWith(2, "new");
  });

  it("calls onAddComment with old side for remove lines", async () => {
    const user = userEvent.setup();
    const onAddComment = vi.fn();
    const hunk = makeHunk([removeLine]);
    render(
      <DiffHunk hunk={hunk} filePath="src/app.ts" onAddComment={onAddComment} />
    );
    await user.click(screen.getByTitle("Add comment"));
    expect(onAddComment).toHaveBeenCalledWith(2, "old");
  });

  it("renders inline comment thread when comments exist", () => {
    const comment: SessionReviewComment = {
      id: "c-1",
      session_id: "s-1",
      org_id: "org-1",
      user_id: "user-1",
      file_path: "src/app.ts",
      line_number: 2,
      diff_side: "new",
      body: "Looks good",
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
      <DiffHunk
        hunk={hunk}
        filePath="src/app.ts"
        commentsByLine={commentsByLine}
        onUpdateComment={vi.fn()}
        onDeleteComment={vi.fn()}
      />
    );
    expect(screen.getByText("Looks good")).toBeInTheDocument();
  });

  it("renders comment input when activeCommentLine matches", () => {
    const hunk = makeHunk([addLine]);
    const { container } = render(
      <DiffHunk
        hunk={hunk}
        filePath="src/app.ts"
        activeCommentLine={{ filePath: "src/app.ts", lineNumber: 2, side: "new" }}
        onSubmitComment={vi.fn()}
        onCancelComment={vi.fn()}
      />
    );
    // CommentInput should have a submit button
    expect(screen.getByRole("button", { name: /submit|comment/i })).toBeInTheDocument();
    expect(container.querySelector('[data-testid="inline-comment-composer"]')).toHaveClass(
      "max-w-[min(42rem,calc(100cqw-10rem))]"
    );
    expect(container.querySelector('[data-testid="inline-comment-composer-anchor"]')).toHaveClass(
      "sticky"
    );
    expect(container.querySelector('[data-testid="inline-comment-composer-anchor"]')).toHaveClass(
      "pl-[100px]"
    );
  });

  it("does not render comment input when activeCommentLine does not match", () => {
    const hunk = makeHunk([addLine]);
    render(
      <DiffHunk
        hunk={hunk}
        filePath="src/app.ts"
        activeCommentLine={{ filePath: "src/other.ts", lineNumber: 2, side: "new" }}
        onSubmitComment={vi.fn()}
        onCancelComment={vi.fn()}
      />
    );
    expect(screen.queryByRole("button", { name: /submit|comment/i })).not.toBeInTheDocument();
  });
});
