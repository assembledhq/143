import { act } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { http, HttpResponse } from "msw";

import { renderWithProviders, screen, userEvent, waitFor } from "@/test/test-utils";
import { server } from "@/test/mocks/server";
import {
  PREVIEW_BOOTSTRAP_COMPLETE_EVENT,
  PREVIEW_BOOTSTRAP_READY_EVENT,
} from "@/lib/preview-bootstrap";
import { PullRequestPreviewContent } from "./page";

afterEach(() => {
  vi.restoreAllMocks();
});

describe("PullRequestPreviewPage", () => {
  it("bootstraps preview access before opening the preview origin", async () => {
    let bootstrapCalls = 0;
    const openedWindow = {
      close: vi.fn(),
      document: {
        close: vi.fn(),
        write: vi.fn(),
      },
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

    expect(await screen.findByText("Partially Ready")).toBeInTheDocument();
    expect(screen.getByText("Start Services")).toBeInTheDocument();
    expect(screen.getByText("Starting")).toBeInTheDocument();
    expect(screen.getByText("Unhealthy")).toBeInTheDocument();
  });

  it("does not auto-open stale previews and makes Start latest primary", async () => {
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
              primary_label: "Start latest",
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

    await userEvent.click(screen.getByRole("button", { name: /Start latest/ }));

    await waitFor(() => {
      expect(startLatestCalled).toBe(true);
    });
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
    expect(screen.queryByRole("button", { name: /Start latest|Start preview|Resume preview/ })).not.toBeInTheDocument();
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
