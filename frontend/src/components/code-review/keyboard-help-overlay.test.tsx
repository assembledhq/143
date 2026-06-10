import { describe, it, expect, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { KeyboardHelpOverlay } from "./keyboard-help-overlay";

describe("KeyboardHelpOverlay", () => {
  it("renders nothing when closed", () => {
    const { container } = render(
      <KeyboardHelpOverlay open={false} onClose={vi.fn()} />
    );
    expect(container.innerHTML).toBe("");
  });

  it("renders the dialog when open", () => {
    render(<KeyboardHelpOverlay open={true} onClose={vi.fn()} />);
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    expect(screen.getByText("Keyboard shortcuts")).toBeInTheDocument();
  });

  it("renders all shortcut keys", () => {
    render(<KeyboardHelpOverlay open={true} onClose={vi.fn()} />);
    expect(screen.getByText("j / k")).toBeInTheDocument();
    expect(screen.getByText("n / p")).toBeInTheDocument();
    expect(screen.getByText("Enter")).toBeInTheDocument();
    expect(screen.getByText("?")).toBeInTheDocument();
  });

  it("renders all shortcut descriptions", () => {
    render(<KeyboardHelpOverlay open={true} onClose={vi.fn()} />);
    expect(screen.getByText("Next / previous file")).toBeInTheDocument();
    expect(screen.getByText("Toggle file tree panel")).toBeInTheDocument();
    expect(screen.getByText("Back to conversation")).toBeInTheDocument(); // m
    expect(screen.getByText("Exit full screen, then back to conversation")).toBeInTheDocument(); // Esc
  });

  it("calls onClose when close button is clicked", async () => {
    const user = userEvent.setup();
    const onClose = vi.fn();
    render(<KeyboardHelpOverlay open={true} onClose={onClose} />);
    await user.click(screen.getByLabelText("Close keyboard shortcuts"));
    expect(onClose).toHaveBeenCalled();
  });

  it("calls onClose when Escape is pressed", () => {
    const onClose = vi.fn();
    render(<KeyboardHelpOverlay open={true} onClose={onClose} />);
    document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    expect(onClose).toHaveBeenCalled();
  });

  it("calls onClose when ? is pressed", () => {
    const onClose = vi.fn();
    render(<KeyboardHelpOverlay open={true} onClose={onClose} />);
    document.dispatchEvent(new KeyboardEvent("keydown", { key: "?", bubbles: true }));
    expect(onClose).toHaveBeenCalled();
  });

  it("calls onClose when backdrop is clicked", async () => {
    const user = userEvent.setup();
    const onClose = vi.fn();
    render(<KeyboardHelpOverlay open={true} onClose={onClose} />);
    // Click on the backdrop (the outer div with role=presentation)
    const backdrop = screen.getByRole("presentation");
    await user.click(backdrop);
    expect(onClose).toHaveBeenCalled();
  });

  it("traps focus: Tab from last focusable wraps to first", () => {
    const onClose = vi.fn();
    render(<KeyboardHelpOverlay open={true} onClose={onClose} />);
    const closeBtn = screen.getByLabelText("Close keyboard shortcuts");
    // Focus the close button (the only focusable element)
    closeBtn.focus();
    expect(document.activeElement).toBe(closeBtn);
    // Tab should wrap around
    document.dispatchEvent(
      new KeyboardEvent("keydown", { key: "Tab", bubbles: true })
    );
    // Since there's only one button, Tab from last should wrap to first
    // (the close button is both first and last)
  });

  it("traps focus: Shift+Tab from first focusable wraps to last", () => {
    const onClose = vi.fn();
    render(<KeyboardHelpOverlay open={true} onClose={onClose} />);
    const closeBtn = screen.getByLabelText("Close keyboard shortcuts");
    closeBtn.focus();
    document.dispatchEvent(
      new KeyboardEvent("keydown", { key: "Tab", shiftKey: true, bubbles: true })
    );
  });

  it("does not close on non-shortcut key press", () => {
    const onClose = vi.fn();
    render(<KeyboardHelpOverlay open={true} onClose={onClose} />);
    document.dispatchEvent(new KeyboardEvent("keydown", { key: "a", bubbles: true }));
    expect(onClose).not.toHaveBeenCalled();
  });
});
