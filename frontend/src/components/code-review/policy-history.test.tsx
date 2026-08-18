import { beforeEach, describe, expect, it, vi } from "vitest";
import { renderWithProviders, screen, userEvent, waitFor, within } from "@/test/test-utils";
import type { CodeReviewPolicyComparison, CodeReviewPolicyVersionSummary } from "@/lib/types";
import { CodeReviewPolicyHistory } from "./policy-history";

const mocks = vi.hoisted(() => ({
  listPolicyVersions: vi.fn(),
  comparePolicyVersions: vi.fn(),
  restorePolicyVersion: vi.fn(),
  getPolicy: vi.fn(),
  listMembers: vi.fn(),
  success: vi.fn(),
  error: vi.fn(),
}));

vi.mock("@/lib/api", async () => {
  const actual = await vi.importActual<typeof import("@/lib/api")>("@/lib/api");
  return {
    ...actual,
    api: {
      codeReviews: {
        listPolicyVersions: mocks.listPolicyVersions,
        comparePolicyVersions: mocks.comparePolicyVersions,
        restorePolicyVersion: mocks.restorePolicyVersion,
        getPolicy: mocks.getPolicy,
      },
      team: { listMembers: mocks.listMembers },
    },
  };
});

vi.mock("@/lib/notify", () => ({
  notify: { success: mocks.success, error: mocks.error },
}));

vi.mock("@/hooks/use-auth", () => ({
  useAuth: () => ({ user: { id: "user-1", role: "admin" }, isLoading: false }),
}));

const versions: CodeReviewPolicyVersionSummary[] = [
  {
    id: "policy-3",
    version: 3,
    active: true,
    previous_policy_id: "policy-2",
    previous_policy_version: 2,
    summary: "Review instructions edited",
    changed_fields: [{ path: "review_instructions", label: "Review instructions", kind: "text" }],
    created_at: "2026-08-18T18:00:00Z",
    audit: {
      id: 3,
      actor_type: "user",
      actor_id: "user-1",
      actor_name: "Alice Smith",
      user_id: "user-1",
      source: "manual",
      reason: "Make tests explicit",
      request_id: "request-3",
      created_at: "2026-08-18T18:00:00Z",
    },
  },
  {
    id: "policy-2",
    version: 2,
    active: false,
    previous_policy_id: "policy-1",
    previous_policy_version: 1,
    summary: "Inline comment limit changed",
    changed_fields: [{ path: "inline_comment_limit", label: "Inline comment limit", kind: "value" }],
    created_at: "2026-08-17T18:00:00Z",
    audit: {
      id: 2,
      actor_type: "user",
      actor_id: "user-2",
      actor_name: "Bob Chen",
      user_id: "user-2",
      source: "manual",
      created_at: "2026-08-17T18:00:00Z",
    },
  },
  {
    id: "policy-1",
    version: 1,
    active: false,
    summary: "Initial organization policy created",
    changed_fields: [],
    created_at: "2026-08-16T18:00:00Z",
  },
];

describe("CodeReviewPolicyHistory", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.listPolicyVersions.mockResolvedValue({ data: versions, meta: {} });
    mocks.listMembers.mockResolvedValue({
      data: [
        { id: "user-1", name: "Alice Smith", role: "admin" },
        { id: "user-2", name: "Bob Chen", role: "admin" },
      ],
      meta: {},
    });
    mocks.getPolicy.mockResolvedValue({ data: { policy: { id: "policy-3", version: 3 } } });
    mocks.comparePolicyVersions.mockImplementation((newerID: string, olderID: string) => {
      const comparison: CodeReviewPolicyComparison = newerID === "policy-2"
        ? {
            newer: versions[1], older: versions[2],
            changes: [{ path: "inline_comment_limit", label: "Inline comment limit", kind: "value", before: 2, after: 4 }],
          }
        : {
            newer: versions[0], older: olderID === "policy-1" ? versions[2] : versions[1],
            changes: [
              {
                path: "review_instructions", label: "Review instructions", kind: "text",
                before: "Review changed code.", after: "Review changed code.\nRequire focused tests.",
              },
              {
                path: "agent_roster.reviewers", label: "Reviewers", kind: "list",
                before: ["codex", "claude_code", "opencode"], after: ["claude_code", "codex"],
              },
            ],
          };
      return Promise.resolve({ data: comparison });
    });
    mocks.restorePolicyVersion.mockImplementation(() => {
      mocks.listPolicyVersions.mockResolvedValue({
        data: [{
          ...versions[0],
          id: "policy-4",
          version: 4,
          previous_policy_id: "policy-3",
          previous_policy_version: 3,
          summary: "2 policy fields changed",
        }, ...versions.map((version) => ({ ...version, active: false }))],
        meta: {},
      });
      return Promise.resolve({
        data: {
          policy: { id: "policy-4", version: 4 },
          restored_from: { id: "policy-2", version: 2 },
        },
      });
    });
  });

  it("shows exact changes, Details, and restores history as a new version", async () => {
    const user = userEvent.setup();
    renderWithProviders(<CodeReviewPolicyHistory />);

    const trigger = await screen.findByRole("button", { name: /Last activity:.*Alice Smith/ });
    await user.click(trigger);
    const history = await screen.findByRole("dialog", { name: "Review policy history" });

    expect(within(history).getByText(/Latest change: Review instructions edited/)).toBeInTheDocument();
    expect(await within(history).findByText("Require focused tests.")).toBeInTheDocument();
    expect(within(history).getByText("opencode")).toBeInTheDocument();
    expect(within(history).getByText("The list order changed.")).toBeInTheDocument();
    expect(within(history).queryByText("Current version")).not.toBeInTheDocument();

    await user.click(within(history).getByRole("combobox", { name: "Compare with" }));
    await user.click(await screen.findByRole("option", { name: /Version 1.*Initial organization policy created/ }));
    expect(await within(history).findByText("Changes from version 1 to version 3")).toBeInTheDocument();
    expect(mocks.comparePolicyVersions).toHaveBeenCalledWith("policy-3", "policy-1");

    await user.click(within(history).getByRole("button", { name: "Details" }));
    expect(within(history).getByText("request-3")).toBeInTheDocument();

    await user.click(within(history).getByRole("button", { name: /Initial organization policy created/ }));
    expect(within(history).getByText(/no earlier version to compare/i)).toBeInTheDocument();
    await user.click(within(history).getByRole("button", { name: "Details" }));
    expect(within(history).getByText("policy-1")).toBeInTheDocument();

    await user.click(within(history).getByRole("button", { name: /Inline comment limit changed/ }));
    expect(await within(history).findByText("2")).toBeInTheDocument();
    expect(within(history).getByText("4")).toBeInTheDocument();
    await user.click(within(history).getByRole("button", { name: "Restore as new version" }));
    const confirmation = await screen.findByRole("alertdialog", { name: "Restore version 2?" });
    await user.click(within(confirmation).getByRole("button", { name: "Restore as new version" }));

    await waitFor(() => expect(mocks.restorePolicyVersion).toHaveBeenCalledWith("policy-2", 3));
    expect(mocks.success).toHaveBeenCalledWith("Version 2 restored as version 4");
    await waitFor(() => expect(mocks.comparePolicyVersions).toHaveBeenCalledWith("policy-4", "policy-3"));
  });

  it("compares the page-boundary version before its predecessor is loaded", async () => {
    const user = userEvent.setup();
    mocks.listPolicyVersions.mockResolvedValue({ data: versions.slice(0, 2), meta: { next_cursor: "2" } });

    renderWithProviders(<CodeReviewPolicyHistory />);

    await user.click(await screen.findByRole("button", { name: /Last activity:.*Alice Smith/ }));
    const history = await screen.findByRole("dialog", { name: "Review policy history" });
    await user.click(within(history).getByRole("button", { name: /Inline comment limit changed/ }));

    expect(await within(history).findByText("Changes from version 1 to version 2")).toBeInTheDocument();
    await waitFor(() => expect(mocks.comparePolicyVersions).toHaveBeenCalledWith("policy-2", "policy-1"));
    expect(within(history).getByText("4")).toBeInTheDocument();
  });

  it("keeps a retryable history error available when the first request fails", async () => {
    const user = userEvent.setup();
    mocks.listPolicyVersions.mockRejectedValueOnce(new Error("history unavailable"));

    renderWithProviders(<CodeReviewPolicyHistory />);

    await user.click(await screen.findByRole("button", { name: "Policy history unavailable" }));
    const history = await screen.findByRole("dialog", { name: "Review policy history" });
    expect(within(history).getByRole("alert")).toHaveTextContent("Policy history could not be loaded");

    await user.click(within(history).getByRole("button", { name: "Retry" }));
    expect(await within(history).findByText(/Latest change: Review instructions edited/)).toBeInTheDocument();
  });
});
