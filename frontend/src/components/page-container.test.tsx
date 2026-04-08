import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { PageContainer } from "./page-container";

describe("PageContainer", () => {
  it("uses default max width and centers content", () => {
    render(<PageContainer>Default container</PageContainer>);

    const container = screen.getByText("Default container");
    expect(container).toHaveClass("max-w-5xl");
    expect(container).toHaveClass("mx-auto");
  });

  it("applies narrow width for narrow size and wider max for wide", () => {
    const { rerender } = render(<PageContainer size="narrow">Sized container</PageContainer>);

    let container = screen.getByText("Sized container");
    expect(container).toHaveClass("max-w-3xl");

    rerender(<PageContainer size="wide">Sized container</PageContainer>);
    container = screen.getByText("Sized container");
    expect(container).toHaveClass("max-w-7xl");
  });
});
