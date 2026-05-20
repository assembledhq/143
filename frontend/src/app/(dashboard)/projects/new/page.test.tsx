import { describe, it, expect, vi } from "vitest";
import { renderWithProviders, screen, userEvent, waitFor } from "@/test/test-utils";
import { server } from "@/test/mocks/server";
import { http, HttpResponse } from "msw";
import NewProjectPage from "./page";

const pushMock = vi.fn();
const replaceMock = vi.fn();
const currentUserRole = vi.hoisted(() => ({ value: "member" }));

vi.mock("next/navigation", () => ({
  useRouter: () => ({
    push: pushMock,
    replace: replaceMock,
  }),
  useSearchParams: () => new URLSearchParams(),
}));

vi.mock("@/hooks/use-auth", () => ({
  useAuth: () => ({
    user: { role: currentUserRole.value },
    isLoading: false,
  }),
}));

describe("NewProjectPage", () => {
  it("submits the selected base branch from the branch picker", async () => {
    currentUserRole.value = "member";
    const user = userEvent.setup();
    let requestBody: Record<string, unknown> | null = null;

    server.use(
      http.get("*/api/v1/settings", () => HttpResponse.json({
        data: {
          id: "org-1",
          name: "Acme",
          settings: { default_agent_type: "codex" },
        },
      })),
      http.get("*/api/v1/repositories", () => HttpResponse.json({
        data: [
          {
            id: "repo-1",
            org_id: "org-1",
            full_name: "acme/api",
            default_branch: "main",
            github_id: 1,
            created_at: "2026-01-01T00:00:00Z",
            updated_at: "2026-01-01T00:00:00Z",
          },
        ],
        meta: {},
      })),
      http.get("*/api/v1/repositories/:id/branches", () => HttpResponse.json({
        data: [
          { name: "main", protected: true },
          { name: "release/2026.04", protected: true },
        ],
        meta: {},
      })),
      http.post("*/api/v1/projects", async ({ request }) => {
        requestBody = await request.json() as Record<string, unknown>;
        return HttpResponse.json({ data: { id: "proj-1" } });
      }),
    );

    renderWithProviders(<NewProjectPage />);

    await user.type(screen.getByLabelText("Title"), "Ship branch picker");
    await user.type(screen.getByLabelText("Goal"), "Replace exact branch inputs");
    await user.click(screen.getAllByRole("combobox")[0]);
    await user.click(await screen.findByText("acme/api"));

    await user.click(screen.getByText("Advanced options"));
    await user.click(await screen.findByRole("button", { name: "Base branch" }));
    await user.type(await screen.findByPlaceholderText("Search branches..."), "release");
    await user.click(await screen.findByText("release/2026.04"));

    await user.click(screen.getByRole("button", { name: "Create project" }));

    await waitFor(() => {
      expect(requestBody).toMatchObject({
        repository_id: "repo-1",
        base_branch: "release/2026.04",
      });
    });
  }, 20000);

  it("redirects builders away from the new project form", async () => {
    currentUserRole.value = "builder";

    renderWithProviders(<NewProjectPage />);

    await waitFor(() => {
      expect(replaceMock).toHaveBeenCalledWith("/projects");
    });
  });
});
