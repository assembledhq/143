import { describe, it, expect, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { CommentsSummary } from "./comments-summary";
import type { SessionReviewComment } from "@/lib/types";

function makeComment(overrides: Partial<SessionReviewComment> = {}): SessionReviewComment {
  return {
    id: "comment-1",
    session_id: "session-1",
    org_id: "org-1",
    user_id: "user-1",
    file_path: "src/app.ts",
    line_number: 10,
    diff_side: "new",
    body: "This needs error handling",
    resolved: false,
    pass_number: 1,
    created_at: new Date().toISOString(),
    updated_at: new Date().toISOString(),
    ...overrides,
  };
}

describe("CommentsSummary", () => {
  it("returns null when no comments", () => {
    const { container } = render(
      <CommentsSummary comments={[]} onCommentClick={vi.fn()} />
    );
    expect(container.firstChild).toBeNull();
  });

  it("renders comment count for single comment", () => {
    render(
      <CommentsSummary
        comments={[makeComment()]}
        onCommentClick={vi.fn()}
      />
    );
    expect(screen.getByText("1 comment")).toBeInTheDocument();
    expect(screen.getByText("(1 open)")).toBeInTheDocument();
  });

  it("renders plural comment count", () => {
    render(
      <CommentsSummary
        comments={[makeComment({ id: "c1" }), makeComment({ id: "c2" })]}
        onCommentClick={vi.fn()}
      />
    );
    expect(screen.getByText("2 comments")).toBeInTheDocument();
  });

  it("shows resolved count when some are resolved", () => {
    render(
      <CommentsSummary
        comments={[
          makeComment({ id: "c1", resolved: false }),
          makeComment({ id: "c2", resolved: true }),
        ]}
        onCommentClick={vi.fn()}
      />
    );
    expect(screen.getByText("(1 open, 1 resolved)")).toBeInTheDocument();
  });

  it("renders comment body text truncated to 80 chars", () => {
    const longBody = "A".repeat(100);
    render(
      <CommentsSummary
        comments={[makeComment({ body: longBody })]}
        onCommentClick={vi.fn()}
      />
    );
    expect(screen.getByText("A".repeat(80) + "...")).toBeInTheDocument();
  });

  it("renders file name and line number", () => {
    render(
      <CommentsSummary
        comments={[makeComment({ file_path: "src/components/app.tsx", line_number: 42 })]}
        onCommentClick={vi.fn()}
      />
    );
    expect(screen.getByText("app.tsx:42")).toBeInTheDocument();
  });

  it("calls onCommentClick when a comment row is clicked", async () => {
    const onCommentClick = vi.fn();
    const user = userEvent.setup();
    render(
      <CommentsSummary
        comments={[makeComment({ file_path: "src/app.ts" })]}
        onCommentClick={onCommentClick}
      />
    );
    await user.click(screen.getByText("app.ts:10"));
    expect(onCommentClick).toHaveBeenCalledWith("src/app.ts");
  });

  it("collapses the list when header is clicked", async () => {
    const user = userEvent.setup();
    render(
      <CommentsSummary
        comments={[makeComment({ body: "visible body" })]}
        onCommentClick={vi.fn()}
      />
    );
    expect(screen.getByText("visible body")).toBeInTheDocument();
    // Click the header toggle
    await user.click(screen.getByText("1 comment"));
    expect(screen.queryByText("visible body")).not.toBeInTheDocument();
  });

  it("shows pass number badge when pass_number > 1", () => {
    render(
      <CommentsSummary
        comments={[makeComment({ pass_number: 3 })]}
        onCommentClick={vi.fn()}
      />
    );
    expect(screen.getByText("P3")).toBeInTheDocument();
  });

  it("does not show pass number badge when pass_number is 1", () => {
    render(
      <CommentsSummary
        comments={[makeComment({ pass_number: 1 })]}
        onCommentClick={vi.fn()}
      />
    );
    expect(screen.queryByText("P1")).not.toBeInTheDocument();
  });
});
