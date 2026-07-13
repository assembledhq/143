import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { SectionGroup } from "./section-group";

describe("SectionGroup", () => {
  it("groups a heading, description, action, and content", () => {
    render(
      <SectionGroup title="Runtime" description="Control sandbox behavior." action={<button type="button">Edit</button>}>
        Settings
      </SectionGroup>,
    );

    expect(screen.getByRole("heading", { name: "Runtime" })).toBeVisible();
    expect(screen.getByText("Control sandbox behavior.")).toBeVisible();
    expect(screen.getByRole("button", { name: "Edit" })).toBeVisible();
    expect(screen.getByText("Settings")).toBeVisible();
  });

  it("accepts composed title content without colliding with the native title attribute", () => {
    render(
      <SectionGroup title={<span>Running <strong>3</strong></span>}>
        Rows
      </SectionGroup>,
    );

    expect(screen.getByRole("heading", { name: "Running 3" })).toBeVisible();
  });
});
