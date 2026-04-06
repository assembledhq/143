import { describe, it, expect, vi } from "vitest";
import { renderWithProviders, screen, waitFor } from "@/test/test-utils";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { server } from "@/test/mocks/server";
import { ProposalInbox } from "./proposal-inbox";
import type { Project } from "@/lib/types";

vi.mock("next/navigation", () => ({
  usePathname: () => "/projects",
  useRouter: () => ({
    push: vi.fn(),
    replace: vi.fn(),
  }),
}));

function makeProposal(overrides: Partial<Project> = {}): Project {
  return {
    id: "p-1",
    org_id: "org-1",
    repository_id: "repo-1",
    title: "Refactor auth module",
    goal: "Improve auth security",
    status: "proposed",
    priority: 1,
    execution_mode: "sequential",
    max_concurrent: 1,
    auto_merge: false,
    base_branch: "main",
    total_tasks: 3,
    completed_tasks: 0,
    failed_tasks: 0,
    proposed_by_pm: true,
    schedule_enabled: false,
    schedule_interval: 1,
    schedule_unit: "days",
    created_at: "2026-04-01T10:00:00Z",
    updated_at: "2026-04-01T10:00:00Z",
    ...overrides,
  };
}

describe("ProposalInbox", () => {
  it("renders nothing when no proposals", () => {
    const { container } = renderWithProviders(
      <ProposalInbox proposals={[]} />,
    );
    expect(container.innerHTML).toBe("");
  });

  it("renders proposals with title and priority badge", () => {
    const proposals = [
      makeProposal({ id: "p-1", title: "Fix billing", priority: 2 }),
      makeProposal({ id: "p-2", title: "Add caching", priority: 5 }),
    ];
    renderWithProviders(<ProposalInbox proposals={proposals} />);

    expect(screen.getByText("PM proposals (2)")).toBeInTheDocument();
    expect(screen.getByText("Fix billing")).toBeInTheDocument();
    expect(screen.getByText("Add caching")).toBeInTheDocument();
    expect(screen.getByText("Priority 2")).toBeInTheDocument();
    expect(screen.getByText("Priority 5")).toBeInTheDocument();
  });

  it("shows seed task count", () => {
    const proposals = [makeProposal({ total_tasks: 5 })];
    renderWithProviders(<ProposalInbox proposals={proposals} />);

    expect(screen.getByText("5 seed tasks")).toBeInTheDocument();
  });

  it("shows singular task label for 1 task", () => {
    const proposals = [makeProposal({ total_tasks: 1 })];
    renderWithProviders(<ProposalInbox proposals={proposals} />);

    expect(screen.getByText("1 seed task")).toBeInTheDocument();
  });

  it("shows source issue count", () => {
    const proposals = [
      makeProposal({ source_issue_ids: ["i-1", "i-2", "i-3"] }),
    ];
    renderWithProviders(<ProposalInbox proposals={proposals} />);

    expect(screen.getByText("3 issues")).toBeInTheDocument();
  });

  it("shows similar project warning", () => {
    const proposals = [
      makeProposal({
        similar_projects: [
          {
            project_id: "existing-1",
            title: "Auth rewrite",
            overlap_score: 0.85,
            overlap_type: "goal",
            explanation: "Both target auth module",
          },
        ],
      }),
    ];
    renderWithProviders(<ProposalInbox proposals={proposals} />);

    expect(screen.getByText(/Similar to: Auth rewrite/)).toBeInTheDocument();
  });

  it("shows overflow count for multiple similar projects", () => {
    const proposals = [
      makeProposal({
        similar_projects: [
          {
            project_id: "e-1",
            title: "Auth rewrite",
            overlap_score: 0.85,
            overlap_type: "goal",
            explanation: "Both target auth",
          },
          {
            project_id: "e-2",
            title: "Security update",
            overlap_score: 0.6,
            overlap_type: "scope",
            explanation: "Overlapping scope",
          },
        ],
      }),
    ];
    renderWithProviders(<ProposalInbox proposals={proposals} />);

    expect(
      screen.getByText(/Similar to: Auth rewrite \+1 more/),
    ).toBeInTheDocument();
  });

  it("renders approve and dismiss buttons for each proposal", () => {
    const proposals = [makeProposal()];
    renderWithProviders(<ProposalInbox proposals={proposals} />);

    expect(screen.getByText("Approve")).toBeInTheDocument();
    expect(screen.getByText("Dismiss")).toBeInTheDocument();
    expect(screen.getByText("View details")).toBeInTheDocument();
  });

  it("opens detail sheet showing goal, scope, criteria, and reasoning", async () => {
    const user = userEvent.setup();
    const proposals = [
      makeProposal({
        title: "Refactor auth module",
        goal: "Improve auth security",
        scope: "All auth endpoints",
        completion_criteria: "All tests pass",
        proposal_reasoning: "Security audit flagged issues",
      }),
    ];

    server.use(
      http.get("/api/v1/projects/:id", () =>
        HttpResponse.json({ data: { ...proposals[0], tasks: [] } }),
      ),
    );

    renderWithProviders(<ProposalInbox proposals={proposals} />);
    await user.click(screen.getByText("View details"));

    await waitFor(() => {
      expect(screen.getByText("Improve auth security")).toBeInTheDocument();
    });
    expect(screen.getByText("All auth endpoints")).toBeInTheDocument();
    expect(screen.getByText("All tests pass")).toBeInTheDocument();
    expect(screen.getByText("Security audit flagged issues")).toBeInTheDocument();
    expect(screen.getByText("No tasks yet.")).toBeInTheDocument();
  });

  it("shows seed tasks in detail sheet", async () => {
    const user = userEvent.setup();
    const proposals = [makeProposal({ total_tasks: 2 })];

    server.use(
      http.get("/api/v1/projects/:id", () =>
        HttpResponse.json({
          data: {
            ...proposals[0],
            tasks: [
              { id: "t-1", title: "Task A", description: "Do A", complexity: "medium", confidence: "high" },
              { id: "t-2", title: "Task B", description: null, complexity: null, confidence: null },
            ],
          },
        }),
      ),
    );

    renderWithProviders(<ProposalInbox proposals={proposals} />);
    await user.click(screen.getByText("View details"));

    await waitFor(() => {
      expect(screen.getByText("Task A")).toBeInTheDocument();
    });
    expect(screen.getByText("Do A")).toBeInTheDocument();
    expect(screen.getByText("medium")).toBeInTheDocument();
    expect(screen.getByText("high confidence")).toBeInTheDocument();
    expect(screen.getByText("Task B")).toBeInTheDocument();
  });

  it("shows source issues and similar projects in detail sheet", async () => {
    const user = userEvent.setup();
    const proposals = [
      makeProposal({
        source_issue_ids: ["issue-abc", "issue-def"],
        similar_projects: [
          {
            project_id: "sp-1",
            title: "Auth rewrite",
            overlap_score: 0.85,
            overlap_type: "goal",
            explanation: "Both target auth module",
          },
        ],
      }),
    ];

    server.use(
      http.get("/api/v1/projects/:id", () =>
        HttpResponse.json({ data: { ...proposals[0], tasks: [] } }),
      ),
    );

    renderWithProviders(<ProposalInbox proposals={proposals} />);
    await user.click(screen.getByText("View details"));

    await waitFor(() => {
      expect(screen.getByText("Motivating issues")).toBeInTheDocument();
    });
    expect(screen.getByText("issue-abc")).toBeInTheDocument();
    expect(screen.getByText("issue-def")).toBeInTheDocument();
    expect(screen.getByText("Similar projects")).toBeInTheDocument();
    expect(screen.getByText("85% goal")).toBeInTheDocument();
    expect(screen.getByText("Both target auth module")).toBeInTheDocument();
  });

  it("shows approve and dismiss buttons in detail sheet", async () => {
    const user = userEvent.setup();
    const proposals = [makeProposal()];

    server.use(
      http.get("/api/v1/projects/:id", () =>
        HttpResponse.json({ data: { ...proposals[0], tasks: [] } }),
      ),
    );

    renderWithProviders(<ProposalInbox proposals={proposals} />);
    await user.click(screen.getByText("View details"));

    await waitFor(() => {
      expect(screen.getByText("Approve proposal")).toBeInTheDocument();
    });
    expect(screen.getByPlaceholderText("Reason for dismissal (optional)")).toBeInTheDocument();
  });
});
