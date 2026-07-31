import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it } from "vitest";
import {
  DataTableSummaryRow,
  type DataTableSummaryCell,
  type DataTableSummaryRowProps,
} from "@/components/data-table-summary-row";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";

const DEFAULT_CELLS: DataTableSummaryCell[] = [
  { content: "12", className: "text-right", ariaLabel: "12 total runs" },
  { content: "67%", className: "text-right" },
];

function renderSummaryRow(props: Partial<DataTableSummaryRowProps> = {}) {
  return render(
    <Table aria-label="Example usage">
      <TableHeader>
        <TableRow>
          <TableHead>Name</TableHead>
          <TableHead>Runs</TableHead>
          <TableHead>Success rate</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        <TableRow>
          <TableCell>Ada</TableCell>
          <TableCell>4</TableCell>
          <TableCell>75%</TableCell>
        </TableRow>
      </TableBody>
      <DataTableSummaryRow
        description="Across all results matching the current filters."
        cells={DEFAULT_CELLS}
        {...props}
      />
    </Table>,
  );
}

describe("DataTableSummaryRow", () => {
  it("renders an accessible, column-aligned table footer", () => {
    renderSummaryRow();

    const table = screen.getByRole("table", { name: "Example usage" });
    const overallRow = within(table).getByRole("row", { name: /Overall/i });

    expect(overallRow.closest("tfoot")).not.toBeNull();
    expect(within(overallRow).getByRole("rowheader", { name: /Overall/i })).toHaveAttribute("scope", "row");
    expect(within(overallRow).getByRole("cell", { name: "12 total runs" })).toHaveTextContent("12");
    expect(within(overallRow).getAllByRole("cell").map((cell) => cell.textContent)).toEqual(["12", "67%"]);
  });

  it("exposes the summary scope through a keyboard-reachable tooltip", async () => {
    const user = userEvent.setup();
    renderSummaryRow();

    const trigger = screen.getByRole("button", { name: "About this summary" });
    await user.tab();
    expect(trigger).toHaveFocus();

    const tooltip = await screen.findByRole("tooltip");
    expect(tooltip).toHaveTextContent("Across all results matching the current filters.");
  });

  it("supports overriding the label and rendering unavailable metrics", () => {
    renderSummaryRow({
      label: "All repositories",
      className: "border-t-2",
      cells: [
        { content: "—", ariaLabel: "No total runs data" },
        { content: "—" },
      ],
    });

    const overallRow = screen.getByRole("row", { name: /All repositories/i });
    expect(overallRow).toHaveClass("border-t-2");
    expect(screen.queryByRole("rowheader", { name: /Overall/i })).not.toBeInTheDocument();
    expect(within(overallRow).getByRole("cell", { name: "No total runs data" })).toHaveTextContent("—");
    expect(within(overallRow).getAllByRole("cell").map((cell) => cell.textContent)).toEqual(["—", "—"]);
  });
});
