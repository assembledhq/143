import { describe, expect, it, vi, beforeEach } from "vitest";
import { http, HttpResponse } from "msw";
import {
  renderWithProviders,
  screen,
  userEvent,
  waitFor,
  within,
} from "@/test/test-utils";
import { server } from "@/test/mocks/server";
import PreviewsPage from "./page";
import type { BranchPreviewResponse, PreviewListMeta } from "@/lib/types";

const currentUserRole = vi.hoisted(() => ({ value: "member" }));

vi.mock("@/hooks/use-auth", () => ({
  useAuth: () => ({
    user: { id: "user-1", role: currentUserRole.value },
    isLoading: false,
  }),
}));

const repositories = [
  {
    id: "repo-1",
    org_id: "org-1",
    full_name: "assembledhq/143",
    default_branch: "main",
    private: false,
    clone_url: "https://github.com/assembledhq/143.git",
    github_id: 143,
    installation_id: 1,
    status: "active",
    created_at: "2026-06-10T12:00:00Z",
    updated_at: "2026-06-10T12:00:00Z",
  },
  {
    id: "repo-2",
    org_id: "org-1",
    full_name: "assembledhq/docs",
    default_branch: "main",
    private: false,
    clone_url: "https://github.com/assembledhq/docs.git",
    github_id: 144,
    installation_id: 1,
    status: "active",
    created_at: "2026-06-10T12:00:00Z",
    updated_at: "2026-06-10T12:00:00Z",
  },
];

const meta: PreviewListMeta = {
  counts: { running: 1, resumable: 1, recent: 1 },
  pool: { user_active: 1, user_max: 4, auto_active: 2, auto_max: 6 },
};

function preview(
  overrides: Partial<BranchPreviewResponse>,
): BranchPreviewResponse {
  return {
    target_id: "target-1",
    preview_id: "preview-1",
    repository_id: "repo-1",
    repository_full_name: "assembledhq/143",
    branch: "feature/checkout",
    commit_sha: "0123456789abcdef",
    source_type: "manual",
    status: "ready",
    stable_url: "https://app.143.dev/previews/target-1",
    preview_url: "https://preview.143.dev",
    created_at: "2026-06-10T12:00:00Z",
    ...overrides,
  };
}

function installPreviewHandlers(
  byScope: Record<string, BranchPreviewResponse[]>,
  requests: string[] = [],
) {
  server.use(
    http.get("*/api/v1/repositories", () =>
      HttpResponse.json({ data: repositories, meta: {} }),
    ),
    http.get("*/api/v1/previews", ({ request }) => {
      const url = new URL(request.url);
      const scope = url.searchParams.get("scope") ?? "recent";
      requests.push(url.search);
      return HttpResponse.json({ data: byScope[scope] ?? [], meta });
    }),
  );
}

describe("PreviewsPage", () => {
  beforeEach(() => {
    currentUserRole.value = "member";
  });

  it("renders running, resumable, and recent sections with counts and pool usage", async () => {
    installPreviewHandlers({
      running: [
        preview({
          target_id: "running-target",
          preview_id: "running-preview",
          branch: "feature/live-preview",
          source_type: "pull_request",
          source_id: "assembledhq/143#42",
          source_url: "https://github.com/assembledhq/143/pull/42",
        }),
      ],
      resumable: [
        preview({
          target_id: "warm-target",
          preview_id: "warm-preview",
          repository_id: "repo-2",
          repository_full_name: "assembledhq/docs",
          branch: "feature/warm-link",
          status: "stopped",
          source_type: "pull_request",
          source_id: "assembledhq/docs#17",
          stopped_reason: "warm_policy",
          resumable: true,
          resume_estimate_seconds: 30,
          preview_url: undefined,
        }),
      ],
      recent: [
        preview({
          target_id: "recent-target",
          preview_id: "recent-preview",
          branch: "fix/startup-error",
          status: "failed",
          stopped_reason: "error",
          error: "install failed",
          preview_url: undefined,
        }),
      ],
    });

    renderWithProviders(<PreviewsPage />);

    expect(
      await screen.findByRole("heading", { level: 1, name: "Previews" }),
    ).toBeInTheDocument();
    expect(await screen.findAllByText("feature/live-preview")).toHaveLength(2);
    expect(
      screen.getByRole("heading", { level: 2, name: "Running (1)" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("heading", { level: 2, name: "Ready to resume (1)" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("heading", { level: 2, name: "Recent (1)" }),
    ).toBeInTheDocument();
    expect(screen.getByText("Pool: 3 of 10 previews")).toBeInTheDocument();
    expect(screen.getAllByText("feature/warm-link")[0]).toBeInTheDocument();
    expect(screen.getByText("resumes in ~30s")).toBeInTheDocument();
    expect(screen.getByText("stopped after error")).toBeInTheDocument();
    expect(screen.getAllByText("assembledhq/docs · PR #17")[0]).toBeInTheDocument();
  });

  it("sends scoped filters and wires stop, resume, and start-latest actions", async () => {
    const listRequests: string[] = [];
    const mutationPaths: string[] = [];
    installPreviewHandlers(
      {
        running: [preview({ target_id: "running-target", preview_id: "running-preview" })],
        resumable: [
          preview({
            target_id: "warm-target",
            preview_id: "warm-preview",
            branch: "feature/warm-link",
            status: "stopped",
            preview_url: undefined,
          }),
        ],
        recent: [
          preview({
            target_id: "recent-target",
            preview_id: "recent-preview",
            branch: "fix/startup-error",
            status: "failed",
            preview_url: undefined,
          }),
        ],
      },
      listRequests,
    );
    server.use(
      http.post("*/api/v1/previews/:id/stop", ({ request }) => {
        mutationPaths.push(new URL(request.url).pathname);
        return HttpResponse.json({ data: preview({}) });
      }),
      http.post("*/api/v1/previews/:id/restart", ({ request }) => {
        mutationPaths.push(new URL(request.url).pathname);
        return HttpResponse.json({ data: preview({}) });
      }),
      http.post("*/api/v1/previews/:id/start-latest", ({ request }) => {
        mutationPaths.push(new URL(request.url).pathname);
        return HttpResponse.json({ data: preview({}) });
      }),
    );

    renderWithProviders(<PreviewsPage />);

    await userEvent.type(
      await screen.findByPlaceholderText("Search branch, repo, or PR"),
      "warm",
    );
    await userEvent.click(screen.getByRole("combobox", { name: "Repository" }));
    await userEvent.click(await screen.findByRole("option", { name: "assembledhq/docs" }));

    await waitFor(() => {
      expect(listRequests).toEqual(
        expect.arrayContaining([
          expect.stringContaining("scope=running"),
          expect.stringContaining("scope=resumable"),
          expect.stringContaining("scope=recent"),
          expect.stringContaining("repository_id=repo-2"),
          expect.stringContaining("q=warm"),
        ]),
      );
    });

    await userEvent.click(screen.getAllByRole("button", { name: /stop/i })[0]);
    await userEvent.click(screen.getAllByRole("button", { name: /resume/i })[0]);
    await userEvent.click(
      screen.getAllByRole("button", { name: /start latest/i })[0],
    );

    await waitFor(() => {
      expect(mutationPaths).toEqual(
        expect.arrayContaining([
          "/api/v1/previews/running-preview/stop",
          "/api/v1/previews/warm-preview/restart",
          "/api/v1/previews/warm-preview/start-latest",
        ]),
      );
    });
  });

  it("hides mutation affordances from viewers but still shows preview content", async () => {
    currentUserRole.value = "viewer";
    installPreviewHandlers({
      running: [preview({ branch: "feature/read-only" })],
      resumable: [preview({ status: "stopped", preview_url: undefined })],
      recent: [],
    });

    renderWithProviders(<PreviewsPage />);

    expect(await screen.findAllByText("feature/read-only")).toHaveLength(2);
    expect(screen.queryByRole("link", { name: /new preview/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /stop/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /resume/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /start latest/i })).not.toBeInTheDocument();
  });

  it("renders the empty state with the create action when every scope is empty", async () => {
    installPreviewHandlers({ running: [], resumable: [], recent: [] });

    renderWithProviders(<PreviewsPage />);

    expect(await screen.findByText("No previews yet")).toBeInTheDocument();
    expect(
      screen.getByRole("link", { name: /create preview/i }),
    ).toHaveAttribute("href", "/previews/new");
  });

  it("shows a stable per-section error state instead of the empty state when the list API fails", async () => {
    server.use(
      http.get("*/api/v1/repositories", () =>
        HttpResponse.json({ data: repositories, meta: {} }),
      ),
      http.get("*/api/v1/previews", () =>
        HttpResponse.json(
          {
            error: {
              code: "PREVIEW_LIST_FAILED",
              message: "failed to count previews",
            },
          },
          { status: 500 },
        ),
      ),
    );

    renderWithProviders(<PreviewsPage />);

    expect(await screen.findAllByText("Failed to load previews.")).toHaveLength(
      3,
    );
    expect(screen.queryByText("No previews yet")).not.toBeInTheDocument();
    expect(screen.queryByText("Loading previews...")).not.toBeInTheDocument();

    // Once the backend recovers, a manual retry repopulates the sections.
    installPreviewHandlers({
      running: [preview({ branch: "feature/recovered" })],
      resumable: [],
      recent: [],
    });
    await userEvent.click(screen.getAllByRole("button", { name: /retry/i })[0]);
    expect(await screen.findAllByText("feature/recovered")).toHaveLength(2);
  });

  it("keeps previously loaded rows visible when interval refetches start failing", async () => {
    let failing = false;
    const failedRequests: string[] = [];
    server.use(
      http.get("*/api/v1/repositories", () =>
        HttpResponse.json({ data: repositories, meta: {} }),
      ),
      http.get("*/api/v1/previews", ({ request }) => {
        const url = new URL(request.url);
        if (failing) {
          failedRequests.push(url.search);
          return HttpResponse.json(
            {
              error: {
                code: "PREVIEW_LIST_FAILED",
                message: "failed to count previews",
              },
            },
            { status: 500 },
          );
        }
        const scope = url.searchParams.get("scope");
        return HttpResponse.json({
          data:
            scope === "running"
              ? [preview({ branch: "feature/sticky-rows" })]
              : [],
          meta,
        });
      }),
    );

    renderWithProviders(<PreviewsPage />);
    expect(await screen.findAllByText("feature/sticky-rows")).toHaveLength(2);

    failing = true;
    // Two failed polls guarantee the first error has settled into the query
    // cache; stale rows must survive it rather than yield to an error card.
    await waitFor(() => expect(failedRequests.length).toBeGreaterThanOrEqual(2));
    expect(screen.getAllByText("feature/sticky-rows")).toHaveLength(2);
    expect(
      screen.queryByText("Failed to load previews."),
    ).not.toBeInTheDocument();
  });

  it("keeps mobile stacked row metadata available alongside the desktop table", async () => {
    installPreviewHandlers({
      running: [],
      resumable: [
        preview({
          target_id: "warm-target",
          branch: "feature/mobile-warm",
          repository_full_name: "assembledhq/docs",
          source_type: "pull_request",
          source_id: "assembledhq/docs#24",
          status: "stopped",
          preview_url: undefined,
        }),
      ],
      recent: [],
    });

    renderWithProviders(<PreviewsPage />);

    const section = await screen.findByRole("region", {
      name: "Ready to resume (1)",
    });
    expect(
      within(section).getAllByText("assembledhq/docs · PR #24")[0],
    ).toBeInTheDocument();
  });
});
