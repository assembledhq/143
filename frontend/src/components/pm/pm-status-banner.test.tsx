import { describe, it, expect, vi, type Mock } from "vitest";
import { http, HttpResponse } from "msw";
import { renderWithProviders, screen, userEvent, waitFor } from "@/test/test-utils";
import { server } from "@/test/mocks/server";
import { PMStatusBanner } from "./pm-status-banner";
import { useAnalyze } from "@/hooks/use-analyze";

vi.mock("next/link", () => ({
  default: ({ children, href, ...props }: React.ComponentProps<"a"> & { href: string }) => (
    <a href={href} {...props}>{children}</a>
  ),
}));

vi.mock("lucide-react", () => {
  const icon = (name: string) => {
    const Component = (props: Record<string, unknown>) => <span data-testid={`icon-${name}`} {...props} />;
    Component.displayName = name;
    return Component;
  };
  return {
    RefreshCw: icon("RefreshCw"),
    Plus: icon("Plus"),
    Activity: icon("Activity"),
    CheckCircle2: icon("CheckCircle2"),
    XCircle: icon("XCircle"),
    Clock: icon("Clock"),
  };
});

vi.mock("@/hooks/use-analyze");
const mockUseAnalyze = useAnalyze as Mock;

function defaultAnalyze(overrides: Partial<ReturnType<typeof useAnalyze>> = {}) {
  return {
    isAnalyzing: false,
    isPending: false,
    analyzeError: null,
    handleAnalyze: vi.fn(),
    dismissError: vi.fn(),
    ...overrides,
  };
}

describe("PMStatusBanner", () => {
  it("renders idle state", async () => {
    mockUseAnalyze.mockReturnValue(defaultAnalyze());

    renderWithProviders(<PMStatusBanner hasActivePlanSession={false} />);

    await waitFor(() => {
      expect(screen.getByText("PM Agent")).toBeInTheDocument();
    });
    expect(screen.getByText("Idle")).toBeInTheDocument();
    expect(screen.getByText("Analyze Now")).toBeInTheDocument();
  });

  it("renders running state", () => {
    mockUseAnalyze.mockReturnValue(defaultAnalyze({ isAnalyzing: true }));

    renderWithProviders(<PMStatusBanner hasActivePlanSession={false} />);

    expect(screen.getByText("Running")).toBeInTheDocument();
    expect(screen.getByText("Analyzing...")).toBeInTheDocument();
    expect(
      screen.getByText("Analyzing issues, reviewing context, and generating a plan...")
    ).toBeInTheDocument();
  });

  it("renders completed/active state", async () => {
    mockUseAnalyze.mockReturnValue(defaultAnalyze());

    server.use(
      http.get("*/api/v1/pm/status", () => {
        return HttpResponse.json({
          data: {
            is_running: false,
            last_run_status: "completed",
            issues_reviewed: 0,
            success_rate: 0,
            success_count: 0,
            total_delegated: 0,
          },
        });
      })
    );

    renderWithProviders(<PMStatusBanner hasActivePlanSession={false} />);

    await waitFor(() => {
      expect(screen.getByText("Active")).toBeInTheDocument();
    });
  });

  it("renders failed state", async () => {
    mockUseAnalyze.mockReturnValue(defaultAnalyze());

    server.use(
      http.get("*/api/v1/pm/status", () => {
        return HttpResponse.json({
          data: {
            is_running: false,
            last_run_status: "failed",
            issues_reviewed: 0,
            success_rate: 0,
            success_count: 0,
            total_delegated: 0,
          },
        });
      })
    );

    renderWithProviders(<PMStatusBanner hasActivePlanSession={false} />);

    await waitFor(() => {
      expect(screen.getByText("Attention needed")).toBeInTheDocument();
    });
  });

  it("shows status details", async () => {
    mockUseAnalyze.mockReturnValue(defaultAnalyze());

    server.use(
      http.get("*/api/v1/pm/status", () => {
        return HttpResponse.json({
          data: {
            is_running: false,
            last_run_at: "2026-03-01T10:00:00Z",
            issues_reviewed: 10,
            next_run_in: "5m",
            success_rate: 0,
            success_count: 0,
            total_delegated: 0,
          },
        });
      })
    );

    renderWithProviders(<PMStatusBanner hasActivePlanSession={false} />);

    await waitFor(() => {
      expect(screen.getByText("10 issues reviewed")).toBeInTheDocument();
    });
    expect(screen.getByText("Next run: 5m")).toBeInTheDocument();
  });

  it("shows delegation stats", async () => {
    mockUseAnalyze.mockReturnValue(defaultAnalyze());

    server.use(
      http.get("*/api/v1/pm/status", () => {
        return HttpResponse.json({
          data: {
            is_running: false,
            issues_reviewed: 0,
            success_rate: 80,
            success_count: 8,
            total_delegated: 10,
          },
        });
      })
    );

    renderWithProviders(<PMStatusBanner hasActivePlanSession={false} />);

    await waitFor(() => {
      expect(screen.getByText("80% success rate")).toBeInTheDocument();
    });
    expect(screen.getByText("8 succeeded")).toBeInTheDocument();
    expect(screen.getByText("2 failed")).toBeInTheDocument();
  });

  it("displays and dismisses analyze error", async () => {
    const dismissError = vi.fn();
    mockUseAnalyze.mockReturnValue(
      defaultAnalyze({ analyzeError: "Failed to start analysis.", dismissError })
    );

    renderWithProviders(<PMStatusBanner hasActivePlanSession={false} />);

    expect(screen.getByText("Failed to start analysis.")).toBeInTheDocument();

    const user = userEvent.setup();
    await user.click(screen.getByText("dismiss"));
    expect(dismissError).toHaveBeenCalled();
  });

  it("renders Manual Session link", () => {
    mockUseAnalyze.mockReturnValue(defaultAnalyze());

    renderWithProviders(<PMStatusBanner hasActivePlanSession={false} />);

    const link = screen.getByRole("link", { name: /Manual Session/ });
    expect(link).toBeInTheDocument();
    expect(link).toHaveAttribute("href", "/sessions/new");
  });
});
