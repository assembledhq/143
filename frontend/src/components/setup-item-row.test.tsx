import { describe, it, expect } from "vitest";
import { renderWithProviders, screen } from "@/test/test-utils";
import { SetupItemRow } from "./setup-item-row";

describe("SetupItemRow", () => {
  it("renders title, description, and action", () => {
    renderWithProviders(
      <SetupItemRow
        icon={<span data-testid="icon" />}
        title="Coding agent"
        description="Codex isn't connected yet."
        action={<button>Configure keys</button>}
      />,
    );
    expect(screen.getByText("Coding agent")).toBeInTheDocument();
    expect(screen.getByText("Codex isn't connected yet.")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Configure keys" })).toBeInTheDocument();
    expect(screen.getByTestId("icon")).toBeInTheDocument();
  });

  it("shows a Connected badge when done and no action is provided", () => {
    renderWithProviders(
      <SetupItemRow icon={<span />} title="Repository" done />,
    );
    expect(screen.getByText("Connected")).toBeInTheDocument();
  });

  it("prefers the action over the done badge", () => {
    renderWithProviders(
      <SetupItemRow icon={<span />} title="Repository" done action={<button>Sync</button>} />,
    );
    expect(screen.getByRole("button", { name: "Sync" })).toBeInTheDocument();
    expect(screen.queryByText("Connected")).not.toBeInTheDocument();
  });
});
