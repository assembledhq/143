import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { SettingsPageFrame } from "./settings-page-frame";

describe("SettingsPageFrame", () => {
  it("uses narrow page container sizing", () => {
    render(
      <SettingsPageFrame title="General Settings" description="Manage your organization.">
        <div>Settings content</div>
      </SettingsPageFrame>
    );

    const container = screen.getByText("General Settings").closest("[data-slot='page-container']");
    expect(container).toHaveAttribute("data-size", "narrow");
  });

  it("renders title, description, and children", () => {
    render(
      <SettingsPageFrame title="Team" description="Manage members.">
        <div>Team content</div>
      </SettingsPageFrame>
    );

    expect(screen.getByText("Team")).toBeInTheDocument();
    expect(screen.getByText("Manage members.")).toBeInTheDocument();
    expect(screen.getByText("Team content")).toBeInTheDocument();
  });
});
