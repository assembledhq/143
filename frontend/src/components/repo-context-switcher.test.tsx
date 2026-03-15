import { describe, it, expect, vi } from "vitest";
import { renderWithProviders, screen, userEvent, waitFor } from "@/test/test-utils";
import { RepoContextSwitcher } from "./repo-context-switcher";
import { http, HttpResponse } from "msw";
import { server } from "@/test/mocks/server";

vi.mock("next/navigation", () => ({
  usePathname: () => "/sessions",
  useRouter: () => ({ push: vi.fn(), replace: vi.fn() }),
}));

const twoRepos = [
  {
    repository_id: "repo-1",
    full_name: "acme/api-server",
    active_session_count: 3,
    latest_session_status: "running",
    active_project_count: 2,
  },
  {
    repository_id: "repo-2",
    full_name: "acme/web-app",
    active_session_count: 0,
    latest_session_status: null,
    active_project_count: 0,
  },
];

const fourRepos = [
  ...twoRepos,
  {
    repository_id: "repo-3",
    full_name: "acme/mobile-app",
    active_session_count: 1,
    latest_session_status: "needs_human_guidance",
    active_project_count: 1,
  },
  {
    repository_id: "repo-4",
    full_name: "acme/infra",
    active_session_count: 0,
    latest_session_status: "failed",
    active_project_count: 0,
  },
];

function mockSummary(data: typeof twoRepos) {
  server.use(
    http.get("/api/v1/repositories/summary", () => {
      return HttpResponse.json({ data, meta: {} });
    })
  );
}

describe("RepoContextSwitcher", () => {
  it("renders nothing when org has only 1 repo", async () => {
    mockSummary([twoRepos[0]]);

    const { container } = renderWithProviders(<RepoContextSwitcher />);

    await waitFor(() => {
      expect(container.querySelector('[data-testid="repo-context-switcher"]')).not.toBeInTheDocument();
    });
  });

  it("renders nothing when org has zero repos", async () => {
    mockSummary([]);

    const { container } = renderWithProviders(<RepoContextSwitcher />);

    await waitFor(() => {
      expect(container.querySelector('[data-testid="repo-context-switcher"]')).not.toBeInTheDocument();
    });
  });

  it("renders the context switcher when org has 2+ repos", async () => {
    mockSummary(twoRepos);

    renderWithProviders(<RepoContextSwitcher />);

    await waitFor(() => {
      expect(screen.getByTestId("repo-context-switcher")).toBeInTheDocument();
    });

    expect(screen.getByText("All repositories")).toBeInTheDocument();
  });

  it("opens dropdown and shows repo items on click", async () => {
    mockSummary(twoRepos);
    const user = userEvent.setup();

    renderWithProviders(<RepoContextSwitcher />);

    await waitFor(() => {
      expect(screen.getByTestId("repo-context-switcher")).toBeInTheDocument();
    });

    await user.click(screen.getByTestId("repo-context-switcher"));

    await waitFor(() => {
      expect(screen.getByText("acme/api-server")).toBeInTheDocument();
      expect(screen.getByText("acme/web-app")).toBeInTheDocument();
    });
  });

  it("shows active session count badge for repos with active sessions", async () => {
    mockSummary(twoRepos);
    const user = userEvent.setup();

    renderWithProviders(<RepoContextSwitcher />);

    await waitFor(() => {
      expect(screen.getByTestId("repo-context-switcher")).toBeInTheDocument();
    });

    await user.click(screen.getByTestId("repo-context-switcher"));

    await waitFor(() => {
      expect(screen.getByText("3")).toBeInTheDocument();
    });
  });

  it("shows search input when 4+ repos and filters results", async () => {
    mockSummary(fourRepos);
    const user = userEvent.setup();

    renderWithProviders(<RepoContextSwitcher />);

    await waitFor(() => {
      expect(screen.getByTestId("repo-context-switcher")).toBeInTheDocument();
    });

    await user.click(screen.getByTestId("repo-context-switcher"));

    await waitFor(() => {
      expect(screen.getByPlaceholderText("Search repos...")).toBeInTheDocument();
    });

    await user.type(screen.getByPlaceholderText("Search repos..."), "mobile");

    await waitFor(() => {
      expect(screen.getByText("acme/mobile-app")).toBeInTheDocument();
      expect(screen.queryByText("acme/api-server")).not.toBeInTheDocument();
    });
  });

  it("selects a repo when clicking a menu item", async () => {
    mockSummary(twoRepos);
    const user = userEvent.setup();

    renderWithProviders(<RepoContextSwitcher />);

    await waitFor(() => {
      expect(screen.getByTestId("repo-context-switcher")).toBeInTheDocument();
    });

    await user.click(screen.getByTestId("repo-context-switcher"));

    await waitFor(() => {
      expect(screen.getByText("acme/api-server")).toBeInTheDocument();
    });

    await user.click(screen.getByText("acme/api-server"));

    await waitFor(() => {
      expect(screen.getByText("api-server")).toBeInTheDocument();
    });
  });
});
