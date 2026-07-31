import { useLayoutEffect, useRef } from "react";

export type SessionScopedResetGroup = {
  name: string;
  reset: () => void;
};

export function useSessionScopedReset(
  sessionId: string,
  groups: SessionScopedResetGroup[],
) {
  const groupsRef = useRef(groups);
  const previousSessionIdRef = useRef(sessionId);

  useLayoutEffect(() => {
    groupsRef.current = groups;
  }, [groups]);

  // Session changes are route transitions. Reset session-owned state before
  // the browser paints the new route so controls from the previous session
  // cannot flash under the newly selected session's title.
  useLayoutEffect(() => {
    if (previousSessionIdRef.current === sessionId) {
      return;
    }
    previousSessionIdRef.current = sessionId;
    for (const group of groupsRef.current) {
      group.reset();
    }
  }, [sessionId]);
}
