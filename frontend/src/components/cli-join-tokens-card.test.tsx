import { describe, expect, it } from "vitest";
import { http, HttpResponse } from "msw";
import { renderWithProviders, screen, userEvent, waitFor } from "@/test/test-utils";
import { server } from "@/test/mocks/server";
import { CLIJoinTokensCard } from "./cli-join-tokens-card";

describe("CLIJoinTokensCard", () => {
  it("renders the title without a leading icon", async () => {
    server.use(
      http.get("/api/v1/org/join-tokens", () => HttpResponse.json({ data: [], meta: {} })),
    );

    renderWithProviders(<CLIJoinTokensCard />);

    const heading = await screen.findByRole("heading", { name: "CLI install links" });
    expect(heading.previousElementSibling).toBeNull();
  });

  it("keeps the create-link controls the same height", async () => {
    server.use(
      http.get("/api/v1/org/join-tokens", () => HttpResponse.json({ data: [], meta: {} })),
    );

    renderWithProviders(<CLIJoinTokensCard />);

    expect(await screen.findByLabelText("Link name")).toHaveClass("h-9");
    expect(screen.getByRole("combobox", { name: "Role granted" })).toHaveClass("h-9");
    expect(screen.getByRole("button", { name: "Create link" })).toHaveClass("h-9");
  });

  it("labels the member role as Engineer in the create-link role picker", async () => {
    server.use(
      http.get("/api/v1/org/join-tokens", () => HttpResponse.json({ data: [], meta: {} })),
    );

    renderWithProviders(<CLIJoinTokensCard />);

    await userEvent.click(await screen.findByRole("combobox", { name: "Role granted" }));

    expect(await screen.findByRole("option", { name: "Engineer" })).toBeInTheDocument();
    expect(screen.queryByRole("option", { name: "Member" })).not.toBeInTheDocument();
  });

  it("keeps the created install command inside the dialog bounds", async () => {
    const longInstallCommand =
      "curl -fsSL https://143.dev/install/143j_Ab3x9kQ2mP4rY7tN2qV6w | sh";
    server.use(
      http.get("/api/v1/org/join-tokens", () => HttpResponse.json({ data: [], meta: {} })),
      http.post("/api/v1/org/join-tokens", () =>
        HttpResponse.json(
          {
            data: {
              id: "join-token-1",
              token: "143j_Ab3x9kQ2mP4rY7tN2qV6w",
              token_prefix: "143j_jD74XFT",
              role: "member",
              name: "",
              install_command: longInstallCommand,
            },
          },
          { status: 201 },
        ),
      ),
    );

    renderWithProviders(<CLIJoinTokensCard />);
    await userEvent.click(await screen.findByRole("button", { name: "Create link" }));

    const dialog = await screen.findByRole("alertdialog", { name: "Install link created" });
    const command = await screen.findByText(longInstallCommand);

    await waitFor(() => {
      expect(dialog).toHaveClass("max-w-[calc(100vw-2rem)]");
      expect(command.parentElement).toHaveClass("min-w-0");
      expect(command).toHaveClass("break-all");
    });
  });
});
