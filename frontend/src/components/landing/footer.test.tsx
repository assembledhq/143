import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import Footer from "./footer";

describe("Footer", () => {
  it("links to the public docs from the project links", () => {
    render(<Footer isDark={false} />);

    const docsLink = screen.getByRole("link", { name: "Docs" });

    expect(docsLink).toHaveAttribute("href", "/docs");
  });
});
