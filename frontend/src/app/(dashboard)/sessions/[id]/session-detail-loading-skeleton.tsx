import { cn } from "@/lib/utils";
import { AgentBadge } from "@/components/agent-badge";

export function SessionTimelineSkeleton() {
  const rows: { align: "left" | "right"; widths: string[] }[] = [
    { align: "right", widths: ["w-3/5", "w-2/5"] },
    { align: "left", widths: ["w-4/5", "w-3/4", "w-1/2"] },
    { align: "left", widths: ["w-2/3", "w-1/3"] },
    { align: "left", widths: ["w-3/4", "w-3/5"] },
  ];

  return (
    <div
      role="status"
      aria-live="polite"
      aria-label="Loading session activity"
      data-testid="session-timeline-skeleton"
      className="space-y-3 py-1"
    >
      {rows.map((row, i) => (
        <div
          key={i}
          className={`flex ${row.align === "right" ? "justify-end" : "justify-start"}`}
        >
          <div
            className={`max-w-[92%] min-w-[40%] rounded-lg px-3 py-2.5 space-y-2 animate-pulse ${
              row.align === "right" ? "bg-primary/10" : "bg-muted"
            }`}
          >
            {row.widths.map((w, j) => (
              <div
                key={j}
                className={`h-3 rounded ${w} ${
                  row.align === "right" ? "bg-primary/20" : "bg-muted-foreground/15"
                }`}
              />
            ))}
          </div>
        </div>
      ))}
      <span className="sr-only">Loading session activity...</span>
    </div>
  );
}

function SkeletonLine({ className }: { className: string }) {
  return <div className={cn("rounded bg-muted-foreground/15", className)} />;
}

// Metadata that is already known while the rest of the page loads (from the
// sidebar-seeded provisional cache or a settled detail payload). Rendering it
// in the skeleton header means the user immediately sees what they opened,
// instead of an all-shimmer page hiding data the client already has.
// Precomputed strings (not the session object) so this component never needs
// imports from session-detail-content — that would be an import cycle.
export type SessionDetailSkeletonMetadata = {
  title: string;
  statusLabel: string;
  statusColor: string;
  agentType?: string | null;
};

export function SessionDetailLoadingSkeleton({
  metadata,
}: {
  metadata?: SessionDetailSkeletonMetadata | null;
}) {
  return (
    <div
      data-testid="session-detail-loading-skeleton"
      aria-busy="true"
      className="flex h-full min-h-0 bg-background"
    >
      <div className="flex min-w-0 flex-1 flex-col">
        {/* Mirrors MobileSessionTopBar's geometry (back button, title, two
            icon buttons) so the swap to the real page does not shift layout.
            The controls stay shimmer — they need session data to act — but
            the title is real as soon as we know it. */}
        <div
          data-testid="session-detail-skeleton-mobile-top-bar"
          className="flex shrink-0 items-center gap-1 border-b border-border bg-background/95 px-2 py-2 md:hidden"
        >
          <span className="animate-pulse">
            <SkeletonLine className="h-9 w-9 rounded-md" />
          </span>
          {metadata ? (
            <p className="min-w-0 flex-1 truncate text-sm font-medium text-foreground">
              {metadata.title}
            </p>
          ) : (
            <div className="min-w-0 flex-1 animate-pulse">
              <SkeletonLine className="h-4 w-3/5 max-w-[240px]" />
            </div>
          )}
          <span className="flex shrink-0 gap-1 animate-pulse">
            <SkeletonLine className="h-9 w-9 rounded-md" />
            <SkeletonLine className="h-9 w-9 rounded-md" />
          </span>
        </div>
        <div className="hidden h-12 shrink-0 border-b border-border px-4 md:flex md:items-center md:justify-between">
          {metadata ? (
            // Mirrors the loaded header's title row (same type classes and
            // status pill) so the swap to the real page does not shift layout.
            <div className="flex min-w-0 flex-1 items-center gap-2">
              <h1 className="text-sm font-medium text-foreground truncate">
                {metadata.title}
              </h1>
              <span
                className={`inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium shrink-0 ${metadata.statusColor}`}
              >
                {metadata.statusLabel}
              </span>
              {metadata.agentType && (
                <span className="hidden shrink-0 lg:inline-flex">
                  <AgentBadge agentType={metadata.agentType} className="h-4 w-4" labelClassName="text-xs" />
                </span>
              )}
            </div>
          ) : (
            <div className="min-w-0 flex-1 animate-pulse space-y-2">
              <SkeletonLine className="h-4 w-2/5 max-w-[360px]" />
              <SkeletonLine className="h-3 w-1/4 max-w-[220px]" />
            </div>
          )}
          <div className="flex shrink-0 gap-2 animate-pulse">
            <SkeletonLine className="h-8 w-8 rounded-md" />
            <SkeletonLine className="h-8 w-8 rounded-md" />
          </div>
        </div>
        <div className="flex min-h-0 flex-1 flex-col">
          <div className="hidden h-10 shrink-0 border-b border-border px-3 md:flex md:items-center">
            <div className="flex gap-2 animate-pulse">
              <SkeletonLine className="h-6 w-24 rounded-md" />
              <SkeletonLine className="h-6 w-28 rounded-md" />
            </div>
          </div>
          <div className="min-h-0 flex-1 overflow-hidden p-4">
            <div className="mx-auto flex h-full max-w-3xl flex-col justify-end gap-3">
              <SessionTimelineSkeleton />
            </div>
          </div>
          <div className="shrink-0 border-t border-border p-3">
            <div className="animate-pulse rounded-lg border border-border bg-card p-3">
              <SkeletonLine className="h-16 w-full rounded-md" />
              <div className="mt-3 flex items-center justify-between">
                <div className="flex gap-2">
                  <SkeletonLine className="h-8 w-8 rounded-md" />
                  <SkeletonLine className="h-8 w-24 rounded-md" />
                </div>
                <SkeletonLine className="h-8 w-8 rounded-md" />
              </div>
            </div>
          </div>
        </div>
      </div>
      <div className="hidden w-[360px] shrink-0 border-l border-border bg-background md:flex md:flex-col">
        <div className="h-12 shrink-0 border-b border-border px-3">
          <div className="flex h-full items-center gap-2 animate-pulse">
            <SkeletonLine className="h-7 w-20 rounded-md" />
            <SkeletonLine className="h-7 w-20 rounded-md" />
            <SkeletonLine className="h-7 w-20 rounded-md" />
          </div>
        </div>
        <div className="space-y-4 p-4 animate-pulse">
          <SkeletonLine className="h-24 w-full rounded-md" />
          <SkeletonLine className="h-16 w-full rounded-md" />
          <SkeletonLine className="h-32 w-full rounded-md" />
        </div>
      </div>
    </div>
  );
}
