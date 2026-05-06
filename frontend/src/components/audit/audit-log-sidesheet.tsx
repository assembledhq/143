"use client";

import {
  Sheet,
  SheetContent,
  SheetHeader,
  SheetTitle,
  SheetDescription,
} from "@/components/ui/sheet";
import { AuditLogTimeline } from "./audit-log-timeline";
import type { User } from "@/lib/types";

interface AuditLogSidesheetProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** Pre-set filters for the scoped view. */
  filters: Record<string, string>;
  /** Title for the sidesheet header. */
  title?: string;
  /** Team members for resolving actor names. */
  members: User[];
}

export function AuditLogSidesheet({
  open,
  onOpenChange,
  filters,
  title = "Activity",
  members,
}: AuditLogSidesheetProps) {
  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent>
        <SheetHeader>
          <SheetTitle>{title}</SheetTitle>
          <SheetDescription>Recent changes and events</SheetDescription>
        </SheetHeader>
        <div className="mt-4 -mx-6">
          <AuditLogTimeline filters={filters} members={members} pageSize={15} />
        </div>
      </SheetContent>
    </Sheet>
  );
}
