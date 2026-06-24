import { describe, expect, it } from "vitest";

import { renderWithProviders, screen } from "@/test/test-utils";
import { AutosaveIndicator } from "./AutosaveIndicator";

describe("AutosaveIndicator", () => {
  it("reserves an empty live status region when idle", () => {
    renderWithProviders(<AutosaveIndicator status="idle" className="custom" />);

    const status = screen.getByRole("status");
    expect(status).toHaveClass("custom");
    expect(status).toHaveTextContent("");
  });

  it("renders the saving state", () => {
    renderWithProviders(<AutosaveIndicator status="saving" />);

    expect(screen.getByRole("status")).toHaveTextContent("Saving…");
  });

  it("renders the saved state", () => {
    renderWithProviders(<AutosaveIndicator status="saved" />);

    expect(screen.getByRole("status")).toHaveTextContent("Saved");
  });

  it("renders the error state", () => {
    renderWithProviders(<AutosaveIndicator status="error" />);

    expect(screen.getByRole("status")).toHaveTextContent("Couldn't save");
  });
});
