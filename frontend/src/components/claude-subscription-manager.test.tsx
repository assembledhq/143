import { describe, expect, it, vi } from "vitest";

import { renderWithProviders, screen, userEvent } from "@/test/test-utils";

import { ClaudeSubscriptionManager } from "./claude-subscription-manager";

describe("ClaudeSubscriptionManager", () => {
  it("shows invalid and pending-auth subscriptions so users can recover them", () => {
    renderWithProviders(
      <ClaudeSubscriptionManager
        subscriptions={[
          {
            id: "sub-active",
            label: "Team Active",
            status: "active",
            account_type: "claude_max",
          },
          {
            id: "sub-invalid",
            label: "Team Broken",
            status: "invalid",
          },
          {
            id: "sub-pending",
            label: "Team Pending",
            status: "pending_auth",
          },
        ]}
        showModal={false}
        onOpenModal={vi.fn()}
        onCloseModal={vi.fn()}
        onRemove={vi.fn()}
      />,
    );

    expect(screen.getByText("Connected subscriptions (1)")).toBeInTheDocument();
    expect(screen.getByText("Needs attention (2)")).toBeInTheDocument();
    expect(screen.getByText("Team Active")).toBeInTheDocument();
    expect(screen.getByText("Team Broken")).toBeInTheDocument();
    expect(screen.getByText("Team Pending")).toBeInTheDocument();
    expect(screen.getByText("Invalid")).toBeInTheDocument();
    expect(screen.getByText("Pending auth")).toBeInTheDocument();
  });

  it("keeps remove actions available for recoverable non-active subscriptions", async () => {
    const onRemove = vi.fn();
    const user = userEvent.setup();

    renderWithProviders(
      <ClaudeSubscriptionManager
        subscriptions={[
          {
            id: "sub-invalid",
            label: "Team Broken",
            status: "invalid",
          },
        ]}
        showModal={false}
        onOpenModal={vi.fn()}
        onCloseModal={vi.fn()}
        onRemove={onRemove}
      />,
    );

    await user.click(screen.getByRole("button", { name: "Remove Claude subscription Team Broken" }));
    expect(onRemove).toHaveBeenCalledWith({
      id: "sub-invalid",
      label: "Team Broken",
      status: "invalid",
    });
  });

  it("uses the standard field-height add button and removes the manual label input", () => {
    renderWithProviders(
      <ClaudeSubscriptionManager
        subscriptions={[]}
        showModal={false}
        onOpenModal={vi.fn()}
        onCloseModal={vi.fn()}
      />,
    );

    expect(screen.queryByPlaceholderText(/subscription label/i)).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Add subscription" })).toHaveAttribute("data-size", "lg");
  });
});
