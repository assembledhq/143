import { act } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { http, HttpResponse } from "msw";

import { renderWithProviders, screen, userEvent, waitFor } from "@/test/test-utils";
import { server } from "@/test/mocks/server";
import {
  PREVIEW_BOOTSTRAP_COMPLETE_EVENT,
  PREVIEW_BOOTSTRAP_READY_EVENT,
} from "@/lib/preview-bootstrap";

const searchParamsMock = vi.hoisted(() => ({ value: new URLSearchParams() }));
const currentUserRole = vi.hoisted(() => ({ value: "member" }));

vi.mock("next/navigation", async () => {
  const actual = await vi.importActual<typeof import("next/navigation")>("next/navigation");
  return {
    ...actual,
    useSearchParams: () => searchParamsMock.value,
  };
});

vi.mock("@/hooks/use-auth", () => ({
  useAuth: () => ({
    user: { id: "user-1", role: currentUserRole.value },
    isLoading: false,
  }),
}));

import { PullRequestPreviewContent } from "./page";

afterEach(() => {
  searchParamsMock.value = new URLSearchParams();
  currentUserRole.value = "member";
  vi.restoreAllMocks();
});

describe("PullRequestPreviewPage", () => {
  it("auto-launches a ready preview when the PR route has a launch session", async () => {
    searchParamsMock.value = new URLSearchParams("launch=1");
    let requestedIntent: string | null = null;
    let bootstrapCalls = 0;
    const openSpy = vi.spyOn(window, "open").mockReturnValue(null);

    server.use(
      http.get("*/api/v1/previews/github/acme/web/pull/42", ({ request }) => {
        requestedIntent = new URL(request.url).searchParams.get("intent");
        return HttpResponse.json({
          data: {
            target_id: "target-1",
            preview_id: "prev-1",
            repository_id: "repo-1",
            repository_full_name: "acme/web",
            branch: "feature/preview",
            commit_sha: "529975ce1faa2961ef3f23abde2418bf561116d9",
            source_type: "pull_request",
            status: "ready",
            current_phase: "ready",
            stable_url: "https://143.dev/previews/github/acme/web/pull/42",
            preview_url: "https://prev-1.preview.143.dev",
            pull_request_url: "https://github.com/acme/web/pull/42",
            launch: {
              action: "open",
              auto_open: true,
              represents_latest: true,
              primary_label: "Open preview",
            },
          },
        });
      }),
      http.post("*/api/v1/previews/prev-1/bootstrap", () => {
        bootstrapCalls += 1;
        return HttpResponse.json({
          data: {
            token: "bootstrap-token",
            preview_id: "prev-1",
          },
        });
      }),
    );

    renderWithProviders(<PullRequestPreviewContent owner="acme" repo="web" number="42" />);

    expect(await screen.findByTitle("Preview bootstrap")).toHaveAttribute(
      "src",
      "https://prev-1.preview.143.dev/bootstrap",
    );
    expect(requestedIntent).toBe("open");
    expect(openSpy).not.toHaveBeenCalled();

    act(() => {
      window.dispatchEvent(
        new MessageEvent("message", {
          origin: "https://prev-1.preview.143.dev",
          data: { type: PREVIEW_BOOTSTRAP_READY_EVENT },
        }),
      );
    });

    await waitFor(() => {
      expect(bootstrapCalls).toBe(1);
    });

    act(() => {
      window.dispatchEvent(
        new MessageEvent("message", {
          origin: "https://prev-1.preview.143.dev",
          data: { type: PREVIEW_BOOTSTRAP_COMPLETE_EVENT },
        }),
      );
    });

    await waitFor(() => {
      expect(openSpy).toHaveBeenCalledWith("https://prev-1.preview.143.dev", "_self");
    });
  });

  it("keeps launch routes read-only for viewers", async () => {
    searchParamsMock.value = new URLSearchParams("launch=1");
    currentUserRole.value = "viewer";
    const user = userEvent.setup();
    let requestedIntent: string | null = null;
    let bootstrapCalls = 0;
    const openSpy = vi.spyOn(window, "open").mockReturnValue(null);

    server.use(
      http.get("*/api/v1/previews/github/acme/web/pull/42", ({ request }) => {
        requestedIntent = new URL(request.url).searchParams.get("intent");
        return HttpResponse.json({
          data: {
            target_id: "target-1",
            preview_id: "prev-1",
            repository_id: "repo-1",
            repository_full_name: "acme/web",
            branch: "feature/preview",
            commit_sha: "529975ce1faa2961ef3f23abde2418bf561116d9",
            source_type: "pull_request",
            status: "ready",
            current_phase: "ready",
            stable_url: "https://143.dev/previews/github/acme/web/pull/42",
            preview_url: "https://prev-1.preview.143.dev",
            pull_request_url: "https://github.com/acme/web/pull/42",
            launch: {
              action: "open",
              auto_open: true,
              represents_latest: true,
              primary_label: "Open preview",
            },
          },
        });
      }),
      http.post("*/api/v1/previews/prev-1/bootstrap", () => {
        bootstrapCalls += 1;
        return HttpResponse.json({ data: { token: "bootstrap-token", preview_id: "prev-1" } });
      }),
    );

    renderWithProviders(<PullRequestPreviewContent owner="acme" repo="web" number="42" />);

    expect(await screen.findByText("Preview is ready")).toBeInTheDocument();
    expect(requestedIntent).toBe("status");
    expect(openSpy).not.toHaveBeenCalled();
    expect(bootstrapCalls).toBe(0);
    expect(screen.queryByTitle("Preview bootstrap")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Open preview" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Start preview" })).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Preview actions" }));

    expect(await screen.findByText("Refresh status")).toBeInTheDocument();
    expect(screen.queryByText("Start latest preview")).not.toBeInTheDocument();
    expect(screen.queryByText("Restart runtime")).not.toBeInTheDocument();
    expect(screen.queryByText("Stop runtime")).not.toBeInTheDocument();
  });

  it("starts latest and opens the new preview when launch session lands on a stale PR", async () => {
    searchParamsMock.value = new URLSearchParams("launch=1");
    let startLatestCalled = false;
    let bootstrapPreviewID = "";
    const openSpy = vi.spyOn(window, "open").mockReturnValue(null);

    server.use(
      http.get("*/api/v1/previews/github/acme/web/pull/42", () =>
        HttpResponse.json({
          data: {
            target_id: "target-old",
            preview_id: "prev-old",
            repository_id: "repo-1",
            repository_full_name: "acme/web",
            branch: "feature/preview",
            commit_sha: "abc123",
            latest_commit_sha: "def456",
            new_commits_available: true,
            source_type: "pull_request",
            status: "ready",
            current_phase: "ready",
            stable_url: "https://143.dev/previews/github/acme/web/pull/42",
            preview_url: "https://prev-old.preview.143.dev",
            pull_request_url: "https://github.com/acme/web/pull/42",
            launch: {
              action: "start_latest",
              reason: "stale",
              auto_open: true,
              represents_latest: false,
              primary_label: "Start latest preview",
              secondary_label: "Open stale preview",
              stale_preview_url: "https://prev-old.preview.143.dev",
            },
          },
        }),
      ),
      http.post("*/api/v1/previews/target-old/start-latest", () => {
        startLatestCalled = true;
        return HttpResponse.json({
          data: {
            target_id: "target-new",
            preview_id: "prev-new",
            repository_id: "repo-1",
            repository_full_name: "acme/web",
            branch: "feature/preview",
            commit_sha: "def456",
            latest_commit_sha: "def456",
            source_type: "pull_request",
            status: "ready",
            current_phase: "ready",
            stable_url: "https://143.dev/previews/github/acme/web/pull/42",
            preview_url: "https://prev-new.preview.143.dev",
            pull_request_url: "https://github.com/acme/web/pull/42",
            launch: {
              action: "open",
              auto_open: true,
              represents_latest: true,
              primary_label: "Open preview",
            },
          },
        });
      }),
      http.post("*/api/v1/previews/prev-new/bootstrap", () => {
        bootstrapPreviewID = "prev-new";
        return HttpResponse.json({
          data: {
            token: "bootstrap-token",
            preview_id: "prev-new",
          },
        });
      }),
    );

    renderWithProviders(<PullRequestPreviewContent owner="acme" repo="web" number="42" />);

    await waitFor(() => {
      expect(startLatestCalled).toBe(true);
    });
    expect(await screen.findByTitle("Preview bootstrap")).toHaveAttribute(
      "src",
      "https://prev-new.preview.143.dev/bootstrap",
    );

    act(() => {
      window.dispatchEvent(
        new MessageEvent("message", {
          origin: "https://prev-new.preview.143.dev",
          data: { type: PREVIEW_BOOTSTRAP_READY_EVENT },
        }),
      );
    });

    await waitFor(() => {
      expect(bootstrapPreviewID).toBe("prev-new");
    });

    act(() => {
      window.dispatchEvent(
        new MessageEvent("message", {
          origin: "https://prev-new.preview.143.dev",
          data: { type: PREVIEW_BOOTSTRAP_COMPLETE_EVENT },
        }),
      );
    });

    await waitFor(() => {
      expect(openSpy).toHaveBeenCalledWith("https://prev-new.preview.143.dev", "_self");
    });
  });

  it("bootstraps preview access before opening the preview origin", async () => {
    let bootstrapCalls = 0;
    const popupDocument = {
      close: vi.fn(),
      write: vi.fn(),
    } as unknown as Document;
    const openedWindow = {
      addEventListener: vi.fn(),
      close: vi.fn(),
      removeEventListener: vi.fn(),
      closed: false,
      document: popupDocument,
      location: {
        href: "about:blank",
      },
      opener: null,
    } as unknown as Window;
    const openSpy = vi.spyOn(window, "open").mockReturnValue(openedWindow);

    server.use(
      http.get("*/api/v1/previews/github/acme/web/pull/42", () =>
        HttpResponse.json({
          data: {
            target_id: "target-1",
            preview_id: "prev-1",
            repository_id: "repo-1",
            repository_full_name: "acme/web",
            branch: "feature/preview",
            commit_sha: "529975ce1faa2961ef3f23abde2418bf561116d9",
            source_type: "pull_request",
            status: "ready",
            current_phase: "ready",
            stable_url: "https://143.dev/previews/github/acme/web/pull/42",
            preview_url: "https://prev-1.preview.143.dev",
            pull_request_url: "https://github.com/acme/web/pull/42",
          },
        }),
      ),
      http.post("*/api/v1/previews/prev-1/bootstrap", () => {
        bootstrapCalls += 1;
        return HttpResponse.json({
          data: {
            token: "bootstrap-token",
            preview_id: "prev-1",
          },
        });
      }),
    );

    renderWithProviders(<PullRequestPreviewContent owner="acme" repo="web" number="42" />);

    await userEvent.click(await screen.findByRole("button", { name: /open preview/i }));

    expect(openSpy).toHaveBeenCalledWith("about:blank", "_blank");
    expect(screen.getByTitle("Preview bootstrap")).toHaveAttribute(
      "src",
      "https://prev-1.preview.143.dev/bootstrap",
    );
    expect(openedWindow.location.href).toBe("about:blank");

    act(() => {
      window.dispatchEvent(
        new MessageEvent("message", {
          origin: "https://prev-1.preview.143.dev",
          data: { type: PREVIEW_BOOTSTRAP_READY_EVENT },
        }),
      );
    });

    await waitFor(() => {
      expect(bootstrapCalls).toBe(1);
    });
    expect(openedWindow.location.href).toBe("about:blank");

    act(() => {
      window.dispatchEvent(
        new MessageEvent("message", {
          origin: "https://prev-1.preview.143.dev",
          data: { type: PREVIEW_BOOTSTRAP_COMPLETE_EVENT },
        }),
      );
    });

    await waitFor(() => {
      expect(openedWindow.location.href).toBe("https://prev-1.preview.143.dev");
    });
  });

  it("capitalizes preview status badges and phase labels", async () => {
    server.use(
      http.get("*/api/v1/previews/github/acme/web/pull/42", () =>
        HttpResponse.json({
          data: {
            target_id: "target-1",
            preview_id: "prev-1",
            repository_id: "repo-1",
            repository_full_name: "acme/web",
            branch: "feature/preview",
            commit_sha: "529975ce1faa2961ef3f23abde2418bf561116d9",
            source_type: "pull_request",
            status: "partially_ready",
            current_phase: "start_services",
            stable_url: "https://143.dev/previews/github/acme/web/pull/42",
            preview_url: "https://prev-1.preview.143.dev",
            services: [
              {
                id: "svc-1",
                preview_instance_id: "prev-1",
                service_name: "web",
                role: "primary",
                status: "starting",
                command: ["npm", "run", "dev"],
                cwd: ".",
                port: 3000,
                created_at: "2026-06-10T12:00:00Z",
              },
            ],
            infrastructure: [
              {
                id: "infra-1",
                preview_instance_id: "prev-1",
                infra_name: "postgres",
                template: "postgres",
                container_id: "container-1",
                status: "unhealthy",
                host: "postgres",
                port: 5432,
                created_at: "2026-06-10T12:00:00Z",
              },
            ],
          },
        }),
      ),
    );

    renderWithProviders(<PullRequestPreviewContent owner="acme" repo="web" number="42" />);

    expect((await screen.findAllByText("Partially Ready")).length).toBeGreaterThan(0);
    expect(screen.getByText("Start Services")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Show details" }));
    expect(screen.getAllByText("Starting").length).toBeGreaterThan(0);
    expect(screen.getByText("Unhealthy")).toBeInTheDocument();
  });

  it("shows spinners inside starting preview status badges", async () => {
    server.use(
      http.get("*/api/v1/previews/github/acme/web/pull/42", () =>
        HttpResponse.json({
          data: {
            target_id: "target-1",
            preview_id: "prev-1",
            repository_id: "repo-1",
            repository_full_name: "acme/web",
            branch: "feature/preview",
            commit_sha: "529975ce1faa2961ef3f23abde2418bf561116d9",
            source_type: "pull_request",
            status: "starting",
            current_phase: "start_services",
            stable_url: "https://143.dev/previews/github/acme/web/pull/42",
            preview_url: "https://prev-1.preview.143.dev",
            services: [
              {
                id: "svc-1",
                preview_instance_id: "prev-1",
                service_name: "web",
                role: "primary",
                status: "starting",
                command: ["npm", "run", "dev"],
                cwd: ".",
                port: 3000,
                created_at: "2026-06-10T12:00:00Z",
              },
            ],
          },
        }),
      ),
    );

    renderWithProviders(<PullRequestPreviewContent owner="acme" repo="web" number="42" />);

    const startingLabels = await screen.findAllByText("Starting");
    const startingBadges = startingLabels.filter((label) => label.closest('[data-slot="status-label"]'));
    expect(startingBadges.length).toBeGreaterThan(0);
    for (const label of startingBadges) {
      expect(label.closest('[data-slot="status-label"]')?.querySelector('[data-slot="status-spinner"]')).toBeInTheDocument();
    }
  });

  it("does not auto-open stale previews and makes Start latest preview primary", async () => {
    let startLatestCalled = false;
    server.use(
      http.get("*/api/v1/previews/github/acme/web/pull/42", () =>
        HttpResponse.json({
          data: {
            target_id: "target-old",
            preview_id: "prev-old",
            repository_id: "repo-1",
            repository_full_name: "acme/web",
            branch: "feature/preview",
            commit_sha: "abc123",
            latest_commit_sha: "def456",
            new_commits_available: true,
            source_type: "pull_request",
            status: "ready",
            current_phase: "ready",
            stable_url: "https://143.dev/previews/github/acme/web/pull/42",
            preview_url: "https://prev-old.preview.143.dev",
            pull_request_url: "https://github.com/acme/web/pull/42",
            launch: {
              action: "start_latest",
              reason: "stale",
              auto_open: false,
              represents_latest: false,
              primary_label: "Start latest preview",
              secondary_label: "Open stale preview",
              stale_preview_url: "https://prev-old.preview.143.dev",
              message: "This preview is for abc123; the pull request is now at def456.",
            },
          },
        }),
      ),
      http.post("*/api/v1/previews/target-old/start-latest", () => {
        startLatestCalled = true;
        return HttpResponse.json({
          data: {
            target_id: "target-new",
            preview_id: "prev-new",
            status: "starting",
            stable_url: "https://143.dev/previews/github/acme/web/pull/42",
            launch: {
              action: "wait",
              reason: "starting",
              auto_open: true,
              represents_latest: true,
            },
          },
        });
      }),
    );

    renderWithProviders(<PullRequestPreviewContent owner="acme" repo="web" number="42" />);

    expect(await screen.findByText("New commits available")).toBeInTheDocument();
    expect(screen.getByText("This preview is for abc123; the pull request is now at def456.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Open stale preview/ })).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: /Start latest preview/ }));

    await waitFor(() => {
      expect(startLatestCalled).toBe(true);
    });
  });

  it("starts a PR preview without an existing target by resolving with open intent", async () => {
    const requestedIntents: Array<string | null> = [];
    let zeroStartCalled = false;
    server.use(
      http.get("*/api/v1/previews/github/acme/web/pull/42", ({ request }) => {
        const intent = new URL(request.url).searchParams.get("intent");
        requestedIntents.push(intent);
        if (intent === "open") {
          return HttpResponse.json({
            data: {
              target_id: "target-new",
              preview_id: "prev-new",
              repository_id: "repo-1",
              repository_full_name: "acme/web",
              branch: "feature/preview",
              commit_sha: "def456",
              latest_commit_sha: "def456",
              source_type: "pull_request",
              status: "starting",
              stable_url: "https://143.dev/previews/github/acme/web/pull/42",
              pull_request_url: "https://github.com/acme/web/pull/42",
              launch: {
                action: "wait",
                reason: "starting",
                auto_open: true,
                represents_latest: true,
                primary_label: "Opening when ready",
              },
            },
          });
        }
        return HttpResponse.json({
          data: {
            target_id: "00000000-0000-0000-0000-000000000000",
            repository_id: "repo-1",
            repository_full_name: "acme/web",
            branch: "feature/preview",
            commit_sha: "def456",
            latest_commit_sha: "def456",
            source_type: "pull_request",
            status: "target_created",
            stable_url: "https://143.dev/previews/github/acme/web/pull/42",
            pull_request_url: "https://github.com/acme/web/pull/42",
            launch: {
              action: "start",
              reason: "no_runtime",
              auto_open: false,
              represents_latest: true,
              primary_label: "Start preview",
            },
          },
        });
      }),
      http.post("*/api/v1/previews/00000000-0000-0000-0000-000000000000/start-latest", () => {
        zeroStartCalled = true;
        return HttpResponse.json(
          { error: { code: "PREVIEW_NOT_FOUND", message: "preview not found" } },
          { status: 404 },
        );
      }),
    );

    renderWithProviders(<PullRequestPreviewContent owner="acme" repo="web" number="42" />);

    await userEvent.click(await screen.findByRole("button", { name: /Start preview/ }));

    await waitFor(() => {
      expect(requestedIntents).toContain("open");
    });
    expect(zeroStartCalled).toBe(false);
    expect(await screen.findByText("Starting preview")).toBeInTheDocument();
  });

  it("renders blocked launch guidance without start actions", async () => {
    server.use(
      http.get("*/api/v1/previews/github/acme/web/pull/42", () =>
        HttpResponse.json({
          data: {
            target_id: "00000000-0000-0000-0000-000000000000",
            repository_id: "repo-1",
            repository_full_name: "acme/web",
            branch: "feature/preview",
            commit_sha: "def456",
            latest_commit_sha: "def456",
            source_type: "pull_request",
            status: "target_created",
            stable_url: "https://143.dev/previews/github/acme/web/pull/42",
            pull_request_url: "https://github.com/acme/web/pull/42",
            launch: {
              action: "blocked",
              reason: "role_forbidden",
              auto_open: false,
              represents_latest: true,
              message: "You can open existing previews, but you do not have permission to start a new preview for this pull request.",
            },
          },
        }),
      ),
    );

    renderWithProviders(<PullRequestPreviewContent owner="acme" repo="web" number="42" />);

    expect(await screen.findByText("Preview blocked")).toBeInTheDocument();
    expect(screen.getByText("You can open existing previews, but you do not have permission to start a new preview for this pull request.")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Start latest preview|Start preview|Resume preview/ })).not.toBeInTheDocument();
  });

  it("prefers failed preview diagnostics over blocked launch copy", async () => {
    server.use(
      http.get("*/api/v1/previews/github/acme/web/pull/42", () =>
        HttpResponse.json({
          data: {
            target_id: "target-1",
            preview_id: "prev-1",
            repository_id: "repo-1",
            repository_full_name: "acme/web",
            branch: "feature/preview",
            commit_sha: "def456",
            latest_commit_sha: "def456",
            source_type: "pull_request",
            status: "failed",
            error: "the preview ran out of memory (OOM, exit 137); capped at 4096 MiB.",
            stable_url: "https://143.dev/previews/github/acme/web/pull/42",
            pull_request_url: "https://github.com/acme/web/pull/42",
            launch: {
              action: "blocked",
              reason: "preview_unavailable",
              auto_open: false,
              represents_latest: true,
              message: "restart preview to apply network setting",
            },
          },
        }),
      ),
    );

    renderWithProviders(<PullRequestPreviewContent owner="acme" repo="web" number="42" />);

    expect(await screen.findByText("Preview failed")).toBeInTheDocument();
    expect(screen.getAllByText("the preview ran out of memory (OOM, exit 137); capped at 4096 MiB.").length).toBeGreaterThan(0);
    expect(screen.queryByText("Preview blocked")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Retry preview|Retry/ })).toBeEnabled();
  });

  it("does not show Open preview button while preview is starting", async () => {
    server.use(
      http.get("*/api/v1/previews/github/acme/web/pull/42", () =>
        HttpResponse.json({
          data: {
            target_id: "target-1",
            preview_id: "prev-1",
            repository_id: "repo-1",
            repository_full_name: "acme/web",
            branch: "feature/preview",
            commit_sha: "abc123",
            latest_commit_sha: "abc123",
            source_type: "pull_request",
            status: "starting",
            preview_url: "https://prev-1.preview.143.dev",
            stable_url: "https://143.dev/previews/github/acme/web/pull/42",
            pull_request_url: "https://github.com/acme/web/pull/42",
            launch: {
              action: "wait",
              reason: "starting",
              auto_open: true,
              represents_latest: true,
              primary_label: "Opening when ready",
            },
          },
        }),
      ),
    );

    renderWithProviders(<PullRequestPreviewContent owner="acme" repo="web" number="42" />);

    expect(await screen.findByText("Starting preview")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /open preview|opening when ready/i })).not.toBeInTheDocument();
  });

  it("shows Preview expired copy instead of Preview not started for expired non-resumable previews", async () => {
    server.use(
      http.get("*/api/v1/previews/github/acme/web/pull/42", () =>
        HttpResponse.json({
          data: {
            target_id: "target-1",
            repository_id: "repo-1",
            repository_full_name: "acme/web",
            branch: "feature/preview",
            commit_sha: "abc123",
            latest_commit_sha: "abc123",
            source_type: "pull_request",
            status: "expired",
            stable_url: "https://143.dev/previews/github/acme/web/pull/42",
            pull_request_url: "https://github.com/acme/web/pull/42",
            launch: {
              action: "start",
              reason: "no_runtime",
              auto_open: false,
              represents_latest: true,
              primary_label: "Start preview",
            },
          },
        }),
      ),
    );

    renderWithProviders(<PullRequestPreviewContent owner="acme" repo="web" number="42" />);

    expect(await screen.findByText("Preview expired")).toBeInTheDocument();
    expect(screen.queryByText("Preview not started")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Start preview/ })).toBeInTheDocument();
  });
});
