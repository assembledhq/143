import { describe, it, expect, vi } from "vitest";
import PrioritizationPage from "./page";

const { redirectMock } = vi.hoisted(() => ({
  redirectMock: vi.fn(),
}));

vi.mock("next/navigation", () => ({
  redirect: redirectMock,
}));

describe("PrioritizationPage", () => {
  it("redirects to autopilot settings", () => {
    PrioritizationPage();

    expect(redirectMock).toHaveBeenCalledWith("/settings/autopilot");
  });
});
