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

// Mock CommentInput to avoid deep rendering
vi.mock("./comment-input", () => ({
  CommentInput: ({ onSubmit, onCancel, submitLabel }: { onSubmit: (v: string) => void; onCancel: () => void; submitLabel?: string }) => (
    <div data-testid="comment-input">
      <button onClick={() => onSubmit("edited text")}>{submitLabel ?? "Add comment"}</button>
      <button onClick={onCancel}>Cancel</button>
    </div>
  ),
}));

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

  it("renders markdown formatting in comment body (bold, code, italic)", () => {
    const comments = [makeComment({ body: "Use **bold** and `code` and _italic_" })];
    render(
      <CommentThread comments={comments} onUpdate={vi.fn()} onDelete={vi.fn()} />
    );
    // The rendered HTML should contain <strong>, <code>, <em>
    expect(document.querySelector("strong")?.textContent).toBe("bold");
    expect(document.querySelector("code")?.textContent).toBe("code");
    expect(document.querySelector("em")?.textContent).toBe("italic");
  });

  it("shows relative time 'just now' for recent comments", () => {
    const comments = [makeComment({ created_at: new Date().toISOString() })];
    render(
      <CommentThread comments={comments} onUpdate={vi.fn()} onDelete={vi.fn()} />
    );
    expect(screen.getByText("just now")).toBeInTheDocument();
  });

  it("shows 'Resolved' badge for resolved comment", async () => {
    const user = userEvent.setup();
    const comments = [makeComment({ resolved: true, body: "Done" })];
    render(
      <CommentThread comments={comments} onUpdate={vi.fn()} onDelete={vi.fn()} />
    );
    // Expand resolved first
    await user.click(screen.getByText("1 resolved comment"));
    expect(screen.getByText("Resolved")).toBeInTheDocument();
  });

  it("shows 'Resolved in pass N' when resolved_by_pass is set", async () => {
    const user = userEvent.setup();
    const comments = [makeComment({ resolved: true, resolved_by_pass: 3, body: "Fixed" })];
    render(
      <CommentThread comments={comments} onUpdate={vi.fn()} onDelete={vi.fn()} />
    );
    await user.click(screen.getByText("1 resolved comment"));
    expect(screen.getByText(/Resolved in pass 3/)).toBeInTheDocument();
  });

  it("shows Unresolve button for resolved comments", async () => {
    const onUpdate = vi.fn();
    const user = userEvent.setup();
    const comments = [makeComment({ id: "c-r", resolved: true, body: "Done" })];
    render(
      <CommentThread comments={comments} onUpdate={onUpdate} onDelete={vi.fn()} />
    );
    await user.click(screen.getByText("1 resolved comment"));
    await user.click(screen.getByTitle("Unresolve"));
    expect(onUpdate).toHaveBeenCalledWith("c-r", { resolved: false });
  });

  it("enters edit mode and submits edited text", async () => {
    const onUpdate = vi.fn();
    const user = userEvent.setup();
    const comments = [makeComment({ id: "c-edit", body: "Original" })];
    render(
      <CommentThread comments={comments} onUpdate={onUpdate} onDelete={vi.fn()} />
    );
    await user.click(screen.getByTitle("Edit"));
    expect(screen.getByTestId("comment-input")).toBeInTheDocument();
    await user.click(screen.getByText("Save"));
    expect(onUpdate).toHaveBeenCalledWith("c-edit", { body: "edited text" });
  });

  it("cancels editing and goes back to normal view", async () => {
    const user = userEvent.setup();
    const comments = [makeComment({ body: "Original" })];
    render(
      <CommentThread comments={comments} onUpdate={vi.fn()} onDelete={vi.fn()} />
    );
    await user.click(screen.getByTitle("Edit"));
    await user.click(screen.getByText("Cancel"));
    expect(screen.getByText("Original")).toBeInTheDocument();
  });

  it("shows pass number badge", () => {
    const comments = [makeComment({ pass_number: 2 })];
    render(
      <CommentThread comments={comments} onUpdate={vi.fn()} onDelete={vi.fn()} />
    );
    expect(screen.getByText("Pass 2")).toBeInTheDocument();
  });

  it("renders saved comments inside a width-constrained thread container", () => {
    const comments = [makeComment()];
    const { container } = render(
      <CommentThread comments={comments} onUpdate={vi.fn()} onDelete={vi.fn()} />
    );

    expect(container.querySelector('[data-testid="comment-thread"]')).toHaveClass(
      "w-fit"
    );
    expect(container.querySelector('[data-testid="comment-thread"]')).toHaveClass(
      "max-w-full"
    );
  });
});
