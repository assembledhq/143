import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";

const { markdownModuleState } = vi.hoisted(() => ({
  markdownModuleState: { loads: 0 },
}));

vi.mock("@/components/markdown", () => {
  markdownModuleState.loads += 1;
  return {
    MarkdownContent: ({ content }: { content: string }) => (
      <div data-testid="markdown-content-mock">{content}</div>
    ),
  };
});

describe("ChatTimeline performance", () => {
  it("does not load the markdown renderer for non-markdown timeline chrome", async () => {
    const { ChatTimeline } = await import("./chat-timeline");

    render(<ChatTimeline entries={[]} isRunning />);

    expect(screen.getByText("Agent is working...")).toBeInTheDocument();
    expect(markdownModuleState.loads).toBe(0);
  });
});
