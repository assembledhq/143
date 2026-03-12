import { beforeEach, describe, expect, it } from "vitest";
import { http, HttpResponse } from "msw";
import { renderWithProviders, screen, userEvent, waitFor, within } from "@/test/test-utils";
import { server } from "@/test/mocks/server";
import { AutomationsPageContent } from "./automations-page-content";

describe("AutomationsPageContent", () => {
  beforeEach(() => {
    window.localStorage.clear();
  });

  it("creates a manual automation and runs it on command", async () => {
    const user = userEvent.setup();
    let capturedBody: Record<string, unknown> | undefined;

    server.use(
      http.post("/api/v1/sessions/manual", async ({ request }) => {
        capturedBody = await request.json() as Record<string, unknown>;
        return HttpResponse.json(
          {
            data: {
              id: "session-1",
              type: "manual",
              status: "active",
              triggered_by: "manual",
              title: "Manual automation run",
              tasks: [],
              task_count: 0,
              active_run_count: 0,
              completed_run_count: 0,
              failed_run_count: 0,
              created_at: "2026-03-10T00:00:00Z",
            },
          },
          { status: 201 },
        );
      }),
    );

    renderWithProviders(<AutomationsPageContent />);

    await user.type(screen.getByLabelText("Automation Name"), "Flaky test sweeper");
    await user.type(screen.getByLabelText("Automation Instructions"), "Find flaky tests and stabilize them with deterministic assertions.");
    await user.click(screen.getByRole("button", { name: "Create Automation" }));

    const row = await screen.findByTestId("automation-row-flaky-test-sweeper");
    expect(within(row).getByText("Flaky test sweeper")).toBeInTheDocument();

    await user.click(within(row).getByRole("button", { name: "Run Now" }));

    await waitFor(() => {
      expect(capturedBody).toBeDefined();
      expect(capturedBody?.message).toContain("Find flaky tests");
    });
    expect(await screen.findByText("Last run succeeded")).toBeInTheDocument();
  });

  it("runs a PM automation template on command", async () => {
    const user = userEvent.setup();
    let pmAnalyzeCalls = 0;

    server.use(
      http.post("/api/v1/pm/analyze", () => {
        pmAnalyzeCalls += 1;
        return HttpResponse.json({ data: { job_id: "job-1" } });
      }),
    );

    renderWithProviders(<AutomationsPageContent />);

    const templateCard = screen.getByTestId("template-linear-triage");
    await user.click(within(templateCard).getByRole("button", { name: "Use Template" }));

    const row = await screen.findByTestId("automation-row-triage-linear-backlog");
    await user.click(within(row).getByRole("button", { name: "Run Now" }));

    await waitFor(() => {
      expect(pmAnalyzeCalls).toBe(1);
    });
    expect(await screen.findByText("PM analysis job queued")).toBeInTheDocument();
  });
});
