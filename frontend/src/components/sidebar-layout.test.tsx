import { describe, it, expect } from "vitest";
import { render } from "@testing-library/react";
import { SidebarLayout } from "./sidebar-layout";

function renderLayout(mobileShow?: "sidebar" | "content") {
  return render(
    <SidebarLayout
      sidebar={<div data-testid="sidebar">sidebar</div>}
      mobileShow={mobileShow}
    >
      <div data-testid="content">content</div>
    </SidebarLayout>,
  );
}

describe("SidebarLayout", () => {
  it('hides the content pane on mobile when mobileShow="sidebar"', () => {
    const { getByTestId } = renderLayout("sidebar");
    // Both panes are mounted; content is display:none below md.
    const sidebar = getByTestId("sidebar").closest("div[style]");
    const content = getByTestId("content").parentElement;
    expect(sidebar?.className).not.toContain("hidden");
    expect(content?.className).toContain("hidden");
    expect(content?.className).toContain("md:block");
  });

  it('hides the sidebar pane on mobile when mobileShow="content"', () => {
    const { getByTestId } = renderLayout("content");
    const sidebar = getByTestId("sidebar").closest("div[style]");
    const content = getByTestId("content").parentElement;
    expect(sidebar?.className).toContain("hidden");
    expect(sidebar?.className).toContain("md:block");
    expect(content?.className).not.toContain("hidden");
  });

  it('defaults mobileShow to "sidebar"', () => {
    const { getByTestId } = renderLayout();
    const content = getByTestId("content").parentElement;
    expect(content?.className).toContain("hidden");
  });
});
