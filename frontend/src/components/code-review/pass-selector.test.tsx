import { describe, it, expect, vi } from "vitest";
import { renderWithProviders, screen, userEvent } from "@/test/test-utils";
import { PassSelector, type DiffPassEntry } from "./pass-selector";

const mockPasses: DiffPassEntry[] = [
  {
    pass: 1,
    diff: "diff --git a/f.ts b/f.ts\n+line1",
    diff_stats: { added: 10, removed: 2, files_changed: 3 },
    created_at: "2026-03-19T10:00:00Z",
  },
  {
    pass: 2,
    diff: "diff --git a/f.ts b/f.ts\n+line1\n+line2",
    diff_stats: { added: 15, removed: 5, files_changed: 4 },
    created_at: "2026-03-19T10:05:00Z",
  },
  {
    pass: 3,
    diff: "diff --git a/f.ts b/f.ts\n+line1\n+line2\n+line3",
    diff_stats: { added: 20, removed: 8, files_changed: 5 },
    created_at: "2026-03-19T10:10:00Z",
  },
];

describe("PassSelector", () => {
  it("renders nothing with fewer than 2 passes", () => {
    const { container } = renderWithProviders(
      <PassSelector
        passes={[mockPasses[0]]}
        selectedRange={null}
        onRangeChange={() => {}}
      />
    );
    expect(container.innerHTML).toBe("");
  });

  it("renders button showing 'All changes' when no range is selected", () => {
    renderWithProviders(
      <PassSelector
        passes={mockPasses}
        selectedRange={null}
        onRangeChange={() => {}}
      />
    );
    expect(screen.getByText("All changes")).toBeInTheDocument();
  });

  it("renders button showing range label when range is selected", () => {
    renderWithProviders(
      <PassSelector
        passes={mockPasses}
        selectedRange={{ from: 1, to: 2 }}
        onRangeChange={() => {}}
      />
    );
    expect(screen.getByText("Pass 1 → Pass 2")).toBeInTheDocument();
  });

  it("shows 'Base → Pass N' label for base ranges", () => {
    renderWithProviders(
      <PassSelector
        passes={mockPasses}
        selectedRange={{ from: 0, to: 2 }}
        onRangeChange={() => {}}
      />
    );
    expect(screen.getByText("Base → Pass 2")).toBeInTheDocument();
  });

  it("opens dropdown on click and shows options", async () => {
    const user = userEvent.setup();
    renderWithProviders(
      <PassSelector
        passes={mockPasses}
        selectedRange={null}
        onRangeChange={() => {}}
      />
    );

    await user.click(screen.getByText("All changes"));

    // Should see the section header
    expect(screen.getByText("Compare passes")).toBeInTheDocument();
    // Should see consecutive pass ranges
    expect(screen.getByText("Pass 1 → Pass 2")).toBeInTheDocument();
    expect(screen.getByText("Pass 2 → Pass 3")).toBeInTheDocument();
  });

  it("calls onRangeChange with selected range", async () => {
    const user = userEvent.setup();
    const onRangeChange = vi.fn();

    renderWithProviders(
      <PassSelector
        passes={mockPasses}
        selectedRange={null}
        onRangeChange={onRangeChange}
      />
    );

    await user.click(screen.getByText("All changes"));
    await user.click(screen.getByText("Pass 1 → Pass 2"));

    expect(onRangeChange).toHaveBeenCalledWith({ from: 1, to: 2 });
  });

  it("calls onRangeChange with null for 'All changes'", async () => {
    const user = userEvent.setup();
    const onRangeChange = vi.fn();

    renderWithProviders(
      <PassSelector
        passes={mockPasses}
        selectedRange={{ from: 1, to: 2 }}
        onRangeChange={onRangeChange}
      />
    );

    await user.click(screen.getByText("Pass 1 → Pass 2"));
    // Find the "All changes" option in the dropdown (different from the button)
    const allChangesOptions = screen.getAllByText("All changes");
    await user.click(allChangesOptions[allChangesOptions.length - 1]);

    expect(onRangeChange).toHaveBeenCalledWith(null);
  });

  it("shows 'Base → Pass N' options for 3+ passes", async () => {
    const user = userEvent.setup();
    renderWithProviders(
      <PassSelector
        passes={mockPasses}
        selectedRange={null}
        onRangeChange={() => {}}
      />
    );

    await user.click(screen.getByText("All changes"));
    expect(screen.getByText("Base → Pass 1")).toBeInTheDocument();
    expect(screen.getByText("Base → Pass 2")).toBeInTheDocument();
    expect(screen.getByText("Base → Pass 3")).toBeInTheDocument();
  });

  it("does not show 'Base → Pass N' options for exactly 2 passes", async () => {
    const user = userEvent.setup();
    const twoPasses = mockPasses.slice(0, 2);
    renderWithProviders(
      <PassSelector
        passes={twoPasses}
        selectedRange={null}
        onRangeChange={() => {}}
      />
    );

    await user.click(screen.getByText("All changes"));
    expect(screen.queryByText("Base → Pass 1")).not.toBeInTheDocument();
  });
});
