"use client";

import { useEffect, useMemo, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useRouter } from "next/navigation";
import { Check, ChevronsUpDown, Plus, Settings } from "lucide-react";
import { toast } from "sonner";

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
import { CreateOrgDialog } from "@/components/create-org-dialog";
import type { MembershipSummary, SingleResponse, MembershipsResponse } from "@/lib/types";

const SEARCH_THRESHOLD = 5;

export interface OrgSwitcherProps {
  userEmail?: string;
}

export function OrgSwitcher({ userEmail }: OrgSwitcherProps) {
  const router = useRouter();
  const queryClient = useQueryClient();

  const { data: membershipsResponse, refetch } = useQuery<SingleResponse<MembershipsResponse>>({
    queryKey: queryKeys.auth.memberships,
    queryFn: () => api.auth.memberships(),
  });

  const [tabOrgId, setTabOrgId] = useState<string | null>(() => getActiveOrgId());
  const [search, setSearch] = useState("");
  const [createOpen, setCreateOpen] = useState(false);

  // Keep tabOrgId in sync with sessionStorage changes from other code paths
  // in the same tab (e.g. Create Org finishing, another component calling
  // setActiveOrgId). Cross-tab sync is intentionally not wired up — per-tab
  // org is the whole point.
  useEffect(() => {
    const handler = () => setTabOrgId(getActiveOrgId());
    window.addEventListener(ACTIVE_ORG_CHANGED_EVENT, handler);
    return () => window.removeEventListener(ACTIVE_ORG_CHANGED_EVENT, handler);
  }, []);

  useEffect(() => {
    const handler = () => {
      void refetch();
      void queryClient.invalidateQueries();
      toast.info("Your access to an organization changed. Switched to your next available workspace.");
    };
    window.addEventListener(ORG_MEMBERSHIP_REVOKED_EVENT, handler);
    return () => window.removeEventListener(ORG_MEMBERSHIP_REVOKED_EVENT, handler);
  }, [refetch, queryClient]);

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

  const showSearch = memberships.length >= SEARCH_THRESHOLD;
  const filtered = useMemo(() => {
    if (!search) return memberships;
    const q = search.toLowerCase();
    return memberships.filter((m) => m.org_name.toLowerCase().includes(q));
  }, [memberships, search]);

  const handleSwitch = (membership: MembershipSummary) => {
    if (membership.org_id === effectiveActiveOrgId) return;
    setActiveOrgId(membership.org_id);
    void queryClient.invalidateQueries();
    router.push("/sessions");
  };

  const orgLabel = activeMembership?.org_name ?? "Workspace";
  const initial = orgLabel[0]?.toUpperCase() ?? "?";

  return (
    <>
      <DropdownMenu onOpenChange={(open) => { if (!open) setSearch(""); }}>
        <DropdownMenuTrigger
          className="flex w-full min-w-0 items-center gap-1.5 rounded-md px-1.5 py-1 -ml-1.5 hover:bg-sidebar-accent transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          data-testid="org-switcher"
          aria-label="Switch organization"
        >
          <div className="flex h-5 w-5 items-center justify-center rounded bg-foreground text-background text-xs font-semibold shrink-0">
            {initial}
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
                  {m.org_name[0]?.toUpperCase() ?? "?"}
                </div>
                <span className="truncate flex-1" title={m.org_name}>
                  {m.org_name}
                </span>
                <span className="text-xs uppercase tracking-wide text-muted-foreground">
                  {m.role}
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
