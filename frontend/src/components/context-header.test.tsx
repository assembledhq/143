import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { ContextHeader } from "./context-header";

describe("ContextHeader", () => {
  it("keeps workspace identity, status, actions, and tabs in one header", () => {
    render(
      <ContextHeader
        title={<h1>Fix checkout</h1>}
        status="Running"
        metadata="main · 2 files"
        actions={<button type="button">Details</button>}
        tabs={<div>Agent tabs</div>}
      />,
    );

    expect(screen.getByRole("heading", { name: "Fix checkout" })).toBeVisible();
    expect(screen.getByText("Running")).toBeVisible();
    expect(screen.getByText("main · 2 files")).toBeVisible();
    expect(screen.getByRole("button", { name: "Details" })).toBeVisible();
    expect(screen.getByText("Agent tabs")).toBeVisible();
  });
});
