import { describe, expect, it } from "vitest";
import { http, HttpResponse } from "msw";
import { renderWithProviders, screen } from "@/test/test-utils";
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
});
