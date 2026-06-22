import { describe, expect, it, vi } from "vitest";
import { http, HttpResponse } from "msw";
import { fireEvent } from "@testing-library/react";
import {
  renderWithProviders,
  screen,
  userEvent,
  waitFor,
} from "@/test/test-utils";
import { server } from "@/test/mocks/server";
import { AutomationGoalImprovementControl } from "./automation-goal-improvement";

describe("AutomationGoalImprovementControl", () => {
  it("disables draft deep improvement until a repository is selected", async () => {
    const user = userEvent.setup();

    renderWithProviders(
      <AutomationGoalImprovementControl name="Tests" goal="Run tests" />,
    );

    await user.click(screen.getByLabelText("Goal improvement options"));

    expect(screen.getByText("Deep improve with agent")).toHaveAttribute(
      "data-disabled",
    );
  });

  it("debounces repeated improve clicks", async () => {
    let calls = 0;

    server.use(
      http.post("/api/v1/automations/goal-improvements", () => {
        calls += 1;
        return HttpResponse.json({
          data: {
            id: "imp-1",
            org_id: "org-1",
            mode: "fast",
            status: "completed",
            input_goal: "Run tests",
            base_goal_hash: "sha256:abc",
            proposed_goal: "Run tests with evidence.",
            proposal: { changes: ["Added evidence."] },
            confidence: "medium",
            warnings: [],
            created_at: "2026-06-18T00:00:00Z",
            updated_at: "2026-06-18T00:00:00Z",
          },
        });
      }),
    );

    renderWithProviders(
      <AutomationGoalImprovementControl
        name="Tests"
        goal="Run tests"
        repositoryId="repo-1"
      />,
    );

    const button = screen.getByRole("button", { name: /improve goal/i });
    fireEvent.click(button);
    fireEvent.click(button);

    await screen.findByText("Review improved goal");
    expect(calls).toBe(1);
  });

  it("applies a draft fast improvement to local goal state", async () => {
    const user = userEvent.setup();
    const onDraftApply = vi.fn();

    server.use(
      http.post(
        "/api/v1/automations/goal-improvements",
        async ({ request }) => {
          const body = (await request.json()) as { goal: string; mode: string };
          expect(body.mode).toBe("fast");
          expect(body.goal).toBe("Run tests");
          return HttpResponse.json({
            data: {
              id: "imp-1",
              org_id: "org-1",
              mode: "fast",
              status: "completed",
              input_goal: "Run tests",
              base_goal_hash: "sha256:abc",
              proposed_goal:
                "Run the focused test suite and report failures with evidence.",
              proposal: {
                rationale: "The original goal was too terse.",
                changes: ["Added evidence requirements."],
                evidence: ["Draft goal only."],
                risks: [],
              },
              confidence: "medium",
              warnings: [],
              created_at: "2026-06-18T00:00:00Z",
              updated_at: "2026-06-18T00:00:00Z",
            },
          });
        },
      ),
    );

    renderWithProviders(
      <AutomationGoalImprovementControl
        name="Tests"
        goal="Run tests"
        repositoryId="repo-1"
        onDraftApply={onDraftApply}
      />,
    );

    await user.click(screen.getByRole("button", { name: /improve goal/i }));
    expect(await screen.findByText("Review improved goal")).toBeInTheDocument();
    const revisedGoal = screen.getByLabelText("Revised goal");
    expect(revisedGoal).toHaveValue(
      "Run the focused test suite and report failures with evidence.",
    );
    await user.clear(revisedGoal);
    await user.type(revisedGoal, "Run only changed tests and summarize failures.");

    await user.click(screen.getByRole("button", { name: "Apply" }));

    await waitFor(() => {
      expect(onDraftApply).toHaveBeenCalledWith(
        "Run only changed tests and summarize failures.",
      );
    });
  });

  it("keeps review details collapsed by default", async () => {
    const user = userEvent.setup();

    server.use(
      http.post("/api/v1/automations/goal-improvements", () =>
        HttpResponse.json({
          data: {
            id: "imp-1",
            org_id: "org-1",
            mode: "fast",
            status: "completed",
            input_goal: "Run tests",
            base_goal_hash: "sha256:abc",
            proposed_goal: "Run tests with concise evidence.",
            proposal: {
              rationale: "The original goal was too terse.",
              changes: ["Added evidence requirements."],
            },
            confidence: "medium",
            warnings: ["No repository evidence was available."],
            created_at: "2026-06-18T00:00:00Z",
            updated_at: "2026-06-18T00:00:00Z",
          },
        }),
      ),
    );

    renderWithProviders(
      <AutomationGoalImprovementControl
        name="Tests"
        goal="Run tests"
        repositoryId="repo-1"
      />,
    );

    await user.click(screen.getByRole("button", { name: /improve goal/i }));
    expect(await screen.findByLabelText("Revised goal")).toBeInTheDocument();
    expect(screen.queryByText("Current goal")).not.toBeInTheDocument();
    expect(screen.queryByText("Proposed changes")).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Show review notes" }));

    expect(await screen.findByText("Current goal")).toBeInTheDocument();
    expect(screen.getByText("Proposed changes")).toBeInTheDocument();
  });

  it("disables Apply when the revised goal textarea is cleared", async () => {
    const user = userEvent.setup();

    server.use(
      http.post("/api/v1/automations/goal-improvements", () =>
        HttpResponse.json({
          data: {
            id: "imp-1",
            org_id: "org-1",
            mode: "fast",
            status: "completed",
            input_goal: "Run tests",
            base_goal_hash: "sha256:abc",
            proposed_goal: "Run tests with evidence.",
            proposal: { changes: ["Added evidence."] },
            confidence: "medium",
            warnings: [],
            created_at: "2026-06-18T00:00:00Z",
            updated_at: "2026-06-18T00:00:00Z",
          },
        }),
      ),
    );

    renderWithProviders(
      <AutomationGoalImprovementControl
        name="Tests"
        goal="Run tests"
        repositoryId="repo-1"
      />,
    );

    await user.click(screen.getByRole("button", { name: /improve goal/i }));
    const revisedGoal = await screen.findByLabelText("Revised goal");
    expect(screen.getByRole("button", { name: "Apply" })).not.toBeDisabled();

    await user.clear(revisedGoal);

    expect(screen.getByRole("button", { name: "Apply" })).toBeDisabled();

    await user.type(revisedGoal, "Run only changed tests.");
    expect(screen.getByRole("button", { name: "Apply" })).not.toBeDisabled();
  });

  it("shows stale-goal guidance when saved apply is rejected", async () => {
    const user = userEvent.setup();
    const onSavedApply = vi.fn();

    server.use(
      http.post("/api/v1/automations/auto-1/goal-improvements", () =>
        HttpResponse.json({
          data: {
            id: "imp-1",
            org_id: "org-1",
            automation_id: "auto-1",
            mode: "fast",
            status: "completed",
            input_goal: "Run tests",
            base_goal_hash: "sha256:old",
            proposed_goal: "Run tests and summarize evidence.",
            proposal: { changes: ["Added output requirements."] },
            confidence: "medium",
            warnings: [],
            created_at: "2026-06-18T00:00:00Z",
            updated_at: "2026-06-18T00:00:00Z",
          },
        }),
      ),
      http.get("/api/v1/automations/goal-improvements/imp-deep", () =>
        HttpResponse.json({
          data: {
            id: "imp-deep",
            org_id: "org-1",
            automation_id: "auto-1",
            repository_id: "repo-1",
            mode: "deep",
            status: "running",
            input_goal: "Run tests",
            base_goal_hash: "sha256:abc",
            warnings: [],
            analysis_session_id: "session-1",
            created_at: "2026-06-18T00:00:00Z",
            updated_at: "2026-06-18T00:00:00Z",
          },
        }),
      ),
      http.post(
        "/api/v1/automations/auto-1/goal-improvements/imp-1/apply",
        () =>
          HttpResponse.json(
            {
              error: {
                code: "STALE_GOAL",
                message:
                  "automation goal changed since this improvement was generated",
              },
            },
            { status: 409 },
          ),
      ),
    );

    renderWithProviders(
      <AutomationGoalImprovementControl
        automationId="auto-1"
        name="Tests"
        goal="Run tests"
        repositoryId="repo-1"
        onSavedApply={onSavedApply}
      />,
    );

    await user.click(screen.getByRole("button", { name: /improve goal/i }));
    expect(await screen.findByText("Review improved goal")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Apply" }));

    expect(
      await screen.findByText(/Regenerate from the current goal/i),
    ).toBeInTheDocument();
    expect(onSavedApply).not.toHaveBeenCalled();
  });

  it("polls a running deep improvement and shows the completed proposal", async () => {
    const user = userEvent.setup();
    server.use(
      http.post("/api/v1/automations/auto-1/goal-improvements", () =>
        HttpResponse.json({
          data: {
            id: "imp-deep",
            org_id: "org-1",
            automation_id: "auto-1",
            repository_id: "repo-1",
            mode: "deep",
            status: "running",
            input_goal: "Run tests",
            base_goal_hash: "sha256:abc",
            warnings: [],
            analysis_session_id: "session-1",
            created_at: "2026-06-18T00:00:00Z",
            updated_at: "2026-06-18T00:00:00Z",
          },
        }),
      ),
      http.get("/api/v1/automations/goal-improvements/imp-deep", () =>
        HttpResponse.json({
          data: {
            id: "imp-deep",
            org_id: "org-1",
            automation_id: "auto-1",
            repository_id: "repo-1",
            mode: "deep",
            status: "completed",
            input_goal: "Run tests",
            base_goal_hash: "sha256:abc",
            proposed_goal:
              "Run the repository test suite and summarize failures with logs.",
            proposal: { changes: ["Added repository evidence."] },
            confidence: "high",
            warnings: [],
            analysis_session_id: "session-1",
            created_at: "2026-06-18T00:00:00Z",
            updated_at: "2026-06-18T00:00:00Z",
          },
        }),
      ),
    );

    renderWithProviders(
      <AutomationGoalImprovementControl
        automationId="auto-1"
        name="Tests"
        goal="Run tests"
        repositoryId="repo-1"
      />,
    );

    await user.click(screen.getByLabelText("Goal improvement options"));
    await user.click(screen.getByText("Deep improve with agent"));

    expect(
      await screen.findByDisplayValue(/repository test suite/i),
    ).toBeInTheDocument();
  });

  it("cancels a running deep improvement", async () => {
    const user = userEvent.setup();
    server.use(
      http.post("/api/v1/automations/auto-1/goal-improvements", () =>
        HttpResponse.json({
          data: {
            id: "imp-deep",
            org_id: "org-1",
            automation_id: "auto-1",
            repository_id: "repo-1",
            mode: "deep",
            status: "running",
            input_goal: "Run tests",
            base_goal_hash: "sha256:abc",
            warnings: [],
            analysis_session_id: "session-1",
            created_at: "2026-06-18T00:00:00Z",
            updated_at: "2026-06-18T00:00:00Z",
          },
        }),
      ),
      http.get("/api/v1/automations/goal-improvements/imp-deep", () =>
        HttpResponse.json({
          data: {
            id: "imp-deep",
            org_id: "org-1",
            automation_id: "auto-1",
            repository_id: "repo-1",
            mode: "deep",
            status: "running",
            input_goal: "Run tests",
            base_goal_hash: "sha256:abc",
            warnings: [],
            analysis_session_id: "session-1",
            created_at: "2026-06-18T00:00:00Z",
            updated_at: "2026-06-18T00:00:00Z",
          },
        }),
      ),
      http.get("/api/v1/sessions/session-1", () =>
        HttpResponse.json({
          data: {
            id: "session-1",
            org_id: "org-1",
            agent_type: "codex",
            status: "running",
            autonomy_level: "supervised",
            token_mode: "low",
            current_turn: 1,
            last_activity_at: "2026-06-18T00:00:00Z",
            sandbox_state: "ready",
          },
        }),
      ),
      http.get("/api/v1/sessions/session-1/threads", () =>
        HttpResponse.json({
          data: [
            {
              id: "thread-1",
              session_id: "session-1",
              org_id: "org-1",
              agent_type: "codex",
              label: "Main",
              status: "running",
              current_turn: 1,
              created_at: "2026-06-18T00:00:00Z",
              cost_cents: 0,
              pending_message_count: 0,
            },
          ],
          meta: {},
        }),
      ),
      http.get("/api/v1/sessions/session-1/threads/thread-1/transcript", () =>
        HttpResponse.json({
          data: [
            {
              turn_number: 1,
              started_at: "2026-06-18T00:00:00Z",
              entries: [
                {
                  id: "entry-1",
                  kind: "log",
                  created_at: "2026-06-18T00:00:00Z",
                  summary: "Inspecting repository test conventions",
                },
              ],
            },
          ],
          meta: {
            position: "latest",
            has_older: false,
            has_newer: false,
            thread_status: "running",
          },
        }),
      ),
      http.post("/api/v1/automations/goal-improvements/imp-deep/cancel", () =>
        HttpResponse.json({
          data: {
            id: "imp-deep",
            org_id: "org-1",
            automation_id: "auto-1",
            repository_id: "repo-1",
            mode: "deep",
            status: "canceled",
            input_goal: "Run tests",
            base_goal_hash: "sha256:abc",
            warnings: [],
            error_message: "proposal was canceled by the user",
            analysis_session_id: "session-1",
            created_at: "2026-06-18T00:00:00Z",
            updated_at: "2026-06-18T00:00:00Z",
          },
        }),
      ),
    );

    renderWithProviders(
      <AutomationGoalImprovementControl
        automationId="auto-1"
        name="Tests"
        goal="Run tests"
        repositoryId="repo-1"
      />,
    );

    await user.click(screen.getByLabelText("Goal improvement options"));
    await user.click(screen.getByText("Deep improve with agent"));
    expect(await screen.findByText("Session status: running")).toBeInTheDocument();
    expect(
      await screen.findByText("Inspecting repository test conventions"),
    ).toBeInTheDocument();
    await user.click(await screen.findByRole("button", { name: "Cancel" }));

    expect(
      await screen.findByText("proposal was canceled by the user"),
    ).toBeInTheDocument();
  });

  it("opens proposal history and selects an existing proposal", async () => {
    const user = userEvent.setup();
    server.use(
      http.get("/api/v1/automations/auto-1/goal-improvements", () =>
        HttpResponse.json({
          data: [
            {
              id: "imp-history",
              org_id: "org-1",
              automation_id: "auto-1",
              mode: "fast",
              status: "completed",
              input_goal: "Run tests",
              base_goal_hash: "sha256:abc",
              proposed_goal: "Run tests and include logs.",
              proposal: { changes: ["Added logs."] },
              confidence: "medium",
              warnings: [],
              created_at: "2026-06-18T00:00:00Z",
              updated_at: "2026-06-18T00:00:00Z",
            },
          ],
        }),
      ),
    );

    renderWithProviders(
      <AutomationGoalImprovementControl
        automationId="auto-1"
        name="Tests"
        goal="Run tests"
        repositoryId="repo-1"
      />,
    );

    await user.click(screen.getByLabelText("Goal improvement history"));
    expect(
      await screen.findByText("Goal improvement history"),
    ).toBeInTheDocument();
    await user.click(await screen.findByText("completed"));

    expect(
      await screen.findByDisplayValue(/include logs/i),
    ).toBeInTheDocument();
  });
});
