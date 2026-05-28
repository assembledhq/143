import { describe, it, expect, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ReviewToolbar } from "./review-toolbar";

function renderToolbar(overrides = {}) {
  const defaults = {
    viewMode: "unified" as const,
    onViewModeChange: vi.fn(),
    maximized: false,
    onToggleMaximize: vi.fn(),
    showFileTree: true,
    onToggleFileTree: vi.fn(),
  };
  const props = { ...defaults, ...overrides };
  return { ...render(<ReviewToolbar {...props} />), props };
}

describe("ReviewToolbar", () => {
  it("renders unified and split view mode buttons", () => {
    renderToolbar();
    expect(screen.getByText("Unified")).toBeInTheDocument();
    expect(screen.getByText("Split")).toBeInTheDocument();
  });

  it("calls onViewModeChange when split is clicked", async () => {
    const user = userEvent.setup();
    const { props } = renderToolbar();
    await user.click(screen.getByText("Split"));
    expect(props.onViewModeChange).toHaveBeenCalledWith("split");
  });

  it("calls onViewModeChange when unified is clicked", async () => {
    const user = userEvent.setup();
    const { props } = renderToolbar({ viewMode: "split" });
    await user.click(screen.getByText("Unified"));
    expect(props.onViewModeChange).toHaveBeenCalledWith("unified");
  });

  it("calls onToggleMaximize when maximize button is clicked", async () => {
    const user = userEvent.setup();
    const { props } = renderToolbar();
    await user.click(screen.getByTitle("Maximize"));
    expect(props.onToggleMaximize).toHaveBeenCalled();
  });

  it("shows Restore title when maximized", () => {
    renderToolbar({ maximized: true });
    expect(screen.getByTitle("Restore")).toBeInTheDocument();
  });

  it("calls onToggleFileTree when file tree button is clicked", async () => {
    const user = userEvent.setup();
    const { props } = renderToolbar();
    await user.click(screen.getByTitle("Hide file tree"));
    expect(props.onToggleFileTree).toHaveBeenCalled();
  });

  it("shows 'Show file tree' title when file tree is hidden", () => {
    renderToolbar({ showFileTree: false });
    expect(screen.getByTitle("Show file tree")).toBeInTheDocument();
  });

  it("renders Browse button when onBrowseRepo is provided", () => {
    renderToolbar({ onBrowseRepo: vi.fn() });
    expect(screen.getByText("Browse")).toBeInTheDocument();
  });

  it("calls onBrowseRepo when Browse is clicked", async () => {
    const user = userEvent.setup();
    const onBrowseRepo = vi.fn();
    renderToolbar({ onBrowseRepo });
    await user.click(screen.getByText("Browse"));
    expect(onBrowseRepo).toHaveBeenCalled();
  });

  it("does not render Browse button when onBrowseRepo is not provided", () => {
    renderToolbar();
    expect(screen.queryByText("Browse")).not.toBeInTheDocument();
  });

  it("shows search bar when search toggle is clicked", async () => {
    const user = userEvent.setup();
    renderToolbar({ onSearchChange: vi.fn(), searchQuery: "" });
    await user.click(screen.getByTitle("Search in diff (Ctrl+F)"));
    expect(screen.getByPlaceholderText("Search in diff...")).toBeInTheDocument();
  });

  it("uses the shared mobile-safe input for search", async () => {
    const user = userEvent.setup();
    renderToolbar({ onSearchChange: vi.fn(), searchQuery: "" });
    await user.click(screen.getByTitle("Search in diff (Ctrl+F)"));

    const input = screen.getByPlaceholderText("Search in diff...");
    expect(input).toHaveAttribute("data-slot", "input");
    expect(input).toHaveClass("max-sm:text-base");
  });

  it("calls onSearchChange when typing in search", async () => {
    const user = userEvent.setup();
    const onSearchChange = vi.fn();
    renderToolbar({ onSearchChange, searchQuery: "" });
    await user.click(screen.getByTitle("Search in diff (Ctrl+F)"));
    const input = screen.getByPlaceholderText("Search in diff...");
    await user.type(input, "hello");
    expect(onSearchChange).toHaveBeenCalled();
  });

  it("hides search and clears query when toggled off", async () => {
    const user = userEvent.setup();
    const onSearchChange = vi.fn();
    renderToolbar({ onSearchChange, searchQuery: "test" });
    // Click to open
    await user.click(screen.getByTitle("Search in diff (Ctrl+F)"));
    // Click to close
    await user.click(screen.getByTitle("Search in diff (Ctrl+F)"));
    expect(onSearchChange).toHaveBeenCalledWith("");
  });
});
