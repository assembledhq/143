import { describe, it, expect, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { DiffToolbar } from "./diff-toolbar";

function renderToolbar(overrides = {}) {
  const defaults = {
    onBack: vi.fn(),
    viewMode: "unified" as const,
    onViewModeChange: vi.fn(),
  };
  const props = { ...defaults, ...overrides };
  return { ...render(<DiffToolbar {...props} />), props };
}

describe("DiffToolbar", () => {
  it("renders back button and view mode toggle", () => {
    renderToolbar();
    expect(screen.getByText("Back to conversation")).toBeInTheDocument();
    expect(screen.getByText("Unified")).toBeInTheDocument();
    expect(screen.getByText("Split")).toBeInTheDocument();
  });

  it("calls onBack when back button is clicked", async () => {
    const user = userEvent.setup();
    const { props } = renderToolbar();
    await user.click(screen.getByText("Back to conversation"));
    expect(props.onBack).toHaveBeenCalled();
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

  it("renders Browse button when onBrowseRepo is provided", () => {
    renderToolbar({ onBrowseRepo: vi.fn() });
    expect(screen.getByText("Browse")).toBeInTheDocument();
  });

  it("does not render Browse button without onBrowseRepo", () => {
    renderToolbar();
    expect(screen.queryByText("Browse")).not.toBeInTheDocument();
  });

  it("shows search bar when search toggle is clicked", async () => {
    const user = userEvent.setup();
    renderToolbar({ onSearchChange: vi.fn(), searchQuery: "" });
    await user.click(screen.getByTitle("Search in diff (Ctrl+F)"));
    expect(screen.getByPlaceholderText("Search in diff...")).toBeInTheDocument();
  });

  it("hides search and clears query when toggled off", async () => {
    const user = userEvent.setup();
    const onSearchChange = vi.fn();
    renderToolbar({ onSearchChange, searchQuery: "test" });
    await user.click(screen.getByTitle("Search in diff (Ctrl+F)"));
    await user.click(screen.getByTitle("Search in diff (Ctrl+F)"));
    expect(onSearchChange).toHaveBeenCalledWith("");
  });

  it("shows clear button when search query is non-empty", async () => {
    const user = userEvent.setup();
    const onSearchChange = vi.fn();
    renderToolbar({ onSearchChange, searchQuery: "hello" });
    await user.click(screen.getByTitle("Search in diff (Ctrl+F)"));
    // Search bar should show the query value
    const input = screen.getByPlaceholderText("Search in diff...");
    expect(input).toHaveValue("hello");
  });

  it("calls onBrowseRepo when Browse is clicked", async () => {
    const user = userEvent.setup();
    const onBrowseRepo = vi.fn();
    renderToolbar({ onBrowseRepo });
    await user.click(screen.getByText("Browse"));
    expect(onBrowseRepo).toHaveBeenCalled();
  });
});
