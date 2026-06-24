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

  it("renders the mobile file-reader controls when mobile props are provided", () => {
    renderToolbar({
      isMobile: true,
      filePath: "src/mobile.ts",
      filePositionLabel: "2 of 5",
      onOpenFileList: vi.fn(),
      onPrevFile: vi.fn(),
      onNextFile: vi.fn(),
      canGoPrev: true,
      canGoNext: true,
    });

    expect(screen.getByText("src/mobile.ts")).toBeInTheDocument();
    expect(screen.getByText("2 of 5")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Open files list" })).toBeInTheDocument();
    expect(screen.queryByText("Split")).not.toBeInTheDocument();
  });

  it("makes the mobile file path horizontally scrollable while keeping navigation controls fixed", () => {
    const longPath = "frontend/src/app/(dashboard)/settings/runtime/components/very-long-file-name-that-overflows-mobile.tsx";
    renderToolbar({
      isMobile: true,
      filePath: longPath,
      filePositionLabel: "2 of 5",
      onOpenFileList: vi.fn(),
      onPrevFile: vi.fn(),
      onNextFile: vi.fn(),
      canGoPrev: true,
      canGoNext: true,
    });

    const path = screen.getByText(longPath);
    expect(path).toHaveClass("overflow-x-auto", "whitespace-nowrap", "scrollbar-hide");
    expect(path).not.toHaveClass("truncate");
    expect(screen.getByRole("button", { name: "Previous file" })).toHaveClass("shrink-0");
    expect(screen.getByRole("button", { name: "Next file" })).toHaveClass("shrink-0");
  });

  it("keeps mobile controls icon-first and does not render desktop-style text actions", () => {
    renderToolbar({
      isMobile: true,
      filePath: "src/mobile.ts",
      filePositionLabel: "2 of 5",
      onOpenFileList: vi.fn(),
      onPrevFile: vi.fn(),
      onNextFile: vi.fn(),
      canGoPrev: true,
      canGoNext: true,
    });

    expect(screen.queryByText("Back to conversation")).not.toBeInTheDocument();
    expect(screen.queryByText("Unified")).not.toBeInTheDocument();
    expect(screen.queryByText("Split")).not.toBeInTheDocument();
  });

  it("renders the full screen toggle when onToggleFullScreen is provided", async () => {
    const user = userEvent.setup();
    const onToggleFullScreen = vi.fn();
    renderToolbar({ onToggleFullScreen });
    const button = screen.getByRole("button", { name: "Enter full screen" });
    await user.click(button);
    expect(onToggleFullScreen).toHaveBeenCalled();
  });

  it("labels the full screen toggle as an exit while in full screen", () => {
    renderToolbar({ onToggleFullScreen: vi.fn(), isFullScreen: true });
    expect(screen.getByRole("button", { name: "Exit full screen" })).toBeInTheDocument();
  });

  it("does not render the full screen toggle without onToggleFullScreen", () => {
    renderToolbar();
    expect(screen.queryByRole("button", { name: "Enter full screen" })).not.toBeInTheDocument();
  });

  it("does not render a dead mobile search control when search is unavailable", () => {
    renderToolbar({
      isMobile: true,
      filePath: "src/mobile.ts",
      filePositionLabel: "2 of 5",
      onOpenFileList: vi.fn(),
      onPrevFile: vi.fn(),
      onNextFile: vi.fn(),
      canGoPrev: true,
      canGoNext: true,
    });

    expect(screen.queryByRole("button", { name: "Search in diff" })).not.toBeInTheDocument();
  });
});
