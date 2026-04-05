import { describe, expect, it, vi } from "vitest";
import { http, HttpResponse } from "msw";
import { renderWithProviders, screen, userEvent, waitFor } from "@/test/test-utils";
import { server } from "@/test/mocks/server";
import { AutopilotWeightsSheet } from "./autopilot-weights-sheet";

describe("AutopilotWeightsSheet", () => {
  it("disables save when weights do not sum to 1.0", async () => {
    const user = userEvent.setup();
    renderWithProviders(
      <AutopilotWeightsSheet
        open
        onOpenChange={vi.fn()}
        weights={{
          customer_impact: 0.35,
          severity: 0.25,
          recency: 0.2,
          revenue_risk: 0.2,
        }}
      />
    );

    await user.click(screen.getByRole("button", { name: "Customer impact increase" }));

    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
  });

  it("saves the weights payload when valid", async () => {
    let capturedBody: unknown;
    server.use(
      http.patch("/api/v1/settings", async ({ request }) => {
        capturedBody = await request.json();
        return HttpResponse.json({ data: { id: "org-1", name: "Org", settings: {} } });
      })
    );

    const user = userEvent.setup();
    renderWithProviders(
      <AutopilotWeightsSheet
        open
        onOpenChange={vi.fn()}
        weights={{
          customer_impact: 0.35,
          severity: 0.25,
          recency: 0.2,
          revenue_risk: 0.2,
        }}
      />
    );

    await user.click(screen.getByRole("button", { name: "Customer impact decrease" }));
    await user.click(screen.getByRole("button", { name: "Revenue risk increase" }));
    await user.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => {
      expect(capturedBody).toEqual({
        settings: {
          priority_weights: {
            customer_impact: 0.3,
            severity: 0.25,
            recency: 0.2,
            revenue_risk: 0.25,
          },
        },
      });
    });
  });
});
