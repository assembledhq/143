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
    expect(trigger).toHaveClass("type-dense", "font-medium", "h-8");
  });

  it("summarizes selected people in the trigger without rendering user badges", () => {
    const members: User[] = [
      {
        id: "user-1",
        org_id: "org-1",
        name: "Jane Doe",
        email: "jane@example.com",
        role: "member",
        created_at: "2026-01-01T00:00:00Z",
      },
      {
        id: "user-2",
        org_id: "org-1",
        name: "Grace Hopper",
        email: "grace@example.com",
        role: "member",
        created_at: "2026-01-01T00:00:00Z",
      },
      {
        id: "user-3",
        org_id: "org-1",
        name: "Margaret Hamilton",
        email: "margaret@example.com",
        role: "member",
        created_at: "2026-01-01T00:00:00Z",
      },
    ];

    const { rerender, container } = renderWithProviders(
      <PeopleFilter
        mode="custom"
        selectedUserIDs={["user-2"]}
        members={members}
        currentUser={members[0]}
        onFilterChange={vi.fn()}
      />,
    );

    expect(screen.getByRole("button", { name: /Grace Hopper/ })).toBeInTheDocument();
    expect(container.querySelector("[data-slot='badge']")).not.toBeInTheDocument();

    rerender(
      <PeopleFilter
        mode="custom"
        selectedUserIDs={["user-2", "user-3"]}
        members={members}
        currentUser={members[0]}
        onFilterChange={vi.fn()}
      />,
    );

    expect(screen.getByRole("button", { name: /Grace Hopper \+1 other/ })).toBeInTheDocument();
    expect(container.querySelector("[data-slot='badge']")).not.toBeInTheDocument();
  });
});
