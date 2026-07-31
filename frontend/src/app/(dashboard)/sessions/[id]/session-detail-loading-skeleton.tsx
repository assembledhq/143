import { cn } from "@/lib/utils";
import { AgentBadge } from "@/components/agent-badge";
import { Button } from "@/components/ui/button";
import { SessionDetailFrame } from "./session-detail-frame";
import {
  SESSION_COMPOSER_SURFACE_HEIGHT_CLASSNAME,
  SESSION_DETAIL_PANEL_DEFAULT_WIDTH,
  SESSION_HEADER_HEIGHT_CLASSNAME,
  SESSION_THREAD_STRIP_HEIGHT_CLASSNAME,
  SESSION_WORKSPACE_MIN_WIDTH_CLASSNAME,
} from "./session-detail-geometry";

// `announce` controls the live region. Inside the loading frame the ancestor
// already carries aria-busy, which suppresses descendant live regions anyway —
// so the announcement there is dead markup that only looks like coverage. In
// the loaded transcript, where nothing else marks the region busy, this is the
// only thing that tells a screen reader the timeline is still filling in.
export function SessionTimelineSkeleton({ announce = true }: { announce?: boolean } = {}) {
  const rows: { align: "left" | "right"; widths: string[] }[] = [
    { align: "right", widths: ["w-3/5", "w-2/5"] },
    { align: "left", widths: ["w-4/5", "w-3/4", "w-1/2"] },
    { align: "left", widths: ["w-2/3", "w-1/3"] },
    { align: "left", widths: ["w-3/4", "w-3/5"] },
  ];

  return (
    <div
      role={announce ? "status" : undefined}
      aria-live={announce ? "polite" : undefined}
      aria-label={announce ? "Loading session activity" : undefined}
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
      {announce ? <span className="sr-only">Loading session activity...</span> : null}
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

type SessionDetailLoadingProps = {
  metadata?: SessionDetailSkeletonMetadata | null;
  detailPanelOpen?: boolean;
  detailPanelWidth?: number;
  errorMessage?: string;
  onRetry?: () => void;
  // A retry keeps the error on screen — the query holds its error until the
  // refetch settles — so the button is the only place we can show that the
  // click did something.
  retrying?: boolean;
};

function SessionDetailTransitionError({
  message,
  onRetry,
  retrying = false,
}: {
  message: string;
  onRetry?: () => void;
  retrying?: boolean;
}) {
  return (
    <div
      role="alert"
      data-testid="session-detail-transition-error"
      className="mx-auto max-w-sm space-y-3 rounded-lg border border-border bg-card p-5 text-center"
    >
      <p className="text-sm font-medium text-foreground">Couldn&apos;t load this session</p>
      <p className="text-xs text-muted-foreground">{message}</p>
      {onRetry ? (
        <Button type="button" size="sm" variant="outline" loading={retrying} onClick={onRetry}>
          Retry
        </Button>
      ) : null}
    </div>
  );
}

export function SessionDetailLoadingContent({
  metadata,
  detailPanelOpen = true,
  detailPanelWidth = SESSION_DETAIL_PANEL_DEFAULT_WIDTH,
  errorMessage,
  onRetry,
  retrying,
}: SessionDetailLoadingProps) {
  // A cold failure has no metadata to preserve and nothing pending, so the
  // shimmer chrome would be a lie: it reads as "still loading" and reserves
  // boxes for content that is never going to arrive. Show only the error.
  //
  // This does mean the reserved detail panel collapses on the way from the
  // initial skeleton to a cold failure. That shift is deliberate: there is no
  // session to show beside the error, and holding a shimmering panel open next
  // to a dead end is worse than the one-time reflow.
  if (errorMessage && !metadata) {
    return (
      <div
        data-testid="session-conversation-workspace-loading"
        className={cn("flex min-w-0 flex-1 flex-col", SESSION_WORKSPACE_MIN_WIDTH_CLASSNAME)}
      >
        <div className="flex min-h-0 flex-1 items-center justify-center p-4">
          <SessionDetailTransitionError
            message={errorMessage}
            onRetry={onRetry}
            retrying={retrying}
          />
        </div>
      </div>
    );
  }

  return (
    <>
      <div
        data-testid="session-conversation-workspace-loading"
        className={cn("flex min-w-0 flex-1 flex-col", SESSION_WORKSPACE_MIN_WIDTH_CLASSNAME)}
      >
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
        <div
          className={cn(
            "hidden shrink-0 border-b border-border px-4 md:flex md:items-center md:justify-between",
            SESSION_HEADER_HEIGHT_CLASSNAME,
          )}
        >
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
          {/* AgentTabStrip holds this same height even when it has no tabs to
              show, so reserving it here matches the loaded view for every
              session — including the zero-thread case a provisional list row
              cannot tell us about. */}
          <div
            data-testid="session-thread-strip-loading"
            className={cn(
              "hidden shrink-0 border-b border-border px-3 md:flex md:items-center",
              SESSION_THREAD_STRIP_HEIGHT_CLASSNAME,
            )}
          >
            <div className="flex gap-2 animate-pulse">
              <SkeletonLine className="h-6 w-24 rounded-md" />
              <SkeletonLine className="h-6 w-28 rounded-md" />
            </div>
          </div>
          <div className="min-h-0 flex-1 overflow-hidden p-4">
            <div className="mx-auto flex h-full max-w-3xl flex-col justify-end gap-3">
              {errorMessage ? (
                <SessionDetailTransitionError
                  message={errorMessage}
                  onRetry={onRetry}
                  retrying={retrying}
                />
              ) : (
                <SessionTimelineSkeleton announce={false} />
              )}
            </div>
          </div>
          {/* No aria-disabled here: on a plain container it is inert, and the
              composer's real "not yet usable" signal is that no textbox
              exists in the tree until the session loads. */}
          <div
            data-testid="session-composer-loading"
            className="shrink-0 border-t border-border p-3"
          >
            <div
              data-testid="session-composer-loading-surface"
              className={cn(
                "flex flex-col rounded-xl border border-border-strong bg-surface-raised animate-pulse",
                SESSION_COMPOSER_SURFACE_HEIGHT_CLASSNAME,
              )}
            >
              <div className="flex min-h-11 flex-1 items-center px-2.5 py-1.5">
                <SkeletonLine className="h-3 w-2/5 rounded-md" />
              </div>
              <div className="flex h-10 items-center justify-between px-2 pb-2">
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
      {detailPanelOpen ? (
        <div
          data-testid="session-detail-panel-loading"
          style={{ width: detailPanelWidth }}
          className="hidden shrink-0 border-l border-border bg-background md:flex md:flex-col"
        >
          <div className={cn("shrink-0 border-b border-border px-3", SESSION_HEADER_HEIGHT_CLASSNAME)}>
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
      ) : null}
    </>
  );
}

export function SessionDetailLoadingSkeleton(props: SessionDetailLoadingProps) {
  return (
    <SessionDetailFrame
      testId="session-detail-loading-skeleton"
      state={props.errorMessage ? "error" : "loading"}
      transition={props.metadata ? "provisional" : "initial"}
      retrying={props.retrying}
    >
      <SessionDetailLoadingContent {...props} />
    </SessionDetailFrame>
  );
}
