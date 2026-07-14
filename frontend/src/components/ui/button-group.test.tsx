import { describe, expect, it } from "vitest"
import { ChevronDown } from "lucide-react"

import { renderWithProviders, screen } from "@/test/test-utils"
import { Button } from "./button"
import { ButtonGroup } from "./button-group"

describe("ButtonGroup", () => {
  it("owns one height for text buttons and attached icon buttons", () => {
    renderWithProviders(
      <ButtonGroup size="sm" aria-label="Publish actions">
        <Button size="sm">Publish</Button>
        <Button size="icon" aria-label="More publish actions">
          <ChevronDown />
        </Button>
      </ButtonGroup>,
    )

    const group = screen.getByRole("group", { name: "Publish actions" })
    expect(group).toHaveAttribute("data-size", "sm")
    expect(group).toHaveClass("h-10", "sm:h-7", "[&_[data-slot=button]]:!h-full")
  })
})
