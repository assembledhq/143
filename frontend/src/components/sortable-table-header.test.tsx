import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { SortableTableHeader, nextSortDirection, sortDirectionAriaValue } from "./sortable-table-header";

describe("SortableTableHeader", () => {
  it.each([
    { direction: false as const, expected: "asc" as const },
    { direction: "asc" as const, expected: "desc" as const },
    { direction: "desc" as const, expected: "asc" as const },
  ])("requests $expected after $direction", async ({ direction, expected }) => {
    const user = userEvent.setup();
    const onSort = vi.fn();
    render(<SortableTableHeader label="Repository" direction={direction} onSort={onSort} />);

    const button = screen.getByRole("button", {
      name: `Sort by Repository ${expected === "asc" ? "ascending" : "descending"}`,
    });
    expect(button).toHaveAttribute("data-sort-direction", direction || "none");
    await user.click(button);
    expect(onSort).toHaveBeenCalledWith(expected);
  });

  it("returns to the table's default order on the third click when unsorting is allowed", async () => {
    const user = userEvent.setup();
    const onSort = vi.fn();
    render(<SortableTableHeader label="Repository" direction="desc" allowUnsorted onSort={onSort} />);

    await user.click(screen.getByRole("button", { name: "Stop sorting by Repository" }));

    expect(onSort).toHaveBeenCalledWith(false);
  });

  it("exposes the shared direction cycle for table adapters", () => {
    expect(nextSortDirection(false)).toBe("asc");
    expect(nextSortDirection("asc")).toBe("desc");
    expect(nextSortDirection("desc")).toBe("asc");
    // Tables with their own default order cycle back to it instead.
    expect(nextSortDirection(false, true)).toBe("asc");
    expect(nextSortDirection("asc", true)).toBe("desc");
    expect(nextSortDirection("desc", true)).toBe(false);
    expect(sortDirectionAriaValue(false)).toBe("none");
    expect(sortDirectionAriaValue("asc")).toBe("ascending");
    expect(sortDirectionAriaValue("desc")).toBe("descending");
  });
});
