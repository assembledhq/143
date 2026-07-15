import { beforeEach, describe, expect, it, vi } from "vitest";
import { renderWithProviders, screen, userEvent } from "@/test/test-utils";
import { AutopilotPageContent } from "./autopilot-page-content";
import type { AutopilotQueueRow } from "@/lib/types";

const replaceMock = vi.fn();
const authState = vi.hoisted(() => ({
  role: "admin",
}));

vi.mock("next/navigation", () => ({
  useRouter: () => ({
    push: vi.fn(),
    replace: replaceMock,
  }),
}));

vi.mock("@/hooks/use-analyze", () => ({
  useAnalyze: () => ({
    handleAnalyze: vi.fn(),
    isAnalyzing: false,
    isPending: false,
  }),
}));

vi.mock("@/hooks/use-auth", () => ({
  useAuth: () => ({
    user: { id: "user-1", role: authState.role },
    isLoading: false,
  }),
}));

const queueRow: AutopilotQueueRow = {
  id: "issue-1",
  rank: 1,
  source: { type: "sentry", key: "SENTRY-123456789" },
  title: "Mobile checkout descriptions should not overlap source badges",
  repo: { id: "repo-1", name: "web" },
  issue_status: "open",
  customer_impact: { label: "High", count: 42 },
  implementation_ease: "Easy",
  low_hanging_fruit: {
    label: "High",
    reasons: ["clear reproduction"],
    cluster_size: 1,
  },
  display_run_state: "not_started",
  available_action: "start_run",
};

let queueRows: AutopilotQueueRow[] = [queueRow];

vi.mock("./use-autopilot-page-data", () => ({
  useAutopilotPageData: () => ({
    isLoading: false,
    isSetupComplete: true,
    pmStatus: { is_running: false },
    settings: {},
    viewModel: {
      statusLine: "Ready",
      directionSummary: "No direction set",
      focusAreas: [],
      documentsSummary: "No documents",
      weightsSummary: "Default weights",
    },
    queue: {
      data: queueRows,
      meta: {
        summary: {
          top_issue_id: "issue-1",
          autorunnable_count: 1,
          needs_review_count: 0,
          open_pr_count: 0,
          active_run_count: 0,
          ranked_issue_count: 1,
        },
      },
    },
    queueLoading: false,
    hasNextQueuePage: false,
    fetchNextQueuePage: vi.fn(),
    isFetchingNextQueuePage: false,
  }),
}));

vi.mock("./autopilot-steering-sheet", () => ({
  AutopilotSteeringSheet: ({ open }: { open: boolean }) => (
    open ? <div role="dialog" aria-label="Autopilot steering" /> : null
  ),
}));

vi.mock("./autopilot-documents-sheet", () => ({
  AutopilotDocumentsSheet: ({ open }: { open: boolean }) => (
    open ? <div role="dialog" aria-label="Autopilot documents" /> : null
  ),
}));

vi.mock("@/components/autopilot-proposal-card", () => ({
  AutopilotProposalCard: () => null,
}));

describe("AutopilotPageContent", () => {
  beforeEach(() => {
    authState.role = "admin";
    queueRows = [queueRow];
  });

  it("shows aggregate summary cards without duplicating the top opportunity", async () => {
    renderWithProviders(<AutopilotPageContent />);

    expect(await screen.findByText("Auto-runnable now")).toBeInTheDocument();
    expect(screen.getByText("Needs review")).toBeInTheDocument();
    expect(screen.getByText("Connected work")).toBeInTheDocument();
    expect(screen.queryByText("Top opportunity")).not.toBeInTheDocument();
  });

  it("turns queue rows into compact cards on mobile while preserving the desktop table", async () => {
    renderWithProviders(<AutopilotPageContent />);

    const table = screen.getByRole("table");
    expect(table).toHaveClass("w-full", "min-w-[64rem]", "table-auto");
    expect(screen.getByTestId("autopilot-queue-row")).toHaveAttribute("data-slot", "table-row");
    expect(screen.getByTestId("autopilot-queue-mobile-row")).toHaveAttribute("data-slot", "resource-row");
    expect(screen.getAllByRole("button", { name: "Start run" })).toHaveLength(2);
    expect(await screen.findByText("Source")).toBeInTheDocument();
    expect(screen.getByText("Source").closest("thead")).not.toHaveClass("hidden");
  });

  it("keeps the queue table header sticky while the issue rows scroll", async () => {
    renderWithProviders(<AutopilotPageContent />);

    const sourceHeader = await screen.findByText("Source");
    const tableHeader = sourceHeader.closest("thead");
    expect(tableHeader).toHaveClass("sticky", "top-0", "z-10", "bg-card");
  });

  it("offers to create a session for a queue issue that has no linked session yet", async () => {
    queueRows = [
      {
        ...queueRow,
        available_action: "blocked",
        action_disabled_reason: null,
        latest_session: undefined,
      },
    ];

    renderWithProviders(<AutopilotPageContent />);

    await userEvent.click((await screen.findAllByRole("button", { name: "Start run" }))[0]);

    expect(await screen.findByRole("heading", { name: "Start run" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Create session" })).toBeEnabled();
  });

  it("hides mutating actions for viewers", async () => {
    authState.role = "viewer";

    renderWithProviders(<AutopilotPageContent />);

    expect(await screen.findByText("Autopilot")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Run analysis" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Start run" })).not.toBeInTheDocument();

    await userEvent.click(screen.getByText("No direction set"));
    await userEvent.click(screen.getByText("No documents"));

    expect(screen.queryByRole("dialog", { name: "Autopilot steering" })).not.toBeInTheDocument();
    expect(screen.queryByRole("dialog", { name: "Autopilot documents" })).not.toBeInTheDocument();
  });

  it("lets admins start a blocked queue issue and attach session notes", async () => {
    queueRows = [
      {
        ...queueRow,
        available_action: "blocked",
        action_disabled_reason: "Autopilot skipped this issue.",
        latest_session: undefined,
      },
    ];

    renderWithProviders(<AutopilotPageContent />);

    await userEvent.click((await screen.findAllByRole("button", { name: "Start run" }))[0]);
    expect(await screen.findByRole("heading", { name: "Start run" })).toBeInTheDocument();

    const notes = screen.getByLabelText("Session notes");
    await userEvent.type(notes, "Focus on the mobile checkout regression.");
    expect(notes).toHaveValue("Focus on the mobile checkout regression.");
    expect(screen.getByRole("button", { name: "Create session" })).toBeEnabled();
  });

  it("lets admins start a blocked issue that already has a linked session", async () => {
    queueRows = [
      {
        ...queueRow,
        available_action: "blocked",
        action_disabled_reason: "Autopilot skipped this issue.",
        latest_session: { id: "sess-1", title: "Existing session", updated_at: "2024-01-01T00:00:00Z" },
      },
    ];

    renderWithProviders(<AutopilotPageContent />);

    await userEvent.click((await screen.findAllByRole("button", { name: "Start run" }))[0]);
    expect(await screen.findByRole("heading", { name: "Start run" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Create session" })).toBeEnabled();
  });

  it("lets admins start a failed (retry) issue with a linked session instead of showing Retry", async () => {
    queueRows = [
      {
        ...queueRow,
        available_action: "retry",
        latest_session: { id: "sess-1", title: "Failed session", updated_at: "2024-01-01T00:00:00Z" },
      },
    ];

    renderWithProviders(<AutopilotPageContent />);

    expect(await screen.findAllByRole("button", { name: "Start run" })).toHaveLength(2);
    expect(screen.queryByRole("button", { name: "Retry" })).not.toBeInTheDocument();
  });

  it("does not offer to start a run for a blocked issue without a repository", async () => {
    queueRows = [
      {
        ...queueRow,
        available_action: "blocked",
        action_disabled_reason: "No repository selected",
        repo: undefined,
        latest_session: { id: "sess-1", title: "Existing session", updated_at: "2024-01-01T00:00:00Z" },
      },
    ];

    renderWithProviders(<AutopilotPageContent />);

    expect(await screen.findAllByRole("button", { name: "Blocked" })).toHaveLength(2);
    expect(screen.queryByRole("button", { name: "Start run" })).not.toBeInTheDocument();
  });

  it("uses Open preview as the primary action for a current ready runtime", async () => {
    queueRows = [
      {
        ...queueRow,
        available_action: "open_pr",
        latest_pr: { id: "pr-1", number: 42, url: "https://github.com/acme/web/pull/42", status: "open" },
        latest_preview: {
          target_id: "target-1",
          preview_id: "preview-1",
          status: "ready",
          commit_sha: "abc123",
          latest_commit_sha: "abc123",
          new_commits_available: false,
        },
      },
    ];

    renderWithProviders(<AutopilotPageContent />);

    expect(await screen.findAllByRole("button", { name: "Open preview" })).toHaveLength(2);
    expect(screen.queryByRole("button", { name: "Open PR" })).not.toBeInTheDocument();
  });

  it("makes stale previews update-first while keeping stale open secondary", async () => {
    queueRows = [
      {
        ...queueRow,
        available_action: "open_pr",
        latest_pr: { id: "pr-1", number: 42, url: "https://github.com/acme/web/pull/42", status: "open" },
        latest_preview: {
          target_id: "target-1",
          preview_id: "preview-1",
          status: "ready",
          commit_sha: "abc123",
          latest_commit_sha: "def456",
          new_commits_available: true,
        },
      },
    ];

    renderWithProviders(<AutopilotPageContent />);

    const updateButtons = await screen.findAllByRole("button", { name: "Update to latest" });
    expect(updateButtons).toHaveLength(2);
    expect(screen.getAllByRole("button", { name: "Open stale preview" })).toHaveLength(2);
    expect(updateButtons[1].parentElement).toHaveClass("flex-col", "md:flex-row");
    expect(screen.queryByRole("button", { name: "Open preview" })).not.toBeInTheDocument();
  });

  it("keeps stale previews openable for viewers without update controls", async () => {
    authState.role = "viewer";
    queueRows = [
      {
        ...queueRow,
        available_action: "open_pr",
        latest_pr: { id: "pr-1", number: 42, url: "https://github.com/acme/web/pull/42", status: "open" },
        latest_preview: {
          target_id: "target-1",
          preview_id: "preview-1",
          status: "ready",
          commit_sha: "abc123",
          latest_commit_sha: "def456",
          new_commits_available: true,
        },
      },
    ];

    renderWithProviders(<AutopilotPageContent />);

    expect(await screen.findAllByRole("button", { name: "Open stale preview" })).toHaveLength(2);
    expect(screen.queryByRole("button", { name: "Update to latest" })).not.toBeInTheDocument();
  });

  it("shows Retry preview instead of generic Open for failed preview rows", async () => {
    queueRows = [
      {
        ...queueRow,
        available_action: "open_pr",
        latest_pr: { id: "pr-1", number: 42, url: "https://github.com/acme/web/pull/42", status: "open" },
        latest_preview: {
          target_id: "target-1",
          preview_id: "preview-1",
          status: "failed",
          commit_sha: "abc123",
          latest_commit_sha: "abc123",
          new_commits_available: false,
        },
      },
    ];

    renderWithProviders(<AutopilotPageContent />);

    expect(await screen.findAllByRole("button", { name: "Retry preview" })).toHaveLength(2);
    expect(screen.queryByRole("button", { name: "Open preview" })).not.toBeInTheDocument();
  });

  it("shows Start preview for preview targets without a runtime", async () => {
    queueRows = [
      {
        ...queueRow,
        available_action: "open_pr",
        latest_pr: { id: "pr-1", number: 42, url: "https://github.com/acme/web/pull/42", status: "open" },
        latest_preview: {
          target_id: "target-1",
          status: "target_created",
          commit_sha: "abc123",
          latest_commit_sha: "abc123",
          new_commits_available: false,
        },
      },
    ];

    renderWithProviders(<AutopilotPageContent />);

    expect(await screen.findAllByRole("button", { name: "Start preview" })).toHaveLength(2);
    expect(screen.queryByRole("button", { name: "Open preview" })).not.toBeInTheDocument();
  });

  it("shows a disabled Starting button for in-progress preview launches", async () => {
    queueRows = [
      {
        ...queueRow,
        available_action: "open_pr",
        latest_pr: { id: "pr-1", number: 42, url: "https://github.com/acme/web/pull/42", status: "open" },
        latest_preview: {
          target_id: "target-1",
          preview_id: "preview-1",
          status: "starting",
          commit_sha: "abc123",
          latest_commit_sha: "abc123",
          new_commits_available: false,
        },
      },
    ];

    renderWithProviders(<AutopilotPageContent />);

    const buttons = await screen.findAllByRole("button", { name: "Starting..." });
    expect(buttons).toHaveLength(2);
    expect(buttons[0]).toBeDisabled();
    expect(buttons[1]).toBeDisabled();
  });

  it("does not let a preview target override view_run for an in-progress session", async () => {
    queueRows = [
      {
        ...queueRow,
        available_action: "view_run",
        display_run_state: "running",
        latest_session: { id: "session-1", title: "Fix auth", updated_at: new Date().toISOString() },
        latest_pr: { id: "pr-1", number: 42, url: "https://github.com/acme/web/pull/42", status: "open" },
        latest_preview: {
          target_id: "target-1",
          preview_id: "preview-1",
          status: "ready",
          commit_sha: "abc123",
          latest_commit_sha: "abc123",
          new_commits_available: false,
        },
      },
    ];

    renderWithProviders(<AutopilotPageContent />);

    expect(await screen.findAllByRole("link", { name: "View run" })).toHaveLength(2);
    expect(screen.queryByRole("button", { name: "Open preview" })).not.toBeInTheDocument();
  });
});
