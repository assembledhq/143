import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import AutomationTemplatesPage from "./page";

describe("AutomationTemplatesPage", () => {
  it("renders a deeper template library with category browsing", () => {
    render(<AutomationTemplatesPage />);

    expect(screen.getByRole("heading", { name: "Automation templates" })).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: "Reliability" })).toBeInTheDocument();
    expect(screen.getByText("Find flaky tests")).toBeInTheDocument();
    expect(screen.getAllByRole("link", { name: /Use template/i }).length).toBeGreaterThan(0);
    expect(screen.getByText(/Browse examples and richer prompts/i)).toBeInTheDocument();
  });
});
