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
        slackConnected={false}
        onConnectSentry={onConnectSentry}
        onConnectLinear={onConnectLinear}
        onConnectSlack={vi.fn()}
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

  it("shows connected repo names when GitHub is connected", () => {
    renderWithProviders(
      <SourceControlIntegrationCard
        githubConnected
        githubRepoNames={["acme/api", "acme/web"]}
        onConnectGitHub={vi.fn()}
      />
    );

    expect(screen.getByText("acme/api")).toBeInTheDocument();
    expect(screen.getByText("acme/web")).toBeInTheDocument();
  });

  it("does not show repo names when GitHub is not connected", () => {
    renderWithProviders(
      <SourceControlIntegrationCard
        githubConnected={false}
        githubRepoNames={["acme/api"]}
        onConnectGitHub={vi.fn()}
      />
    );

    expect(screen.queryByText("acme/api")).not.toBeInTheDocument();
  });

  it("disables Linear connect when already connected", () => {
    renderWithProviders(
      <AllIntegrationCards
        githubConnected={false}
        sentryConnected={false}
        linearConnected
        linearLoading={false}
        slackConnected={false}
        onConnectGitHub={vi.fn()}
        onConnectSentry={vi.fn()}
        onConnectLinear={vi.fn()}
        onConnectSlack={vi.fn()}
      />
    );

    expect(screen.getByRole("button", { name: "Linear Connected" })).toBeDisabled();
  });

  it("renders Slack card with Connect button when not connected", () => {
    renderWithProviders(
      <AdditionalIntegrationCards
        sentryConnected={false}
        linearConnected={false}
        linearLoading={false}
        slackConnected={false}
        onConnectSentry={vi.fn()}
        onConnectLinear={vi.fn()}
        onConnectSlack={vi.fn()}
      />
    );

    expect(screen.getByText("Slack")).toBeInTheDocument();
    expect(screen.getByAltText("Slack logo")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Connect Slack" })).toBeEnabled();
  });

  it("shows Slack as Connected when slackConnected is true", () => {
    renderWithProviders(
      <AdditionalIntegrationCards
        sentryConnected={false}
        linearConnected={false}
        linearLoading={false}
        slackConnected
        onConnectSentry={vi.fn()}
        onConnectLinear={vi.fn()}
        onConnectSlack={vi.fn()}
      />
    );

    expect(screen.getByRole("button", { name: "Slack Connected" })).toBeDisabled();
  });

  it("calls onConnectSlack when Connect button is clicked", async () => {
    const user = userEvent.setup();
    const onConnectSlack = vi.fn();

    renderWithProviders(
      <AdditionalIntegrationCards
        sentryConnected={false}
        linearConnected={false}
        linearLoading={false}
        slackConnected={false}
        onConnectSentry={vi.fn()}
        onConnectLinear={vi.fn()}
        onConnectSlack={onConnectSlack}
      />
    );

    await user.click(screen.getByRole("button", { name: "Connect Slack" }));

    expect(onConnectSlack).toHaveBeenCalledTimes(1);
  });
});
