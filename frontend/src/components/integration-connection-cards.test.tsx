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

    renderWithProviders(<SourceControlIntegrationCard onConnectGitHub={onConnectGitHub} />);

    expect(screen.getByText("Connect GitHub")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Connect GitHub" }));

    expect(onConnectGitHub).toHaveBeenCalledTimes(1);
  });

  it("renders additional cards and supports Sentry and Linear connect", async () => {
    const user = userEvent.setup();
    const onConnectSentry = vi.fn();
    const onConnectLinear = vi.fn();

    renderWithProviders(
      <AdditionalIntegrationCards
        linearConnected={false}
        linearLoading={false}
        onConnectSentry={onConnectSentry}
        onConnectLinear={onConnectLinear}
      />
    );

    expect(screen.getByText("Connect Sentry")).toBeInTheDocument();
    expect(screen.getByText("Connect Linear")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Connect Sentry" }));
    await user.click(screen.getByRole("button", { name: "Connect Linear" }));

    expect(onConnectSentry).toHaveBeenCalledTimes(1);
    expect(onConnectLinear).toHaveBeenCalledTimes(1);
  });

  it("disables Linear connect when already connected", () => {
    renderWithProviders(
      <AllIntegrationCards
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
