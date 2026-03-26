import { describe, expect, it, vi } from "vitest";
import { http, HttpResponse } from "msw";
import { renderWithProviders, screen, userEvent, waitFor } from "@/test/test-utils";
import { server } from "@/test/mocks/server";
import { AutopilotSteeringSheet } from "./autopilot-steering-sheet";
import type { OrgSettings } from "@/lib/types";

const baseSettings: OrgSettings = {
  autonomy_level: "auto_simple",
  product_direction: "Payments hardening this quarter.",
  product_context: {
    philosophy: "Ship reliability first.",
    direction: "Payments hardening this quarter.",
    focus_areas: ["auth"],
    avoid_areas: ["redesigns"],
  },
};

describe("AutopilotSteeringSheet", () => {
  it("renders the current steering fields", async () => {
    renderWithProviders(
      <AutopilotSteeringSheet open onOpenChange={vi.fn()} settings={baseSettings} />
    );

    expect(screen.getByText("Edit direction")).toBeInTheDocument();
    expect(screen.getByDisplayValue("Ship reliability first.")).toBeInTheDocument();
    expect(screen.getByDisplayValue("Payments hardening this quarter.")).toBeInTheDocument();
    expect(screen.getByDisplayValue("auth")).toBeInTheDocument();
  });

  it("saves the steering settings payload and closes on success", async () => {
    let capturedBody: unknown;
    const onOpenChange = vi.fn();
    server.use(
      http.patch("/api/v1/settings", async ({ request }) => {
        capturedBody = await request.json();
        return HttpResponse.json({ data: { id: "org-1", name: "Org", settings: {} } });
      })
    );

    const user = userEvent.setup();
    renderWithProviders(
      <AutopilotSteeringSheet open onOpenChange={onOpenChange} settings={baseSettings} />
    );

    await user.clear(screen.getByLabelText("Philosophy"));
    await user.type(screen.getByLabelText("Philosophy"), "Reliability first, then speed.");
    await user.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => {
      expect(capturedBody).toEqual({
        settings: {
          autonomy_level: "auto_simple",
          product_direction: "Payments hardening this quarter.",
          product_context: {
            philosophy: "Reliability first, then speed.",
            direction: "Payments hardening this quarter.",
            focus_areas: ["auth"],
            avoid_areas: ["redesigns"],
          },
        },
      });
    });

    expect(onOpenChange).toHaveBeenCalledWith(false);
  });
});
