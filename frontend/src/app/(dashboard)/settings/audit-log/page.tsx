"use client";

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useQueryState, parseAsString } from "nuqs";
import { api } from "@/lib/api";
import { useAuth } from "@/hooks/use-auth";
import { PageHeader } from "@/components/page-header";
import { PageContainer } from "@/components/page-container";
import { AuditLogEntry } from "@/components/audit/audit-log-entry";
import { AuditLogDetailDrawer } from "@/components/audit/audit-log-detail-drawer";
import { EmptyState } from "@/components/empty-state";
import { ScrollText, History } from "lucide-react";
import { Button } from "@/components/ui/button";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import type { AuditLog, User, ListResponse } from "@/lib/types";
import { useAuditLogFeed } from "@/components/audit/use-audit-log-feed";

const resourceTypeOptions = [
  { value: "", label: "All resources" },
  { value: "session", label: "Sessions" },
  { value: "project", label: "Projects" },
  { value: "project_task", label: "Tasks" },
  { value: "automation", label: "Automations" },
  { value: "settings", label: "Settings" },
  { value: "team_member", label: "Team members" },
  { value: "invitation", label: "Invitations" },
  { value: "integration", label: "Integrations" },
  { value: "credential", label: "Credentials" },
];

const actionPrefixOptions = [
  { value: "", label: "All actions" },
  { value: "session.", label: "Session actions" },
  { value: "project.", label: "Project actions" },
  { value: "automation.", label: "Automation actions" },
  { value: "team.", label: "Team actions" },
  { value: "settings.", label: "Settings actions" },
  { value: "auth.", label: "Auth actions" },
  { value: "integration.", label: "Integration actions" },
  { value: "credential.", label: "Credential actions" },
];

export default function AuditLogPage() {
  const { user } = useAuth();
  const [resourceType, setResourceType] = useQueryState("resource_type", parseAsString);
  const [actionPrefix, setActionPrefix] = useQueryState("action_prefix", parseAsString);
  const [userId, setUserId] = useQueryState("user_id", parseAsString);
  const [selectedEntry, setSelectedEntry] = useState<AuditLog | null>(null);

  const isAdmin = user?.role === "admin";

  const filters: Record<string, string> = {};
  if (resourceType) filters.resource_type = resourceType;
  if (actionPrefix) filters.action_prefix = actionPrefix;
  if (userId) filters.user_id = userId;

  const hasActiveFilters = !!(resourceType || actionPrefix || userId);

  const { data: membersData } = useQuery<ListResponse<User>>({
    queryKey: ["team", "members"],
    queryFn: () => api.team.listMembers(),
    enabled: isAdmin,
  });
  const members = membersData?.data ?? [];

  const {
    entries,
    isLoading,
    error,
    fetchNextPage,
    hasNextPage,
    isFetchingNextPage,
  } = useAuditLogFeed({
    filters,
    pageSize: 25,
    enabled: isAdmin,
  });

  if (error) {
    console.error("Failed to load audit logs:", error);
  }

  // Only admins can view audit logs
  if (!isAdmin) {
    return (
      <PageContainer size="default">
        <div className="space-y-6">
          <PageHeader
            title="Audit log"
            description="View all activity across your organization."
          />
          <div className="rounded-md bg-muted px-3 py-2 text-xs text-muted-foreground">
            Only admins can view audit logs.
          </div>
        </div>
      </PageContainer>
    );
  }

  function resetFilters() {
    setResourceType(null);
    setActionPrefix(null);
    setUserId(null);
  }

  // Reset cursors when filters change
  function handleResourceTypeChange(value: string) {
    setResourceType(value === "_all" ? null : value);
  }
  function handleActionPrefixChange(value: string) {
    setActionPrefix(value === "_all" ? null : value);
  }
  function handleUserIdChange(value: string) {
    setUserId(value === "_all" ? null : value);
  }

  return (
    <PageContainer size="default">
      <div className="space-y-6">
        <PageHeader
          title="Audit log"
          description="View all activity across your organization."
        />

        {/* Filters */}
        <div className="flex flex-wrap items-center gap-3">
          <Select value={resourceType ?? "_all"} onValueChange={handleResourceTypeChange}>
            <SelectTrigger className="h-8 w-[160px] text-xs">
              <SelectValue placeholder="All resources" />
            </SelectTrigger>
            <SelectContent>
              {resourceTypeOptions.map((opt) => (
                <SelectItem key={opt.value || "_all"} value={opt.value || "_all"}>
                  {opt.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>

          <Select value={actionPrefix ?? "_all"} onValueChange={handleActionPrefixChange}>
            <SelectTrigger className="h-8 w-[160px] text-xs">
              <SelectValue placeholder="All actions" />
            </SelectTrigger>
            <SelectContent>
              {actionPrefixOptions.map((opt) => (
                <SelectItem key={opt.value || "_all"} value={opt.value || "_all"}>
                  {opt.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>

          <Select value={userId ?? "_all"} onValueChange={handleUserIdChange}>
            <SelectTrigger className="h-8 w-[160px] text-xs">
              <SelectValue placeholder="All actors" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="_all">All actors</SelectItem>
              {members.map((member) => (
                <SelectItem key={member.id} value={member.id}>
                  {member.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>

          {hasActiveFilters && (
            <Button variant="ghost" size="sm" className="text-xs h-8" onClick={resetFilters}>
              Clear filters
            </Button>
          )}
        </div>

        {/* Entries */}
        <div className="rounded-lg border border-border bg-surface-raised shadow-sm">
          <div
            className="flex flex-col gap-3 border-b border-border/50 px-6 py-4 sm:flex-row sm:items-center sm:justify-between"
          >
            <div className="space-y-1">
              <div className="flex flex-wrap items-center gap-2">
                <span className="flex items-center gap-1.5 text-xs text-muted-foreground">
                  <History className="h-3.5 w-3.5" />
                  {entries.length} event{entries.length === 1 ? "" : "s"} loaded
                </span>
              </div>
              <p className="text-xs text-muted-foreground">
                {hasActiveFilters
                  ? "Filtered activity stays anchored while you load older events."
                  : "Browse recent activity first, then extend the timeline without losing your place."}
              </p>
            </div>
          </div>
          {error ? (
            <div className="rounded-md bg-destructive/10 px-3 py-2 text-xs text-destructive m-3">
              Failed to load audit logs.
            </div>
          ) : isLoading && entries.length === 0 ? (
            <div className="py-8 text-center text-sm text-muted-foreground">
              Loading audit logs...
            </div>
          ) : entries.length === 0 ? (
            <EmptyState
              icon={ScrollText}
              title="No audit log entries found"
              description={hasActiveFilters ? "Try adjusting your filters." : "Activity will appear here as actions are performed."}
            />
          ) : (
            <>
              <div className="divide-y-0">
                {entries.map((entry) => (
                  <AuditLogEntry
                    key={entry.id}
                    entry={entry}
                    members={members}
                    onSelect={setSelectedEntry}
                  />
                ))}
              </div>
              {hasNextPage && (
                <div className="border-t border-border/50 px-6 py-3">
                  <Button
                    variant="outline"
                    size="sm"
                    className="h-8 rounded-full px-3 text-xs"
                    onClick={() => fetchNextPage()}
                    disabled={isFetchingNextPage}
                  >
                    {isFetchingNextPage ? "Loading..." : "Load more"}
                  </Button>
                </div>
              )}
            </>
          )}
        </div>
      </div>
      <AuditLogDetailDrawer
        entry={selectedEntry}
        onClose={() => setSelectedEntry(null)}
        members={members}
      />
    </PageContainer>
  );
}
