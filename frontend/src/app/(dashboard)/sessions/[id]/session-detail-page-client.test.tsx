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

import { SessionDetailPageClient } from "./session-detail-page-client";

describe("SessionDetailPageClient", () => {
  it("renders the route skeleton while the split session detail module loads", () => {
    render(<SessionDetailPageClient id="session-1" />);

    expect(screen.getByTestId("mock-dynamic-session-detail")).toBeInTheDocument();
    expect(screen.getByTestId("session-detail-loading-skeleton")).toBeInTheDocument();
  });
});
