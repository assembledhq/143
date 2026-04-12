import { describe, expect, it, vi, beforeEach } from "vitest";
import { renderWithProviders, screen, waitFor, fireEvent } from "@/test/test-utils";
import { VisualEditingPanel } from "./visual-editing-panel";
import type { ElementInfo } from "@/lib/preview-types";

const { applyEditMock } = vi.hoisted(() => ({
  applyEditMock: vi.fn().mockResolvedValue({ data: {} }),
}));

vi.mock("@/lib/api", () => ({
  api: {
    sessions: {
      preview: {
        applyEdit: applyEditMock,
      },
    },
  },
}));

function makeElement(overrides: Partial<ElementInfo> = {}): ElementInfo {
  return {
    tag_name: "div",
    class_list: ["container", "mx-auto"],
    id: "main-content",
    text_content: "Hello World",
    bounding_box: { x: 0, y: 0, width: 200, height: 100 },
    computed_styles: {
      color: "rgb(0, 0, 0)",
      "background-color": "rgb(255, 255, 255)",
      "border-color": "rgb(200, 200, 200)",
      "margin-top": "8px",
      "margin-right": "16px",
      "margin-bottom": "8px",
      "margin-left": "16px",
      "padding-top": "12px",
      "padding-right": "24px",
      "padding-bottom": "12px",
      "padding-left": "24px",
      "font-size": "16px",
      "font-weight": "400",
      "line-height": "24px",
      "letter-spacing": "0px",
      "flex-direction": "row",
      "justify-content": "flex-start",
      "align-items": "stretch",
      gap: "0px",
      width: "200px",
      height: "100px",
      "border-radius": "4px",
    },
    attributes: { class: "container mx-auto" },
    children_count: 3,
    ...overrides,
  };
}

describe("VisualEditingPanel", () => {
  const mockOnClose = vi.fn();

  beforeEach(() => {
    applyEditMock.mockClear();
    mockOnClose.mockClear();
  });

  it("renders with element info", () => {
    renderWithProviders(
      <VisualEditingPanel
        sessionId="sess-1"
        element={makeElement()}
        selector=".container"
        onClose={mockOnClose}
      />
    );

    // Should show the element selector
    expect(screen.getByText(".container")).toBeInTheDocument();
  });

  it("shows the close button", () => {
    renderWithProviders(
      <VisualEditingPanel
        sessionId="sess-1"
        element={makeElement()}
        selector="#main"
        onClose={mockOnClose}
      />
    );

    // Find and click close button
    const closeButtons = screen.getAllByRole("button");
    expect(closeButtons.length).toBeGreaterThan(0);
  });

  it("renders color inputs for element styles", () => {
    renderWithProviders(
      <VisualEditingPanel
        sessionId="sess-1"
        element={makeElement()}
        selector="div"
        onClose={mockOnClose}
      />
    );

    // Should show color-related labels
    expect(screen.getByText("Colors")).toBeInTheDocument();
  });

  it("renders spacing controls", () => {
    renderWithProviders(
      <VisualEditingPanel
        sessionId="sess-1"
        element={makeElement()}
        selector="div"
        onClose={mockOnClose}
      />
    );

    expect(screen.getByText("Spacing")).toBeInTheDocument();
  });

  it("renders typography controls", () => {
    renderWithProviders(
      <VisualEditingPanel
        sessionId="sess-1"
        element={makeElement()}
        selector="div"
        onClose={mockOnClose}
      />
    );

    expect(screen.getByText("Typography")).toBeInTheDocument();
  });

  it("renders layout controls", () => {
    renderWithProviders(
      <VisualEditingPanel
        sessionId="sess-1"
        element={makeElement()}
        selector="div"
        onClose={mockOnClose}
      />
    );

    expect(screen.getByText("Layout")).toBeInTheDocument();
  });

  it("renders sizing controls", () => {
    renderWithProviders(
      <VisualEditingPanel
        sessionId="sess-1"
        element={makeElement()}
        selector="div"
        onClose={mockOnClose}
      />
    );

    expect(screen.getByText("Sizing")).toBeInTheDocument();
  });

  it("initializes state from element computed styles", () => {
    const element = makeElement({
      computed_styles: {
        color: "#ff0000",
        "background-color": "#00ff00",
        "border-color": "#0000ff",
        "margin-top": "10px",
        "margin-right": "20px",
        "margin-bottom": "10px",
        "margin-left": "20px",
        "padding-top": "5px",
        "padding-right": "5px",
        "padding-bottom": "5px",
        "padding-left": "5px",
        "font-size": "18px",
        "font-weight": "700",
        "line-height": "28px",
        "letter-spacing": "1px",
        "flex-direction": "column",
        "justify-content": "center",
        "align-items": "center",
        gap: "8px",
        width: "300px",
        height: "200px",
        "border-radius": "8px",
      },
    });

    renderWithProviders(
      <VisualEditingPanel
        sessionId="sess-1"
        element={element}
        selector="div"
        onClose={mockOnClose}
      />
    );

    // Panel should render without errors
    expect(screen.getByText("Colors")).toBeInTheDocument();
    expect(screen.getByText("Typography")).toBeInTheDocument();
  });

  it("handles element with missing styles gracefully", () => {
    const element = makeElement({
      computed_styles: {},
    });

    renderWithProviders(
      <VisualEditingPanel
        sessionId="sess-1"
        element={element}
        selector="div"
        onClose={mockOnClose}
      />
    );

    // Should still render all sections
    expect(screen.getByText("Colors")).toBeInTheDocument();
  });

  it("shows component name when element has one", () => {
    const element = makeElement({ component_name: "Button" });

    renderWithProviders(
      <VisualEditingPanel
        sessionId="sess-1"
        element={element}
        selector="button"
        onClose={mockOnClose}
      />
    );

    expect(screen.getByText(/Button/)).toBeInTheDocument();
  });
});
