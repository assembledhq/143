import { describe, expect, it, vi } from "vitest";
import { renderWithProviders, screen } from "@/test/test-utils";
import { PeopleFilter } from "./people-filter";
import type { User } from "@/lib/types";

describe("PeopleFilter", () => {
  it("uses the default button typography and sizing for the trigger", () => {
    const currentUser: User = {
      id: "user-1",
      org_id: "org-1",
      name: "Jane Doe",
      email: "jane@example.com",
      role: "member",
      created_at: "2026-01-01T00:00:00Z",
    };

    renderWithProviders(
      <PeopleFilter
        mode="mine"
        selectedUserIDs={["user-1"]}
        members={[currentUser]}
        currentUser={currentUser}
        onFilterChange={vi.fn()}
      />,
    );

    const trigger = screen.getByRole("button", { name: /mine/i });
    expect(trigger).toHaveAttribute("data-size", "default");
    expect(trigger).toHaveClass("text-xs", "font-medium", "h-8");
  });
});
