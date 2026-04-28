import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { ToastCard } from "./toast-card";

describe("ToastCard", () => {
  it("renders a compact success toast without a dismiss button", () => {
    render(<ToastCard variant="success" title="Organization created" />);

    expect(screen.getByText("Organization created")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Dismiss notification" })).not.toBeInTheDocument();
  });

  it("renders a detailed error toast with action and dismiss controls", () => {
    const onAction = vi.fn();
    const onDismiss = vi.fn();

    render(
      <ToastCard
        variant="error"
        title="PR creation failed"
        description="GitHub rejected the branch update."
        action={{ label: "Retry", onClick: onAction }}
        onDismiss={onDismiss}
      />,
    );

    expect(screen.getByText("PR creation failed")).toBeInTheDocument();
    expect(screen.getByText("GitHub rejected the branch update.")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    expect(onAction).toHaveBeenCalledTimes(1);

    fireEvent.click(screen.getByRole("button", { name: "Dismiss notification" }));
    expect(onDismiss).toHaveBeenCalledTimes(1);
  });
});
