import { describe, it, expect, vi } from "vitest";
import { http, HttpResponse } from "msw";
import { renderWithProviders, screen, waitFor } from "@/test/test-utils";
import { server } from "@/test/mocks/server";
import { DecisionsView } from "./decisions-view";

vi.mock("lucide-react", () => {
  const icon = (name: string) => {
    const Component = (props: Record<string, unknown>) => (
      <span data-testid={`icon-${name}`} {...props} />
    );
    Component.displayName = name;
    return Component;
  };
  return {
    CheckCircle2: icon("CheckCircle2"),
    XCircle: icon("XCircle"),
    Clock: icon("Clock"),
    Minus: icon("Minus"),
    TrendingUp: icon("TrendingUp"),
    TrendingDown: icon("TrendingDown"),
  };
});

describe("DecisionsView", () => {
  it("shows loading state", () => {
    server.use(
      http.get("*/api/v1/pm/decisions", () => {
        return new Promise(() => {});
      }),
    );

    renderWithProviders(<DecisionsView />);
    expect(screen.getByText("Loading decisions...")).toBeInTheDocument();
  });

  it("shows error state", async () => {
    server.use(
      http.get("*/api/v1/pm/decisions", () => {
        return HttpResponse.json(
          { error: { code: "INTERNAL", message: "fail" } },
          { status: 500 },
        );
      }),
    );

    renderWithProviders(<DecisionsView />);
    await waitFor(() => {
      expect(
        screen.getByText("Failed to load decision history."),
      ).toBeInTheDocument();
    });
  });

  it("shows empty state", async () => {
    renderWithProviders(<DecisionsView />);
    await waitFor(() => {
      expect(screen.getByText(/No decisions yet/)).toBeInTheDocument();
    });
  });

  it("renders decisions with badges", async () => {
    server.use(
      http.get("*/api/v1/pm/decisions", () => {
        return HttpResponse.json({
          data: [
            {
              id: "d1",
              plan_id: "p1",
              issue_id: "i1",
              issue_title: "Auth timeout",
              project_title: "Project A",
              decision: "delegate",
              reasoning: "Critical",
              outcome: "succeeded",
              created_at: "2026-03-01T10:00:00Z",
            },
            {
              id: "d2",
              plan_id: "p1",
              issue_id: "i2",
              issue_title: "Payment bug",
              project_title: "Project A",
              decision: "skip",
              reasoning: "Low priority",
              created_at: "2026-03-01T10:00:00Z",
            },
            {
              id: "d3",
              plan_id: "p1",
              issue_id: "i3",
              issue_title: "CSS issue",
              project_title: "Project B",
              decision: "cluster",
              reasoning: "Related issues",
              outcome: "failed",
              created_at: "2026-03-02T10:00:00Z",
            },
          ],
          summary: {
            total_delegated: 10,
            succeeded: 8,
            failed: 1,
            still_open: 1,
          },
          meta: {},
        });
      }),
    );

    renderWithProviders(<DecisionsView />);

    await waitFor(() => {
      expect(screen.getByText("Auth timeout")).toBeInTheDocument();
    });

    expect(screen.getByText("Payment bug")).toBeInTheDocument();
    expect(screen.getByText("CSS issue")).toBeInTheDocument();
    // Filter buttons + decision badges both show these labels
    expect(screen.getAllByText("Delegated").length).toBeGreaterThanOrEqual(1);
    expect(screen.getAllByText("Skipped").length).toBeGreaterThanOrEqual(1);
    expect(screen.getAllByText("Clustered").length).toBeGreaterThanOrEqual(1);
    expect(screen.getAllByText("Succeeded").length).toBeGreaterThanOrEqual(1);
    expect(screen.getAllByText("Failed").length).toBeGreaterThanOrEqual(1);
  });

  it("renders summary bar with stat cards", async () => {
    server.use(
      http.get("*/api/v1/pm/decisions", () => {
        return HttpResponse.json({
          data: [
            {
              id: "d1",
              plan_id: "p1",
              issue_id: "i1",
              issue_title: "Auth timeout",
              decision: "delegate",
              reasoning: "r",
              outcome: "succeeded",
              created_at: "2026-03-01T10:00:00Z",
            },
          ],
          summary: {
            total_delegated: 10,
            succeeded: 8,
            failed: 1,
            still_open: 1,
          },
          meta: {},
        });
      }),
    );

    renderWithProviders(<DecisionsView />);

    await waitFor(() => {
      expect(screen.getByText("80%")).toBeInTheDocument();
    });
    expect(screen.getByText("Success rate")).toBeInTheDocument();
    expect(screen.getByText("8")).toBeInTheDocument();
    expect(screen.getAllByText("Succeeded").length).toBeGreaterThanOrEqual(1);
  });

  it("renders Still open outcome for decisions without outcome", async () => {
    server.use(
      http.get("*/api/v1/pm/decisions", () => {
        return HttpResponse.json({
          data: [
            {
              id: "d1",
              plan_id: "p1",
              issue_id: "i1",
              issue_title: "Pending fix",
              decision: "delegate",
              reasoning: "r",
              created_at: "2026-03-01T10:00:00Z",
            },
          ],
          summary: {
            total_delegated: 1,
            succeeded: 0,
            failed: 0,
            still_open: 1,
          },
          meta: {},
        });
      }),
    );

    renderWithProviders(<DecisionsView />);

    await waitFor(() => {
      expect(screen.getByText("Pending fix")).toBeInTheDocument();
    });
    expect(screen.getAllByText("Still open").length).toBeGreaterThanOrEqual(1);
  });

  it("does not render summary bar when total_delegated is 0", async () => {
    server.use(
      http.get("*/api/v1/pm/decisions", () => {
        return HttpResponse.json({
          data: [
            {
              id: "d1",
              plan_id: "p1",
              issue_id: "i1",
              issue_title: "Skipped issue",
              decision: "skip",
              reasoning: "r",
              created_at: "2026-03-01T10:00:00Z",
            },
          ],
          summary: {
            total_delegated: 0,
            succeeded: 0,
            failed: 0,
            still_open: 0,
          },
          meta: {},
        });
      }),
    );

    renderWithProviders(<DecisionsView />);

    await waitFor(() => {
      expect(screen.getByText("Skipped issue")).toBeInTheDocument();
    });
    expect(screen.queryByText(/Success rate/)).not.toBeInTheDocument();
  });
});
