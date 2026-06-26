import { describe, it, expect, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { DiffLineRow } from "./diff-line";
import type { DiffLine } from "@/lib/diff-parser";

function makeLine(overrides: Partial<DiffLine> = {}): DiffLine {
  return {
    type: "context",
    content: "const x = 1;",
    oldLineNumber: 5,
    newLineNumber: 5,
    ...overrides,
  };
}

describe("DiffLineRow", () => {
  it("renders line content", () => {
    render(<DiffLineRow line={makeLine()} />);
    expect(screen.getByText("const x = 1;")).toBeInTheDocument();
  });

  it("renders old and new line numbers", () => {
    render(<DiffLineRow line={makeLine({ oldLineNumber: 10, newLineNumber: 12 })} />);
    expect(screen.getByText("10")).toBeInTheDocument();
    expect(screen.getByText("12")).toBeInTheDocument();
  });

  it("uses compact line-number gutters so code has more horizontal room", () => {
    render(<DiffLineRow line={makeLine({ oldLineNumber: 10, newLineNumber: 12 })} />);
    expect(screen.getByText("10")).toHaveClass("w-[42px]", "pr-1");
    expect(screen.getByText("12")).toHaveClass("w-[42px]", "pr-1");
  });

  it("renders add line with + prefix", () => {
    const { container } = render(
      <DiffLineRow line={makeLine({ type: "add", oldLineNumber: null, newLineNumber: 3 })} />
    );
    expect(container.textContent).toContain("+");
  });

  it("renders remove line with - prefix", () => {
    const { container } = render(
      <DiffLineRow line={makeLine({ type: "remove", oldLineNumber: 3, newLineNumber: null })} />
    );
    expect(container.textContent).toContain("-");
  });

  it("renders highlighted content as HTML when provided", () => {
    render(
      <DiffLineRow
        line={makeLine()}
        highlightedContent='<span style="color:#f00">highlighted</span>'
      />
    );
    expect(screen.getByText("highlighted")).toBeInTheDocument();
  });

  it("preserves syntax-highlighted HTML when applying inline diff ranges", () => {
    const { container } = render(
      <DiffLineRow
        line={makeLine({ type: "add", content: "const value = 2;" })}
        highlightedContent='<span style="color:#f00">const value = 2;</span>'
        inlineHighlightRanges={[{ start: 14, end: 15 }]}
      />
    );
    expect(container.querySelector('span[style="color:#f00"]')).toBeInTheDocument();
    const highlight = container.querySelector(".bg-green-200\\/80");
    expect(highlight).toHaveTextContent("2");
  });

  it("renders inline diff ranges with a darker change highlight", () => {
    const { container } = render(
      <DiffLineRow
        line={makeLine({ type: "add", content: "const value = 2;" })}
        inlineHighlightRanges={[{ start: 14, end: 15 }]}
      />
    );
    const highlight = screen.getByText("2");
    expect(highlight).toHaveClass("bg-green-200/80");
    expect(container.textContent).toContain("const value = 2;");
  });

  it("renders multiple inline diff ranges in one line", () => {
    const { container } = render(
      <DiffLineRow
        line={makeLine({ type: "add", content: "alpha: 9, beta: 8" })}
        inlineHighlightRanges={[
          { start: 7, end: 8 },
          { start: 16, end: 17 },
        ]}
      />
    );
    const highlights = container.querySelectorAll(".bg-green-200\\/80");
    expect(highlights).toHaveLength(2);
    expect(highlights[0]).toHaveTextContent("9");
    expect(highlights[1]).toHaveTextContent("8");
  });

  it("allows long line content to wrap while preserving whitespace", () => {
    const { container } = render(
      <DiffLineRow line={makeLine({ content: "const token = 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa';" })} />
    );
    const content = container.querySelector('[data-testid="diff-line-content"]');
    expect(content).toHaveClass("whitespace-pre-wrap");
    expect(content).toHaveClass("break-words");
  });

  it("contains row layout and paint work so wrapped offscreen lines stay cheap to scroll", () => {
    const { container } = render(<DiffLineRow line={makeLine()} />);
    const row = container.firstElementChild;
    expect(row).toHaveClass("[content-visibility:auto]");
    expect(row).toHaveClass("[contain-intrinsic-size:auto_20px]");
  });

  it("renders non-breaking space for empty content", () => {
    const { container } = render(
      <DiffLineRow line={makeLine({ content: "" })} />
    );
    // \u00A0 is the non-breaking space
    expect(container.textContent).toContain("\u00A0");
  });

  it("shows add comment button when onAddComment is provided", () => {
    render(<DiffLineRow line={makeLine()} onAddComment={vi.fn()} />);
    expect(screen.getByTitle("Add comment")).toBeInTheDocument();
  });

  it("does not show add comment button when onAddComment is absent", () => {
    render(<DiffLineRow line={makeLine()} />);
    expect(screen.queryByTitle("Add comment")).not.toBeInTheDocument();
  });

  it("calls onAddComment when + button is clicked", async () => {
    const onAddComment = vi.fn();
    const user = userEvent.setup();
    render(<DiffLineRow line={makeLine()} onAddComment={onAddComment} />);
    await user.click(screen.getByTitle("Add comment"));
    expect(onAddComment).toHaveBeenCalled();
  });

  it("calls onAddComment exactly once when + button is clicked (no double-fire via bubble)", async () => {
    const onAddComment = vi.fn();
    const user = userEvent.setup();
    render(<DiffLineRow line={makeLine()} onAddComment={onAddComment} />);
    await user.click(screen.getByTitle("Add comment"));
    expect(onAddComment).toHaveBeenCalledTimes(1);
  });

  it("calls onAddComment when clicking on the line content (anywhere on the row)", async () => {
    const onAddComment = vi.fn();
    const user = userEvent.setup();
    render(<DiffLineRow line={makeLine()} onAddComment={onAddComment} />);
    await user.click(screen.getByText("const x = 1;"));
    expect(onAddComment).toHaveBeenCalled();
  });

  it("calls onAddComment when pressing Enter on the focused row", async () => {
    const onAddComment = vi.fn();
    const user = userEvent.setup();
    render(<DiffLineRow line={makeLine()} onAddComment={onAddComment} />);
    const row = screen.getByRole("button", { name: /add comment on line/i });
    row.focus();
    await user.keyboard("{Enter}");
    expect(onAddComment).toHaveBeenCalled();
  });

  it("calls onAddComment when pressing Space on the focused row", async () => {
    const onAddComment = vi.fn();
    const user = userEvent.setup();
    render(<DiffLineRow line={makeLine()} onAddComment={onAddComment} />);
    const row = screen.getByRole("button", { name: /add comment on line/i });
    row.focus();
    await user.keyboard(" ");
    expect(onAddComment).toHaveBeenCalled();
  });

  it("renders the row with role=button and a tabIndex when onAddComment is provided", () => {
    const { container } = render(
      <DiffLineRow line={makeLine()} onAddComment={vi.fn()} />
    );
    const row = container.querySelector('[role="button"]');
    expect(row).toBeInTheDocument();
    expect(row).toHaveAttribute("tabindex", "0");
  });

  it("does not set role=button when onAddComment is absent", () => {
    const { container } = render(<DiffLineRow line={makeLine()} />);
    expect(container.querySelector('[role="button"]')).not.toBeInTheDocument();
  });

  it("updates URL hash when line number is clicked", async () => {
    const user = userEvent.setup();
    const replaceStateSpy = vi.spyOn(window.history, "replaceState");
    render(
      <DiffLineRow
        line={makeLine({ oldLineNumber: 7, newLineNumber: 9 })}
        filePath="src/app.ts"
      />
    );
    // Click the new line number (second button = R side)
    // First button is old line number "7", second is new line number "9"
    await user.click(screen.getByText("7"));
    expect(replaceStateSpy).toHaveBeenCalledWith(null, "", "#src/app.ts-L7");
    replaceStateSpy.mockRestore();
  });

  it("does not add a comment when line number gutter is clicked", async () => {
    const user = userEvent.setup();
    const onAddComment = vi.fn();
    const replaceStateSpy = vi.spyOn(window.history, "replaceState");
    render(
      <DiffLineRow
        line={makeLine({ oldLineNumber: 7, newLineNumber: 9 })}
        filePath="src/app.ts"
        onAddComment={onAddComment}
      />
    );
    await user.click(screen.getByText("7"));
    expect(replaceStateSpy).toHaveBeenCalledWith(null, "", "#src/app.ts-L7");
    expect(onAddComment).not.toHaveBeenCalled();
    replaceStateSpy.mockRestore();
  });

  it("does not add a comment when Enter is pressed on a focused line number gutter", async () => {
    const user = userEvent.setup();
    const onAddComment = vi.fn();
    const replaceStateSpy = vi.spyOn(window.history, "replaceState");
    render(
      <DiffLineRow
        line={makeLine({ oldLineNumber: 7, newLineNumber: 9 })}
        filePath="src/app.ts"
        onAddComment={onAddComment}
      />
    );
    const oldLineButton = screen.getByText("7");
    oldLineButton.focus();
    await user.keyboard("{Enter}");
    expect(replaceStateSpy).toHaveBeenCalledWith(null, "", "#src/app.ts-L7");
    expect(onAddComment).not.toHaveBeenCalled();
    replaceStateSpy.mockRestore();
  });

  it("sets id attribute on the row when filePath and newLineNumber are present", () => {
    const { container } = render(
      <DiffLineRow line={makeLine({ newLineNumber: 42 })} filePath="src/foo.ts" />
    );
    expect(container.querySelector('[id="src/foo.ts-L42"]')).toBeInTheDocument();
  });
});
