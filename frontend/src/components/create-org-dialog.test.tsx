import { describe, it, expect, vi, beforeEach } from "vitest";
import { http, HttpResponse } from "msw";
import { renderWithProviders, screen, userEvent, waitFor } from "@/test/test-utils";
import { server } from "@/test/mocks/server";
import { CreateOrgDialog } from "./create-org-dialog";
import { getActiveOrgId } from "@/lib/active-org";

const { pushMock, toastSuccess, toastInfo, toastError } = vi.hoisted(() => ({
  pushMock: vi.fn(),
  toastSuccess: vi.fn(),
  toastInfo: vi.fn(),
  toastError: vi.fn(),
}));

vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: pushMock, replace: vi.fn() }),
}));

vi.mock("@/lib/notify", () => ({
  notify: { success: toastSuccess, info: toastInfo, error: toastError },
}));

beforeEach(() => {
  pushMock.mockReset();
  toastSuccess.mockReset();
  toastInfo.mockReset();
  toastError.mockReset();
  window.sessionStorage.clear();
});

describe("CreateOrgDialog", () => {
  it("on success: sets active org, pushes /sessions, closes, toasts", async () => {
    let persistedOrgId: string | null = null;
    server.use(
      http.post("/api/v1/organizations", async () => {
        return HttpResponse.json(
          {
            data: {
              id: "org-new",
              name: "Acme",
              role: "admin",
              created_at: "2026-04-21T00:00:00Z",
            },
          },
          { status: 201 },
        );
      }),
      http.post("/api/v1/auth/active-org", async ({ request }) => {
        const body = (await request.json()) as { org_id: string };
        persistedOrgId = body.org_id;
        return new HttpResponse(null, { status: 204 });
      }),
    );

    const onOpenChange = vi.fn();
    const user = userEvent.setup();
    renderWithProviders(<CreateOrgDialog open={true} onOpenChange={onOpenChange} />);

    await user.type(screen.getByLabelText("Name"), "Acme");
    await user.click(screen.getByRole("button", { name: "Create" }));

    await waitFor(() => {
      expect(pushMock).toHaveBeenCalledWith("/sessions");
    });
    expect(getActiveOrgId()).toBe("org-new");
    expect(persistedOrgId).toBe("org-new");
    expect(toastSuccess).toHaveBeenCalledWith("Created Acme");
    expect(onOpenChange).toHaveBeenLastCalledWith(false);
  });

  it("warns when the org is created but the default workspace could not be saved", async () => {
    server.use(
      http.post("/api/v1/organizations", async () => {
        return HttpResponse.json(
          {
            data: {
              id: "org-new",
              name: "Acme",
              role: "admin",
              created_at: "2026-04-21T00:00:00Z",
            },
          },
          { status: 201 },
        );
      }),
      http.post("/api/v1/auth/active-org", () =>
        HttpResponse.json(
          {
            error: {
              code: "ACTIVE_ORG_UPDATE_FAILED",
              message: "failed to persist active organization",
            },
          },
          { status: 500 },
        ),
      ),
    );

    const onOpenChange = vi.fn();
    const user = userEvent.setup();
    renderWithProviders(<CreateOrgDialog open={true} onOpenChange={onOpenChange} />);

    await user.type(screen.getByLabelText("Name"), "Acme");
    await user.click(screen.getByRole("button", { name: "Create" }));

    await waitFor(() => {
      expect(pushMock).toHaveBeenCalledWith("/sessions");
      expect(toastError).toHaveBeenCalled();
    });
    expect(getActiveOrgId()).toBe("org-new");
    expect(toastSuccess).not.toHaveBeenCalled();
    expect(onOpenChange).toHaveBeenLastCalledWith(false);
  });

  it("maps CREATE_ORG_RATE_LIMITED to the human-readable copy", async () => {
    server.use(
      http.post("/api/v1/organizations", () =>
        HttpResponse.json(
          {
            error: {
              code: "CREATE_ORG_RATE_LIMITED",
              message: "too many organization-creation attempts; try again later",
            },
          },
          { status: 429 },
        ),
      ),
    );

    const user = userEvent.setup();
    renderWithProviders(<CreateOrgDialog open={true} onOpenChange={vi.fn()} />);

    await user.type(screen.getByLabelText("Name"), "Acme");
    await user.click(screen.getByRole("button", { name: "Create" }));

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(/too many organizations/i);
    // Active org must not change on failure.
    expect(getActiveOrgId()).toBeNull();
    expect(pushMock).not.toHaveBeenCalled();
  });

  it("maps NAME_TOO_LONG from the server to a length-specific message", async () => {
    server.use(
      http.post("/api/v1/organizations", () =>
        HttpResponse.json(
          {
            error: { code: "NAME_TOO_LONG", message: "Name must be 120 characters or fewer." },
          },
          { status: 400 },
        ),
      ),
    );

    const user = userEvent.setup();
    renderWithProviders(<CreateOrgDialog open={true} onOpenChange={vi.fn()} />);

    await user.type(screen.getByLabelText("Name"), "Acme");
    await user.click(screen.getByRole("button", { name: "Create" }));

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(/120 characters or fewer/i);
  });

  it("disables Create while the name is empty or whitespace-only", async () => {
    const user = userEvent.setup();
    renderWithProviders(<CreateOrgDialog open={true} onOpenChange={vi.fn()} />);

    const createButton = screen.getByRole("button", { name: "Create" });
    expect(createButton).toBeDisabled();

    await user.type(screen.getByLabelText("Name"), "   ");
    expect(createButton).toBeDisabled();

    await user.type(screen.getByLabelText("Name"), "Acme");
    expect(createButton).not.toBeDisabled();
  });
});
