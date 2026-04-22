import { describe, expect, it, vi, beforeEach } from "vitest";
import { http, HttpResponse } from "msw";
import { renderWithProviders, screen, waitFor } from "@/test/test-utils";
import { server } from "@/test/mocks/server";
import NewAutomationPage from "./page";

const pushMock = vi.fn();
const searchParams = new URLSearchParams("template=security-sweep");

vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: pushMock }),
  useSearchParams: () => searchParams,
}));

describe("NewAutomationPage", () => {
  beforeEach(() => {
    pushMock.mockReset();
  });

  it("prefills the form from the selected template and links to the full library", async () => {
    server.use(
      http.get("/api/v1/repositories", () =>
        HttpResponse.json({
          data: [
            {
              id: "repo-1",
              org_id: "org-1",
              integration_id: "int-1",
              github_id: 1,
              full_name: "acme/repo",
              default_branch: "main",
              private: false,
              clone_url: "https://github.com/acme/repo.git",
              installation_id: 10,
              status: "active",
              settings: {},
              created_at: "2026-03-05T12:00:00Z",
              updated_at: "2026-03-05T12:00:00Z",
            },
          ],
          meta: {},
        }),
      ),
    );

    renderWithProviders(<NewAutomationPage />);

    await waitFor(() => {
      expect(screen.getByDisplayValue("Security sweep")).toBeInTheDocument();
    });

    expect(screen.getByRole("link", { name: /Browse all templates/i })).toHaveAttribute(
      "href",
      "/automations/templates",
    );
    expect(
      (screen.getByLabelText("Goal") as HTMLTextAreaElement).value,
    ).toContain(
      "Review the repository for concrete, actionable security risk",
    );
  });
});
