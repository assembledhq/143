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
            description: "Connect your GitHub account to sync repositories and open PRs.",
            action: (
              <Button size="sm" onClick={onConnect} aria-label="Connect GitHub">
                Connect
              </Button>
            ),
          },
          {
            id: "linear",
            title: "Linear",
            description: "Sync issues from Linear and auto-assign fixes.",
            action: <Badge variant="secondary">Coming soon</Badge>,
          },
        ]}
      />
    );

    expect(screen.getByText("GitHub")).toBeInTheDocument();
    expect(screen.getByText("Linear")).toBeInTheDocument();
    expect(screen.getAllByTestId("integration-card")).toHaveLength(2);
    await user.click(screen.getByRole("button", { name: "Connect GitHub" }));
    expect(onConnect).toHaveBeenCalledTimes(1);
    expect(screen.getByText("Coming soon")).toBeInTheDocument();
  });
});
