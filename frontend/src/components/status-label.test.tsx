import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { StatusLabel } from "./status-label";

describe("StatusLabel", () => {
  it("renders status text independently of its color treatment", () => {
    render(<StatusLabel label="Ready" tone="success" detail="Expires in 20m" />);

    expect(screen.getByText("Ready")).toBeVisible();
    expect(screen.getByText("Expires in 20m")).toBeVisible();
  });

  it("shows an indeterminate activity icon for short-lived operations", () => {
    render(<StatusLabel label="Starting" tone="primary" activity="indeterminate" />);

    expect(screen.getByText("Starting")).toBeVisible();
    expect(screen.getByText("Starting").previousElementSibling).toHaveAttribute("data-activity", "indeterminate");
  });

  it("can render colored text without a dot", () => {
    const { container } = render(<StatusLabel label="Approved" tone="success" indicator="none" />);

    expect(screen.getByText("Approved")).toHaveClass("text-success");
    expect(container.querySelector('[aria-hidden="true"]')).toBeNull();
  });

  it("announces only statuses that opt into live updates", () => {
    const { rerender } = render(<StatusLabel label="Running" activity="breathing" />);
    expect(screen.getByText("Running").closest('[data-slot="status-label"]')).not.toHaveAttribute("role");

    rerender(<StatusLabel label="Waiting for you" tone="warning" announcement="polite" />);
    expect(screen.getByRole("status")).toHaveAttribute("aria-live", "polite");
  });
});
