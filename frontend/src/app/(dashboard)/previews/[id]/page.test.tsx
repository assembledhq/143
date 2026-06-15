import { act } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { http, HttpResponse } from "msw";

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
    );

    renderLaunchPage();

    expect(await screen.findByText("Opening preview")).toBeInTheDocument();

    await act(async () => {
      await new Promise((resolve) => window.setTimeout(resolve, 5_100));
    });

    await waitFor(() => {
      expect(screen.getByText("Preview could not open")).toBeInTheDocument();
    });
    expect(screen.getByText("Preview bootstrap timed out. Try opening it again.")).toBeInTheDocument();
  }, 10_000);

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
