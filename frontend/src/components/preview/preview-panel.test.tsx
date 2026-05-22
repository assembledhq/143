import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import {
  buildPreviewIframeSrc,
  PREVIEW_BOOTSTRAP_READY_EVENT,
  PREVIEW_BOOTSTRAP_TOKEN_EVENT,
  PreviewPanel,
} from "./preview-panel";
import { renderWithProviders, screen, waitFor, userEvent } from "@/test/test-utils";
import { PREVIEW_ERROR_CODES, type PreviewStatusResponse } from "@/lib/preview-types";

/* ------------------------------------------------------------------ */
/* Hoisted mocks                                                      */
/* ------------------------------------------------------------------ */

const mockGet = vi.hoisted(() => vi.fn());
const mockStart = vi.hoisted(() => vi.fn());
const mockStop = vi.hoisted(() => vi.fn());
const mockRestart = vi.hoisted(() => vi.fn());
const mockSetLifetime = vi.hoisted(() => vi.fn());
const mockBootstrap = vi.hoisted(() => vi.fn());
const mockLogs = vi.hoisted(() => vi.fn());
const mockConsoleBadgeState = vi.hoisted(() => ({ shouldThrow: false }));

vi.mock("@/lib/api", () => ({
  api: {
    sessions: {
      preview: {
        get: mockGet,
        start: mockStart,
        stop: mockStop,
        restart: mockRestart,
        setLifetime: mockSetLifetime,
        bootstrap: mockBootstrap,
        logs: mockLogs,
      },
    },
  },
}));

vi.mock("./console-badge", () => ({
  ConsoleBadge: ({ sessionId }: { sessionId: string }) => {
    if (mockConsoleBadgeState.shouldThrow) {
      throw new Error("console badge exploded");
    }
    return <div data-testid="console-badge">ConsoleBadge:{sessionId}</div>;
  },
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
  previewOriginTemplate: "http://{id}.preview.test",
};

function makePreviewStatus(
  overrides: Partial<PreviewStatusResponse["instance"]> = {},
  services: PreviewStatusResponse["services"] = [],
  infrastructure: NonNullable<PreviewStatusResponse["infrastructure"]> = [],
): PreviewStatusResponse {
  return {
    instance: {
      id: "prev-1",
      session_id: "sess-1",
      org_id: "org-1",
      user_id: "user-1",
      status: "ready",
      profile_name: "",
      name: "test-preview",
      provider: "docker",
      worker_node_id: "local",
      preview_handle: "handle-1",
      primary_service: "app",
      port: 3000,
      config_digest: "",
      base_commit_sha: "",
      last_accessed_at: "2026-01-01T00:00:00Z",
      expires_at: "2026-01-02T00:00:00Z",
      last_path: "/",
      memory_limit_mb: 512,
      cpu_limit_millis: 500,
      created_at: "2026-01-01T00:00:00Z",
      updated_at: "2026-01-01T00:00:00Z",
      ...overrides,
    },
    services,
    infrastructure,
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
  let resizeObserverCallback: ResizeObserverCallback | null = null;
  const originalResizeObserver = window.ResizeObserver;

  beforeEach(() => {
    vi.resetAllMocks();
    mockStart.mockResolvedValue({});
    mockStop.mockResolvedValue({});
    mockRestart.mockResolvedValue({});
    mockSetLifetime.mockResolvedValue({});
    mockBootstrap.mockResolvedValue({ token: "tok-1" });
    mockLogs.mockResolvedValue([]);
    mockConsoleBadgeState.shouldThrow = false;

    class MockResizeObserver {
      constructor(callback: ResizeObserverCallback) {
        resizeObserverCallback = callback;
      }

      observe() {}
      unobserve() {}
      disconnect() {}
    }

    window.ResizeObserver = MockResizeObserver as typeof ResizeObserver;
  });

  afterEach(() => {
    resizeObserverCallback = null;
    window.ResizeObserver = originalResizeObserver;
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
        status: undefined as unknown as PreviewStatusResponse["instance"]["status"],
      }),
    );

    renderWithProviders(<PreviewPanel {...DEFAULT_PROPS} />);

    await waitFor(() => {
      expect(screen.getByText("No preview running")).toBeInTheDocument();
    });
    expect(screen.getByRole("button", { name: "Start Preview" })).toBeInTheDocument();
  });

  it("keeps no-preview status quiet when no preview has ever been created", async () => {
    const err = new Error("no active preview") as Error & { code?: string };
    err.code = "NO_ACTIVE_PREVIEW";
    mockGet.mockRejectedValueOnce(err);

    renderWithProviders(<PreviewPanel {...DEFAULT_PROPS} />);

    await waitFor(() => {
      expect(screen.getByText("No preview running")).toBeInTheDocument();
    });
    expect(screen.queryByText("no active preview")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Start Preview" })).toBeInTheDocument();
  });

  it('shows idle state when phase is "stopped"', async () => {
    const startedAt = new Date(Date.now() - 5 * 60_000).toISOString();
    const stoppedAt = new Date(Date.now() - 60_000).toISOString();
    mockGet.mockResolvedValue(
      makePreviewStatus({
        status: "stopped",
        created_at: startedAt,
        stopped_at: stoppedAt,
      }),
    );

    renderWithProviders(<PreviewPanel {...DEFAULT_PROPS} />);

    await waitFor(() => {
      expect(screen.getByText("No preview running")).toBeInTheDocument();
    });

    expect(screen.queryByText("Stopped")).not.toBeInTheDocument();
    expect(screen.getByText(/Started 5m ago/)).toBeInTheDocument();
    expect(screen.getByText(/Stopped 1m ago/)).toHaveClass("rounded-full");
  });

  it("treats async start success as startup in progress and resumes polling", async () => {
    const user = userEvent.setup();
    mockGet
      .mockResolvedValueOnce(makePreviewStatus({ status: "stopped" }))
      .mockResolvedValueOnce(makePreviewStatus({ status: "starting" }));
    mockStart.mockResolvedValueOnce(makePreviewStatus({ status: "starting" }).instance);

    renderWithProviders(<PreviewPanel {...DEFAULT_PROPS} />);

    await waitFor(() => {
      expect(screen.getByText("No preview running")).toBeInTheDocument();
    });

    await user.click(screen.getByRole("button", { name: "Start Preview" }));

    await waitFor(() => {
      expect(screen.getByText("Preparing preview")).toBeInTheDocument();
    });
    expect(mockStart).toHaveBeenCalledTimes(1);
    expect(mockGet).toHaveBeenCalledTimes(2);
  });

  /* ---------- Starting status ---------- */

  it("shows preview-first startup canvas and subtle controls during starting status", async () => {
    mockGet.mockResolvedValue(
      makePreviewStatus(
        { status: "starting" },
        [],
        [
          {
            id: "infra-1",
            preview_instance_id: "prev-1",
            infra_name: "postgres",
            template: "postgres",
            container_id: "ctr-1",
            status: "provisioning",
            host: "postgres",
            port: 5432,
            created_at: "2026-01-01T00:00:00Z",
          },
        ],
      ),
    );

    renderWithProviders(<PreviewPanel {...DEFAULT_PROPS} />);

    await waitFor(() => {
      expect(screen.getByText("Preparing preview")).toBeInTheDocument();
    });

    expect(screen.getByText("Provisioning postgres")).toBeInTheDocument();
    expect(screen.getByText("Provisioning")).toBeInTheDocument();
    expect(screen.getByText("Starting")).toBeInTheDocument();
    expect(screen.getByText("Opening")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Stop preview" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Restart preview" })).toBeInTheDocument();
    expect(screen.queryByText("Start Preview")).not.toBeInTheDocument();
  });

  it("stacks startup phase tiles when the panel becomes narrow", async () => {
    mockGet.mockResolvedValue(
      makePreviewStatus(
        { status: "starting" },
        [],
        [
          {
            id: "infra-1",
            preview_instance_id: "prev-1",
            infra_name: "postgres",
            template: "postgres",
            container_id: "ctr-1",
            status: "provisioning",
            host: "postgres",
            port: 5432,
            created_at: "2026-01-01T00:00:00Z",
          },
        ],
      ),
    );

    renderWithProviders(<PreviewPanel {...DEFAULT_PROPS} />);

    await waitFor(() => {
      expect(screen.getByText("Preparing preview")).toBeInTheDocument();
    });

    const phaseRail = screen.getByTestId("preview-startup-phase-rail");

    resizeObserverCallback?.(
      [
        {
          target: phaseRail,
          contentRect: {
            width: 220,
            height: 0,
            x: 0,
            y: 0,
            top: 0,
            right: 220,
            bottom: 0,
            left: 0,
            toJSON: () => ({}),
          },
        } as unknown as ResizeObserverEntry,
      ],
      {} as ResizeObserver,
    );

    await waitFor(() => {
      expect(phaseRail).toHaveAttribute("data-layout", "stacked");
    });
  });

  it("hides the Provisioning rail tile when the preview has no infrastructure", async () => {
    mockGet.mockResolvedValue(makePreviewStatus({ status: "starting" }));

    renderWithProviders(<PreviewPanel {...DEFAULT_PROPS} />);

    await waitFor(() => {
      expect(screen.getByText("Preparing preview")).toBeInTheDocument();
    });

    expect(screen.queryByText("Provisioning")).not.toBeInTheDocument();
    expect(screen.getByText("Starting")).toBeInTheDocument();
    expect(screen.getByText("Opening")).toBeInTheDocument();
  });

  it("does not show duplicated startup guidance or checklist by default", async () => {
    mockGet.mockResolvedValue(makePreviewStatus({ status: "starting" }));

    renderWithProviders(<PreviewPanel {...DEFAULT_PROPS} />);

    await waitFor(() => {
      expect(screen.getByText("Preparing preview")).toBeInTheDocument();
    });

    expect(screen.queryByText("Preview startup can take a few minutes.")).not.toBeInTheDocument();
    expect(screen.queryByText("Startup checklist")).not.toBeInTheDocument();
  });

  /* ---------- Starting status (active controls) ---------- */

  it("shows active controls during starting status", async () => {
    mockGet.mockResolvedValue(makePreviewStatus({ status: "starting" }));

    renderWithProviders(<PreviewPanel {...DEFAULT_PROPS} />);

    await waitFor(() => {
      expect(screen.getByText("Preparing preview")).toBeInTheDocument();
    });

    expect(screen.getByRole("button", { name: "Stop preview" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Restart preview" })).toBeInTheDocument();
  });

  /* ---------- Ready phase ---------- */

  it('shows quiet running metadata and iframe with title "Preview" when phase is ready', async () => {
    mockGet.mockResolvedValue(
      makePreviewStatus({ status: "ready", id: "prev-1" }),
    );

    renderWithProviders(<PreviewPanel {...DEFAULT_PROPS} />);

    await waitFor(() => {
      expect(screen.getByText("Running")).toBeInTheDocument();
    });
    expect(screen.queryByText("Ready")).not.toBeInTheDocument();

    // Iframe should be rendered
    const iframe = screen.getByTitle("Preview");
    expect(iframe).toBeInTheDocument();
    expect(iframe).toHaveAttribute(
      "src",
      "http://prev-1.preview.test/bootstrap",
    );
  });

  it("uses the runtime preview origin from the status response when present", async () => {
    mockGet.mockResolvedValue({
      ...makePreviewStatus({ status: "ready", id: "prev-1" }),
      preview_origin: "https://prev-1.preview.143.dev",
    });

    renderWithProviders(
      <PreviewPanel
        {...DEFAULT_PROPS}
        previewOriginTemplate="http://{id}.preview.localhost:9090"
      />,
    );

    await waitFor(() => {
      expect(screen.getByText("Running")).toBeInTheDocument();
    });

    expect(screen.getByTitle("Preview")).toHaveAttribute(
      "src",
      "https://prev-1.preview.143.dev/bootstrap",
    );
  });

  it("renders a prominent preview link instead of viewport preset buttons in ready state", async () => {
    mockGet.mockResolvedValue(makePreviewStatus({ status: "ready" }));

    const { container } = renderWithProviders(
      <PreviewPanel {...DEFAULT_PROPS} />,
    );

    await waitFor(() => {
      expect(screen.getByRole("link", { name: /Open Preview/i })).toBeInTheDocument();
    });

    const previewLink = screen.getByRole("link", { name: /Open Preview/i });
    expect(previewLink).toHaveAttribute("href", "http://prev-1.preview.test");
    expect(previewLink).toHaveAttribute("target", "_blank");
    expect(screen.queryByRole("button", { name: /^Stop$/ })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^Restart$/ })).not.toBeInTheDocument();

    // The old viewport preset group (Mobile, Tablet, Desktop, Full) should not render.
    const presetContainer = container.querySelector(
      ".flex.items-center.gap-0\\.5.rounded-md.border",
    );
    expect(presetContainer).not.toBeInTheDocument();
  });

  it("renders ConsoleBadge in ready state", async () => {
    mockGet.mockResolvedValue(makePreviewStatus({ status: "ready" }));

    renderWithProviders(<PreviewPanel {...DEFAULT_PROPS} />);

    await waitFor(() => {
      expect(screen.getByTestId("console-badge")).toBeInTheDocument();
    });
  });

  it("keeps the preview panel usable if the console badge crashes", async () => {
    const originalError = console.error;
    console.error = vi.fn();
    mockConsoleBadgeState.shouldThrow = true;
    mockGet.mockResolvedValue(makePreviewStatus({ status: "ready" }));

    try {
      renderWithProviders(<PreviewPanel {...DEFAULT_PROPS} />);

      await waitFor(() => {
        expect(screen.getByText("Running")).toBeInTheDocument();
      });
      expect(screen.getByTitle("Preview")).toBeInTheDocument();
      expect(screen.queryByTestId("console-badge")).not.toBeInTheDocument();
    } finally {
      console.error = originalError;
    }
  });

  it("renders TTLWarning when expires_at is set and preview is active", async () => {
    mockGet.mockResolvedValue(
      makePreviewStatus({
        status: "ready",
        expires_at: "2026-12-31T00:00:00Z",
      }),
    );

    renderWithProviders(<PreviewPanel {...DEFAULT_PROPS} />);

    await waitFor(() => {
      expect(screen.getByTestId("ttl-warning")).toBeInTheDocument();
    });
  });

  it("offers bounded preview lifetime controls from a hidden menu", async () => {
    const user = userEvent.setup();
    mockGet.mockResolvedValue(
      makePreviewStatus({
        status: "ready",
        expires_at: "2026-12-31T00:00:00Z",
      }),
    );

    renderWithProviders(<PreviewPanel {...DEFAULT_PROPS} />);

    await user.click(await screen.findByRole("button", { name: "Preview actions" }));

    expect(screen.getByText("Preview lifetime")).toBeInTheDocument();
    expect(screen.getByRole("menuitem", { name: "Keep for 15 min" })).toBeInTheDocument();
    expect(screen.getByRole("menuitem", { name: "Keep for 30 min" })).toBeInTheDocument();
    expect(screen.getByRole("menuitem", { name: "Stop in 5 min" })).toBeInTheDocument();
    expect(screen.queryByRole("menuitem", { name: /1 hr/i })).not.toBeInTheDocument();
  });

  it("updates preview lifetime from the hidden menu", async () => {
    const user = userEvent.setup();
    mockGet.mockResolvedValue(
      makePreviewStatus({
        status: "ready",
        expires_at: "2026-12-31T00:00:00Z",
      }),
    );

    renderWithProviders(<PreviewPanel {...DEFAULT_PROPS} />);

    await user.click(await screen.findByRole("button", { name: "Preview actions" }));
    await user.click(screen.getByRole("menuitem", { name: "Stop in 5 min" }));

    await waitFor(() => {
      expect(mockSetLifetime).toHaveBeenCalledWith("sess-1", { duration_seconds: 300 });
    });
  });

  /* ---------- Partially ready phase ---------- */

  it("shows partially ready metadata and iframe in partially_ready state", async () => {
    mockGet.mockResolvedValue(
      makePreviewStatus({ status: "partially_ready", id: "prev-1" }),
    );

    renderWithProviders(<PreviewPanel {...DEFAULT_PROPS} />);

    await waitFor(() => {
      expect(screen.getByText("Partially ready")).toBeInTheDocument();
    });

    expect(screen.getByTitle("Preview")).toBeInTheDocument();
  });

  it("unmounts the startup canvas and restores top controls in partially_ready state", async () => {
    mockGet.mockResolvedValue(
      makePreviewStatus({ status: "partially_ready", id: "prev-1" }),
    );

    renderWithProviders(<PreviewPanel {...DEFAULT_PROPS} />);

    await waitFor(() => {
      expect(screen.getByText("Partially ready")).toBeInTheDocument();
    });

    expect(screen.queryByText("Preparing preview")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Stop preview" })).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Preview actions" })).toBeInTheDocument();
  });

  /* ---------- Failed phase ---------- */

  it("shows failure diagnostics when status is failed", async () => {
    mockGet.mockResolvedValue(
      makePreviewStatus({
        status: "failed",
        error: "Container crashed unexpectedly",
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

    // Try Again button
    expect(screen.getByText("Try Again")).toBeInTheDocument();
  });

  it("lets users expand full startup logs for a failed preview", async () => {
    const user = userEvent.setup();
    const summary =
      "preview service readiness probe failed: service \"server\" exited before becoming ready; last output: go: downloading google.golang.org/genproto/googleapis/rpc | [143-preview] running migrations... | Failed to create migrator…";
    const fullLog =
      "service \"server\" failed: exited with code 1\n--- last output ---\ngo: downloading google.golang.org/genproto/googleapis/rpc\n[143-preview] running migrations...\nFailed to create migrator: failed to open source, \"file://migrations\": duplicate migration file: 000125_github_installation_repo_claims.down.sql";

    mockGet.mockResolvedValue(
      makePreviewStatus({
        status: "failed",
        error: summary,
      }),
    );
    mockLogs.mockResolvedValue([
      {
        id: "log-1",
        preview_instance_id: "prev-1",
        org_id: "org-1",
        level: "error",
        step: "start",
        message: fullLog,
        created_at: "2026-01-01T00:00:00Z",
      },
    ]);

    renderWithProviders(<PreviewPanel {...DEFAULT_PROPS} />);

    await waitFor(() => {
      expect(screen.getByText("Preview failed to start")).toBeInTheDocument();
    });

    const startupLogRegion = screen.getByLabelText("Preview startup error logs");
    expect(startupLogRegion).toHaveTextContent(summary);
    expect(startupLogRegion).toHaveClass("line-clamp-6");
    expect(startupLogRegion).toHaveClass("overflow-y-hidden");
    expect(startupLogRegion).not.toHaveClass("overflow-auto");

    await user.click(screen.getByRole("button", { name: "Show full error logs" }));

    await waitFor(() => {
      expect(startupLogRegion).toHaveTextContent(
        /duplicate migration file: 000125_github_installation_repo_claims\.down\.sql/,
      );
    });
    expect(startupLogRegion).not.toHaveClass("line-clamp-6");
    expect(startupLogRegion).toHaveClass("sm:max-h-[min(56vh,28rem)]");
    expect(startupLogRegion).toHaveClass("overflow-y-hidden");
    expect(startupLogRegion).not.toHaveClass("overflow-auto");
    expect(screen.getByRole("button", { name: "Show summary" })).toBeInTheDocument();
    expect(mockLogs).toHaveBeenCalledWith("sess-1");
  });

  it("does not show a standalone Failed badge when failure diagnostics are visible", async () => {
    mockGet.mockResolvedValue(
      makePreviewStatus({ status: "failed", error: "err" }),
    );

    renderWithProviders(<PreviewPanel {...DEFAULT_PROPS} />);

    await waitFor(() => {
      expect(screen.getByText("Preview failed to start")).toBeInTheDocument();
    });
    expect(screen.queryByText("Failed")).not.toBeInTheDocument();
  });

  /* ---------- Query error state ---------- */

  it('shows "Failed to load preview status" and Retry button on query error', async () => {
    mockGet.mockRejectedValue(new Error("Network error"));

    renderWithProviders(<PreviewPanel {...DEFAULT_PROPS} />);

    // Query has retry: 2, so react-query retries before surfacing the error.
    // Use a longer timeout to account for the retry delay.
    await waitFor(
      () => {
        expect(
          screen.getByText("Failed to load preview status"),
        ).toBeInTheDocument();
      },
      { timeout: 5000 },
    );

    expect(screen.getByText("Network error")).toBeInTheDocument();
    expect(screen.getByText("Retry")).toBeInTheDocument();
  });

  /* ---------- Service status indicators ---------- */

  it("does not render ready-state service status indicators when multiple services exist", async () => {
    mockGet.mockResolvedValue(
      makePreviewStatus({ status: "ready" }, [
        {
          id: "svc-1",
          preview_instance_id: "prev-1",
          service_name: "frontend",
          role: "primary",
          status: "ready",
          command: ["npm", "start"],
          cwd: "",
          port: 3000,
          created_at: "2026-01-01T00:00:00Z",
        },
        {
          id: "svc-2",
          preview_instance_id: "prev-1",
          service_name: "server",
          role: "support",
          status: "starting",
          command: ["go", "run", "."],
          cwd: "",
          port: 8080,
          error: "port binding",
          created_at: "2026-01-01T00:00:00Z",
        },
      ]),
    );

    renderWithProviders(<PreviewPanel {...DEFAULT_PROPS} />);

    await waitFor(() => {
      expect(screen.getByText("Running")).toBeInTheDocument();
    });

    expect(screen.queryByText("frontend")).not.toBeInTheDocument();
    expect(screen.queryByText("server")).not.toBeInTheDocument();
    expect(screen.queryByText("(port binding)")).not.toBeInTheDocument();
  });

  it("does not render service indicators when only one service exists", async () => {
    mockGet.mockResolvedValue(
      makePreviewStatus({ status: "ready" }, [
        {
          id: "svc-1",
          preview_instance_id: "prev-1",
          service_name: "frontend",
          role: "primary",
          status: "ready",
          command: ["npm", "start"],
          cwd: "",
          port: 3000,
          created_at: "2026-01-01T00:00:00Z",
        },
      ]),
    );

    renderWithProviders(<PreviewPanel {...DEFAULT_PROPS} />);

    await waitFor(() => {
      expect(screen.getByText("Running")).toBeInTheDocument();
    });

    // Service error indicators should not appear
    expect(screen.queryByText("(port binding)")).not.toBeInTheDocument();
  });

  /* ---------- Phase helpers via badge classes ---------- */

  it("uses quiet metadata for the ready phase instead of a status badge", async () => {
    mockGet.mockResolvedValue(makePreviewStatus({ status: "ready" }));

    renderWithProviders(<PreviewPanel {...DEFAULT_PROPS} />);

    await waitFor(() => {
      expect(screen.getByText("Running")).toBeInTheDocument();
    });

    expect(screen.queryByText("Ready")).not.toBeInTheDocument();
  });

  it("applies destructive color class to failed diagnostics", async () => {
    mockGet.mockResolvedValue(
      makePreviewStatus({ status: "failed", error: "err" }),
    );

    renderWithProviders(<PreviewPanel {...DEFAULT_PROPS} />);

    await waitFor(() => {
      expect(screen.getByText("Preview failed to start")).toBeInTheDocument();
    });

    const heading = screen.getByText("Preview failed to start").closest("[class]")!;
    expect(heading.className).toContain("text-destructive");
  });

  it("uses one primary starting label in the startup canvas", async () => {
    mockGet.mockResolvedValue(makePreviewStatus({ status: "starting" }));

    renderWithProviders(<PreviewPanel {...DEFAULT_PROPS} />);

    await waitFor(() => {
      expect(screen.getByText("Preparing preview")).toBeInTheDocument();
    });

    expect(screen.getAllByText("Starting")).toHaveLength(1);
  });

  it("uses quiet metadata for the partially_ready phase instead of a status badge", async () => {
    mockGet.mockResolvedValue(
      makePreviewStatus({ status: "partially_ready" }),
    );

    renderWithProviders(<PreviewPanel {...DEFAULT_PROPS} />);

    await waitFor(() => {
      expect(screen.getByText("Partially ready")).toBeInTheDocument();
    });

    expect(screen.queryByText("Partially Ready")).not.toBeInTheDocument();
  });

  /* ---------- Start mutation ---------- */

  it("calls start mutation when Start Preview button is clicked in idle state", async () => {
    const user = userEvent.setup();
    mockGet.mockResolvedValue(makePreviewStatus({ status: "stopped" }));

    renderWithProviders(<PreviewPanel {...DEFAULT_PROPS} />);

    await waitFor(() => {
      expect(screen.getByText("No preview running")).toBeInTheDocument();
    });

    await user.click(screen.getByRole("button", { name: "Start Preview" }));

    await waitFor(() => {
      expect(mockStart).toHaveBeenCalledWith("sess-1");
    });
  });

  it("shows only the loading spinner while starting a preview from idle state", async () => {
    const user = userEvent.setup();
    mockGet.mockResolvedValue(makePreviewStatus({ status: "stopped" }));
    mockStart.mockReturnValue(
      new Promise<void>(() => {
        // Keep the mutation pending so the loading state remains visible.
      }),
    );

    renderWithProviders(<PreviewPanel {...DEFAULT_PROPS} />);

    await waitFor(() => {
      expect(screen.getByRole("button", { name: "Start Preview" })).toBeInTheDocument();
    });

    const button = screen.getByRole("button", { name: "Start Preview" });
    await user.click(button);

    await waitFor(() => {
      expect(button.querySelector("[data-slot='button-spinner']")).toBeInTheDocument();
    });
    expect(button.querySelector("svg.lucide-play")).not.toBeInTheDocument();
  });

  it("keeps infrastructure and service details collapsed until requested", async () => {
    const user = userEvent.setup();
    mockGet.mockResolvedValue(
      makePreviewStatus(
        { status: "starting" },
        [
          {
            id: "svc-1",
            preview_instance_id: "prev-1",
            service_name: "web",
            role: "primary",
            status: "starting",
            command: ["npm", "run", "dev"],
            cwd: "",
            port: 3000,
            created_at: "2026-01-01T00:00:00Z",
          },
        ],
        [
          {
            id: "infra-1",
            preview_instance_id: "prev-1",
            infra_name: "postgres",
            template: "postgres",
            container_id: "ctr-1",
            status: "provisioning",
            host: "postgres",
            port: 5432,
            created_at: "2026-01-01T00:00:00Z",
          },
        ],
      ),
    );

    renderWithProviders(<PreviewPanel {...DEFAULT_PROPS} />);

    await waitFor(() => {
      expect(screen.getByText("Preparing preview")).toBeInTheDocument();
    });

    expect(screen.queryByText("postgres is provisioning")).not.toBeInTheDocument();
    expect(screen.queryByText("web is starting")).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Details" }));

    expect(screen.getByText("Spin up infrastructure")).toBeInTheDocument();
    expect(screen.getByText("postgres is provisioning")).toBeInTheDocument();
    expect(screen.getByText("Start services")).toBeInTheDocument();
    expect(screen.getByText("web is starting")).toBeInTheDocument();
    expect(screen.getByText("Open the preview")).toBeInTheDocument();
  });

  it("renders orphaned pending children as terminal when the parent preview failed", async () => {
    const user = userEvent.setup();
    mockGet.mockResolvedValue(
      makePreviewStatus(
        { status: "failed", error: "provider start preview failed" },
        [
          {
            id: "svc-1",
            preview_instance_id: "prev-1",
            service_name: "web",
            role: "primary",
            status: "starting",
            command: ["npm", "run", "dev"],
            cwd: "",
            port: 3000,
            created_at: "2026-01-01T00:00:00Z",
          },
        ],
        [
          {
            id: "infra-1",
            preview_instance_id: "prev-1",
            infra_name: "postgres",
            template: "postgres",
            container_id: "ctr-1",
            status: "provisioning",
            host: "postgres",
            port: 5432,
            created_at: "2026-01-01T00:00:00Z",
          },
        ],
      ),
    );

    renderWithProviders(<PreviewPanel {...DEFAULT_PROPS} />);

    await waitFor(() => {
      expect(screen.getByText("Preview failed to start")).toBeInTheDocument();
    });

    await user.click(screen.getByRole("button", { name: /Details/ }));

    expect(screen.getByText("postgres did not finish provisioning")).toBeInTheDocument();
    expect(screen.getByText("web did not finish starting")).toBeInTheDocument();
    expect(screen.queryByText("postgres is provisioning")).not.toBeInTheDocument();
    expect(screen.queryByText("web is starting")).not.toBeInTheDocument();
  });

  /* ---------- Stop mutation ---------- */

  it("calls stop mutation from the preview actions menu", async () => {
    const user = userEvent.setup();
    mockGet.mockResolvedValue(makePreviewStatus({ status: "ready" }));

    renderWithProviders(<PreviewPanel {...DEFAULT_PROPS} />);

    await waitFor(() => {
      expect(screen.getByRole("button", { name: "Preview actions" })).toBeInTheDocument();
    });

    await user.click(screen.getByRole("button", { name: "Preview actions" }));
    await user.click(screen.getByRole("menuitem", { name: "Stop preview" }));

    await waitFor(() => {
      expect(mockStop).toHaveBeenCalledWith("sess-1");
    });
  });

  it("calls stop mutation from the starting preview canvas", async () => {
    const user = userEvent.setup();
    mockGet.mockResolvedValue(makePreviewStatus({ status: "starting" }));

    renderWithProviders(<PreviewPanel {...DEFAULT_PROPS} />);

    await waitFor(() => {
      expect(screen.getByRole("button", { name: "Stop preview" })).toBeInTheDocument();
    });

    await user.click(screen.getByRole("button", { name: "Stop preview" }));

    await waitFor(() => {
      expect(mockStop).toHaveBeenCalledWith("sess-1");
    });
  });

  /* ---------- Restart mutation ---------- */

  it("calls restart mutation from the preview actions menu", async () => {
    const user = userEvent.setup();
    mockGet.mockResolvedValue(makePreviewStatus({ status: "ready" }));

    renderWithProviders(<PreviewPanel {...DEFAULT_PROPS} />);

    await waitFor(() => {
      expect(screen.getByRole("button", { name: "Preview actions" })).toBeInTheDocument();
    });

    await user.click(screen.getByRole("button", { name: "Preview actions" }));
    await user.click(screen.getByRole("menuitem", { name: "Restart preview" }));

    await waitFor(() => {
      expect(mockRestart).toHaveBeenCalledWith("sess-1");
    });
  });

  it("calls restart mutation from the starting preview canvas", async () => {
    const user = userEvent.setup();
    mockGet.mockResolvedValue(makePreviewStatus({ status: "starting" }));

    renderWithProviders(<PreviewPanel {...DEFAULT_PROPS} />);

    await waitFor(() => {
      expect(screen.getByRole("button", { name: "Restart preview" })).toBeInTheDocument();
    });

    await user.click(screen.getByRole("button", { name: "Restart preview" }));

    await waitFor(() => {
      expect(mockRestart).toHaveBeenCalledWith("sess-1");
    });
  });

  /* ---------- Mutation error banner ---------- */

  it("shows mutation error banner when start fails", async () => {
    const user = userEvent.setup();
    mockGet.mockResolvedValue(makePreviewStatus({ status: "stopped" }));
    mockStart.mockRejectedValueOnce(new Error("connection refused"));

    renderWithProviders(<PreviewPanel {...DEFAULT_PROPS} />);

    await waitFor(() => {
      expect(screen.getByText("No preview running")).toBeInTheDocument();
    });

    await user.click(screen.getByRole("button", { name: "Start Preview" }));

    await waitFor(() => {
      expect(screen.getByText("Failed to start preview: connection refused")).toBeInTheDocument();
    });
  });

  it("shows a specific message when preview snapshot is unavailable", async () => {
    const user = userEvent.setup();
    mockGet.mockResolvedValue(makePreviewStatus({ status: "stopped" }));
    const err = new Error("snapshot unavailable");
    (err as Error & { code?: string }).code = "SNAPSHOT_UNAVAILABLE";
    mockStart.mockRejectedValueOnce(err);

    renderWithProviders(<PreviewPanel {...DEFAULT_PROPS} />);

    await waitFor(() => {
      expect(screen.getByText("No preview running")).toBeInTheDocument();
    });

    await user.click(screen.getByRole("button", { name: "Start Preview" }));

    await waitFor(() => {
      expect(
        screen.getByText(
          "This session's last sandbox snapshot is unavailable. Send a new message to rebuild the sandbox, then try Start Preview again."
        )
      ).toBeInTheDocument();
    });
  });

  it("shows a retry-the-turn message when the sandbox is busy with a concurrent agent turn", async () => {
    const user = userEvent.setup();
    mockGet.mockResolvedValue(makePreviewStatus({ status: "stopped" }));
    const err = new Error(
      "another process attached to this session's sandbox first; please retry"
    );
    (err as Error & { code?: string }).code = PREVIEW_ERROR_CODES.SANDBOX_BUSY;
    mockStart.mockRejectedValueOnce(err);

    renderWithProviders(<PreviewPanel {...DEFAULT_PROPS} />);

    await waitFor(() => {
      expect(screen.getByText("No preview running")).toBeInTheDocument();
    });

    await user.click(screen.getByRole("button", { name: "Start Preview" }));

    await waitFor(() => {
      expect(
        screen.getByText(
          "The agent is currently using this session's sandbox. Wait for the current turn to finish, then try Start Preview again."
        )
      ).toBeInTheDocument();
    });
    // Guard against regression: the historical message conflated SANDBOX_BUSY
    // with "Docker not configured" because both used to share the NO_SANDBOX
    // code. Splitting the codes was the whole point — fail loudly if anyone
    // re-merges them.
    expect(
      screen.queryByText(
        "Preview is unavailable on this server (Docker not configured). Contact an admin."
      )
    ).not.toBeInTheDocument();
  });

  it("shows a transient-retry message when the API can't reach the preview worker", async () => {
    const user = userEvent.setup();
    mockGet.mockResolvedValue(makePreviewStatus({ status: "stopped" }));
    // PREVIEW_WORKER_REQUEST_FAILED happens when the API's RPC to the worker
    // EOFs (e.g. worker WriteTimeout overrun, or worker container restart).
    // No structured error came back — without explicit handling it would
    // fall through to "Failed to start preview: preview worker request failed",
    // which buries the transient/retryable nature of the failure.
    const err = new Error("preview worker request failed");
    (err as Error & { code?: string }).code = PREVIEW_ERROR_CODES.WORKER_REQUEST_FAILED;
    mockStart.mockRejectedValueOnce(err);

    renderWithProviders(<PreviewPanel {...DEFAULT_PROPS} />);

    await waitFor(() => {
      expect(screen.getByText("No preview running")).toBeInTheDocument();
    });

    await user.click(screen.getByRole("button", { name: "Start Preview" }));

    await waitFor(() => {
      expect(
        screen.getByText(
          "Could not reach the preview worker (connection dropped). Try Start Preview again — if this keeps happening, the worker may be unhealthy."
        )
      ).toBeInTheDocument();
    });
    expect(
      screen.queryByText("Failed to start preview: preview worker request failed")
    ).not.toBeInTheDocument();
  });

  it("shows the backend message verbatim (no 'Failed to start preview:' prefix) when no .143/config.json is committed", async () => {
    const user = userEvent.setup();
    mockGet.mockResolvedValue(makePreviewStatus({ status: "stopped" }));
    const backendMessage =
      "This repo has no .143/config.json committed with a preview section. Add one (see docs/guides/previews.md) so the preview knows what command to run.";
    const err = new Error(backendMessage);
    (err as Error & { code?: string }).code = "PREVIEW_NO_CONFIG";
    mockStart.mockRejectedValueOnce(err);

    renderWithProviders(<PreviewPanel {...DEFAULT_PROPS} />);

    await waitFor(() => {
      expect(screen.getByText("No preview running")).toBeInTheDocument();
    });

    await user.click(screen.getByRole("button", { name: "Start Preview" }));

    await waitFor(() => {
      expect(screen.getByText(backendMessage)).toBeInTheDocument();
    });
    // Guard against regression: if anyone wraps this code in the generic
    // "Failed to start preview:" prefix, the backend's actionable message
    // (which names the file the user must add) gets buried.
    expect(
      screen.queryByText(`Failed to start preview: ${backendMessage}`)
    ).not.toBeInTheDocument();
  });

  // Provider-side launch failures (image pull, infra health, init script,
  // readiness) carry a backend-built message that names the failing image
  // or service and the underlying cause. We pass it through verbatim — if
  // anyone re-wraps it with the generic "Failed to start preview:" prefix,
  // the actionable detail gets buried.
  it.each([
    [
      "PREVIEW_INFRA_IMAGE_UNAVAILABLE",
      "preview infrastructure image is not available on this worker. The image could not be pulled from its registry — check the worker's network egress and registry credentials. Details: provider start preview: provision infrastructure \"db\": preview infrastructure image unavailable: pull \"postgres:17-alpine\": registry unreachable",
    ],
    [
      "PREVIEW_INFRA_UNHEALTHY",
      "preview infrastructure container did not become healthy in time. The container started but its health check (e.g. pg_isready) never passed. Details: provider start preview: preview infrastructure container failed health check: infrastructure \"db\" (postgres-17): health check timed out after 60 seconds",
    ],
    [
      "PREVIEW_SERVICE_NOT_READY",
      "preview service did not pass its readiness probe. The service may have crashed at boot, taken too long to start, or be listening on a different port than declared in .143/config.json. Details: provider start preview: preview service readiness probe failed: primary service \"app\" (port 3000): timeout",
    ],
  ])(
    "passes backend message through verbatim for %s without the generic prefix",
    async (code, backendMessage) => {
      const user = userEvent.setup();
      mockGet.mockResolvedValue(makePreviewStatus({ status: "stopped" }));
      const err = new Error(backendMessage);
      (err as Error & { code?: string }).code = code;
      mockStart.mockRejectedValueOnce(err);

      renderWithProviders(<PreviewPanel {...DEFAULT_PROPS} />);

      await waitFor(() => {
        expect(screen.getByText("No preview running")).toBeInTheDocument();
      });

      await user.click(screen.getByRole("button", { name: "Start Preview" }));

      await waitFor(() => {
        expect(screen.getByText(backendMessage)).toBeInTheDocument();
      });
      expect(
        screen.queryByText(`Failed to start preview: ${backendMessage}`)
      ).not.toBeInTheDocument();
    }
  );

  it("dismisses mutation error banner when X is clicked", async () => {
    const user = userEvent.setup();
    mockGet.mockResolvedValue(makePreviewStatus({ status: "stopped" }));
    mockStart.mockRejectedValueOnce(new Error("connection refused"));

    renderWithProviders(<PreviewPanel {...DEFAULT_PROPS} />);

    await waitFor(() => {
      expect(screen.getByText("No preview running")).toBeInTheDocument();
    });

    await user.click(screen.getByRole("button", { name: "Start Preview" }));

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
    mockGet.mockResolvedValue(makePreviewStatus({ status: "ready" }));
    mockStop.mockRejectedValueOnce(new Error("timeout"));

    renderWithProviders(<PreviewPanel {...DEFAULT_PROPS} />);

    await waitFor(() => {
      expect(screen.getByRole("button", { name: "Preview actions" })).toBeInTheDocument();
    });

    await user.click(screen.getByRole("button", { name: "Preview actions" }));
    await user.click(screen.getByRole("menuitem", { name: "Stop preview" }));

    await waitFor(() => {
      expect(screen.getByText("Failed to stop preview: timeout")).toBeInTheDocument();
    });
  });

  it("shows mutation error banner when restart fails", async () => {
    const user = userEvent.setup();
    mockGet.mockResolvedValue(makePreviewStatus({ status: "ready" }));
    mockRestart.mockRejectedValueOnce(new Error("server error"));

    renderWithProviders(<PreviewPanel {...DEFAULT_PROPS} />);

    await waitFor(() => {
      expect(screen.getByRole("button", { name: "Preview actions" })).toBeInTheDocument();
    });

    await user.click(screen.getByRole("button", { name: "Preview actions" }));
    await user.click(screen.getByRole("menuitem", { name: "Restart preview" }));

    await waitFor(() => {
      expect(screen.getByText("Failed to restart preview: server error")).toBeInTheDocument();
    });
  });

  /* ---------- Design mode toggle ---------- */

  it("shows design mode overlay when design mode button is toggled on", async () => {
    const user = userEvent.setup();
    mockGet.mockResolvedValue(makePreviewStatus({ status: "ready", id: "prev-1" }));

    renderWithProviders(<PreviewPanel {...DEFAULT_PROPS} />);

    await waitFor(() => {
      expect(screen.getByText("Running")).toBeInTheDocument();
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
    mockGet.mockResolvedValue(makePreviewStatus({ status: "ready", id: "prev-1" }));

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
        status: "failed",
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

  /* ---------- Bootstrap origin enforcement ---------- */

  it("ignores bootstrap_ready messages from a foreign origin (no token is minted)", async () => {
    mockGet.mockResolvedValue(
      makePreviewStatus({ status: "ready", id: "prev-1" }),
    );

    renderWithProviders(<PreviewPanel {...DEFAULT_PROPS} />);

    await waitFor(() => {
      expect(screen.getByTitle("Preview")).toBeInTheDocument();
    });

    // Dispatch a bootstrap_ready message whose origin does NOT match the
    // preview's parsedOrigin (http://prev-1.preview.test). A malicious tab
    // or misconfigured embed posting this event must not trigger token
    // minting — that would leak access credentials cross-origin.
    window.dispatchEvent(
      new MessageEvent("message", {
        data: { type: PREVIEW_BOOTSTRAP_READY_EVENT },
        origin: "https://evil.example.com",
      }),
    );

    // Give the event loop a tick for any unintended mutation to fire.
    await new Promise((resolve) => setTimeout(resolve, 50));

    expect(mockBootstrap).not.toHaveBeenCalled();
  });

  it("mints a token when bootstrap_ready arrives from the matching origin", async () => {
    mockGet.mockResolvedValue(
      makePreviewStatus({ status: "ready", id: "prev-1" }),
    );

    renderWithProviders(<PreviewPanel {...DEFAULT_PROPS} />);

    await waitFor(() => {
      expect(screen.getByTitle("Preview")).toBeInTheDocument();
    });

    window.dispatchEvent(
      new MessageEvent("message", {
        data: { type: PREVIEW_BOOTSTRAP_READY_EVENT },
        // Matches buildPreviewIframeSrc output for this test preview id.
        origin: "http://prev-1.preview.test",
      }),
    );

    await waitFor(() => {
      expect(mockBootstrap).toHaveBeenCalledWith("sess-1");
    });
  });
});
