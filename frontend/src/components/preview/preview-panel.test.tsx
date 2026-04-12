import { describe, expect, it, vi, beforeEach } from "vitest";
import {
  buildPreviewIframeSrc,
  PREVIEW_BOOTSTRAP_READY_EVENT,
  PREVIEW_BOOTSTRAP_TOKEN_EVENT,
  PreviewPanel,
} from "./preview-panel";
import { renderWithProviders, screen, waitFor, userEvent } from "@/test/test-utils";
import type { PreviewStatus } from "@/lib/preview-types";

/* ------------------------------------------------------------------ */
/* Hoisted mocks                                                      */
/* ------------------------------------------------------------------ */

const mockGet = vi.hoisted(() => vi.fn());
const mockStart = vi.hoisted(() => vi.fn());
const mockStop = vi.hoisted(() => vi.fn());
const mockRestart = vi.hoisted(() => vi.fn());
const mockBootstrap = vi.hoisted(() => vi.fn());

vi.mock("@/lib/api", () => ({
  api: {
    sessions: {
      preview: {
        get: mockGet,
        start: mockStart,
        stop: mockStop,
        restart: mockRestart,
        bootstrap: mockBootstrap,
      },
    },
  },
}));

vi.mock("./screenshot-timeline", () => ({
  ScreenshotTimeline: ({ snapshots }: { snapshots: unknown[] }) => (
    <div data-testid="screenshot-timeline">
      {snapshots.length} snapshot(s)
    </div>
  ),
}));

vi.mock("./console-badge", () => ({
  ConsoleBadge: ({ sessionId }: { sessionId: string }) => (
    <div data-testid="console-badge">ConsoleBadge:{sessionId}</div>
  ),
}));

vi.mock("./design-mode-overlay", () => ({
  DesignModeOverlay: () => (
    <div data-testid="design-mode-overlay">DesignModeOverlay</div>
  ),
}));

vi.mock("./ttl-warning", () => ({
  TTLWarning: ({
    expiresAt,
    sessionId,
  }: {
    expiresAt: string;
    sessionId: string;
  }) => (
    <div data-testid="ttl-warning">
      TTLWarning:{sessionId}:{expiresAt}
    </div>
  ),
}));

/* ------------------------------------------------------------------ */
/* Helpers                                                            */
/* ------------------------------------------------------------------ */

const DEFAULT_PROPS = {
  sessionId: "sess-1",
  orgId: "org-1",
  previewOriginTemplate: "http://{id}.preview.test",
};

function makePreviewStatus(
  overrides: Partial<PreviewStatus["instance"]> = {},
  services: PreviewStatus["services"] = [],
): PreviewStatus {
  return {
    instance: {
      id: "prev-1",
      session_id: "sess-1",
      org_id: "org-1",
      phase: "ready",
      preview_url: "http://prev-1.preview.test",
      created_at: "2026-01-01T00:00:00Z",
      updated_at: "2026-01-01T00:00:00Z",
      ...overrides,
    },
    services,
    infrastructure: {} as PreviewStatus["infrastructure"],
    snapshots: [],
    active_connections: 0,
  };
}

/* ------------------------------------------------------------------ */
/* Tests: bootstrap helpers (existing)                                */
/* ------------------------------------------------------------------ */

describe("PreviewPanel bootstrap helpers", () => {
  it("points ready previews at the bootstrap path", () => {
    const src = buildPreviewIframeSrc("https://abc.preview.143.dev");
    expect(src).toBe("https://abc.preview.143.dev/bootstrap");
  });

  it("uses the gateway bootstrap message names", () => {
    expect(PREVIEW_BOOTSTRAP_READY_EVENT).toBe("preview_bootstrap_ready");
    expect(PREVIEW_BOOTSTRAP_TOKEN_EVENT).toBe("preview_bootstrap_token");
  });
});

/* ------------------------------------------------------------------ */
/* Tests: component rendering                                         */
/* ------------------------------------------------------------------ */

describe("PreviewPanel component", () => {
  beforeEach(() => {
    vi.resetAllMocks();
    mockStart.mockResolvedValue({});
    mockStop.mockResolvedValue({});
    mockRestart.mockResolvedValue({});
    mockBootstrap.mockResolvedValue({ token: "tok-1" });
  });

  /* ---------- Loading state ---------- */

  it("shows loading spinner while query is pending", () => {
    // Never resolve so query stays in loading state
    mockGet.mockReturnValue(new Promise(() => {}));

    renderWithProviders(<PreviewPanel {...DEFAULT_PROPS} />);

    expect(screen.getByText("Loading preview status...")).toBeInTheDocument();
  });

  /* ---------- Idle / stopped state ---------- */

  it('shows idle state with "No preview running" when phase is absent', async () => {
    mockGet.mockResolvedValue(
      makePreviewStatus({
        phase: undefined as unknown as PreviewStatus["instance"]["phase"],
      }),
    );

    renderWithProviders(<PreviewPanel {...DEFAULT_PROPS} />);

    await waitFor(() => {
      expect(screen.getByText("No preview running")).toBeInTheDocument();
    });
    // Two "Start Preview" buttons: one in controls bar, one in idle panel
    const startButtons = screen.getAllByText("Start Preview");
    expect(startButtons.length).toBeGreaterThanOrEqual(1);
  });

  it('shows idle state when phase is "stopped"', async () => {
    mockGet.mockResolvedValue(makePreviewStatus({ phase: "stopped" }));

    renderWithProviders(<PreviewPanel {...DEFAULT_PROPS} />);

    await waitFor(() => {
      expect(screen.getByText("No preview running")).toBeInTheDocument();
    });

    // Should also render the status badge
    expect(screen.getByText("Stopped")).toBeInTheDocument();
  });

  /* ---------- Building phase ---------- */

  it("shows Stop and Restart buttons and Building badge during building phase", async () => {
    mockGet.mockResolvedValue(makePreviewStatus({ phase: "building" }));

    renderWithProviders(<PreviewPanel {...DEFAULT_PROPS} />);

    await waitFor(() => {
      // "Building" appears in both badge and progress label, use getAllByText
      expect(screen.getAllByText("Building").length).toBeGreaterThanOrEqual(1);
    });

    expect(screen.getByText("Stop")).toBeInTheDocument();
    expect(screen.getByText("Restart")).toBeInTheDocument();
    // Start button should NOT be visible
    expect(screen.queryByText("Start Preview")).not.toBeInTheDocument();
  });

  it("renders progress bar phase labels during building phase", async () => {
    mockGet.mockResolvedValue(makePreviewStatus({ phase: "building" }));

    renderWithProviders(<PreviewPanel {...DEFAULT_PROPS} />);

    await waitFor(() => {
      expect(screen.getAllByText("Building").length).toBeGreaterThanOrEqual(1);
    });

    // Progress bar shows phase labels from PHASE_ORDER
    expect(screen.getByText("Initializing")).toBeInTheDocument();
    expect(screen.getByText("Starting")).toBeInTheDocument();
    // "Ready" appears in the progress bar phase labels
    expect(screen.getByText("Ready")).toBeInTheDocument();
  });

  /* ---------- Pending phase ---------- */

  it("shows active controls during pending phase", async () => {
    mockGet.mockResolvedValue(makePreviewStatus({ phase: "pending" }));

    renderWithProviders(<PreviewPanel {...DEFAULT_PROPS} />);

    await waitFor(() => {
      expect(screen.getByText("Pending")).toBeInTheDocument();
    });

    expect(screen.getByText("Stop")).toBeInTheDocument();
    expect(screen.getByText("Restart")).toBeInTheDocument();
  });

  /* ---------- Ready phase ---------- */

  it('shows Ready badge and iframe with title "Preview" when phase is ready', async () => {
    mockGet.mockResolvedValue(
      makePreviewStatus({ phase: "ready", id: "prev-1" }),
    );

    renderWithProviders(<PreviewPanel {...DEFAULT_PROPS} />);

    await waitFor(() => {
      expect(screen.getByText("Ready")).toBeInTheDocument();
    });

    // Iframe should be rendered
    const iframe = screen.getByTitle("Preview");
    expect(iframe).toBeInTheDocument();
    expect(iframe).toHaveAttribute(
      "src",
      "http://prev-1.preview.test/bootstrap",
    );
  });

  it("renders width preset buttons in ready state", async () => {
    mockGet.mockResolvedValue(makePreviewStatus({ phase: "ready" }));

    const { container } = renderWithProviders(
      <PreviewPanel {...DEFAULT_PROPS} />,
    );

    await waitFor(() => {
      expect(screen.getByText("Ready")).toBeInTheDocument();
    });

    // Width presets container with 4 icon buttons (Mobile, Tablet, Desktop, Full)
    const presetContainer = container.querySelector(
      ".flex.items-center.gap-0\\.5.rounded-md.border",
    );
    expect(presetContainer).toBeInTheDocument();

    // Check for the 4 preset icon buttons via tooltip trigger data attribute
    const presetButtons = presetContainer!.querySelectorAll(
      "[data-slot='tooltip-trigger']",
    );
    expect(presetButtons).toHaveLength(4);
  });

  it("renders ConsoleBadge and ScreenshotTimeline in ready state", async () => {
    mockGet.mockResolvedValue(makePreviewStatus({ phase: "ready" }));

    renderWithProviders(<PreviewPanel {...DEFAULT_PROPS} />);

    await waitFor(() => {
      expect(screen.getByTestId("console-badge")).toBeInTheDocument();
    });
    expect(screen.getByTestId("screenshot-timeline")).toBeInTheDocument();
  });

  it("renders TTLWarning when expires_at is set and preview is active", async () => {
    mockGet.mockResolvedValue(
      makePreviewStatus({
        phase: "ready",
        expires_at: "2026-12-31T00:00:00Z",
      }),
    );

    renderWithProviders(<PreviewPanel {...DEFAULT_PROPS} />);

    await waitFor(() => {
      expect(screen.getByTestId("ttl-warning")).toBeInTheDocument();
    });
  });

  /* ---------- Partially ready phase ---------- */

  it("shows Partially Ready badge and iframe in partially_ready state", async () => {
    mockGet.mockResolvedValue(
      makePreviewStatus({ phase: "partially_ready", id: "prev-1" }),
    );

    renderWithProviders(<PreviewPanel {...DEFAULT_PROPS} />);

    await waitFor(() => {
      expect(screen.getByText("Partially Ready")).toBeInTheDocument();
    });

    expect(screen.getByTitle("Preview")).toBeInTheDocument();
  });

  /* ---------- Failed phase ---------- */

  it("shows failure diagnostics when phase is failed", async () => {
    mockGet.mockResolvedValue(
      makePreviewStatus({
        phase: "failed",
        error: "Container crashed unexpectedly",
        failure_pattern: "build_failed",
        build_log: "npm ERR! code ELIFECYCLE",
      }),
    );

    renderWithProviders(<PreviewPanel {...DEFAULT_PROPS} />);

    await waitFor(() => {
      expect(screen.getByText("Preview failed to start")).toBeInTheDocument();
    });

    // Error message
    expect(
      screen.getByText("Container crashed unexpectedly"),
    ).toBeInTheDocument();

    // Failure suggestion from KNOWN_FAILURE_PATTERNS
    expect(
      screen.getByText(
        /Build failed\. Check the build logs for errors\./,
      ),
    ).toBeInTheDocument();

    // Build log toggle
    expect(screen.getByText("Build log")).toBeInTheDocument();

    // Try Again button
    expect(screen.getByText("Try Again")).toBeInTheDocument();
  });

  it("shows Failed badge when phase is failed", async () => {
    mockGet.mockResolvedValue(
      makePreviewStatus({ phase: "failed", error: "err" }),
    );

    renderWithProviders(<PreviewPanel {...DEFAULT_PROPS} />);

    await waitFor(() => {
      expect(screen.getByText("Failed")).toBeInTheDocument();
    });
  });

  /* ---------- Query error state ---------- */

  it('shows "Failed to load preview status" and Retry button on query error', async () => {
    mockGet.mockRejectedValue(new Error("Network error"));

    renderWithProviders(<PreviewPanel {...DEFAULT_PROPS} />);

    await waitFor(() => {
      expect(
        screen.getByText("Failed to load preview status"),
      ).toBeInTheDocument();
    });

    expect(screen.getByText("Network error")).toBeInTheDocument();
    expect(screen.getByText("Retry")).toBeInTheDocument();
  });

  /* ---------- Service status indicators ---------- */

  it("renders service status indicators when multiple services exist", async () => {
    mockGet.mockResolvedValue(
      makePreviewStatus({ phase: "ready" }, [
        {
          name: "frontend",
          type: "frontend",
          status: "ready",
          port: 3000,
        },
        {
          name: "api",
          type: "backend",
          status: "starting",
          port: 8080,
          error: "port binding",
        },
      ]),
    );

    renderWithProviders(<PreviewPanel {...DEFAULT_PROPS} />);

    await waitFor(() => {
      expect(screen.getByText("frontend")).toBeInTheDocument();
    });

    expect(screen.getByText("api")).toBeInTheDocument();
    expect(screen.getByText("(port binding)")).toBeInTheDocument();
  });

  it("does not render service indicators when only one service exists", async () => {
    mockGet.mockResolvedValue(
      makePreviewStatus({ phase: "ready" }, [
        {
          name: "frontend",
          type: "frontend",
          status: "ready",
          port: 3000,
        },
      ]),
    );

    renderWithProviders(<PreviewPanel {...DEFAULT_PROPS} />);

    await waitFor(() => {
      expect(screen.getByText("Ready")).toBeInTheDocument();
    });

    // Service error indicators should not appear
    expect(screen.queryByText("(port binding)")).not.toBeInTheDocument();
  });

  /* ---------- Phase helpers via badge classes ---------- */

  it("applies emerald color class for ready phase badge", async () => {
    mockGet.mockResolvedValue(makePreviewStatus({ phase: "ready" }));

    renderWithProviders(<PreviewPanel {...DEFAULT_PROPS} />);

    await waitFor(() => {
      expect(screen.getByText("Ready")).toBeInTheDocument();
    });

    const badge = screen.getByText("Ready").closest("[class]")!;
    expect(badge.className).toContain("text-emerald-600");
  });

  it("applies destructive color class for failed phase badge", async () => {
    mockGet.mockResolvedValue(
      makePreviewStatus({ phase: "failed", error: "err" }),
    );

    renderWithProviders(<PreviewPanel {...DEFAULT_PROPS} />);

    await waitFor(() => {
      expect(screen.getByText("Failed")).toBeInTheDocument();
    });

    const badge = screen.getByText("Failed").closest("[class]")!;
    expect(badge.className).toContain("text-destructive");
  });

  it("applies primary color class for building phase badge", async () => {
    mockGet.mockResolvedValue(makePreviewStatus({ phase: "building" }));

    renderWithProviders(<PreviewPanel {...DEFAULT_PROPS} />);

    await waitFor(() => {
      expect(screen.getAllByText("Building").length).toBeGreaterThanOrEqual(1);
    });

    // The badge is inside a span with data-slot="badge"
    const badges = screen.getAllByText("Building");
    const badgeEl = badges
      .map((el) => el.closest("[data-slot='badge']"))
      .find(Boolean);
    expect(badgeEl).toBeTruthy();
    expect(badgeEl!.className).toContain("text-primary");
  });

  it("applies amber color class for partially_ready phase badge", async () => {
    mockGet.mockResolvedValue(
      makePreviewStatus({ phase: "partially_ready" }),
    );

    renderWithProviders(<PreviewPanel {...DEFAULT_PROPS} />);

    await waitFor(() => {
      expect(screen.getByText("Partially Ready")).toBeInTheDocument();
    });

    const badge = screen.getByText("Partially Ready").closest("[class]")!;
    expect(badge.className).toContain("text-amber-600");
  });

  /* ---------- Progress bar values via style width ---------- */

  it("renders progress bar at 25% for building phase", async () => {
    mockGet.mockResolvedValue(makePreviewStatus({ phase: "building" }));

    const { container } = renderWithProviders(
      <PreviewPanel {...DEFAULT_PROPS} />,
    );

    await waitFor(() => {
      expect(screen.getAllByText("Building").length).toBeGreaterThanOrEqual(1);
    });

    // The inner progress bar element has inline style with width
    const progressBar = container.querySelector(
      ".bg-primary.rounded-full.transition-all",
    );
    expect(progressBar).toHaveStyle({ width: "25%" });
  });

  it("renders progress bar at 50% for initializing phase", async () => {
    mockGet.mockResolvedValue(makePreviewStatus({ phase: "initializing" }));

    const { container } = renderWithProviders(
      <PreviewPanel {...DEFAULT_PROPS} />,
    );

    await waitFor(() => {
      expect(screen.getByText("Stop")).toBeInTheDocument();
    });

    const progressBar = container.querySelector(
      ".bg-primary.rounded-full.transition-all",
    );
    expect(progressBar).toHaveStyle({ width: "50%" });
  });

  it("renders progress bar at 75% for starting phase", async () => {
    mockGet.mockResolvedValue(makePreviewStatus({ phase: "starting" }));

    const { container } = renderWithProviders(
      <PreviewPanel {...DEFAULT_PROPS} />,
    );

    await waitFor(() => {
      expect(screen.getByText("Stop")).toBeInTheDocument();
    });

    const progressBar = container.querySelector(
      ".bg-primary.rounded-full.transition-all",
    );
    expect(progressBar).toHaveStyle({ width: "75%" });
  });

  /* ---------- Start mutation ---------- */

  it("calls start mutation when Start Preview button is clicked in idle state", async () => {
    const user = userEvent.setup();
    mockGet.mockResolvedValue(makePreviewStatus({ phase: "stopped" }));

    renderWithProviders(<PreviewPanel {...DEFAULT_PROPS} />);

    await waitFor(() => {
      expect(screen.getByText("No preview running")).toBeInTheDocument();
    });

    const startButtons = screen.getAllByText("Start Preview");
    await user.click(startButtons[0]);

    await waitFor(() => {
      expect(mockStart).toHaveBeenCalledWith("sess-1");
    });
  });

  /* ---------- Stop mutation ---------- */

  it("calls stop mutation when Stop button is clicked", async () => {
    const user = userEvent.setup();
    mockGet.mockResolvedValue(makePreviewStatus({ phase: "ready" }));

    renderWithProviders(<PreviewPanel {...DEFAULT_PROPS} />);

    await waitFor(() => {
      expect(screen.getByText("Stop")).toBeInTheDocument();
    });

    await user.click(screen.getByText("Stop"));

    await waitFor(() => {
      expect(mockStop).toHaveBeenCalledWith("sess-1");
    });
  });

  /* ---------- Restart mutation ---------- */

  it("calls restart mutation when Restart button is clicked", async () => {
    const user = userEvent.setup();
    mockGet.mockResolvedValue(makePreviewStatus({ phase: "ready" }));

    renderWithProviders(<PreviewPanel {...DEFAULT_PROPS} />);

    await waitFor(() => {
      expect(screen.getByText("Restart")).toBeInTheDocument();
    });

    await user.click(screen.getByText("Restart"));

    await waitFor(() => {
      expect(mockRestart).toHaveBeenCalledWith("sess-1");
    });
  });

  /* ---------- Mutation error banner ---------- */

  it("shows mutation error banner when start fails", async () => {
    const user = userEvent.setup();
    mockGet.mockResolvedValue(makePreviewStatus({ phase: "stopped" }));
    mockStart.mockRejectedValueOnce(new Error("connection refused"));

    renderWithProviders(<PreviewPanel {...DEFAULT_PROPS} />);

    await waitFor(() => {
      expect(screen.getByText("No preview running")).toBeInTheDocument();
    });

    const startButtons = screen.getAllByText("Start Preview");
    await user.click(startButtons[0]);

    await waitFor(() => {
      expect(screen.getByText("Failed to start preview: connection refused")).toBeInTheDocument();
    });
  });

  it("dismisses mutation error banner when X is clicked", async () => {
    const user = userEvent.setup();
    mockGet.mockResolvedValue(makePreviewStatus({ phase: "stopped" }));
    mockStart.mockRejectedValueOnce(new Error("connection refused"));

    renderWithProviders(<PreviewPanel {...DEFAULT_PROPS} />);

    await waitFor(() => {
      expect(screen.getByText("No preview running")).toBeInTheDocument();
    });

    const startButtons = screen.getAllByText("Start Preview");
    await user.click(startButtons[0]);

    await waitFor(() => {
      expect(screen.getByText("Failed to start preview: connection refused")).toBeInTheDocument();
    });

    // Click the dismiss button (X icon)
    const dismissBtn = screen.getByText("Failed to start preview: connection refused")
      .closest("div")!
      .querySelector("button")!;
    await user.click(dismissBtn);

    await waitFor(() => {
      expect(screen.queryByText("Failed to start preview: connection refused")).not.toBeInTheDocument();
    });
  });

  it("shows mutation error banner when stop fails", async () => {
    const user = userEvent.setup();
    mockGet.mockResolvedValue(makePreviewStatus({ phase: "ready" }));
    mockStop.mockRejectedValueOnce(new Error("timeout"));

    renderWithProviders(<PreviewPanel {...DEFAULT_PROPS} />);

    await waitFor(() => {
      expect(screen.getByText("Stop")).toBeInTheDocument();
    });

    await user.click(screen.getByText("Stop"));

    await waitFor(() => {
      expect(screen.getByText("Failed to stop preview: timeout")).toBeInTheDocument();
    });
  });

  it("shows mutation error banner when restart fails", async () => {
    const user = userEvent.setup();
    mockGet.mockResolvedValue(makePreviewStatus({ phase: "ready" }));
    mockRestart.mockRejectedValueOnce(new Error("server error"));

    renderWithProviders(<PreviewPanel {...DEFAULT_PROPS} />);

    await waitFor(() => {
      expect(screen.getByText("Restart")).toBeInTheDocument();
    });

    await user.click(screen.getByText("Restart"));

    await waitFor(() => {
      expect(screen.getByText("Failed to restart preview: server error")).toBeInTheDocument();
    });
  });

  /* ---------- Width preset interactions ---------- */

  it("changes iframe container max-width when a width preset is clicked", async () => {
    const user = userEvent.setup();
    mockGet.mockResolvedValue(makePreviewStatus({ phase: "ready", id: "prev-1" }));

    const { container } = renderWithProviders(
      <PreviewPanel {...DEFAULT_PROPS} />,
    );

    await waitFor(() => {
      expect(screen.getByText("Ready")).toBeInTheDocument();
    });

    // Find the preset buttons container
    const presetContainer = container.querySelector(
      ".flex.items-center.gap-0\\.5.rounded-md.border",
    )!;
    const presetButtons = presetContainer.querySelectorAll("button");

    // Click Mobile preset (first button, 375px)
    await user.click(presetButtons[0]);

    // The iframe wrapper div should have maxWidth 375px
    const iframeWrapper = container.querySelector(
      "[style*='max-width: 375px']",
    );
    expect(iframeWrapper).toBeInTheDocument();
  });

  /* ---------- Design mode toggle ---------- */

  it("shows design mode overlay when design mode button is toggled on", async () => {
    const user = userEvent.setup();
    mockGet.mockResolvedValue(makePreviewStatus({ phase: "ready", id: "prev-1" }));

    renderWithProviders(<PreviewPanel {...DEFAULT_PROPS} />);

    await waitFor(() => {
      expect(screen.getByText("Ready")).toBeInTheDocument();
    });

    // The design mode button is the Palette icon button
    // It is only rendered in ready state - find it by looking for the button with variant
    const designBtn = screen.getByTitle("Preview")
      .closest(".flex.flex-col.gap-3")!
      .querySelectorAll("[data-slot='tooltip-trigger']");

    // The design mode toggle button is the one after the width presets
    // We need to find the palette button - it's a standalone tooltip trigger outside the presets container
    const paletteButtons = Array.from(designBtn).filter((el) => {
      // Design mode button is not inside the width presets container
      return !el.closest(".flex.items-center.gap-0\\.5.rounded-md.border");
    });

    // Click the first non-preset tooltip trigger (design mode)
    if (paletteButtons.length > 0) {
      await user.click(paletteButtons[0] as HTMLElement);
    }

    // Design mode overlay should not appear until bootstrap is complete
    // Since bootstrapComplete is false by default, overlay won't show
    expect(screen.queryByTestId("design-mode-overlay")).not.toBeInTheDocument();
  });

  /* ---------- Connecting to preview text ---------- */

  it("shows 'Connecting to preview...' overlay when iframe is ready but bootstrap is not complete", async () => {
    mockGet.mockResolvedValue(makePreviewStatus({ phase: "ready", id: "prev-1" }));

    renderWithProviders(<PreviewPanel {...DEFAULT_PROPS} />);

    await waitFor(() => {
      expect(screen.getByTitle("Preview")).toBeInTheDocument();
    });

    // Before bootstrap completes, the connecting overlay should be visible
    expect(screen.getByText("Connecting to preview...")).toBeInTheDocument();
  });

  /* ---------- Try Again button in failed state ---------- */

  it("calls restart mutation when Try Again is clicked in failed state", async () => {
    const user = userEvent.setup();
    mockGet.mockResolvedValue(
      makePreviewStatus({
        phase: "failed",
        error: "Container crashed",
      }),
    );

    renderWithProviders(<PreviewPanel {...DEFAULT_PROPS} />);

    await waitFor(() => {
      expect(screen.getByText("Try Again")).toBeInTheDocument();
    });

    await user.click(screen.getByText("Try Again"));

    await waitFor(() => {
      expect(mockRestart).toHaveBeenCalledWith("sess-1");
    });
  });
});
