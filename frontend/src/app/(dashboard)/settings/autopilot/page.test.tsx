import { describe, expect, it } from "vitest";
import { http, HttpResponse } from "msw";
import { renderWithProviders, screen, userEvent, waitFor } from "@/test/test-utils";
import { server } from "@/test/mocks/server";
import AutopilotSettingsPage from "./page";

describe("AutopilotSettingsPage", () => {
  it("renders PM model and cadence controls without workspace steering fields", async () => {
    server.use(
      http.get("/api/v1/settings", () => HttpResponse.json({
        data: {
          id: "org-1",
          name: "Org",
          settings: {
            pm_schedule_hours: 4,
            pm_model: "claude-sonnet-4-5",
            default_agent_type: "codex",
            agent_config: {},
          },
          created_at: "2026-03-20T00:00:00Z",
          updated_at: "2026-03-20T00:00:00Z",
        },
      })),
      http.get("/api/v1/settings/agent-defaults", () => HttpResponse.json({ data: {} })),
      http.get("/api/v1/repositories", () => HttpResponse.json({ data: [], meta: {} }))
    );

    renderWithProviders(<AutopilotSettingsPage />);

    expect(await screen.findByLabelText("Schedule (hours)")).toBeInTheDocument();
    expect(screen.getByLabelText("PM model")).toBeInTheDocument();
    expect(screen.queryByText("Reference documents")).not.toBeInTheDocument();
    expect(screen.queryByText("Priority weights")).not.toBeInTheDocument();
  });

  it("saves PM cadence and model", async () => {
    let capturedBody: unknown;
    server.use(
      http.get("/api/v1/settings", () => HttpResponse.json({
        data: {
          id: "org-1",
          name: "Org",
          settings: {
            pm_schedule_hours: 4,
            pm_model: "claude-sonnet-4-5",
            default_agent_type: "codex",
            agent_config: {},
          },
          created_at: "2026-03-20T00:00:00Z",
          updated_at: "2026-03-20T00:00:00Z",
        },
      })),
      http.get("/api/v1/settings/agent-defaults", () => HttpResponse.json({ data: {} })),
      http.get("/api/v1/repositories", () => HttpResponse.json({ data: [], meta: {} })),
      http.patch("/api/v1/settings", async ({ request }) => {
        capturedBody = await request.json();
        return HttpResponse.json({ data: { id: "org-1", name: "Org", settings: {} } });
      })
    );

    const user = userEvent.setup();
    renderWithProviders(<AutopilotSettingsPage />);

    const scheduleInput = await screen.findByLabelText("Schedule (hours)");
    await user.clear(scheduleInput);
    await user.type(scheduleInput, "6");
    await user.click(screen.getByRole("button", { name: "Save settings" }));

    await waitFor(() => {
      expect(capturedBody).toEqual({
        settings: {
          pm_schedule_hours: 6,
          pm_model: "claude-sonnet-4-5",
        },
      });
    });
  });
});
