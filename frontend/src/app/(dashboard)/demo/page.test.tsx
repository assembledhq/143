import { describe, expect, it } from "vitest";
import { http, HttpResponse } from "msw";
import { renderWithProviders, screen } from "@/test/test-utils";
import { server } from "@/test/mocks/server";
import DemoPage from "./page";

describe("DemoPage", () => {
  it("renders the manifest-backed guided replay", async () => {
    server.use(
      http.get("/api/v1/demo/manifest", () => {
        return HttpResponse.json({
          data: {
            org: { id: "00000000-0000-4000-a000-000000000001", name: "143 Dogfood" },
            primary: {
              session_id: "00000000-0000-4000-a000-000000000300",
              preview_group_id: "00000000-0000-4000-a000-000000000430",
              preview_target_id: "00000000-0000-4000-a000-000000000431",
            },
            pull_request: {
              id: "00000000-0000-4000-a000-000000000501",
              repository: "assembledhq/143",
              number: 42,
              url: "https://github.com/assembledhq/143/pull/42",
            },
            routes: {
              demo: "/demo",
              sessions: "/sessions",
              primary_session: "/sessions/00000000-0000-4000-a000-000000000300",
              primary_preview: "/previews/00000000-0000-4000-a000-000000000431",
              pull_request: "https://github.com/assembledhq/143/pull/42",
            },
            read_only: true,
          },
        });
      }),
    );

    renderWithProviders(<DemoPage />);

    expect(await screen.findByText("143 Dogfood")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /Open session/i })).toHaveAttribute(
      "href",
      "/sessions/00000000-0000-4000-a000-000000000300",
    );
    expect(screen.getByRole("link", { name: /Open preview state/i })).toHaveAttribute(
      "href",
      "/previews/00000000-0000-4000-a000-000000000431",
    );
    expect(screen.getByText("Start from production context")).toBeInTheDocument();
    expect(screen.getByText("PR #42 in assembledhq/143")).toBeInTheDocument();
  });
});
