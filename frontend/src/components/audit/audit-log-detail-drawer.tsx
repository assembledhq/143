"use client";

import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
  SheetDescription,
} from "@/components/ui/sheet";
import { Badge } from "@/components/ui/badge";
import { formatAuditDetailValue } from "@/lib/audit-details";
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
          <SheetTitle className="text-xs">Event details</SheetTitle>
          <SheetDescription className="text-xs">
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
              <div className="rounded-md bg-surface-pane border border-border/50 p-3 space-y-2">
                {Object.entries(entry.details).map(([key, value]) => {
                  if (key === "changes" && isChangesMap(value)) {
                    return <ChangesBlock key={key} changes={value} />;
                  }
                  return (
                    <div key={key} className="flex gap-2 text-xs">
                      <span className="font-medium text-muted-foreground min-w-[100px] shrink-0">{key}</span>
                      <span className="text-foreground break-all font-mono text-xs">
                        {formatAuditDetailValue(key, value)}
                      </span>
                    </div>
                  );
                })}
              </div>
            </div>
          )}

          {/* Request metadata */}
          {(entry.ip_address || entry.user_agent || entry.request_id) && (
            <div className="space-y-2">
              <h3 className="text-xs font-semibold text-muted-foreground uppercase tracking-wider">Request info</h3>
              <div className="rounded-md bg-surface-pane border border-border/50 p-3 space-y-2">
                {entry.ip_address && <DetailRow label="IP Address" value={entry.ip_address} mono />}
                {entry.user_agent && <DetailRow label="User Agent" value={entry.user_agent} />}
                {entry.request_id && <DetailRow label="Request ID" value={entry.request_id} mono />}
              </div>
            </div>
          )}

          {/* Correlation IDs */}
          {(entry.session_id || entry.project_id) && (
            <div className="space-y-2">
              <h3 className="text-xs font-semibold text-muted-foreground uppercase tracking-wider">Related</h3>
              <div className="rounded-md bg-surface-pane border border-border/50 p-3 space-y-2">
                {entry.session_id && <DetailRow label="Session ID" value={entry.session_id} mono />}
                {entry.project_id && <DetailRow label="Project ID" value={entry.project_id} mono />}
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
  children,
}: {
  label: string;
  value?: string;
  mono?: boolean;
  children?: React.ReactNode;
}) {
  return (
    <div className="flex items-start gap-3 text-xs">
      <span className="text-muted-foreground min-w-[100px] shrink-0 font-medium">{label}</span>
      {children ?? (
        <span className={`text-foreground break-all ${mono ? "font-mono" : ""}`}>
          {value}
        </span>
      )}
    </div>
  );
}

type ChangesMap = Record<string, { before: unknown; after: unknown }>;

/**
 * isChangesMap narrows an unknown details value to the {field: {before,after}}
 * shape emitted by backend update handlers. We reject arrays up front because
 * `typeof []` is "object" in JS and we don't want "changes: [1,2,3]" to fall
 * into the diff renderer.
 */
function isChangesMap(value: unknown): value is ChangesMap {
  if (typeof value !== "object" || value === null || Array.isArray(value)) return false;
  return Object.values(value).every(
    (v) =>
      typeof v === "object" &&
      v !== null &&
      !Array.isArray(v) &&
      "before" in (v as object) &&
      "after" in (v as object),
  );
}

function formatDiffValue(field: string, v: unknown): string {
  if (v === null || v === undefined || v === "") return "—";
  return formatAuditDetailValue(field, v);
}

function ChangesBlock({ changes }: { changes: ChangesMap }) {
  return (
    <div className="text-xs space-y-1.5">
      <span className="font-medium text-muted-foreground">changes</span>
      <div className="ml-2 space-y-1.5">
        {Object.entries(changes).map(([field, { before, after }]) => (
          <div key={field} className="flex flex-wrap items-baseline gap-2">
            <span className="font-mono text-muted-foreground">{field}</span>
            <span className="font-mono text-foreground/70 line-through">{formatDiffValue(field, before)}</span>
            <span className="text-muted-foreground/50">→</span>
            <span className="font-mono text-foreground">{formatDiffValue(field, after)}</span>
          </div>
        ))}
      </div>
    </div>
  );
}
