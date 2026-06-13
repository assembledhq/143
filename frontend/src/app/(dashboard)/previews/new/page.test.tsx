import { beforeEach, describe, expect, it, vi } from "vitest";
import { http, HttpResponse } from "msw";
import { renderWithProviders, screen, waitFor, userEvent } from "@/test/test-utils";
import { server } from "@/test/mocks/server";
import NewPreviewPage from "./page";
import type { Repository } from "@/lib/types";

const searchParamsMock = vi.hoisted(() => ({ value: new URLSearchParams() }));
const pushMock = vi.hoisted(() => vi.fn());

vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: pushMock }),
  useSearchParams: () => searchParamsMock.value,
}));

const repositories: Repository[] = [
  {
    id: "repo-1",
    org_id: "org-1",
    integration_id: "integration-1",
    full_name: "assembledhq/api",
    default_branch: "main",
    private: false,
    clone_url: "https://github.com/assembledhq/api.git",
    github_id: 101,
    installation_id: 1,
    status: "active",
    settings: {},
    created_at: "2026-06-10T12:00:00Z",
    updated_at: "2026-06-10T12:00:00Z",
  },
  {
    id: "repo-2",
    org_id: "org-1",
    integration_id: "integration-1",
    full_name: "assembledhq/web",
    default_branch: "develop",
    private: false,
    clone_url: "https://github.com/assembledhq/web.git",
    github_id: 102,
    installation_id: 1,
    status: "active",
    settings: {},
    created_at: "2026-06-10T12:00:00Z",
    updated_at: "2026-06-10T12:00:00Z",
  },
];

function installHandlers() {
  server.use(
    http.get("*/api/v1/repositories", () =>
      HttpResponse.json({ data: repositories, meta: {} }),
    ),
    http.get("*/api/v1/repositories/:id/branches", () =>
      HttpResponse.json({
        data: [
          { name: "develop", protected: true },
          { name: "feature/session-input-branch", protected: false },
        ],
        meta: {},
      }),
    ),
    http.get("*/api/v1/previews", () =>
      HttpResponse.json({ data: [], meta: {} }),
    ),
    http.get("*/api/v1/previews/configs", () =>
      HttpResponse.json({
        data: {
          names: [],
          default_name: "",
          selected_name: "",
          requires_selection: false,
        },
      }),
    ),
  );
}

describe("NewPreviewPage", () => {
  beforeEach(() => {
    searchParamsMock.value = new URLSearchParams();
    pushMock.mockReset();
  });

  it("preselects the repository and branch from the session input query params", async () => {
    searchParamsMock.value = new URLSearchParams({
      repo: "repo-2",
      branch: "feature/session-input-branch",
    });
    installHandlers();

    renderWithProviders(<NewPreviewPage />);

    await waitFor(() => {
      expect(screen.getByRole("combobox", { name: "Repository" })).toHaveTextContent(
        "assembledhq/web",
      );
    });
    expect(
      screen.getByRole("button", { name: "Target branch" }),
    ).toHaveTextContent("feature/session-input-branch");
  });

  it("falls back to the preselected repository default branch when no branch param exists", async () => {
    searchParamsMock.value = new URLSearchParams({ repo: "repo-2" });
    installHandlers();

    renderWithProviders(<NewPreviewPage />);

    await waitFor(() => {
      expect(screen.getByRole("combobox", { name: "Repository" })).toHaveTextContent(
        "assembledhq/web",
      );
    });
    await waitFor(() => {
      expect(screen.getByRole("button", { name: "Target branch" })).toHaveTextContent(
        "develop",
      );
    });
  });

  it("navigates to /previews when the dialog is closed", async () => {
    installHandlers();

    renderWithProviders(<NewPreviewPage />);

    await screen.findByRole("dialog", { name: "Create preview" });
    await userEvent.click(screen.getByRole("button", { name: /cancel/i }));

    expect(pushMock).toHaveBeenCalledWith("/previews");
  });
});
