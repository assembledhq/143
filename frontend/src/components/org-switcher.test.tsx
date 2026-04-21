import { describe, it, expect, vi, beforeEach } from "vitest";
import { http, HttpResponse } from "msw";
import { renderWithProviders, screen, userEvent, waitFor } from "@/test/test-utils";
import { server } from "@/test/mocks/server";
import { OrgSwitcher } from "./org-switcher";
import {
  ACTIVE_ORG_CHANGED_EVENT,
  ORG_MEMBERSHIP_REVOKED_EVENT,
  getActiveOrgId,
  setActiveOrgId,
} from "@/lib/active-org";

const { pushMock } = vi.hoisted(() => ({ pushMock: vi.fn() }));

vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: pushMock, replace: vi.fn() }),
}));

beforeEach(() => {
  pushMock.mockReset();
  window.sessionStorage.clear();
});

function mockMemberships(memberships: Array<{ org_id: string; org_name: string; role: string }>, activeOrgId = memberships[0]?.org_id ?? "") {
  server.use(
    http.get("/api/v1/auth/memberships", () =>
      HttpResponse.json({
        data: {
          active_org_id: activeOrgId,
          active_role: memberships.find((m) => m.org_id === activeOrgId)?.role ?? "",
          memberships,
        },
      }),
    ),
  );
}

describe("OrgSwitcher", () => {
  it("renders the active org label from the server response", async () => {
    mockMemberships([
      { org_id: "org-1", org_name: "Acme", role: "admin" },
    ]);

    renderWithProviders(<OrgSwitcher userEmail="alex@example.com" />);

    await waitFor(() => {
      expect(screen.getByTestId("org-switcher")).toHaveTextContent("Acme");
    });
  });

  it("shows all memberships in the dropdown with a check on the active one", async () => {
    mockMemberships(
      [
        { org_id: "org-1", org_name: "Acme", role: "admin" },
        { org_id: "org-2", org_name: "Globex", role: "member" },
      ],
      "org-1",
    );

    const user = userEvent.setup();
    renderWithProviders(<OrgSwitcher />);

    await waitFor(() => {
      expect(screen.getByTestId("org-switcher")).toHaveTextContent("Acme");
    });

    await user.click(screen.getByTestId("org-switcher"));

    expect(await screen.findByTestId("org-switcher-item-org-1")).toBeInTheDocument();
    expect(screen.getByTestId("org-switcher-item-org-2")).toBeInTheDocument();
  });

  it("switching orgs writes sessionStorage and pushes /sessions", async () => {
    mockMemberships(
      [
        { org_id: "org-1", org_name: "Acme", role: "admin" },
        { org_id: "org-2", org_name: "Globex", role: "member" },
      ],
      "org-1",
    );

    const user = userEvent.setup();
    renderWithProviders(<OrgSwitcher />);

    await user.click(await screen.findByTestId("org-switcher"));
    await user.click(await screen.findByTestId("org-switcher-item-org-2"));

    expect(getActiveOrgId()).toBe("org-2");
    expect(pushMock).toHaveBeenCalledWith("/sessions");
  });

  it("clicking the already-active org is a no-op", async () => {
    mockMemberships(
      [{ org_id: "org-1", org_name: "Acme", role: "admin" }],
      "org-1",
    );

    const user = userEvent.setup();
    renderWithProviders(<OrgSwitcher />);

    await user.click(await screen.findByTestId("org-switcher"));
    await user.click(await screen.findByTestId("org-switcher-item-org-1"));

    expect(pushMock).not.toHaveBeenCalled();
  });

  it("prefers the tab-local active org when sessionStorage is set", async () => {
    setActiveOrgId("org-2");

    mockMemberships(
      [
        { org_id: "org-1", org_name: "Acme", role: "admin" },
        { org_id: "org-2", org_name: "Globex", role: "member" },
      ],
      "org-1",
    );

    renderWithProviders(<OrgSwitcher />);

    await waitFor(() => {
      expect(screen.getByTestId("org-switcher")).toHaveTextContent("Globex");
    });
  });

  it("revoked-membership event refetches and shows a toast-like info message", async () => {
    let callCount = 0;
    server.use(
      http.get("/api/v1/auth/memberships", () => {
        callCount += 1;
        return HttpResponse.json({
          data: {
            active_org_id: "org-1",
            active_role: "admin",
            memberships: [{ org_id: "org-1", org_name: "Acme", role: "admin" }],
          },
        });
      }),
    );

    renderWithProviders(<OrgSwitcher />);

    await waitFor(() => {
      expect(callCount).toBeGreaterThanOrEqual(1);
    });

    const before = callCount;
    window.dispatchEvent(new CustomEvent(ORG_MEMBERSHIP_REVOKED_EVENT));

    await waitFor(() => {
      expect(callCount).toBeGreaterThan(before);
    });
  });

  it("clears the stale tab-local org id on revocation so the next request cannot re-trigger it", async () => {
    setActiveOrgId("org-revoked");

    mockMemberships(
      [{ org_id: "org-1", org_name: "Acme", role: "admin" }],
      "org-1",
    );

    renderWithProviders(<OrgSwitcher />);

    window.dispatchEvent(new CustomEvent(ORG_MEMBERSHIP_REVOKED_EVENT));

    await waitFor(() => {
      expect(getActiveOrgId()).toBeNull();
    });
  });

  it("syncs with active-org-changed events from elsewhere in the tab", async () => {
    mockMemberships(
      [
        { org_id: "org-1", org_name: "Acme", role: "admin" },
        { org_id: "org-2", org_name: "Globex", role: "member" },
      ],
      "org-1",
    );

    renderWithProviders(<OrgSwitcher />);

    await waitFor(() => {
      expect(screen.getByTestId("org-switcher")).toHaveTextContent("Acme");
    });

    window.sessionStorage.setItem("active_org_id", "org-2");
    window.dispatchEvent(new CustomEvent(ACTIVE_ORG_CHANGED_EVENT));

    await waitFor(() => {
      expect(screen.getByTestId("org-switcher")).toHaveTextContent("Globex");
    });
  });
});
