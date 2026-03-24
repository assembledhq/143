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

  it("sets id attribute on the row when filePath and newLineNumber are present", () => {
    const { container } = render(
      <DiffLineRow line={makeLine({ newLineNumber: 42 })} filePath="src/foo.ts" />
    );
    expect(container.querySelector('[id="src/foo.ts-L42"]')).toBeInTheDocument();
  });
});
