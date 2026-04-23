import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ErrorNotice } from "./error-notice";

describe("ErrorNotice", () => {
  it("renders a structured alert with title and description", () => {
    render(
      <ErrorNotice
        title="PR session expired"
        description="This draft is no longer valid. Re-run PR creation to generate a fresh one."
      />
    );

    const alert = screen.getByRole("alert");
    expect(alert).toHaveTextContent("PR session expired");
    expect(alert).toHaveTextContent("This draft is no longer valid. Re-run PR creation to generate a fresh one.");
  });

  it("renders an action button when provided", async () => {
    const onAction = vi.fn();
    const user = userEvent.setup();

    render(
      <ErrorNotice
        title="Couldn't create the PR"
        description="GitHub rejected the branch push."
        action={{ label: "Retry", onClick: onAction }}
      />
    );

    await user.click(screen.getByRole("button", { name: "Retry" }));

    expect(onAction).toHaveBeenCalledTimes(1);
  });
});
