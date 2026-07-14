import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { ResourceRow } from "./resource-row";

describe("ResourceRow", () => {
  it("renders the operational resource hierarchy", () => {
    render(
      <ResourceRow
        title="PR #42"
        metadata="assembledhq/143 · abc1234"
        status="Ready"
        detail="Expires in 20m"
        actions={<button type="button">Open</button>}
      />,
    );

    expect(screen.getByText("PR #42")).toBeVisible();
    expect(screen.getByText("assembledhq/143 · abc1234")).toBeVisible();
    expect(screen.getByText("Ready")).toBeVisible();
    expect(screen.getByRole("button", { name: "Open" })).toBeVisible();
  });

  it("uses one selected-state background and leading indicator", () => {
    render(<ResourceRow title="Selected" selected data-testid="row" />);

    expect(screen.getByTestId("row")).toHaveClass("bg-accent/55");
    expect(screen.getByTestId("row")).toHaveAttribute("data-selected", "true");
  });
});
