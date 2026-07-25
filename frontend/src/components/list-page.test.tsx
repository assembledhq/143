import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { ListPage } from "./list-page";
import { Button } from "./ui/button";

describe("ListPage", () => {
  it("uses the canonical wide container, page header, and section rhythm", () => {
    render(
      <ListPage
        title="Automations"
        description="Recurring agents for the team."
        action={<Button>New automation</Button>}
      >
        <div>Page content</div>
      </ListPage>,
    );

    const page = screen.getByText("Page content").closest('[data-slot="list-page"]');
    const container = page?.closest('[data-slot="page-container"]');

    expect(page).toHaveClass("space-y-6");
    expect(container).toHaveAttribute("data-size", "wide");
    expect(container).toHaveClass("max-w-7xl", "mx-auto");
    expect(screen.getByRole("heading", { name: "Automations" })).toBeInTheDocument();
    expect(screen.getByText("Recurring agents for the team.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "New automation" })).toBeInTheDocument();
  });
});
