"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useRouter } from "next/navigation";
import { Check, ChevronsUpDown, Mail, Plus, Settings } from "lucide-react";
import { notify as toast } from "@/lib/notify";

import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Input } from "@/components/ui/input";
import { api } from "@/lib/api";
import {
  ACTIVE_ORG_CHANGED_EVENT,
  ORG_MEMBERSHIP_REVOKED_EVENT,
  getActiveOrgId,
  setActiveOrgId,
} from "@/lib/active-org";
import { cn } from "@/lib/utils";
import { queryKeys } from "@/lib/query-keys";
import { ApiError } from "@/lib/api";
import { roleLabel } from "@/lib/roles";
import { CreateOrgDialog } from "@/components/create-org-dialog";
import { usePendingInvites } from "@/hooks/use-pending-invites";
import type {
  MembershipSummary,
  PendingInvitationForUser,
  SingleResponse,
  MembershipsResponse,
} from "@/lib/types";

const SEARCH_THRESHOLD = 5;
// Threshold for the "expires soon" hint on a pending-invite row. Below 24h
// the user sees an inline warning; above it the expiration is left implicit
// so the section doesn't shout at them about invites that are days away.
const EXPIRES_SOON_MS = 24 * 60 * 60 * 1000;

export interface OrgSwitcherProps {
  userEmail?: string;
}

function messageForActiveOrgSwitchError(err: unknown, orgName: string): string {
  const code = typeof err === "object" && err !== null ? (err as { code?: unknown }).code : undefined;
  switch (code) {
    case "UNAUTHORIZED":
      return "Your session expired. Please sign in again before switching workspaces.";
    default:
      return `Couldn't switch to ${orgName}. Please try again.`;
  }
}

// JoinedOrg is the inline confirmation row that replaces an accepted-invite
// row inside the dropdown until the dropdown closes. Holding a snapshot of
// the org name + id (rather than re-deriving from memberships on the next
// poll) lets the "Joined Acme. [Switch to it]" affordance render instantly
// without flickering through a "loading…" state while memberships refetch.
interface JoinedOrg {
  org_id: string;
  org_name: string;
}

export function OrgSwitcher({ userEmail }: OrgSwitcherProps) {
  const router = useRouter();
  const queryClient = useQueryClient();

  const { data: membershipsResponse } = useQuery<SingleResponse<MembershipsResponse>>({
    queryKey: queryKeys.auth.memberships,
    queryFn: () => api.auth.memberships(),
  });

  // Gate the pending-invites query on having a confirmed user identity. The
  // switcher mounts inside AuthenticatedLayout so userEmail is set in
  // practice; the explicit gate keeps a future pre-auth render from polling
  // /api/v1/invitations/pending and producing 401 noise in the console.
  const pendingInvites = usePendingInvites({ enabled: !!userEmail });

  const [tabOrgId, setTabOrgId] = useState<string | null>(() => getActiveOrgId());
  const [search, setSearch] = useState("");
  const [createOpen, setCreateOpen] = useState(false);
  // Tracks invitations the user just accepted in this session so the row
  // collapses into a confirmation. Cleared when the dropdown closes; the
  // next open shows the fresh state from the refetched pending list.
  const [justJoined, setJustJoined] = useState<Record<string, JoinedOrg>>({});
  // Wall-clock snapshot for the per-row "expires soon" annotation. Captured
  // once on mount and re-captured whenever a fresh pending-invites result
  // arrives (see the effect below) so a dropdown left open across the
  // 5-minute poll still sees an accurate badge. Reading from state rather
  // than calling Date.now() mid-render keeps the render pure.
  const [nowMs, setNowMs] = useState<number>(() => Date.now());

  const memberships = useMemo(
    () => membershipsResponse?.data?.memberships ?? [],
    [membershipsResponse],
  );
  const serverActiveOrgId = membershipsResponse?.data?.active_org_id ?? "";

  // Prefer the tab-local selection; fall back to the server-resolved active
  // org until the user picks one explicitly. Using the server value here keeps
  // the switcher showing the same org the backend will scope requests to on
  // the very first page load of a fresh tab (no X-Active-Org-ID sent yet).
  const effectiveActiveOrgId = tabOrgId ?? serverActiveOrgId;
  const activeMembership = useMemo(
    () => memberships.find((m) => m.org_id === effectiveActiveOrgId) ?? memberships[0],
    [memberships, effectiveActiveOrgId],
  );

  // Keep tabOrgId in sync with sessionStorage changes from other code paths
  // in the same tab (e.g. Create Org finishing, another component calling
  // setActiveOrgId). Cross-tab sync is intentionally not wired up — per-tab
  // org is the whole point.
  useEffect(() => {
    const handler = () => setTabOrgId(getActiveOrgId());
    window.addEventListener(ACTIVE_ORG_CHANGED_EVENT, handler);
    return () => window.removeEventListener(ACTIVE_ORG_CHANGED_EVENT, handler);
  }, []);

  // Name the currently-active org in the revocation toast. Capturing via ref
  // lets us read the label the user *had* right before the server signaled
  // revocation without making invalidateQueries race our own read of it.
  const activeMembershipRef = useRef(activeMembership);
  useEffect(() => {
    activeMembershipRef.current = activeMembership;
  }, [activeMembership]);

  useEffect(() => {
    const handler = () => {
      const previousLabel = activeMembershipRef.current?.org_name;
      // Drop the stale tab selection so the next request doesn't resend the
      // revoked org id and trip the same response header in a loop. Falling
      // back to null lets effectiveActiveOrgId pick up whatever org the
      // server resolves for this user on the next fetch.
      setActiveOrgId(null);
      // clear() drops cached data without kicking off refetches against the
      // current page. Mounted queries will fetch fresh data with the new
      // active-org resolution; unmounted queries won't waste a request.
      queryClient.clear();
      toast.info(
        previousLabel
          ? `Your access to ${previousLabel} changed. Switched to your next available workspace.`
          : "Your access to an organization changed. Switched to your next available workspace.",
        { id: "org-membership-revoked" },
      );
    };
    window.addEventListener(ORG_MEMBERSHIP_REVOKED_EVENT, handler);
    return () => window.removeEventListener(ORG_MEMBERSHIP_REVOKED_EVENT, handler);
  }, [queryClient]);

  const showSearch = memberships.length >= SEARCH_THRESHOLD;
  const filtered = useMemo(() => {
    if (!search) return memberships;
    const q = search.toLowerCase();
    return memberships.filter((m) => m.org_name.toLowerCase().includes(q));
  }, [memberships, search]);

  // Shared between the regular membership switch and the post-accept
  // "Switch to it" affordance. Takes the minimal {org_id, org_name} shape
  // so the caller can pass either a MembershipSummary or a freshly-claimed
  // JoinedOrg without going through the memberships cache (which may not
  // have refetched yet in the just-accepted case).
  //
  // clear() over invalidateQueries(): invalidating here would refetch every
  // cached query against the *current* page right before it unmounts on
  // navigation — doubling request volume. Clearing drops the cache so the
  // destination page fetches fresh under the new active-org header.
  const activateOrgAndNavigate = useCallback(
    async (org: { org_id: string; org_name: string }) => {
      try {
        await api.auth.setActiveOrg(org.org_id);
      } catch (err: unknown) {
        toast.error(messageForActiveOrgSwitchError(err, org.org_name));
        return;
      }
      setActiveOrgId(org.org_id);
      queryClient.clear();
      router.push("/sessions");
    },
    [queryClient, router],
  );

  const handleSwitch = async (membership: MembershipSummary) => {
    if (membership.org_id === effectiveActiveOrgId) return;
    await activateOrgAndNavigate(membership);
  };

  const handleSwitchToJoined = useCallback(
    (joined: JoinedOrg) => activateOrgAndNavigate(joined),
    [activateOrgAndNavigate],
  );

  const handleAcceptInvite = useCallback(
    async (invite: PendingInvitationForUser) => {
      try {
        const result = await pendingInvites.accept(invite.id);
        setJustJoined((prev) => ({
          ...prev,
          [invite.id]: { org_id: result.org_id, org_name: invite.org_name },
        }));
      } catch (err: unknown) {
        // 410 (INVITE_INVALID / INVITE_EXPIRED) means the row got out from
        // under us — refetch silently so the dropdown matches reality on the
        // next open, but tell the user what happened.
        if (
          err instanceof ApiError &&
          (err.code === "INVITE_INVALID" || err.code === "INVITE_EXPIRED")
        ) {
          pendingInvites.refetch();
          toast.error(`This invitation to ${invite.org_name} is no longer valid.`);
          return;
        }
        toast.error(`Couldn't accept the invitation to ${invite.org_name}. Please try again.`);
      }
    },
    [pendingInvites],
  );

  const handleDeclineInvite = useCallback(
    async (invite: PendingInvitationForUser) => {
      try {
        await pendingInvites.decline(invite.id);
      } catch (err: unknown) {
        // 410 (INVITE_INVALID / INVITE_EXPIRED) means an admin revoked or a
        // concurrent decline landed first. From the user's perspective the
        // row they wanted gone *is* gone, so refetch quietly and let them
        // know the list was already out of sync — same pattern as accept,
        // but with toast.info instead of .error since the end state matches
        // what they asked for.
        if (
          err instanceof ApiError &&
          (err.code === "INVITE_INVALID" || err.code === "INVITE_EXPIRED")
        ) {
          pendingInvites.refetch();
          toast.info(`The invitation to ${invite.org_name} was already resolved.`);
          return;
        }
        toast.error(`Couldn't decline the invitation to ${invite.org_name}. Please try again.`);
      }
    },
    [pendingInvites],
  );

  const orgLabel = activeMembership?.org_name ?? "Workspace";
  // Index by code points, not UTF-16 code units: "🏢 Acme"[0] is a lone high
  // surrogate that renders as a replacement char. Array.from splits correctly.
  const initial = Array.from(orgLabel)[0]?.toUpperCase() ?? "?";

  // Sync nowMs with real time whenever new pending-invites data arrives.
  // This is the "update React state from an external source" shape the
  // set-state-in-effect rule has a narrow carve-out for — Date.now() is
  // the external source, pendingInvites.invites identity is the signal
  // that the data we're rendering against just changed. Stable dep:
  // pendingInvites.invites is memoized in the hook, so this fires only
  // on fresh query results, not on every render.
  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setNowMs(Date.now());
  }, [pendingInvites.invites]);

  // The pending-invites count drives both the trigger dot and the in-dropdown
  // section. Treating the *visible* count (pending minus already-joined-in-
  // this-session) as the indicator stops the dot from lingering after the
  // user has acted on every invitation but the cache hasn't refetched yet.
  const visiblePendingInvites = useMemo(
    () => pendingInvites.invites.filter((inv) => !justJoined[inv.id]),
    [pendingInvites.invites, justJoined],
  );
  const joinedEntries = useMemo(() => Object.entries(justJoined), [justJoined]);
  const visiblePendingCount = visiblePendingInvites.length;
  const hasPendingInvites = visiblePendingCount > 0 || joinedEntries.length > 0;
  const ariaPendingSuffix =
    visiblePendingCount > 0
      ? `, ${visiblePendingCount} pending invitation${visiblePendingCount === 1 ? "" : "s"}`
      : "";

  return (
    <>
      <DropdownMenu
        onOpenChange={(open) => {
          if (open) {
            // Refetch on open so the "you have a pending invite" surface is
            // never staler than the moment the user looked at it. The wall-
            // clock snapshot used by the per-row "expires soon" check rides
            // on top of the refetched invites identity via nowMs's useMemo.
            pendingInvites.refetch();
          } else {
            setSearch("");
            // Drop the just-joined confirmation rows once the dropdown
            // closes; the next open shows the freshly-refetched real state.
            setJustJoined({});
          }
        }}
      >
        <DropdownMenuTrigger
          className="flex w-full min-w-0 items-center gap-1.5 rounded-md px-1.5 py-1 -ml-1.5 hover:bg-sidebar-accent transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          data-testid="org-switcher"
          aria-label={`Switch organization${ariaPendingSuffix}`}
        >
          <div className="relative flex h-5 w-5 items-center justify-center rounded bg-foreground text-background text-xs font-semibold shrink-0">
            {initial}
            {visiblePendingCount > 0 && (
              <span
                // bg-primary rather than bg-foreground: the avatar is already
                // bg-foreground, so reusing it would camouflage the dot. The
                // ring-sidebar halo punches the dot out of the avatar visually,
                // matching the standard notification-dot affordance.
                className="absolute -top-0.5 -right-0.5 block h-1.5 w-1.5 rounded-full bg-primary ring-2 ring-sidebar"
                data-testid="org-switcher-pending-invite-dot"
                aria-hidden="true"
              />
            )}
          </div>
          <span className="text-sm font-semibold text-sidebar-foreground truncate flex-1 text-left">
            {orgLabel}
          </span>
          <ChevronsUpDown className="h-3.5 w-3.5 shrink-0 opacity-40" />
        </DropdownMenuTrigger>
        <DropdownMenuContent align="start" side="bottom" className="w-64">
          {userEmail && (
            <>
              <DropdownMenuLabel className="text-xs font-normal text-muted-foreground truncate">
                {userEmail}
              </DropdownMenuLabel>
              <DropdownMenuSeparator />
            </>
          )}
          {hasPendingInvites && (
            <>
              <DropdownMenuLabel className="text-xs font-normal text-muted-foreground">
                Pending invitations
              </DropdownMenuLabel>
              <div className="px-1 pb-1" data-testid="pending-invitations-section">
                {/*
                  Render joined confirmations from justJoined state directly,
                  not by walking invites[]. The accept mutation invalidates
                  the pending list, and the server's ListPendingForUser
                  (status='pending' + NOT EXISTS membership) drops the just-
                  accepted row within a single refetch — so rendering the
                  "Joined X — Switch to it" affordance inside invites.map
                  would flash it for a few hundred ms and then lose the row
                  before the user could click "Switch to it".
                */}
                {joinedEntries.map(([inviteID, joined]) => (
                  <div
                    key={`joined-${inviteID}`}
                    className="flex items-center gap-2 rounded-sm px-2 py-1.5 text-sm"
                    data-testid={`pending-invitation-joined-${inviteID}`}
                  >
                    <Check className="h-3.5 w-3.5 text-foreground shrink-0" />
                    <span className="truncate flex-1">Joined {joined.org_name}</span>
                    <button
                      type="button"
                      className="text-xs text-muted-foreground hover:text-foreground transition-colors focus-visible:outline-none focus-visible:underline"
                      onClick={(e) => {
                        e.preventDefault();
                        void handleSwitchToJoined(joined);
                      }}
                      data-testid={`pending-invitation-switch-${inviteID}`}
                    >
                      Switch to it
                    </button>
                  </div>
                ))}
                {visiblePendingInvites.map((invite) => {
                  const expiresAtMs = new Date(invite.expires_at).getTime();
                  const expiresSoon =
                    Number.isFinite(expiresAtMs) &&
                    expiresAtMs - nowMs < EXPIRES_SOON_MS;
                  return (
                    <div
                      key={invite.id}
                      className="flex flex-col gap-1.5 rounded-sm px-2 py-1.5 text-sm"
                      data-testid={`pending-invitation-${invite.id}`}
                    >
                      <div className="flex items-center gap-2 min-w-0">
                        <Mail className="h-3.5 w-3.5 text-muted-foreground shrink-0" />
                        <span className="truncate flex-1 font-medium" title={invite.org_name}>
                          {invite.org_name}
                        </span>
                        <span className="text-xs uppercase tracking-wide text-muted-foreground shrink-0">
                          {roleLabel(invite.role)}
                        </span>
                      </div>
                      <div className="text-xs text-muted-foreground truncate">
                        Invited by {invite.invited_by.name}
                        {expiresSoon && (
                          <span className="ml-1 text-destructive">· expires soon</span>
                        )}
                      </div>
                      <div className="flex items-center justify-end gap-1.5 pt-0.5">
                        <Button
                          type="button"
                          size="sm"
                          variant="ghost"
                          className="h-6 px-2 text-xs"
                          disabled={pendingInvites.acceptingId === invite.id || pendingInvites.decliningId === invite.id}
                          onClick={(e) => {
                            e.preventDefault();
                            void handleDeclineInvite(invite);
                          }}
                          data-testid={`pending-invitation-decline-${invite.id}`}
                        >
                          Decline
                        </Button>
                        <Button
                          type="button"
                          size="sm"
                          className="h-6 px-2 text-xs"
                          disabled={pendingInvites.acceptingId === invite.id || pendingInvites.decliningId === invite.id}
                          onClick={(e) => {
                            e.preventDefault();
                            void handleAcceptInvite(invite);
                          }}
                          data-testid={`pending-invitation-accept-${invite.id}`}
                        >
                          Accept
                        </Button>
                      </div>
                    </div>
                  );
                })}
              </div>
              <DropdownMenuSeparator />
            </>
          )}
          {showSearch && (
            <div className="px-2 pb-1.5">
              <Input
                type="text"
                placeholder="Search workspaces..."
                value={search}
                onChange={(e) => setSearch(e.target.value)}
                onClick={(e) => e.stopPropagation()}
                onKeyDown={(e) => e.stopPropagation()}
              />
            </div>
          )}
          {filtered.length === 0 && (
            <div className="px-2 py-4 text-center text-xs text-muted-foreground">
              No matching workspaces
            </div>
          )}
          {filtered.map((m) => {
            const isActive = m.org_id === effectiveActiveOrgId;
            return (
              <DropdownMenuItem
                key={m.org_id}
                onClick={() => handleSwitch(m)}
                className={cn("flex items-center gap-2", isActive && "font-medium")}
                data-testid={`org-switcher-item-${m.org_id}`}
              >
                <div className="flex h-5 w-5 items-center justify-center rounded bg-muted text-xs font-semibold shrink-0">
                  {Array.from(m.org_name)[0]?.toUpperCase() ?? "?"}
                </div>
                <span className="truncate flex-1" title={m.org_name}>
                  {m.org_name}
                </span>
                <span className="text-xs uppercase tracking-wide text-muted-foreground">
                  {roleLabel(m.role)}
                </span>
                {isActive && <Check className="h-3.5 w-3.5 text-foreground shrink-0" />}
              </DropdownMenuItem>
            );
          })}
          <DropdownMenuSeparator />
          <DropdownMenuItem
            onSelect={(e) => {
              e.preventDefault();
              setCreateOpen(true);
            }}
          >
            <Plus className="h-4 w-4" />
            Create organization
          </DropdownMenuItem>
          <DropdownMenuItem onClick={() => router.push("/settings")}>
            <Settings className="h-4 w-4" />
            Organization settings
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
      <CreateOrgDialog open={createOpen} onOpenChange={setCreateOpen} />
    </>
  );
}
