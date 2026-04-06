import { describe, it, expect, vi } from "vitest";
import { renderWithProviders, screen, waitFor } from "@/test/test-utils";
import { http, HttpResponse } from "msw";
import { server } from "@/test/mocks/server";
import { AutopilotProposalCard } from "./autopilot-proposal-card";

vi.mock("next/navigation", () => ({
  usePathname: () => "/autopilot",
  useRouter: () => ({
    push: vi.fn(),
    replace: vi.fn(),
  }),
}));

vi.mock("@/hooks/use-auth", () => ({
  useAuth: () => ({ isAuthenticated: true, user: { id: "u1" }, isLoading: false, logout: vi.fn() }),
}));

describe("AutopilotProposalCard", () => {
  it("renders nothing when count is 0", async () => {
    server.use(
      http.get("/api/v1/projects/proposals/summary", () =>
        HttpResponse.json({ data: { count: 0 } }),
      ),
      http.get("/api/v1/projects", () =>
        HttpResponse.json({ data: [] }),
      ),
    );

    const { container } = renderWithProviders(<AutopilotProposalCard />);

    // Wait for queries to settle, then check it renders nothing
    await waitFor(() => {
      expect(container.querySelector(".border-purple-200")).not.toBeInTheDocument();
    });
  });

  it("renders proposal count and review button when proposals exist", async () => {
    server.use(
      http.get("/api/v1/projects/proposals/summary", () =>
        HttpResponse.json({ data: { count: 3 } }),
      ),
      http.get("/api/v1/projects", () =>
        HttpResponse.json({
          data: [
            {
              id: "p-1",
              title: "Refactor auth module",
              status: "proposed",
            },
          ],
        }),
      ),
    );

    renderWithProviders(<AutopilotProposalCard />);

    await waitFor(() => {
      expect(
        screen.getByText("PM found 3 strategic opportunities"),
      ).toBeInTheDocument();
    });

    expect(screen.getByText("Review proposals")).toBeInTheDocument();
    expect(
      screen.getByText("Top proposal: Refactor auth module"),
    ).toBeInTheDocument();
  });

  it("uses singular label for 1 proposal", async () => {
    server.use(
      http.get("/api/v1/projects/proposals/summary", () =>
        HttpResponse.json({ data: { count: 1 } }),
      ),
      http.get("/api/v1/projects", () =>
        HttpResponse.json({
          data: [{ id: "p-1", title: "Fix billing", status: "proposed" }],
        }),
      ),
    );

    renderWithProviders(<AutopilotProposalCard />);

    await waitFor(() => {
      expect(
        screen.getByText("PM found 1 strategic opportunity"),
      ).toBeInTheDocument();
    });
  });
});
