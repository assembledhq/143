import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { StatusLabel } from "./status-label";

describe("StatusLabel", () => {
  it("renders status text independently of its color treatment", () => {
    render(<StatusLabel label="Ready" tone="success" detail="Expires in 20m" />);

    expect(screen.getByText("Ready")).toBeVisible();
    expect(screen.getByText("Expires in 20m")).toBeVisible();
  });

  it("shows a semantic activity icon for active states", () => {
    const { container } = render(<StatusLabel label="Starting" tone="primary" active />);

    expect(screen.getByText("Starting")).toBeVisible();
    expect(container.querySelector("svg")).toHaveClass("animate-spin");
  });
});
