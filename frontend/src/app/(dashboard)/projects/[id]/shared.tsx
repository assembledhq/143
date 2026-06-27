"use client";

import { useState } from "react";
import {
  FileText,
  ChevronDown,
  ChevronRight,
  AlertCircle,
  CheckCircle2,
  Circle,
  Loader2,
  Ban,
  Pause,
  ArrowUpRight,
} from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { formatDateTime } from "@/lib/utils";

export const taskStatusConfig: Record<
  string,
  { color: string; label: string; icon: typeof Circle }
> = {
  pending: { color: "bg-muted text-muted-foreground", label: "Pending", icon: Circle },
  blocked: { color: "bg-warning/10 text-warning", label: "Blocked", icon: Pause },
  delegated: { color: "bg-indigo-500/10 text-indigo-700 dark:text-indigo-400", label: "Delegated", icon: ArrowUpRight },
  running: { color: "bg-info/10 text-info", label: "Running", icon: Loader2 },
  completed: { color: "bg-success/10 text-success", label: "Completed", icon: CheckCircle2 },
  failed: { color: "bg-destructive/10 text-destructive", label: "Failed", icon: AlertCircle },
  skipped: { color: "bg-muted text-muted-foreground", label: "Skipped", icon: Ban },
  cancelled: { color: "bg-muted text-muted-foreground", label: "Cancelled", icon: Ban },
};

export const specTypeConfig: Record<string, { label: string; color: string }> = {
  prd: { label: "PRD", color: "bg-blue-500/10 text-blue-700 dark:text-blue-400" },
  technical: { label: "Technical", color: "bg-purple-500/10 text-purple-700 dark:text-purple-400" },
  design: { label: "Design", color: "bg-pink-500/10 text-pink-700 dark:text-pink-400" },
  user_story: { label: "User story", color: "bg-emerald-500/10 text-emerald-700 dark:text-emerald-400" },
};

export const attachmentCategoryConfig: Record<string, { label: string; color: string }> = {
  screenshot: { label: "Screenshot", color: "bg-blue-500/10 text-blue-700 dark:text-blue-400" },
  mockup: { label: "Mockup", color: "bg-purple-500/10 text-purple-700 dark:text-purple-400" },
  wireframe: { label: "Wireframe", color: "bg-orange-500/10 text-orange-700 dark:text-orange-400" },
  reference: { label: "Reference", color: "bg-muted text-muted-foreground" },
};

export function formatTimestamp(dateStr?: string): string {
  return formatDateTime(dateStr, { fallback: "-" });
}

export function ProgressBar({ completed, total }: { completed: number; total: number }) {
  const pct = total > 0 ? Math.round((completed / total) * 100) : 0;
  return (
    <div className="flex items-center gap-3">
      <div className="h-2 flex-1 rounded-full bg-muted overflow-hidden">
        <div
          className="h-full rounded-full bg-[image:var(--gradient-primary)] transition-all"
          style={{ width: `${pct}%` }}
        />
      </div>
      <span className="text-sm text-muted-foreground whitespace-nowrap">
        {completed}/{total} ({pct}%)
      </span>
    </div>
  );
}

export function CollapsibleSection({
  title,
  icon: Icon,
  count,
  defaultOpen = true,
  children,
  actions,
}: {
  title: string;
  icon: typeof FileText;
  count?: number;
  defaultOpen?: boolean;
  children: React.ReactNode;
  actions?: React.ReactNode;
}) {
  const [open, setOpen] = useState(defaultOpen);
  return (
    <div>
      <div
        role="button"
        tabIndex={0}
        onClick={() => setOpen(!open)}
        onKeyDown={(e) => {
          if (e.key === "Enter" || e.key === " ") {
            e.preventDefault();
            setOpen(!open);
          }
        }}
        className="flex items-center gap-2 w-full text-left py-2 group cursor-pointer"
      >
        {open ? (
          <ChevronDown className="h-3.5 w-3.5 text-muted-foreground" />
        ) : (
          <ChevronRight className="h-3.5 w-3.5 text-muted-foreground" />
        )}
        <Icon className="h-3.5 w-3.5 text-muted-foreground" />
        <span className="text-sm font-semibold">{title}</span>
        {count != null && count > 0 && (
          <Badge variant="secondary" className="text-xs px-1.5 py-0">
            {count}
          </Badge>
        )}
        <div className="flex-1" />
        {actions && (
          <span onClick={(e) => e.stopPropagation()}>{actions}</span>
        )}
      </div>
      {open && <div className="pl-6 pb-4">{children}</div>}
    </div>
  );
}
