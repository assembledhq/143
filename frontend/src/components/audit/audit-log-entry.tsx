"use client";

import { useState } from "react";
import { ChevronDown, ChevronRight } from "lucide-react";
import type { AuditLog, User } from "@/lib/types";
import { formatTimeAgo } from "@/lib/utils";
import { formatAuditDetailValue } from "@/lib/audit-details";
import { Button } from "@/components/ui/button";

/** Human-readable labels for audit actions. */
const actionLabels: Record<string, string> = {
  "session.created": "created session",
  "session.started": "started session",
  "session.completed": "session completed",
  "session.failed": "session failed",
  "session.cancelled": "cancelled session",
  "session.status_changed": "session status changed",
  "session.question.created": "question created",
  "session.question.answered": "answered question",
  "session.resumed_locally": "resumed session locally",
  "project.created": "created project",
  "project.updated": "updated project",
  "project.deleted": "deleted project",
  "project.started": "started project",
  "project.completed": "project completed",
  "project.run_triggered": "triggered project run",
  "project.cycle_completed": "project cycle completed",
  "project.task.created": "created task",
  "project.task.updated": "updated task",
  "project.task.deleted": "deleted task",
  "project.task.retried": "retried task",
  "automation.created": "created automation",
  "automation.updated": "updated automation",
  "automation.deleted": "deleted automation",
  "automation.paused": "paused automation",
  "automation.resumed": "resumed automation",
  "automation.run_triggered": "triggered automation run",
  "issue.created": "created issue",
  "issue.reprioritized": "reprioritized issue",
  "pm.analysis_triggered": "triggered PM analysis",
  "pm.plan_created": "PM plan created",
  "pm.decision_made": "PM decision made",
  "settings.updated": "updated settings",
  "team.member_invited": "invited team member",
  "team.member_role_changed": "changed user role",
  "team.member_removed": "removed team member",
  "team.invitation_revoked": "revoked invitation",
  "team.invitation_accepted": "accepted invitation",
  "team.domain_added": "added verified domain",
  "team.domain_verified": "verified domain",
  "team.domain_updated": "updated verified domain",
  "team.domain_removed": "removed verified domain",
  "team.member_auto_joined": "auto-joined team member",
  "team.github_org_auto_join_enabled": "enabled GitHub organization auto-join",
  "team.github_org_auto_join_disabled": "disabled GitHub organization auto-join",
  "integration.connected": "connected integration",
  "credential.updated": "updated credential",
  "credential.deleted": "deleted credential",
  "auth.login": "logged in",
  "auth.logout": "logged out",
  "auth.register": "registered",
};

const actorTypeLabels: Record<string, string> = {
  user: "",
  agent: "Agent",
  system: "System",
  webhook: "Webhook",
};

function getActorName(entry: AuditLog, members: User[]): string {
  if (entry.actor_type === "user" && entry.user_id) {
    const member = members.find((m) => m.id === entry.user_id);
    if (member) return member.name;
  }
  if (entry.actor_type !== "user") {
    return actorTypeLabels[entry.actor_type] || entry.actor_type;
  }
  return entry.actor_id;
}

function getActionLabel(action: string): string {
  return actionLabels[action] || action.replace(/\./g, " ");
}

interface AuditLogEntryProps {
  entry: AuditLog;
  members: User[];
  /** Optional click handler (e.g., to open a detail drawer). When provided, clicking the row calls this instead of expanding inline. */
  onSelect?: (entry: AuditLog) => void;
}

export function AuditLogEntry({ entry, members, onSelect }: AuditLogEntryProps) {
  const [expanded, setExpanded] = useState(false);
  const actorName = getActorName(entry, members);
  const actionLabel = getActionLabel(entry.action);
  const hasDetails = entry.details && Object.keys(entry.details).length > 0;

  return (
    <div className="border-b border-border/50 last:border-b-0">
      <Button
        variant="ghost"
        onClick={() => {
          if (onSelect) {
            onSelect(entry);
          } else if (hasDetails) {
            setExpanded(!expanded);
          }
        }}
        className="flex w-full items-start gap-2 whitespace-normal px-6 py-3.5 text-left text-sm hover:bg-muted/30 transition-all duration-150 h-auto rounded-none"
        disabled={!onSelect && !hasDetails}
      >
        <span className="shrink-0 w-14 text-xs text-muted-foreground/70 pt-0.5 tabular-nums">
          {formatTimeAgo(entry.created_at)}
        </span>
        <span className="flex-1 min-w-0 whitespace-normal break-words">
          <span className="font-medium text-foreground">{actorName}</span>
          {" "}
          <span className="text-muted-foreground">{actionLabel}</span>
        </span>
        {!onSelect && hasDetails && (
          <span className="shrink-0 pt-0.5 text-muted-foreground/50">
            {expanded ? (
              <ChevronDown className="h-3.5 w-3.5" />
            ) : (
              <ChevronRight className="h-3.5 w-3.5" />
            )}
          </span>
        )}
      </Button>
      {expanded && hasDetails && (
        <div className="px-6 pb-3 pl-[4.5rem]">
          <div className="rounded-md bg-muted/30 border border-border/50 p-3 text-xs">
            <div className="space-y-1.5">
              {Object.entries(entry.details!).map(([key, value]) => (
                <div key={key} className="flex gap-2">
                  <span className="font-medium text-muted-foreground min-w-[80px]">{key}:</span>
                  <span className="min-w-0 text-foreground break-words">
                    {formatAuditDetailValue(key, value)}
                  </span>
                </div>
              ))}
              {entry.ip_address && (
                <div className="flex gap-2">
                  <span className="font-medium text-muted-foreground min-w-[80px]">IP:</span>
                  <span className="text-foreground">{entry.ip_address}</span>
                </div>
              )}
              <div className="flex gap-2">
                <span className="font-medium text-muted-foreground min-w-[80px]">Time:</span>
                <span className="text-foreground">{new Date(entry.created_at).toLocaleString()}</span>
              </div>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}

export { getActorName, getActionLabel };
