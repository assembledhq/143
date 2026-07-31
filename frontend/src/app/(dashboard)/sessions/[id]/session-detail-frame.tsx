import type { ReactNode } from "react";
import { cn } from "@/lib/utils";

// What the frame knows about the session it is standing in for: whether a
// seeded row gave us the title and status, or we have nothing yet.
export type SessionDetailTransition = "initial" | "provisional";

// Why the frame is standing in. Absent once the workspace is loaded, so both
// data attributes only appear while the real page is not on screen.
export type SessionDetailTransitionState = "loading" | "error";

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
  transition,
  state,
  retrying = false,
  className,
  testId = "session-detail-frame",
}: {
  children: ReactNode;
  transition?: SessionDetailTransition;
  state?: SessionDetailTransitionState;
  // Only meaningful for `state: "error"`: the query keeps its error until a
  // refetch settles, so a retry is a failed frame that is busy again.
  retrying?: boolean;
  className?: string;
  testId?: string;
}) {
  // Derived rather than passed in. `aria-busy` and the state it describes used
  // to be independent props, which let call sites disagree — a settled failure
  // announcing itself as busy suppresses the alert it contains, and a loading
  // frame that forgets the flag announces nothing at all.
  const busy = state === "loading" || (state === "error" && retrying);

  return (
    <div
      data-testid={testId}
      data-session-transition={transition}
      data-session-state={state}
      aria-busy={busy || undefined}
      className={cn("flex h-full min-h-0 bg-background", className)}
    >
      {children}
    </div>
  );
}
