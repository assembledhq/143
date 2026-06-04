import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { Command, CommandCheckItem, CommandGroup, CommandInput, CommandList } from "./command";

describe("CommandInput", () => {
  it("uses a mobile-safe font size and keeps compact desktop sizing", () => {
    render(
      <Command>
        <CommandInput placeholder="Filter people..." />
      </Command>,
    );

    const input = screen.getByPlaceholderText("Filter people...");
    expect(input).toHaveClass("max-sm:text-base");
    expect(input).toHaveClass("sm:text-sm");
  });
});

describe("CommandCheckItem", () => {
  it("renders a high-contrast checked indicator", () => {
    render(
      <Command>
        <CommandList>
          <CommandGroup>
            <CommandCheckItem checked value="grace">
              Grace Hopper
            </CommandCheckItem>
          </CommandGroup>
        </CommandList>
      </Command>,
    );

    const option = screen.getByRole("option", { name: "Grace Hopper" });
    const indicator = option.querySelector('[data-slot="command-item-indicator"]');

    expect(indicator).not.toBeNull();
    expect(indicator).toHaveClass("border-primary");
    expect(indicator).toHaveClass("bg-primary");
    expect(indicator).toHaveClass("text-primary-foreground");
    expect(indicator?.querySelector("svg")).toHaveClass("text-primary-foreground");
  });

  it("renders an unselected indicator with a visible outline", () => {
    render(
      <Command>
        <CommandList>
          <CommandGroup>
            <CommandCheckItem checked={false} value="ada">
              Ada Lovelace
            </CommandCheckItem>
          </CommandGroup>
        </CommandList>
      </Command>,
    );

    const option = screen.getByRole("option", { name: "Ada Lovelace" });
    const indicator = option.querySelector('[data-slot="command-item-indicator"]');

    expect(indicator).not.toBeNull();
    expect(indicator).toHaveClass("border-border");
    expect(indicator).toHaveClass("bg-background");
    expect(indicator).toHaveClass("text-transparent");
    expect(indicator?.querySelector("svg")).toHaveClass("text-transparent");
  });
});
