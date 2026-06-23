import { act } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { http, HttpResponse } from "msw";
import userEvent from "@testing-library/user-event";

import { renderWithProviders, screen, waitFor } from "@/test/test-utils";
import { server } from "@/test/mocks/server";
import {
  PREVIEW_BOOTSTRAP_COMPLETE_EVENT,
  PREVIEW_BOOTSTRAP_READY_EVENT,
  PREVIEW_LAUNCH_COMPLETE_EVENT,
} from "@/lib/preview-bootstrap";
import { PreviewLandingContent } from "./page";

let searchParams = new URLSearchParams("launch=1");

vi.mock("next/navigation", () => ({
  useSearchParams: () => searchParams,
}));

afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
  searchParams = new URLSearchParams("launch=1");
});

function renderLaunchPage(id = "target-1") {
  return renderWithProviders(<PreviewLandingContent id={id} />);
}

describe("PreviewLandingPage launch mode", () => {
  it("keeps launch mode on the canonical preview detail surface", async () => {
    server.use(
      http.get("*/api/v1/previews/target-1", () =>
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
            stable_url: "https://143.dev/previews/target-1",
            preview_url: "https://target-1.preview.143.dev",
            expires_at: "2026-05-26T21:05:00Z",
            phase_steps: [
              { name: "checkout", status: "complete" },
              { name: "install_build", status: "complete" },
              { name: "start_services", status: "active" },
              { name: "readiness", status: "pending" },
            ],
          },
        }),
      ),
    );

    renderLaunchPage();

    expect(await screen.findByRole("heading", { name: "acme/web" })).toBeInTheDocument();
    expect(screen.getByText("feature/preview")).toBeInTheDocument();
    expect(screen.getByText("529975ce1faa")).toBeInTheDocument();
    expect(screen.getByText("Opening when ready")).toBeInTheDocument();
    expect(screen.getByText("This preview will open automatically when it is ready.")).toBeInTheDocument();
    expect(screen.getByText("Start services")).toBeInTheDocument();
  });

  it("waits for bootstrap completion before navigating to the preview origin", async () => {
    const originalLocation = window.location;
    const locationMock = { href: "" };

    let bootstrapCalls = 0;
    server.use(
      http.get("*/api/v1/previews/target-1", () =>
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
            stable_url: "https://143.dev/previews/target-1",
            preview_url: "https://target-1.preview.143.dev",
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

    try {
      renderLaunchPage();

      await screen.findByText("Opening preview");
      expect(screen.getByTitle("Preview bootstrap")).toHaveAttribute(
        "src",
        "https://target-1.preview.143.dev/bootstrap",
      );
      act(() => {
        window.dispatchEvent(
          new MessageEvent("message", {
            origin: "https://target-1.preview.143.dev",
            data: { type: PREVIEW_BOOTSTRAP_READY_EVENT },
          }),
        );
      });

      await waitFor(() => {
        expect(bootstrapCalls).toBe(1);
      });
      Object.defineProperty(window, "location", {
        value: locationMock,
        writable: true,
        configurable: true,
      });

      await new Promise((resolve) => window.setTimeout(resolve, 300));
      expect(locationMock.href).toBe("");

      act(() => {
        window.dispatchEvent(
          new MessageEvent("message", {
            origin: "https://target-1.preview.143.dev",
            data: { type: PREVIEW_BOOTSTRAP_COMPLETE_EVENT },
          }),
        );
      });

      await waitFor(() => {
        expect(locationMock.href).toBe("https://target-1.preview.143.dev");
      });
    } finally {
      Object.defineProperty(window, "location", {
        value: originalLocation,
        writable: true,
        configurable: true,
      });
    }
  });

  it("stops showing opening state when preview bootstrap does not respond", async () => {
    const user = userEvent.setup();
    let restartCalls = 0;
    server.use(
      http.get("*/api/v1/previews/target-1", () =>
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
            stable_url: "https://143.dev/previews/target-1",
            preview_url: "https://target-1.preview.143.dev",
          },
        }),
      ),
      http.post("*/api/v1/previews/prev-1/start-latest", () => {
        restartCalls += 1;
        return HttpResponse.json({
          data: {
            target_id: "target-1",
            preview_id: "prev-2",
            repository_id: "repo-1",
            repository_full_name: "acme/web",
            branch: "feature/preview",
            commit_sha: "529975ce1faa2961ef3f23abde2418bf561116d9",
            source_type: "pull_request",
            status: "starting",
            current_phase: "start_services",
            stable_url: "https://143.dev/previews/target-1",
            preview_url: "https://target-1.preview.143.dev",
          },
        });
      }),
    );

    renderLaunchPage();

    expect(await screen.findByText("Opening preview")).toBeInTheDocument();

    await act(async () => {
      await new Promise((resolve) => window.setTimeout(resolve, 5_100));
    });

    await waitFor(() => {
      expect(screen.getByRole("heading", { name: "Preview could not open" })).toBeInTheDocument();
    });
    expect(
      screen.getByText(
        "Preview connection timed out. The preview gateway did not answer in time. The preview gateway did not answer the browser bootstrap handshake within 5 seconds. The runtime may still be starting, or the preview edge may be temporarily unavailable.",
      ),
    ).toBeInTheDocument();
    const retry = screen.getByRole("button", { name: "Retry preview" });
    expect(retry).toBeEnabled();

    await user.click(retry);

    expect(restartCalls).toBe(1);
  }, 10_000);

  it("surfaces an error instead of an iframe when the preview is unhealthy", async () => {
    server.use(
      http.get("*/api/v1/previews/target-1", () =>
        HttpResponse.json({
          data: {
            target_id: "target-1",
            preview_id: "prev-1",
            repository_id: "repo-1",
            repository_full_name: "acme/web",
            branch: "feature/preview",
            commit_sha: "529975ce1faa2961ef3f23abde2418bf561116d9",
            source_type: "pull_request",
            status: "unhealthy",
            error: 'primary service "frontend" stopped: exited with code 137',
            current_phase: "ready",
            stable_url: "https://143.dev/previews/target-1",
            preview_url: "https://target-1.preview.143.dev",
          },
        }),
      ),
    );

    renderLaunchPage();

    expect(await screen.findByRole("heading", { name: "Preview could not open" })).toBeInTheDocument();
    expect(
      screen.getByText('primary service "frontend" stopped: exited with code 137'),
    ).toBeInTheDocument();
    // No bootstrap handshake against the dead process.
    expect(screen.queryByTitle("Preview bootstrap")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Retry preview" })).toBeEnabled();
  });

  it("notifies the opener and closes in popup mode instead of navigating", async () => {
    searchParams = new URLSearchParams("launch=1&popup=1");

    const originalLocation = window.location;
    const locationMock = { href: "" };
    let bootstrapCalls = 0;
    const openerPostMessage = vi.fn();
    Object.defineProperty(window, "opener", {
      value: { postMessage: openerPostMessage },
      writable: true,
      configurable: true,
    });
    const closeSpy = vi.spyOn(window, "close").mockImplementation(() => {});

    server.use(
      http.get("*/api/v1/previews/target-1", () =>
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
            stable_url: "https://143.dev/previews/target-1",
            preview_url: "https://target-1.preview.143.dev",
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

    try {
      renderLaunchPage();

      await screen.findByText("Opening preview");
      act(() => {
        window.dispatchEvent(
          new MessageEvent("message", {
            origin: "https://target-1.preview.143.dev",
            data: { type: PREVIEW_BOOTSTRAP_READY_EVENT },
          }),
        );
      });

      await waitFor(() => {
        expect(bootstrapCalls).toBe(1);
      });
      Object.defineProperty(window, "location", {
        value: locationMock,
        writable: true,
        configurable: true,
      });

      act(() => {
        window.dispatchEvent(
          new MessageEvent("message", {
            origin: "https://target-1.preview.143.dev",
            data: { type: PREVIEW_BOOTSTRAP_COMPLETE_EVENT },
          }),
        );
      });

      await waitFor(() => {
        expect(openerPostMessage).toHaveBeenCalledWith(
          { type: PREVIEW_LAUNCH_COMPLETE_EVENT, url: "https://target-1.preview.143.dev" },
          "https://target-1.preview.143.dev",
        );
      });
      expect(closeSpy).toHaveBeenCalled();
      expect(locationMock.href).toBe("");
    } finally {
      Object.defineProperty(window, "location", {
        value: originalLocation,
        writable: true,
        configurable: true,
      });
      Object.defineProperty(window, "opener", {
        value: null,
        writable: true,
        configurable: true,
      });
    }
  });

  it("does not repeatedly auto-start after launch start-latest fails", async () => {
    let startCalls = 0;
    server.use(
      http.get("*/api/v1/previews/target-1", () =>
        HttpResponse.json({
          data: {
            target_id: "target-1",
            preview_id: "prev-1",
            repository_id: "repo-1",
            repository_full_name: "acme/web",
            branch: "feature/preview",
            commit_sha: "529975ce1faa2961ef3f23abde2418bf561116d9",
            source_type: "pull_request",
            status: "stopped",
            current_phase: "stopped",
            stable_url: "https://143.dev/previews/target-1",
            preview_url: "https://target-1.preview.143.dev",
            stopped_at: "2026-05-26T20:05:00Z",
          },
        }),
      ),
      http.post("*/api/v1/previews/prev-1/start-latest", () => {
        startCalls += 1;
        return HttpResponse.json(
          { error: { code: "START_FAILED", message: "Preview could not start." } },
          { status: 500 },
        );
      }),
    );

    renderLaunchPage();

    expect(await screen.findByText("Preview could not start.")).toBeInTheDocument();

    await new Promise((resolve) => window.setTimeout(resolve, 50));
    expect(startCalls).toBe(1);
  });

  it("keeps the failure visible when a launched preview fails after start-latest mints a new id", async () => {
    let phase: "stopped" | "failed" = "stopped";
    let prev1StartCalls = 0;
    let prev2StartCalls = 0;

    server.use(
      http.get("*/api/v1/previews/target-1", () => {
        if (phase === "stopped") {
          return HttpResponse.json({
            data: {
              target_id: "target-1",
              preview_id: "prev-1",
              repository_full_name: "acme/web",
              branch: "feature/preview",
              status: "stopped",
              current_phase: "stopped",
              stable_url: "https://143.dev/previews/target-1",
              preview_url: "https://target-1.preview.143.dev",
            },
          });
        }
        // start-latest minted a fresh instance (prev-2) that then fails readiness.
        return HttpResponse.json({
          data: {
            target_id: "target-1",
            preview_id: "prev-2",
            repository_full_name: "acme/web",
            branch: "feature/preview",
            status: "failed",
            error: "Service failed readiness checks.",
            current_phase: "readiness",
            stable_url: "https://143.dev/previews/target-1",
            preview_url: "https://target-1.preview.143.dev",
          },
        });
      }),
      http.post("*/api/v1/previews/prev-1/start-latest", () => {
        prev1StartCalls += 1;
        phase = "failed";
        return HttpResponse.json({
          data: {
            target_id: "target-1",
            preview_id: "prev-2",
            repository_full_name: "acme/web",
            branch: "feature/preview",
            status: "starting",
            current_phase: "start_services",
            stable_url: "https://143.dev/previews/target-1",
            preview_url: "https://target-1.preview.143.dev",
          },
        });
      }),
      http.post("*/api/v1/previews/prev-2/start-latest", () => {
        prev2StartCalls += 1;
        return HttpResponse.json({ data: { target_id: "target-1", preview_id: "prev-2", status: "starting" } });
      }),
    );

    renderLaunchPage();

    expect(await screen.findByText("Service failed readiness checks.")).toBeInTheDocument();

    // The error must stick — not get clobbered by an auto-restart loop that
    // flips the UI back to "Opening when ready" and spins up new instances.
    await new Promise((resolve) => window.setTimeout(resolve, 250));
    expect(screen.getByText("Service failed readiness checks.")).toBeInTheDocument();
    expect(screen.queryByText("Opening when ready")).not.toBeInTheDocument();
    expect(prev1StartCalls).toBe(1);
    expect(prev2StartCalls).toBe(0);
  });
});

describe("PreviewLandingPage detail mode", () => {
  it("prioritizes the open command and keeps lifecycle controls in preview actions", async () => {
    searchParams = new URLSearchParams("");

    server.use(
      http.get("*/api/v1/previews/target-1", () =>
        HttpResponse.json({
          data: {
            target_id: "target-1",
            preview_id: "prev-1",
            repository_id: "repo-1",
            repository_full_name: "acme/web",
            branch: "feature/preview",
            commit_sha: "529975ce1faa2961ef3f23abde2418bf561116d9",
            source_type: "manual",
            status: "ready",
            current_phase: "ready",
            stable_url: "https://143.dev/previews/target-1",
            preview_url: "https://target-1.preview.143.dev",
            expires_at: "2026-05-26T21:05:00Z",
          },
        }),
      ),
    );

    renderLaunchPage();

    expect(await screen.findByRole("heading", { name: "acme/web" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Open preview" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Preview actions" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Bootstrap token" })).not.toBeInTheDocument();
    expect(screen.queryByText("prev-1")).not.toBeInTheDocument();
  });
});

describe("PreviewLandingPage status mode", () => {
  it("shows endpoint-unreachable recovery copy and restart actions", async () => {
    searchParams = new URLSearchParams("");

    server.use(
      http.get("*/api/v1/previews/target-1", () =>
        HttpResponse.json({
          data: {
            target_id: "target-1",
            preview_id: "prev-1",
            repository_id: "repo-1",
            repository_full_name: "acme/web",
            branch: "feature/preview",
            commit_sha: "529975ce1faa2961ef3f23abde2418bf561116d9",
            source_type: "pull_request",
            status: "unavailable",
            unavailable_reason: "endpoint_unreachable",
            current_phase: "unavailable",
            stable_url: "https://143.dev/previews/target-1",
            preview_url: "https://target-1.preview.143.dev",
            stopped_at: "2026-05-26T20:05:00Z",
          },
        }),
      ),
    );

    renderLaunchPage();

    expect(await screen.findByText("Preview connection lost")).toBeInTheDocument();
    expect(
      screen.getByText(
        "The worker that was serving this preview stopped responding. Start the preview again to create a fresh runtime.",
      ),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Start preview" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Preview actions" })).toBeInTheDocument();
  });
});
