import { describe, expect, it, vi } from "vitest";
import { renderWithProviders, screen, userEvent } from "@/test/test-utils";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { IntegrationsCard } from "./integrations-card";

describe("IntegrationsCard", () => {
  it("renders each integration as a grouped resource row with actions", async () => {
    const onConnect = vi.fn();
    const user = userEvent.setup();

    renderWithProviders(
      <IntegrationsCard
        items={[
          {
            id: "github",
            title: "GitHub",
            description: "Sync repositories and open PRs.",
            action: (
              <Button size="sm" onClick={onConnect} aria-label="Connect GitHub">
                Connect
              </Button>
            ),
          },
          {
            id: "sentry",
            title: "Sentry",
            description: "Pull errors and auto-generate fixes.",
            action: <Badge variant="secondary">Coming soon</Badge>,
          },
        ]}
      />
    );

    expect(screen.getByText("GitHub")).toBeInTheDocument();
    expect(screen.getByText("Sentry")).toBeInTheDocument();
    expect(screen.getAllByTestId("integration-card")).toHaveLength(2);
    await user.click(screen.getByRole("button", { name: "Connect GitHub" }));
    expect(onConnect).toHaveBeenCalledTimes(1);
    expect(screen.getByText("Coming soon")).toBeInTheDocument();
  });

  it("gives actions a full-width compact layout and an inline desktop layout", () => {
    renderWithProviders(
      <IntegrationsCard
        items={[
          {
            id: "github",
            title: "GitHub",
            description: "Sync repositories and open PRs.",
            action: <Button size="sm">Connect</Button>,
          },
        ]}
      />,
    );

    const row = screen.getByTestId("integration-card");
    const actionWrapper = screen.getByRole("button", { name: "Connect" }).parentElement?.parentElement;

    expect(row).toHaveAttribute("data-slot", "resource-row");
    expect(row).toHaveClass("flex-wrap");
    expect(row).toHaveClass("sm:flex-nowrap");
    expect(actionWrapper).toHaveClass("w-full");
    expect(actionWrapper).toHaveClass("sm:w-auto");
  });
});
