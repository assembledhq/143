import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { DocsThemeSwitch } from "./docs-theme-switch";

const themeState = vi.hoisted(() => ({
  theme: "dark" as string | undefined,
  resolvedTheme: "dark" as string | undefined,
  setTheme: vi.fn(),
}));

vi.mock("next-themes", () => ({
  useTheme: () => themeState,
}));

describe("DocsThemeSwitch", () => {
  beforeEach(() => {
    themeState.theme = "dark";
    themeState.resolvedTheme = "dark";
    themeState.setTheme.mockReset();
  });

  it("exposes separate local controls with an unambiguous selected theme", async () => {
    const user = userEvent.setup();
    render(<DocsThemeSwitch />);

    const lightButton = screen.getByRole("button", { name: "Light theme" });
    const darkButton = screen.getByRole("button", { name: "Dark theme" });

    expect(lightButton).toHaveAttribute("aria-pressed", "false");
    expect(lightButton).toHaveClass("size-11", "sm:size-11", "md:size-7");
    expect(darkButton).toHaveAttribute("aria-pressed", "true");

    await user.click(lightButton);

    expect(themeState.setTheme).toHaveBeenCalledWith("light");
  });

  it("includes the system preference only when the full mode is requested", () => {
    render(<DocsThemeSwitch mode="light-dark-system" />);

    expect(screen.getByRole("button", { name: "Use system theme" })).toBeInTheDocument();
  });
});
