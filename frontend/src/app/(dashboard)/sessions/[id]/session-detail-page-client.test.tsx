import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import type { ReactNode } from "react";

vi.mock("next/dynamic", () => ({
  default: (_loader: unknown, options: { loading?: () => ReactNode }) => {
    function MockDynamicSessionDetail() {
      return (
        <div data-testid="mock-dynamic-session-detail">
          {options.loading ? options.loading() : null}
        </div>
      );
    }
    return MockDynamicSessionDetail;
  },
}));

const contentModule = vi.hoisted(() => ({ loaded: false }));

vi.mock("./session-detail-content", () => {
  contentModule.loaded = true;
  return { SessionDetailContent: () => null };
});

import { preloadSessionDetailContent, SessionDetailPageClient } from "./session-detail-page-client";

describe("SessionDetailPageClient", () => {
  it("renders the route skeleton while the split session detail module loads", () => {
    render(<SessionDetailPageClient id="session-1" />);

    expect(screen.getByTestId("mock-dynamic-session-detail")).toBeInTheDocument();
    expect(screen.getByTestId("session-detail-loading-skeleton")).toBeInTheDocument();
  });

  it("loads the split session detail module when preloaded ahead of navigation", async () => {
    expect(contentModule.loaded).toBe(false);

    preloadSessionDetailContent();

    await vi.waitFor(() => expect(contentModule.loaded).toBe(true));
  });
});
