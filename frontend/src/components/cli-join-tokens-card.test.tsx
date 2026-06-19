import { describe, expect, it, vi } from "vitest";
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

  it("copies an existing recoverable install link on demand", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: { writeText },
    });
    let requestedTokenID = "";
    server.use(
      http.get("/api/v1/org/join-tokens", () =>
        HttpResponse.json({
          data: [
            {
              id: "token-1",
              token_prefix: "143j_jD74XFTt",
              can_reveal: true,
              name: "John's CLI",
              role: "member",
              use_count: 0,
              status: "active",
              created_at: "2026-06-19T21:00:00Z",
            },
          ],
          meta: {},
        }),
      ),
      http.get("/api/v1/org/join-tokens/:id/link", ({ params }) => {
        requestedTokenID = String(params.id);
        return HttpResponse.json({
          data: {
            id: "token-1",
            token_prefix: "143j_jD74XFTt",
            install_command: "curl -fsSL https://143.example/install/143j_jD74XFTtabcdefghijkl | sh",
          },
        });
      }),
    );

    renderWithProviders(<CLIJoinTokensCard />);

    await userEvent.click(await screen.findByRole("button", { name: "Copy install link for John's CLI" }));

    await waitFor(() => {
      expect(writeText).toHaveBeenCalledWith("curl -fsSL https://143.example/install/143j_jD74XFTtabcdefghijkl | sh");
    });
    expect(requestedTokenID).toBe("token-1");
  });
});
