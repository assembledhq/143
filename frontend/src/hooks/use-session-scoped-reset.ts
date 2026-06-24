import { useEffect, useRef } from "react";

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

  useEffect(() => {
    groupsRef.current = groups;
  }, [groups]);

  useEffect(() => {
    if (previousSessionIdRef.current === sessionId) {
      return;
    }
    previousSessionIdRef.current = sessionId;
    for (const group of groupsRef.current) {
      group.reset();
    }
  }, [sessionId]);
}
