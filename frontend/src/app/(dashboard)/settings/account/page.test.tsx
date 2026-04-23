import { describe, expect, it } from "vitest";
import { http, HttpResponse } from "msw";
import { renderWithProviders, screen, userEvent } from "@/test/test-utils";
import { server } from "@/test/mocks/server";
import AccountPage from "./page";

describe("Account settings page", () => {
  it("renders configured personal auths without inventing a fallback order", async () => {
    server.use(
      http.get("/api/v1/settings/credentials/personal", () =>
        HttpResponse.json({
          data: [
            {
              provider: "anthropic",
              configured: true,
              is_team_default: false,
              masked_key: "sk-ant...5678",
              status: "active",
            },
            {
              provider: "openai",
              configured: true,
              is_team_default: false,
              masked_key: "sk-open...1234",
              status: "active",
            },
          ],
          meta: {},
        }),
      ),
    );

    renderWithProviders(<AccountPage />);

    expect(screen.getByText("My settings")).toBeInTheDocument();
    expect(await screen.findByText("Configured personal auths")).toBeInTheDocument();
    expect(await screen.findByText("sk-ant...5678")).toBeInTheDocument();
    expect(await screen.findByText("sk-open...1234")).toBeInTheDocument();
    expect(screen.queryByText("Default auth")).not.toBeInTheDocument();
    expect(screen.queryByText("Backups in fallback order")).not.toBeInTheDocument();
    expect(screen.queryByText(/Effective resolution:/)).not.toBeInTheDocument();
  });

  it("uses the shared provider-card modal with Gemini support", async () => {
    const user = userEvent.setup();
    server.use(
      http.get("/api/v1/settings/credentials/personal", () =>
        HttpResponse.json({ data: [], meta: {} }),
      ),
    );

    renderWithProviders(<AccountPage />);

    await user.click(screen.getByRole("button", { name: "Add auth" }));

    expect(await screen.findByText("Codex")).toBeInTheDocument();
    expect(screen.getAllByText("Claude Code").length).toBeGreaterThan(0);
    expect(screen.getByText("Gemini CLI")).toBeInTheDocument();

    await user.click(screen.getByLabelText("Gemini CLI"));
    expect(screen.getByPlaceholderText("AIza...")).toBeInTheDocument();
  });
});
