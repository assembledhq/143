import { describe, expect, it, vi, beforeEach } from "vitest";
import { renderWithProviders, screen, userEvent } from "@/test/test-utils";
import { VisualEditingPanel } from "./visual-editing-panel";
import type { ElementInfo } from "@/lib/preview-types";

const { applyEditMock, designFeedbackMock } = vi.hoisted(() => ({
  applyEditMock: vi.fn().mockResolvedValue({ data: {} }),
  designFeedbackMock: vi.fn().mockResolvedValue({ data: {} }),
}));

vi.mock("@/lib/api", () => ({
  api: {
    sessions: {
      preview: {
        applyEdit: applyEditMock,
        designFeedback: designFeedbackMock,
      },
    },
  },
}));

function makeElement(overrides: Partial<ElementInfo> = {}): ElementInfo {
  return {
    tag_name: "div",
    inner_text: "Hello World",
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
    attributes: { class: "container mx-auto", id: "main-content" },
    dom_path: "html > body > div#main-content",
    ...overrides,
  };
}

describe("VisualEditingPanel", () => {
  const mockOnClose = vi.fn();

  beforeEach(() => {
    applyEditMock.mockClear();
    designFeedbackMock.mockClear();
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

    // Should show the Visual Editor header
    expect(screen.getByText("Visual Editor")).toBeInTheDocument();
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

  it("renders tab list with tab triggers", () => {
    renderWithProviders(
      <VisualEditingPanel
        sessionId="sess-1"
        element={makeElement()}
        selector="div"
        onClose={mockOnClose}
      />
    );

    // Tabs render as icon-only triggers
    expect(screen.getByRole("tablist")).toBeInTheDocument();
    const tabs = screen.getAllByRole("tab");
    expect(tabs).toHaveLength(5);
  });

  it("renders active tab content (colors by default)", () => {
    renderWithProviders(
      <VisualEditingPanel
        sessionId="sess-1"
        element={makeElement()}
        selector="div"
        onClose={mockOnClose}
      />
    );

    // Default active tab is "colors" which shows Color, Background, Border labels
    expect(screen.getByText("Color")).toBeInTheDocument();
    expect(screen.getByText("Background")).toBeInTheDocument();
    expect(screen.getByText("Border")).toBeInTheDocument();
  });

  it("renders the Visual Editor header", () => {
    renderWithProviders(
      <VisualEditingPanel
        sessionId="sess-1"
        element={makeElement()}
        selector="div"
        onClose={mockOnClose}
      />
    );

    expect(screen.getByText("Visual Editor")).toBeInTheDocument();
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

    // Panel should render without errors — active tab shows color controls
    expect(screen.getByText("Color")).toBeInTheDocument();
    expect(screen.getByRole("tablist")).toBeInTheDocument();
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

    // Should still render panel with tabs
    expect(screen.getByRole("tablist")).toBeInTheDocument();
    expect(screen.getByText("Visual Editor")).toBeInTheDocument();
  });

  it("renders with component_name prop without errors", () => {
    const element = makeElement({ component_name: "Button" });

    renderWithProviders(
      <VisualEditingPanel
        sessionId="sess-1"
        element={element}
        selector="button"
        onClose={mockOnClose}
      />
    );

    // Panel renders successfully with component_name
    expect(screen.getByText("Visual Editor")).toBeInTheDocument();
    expect(screen.getByRole("tablist")).toBeInTheDocument();
  });

  it("clicking spacing tab shows margin and padding fields", async () => {
    const user = userEvent.setup();

    renderWithProviders(
      <VisualEditingPanel
        sessionId="sess-1"
        element={makeElement()}
        selector="div"
        onClose={mockOnClose}
      />
    );

    // Click the spacing tab (index 1)
    const tabs = screen.getAllByRole("tab");
    await user.click(tabs[1]);

    // Spacing tab content should now be visible
    expect(screen.getByText("Margin")).toBeInTheDocument();
    expect(screen.getByText("Padding")).toBeInTheDocument();
    expect(screen.getByText("Border Radius")).toBeInTheDocument();
  });

  it("clicking typography tab shows font fields", async () => {
    const user = userEvent.setup();

    renderWithProviders(
      <VisualEditingPanel
        sessionId="sess-1"
        element={makeElement()}
        selector="div"
        onClose={mockOnClose}
      />
    );

    // Click the typography tab (index 2)
    const tabs = screen.getAllByRole("tab");
    await user.click(tabs[2]);

    expect(screen.getByText("Font Size")).toBeInTheDocument();
    expect(screen.getByText("Font Weight")).toBeInTheDocument();
    expect(screen.getByText("Line Height")).toBeInTheDocument();
    expect(screen.getByText("Letter Spacing")).toBeInTheDocument();
  });

  it("clicking layout tab shows flex and gap fields", async () => {
    const user = userEvent.setup();

    renderWithProviders(
      <VisualEditingPanel
        sessionId="sess-1"
        element={makeElement()}
        selector="div"
        onClose={mockOnClose}
      />
    );

    // Click the layout tab (index 3)
    const tabs = screen.getAllByRole("tab");
    await user.click(tabs[3]);

    expect(screen.getByText("Flex Direction")).toBeInTheDocument();
    expect(screen.getByText("Justify Content")).toBeInTheDocument();
    expect(screen.getByText("Align Items")).toBeInTheDocument();
    expect(screen.getByText("Gap")).toBeInTheDocument();
  });

  it("clicking size tab shows width and height fields", async () => {
    const user = userEvent.setup();

    renderWithProviders(
      <VisualEditingPanel
        sessionId="sess-1"
        element={makeElement()}
        selector="div"
        onClose={mockOnClose}
      />
    );

    // Click the size tab (index 4)
    const tabs = screen.getAllByRole("tab");
    await user.click(tabs[4]);

    expect(screen.getByText("Width")).toBeInTheDocument();
    expect(screen.getByText("Height")).toBeInTheDocument();
  });

  it("renders the Apply button with disabled state when no changes", () => {
    renderWithProviders(
      <VisualEditingPanel
        sessionId="sess-1"
        element={makeElement()}
        selector="div"
        onClose={mockOnClose}
      />
    );

    const applyButton = screen.getByRole("button", { name: /apply/i });
    expect(applyButton).toBeInTheDocument();
    expect(applyButton).toBeDisabled();
  });

  it("Apply button shows change count after editing a color field", async () => {
    const user = userEvent.setup();

    renderWithProviders(
      <VisualEditingPanel
        sessionId="sess-1"
        element={makeElement()}
        selector="div"
        onClose={mockOnClose}
      />
    );

    // Type into the first color hex input to mark the field dirty
    const colorInputs = screen.getAllByPlaceholderText("#000000");
    await user.clear(colorInputs[0]);
    await user.type(colorInputs[0], "#ff0000");

    const applyButton = screen.getByRole("button", { name: /apply/i });
    expect(applyButton).toHaveTextContent(/1 change/i);
    expect(applyButton).not.toBeDisabled();
  });

  it("shows validation error for invalid hex color", async () => {
    const user = userEvent.setup();

    renderWithProviders(
      <VisualEditingPanel
        sessionId="sess-1"
        element={makeElement()}
        selector="div"
        onClose={mockOnClose}
      />
    );

    // Type invalid hex into the color field
    const colorInputs = screen.getAllByPlaceholderText("#000000");
    await user.clear(colorInputs[0]);
    await user.type(colorInputs[0], "notahex");

    // Validation error should appear
    expect(screen.getByText("Invalid text color hex value")).toBeInTheDocument();

    // Apply button should be disabled due to validation error
    const applyButton = screen.getByRole("button", { name: /apply/i });
    expect(applyButton).toBeDisabled();
  });

  it("shows validation error for invalid background color hex", async () => {
    const user = userEvent.setup();

    renderWithProviders(
      <VisualEditingPanel
        sessionId="sess-1"
        element={makeElement()}
        selector="div"
        onClose={mockOnClose}
      />
    );

    // Type invalid hex into the background color field (second input)
    const colorInputs = screen.getAllByPlaceholderText("#000000");
    await user.clear(colorInputs[1]);
    await user.type(colorInputs[1], "xyz");

    expect(
      screen.getByText("Invalid background color hex value")
    ).toBeInTheDocument();
  });

  it("shows validation error for invalid border color hex", async () => {
    const user = userEvent.setup();

    renderWithProviders(
      <VisualEditingPanel
        sessionId="sess-1"
        element={makeElement()}
        selector="div"
        onClose={mockOnClose}
      />
    );

    // Type invalid hex into the border color field (third input)
    const colorInputs = screen.getAllByPlaceholderText("#000000");
    await user.clear(colorInputs[2]);
    await user.type(colorInputs[2], "bad");

    expect(
      screen.getByText("Invalid border color hex value")
    ).toBeInTheDocument();
  });

  it("calls onClose when close button is clicked", async () => {
    const user = userEvent.setup();

    renderWithProviders(
      <VisualEditingPanel
        sessionId="sess-1"
        element={makeElement()}
        selector="div"
        onClose={mockOnClose}
      />
    );

    // The close button is the first icon button in the header
    const buttons = screen.getAllByRole("button");
    // Find the close button (the small one in the header, before the Apply button)
    await user.click(buttons[0]);

    expect(mockOnClose).toHaveBeenCalledTimes(1);
  });

  it("spacing tab shows T/R/B/L labels for margin and padding", async () => {
    const user = userEvent.setup();

    renderWithProviders(
      <VisualEditingPanel
        sessionId="sess-1"
        element={makeElement()}
        selector="div"
        onClose={mockOnClose}
      />
    );

    const tabs = screen.getAllByRole("tab");
    await user.click(tabs[1]);

    // SpacingSlider components render T, R, B, L labels (appears twice: margin + padding)
    const tLabels = screen.getAllByText("T");
    const rLabels = screen.getAllByText("R");
    const bLabels = screen.getAllByText("B");
    const lLabels = screen.getAllByText("L");

    expect(tLabels).toHaveLength(2);
    expect(rLabels).toHaveLength(2);
    expect(bLabels).toHaveLength(2);
    expect(lLabels).toHaveLength(2);
  });

  it("size tab renders input fields with current width and height values", async () => {
    const user = userEvent.setup();

    renderWithProviders(
      <VisualEditingPanel
        sessionId="sess-1"
        element={makeElement()}
        selector="div"
        onClose={mockOnClose}
      />
    );

    const tabs = screen.getAllByRole("tab");
    await user.click(tabs[4]);

    // Width/Height inputs should have values from computed styles
    const widthInput = screen.getByDisplayValue("200");
    const heightInput = screen.getByDisplayValue("100");
    expect(widthInput).toBeInTheDocument();
    expect(heightInput).toBeInTheDocument();
  });

  it("accepts valid hex colors without showing errors", async () => {
    const user = userEvent.setup();

    renderWithProviders(
      <VisualEditingPanel
        sessionId="sess-1"
        element={makeElement()}
        selector="div"
        onClose={mockOnClose}
      />
    );

    const colorInputs = screen.getAllByPlaceholderText("#000000");
    await user.clear(colorInputs[0]);
    await user.type(colorInputs[0], "#abc");

    // No validation errors should appear
    expect(
      screen.queryByText("Invalid text color hex value")
    ).not.toBeInTheDocument();

    // Apply button should be enabled
    const applyButton = screen.getByRole("button", { name: /apply/i });
    expect(applyButton).not.toBeDisabled();
  });

  it("accepts transparent as a valid color value", async () => {
    const user = userEvent.setup();

    renderWithProviders(
      <VisualEditingPanel
        sessionId="sess-1"
        element={makeElement()}
        selector="div"
        onClose={mockOnClose}
      />
    );

    const colorInputs = screen.getAllByPlaceholderText("#000000");
    await user.clear(colorInputs[0]);
    await user.type(colorInputs[0], "transparent");

    expect(
      screen.queryByText("Invalid text color hex value")
    ).not.toBeInTheDocument();
  });

  it("shows multiple validation errors simultaneously", async () => {
    const user = userEvent.setup();

    renderWithProviders(
      <VisualEditingPanel
        sessionId="sess-1"
        element={makeElement()}
        selector="div"
        onClose={mockOnClose}
      />
    );

    // Make both color and background color invalid
    const colorInputs = screen.getAllByPlaceholderText("#000000");
    await user.clear(colorInputs[0]);
    await user.type(colorInputs[0], "bad1");
    await user.clear(colorInputs[1]);
    await user.type(colorInputs[1], "bad2");

    expect(screen.getByText("Invalid text color hex value")).toBeInTheDocument();
    expect(
      screen.getByText("Invalid background color hex value")
    ).toBeInTheDocument();
  });

  it("clicking Apply with valid changes calls designFeedback", async () => {
    const user = userEvent.setup();

    renderWithProviders(
      <VisualEditingPanel
        sessionId="sess-1"
        element={makeElement()}
        selector=".container"
        onClose={mockOnClose}
      />
    );

    // Make a valid color change
    const colorInputs = screen.getAllByPlaceholderText("#000000");
    await user.clear(colorInputs[0]);
    await user.type(colorInputs[0], "#ff0000");

    // Click Apply
    const applyButton = screen.getByRole("button", { name: /apply/i });
    await user.click(applyButton);

    expect(designFeedbackMock).toHaveBeenCalledTimes(1);
    expect(designFeedbackMock).toHaveBeenCalledWith(
      "sess-1",
      expect.objectContaining({
        type: "visual_edit",
        instruction: expect.stringContaining(".container"),
        elements: expect.arrayContaining([
          expect.objectContaining({
            tag_name: "div",
          }),
        ]),
      })
    );
  });

  it("colors tab displays color swatch previews", () => {
    renderWithProviders(
      <VisualEditingPanel
        sessionId="sess-1"
        element={makeElement({
          computed_styles: {
            ...makeElement().computed_styles,
            color: "#ff0000",
          },
        })}
        selector="div"
        onClose={mockOnClose}
      />
    );

    // Should render color picker inputs (type="color")
    const colorPickers = document.querySelectorAll('input[type="color"]');
    expect(colorPickers.length).toBe(3);
  });
});
