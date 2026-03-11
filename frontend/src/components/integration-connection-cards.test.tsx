import { describe, expect, it, vi } from "vitest";
import { renderWithProviders, screen, userEvent } from "@/test/test-utils";
import {
  AdditionalIntegrationCards,
  AllIntegrationCards,
  SourceControlIntegrationCard,
} from "./integration-connection-cards";

describe("integration connection cards", () => {
  it("renders source control card and triggers GitHub connect", async () => {
    const user = userEvent.setup();
    const onConnectGitHub = vi.fn();

    renderWithProviders(<SourceControlIntegrationCard githubConnected={false} onConnectGitHub={onConnectGitHub} />);

    expect(screen.getByText("GitHub")).toBeInTheDocument();
    expect(screen.getByAltText("GitHub logo")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Connect GitHub" }));

    expect(onConnectGitHub).toHaveBeenCalledTimes(1);
  });

  it("renders additional cards and supports Sentry and Linear connect", async () => {
    const user = userEvent.setup();
    const onConnectSentry = vi.fn();
    const onConnectLinear = vi.fn();

    renderWithProviders(
      <AdditionalIntegrationCards
        sentryConnected={false}
        linearConnected={false}
        linearLoading={false}
        onConnectSentry={onConnectSentry}
        onConnectLinear={onConnectLinear}
      />
    );

    expect(screen.getByText("Sentry")).toBeInTheDocument();
    expect(screen.getByText("Linear")).toBeInTheDocument();
    expect(screen.getByAltText("Sentry logo")).toBeInTheDocument();
    expect(screen.getByAltText("Linear logo")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Connect Sentry" }));
    await user.click(screen.getByRole("button", { name: "Connect Linear" }));

    expect(onConnectSentry).toHaveBeenCalledTimes(1);
    expect(onConnectLinear).toHaveBeenCalledTimes(1);
  });

  it("disables Linear connect when already connected", () => {
    renderWithProviders(
      <AllIntegrationCards
        githubConnected={false}
        sentryConnected={false}
        linearConnected
        linearLoading={false}
        onConnectGitHub={vi.fn()}
        onConnectSentry={vi.fn()}
        onConnectLinear={vi.fn()}
      />
    );

    expect(screen.getByRole("button", { name: "Linear Connected" })).toBeDisabled();
  });
});
