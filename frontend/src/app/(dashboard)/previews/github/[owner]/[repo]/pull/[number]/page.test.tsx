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
});
