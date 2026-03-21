import { describe, it, expect, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { CommentThread } from "./comment-thread";
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

describe("CommentThread", () => {
  it("renders comment body text", () => {
    const comments = [makeComment()];
    render(
      <CommentThread comments={comments} onUpdate={vi.fn()} onDelete={vi.fn()} />
    );
    expect(screen.getByText("This needs error handling")).toBeInTheDocument();
  });

  it("shows resolve button for unresolved comments", () => {
    const comments = [makeComment()];
    render(
      <CommentThread comments={comments} onUpdate={vi.fn()} onDelete={vi.fn()} />
    );
    expect(screen.getByTitle("Resolve")).toBeInTheDocument();
  });

  it("calls onUpdate with resolved:true when resolve is clicked", async () => {
    const onUpdate = vi.fn();
    const user = userEvent.setup();
    const comments = [makeComment({ id: "c-42" })];
    render(
      <CommentThread comments={comments} onUpdate={onUpdate} onDelete={vi.fn()} />
    );
    await user.click(screen.getByTitle("Resolve"));
    expect(onUpdate).toHaveBeenCalledWith("c-42", { resolved: true });
  });

  it("calls onDelete when delete button is clicked", async () => {
    const onDelete = vi.fn();
    const user = userEvent.setup();
    const comments = [makeComment({ id: "c-42" })];
    render(
      <CommentThread comments={comments} onUpdate={vi.fn()} onDelete={onDelete} />
    );
    await user.click(screen.getByTitle("Delete"));
    expect(onDelete).toHaveBeenCalledWith("c-42");
  });

  it("collapses resolved comments by default", () => {
    const comments = [
      makeComment({ id: "c-1", resolved: true, body: "Old feedback" }),
      makeComment({ id: "c-2", resolved: true, body: "More old feedback" }),
    ];
    render(
      <CommentThread comments={comments} onUpdate={vi.fn()} onDelete={vi.fn()} />
    );
    // Should show collapsed count
    expect(screen.getByText("2 resolved comments")).toBeInTheDocument();
    // Should NOT show the comment bodies
    expect(screen.queryByText("Old feedback")).not.toBeInTheDocument();
  });

  it("expands resolved comments when clicked", async () => {
    const user = userEvent.setup();
    const comments = [
      makeComment({ id: "c-1", resolved: true, body: "Old feedback" }),
    ];
    render(
      <CommentThread comments={comments} onUpdate={vi.fn()} onDelete={vi.fn()} />
    );
    await user.click(screen.getByText("1 resolved comment"));
    expect(screen.getByText("Old feedback")).toBeInTheDocument();
    expect(screen.getByText("Hide resolved")).toBeInTheDocument();
  });

  it("shows open comments alongside collapsed resolved", () => {
    const comments = [
      makeComment({ id: "c-open", body: "Please fix this" }),
      makeComment({ id: "c-resolved", resolved: true, body: "Done already" }),
    ];
    render(
      <CommentThread comments={comments} onUpdate={vi.fn()} onDelete={vi.fn()} />
    );
    expect(screen.getByText("Please fix this")).toBeInTheDocument();
    expect(screen.queryByText("Done already")).not.toBeInTheDocument();
    expect(screen.getByText("1 resolved comment")).toBeInTheDocument();
  });
});
