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

const { pushMock, toastInfo, toastError } = vi.hoisted(() => ({
  pushMock: vi.fn(),
  toastInfo: vi.fn(),
  toastError: vi.fn(),
}));

vi.mock("next/navigation", () => ({
  useRouter: () => ({ push: pushMock, replace: vi.fn() }),
}));

vi.mock("@/lib/notify", () => ({
  notify: {
    info: toastInfo,
    error: toastError,
  },
}));

beforeEach(() => {
  pushMock.mockReset();
  toastInfo.mockReset();
  toastError.mockReset();
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

interface PendingInviteFixture {
  id: string;
  org_id: string;
  org_name: string;
  role: string;
  invited_by: { id: string; name: string };
  expires_at?: string;
  created_at?: string;
}

function mockPendingInvites(invites: PendingInviteFixture[]) {
  const enriched = invites.map((inv) => ({
    expires_at: new Date(Date.now() + 7 * 24 * 60 * 60 * 1000).toISOString(),
    created_at: new Date().toISOString(),
    ...inv,
  }));
  server.use(
    http.get("/api/v1/invitations/pending", () =>
      HttpResponse.json({ data: enriched, meta: {} }),
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

  it("labels member memberships as Engineer in the dropdown", async () => {
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

    expect(await screen.findByTestId("org-switcher-item-org-2")).toHaveTextContent("Engineer");
    expect(screen.getByTestId("org-switcher-item-org-2")).not.toHaveTextContent("member");
  });

  it("switching orgs writes sessionStorage and pushes /sessions", async () => {
    let persistedOrgId: string | null = null;
    mockMemberships(
      [
        { org_id: "org-1", org_name: "Acme", role: "admin" },
        { org_id: "org-2", org_name: "Globex", role: "member" },
      ],
      "org-1",
    );
    server.use(
      http.post("/api/v1/auth/active-org", async ({ request }) => {
        const body = (await request.json()) as { org_id: string };
        persistedOrgId = body.org_id;
        return new HttpResponse(null, { status: 204 });
      }),
    );

    const user = userEvent.setup();
    renderWithProviders(<OrgSwitcher />);

    await user.click(await screen.findByTestId("org-switcher"));
    await user.click(await screen.findByTestId("org-switcher-item-org-2"));

    await waitFor(() => {
      expect(getActiveOrgId()).toBe("org-2");
      expect(persistedOrgId).toBe("org-2");
      expect(pushMock).toHaveBeenCalledWith("/sessions");
    });
  });

  it("switch failure leaves the previous org selected and shows an error", async () => {
    setActiveOrgId("org-1");
    mockMemberships(
      [
        { org_id: "org-1", org_name: "Acme", role: "admin" },
        { org_id: "org-2", org_name: "Globex", role: "member" },
      ],
      "org-1",
    );
    server.use(
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

    const user = userEvent.setup();
    renderWithProviders(<OrgSwitcher />);

    await user.click(await screen.findByTestId("org-switcher"));
    await user.click(await screen.findByTestId("org-switcher-item-org-2"));

    await waitFor(() => {
      expect(toastError).toHaveBeenCalled();
    });
    expect(getActiveOrgId()).toBe("org-1");
    expect(pushMock).not.toHaveBeenCalled();
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

  it("hides the pending-invite dot and section when there are no invitations", async () => {
    mockMemberships([{ org_id: "org-1", org_name: "Acme", role: "admin" }]);
    // Default handler in handlers.ts already returns []; explicit override
    // here makes the intent obvious in the test body.
    server.use(
      http.get("/api/v1/invitations/pending", () =>
        HttpResponse.json({ data: [], meta: {} }),
      ),
    );

    const user = userEvent.setup();
    // userEmail is required because OrgSwitcher gates the pending-invites
    // query on it (production never mounts the switcher pre-auth).
    renderWithProviders(<OrgSwitcher userEmail="u@example.com" />);

    await user.click(await screen.findByTestId("org-switcher"));
    // Wait for the dropdown to open before asserting absence — querying
    // immediately after click would race the popover mount.
    await screen.findByTestId("org-switcher-item-org-1");
    expect(screen.queryByTestId("org-switcher-pending-invite-dot")).not.toBeInTheDocument();
    expect(screen.queryByTestId("pending-invitations-section")).not.toBeInTheDocument();
  });

  it("renders the pending-invite dot and section when there is at least one invitation", async () => {
    mockMemberships([{ org_id: "org-1", org_name: "Acme", role: "admin" }]);
    mockPendingInvites([
      {
        id: "inv-1",
        org_id: "org-2",
        org_name: "Globex",
        role: "member",
        invited_by: { id: "u-2", name: "Bob" },
      },
    ]);

    const user = userEvent.setup();
    renderWithProviders(<OrgSwitcher userEmail="u@example.com" />);

    await waitFor(() => {
      expect(screen.getByTestId("org-switcher-pending-invite-dot")).toBeInTheDocument();
    });

    await user.click(screen.getByTestId("org-switcher"));
    await screen.findByTestId("pending-invitations-section");
    expect(screen.getByTestId("pending-invitation-inv-1")).toHaveTextContent("Globex");
    expect(screen.getByTestId("pending-invitation-inv-1")).toHaveTextContent("Engineer");
    expect(screen.getByTestId("pending-invitation-inv-1")).not.toHaveTextContent("member");
    expect(screen.getByTestId("pending-invitation-inv-1")).toHaveTextContent("Invited by Bob");
  });

  it("accepting an invitation shows the inline confirmation without auto-switching the active org", async () => {
    let acceptCalls = 0;
    let setActiveOrgCalls = 0;
    mockMemberships([{ org_id: "org-1", org_name: "Acme", role: "admin" }], "org-1");
    setActiveOrgId("org-1");
    mockPendingInvites([
      {
        id: "inv-2",
        org_id: "org-3",
        org_name: "Initech",
        role: "member",
        invited_by: { id: "u-3", name: "Bill" },
      },
    ]);
    server.use(
      http.post("/api/v1/invitations/inv-2/accept", () => {
        acceptCalls += 1;
        return HttpResponse.json({ data: { org_id: "org-3", role: "member" } });
      }),
      http.post("/api/v1/auth/active-org", () => {
        setActiveOrgCalls += 1;
        return new HttpResponse(null, { status: 204 });
      }),
    );

    const user = userEvent.setup();
    renderWithProviders(<OrgSwitcher userEmail="u@example.com" />);

    await user.click(await screen.findByTestId("org-switcher"));
    await user.click(await screen.findByTestId("pending-invitation-accept-inv-2"));

    await waitFor(() => {
      expect(acceptCalls).toBe(1);
      expect(screen.getByTestId("pending-invitation-joined-inv-2")).toHaveTextContent("Joined Initech");
    });
    // Critical invariant: in-app accept does NOT teleport the user out of
    // their current workspace. The "Switch to it" link is the explicit opt-in.
    expect(setActiveOrgCalls).toBe(0);
    expect(getActiveOrgId()).toBe("org-1");
    expect(pushMock).not.toHaveBeenCalled();
  });

  it("the pending-invite dot and section disappear after the last invite is accepted and the dropdown reopens", async () => {
    // Transition coverage for the pending → joined → closed-and-reopened = gone
    // state machine. The dot is driven by visiblePendingCount (refetched list
    // minus still-in-justJoined rows), and the section's existence is driven
    // by hasPendingInvites = count > 0 || joinedEntries.length > 0 — so only a
    // close+reopen cycle *plus* the server dropping the row (server filters
    // accepted rows via NOT EXISTS membership) gets us back to neither.
    mockMemberships([{ org_id: "org-1", org_name: "Acme", role: "admin" }], "org-1");
    setActiveOrgId("org-1");
    mockPendingInvites([
      {
        id: "inv-last",
        org_id: "org-9",
        org_name: "Soylent",
        role: "member",
        invited_by: { id: "u-9", name: "Zed" },
      },
    ]);
    server.use(
      http.post("/api/v1/invitations/inv-last/accept", () => {
        // Simulate the server's real behavior: once the membership exists,
        // ListPendingForUser's NOT EXISTS filter drops the row from /pending.
        server.use(
          http.get("/api/v1/invitations/pending", () =>
            HttpResponse.json({ data: [], meta: {} }),
          ),
        );
        return HttpResponse.json({ data: { org_id: "org-9", role: "member" } });
      }),
    );

    const user = userEvent.setup();
    renderWithProviders(<OrgSwitcher userEmail="u@example.com" />);

    // Dot visible before acceptance.
    await waitFor(() => {
      expect(screen.getByTestId("org-switcher-pending-invite-dot")).toBeInTheDocument();
    });

    // Open, accept, see the inline joined confirmation.
    await user.click(screen.getByTestId("org-switcher"));
    await user.click(await screen.findByTestId("pending-invitation-accept-inv-last"));
    await screen.findByTestId("pending-invitation-joined-inv-last");

    // Close the dropdown — Escape rather than clicking the trigger because
    // Radix locks pointer-events on the rest of the document while the menu
    // is open, which makes a second trigger click unreachable in jsdom.
    // Closing clears justJoined so the section can hide on the next open.
    await user.keyboard("{Escape}");

    // Dot is already gone (visiblePendingCount derives from the refetched
    // list, which the server now returns empty). Wait for the refetch to
    // settle before asserting absence so we don't race the invalidation.
    await waitFor(() => {
      expect(screen.queryByTestId("org-switcher-pending-invite-dot")).not.toBeInTheDocument();
    });

    // Reopen — section must not render.
    await user.click(screen.getByTestId("org-switcher"));
    await screen.findByTestId("org-switcher-item-org-1");
    expect(screen.queryByTestId("pending-invitations-section")).not.toBeInTheDocument();
  });

  it("the post-accept Switch to it link sets the active org and navigates", async () => {
    mockMemberships([{ org_id: "org-1", org_name: "Acme", role: "admin" }], "org-1");
    setActiveOrgId("org-1");
    mockPendingInvites([
      {
        id: "inv-3",
        org_id: "org-4",
        org_name: "Pied Piper",
        role: "admin",
        invited_by: { id: "u-4", name: "Erlich" },
      },
    ]);
    let switchedTo: string | null = null;
    server.use(
      http.post("/api/v1/invitations/inv-3/accept", () =>
        HttpResponse.json({ data: { org_id: "org-4", role: "admin" } }),
      ),
      http.post("/api/v1/auth/active-org", async ({ request }) => {
        const body = (await request.json()) as { org_id: string };
        switchedTo = body.org_id;
        return new HttpResponse(null, { status: 204 });
      }),
    );

    const user = userEvent.setup();
    renderWithProviders(<OrgSwitcher userEmail="u@example.com" />);

    await user.click(await screen.findByTestId("org-switcher"));
    await user.click(await screen.findByTestId("pending-invitation-accept-inv-3"));
    await screen.findByTestId("pending-invitation-joined-inv-3");
    await user.click(screen.getByTestId("pending-invitation-switch-inv-3"));

    await waitFor(() => {
      expect(switchedTo).toBe("org-4");
      expect(getActiveOrgId()).toBe("org-4");
      expect(pushMock).toHaveBeenCalledWith("/sessions");
    });
  });

  it("an accept that returns 410 surfaces an error toast and refetches the list", async () => {
    mockMemberships([{ org_id: "org-1", org_name: "Acme", role: "admin" }], "org-1");
    setActiveOrgId("org-1");
    mockPendingInvites([
      {
        id: "inv-stale",
        org_id: "org-9",
        org_name: "Vandelay",
        role: "member",
        invited_by: { id: "u-9", name: "Art" },
      },
    ]);
    let listCallsAfterAccept = 0;
    server.use(
      http.post("/api/v1/invitations/inv-stale/accept", () => {
        // Simulate the row being revoked or accepted by another flow between
        // the dropdown's last poll and the user's click.
        server.use(
          http.get("/api/v1/invitations/pending", () => {
            listCallsAfterAccept += 1;
            return HttpResponse.json({ data: [], meta: {} });
          }),
        );
        return HttpResponse.json(
          {
            error: { code: "INVITE_INVALID", message: "this invitation is no longer valid" },
          },
          { status: 410 },
        );
      }),
    );

    const user = userEvent.setup();
    renderWithProviders(<OrgSwitcher userEmail="u@example.com" />);

    await user.click(await screen.findByTestId("org-switcher"));
    await user.click(await screen.findByTestId("pending-invitation-accept-inv-stale"));

    await waitFor(() => {
      expect(toastError).toHaveBeenCalled();
      expect(listCallsAfterAccept).toBeGreaterThanOrEqual(1);
      expect(screen.queryByTestId("pending-invitation-joined-inv-stale")).not.toBeInTheDocument();
    });
    // Active org is unchanged — the failed accept must not move the user.
    expect(getActiveOrgId()).toBe("org-1");
  });

  it("declining an invitation calls the API and refetches the list", async () => {
    mockMemberships([{ org_id: "org-1", org_name: "Acme", role: "admin" }]);
    mockPendingInvites([
      {
        id: "inv-4",
        org_id: "org-5",
        org_name: "Hooli",
        role: "viewer",
        invited_by: { id: "u-5", name: "Gavin" },
      },
    ]);
    let declineCalls = 0;
    let listCallsAfterDecline = 0;
    server.use(
      http.post("/api/v1/invitations/inv-4/decline", () => {
        declineCalls += 1;
        // Subsequent /pending GETs return an empty list — the row should
        // disappear from the dropdown and the dot should hide.
        server.use(
          http.get("/api/v1/invitations/pending", () => {
            listCallsAfterDecline += 1;
            return HttpResponse.json({ data: [], meta: {} });
          }),
        );
        return new HttpResponse(null, { status: 204 });
      }),
    );

    const user = userEvent.setup();
    renderWithProviders(<OrgSwitcher userEmail="u@example.com" />);

    await user.click(await screen.findByTestId("org-switcher"));
    await user.click(await screen.findByTestId("pending-invitation-decline-inv-4"));

    await waitFor(() => {
      expect(declineCalls).toBe(1);
      expect(listCallsAfterDecline).toBeGreaterThanOrEqual(1);
      expect(screen.queryByTestId("pending-invitation-inv-4")).not.toBeInTheDocument();
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

  describe("domain-joinable workspaces", () => {
    function mockJoinable(
      orgs: Array<{ org_id: string; org_name: string; domain: string }>,
      emailVerificationRequired = false,
    ) {
      server.use(
        http.get("/api/v1/orgs/joinable", () =>
          HttpResponse.json({ data: orgs, email_verification_required: emailVerificationRequired }),
        ),
      );
    }

    it("renders the joinable section with the verified-domain hint", async () => {
      mockMemberships([{ org_id: "org-1", org_name: "Mine", role: "admin" }]);
      mockJoinable([{ org_id: "org-2", org_name: "Assembled", domain: "assembledhq.com" }]);

      renderWithProviders(<OrgSwitcher userEmail="alice@assembledhq.com" />);
      await userEvent.click(await screen.findByTestId("org-switcher"));

      expect(await screen.findByTestId("joinable-orgs-section")).toBeInTheDocument();
      expect(screen.getByText("Assembled")).toBeInTheDocument();
      expect(screen.getByText(/assembledhq\.com email grants access/)).toBeInTheDocument();
    });

    it("does not offer joining an org the user already belongs to", async () => {
      mockMemberships([{ org_id: "org-2", org_name: "Assembled", role: "member" }]);
      // Stale joinable response naming an org the memberships cache knows.
      mockJoinable([{ org_id: "org-2", org_name: "Assembled", domain: "assembledhq.com" }]);

      renderWithProviders(<OrgSwitcher userEmail="alice@assembledhq.com" />);
      await userEvent.click(await screen.findByTestId("org-switcher"));

      await waitFor(() => {
        expect(screen.getByTestId("org-switcher-item-org-2")).toBeInTheDocument();
      });
      expect(screen.queryByTestId("joinable-orgs-section")).not.toBeInTheDocument();
    });

    it("joining shows the inline confirmation without auto-switching", async () => {
      mockMemberships([{ org_id: "org-1", org_name: "Mine", role: "admin" }]);
      mockJoinable([{ org_id: "org-2", org_name: "Assembled", domain: "assembledhq.com" }]);
      server.use(
        http.post("/api/v1/orgs/org-2/join", () =>
          HttpResponse.json({ data: { org_id: "org-2", org_name: "Assembled", role: "member" } }),
        ),
      );

      renderWithProviders(<OrgSwitcher userEmail="alice@assembledhq.com" />);
      await userEvent.click(await screen.findByTestId("org-switcher"));
      await userEvent.click(await screen.findByTestId("joinable-org-join-org-2"));

      expect(await screen.findByText("Joined Assembled")).toBeInTheDocument();
      expect(getActiveOrgId()).toBeNull();
    });

    it("a join rejected as NOT_ELIGIBLE surfaces an error toast", async () => {
      mockMemberships([{ org_id: "org-1", org_name: "Mine", role: "admin" }]);
      mockJoinable([{ org_id: "org-2", org_name: "Assembled", domain: "assembledhq.com" }]);
      server.use(
        http.post("/api/v1/orgs/org-2/join", () =>
          HttpResponse.json(
            { error: { code: "NOT_ELIGIBLE", message: "no" } },
            { status: 403 },
          ),
        ),
      );

      renderWithProviders(<OrgSwitcher userEmail="alice@assembledhq.com" />);
      await userEvent.click(await screen.findByTestId("org-switcher"));
      await userEvent.click(await screen.findByTestId("joinable-org-join-org-2"));

      await waitFor(() => {
        expect(toastError).toHaveBeenCalledWith(
          expect.stringContaining("no longer available"),
        );
      });
    });

    it("prompts unverified users to verify their email without naming the org", async () => {
      mockMemberships([{ org_id: "org-1", org_name: "Mine", role: "admin" }]);
      mockJoinable([], true);

      renderWithProviders(<OrgSwitcher userEmail="bob@assembledhq.com" />);
      await userEvent.click(await screen.findByTestId("org-switcher"));

      expect(await screen.findByTestId("verify-email-prompt")).toBeInTheDocument();
      expect(screen.getByText("Your team has a workspace")).toBeInTheDocument();
      expect(screen.queryByTestId("joinable-orgs-section")).not.toBeInTheDocument();
    });
  });
});
