import { describe, it, expect, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ContextExpander } from "./context-expander";

// Mock the api module. The ApiError class is defined inside the factory
// because vi.mock is hoisted above any module-level declarations.
vi.mock("@/lib/api", () => {
  class MockApiError extends Error {
    constructor(public code: string, message: string, public details?: unknown) {
      super(message);
      this.name = "ApiError";
    }
  }
  return {
    ApiError: MockApiError,
    api: {
      sessions: {
        getFileContext: vi.fn(),
      },
    },
  };
});

import { ApiError, api } from "@/lib/api";

describe("ContextExpander", () => {
  it("returns null when hiddenLineCount <= 0", () => {
    const { container } = render(<ContextExpander kind="middle" hiddenLineCount={0} hiddenStart={1} hiddenEnd={0} />);
    expect(container.firstChild).toBeNull();
  });

  it("renders a top boundary up-arrow with a blank row label", () => {
    render(
      <ContextExpander
        kind="top"
        hiddenLineCount={15}
        hiddenStart={6}
        hiddenEnd={20}
      />
    );
    expect(screen.getByRole("button", { name: "Reveal context above" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Reveal context below" })).not.toBeInTheDocument();
    expect(screen.getByTestId("context-expander-label")).toBeEmptyDOMElement();
    expect(screen.queryByText("Before change")).not.toBeInTheDocument();
    expect(screen.queryByText("Show 20 above")).not.toBeInTheDocument();
    expect(screen.queryByText("Show 20 below")).not.toBeInTheDocument();
    expect(screen.queryByText("Show all")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Show all hidden lines" })).not.toBeInTheDocument();
  });

  it("renders a bottom boundary down-arrow with a blank row label", () => {
    render(
      <ContextExpander
        kind="bottom"
        hiddenLineCount={15}
        hiddenStart={6}
        hiddenEnd={20}
      />
    );

    expect(screen.queryByRole("button", { name: "Reveal context above" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Reveal context below" })).toBeInTheDocument();
    expect(screen.getByTestId("context-expander-label")).toBeEmptyDOMElement();
    expect(screen.queryByText("After change")).not.toBeInTheDocument();
  });

  it("anchors expansion controls in the line-number gutter", () => {
    render(
      <ContextExpander
        kind="top"
        hiddenLineCount={15}
        hiddenStart={6}
        hiddenEnd={20}
      />
    );

    const gutter = screen.getByTestId("context-expander-gutter-controls");
    expect(within(gutter).getByRole("button", { name: "Reveal context above" })).toBeInTheDocument();
    expect(within(gutter).queryByRole("button", { name: "Reveal context below" })).not.toBeInTheDocument();
    expect(within(gutter).queryByRole("button", { name: "Show all hidden lines" })).not.toBeInTheDocument();
    expect(screen.getByTestId("context-expander-prefix-spacer")).toBeInTheDocument();
    expect(screen.getByTestId("context-expander-label")).toBeEmptyDOMElement();
  });

  it("matches the compact unified diff gutter width", () => {
    render(
      <ContextExpander
        kind="middle"
        hiddenLineCount={15}
        hiddenStart={6}
        hiddenEnd={20}
      />
    );

    expect(screen.getByTestId("context-expander-gutter-controls")).toHaveClass("w-[84px]");
    expect(screen.getByTestId("context-expander-prefix-spacer")).toHaveClass("w-[16px]");
  });

  it("matches the compact split diff gutter width", () => {
    render(
      <ContextExpander
        kind="middle"
        viewMode="split"
        hiddenLineCount={15}
        hiddenStart={6}
        hiddenEnd={20}
      />
    );

    expect(screen.getByTestId("context-expander-gutter-controls")).toHaveClass("w-[58px]");
    expect(screen.getByTestId("context-expander-prefix-spacer")).toHaveClass("w-0");
  });

  it("is disabled when sessionId/filePath/startLine are missing", () => {
    render(<ContextExpander kind="middle" hiddenLineCount={10} hiddenStart={6} hiddenEnd={15} />);
    expect(screen.getByRole("button", { name: "Reveal context above" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Reveal context below" })).toBeDisabled();
  });

  it("is enabled when required expand props are provided", () => {
    render(
      <ContextExpander
        kind="middle"
        hiddenLineCount={10}
        sessionId="s1"
        filePath="src/app.ts"
        hiddenStart={6}
        hiddenEnd={15}
        onExpand={vi.fn()}
      />
    );
    expect(screen.getByRole("button", { name: "Reveal context above" })).not.toBeDisabled();
    expect(screen.getByRole("button", { name: "Reveal context below" })).not.toBeDisabled();
  });

  it("shows correct title when expandable", () => {
    render(
      <ContextExpander
        kind="middle"
        hiddenLineCount={10}
        sessionId="s1"
        filePath="src/app.ts"
        hiddenStart={6}
        hiddenEnd={15}
        onExpand={vi.fn()}
      />
    );
    expect(screen.getByTitle("Reveal 10 hidden context lines")).toBeInTheDocument();
  });

  it("shows unavailable title when not expandable", () => {
    render(<ContextExpander kind="middle" hiddenLineCount={10} hiddenStart={6} hiddenEnd={15} />);
    expect(screen.getByTitle("Context expansion unavailable (sandbox not running)")).toBeInTheDocument();
  });

  it("fetches the next window above the visible range", async () => {
    const onExpand = vi.fn();
    const mockLines = [{ number: 6, content: "expanded line" }];
    vi.mocked(api.sessions.getFileContext).mockResolvedValue({
      data: {
        lines: mockLines,
        start_line: 6,
        end_line: 6,
        has_more_above: true,
        has_more_below: true,
        total_lines: 40,
      },
    } as ReturnType<typeof api.sessions.getFileContext> extends Promise<infer T> ? T : never);

    const user = userEvent.setup();
    render(
      <ContextExpander
        kind="middle"
        hiddenLineCount={10}
        sessionId="s1"
        filePath="src/app.ts"
        hiddenStart={6}
        hiddenEnd={15}
        visibleStart={11}
        visibleEnd={15}
        onExpand={onExpand}
      />
    );

    await user.click(screen.getByRole("button", { name: "Reveal context above" }));

    expect(api.sessions.getFileContext).toHaveBeenCalledWith("s1", "src/app.ts", 6, 0, 4);
    expect(onExpand).toHaveBeenCalledWith("above", mockLines, {
      startLine: 6,
      endLine: 6,
      hasMoreAbove: true,
      hasMoreBelow: true,
      totalLines: 40,
    });
  });

  it("fetches the next window below the visible range", async () => {
    const onExpand = vi.fn();
    vi.mocked(api.sessions.getFileContext).mockResolvedValue({
      data: {
        lines: [{ number: 16, content: "x" }],
        start_line: 16,
        end_line: 16,
        has_more_above: true,
        has_more_below: false,
        total_lines: 16,
      },
    } as ReturnType<typeof api.sessions.getFileContext> extends Promise<infer T> ? T : never);

    const user = userEvent.setup();
    render(
      <ContextExpander
        kind="middle"
        hiddenLineCount={4}
        sessionId="s1"
        filePath="f.ts"
        hiddenStart={12}
        hiddenEnd={16}
        visibleStart={12}
        visibleEnd={15}
        onExpand={onExpand}
      />
    );

    await user.click(screen.getByRole("button", { name: "Reveal context below" }));

    expect(api.sessions.getFileContext).toHaveBeenCalledWith("s1", "f.ts", 16, 0, 0);
    expect(onExpand).toHaveBeenCalledWith("below", [{ number: 16, content: "x" }], {
      startLine: 16,
      endLine: 16,
      hasMoreAbove: true,
      hasMoreBelow: false,
      totalLines: 16,
    });
  });

  it("does not call onExpand on API error", async () => {
    const onExpand = vi.fn();
    vi.mocked(api.sessions.getFileContext).mockRejectedValue(new Error("fail"));

    const user = userEvent.setup();
    render(
      <ContextExpander
        kind="middle"
        hiddenLineCount={4}
        sessionId="s1"
        filePath="f.ts"
        hiddenStart={1}
        hiddenEnd={4}
        onExpand={onExpand}
      />
    );

    await user.click(screen.getByRole("button", { name: "Reveal context above" }));
    expect(onExpand).not.toHaveBeenCalled();
    // Button should still be visible (not expanded)
    expect(screen.getByRole("button", { name: "Reveal context above" })).toBeInTheDocument();
  });

  it("calls onContextUnavailable on NO_SANDBOX response", async () => {
    const onContextUnavailable = vi.fn();
    vi.mocked(api.sessions.getFileContext).mockRejectedValue(
      new ApiError("NO_SANDBOX", "session has no active sandbox container")
    );

    const user = userEvent.setup();
    render(
      <ContextExpander
        kind="middle"
        hiddenLineCount={4}
        sessionId="s1"
        filePath="f.ts"
        hiddenStart={1}
        hiddenEnd={4}
        onExpand={vi.fn()}
        onContextUnavailable={onContextUnavailable}
      />
    );

    await user.click(screen.getByRole("button", { name: "Reveal context above" }));
    expect(onContextUnavailable).toHaveBeenCalledTimes(1);
  });

  it("does not call onContextUnavailable on non-NO_SANDBOX errors", async () => {
    const onContextUnavailable = vi.fn();
    vi.mocked(api.sessions.getFileContext).mockRejectedValue(
      new ApiError("INTERNAL", "boom")
    );

    const user = userEvent.setup();
    render(
      <ContextExpander
        kind="middle"
        hiddenLineCount={4}
        sessionId="s1"
        filePath="f.ts"
        hiddenStart={1}
        hiddenEnd={4}
        onExpand={vi.fn()}
        onContextUnavailable={onContextUnavailable}
      />
    );

    await user.click(screen.getByRole("button", { name: "Reveal context above" }));
    expect(onContextUnavailable).not.toHaveBeenCalled();
  });

  it("disables all controls and shows unavailable copy when contextUnavailable is true", () => {
    render(
      <ContextExpander
        kind="middle"
        hiddenLineCount={10}
        sessionId="s1"
        filePath="src/app.ts"
        hiddenStart={6}
        hiddenEnd={15}
        onExpand={vi.fn()}
        contextUnavailable
      />
    );

    expect(screen.getByRole("button", { name: "Reveal context above" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Reveal context below" })).toBeDisabled();
    expect(
      screen.getByText("Additional file context unavailable for this session")
    ).toBeInTheDocument();
  });

  it("disables above and below controls when the gap is fully revealed", () => {
    render(
      <ContextExpander
        kind="middle"
        hiddenLineCount={4}
        sessionId="s1"
        filePath="f.ts"
        hiddenStart={12}
        hiddenEnd={15}
        visibleStart={12}
        visibleEnd={15}
        onExpand={vi.fn()}
      />
    );

    expect(screen.getByRole("button", { name: "Reveal context above" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Reveal context below" })).toBeDisabled();
  });
});
