"use client";

import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
  SheetDescription,
} from "@/components/ui/sheet";
import { Badge } from "@/components/ui/badge";
import type { AuditLog, User } from "@/lib/types";
import { getActorName, getActionLabel } from "./audit-log-entry";

interface AuditLogDetailDrawerProps {
  entry: AuditLog | null;
  onClose: () => void;
  members: User[];
}

export function AuditLogDetailDrawer({ entry, onClose, members }: AuditLogDetailDrawerProps) {
  if (!entry) return null;

  const actorName = getActorName(entry, members);
  const actionLabel = getActionLabel(entry.action);

  return (
    <Sheet open={!!entry} onOpenChange={(open) => !open && onClose()}>
      <SheetContent>
        <SheetHeader>
          <SheetTitle className="text-sm">Event details</SheetTitle>
          <SheetDescription>
            {actorName} {actionLabel}
          </SheetDescription>
        </SheetHeader>
        <div className="mt-6 space-y-5">
          {/* Core info */}
          <div className="space-y-3">
            <DetailRow label="Action" value={entry.action} />
            <DetailRow label="Actor" value={actorName} />
            <DetailRow label="Actor Type">
              <Badge variant="secondary" className="text-xs">{entry.actor_type}</Badge>
            </DetailRow>
            <DetailRow label="Resource Type">
              <Badge variant="outline" className="text-xs">{entry.resource_type}</Badge>
            </DetailRow>
            {entry.resource_id && <DetailRow label="Resource ID" value={entry.resource_id} mono />}
            <DetailRow label="Time" value={new Date(entry.created_at).toLocaleString()} />
          </div>

          {/* Details payload */}
          {entry.details && Object.keys(entry.details).length > 0 && (
            <div className="space-y-2">
              <h3 className="text-xs font-semibold text-muted-foreground uppercase tracking-wider">Details</h3>
              <div className="rounded-md bg-muted/30 border border-border/50 p-3 space-y-2">
                {Object.entries(entry.details).map(([key, value]) => (
                  <div key={key} className="flex gap-2 text-xs">
                    <span className="font-medium text-muted-foreground min-w-[100px] shrink-0">{key}</span>
                    <span className="text-foreground break-all font-mono text-xs">
                      {typeof value === "object" ? JSON.stringify(value, null, 2) : String(value)}
                    </span>
                  </div>
                ))}
              </div>
            </div>
          )}

          {/* Request metadata */}
          {(entry.ip_address || entry.user_agent || entry.request_id) && (
            <div className="space-y-2">
              <h3 className="text-xs font-semibold text-muted-foreground uppercase tracking-wider">Request info</h3>
              <div className="rounded-md bg-muted/30 border border-border/50 p-3 space-y-2">
                {entry.ip_address && <DetailRow label="IP Address" value={entry.ip_address} mono small />}
                {entry.user_agent && <DetailRow label="User Agent" value={entry.user_agent} small />}
                {entry.request_id && <DetailRow label="Request ID" value={entry.request_id} mono small />}
              </div>
            </div>
          )}

          {/* Correlation IDs */}
          {(entry.session_id || entry.project_id) && (
            <div className="space-y-2">
              <h3 className="text-xs font-semibold text-muted-foreground uppercase tracking-wider">Related</h3>
              <div className="rounded-md bg-muted/30 border border-border/50 p-3 space-y-2">
                {entry.session_id && <DetailRow label="Session ID" value={entry.session_id} mono small />}
                {entry.project_id && <DetailRow label="Project ID" value={entry.project_id} mono small />}
              </div>
            </div>
          )}
        </div>
      </SheetContent>
    </Sheet>
  );
}

function DetailRow({
  label,
  value,
  mono,
  small,
  children,
}: {
  label: string;
  value?: string;
  mono?: boolean;
  small?: boolean;
  children?: React.ReactNode;
}) {
  return (
    <div className={`flex items-start gap-3 ${small ? "text-xs" : "text-sm"}`}>
      <span className="text-muted-foreground min-w-[100px] shrink-0 text-xs font-medium">{label}</span>
      {children ?? (
        <span className={`text-foreground break-all ${mono ? "font-mono text-xs" : ""}`}>
          {value}
        </span>
      )}
    </div>
  );
}
