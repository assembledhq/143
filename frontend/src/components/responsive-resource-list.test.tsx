import { render, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import {
  ResponsiveResourceList,
  type ResponsiveResourceListColumn,
} from "./responsive-resource-list";

type TestResource = {
  id: string;
  name: string;
  status: string;
};

const columns: ResponsiveResourceListColumn<TestResource>[] = [
  {
    id: "name",
    header: "Name",
    render: (item) => item.name,
  },
  {
    id: "status",
    header: "Status",
    render: (item) => item.status,
  },
];

describe("ResponsiveResourceList", () => {
  it("shares one card, table header, desktop rows, and labeled mobile list", () => {
    render(
      <ResponsiveResourceList
        ariaLabel="Automations"
        mobileAriaLabel="Automations mobile list"
        items={[{ id: "automation-1", name: "Release check", status: "Enabled" }]}
        getItemKey={(item) => item.id}
        columns={columns}
        emptyState="No automations."
        tableClassName="min-w-[64rem]"
        getDesktopRowProps={(item) => ({ "aria-label": `Desktop ${item.name}` })}
        renderMobileItem={(item) => <div>Mobile {item.name}</div>}
      />,
    );

    const table = screen.getByRole("table", { name: "Automations" });
    expect(table).toHaveClass("min-w-[64rem]");
    expect(within(table).getByRole("columnheader", { name: "Name" })).toBeInTheDocument();
    expect(within(table).getByRole("columnheader", { name: "Status" })).toBeInTheDocument();
    expect(within(table).getByRole("row", { name: "Desktop Release check" })).toBeInTheDocument();

    const mobileList = screen.getByRole("list", { name: "Automations mobile list" });
    expect(within(mobileList).getByText("Mobile Release check")).toBeInTheDocument();
    expect(within(mobileList).getByRole("listitem")).toBeInTheDocument();
    expect(table.closest('[data-slot="card"]')).toBe(mobileList.closest('[data-slot="card"]'));
  });

  it("uses the shared card treatment for empty resource lists", () => {
    render(
      <ResponsiveResourceList
        ariaLabel="Automations"
        items={[]}
        getItemKey={(item: TestResource) => item.id}
        columns={columns}
        emptyState="No automations."
        renderMobileItem={(item) => <div>{item.name}</div>}
      />,
    );

    const emptyState = screen.getByText("No automations.");
    expect(emptyState.closest('[data-slot="card"]')).toHaveClass(
      "rounded-xl",
      "bg-surface-recessed/45",
    );
    expect(screen.queryByRole("table")).not.toBeInTheDocument();
  });
});
