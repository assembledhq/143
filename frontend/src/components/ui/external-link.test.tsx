import { describe, expect, it } from "vitest"

import { renderWithProviders, screen } from "@/test/test-utils"

import { ExternalLink } from "./external-link"

describe("ExternalLink", () => {
  it("renders a recognizable external link with safe new-tab defaults", () => {
    const { container } = renderWithProviders(
      <ExternalLink href="https://example.com/review">Final review</ExternalLink>,
    )

    const link = screen.getByRole("link", { name: "Final review" })
    expect(link).toHaveAttribute("href", "https://example.com/review")
    expect(link).toHaveAttribute("target", "_blank")
    expect(link).toHaveAttribute("rel", "noopener noreferrer")
    expect(link).toHaveClass("text-primary", "underline")
    const icon = container.querySelector('[data-slot="external-link-icon"]')
    expect(icon).toHaveAttribute("aria-hidden", "true")
    expect(icon).toHaveClass("inline-block")
  })
})
