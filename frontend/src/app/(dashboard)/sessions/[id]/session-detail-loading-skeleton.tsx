import { cn } from "@/lib/utils";

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

export function SessionDetailLoadingSkeleton() {
  return (
    <div
      data-testid="session-detail-loading-skeleton"
      aria-busy="true"
      className="flex h-full min-h-0 bg-background"
    >
      <div className="flex min-w-0 flex-1 flex-col">
        <div className="hidden h-12 shrink-0 border-b border-border px-4 md:flex md:items-center md:justify-between">
          <div className="min-w-0 flex-1 animate-pulse space-y-2">
            <SkeletonLine className="h-4 w-2/5 max-w-[360px]" />
            <SkeletonLine className="h-3 w-1/4 max-w-[220px]" />
          </div>
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
