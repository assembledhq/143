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
import type { PreviewCurrentResponse, PreviewListMeta } from "@/lib/types";

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
  counts: { running: 1, resumable: 1, attention: 0, recent: 1 },
  pool: { user_active: 1, user_max: 4, auto_active: 2, auto_max: 6 },
};

function preview(
  overrides: Partial<PreviewCurrentResponse>,
): PreviewCurrentResponse {
  return {
    preview_group_id: "group-1",
    current_target_id: "target-1",
    current_preview_id: "preview-1",
    repository_id: "repo-1",
    repository_full_name: "assembledhq/143",
    group_kind: "branch",
    branch: "feature/checkout",
    latest_commit_sha: "0123456789abcdef",
    running_commit_sha: "0123456789abcdef",
    source_type: "manual",
    status: "ready",
    freshness: "current",
    stable_url: "https://app.143.dev/previews/target-1",
    preview_url: "https://preview.143.dev",
    pinned: false,
    created_at: "2026-06-10T12:00:00Z",
    last_activity_at: "2026-06-10T12:00:00Z",
    attempt_count: 1,
    target_count: 1,
    resumable: false,
    launch: { action: "open", primary_label: "Open" },
    ...overrides,
  };
}

function installPreviewHandlers(
  byScope: Record<string, PreviewCurrentResponse[]>,
  requests: string[] = [],
) {
  server.use(
    http.get("*/api/v1/repositories", () =>
      HttpResponse.json({ data: repositories, meta: {} }),
    ),
    http.get("*/api/v1/previews/current", ({ request }) => {
      const url = new URL(request.url);
      const scope = url.searchParams.get("scope") ?? "recent";
      requests.push(url.search);
      return HttpResponse.json({ data: byScope[scope] ?? [], meta });
    }),
    http.get("*/api/v1/repositories/:id/branches", () =>
      HttpResponse.json({
        data: [
          { name: "main", protected: true },
          { name: "feature/session-input-branch", protected: false },
        ],
        meta: {},
      }),
    ),
    http.get("*/api/v1/previews/configs", () =>
      HttpResponse.json({
        data: {
          names: [],
          default_name: "",
          selected_name: "",
          requires_selection: false,
        },
      }),
    ),
  );
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((res) => {
    resolve = res;
  });
  return { promise, resolve };
}

describe("PreviewsPage", () => {
  beforeEach(() => {
    currentUserRole.value = "member";
  });

  it("renders running, resumable, and recent sections with counts and pool usage", async () => {
    installPreviewHandlers({
      running: [
        preview({
          preview_group_id: "running-group",
          current_target_id: "running-target",
          current_preview_id: "running-preview",
          branch: "feature/live-preview",
          source_type: "pull_request",
          group_kind: "pull_request",
          pull_request_number: 42,
          source_id: "assembledhq/143#42",
          source_url: "https://github.com/assembledhq/143/pull/42",
        }),
      ],
      resumable: [
        preview({
          preview_group_id: "warm-group",
          current_target_id: "warm-target",
          current_preview_id: "warm-preview",
          repository_id: "repo-2",
          repository_full_name: "assembledhq/docs",
          branch: "feature/warm-link",
          status: "stopped",
          source_type: "pull_request",
          group_kind: "pull_request",
          pull_request_number: 17,
          source_id: "assembledhq/docs#17",
          stopped_reason: "warm_policy",
          resumable: true,
          resume_estimate_seconds: 30,
          preview_url: undefined,
        }),
      ],
      recent: [
        preview({
          preview_group_id: "recent-group",
          current_target_id: "recent-target",
          current_preview_id: "recent-preview",
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
    expect(await screen.findAllByText("PR #42 - feature/live-preview")).toHaveLength(2);
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
    expect(screen.getAllByText("PR #17 - feature/warm-link")[0]).toBeInTheDocument();
    expect(screen.getAllByText("Ready")[0]).toBeInTheDocument();
    expect(screen.getAllByText("Stopped")[0]).toBeInTheDocument();
    expect(screen.getAllByText("Failed")[0]).toBeInTheDocument();
    expect(screen.getByText("resumes in ~30s")).toBeInTheDocument();
    expect(screen.getByText("stopped after error")).toBeInTheDocument();
    expect(screen.getAllByText("PR #17")[0]).toBeInTheDocument();
  });

  it("sends scoped filters and wires stop, resume, and start-latest actions", async () => {
    const listRequests: string[] = [];
    const mutationPaths: string[] = [];
    installPreviewHandlers(
      {
        running: [preview({ preview_group_id: "running-group", current_target_id: "running-target", current_preview_id: "running-preview" })],
        resumable: [
          preview({
            preview_group_id: "warm-group",
            current_target_id: "warm-target",
            current_preview_id: "warm-preview",
            branch: "feature/warm-link",
            status: "stopped",
            preview_url: undefined,
          }),
        ],
        recent: [
          preview({
            preview_group_id: "recent-group",
            current_target_id: "recent-target",
            current_preview_id: "recent-preview",
            branch: "fix/startup-error",
            status: "failed",
            preview_url: undefined,
          }),
        ],
      },
      listRequests,
    );
    server.use(
      http.post("*/api/v1/previews/current/:id/stop", ({ request }) => {
        mutationPaths.push(new URL(request.url).pathname);
        return HttpResponse.json({ data: preview({}) });
      }),
      http.post("*/api/v1/previews/current/:id/restart", ({ request }) => {
        mutationPaths.push(new URL(request.url).pathname);
        return HttpResponse.json({ data: preview({}) });
      }),
      http.post("*/api/v1/previews/current/:id/start-latest", ({ request }) => {
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
          expect.stringContaining("scope=attention"),
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
          "/api/v1/previews/current/running-group/stop",
          "/api/v1/previews/current/warm-group/restart",
          "/api/v1/previews/current/warm-group/start-latest",
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

  it("opens the create preview dialog from the empty state action", async () => {
    installPreviewHandlers({ running: [], resumable: [], recent: [] });

    renderWithProviders(<PreviewsPage />);

    expect(await screen.findByText("No previews yet")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: /create preview/i }));
    expect(
      await screen.findByRole("dialog", { name: "Create preview" }),
    ).toBeInTheDocument();
  });

  it("opens create preview in a modal and submits the preselected repo and branch", async () => {
    const createRequests: unknown[] = [];
    installPreviewHandlers({ running: [], resumable: [], recent: [] });
    server.use(
      http.post("*/api/v1/previews", async ({ request }) => {
        createRequests.push(await request.json());
        return HttpResponse.json({
          data: preview({
            current_target_id: "target-created",
            current_preview_id: "preview-created",
            repository_id: "repo-1",
            branch: "feature/session-input-branch",
          }),
        });
      }),
    );

    renderWithProviders(<PreviewsPage />, {
      searchParams: {
        repo: "repo-1",
        branch: "feature/session-input-branch",
      },
    });

    await userEvent.click(await screen.findByRole("button", { name: /new preview/i }));

    expect(
      await screen.findByRole("dialog", { name: "Create preview" }),
    ).toBeInTheDocument();
    expect(screen.getByRole("combobox", { name: "Repository" })).toHaveTextContent(
      "assembledhq/143",
    );
    expect(screen.getByRole("button", { name: "Target branch" })).toHaveTextContent(
      "feature/session-input-branch",
    );

    await userEvent.click(screen.getByRole("button", { name: "Start preview" }));

    await waitFor(() => {
      expect(createRequests).toEqual([
        expect.objectContaining({
          repository_id: "repo-1",
          branch: "feature/session-input-branch",
          source: { type: "manual" },
        }),
      ]);
    });
  });

  it("keeps the initial preview content quiet until all sections resolve empty", async () => {
    const previewsReleased = deferred<void>();
    server.use(
      http.get("*/api/v1/repositories", () =>
        HttpResponse.json({ data: repositories, meta: {} }),
      ),
      http.get("*/api/v1/previews/current", async () => {
        await previewsReleased.promise;
        return HttpResponse.json({ data: [], meta });
      }),
    );

    renderWithProviders(<PreviewsPage />);

    expect(screen.queryByText("Loading previews...")).not.toBeInTheDocument();
    expect(screen.queryByText("No previews are running.")).not.toBeInTheDocument();
    expect(screen.queryByText("No previews yet")).not.toBeInTheDocument();

    previewsReleased.resolve();

    expect(await screen.findByText("No previews yet")).toBeInTheDocument();
  });

  it("shows a stable per-section error state instead of the empty state when the list API fails", async () => {
    server.use(
      http.get("*/api/v1/repositories", () =>
        HttpResponse.json({ data: repositories, meta: {} }),
      ),
      http.get("*/api/v1/previews/current", () =>
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
      4,
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
      http.get("*/api/v1/previews/current", ({ request }) => {
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

  it("renders Out of date badge and running-vs-head SHA detail for outdated rows", async () => {
    installPreviewHandlers({
      attention: [
        preview({
          preview_group_id: "outdated-group",
          branch: "feature/stale",
          status: "ready",
          freshness: "outdated",
          running_commit_sha: "aabb1122",
          latest_commit_sha: "ccdd3344",
          preview_url: "https://preview.143.dev",
        }),
      ],
    });

    renderWithProviders(<PreviewsPage />);

    const attentionSection = await screen.findByRole("region", {
      name: /needs attention/i,
    });
    expect(within(attentionSection).getAllByText("Out of date")[0]).toBeInTheDocument();
    expect(
      within(attentionSection).getAllByText(/running aabb1122, branch is ccdd3344/)[0],
    ).toBeInTheDocument();
  });

  it("does not render unsafe external preview or source URLs as links", async () => {
    installPreviewHandlers({
      running: [
        preview({
          preview_group_id: "unsafe-group",
          branch: "feature/unsafe-url",
          source_type: "api",
          source_url: "javascript:alert(1)",
          preview_url: "javascript:alert(2)",
        }),
      ],
    });

    renderWithProviders(<PreviewsPage />);

    const runningSection = await screen.findByRole("region", {
      name: /running/i,
    });
    expect(
      within(runningSection).queryByRole("link", { name: /open/i }),
    ).not.toBeInTheDocument();
    const unsafeLinks = within(runningSection)
      .queryAllByRole("link")
      .filter((link) =>
        (link as HTMLAnchorElement)
          .getAttribute("href")
          ?.startsWith("javascript:"),
      );
    expect(unsafeLinks).toHaveLength(0);
  });

  it("renders Pinned indicator in row subtitle for pinned previews", async () => {
    installPreviewHandlers({
      running: [
        preview({
          preview_group_id: "pinned-group",
          branch: "feature/pinned-commit",
          pinned: true,
          running_commit_sha: "deadbeef",
        }),
      ],
    });

    renderWithProviders(<PreviewsPage />);

    const runningSection = await screen.findByRole("region", {
      name: /running/i,
    });
    expect(
      within(runningSection).getAllByText(/Pinned ·/)[0],
    ).toBeInTheDocument();
  });

  it("renders the attention section with failed previews sorted before stopped ones", async () => {
    installPreviewHandlers({
      attention: [
        preview({
          preview_group_id: "stopped-group",
          branch: "feature/stopped",
          status: "stopped",
          freshness: "unknown",
          preview_url: undefined,
        }),
        preview({
          preview_group_id: "failed-group",
          branch: "feature/failed",
          status: "failed",
          freshness: "current",
          stopped_reason: "error",
          preview_url: undefined,
        }),
      ],
    });

    renderWithProviders(<PreviewsPage />);

    const attentionSection = await screen.findByRole("region", {
      name: /needs attention/i,
    });
    expect(within(attentionSection).getAllByText("Failed")[0]).toBeInTheDocument();
    expect(within(attentionSection).getAllByText("Needs attention")[0]).toBeInTheDocument();
  });

  it("keeps mobile stacked row metadata available alongside the desktop table", async () => {
    installPreviewHandlers({
      running: [],
      resumable: [
        preview({
          current_target_id: "warm-target",
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
