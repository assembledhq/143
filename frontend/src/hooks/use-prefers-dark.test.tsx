import { render, screen } from "@testing-library/react";
import { renderToString } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";

import { usePrefersDark } from "./use-prefers-dark";

vi.mock("next-themes", () => ({
  useTheme: () => ({ resolvedTheme: "dark" }),
}));

function ThemeProbe() {
  return <span>{usePrefersDark() ? "dark" : "light"}</span>;
}

describe("usePrefersDark", () => {
  it("keeps the server render light before applying the resolved dark theme after mount", async () => {
    expect(renderToString(<ThemeProbe />)).toContain("light");

    render(<ThemeProbe />);

    expect(await screen.findByText("dark")).toBeInTheDocument();
  });
});
