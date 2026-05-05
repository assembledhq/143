import { describe, expect, it, vi } from "vitest";
import { renderWithProviders, screen, userEvent } from "@/test/test-utils";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { IntegrationsCard } from "./integrations-card";

describe("IntegrationsCard", () => {
  it("renders each integration as its own card with actions", async () => {
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

  it("stacks card content for compact layouts", () => {
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

    const card = screen.getByTestId("integration-card");
    const content = card.querySelector('[data-slot="card-content"]');
    const actionWrapper = screen.getByRole("button", { name: "Connect" }).parentElement;

    expect(content).toHaveClass("flex-col");
    expect(content).toHaveClass("items-start");
    expect(content).toHaveClass("sm:flex-row");
    expect(content).toHaveClass("sm:items-center");
    expect(actionWrapper).toHaveClass("w-full");
    expect(actionWrapper).toHaveClass("sm:w-auto");
    expect(actionWrapper).toHaveClass("[&>*]:w-full");
    expect(actionWrapper).toHaveClass("sm:[&>*]:w-auto");
  });
});
