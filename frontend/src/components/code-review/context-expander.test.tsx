import { describe, it, expect, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ContextExpander } from "./context-expander";

// Mock the api module
vi.mock("@/lib/api", () => ({
  api: {
    sessions: {
      getFileContext: vi.fn(),
    },
  },
}));

import { api } from "@/lib/api";

describe("ContextExpander", () => {
  it("returns null when hiddenLineCount <= 0", () => {
    const { container } = render(<ContextExpander hiddenLineCount={0} />);
    expect(container.firstChild).toBeNull();
  });

  it("renders hidden line count text", () => {
    render(<ContextExpander hiddenLineCount={15} />);
    expect(screen.getByText("Show 15 hidden lines")).toBeInTheDocument();
  });

  it("is disabled when sessionId/filePath/startLine are missing", () => {
    render(<ContextExpander hiddenLineCount={10} />);
    const button = screen.getByRole("button");
    expect(button).toBeDisabled();
  });

  it("is enabled when all expand props are provided", () => {
    render(
      <ContextExpander
        hiddenLineCount={10}
        sessionId="s1"
        filePath="src/app.ts"
        startLine={5}
        onExpand={vi.fn()}
      />
    );
    const button = screen.getByRole("button");
    expect(button).not.toBeDisabled();
  });

  it("shows correct title when expandable", () => {
    render(
      <ContextExpander
        hiddenLineCount={10}
        sessionId="s1"
        filePath="src/app.ts"
        startLine={5}
        onExpand={vi.fn()}
      />
    );
    expect(screen.getByTitle("Show 10 hidden lines")).toBeInTheDocument();
  });

  it("shows unavailable title when not expandable", () => {
    render(<ContextExpander hiddenLineCount={10} />);
    expect(screen.getByTitle("Context expansion unavailable (sandbox not running)")).toBeInTheDocument();
  });

  it("calls API and onExpand when clicked", async () => {
    const onExpand = vi.fn();
    const mockLines = [{ number: 6, content: "expanded line" }];
    vi.mocked(api.sessions.getFileContext).mockResolvedValue({
      data: { lines: mockLines },
    } as ReturnType<typeof api.sessions.getFileContext> extends Promise<infer T> ? T : never);

    const user = userEvent.setup();
    render(
      <ContextExpander
        hiddenLineCount={10}
        sessionId="s1"
        filePath="src/app.ts"
        startLine={5}
        onExpand={onExpand}
      />
    );

    await user.click(screen.getByRole("button"));

    expect(api.sessions.getFileContext).toHaveBeenCalledWith("s1", "src/app.ts", 10, 6, 6);
    expect(onExpand).toHaveBeenCalledWith(mockLines);
  });

  it("hides after successful expansion", async () => {
    const onExpand = vi.fn();
    vi.mocked(api.sessions.getFileContext).mockResolvedValue({
      data: { lines: [{ number: 1, content: "x" }] },
    } as ReturnType<typeof api.sessions.getFileContext> extends Promise<infer T> ? T : never);

    const user = userEvent.setup();
    const { container } = render(
      <ContextExpander
        hiddenLineCount={4}
        sessionId="s1"
        filePath="f.ts"
        startLine={1}
        onExpand={onExpand}
      />
    );

    await user.click(screen.getByRole("button"));
    // After expansion, component returns null
    expect(container.firstChild).toBeNull();
  });

  it("does not call onExpand on API error", async () => {
    const onExpand = vi.fn();
    vi.mocked(api.sessions.getFileContext).mockRejectedValue(new Error("fail"));

    const user = userEvent.setup();
    render(
      <ContextExpander
        hiddenLineCount={4}
        sessionId="s1"
        filePath="f.ts"
        startLine={1}
        onExpand={onExpand}
      />
    );

    await user.click(screen.getByRole("button"));
    expect(onExpand).not.toHaveBeenCalled();
    // Button should still be visible (not expanded)
    expect(screen.getByRole("button")).toBeInTheDocument();
  });
});
