import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ErrorNotice, ErrorText } from "./error-notice";

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

  it("uses the shared compact typography for inline errors", () => {
    render(
      <ErrorNotice
        title="Couldn't create the PR"
        description="Check GitHub access or repo permissions and try again."
      />
    );

    const title = screen.getByText("Couldn't create the PR");
    const description = screen.getByText("Check GitHub access or repo permissions and try again.");

    expect(title.className).toContain("text-xs");
    expect(title.className).not.toContain("text-sm");
    expect(description.className).toContain("text-xs");
    expect(description.className).not.toContain("text-sm");
  });

  it("forces long error text to wrap inside the notice", () => {
    const longTitle =
      "preview service did not pass its readiness probe for /home/sandbox/assembled/gocode/msgconsumer/msgconsumer/internal/super/long/generated/path/with/no/spaces/github.com/assembledhq/assembled/gocode/msgconsumer";
    const longDescription =
      "Details: provider start preview: github.com/assembledhq/assembled/gocode/msgconsumer/runtimecreatedbygithub.com/assembledhq/assembled/gocode/msgconsumer";

    render(<ErrorNotice title={longTitle} description={longDescription} />);

    expect(screen.getByText(longTitle)).toHaveClass(
      "min-w-0",
      "break-words",
      "[overflow-wrap:anywhere]",
    );
    expect(screen.getByText(longDescription)).toHaveClass(
      "min-w-0",
      "break-words",
      "[overflow-wrap:anywhere]",
    );
  });

  it("renders a dismiss button when provided", async () => {
    const onDismiss = vi.fn();
    const user = userEvent.setup();

    render(
      <ErrorNotice
        title="Preview failed"
        description="The readiness probe timed out."
        onDismiss={onDismiss}
      />,
    );

    await user.click(screen.getByRole("button", { name: "Dismiss error" }));

    expect(onDismiss).toHaveBeenCalledTimes(1);
  });

  it("renders inline error text with the same wrapping guarantees", () => {
    const message =
      "Failed to save /home/sandbox/assembled/gocode/msgconsumer/msgconsumer/internal/super/long/generated/path/with/no/spaces";

    render(<ErrorText role="alert">{message}</ErrorText>);

    expect(screen.getByRole("alert")).toHaveClass(
      "min-w-0",
      "break-words",
      "[overflow-wrap:anywhere]",
    );
  });
});
