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
        notionConnected={false}
        onConnectSentry={onConnectSentry}
        onConnectLinear={onConnectLinear}
        onConnectSlack={vi.fn()}
        onConnectNotion={vi.fn()}
        circleciConnected={false}
        onConnectCircleCI={vi.fn()}
        mezmoConnected={false}
        onConnectMezmo={vi.fn()}
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

  it("renders the Mezmo card and triggers connect", async () => {
    const user = userEvent.setup();
    const onConnectMezmo = vi.fn();

    renderWithProviders(
      <AdditionalIntegrationCards
        sentryConnected={false}
        linearConnected={false}
        linearLoading={false}
        slackConnected={false}
        notionConnected={false}
        onConnectSentry={vi.fn()}
        onConnectLinear={vi.fn()}
        onConnectSlack={vi.fn()}
        onConnectNotion={vi.fn()}
        circleciConnected={false}
        onConnectCircleCI={vi.fn()}
        mezmoConnected={false}
        onConnectMezmo={onConnectMezmo}
      />
    );

    expect(screen.getByText("Mezmo")).toBeInTheDocument();
    expect(screen.getByAltText("Mezmo logo")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Connect Mezmo" }));

    expect(onConnectMezmo).toHaveBeenCalledTimes(1);
  });

  it("shows a Disconnect button for Mezmo when connected with a handler", () => {
    renderWithProviders(
      <AdditionalIntegrationCards
        sentryConnected={false}
        linearConnected={false}
        linearLoading={false}
        slackConnected={false}
        notionConnected={false}
        onConnectSentry={vi.fn()}
        onConnectLinear={vi.fn()}
        onConnectSlack={vi.fn()}
        onConnectNotion={vi.fn()}
        circleciConnected={false}
        onConnectCircleCI={vi.fn()}
        mezmoConnected
        onConnectMezmo={vi.fn()}
        onDisconnect={vi.fn()}
      />
    );

    expect(screen.getByRole("button", { name: "Disconnect Mezmo" })).toBeInTheDocument();
  });

  it("shows connected repo names when GitHub is connected", () => {
    renderWithProviders(
      <SourceControlIntegrationCard
        githubConnected
        githubRepos={[
          { id: "r1", full_name: "acme/api", status: "active" },
          { id: "r2", full_name: "acme/web", status: "active" },
        ]}
        onConnectGitHub={vi.fn()}
      />
    );

    expect(screen.getByText("acme/api")).toBeInTheDocument();
    expect(screen.getByText("acme/web")).toBeInTheDocument();
  });

  it("summarizes overflow repository names instead of expanding card height", () => {
    renderWithProviders(
      <SourceControlIntegrationCard
        githubConnected
        githubRepos={[
          { id: "r1", full_name: "acme/api", status: "active" },
          { id: "r2", full_name: "acme/web", status: "active" },
          { id: "r3", full_name: "acme/mobile", status: "active" },
          { id: "r4", full_name: "acme/docs", status: "active" },
        ]}
        onConnectGitHub={vi.fn()}
      />
    );

    expect(screen.getByText("acme/api")).toBeInTheDocument();
    expect(screen.getByText("acme/web")).toBeInTheDocument();
    expect(screen.getByText("acme/mobile")).toBeInTheDocument();
    expect(screen.getByText("+1 more")).toBeInTheDocument();
    expect(screen.queryByText("acme/docs")).not.toBeInTheDocument();
  });

  it("does not show repo names when GitHub is not connected", () => {
    renderWithProviders(
      <SourceControlIntegrationCard
        githubConnected={false}
        githubRepos={[{ id: "r1", full_name: "acme/api", status: "active" }]}
        onConnectGitHub={vi.fn()}
      />
    );

    expect(screen.queryByText("acme/api")).not.toBeInTheDocument();
  });

  it("does not render per-repo disconnect controls on the summary card", () => {
    renderWithProviders(
      <SourceControlIntegrationCard
        githubConnected
        githubRepos={[{ id: "r1", full_name: "acme/api", status: "active" }]}
        onConnectGitHub={vi.fn()}
      />
    );

    expect(screen.queryByRole("button", { name: "Disconnect acme/api" })).not.toBeInTheDocument();
  });

  it("does not render per-repo reconnect controls on the summary card", () => {
    renderWithProviders(
      <SourceControlIntegrationCard
        githubConnected
        githubRepos={[{ id: "r1", full_name: "acme/api", status: "disconnected" }]}
        onConnectGitHub={vi.fn()}
      />
    );

    expect(screen.getByText("No active repositories")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Reconnect acme/api" })).not.toBeInTheDocument();
  });

  it("disables Linear connect when already connected and no disconnect handler", () => {
    renderWithProviders(
      <AllIntegrationCards
        githubConnected={false}
        sentryConnected={false}
        linearConnected
        linearLoading={false}
        slackConnected={false}
        notionConnected={false}
        onConnectGitHub={vi.fn()}
        onConnectSentry={vi.fn()}
        onConnectLinear={vi.fn()}
        onConnectSlack={vi.fn()}
        onConnectNotion={vi.fn()}
        circleciConnected={false}
        onConnectCircleCI={vi.fn()}
        mezmoConnected={false}
        onConnectMezmo={vi.fn()}
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
        notionConnected={false}
        onConnectSentry={vi.fn()}
        onConnectLinear={vi.fn()}
        onConnectSlack={vi.fn()}
        onConnectNotion={vi.fn()}
        circleciConnected={false}
        onConnectCircleCI={vi.fn()}
        mezmoConnected={false}
        onConnectMezmo={vi.fn()}
      />
    );

    expect(screen.getByText("Slack")).toBeInTheDocument();
    expect(screen.getByAltText("Slack logo")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Connect Slack" })).toBeEnabled();
  });

  it("shows Slack as Connected with no disconnect when no handler provided", () => {
    renderWithProviders(
      <AdditionalIntegrationCards
        sentryConnected={false}
        linearConnected={false}
        linearLoading={false}
        slackConnected
        notionConnected={false}
        onConnectSentry={vi.fn()}
        onConnectLinear={vi.fn()}
        onConnectSlack={vi.fn()}
        onConnectNotion={vi.fn()}
        circleciConnected={false}
        onConnectCircleCI={vi.fn()}
        mezmoConnected={false}
        onConnectMezmo={vi.fn()}
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
        notionConnected={false}
        onConnectSentry={vi.fn()}
        onConnectLinear={vi.fn()}
        onConnectSlack={onConnectSlack}
        onConnectNotion={vi.fn()}
        circleciConnected={false}
        onConnectCircleCI={vi.fn()}
        mezmoConnected={false}
        onConnectMezmo={vi.fn()}
      />
    );

    await user.click(screen.getByRole("button", { name: "Connect Slack" }));

    expect(onConnectSlack).toHaveBeenCalledTimes(1);
  });

  it("shows Disconnect button when connected and onDisconnect is provided", () => {
    renderWithProviders(
      <SourceControlIntegrationCard
        githubConnected
        onConnectGitHub={vi.fn()}
        onDisconnect={vi.fn()}
      />
    );

    const disconnectButton = screen.getByRole("button", { name: "Disconnect GitHub" });
    expect(disconnectButton).toBeInTheDocument();
    expect(disconnectButton).toBeEnabled();
  });

  it("opens confirmation dialog and calls onDisconnect on confirm", async () => {
    const user = userEvent.setup();
    const onDisconnect = vi.fn();

    renderWithProviders(
      <SourceControlIntegrationCard
        githubConnected
        onConnectGitHub={vi.fn()}
        onDisconnect={onDisconnect}
      />
    );

    // Click Disconnect to open dialog
    await user.click(screen.getByRole("button", { name: "Disconnect GitHub" }));

    // Confirmation dialog should appear
    expect(screen.getByText("Disconnect GitHub")).toBeInTheDocument();
    expect(screen.getByText(/This will disconnect GitHub/)).toBeInTheDocument();

    // Confirm the disconnect
    await user.click(screen.getByRole("button", { name: "Disconnect" }));

    expect(onDisconnect).toHaveBeenCalledWith("github");
  });

  it("cancels disconnect when Cancel is clicked in dialog", async () => {
    const user = userEvent.setup();
    const onDisconnect = vi.fn();

    renderWithProviders(
      <SourceControlIntegrationCard
        githubConnected
        onConnectGitHub={vi.fn()}
        onDisconnect={onDisconnect}
      />
    );

    await user.click(screen.getByRole("button", { name: "Disconnect GitHub" }));
    await user.click(screen.getByRole("button", { name: "Cancel" }));

    expect(onDisconnect).not.toHaveBeenCalled();
  });

  it("shows Disconnect buttons for additional integrations when connected with handler", () => {
    renderWithProviders(
      <AdditionalIntegrationCards
        sentryConnected
        linearConnected
        linearLoading={false}
        slackConnected
        notionConnected
        onConnectSentry={vi.fn()}
        onConnectLinear={vi.fn()}
        onConnectSlack={vi.fn()}
        onConnectNotion={vi.fn()}
        circleciConnected={false}
        onConnectCircleCI={vi.fn()}
        mezmoConnected={false}
        onConnectMezmo={vi.fn()}
        onDisconnect={vi.fn()}
      />
    );

    expect(screen.getByRole("button", { name: "Disconnect Sentry" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Disconnect Linear" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Disconnect Slack" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Disconnect Notion" })).toBeInTheDocument();
  });

  it("shows error message when disconnect fails", () => {
    renderWithProviders(
      <SourceControlIntegrationCard
        githubConnected
        onConnectGitHub={vi.fn()}
        onDisconnect={vi.fn()}
        disconnectErrorProvider="github"
        disconnectError="Failed to disconnect."
      />
    );

    expect(screen.getByText("Failed to disconnect.")).toBeInTheDocument();
  });

  it("renders the auth-error banner and folds Reconnect into the row's primary CTA", async () => {
    const user = userEvent.setup();
    const onConnectLinear = vi.fn();

    renderWithProviders(
      <AdditionalIntegrationCards
        sentryConnected={false}
        linearConnected={false}
        linearLoading={false}
        linearAuthError={{
          reason: "Linear rejected the access token (HTTP 401). Reconnect to continue syncing.",
          at: "2026-05-02T20:02:11Z",
        }}
        slackConnected={false}
        notionConnected={false}
        onConnectSentry={vi.fn()}
        onConnectLinear={onConnectLinear}
        onConnectSlack={vi.fn()}
        onConnectNotion={vi.fn()}
        circleciConnected={false}
        onConnectCircleCI={vi.fn()}
        mezmoConnected={false}
        onConnectMezmo={vi.fn()}
      />
    );

    // Reason banner present.
    expect(screen.getByText("Reconnect required")).toBeInTheDocument();
    expect(screen.getByText(/Linear rejected the access token/)).toBeInTheDocument();

    // Single CTA on the row — not duplicated inside the banner.
    const reconnectButtons = screen.getAllByRole("button", { name: "Reconnect Linear" });
    expect(reconnectButtons).toHaveLength(1);

    // Generic "Connect Linear" affordance is suppressed when authErrored.
    expect(screen.queryByRole("button", { name: "Connect Linear" })).not.toBeInTheDocument();

    await user.click(reconnectButtons[0]);
    expect(onConnectLinear).toHaveBeenCalledTimes(1);
  });

  it("readOnly auth-errored row shows a 'Reconnect required' badge and no buttons", () => {
    renderWithProviders(
      <AdditionalIntegrationCards
        sentryConnected={false}
        linearConnected={false}
        linearLoading={false}
        linearAuthError={{
          reason: "Linear rejected the access token (HTTP 401). Reconnect to continue syncing.",
          at: "2026-05-02T20:02:11Z",
        }}
        slackConnected={false}
        notionConnected={false}
        onConnectSentry={vi.fn()}
        onConnectLinear={vi.fn()}
        onConnectSlack={vi.fn()}
        onConnectNotion={vi.fn()}
        circleciConnected={false}
        onConnectCircleCI={vi.fn()}
        mezmoConnected={false}
        onConnectMezmo={vi.fn()}
        readOnly
      />
    );

    // Two "Reconnect required" elements expected: the alert title above the
    // row and the read-only status badge in the action slot.
    expect(screen.getAllByText("Reconnect required")).toHaveLength(2);
    expect(screen.queryByRole("button", { name: "Reconnect Linear" })).not.toBeInTheDocument();
  });

  it("readOnly hides connect/disconnect buttons and per-repo controls; shows status badges instead", () => {
    renderWithProviders(
      <AllIntegrationCards
        githubConnected
        githubRepos={[{ id: "r1", full_name: "acme/api", status: "active" }]}
        sentryConnected={false}
        linearConnected
        linearLoading={false}
        slackConnected={false}
        notionConnected
        onConnectGitHub={vi.fn()}
        onConnectSentry={vi.fn()}
        onConnectLinear={vi.fn()}
        onConnectSlack={vi.fn()}
        onConnectNotion={vi.fn()}
        circleciConnected={false}
        onConnectCircleCI={vi.fn()}
        mezmoConnected={false}
        onConnectMezmo={vi.fn()}
        onDisconnect={vi.fn()}
        readOnly
      />
    );

    expect(screen.queryByRole("button", { name: /Connect/ })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Disconnect/ })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Disconnect acme\/api/ })).not.toBeInTheDocument();
    expect(screen.getAllByText("Connected").length).toBeGreaterThan(0);
    expect(screen.getAllByText("Not connected").length).toBeGreaterThan(0);
  });
});
