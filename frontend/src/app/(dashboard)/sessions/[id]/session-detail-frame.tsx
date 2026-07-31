import type { ReactNode } from "react";
import { cn } from "@/lib/utils";

// Which transition this frame is standing in for. Absent once the workspace is
// loaded, so `data-session-transition` only appears while something is
// standing in for the real page.
export type SessionDetailTransition = "initial" | "provisional" | "error";

// The single root element for every session detail state. Keeping the same
// element type at the root of all four of SessionDetailContent's returns lets
// React reconcile it in place, so switching sessions swaps the contents
// instead of tearing down and rebuilding the workspace. The base classes
// therefore have to serve the loaded page too, not just the skeleton:
// `min-h-0` lets the frame shrink inside its flex parent and `bg-background`
// keeps the surface opaque while the contents swap.
//
// The guarantee is scoped to transitions inside SessionDetailContent. The
// first open of any session still crosses the dynamic() boundary in
// session-detail-page-client, where the `loading` fallback and the loaded
// component are different element types, so React remounts the frame there.
export function SessionDetailFrame({
  children,
  busy = false,
  transition,
  className,
  testId = "session-detail-frame",
}: {
  children: ReactNode;
  // Whether a request is in flight right now. Independent of `transition`: a
  // failed transition is idle until the user retries, and busy again while
  // that retry runs.
  busy?: boolean;
  transition?: SessionDetailTransition;
  className?: string;
  testId?: string;
}) {
  return (
    <div
      data-testid={testId}
      data-session-transition={transition}
      aria-busy={busy || undefined}
      className={cn("flex h-full min-h-0 bg-background", className)}
    >
      {children}
    </div>
  );
}
