"use client";

import { useEffect, useRef, useState } from "react";

export interface UseDebouncedTextFieldOptions {
  serverValue: string;
  onCommit: (value: string) => void;
  /**
   * Debounce window between the last keystroke and the commit. Matches the
   * 400ms convention shared by every settings text/textarea field.
   */
  debounceMs?: number;
}

export interface UseDebouncedTextFieldResult {
  value: string;
  onChange: (next: string) => void;
  onBlur: () => void;
}

/**
 * Binds a free-form text `<input>` or `<textarea>` to an autosave scope.
 *
 * Tracks the string the user is typing in local state so partially typed
 * values don't get clobbered by optimistic refetches, debounces commits by
 * `debounceMs` (default 400ms), and flushes on blur. When the server value
 * changes for reasons other than the caller's own save (rollback, another
 * tab), the local display resyncs via the "store info from previous renders"
 * pattern so no effect-driven setState is needed.
 *
 * Distinct from `useAutosaveNumericField`: text fields don't parse or clamp,
 * and an empty string is a valid value (often meaning "delete this entry").
 *
 * IMPORTANT: the `onCommit` callback should dispatch through a `useAutosave`
 * whose own `debounceMs` is `0`. This hook already paces keystrokes; stacking
 * a second debounce on the autosave silently doubles user-perceived latency.
 * Unlike `useAutosaveNumericField`, this hook doesn't take the autosave
 * directly and therefore can't detect the misconfiguration — it's on the
 * caller to keep the outer `useAutosave` at `debounceMs: 0`.
 */
export function useDebouncedTextField({
  serverValue,
  onCommit,
  debounceMs = 400,
}: UseDebouncedTextFieldOptions): UseDebouncedTextFieldResult {
  const [trackedServer, setTrackedServer] = useState(serverValue);
  const [local, setLocal] = useState(serverValue);
  const [lastSent, setLastSent] = useState(serverValue);
  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  // Hold `onCommit` in a ref so the debounce timer reads the latest
  // closure at fire time. Without this, a timer armed during render N
  // would call render N's `onCommit`, which may close over stale props
  // the caller built it from (e.g. a nested-object merge against the
  // render-N cache snapshot). Assignment lives in an effect per
  // react-hooks/refs.
  const onCommitRef = useRef(onCommit);
  useEffect(() => {
    onCommitRef.current = onCommit;
  });

  // Resync when the server value changes for reasons other than our own
  // commit (rollback, another tab, refetch). Two guards:
  //   1. Only overwrite if the new server value differs from what we last
  //      sent — otherwise we'd stomp mid-edit state on every successful save.
  //   2. Don't overwrite while the user has uncommitted typed input. A
  //      divergence between `local` and `lastSent` means the user has
  //      typed something the debounce hasn't dispatched yet; that intent
  //      is more recent than any incoming server refetch. Using state
  //      rather than a ref keeps render-body lint rules happy.
  if (serverValue !== trackedServer) {
    setTrackedServer(serverValue);
    const hasPendingEdit = local !== lastSent;
    if (serverValue !== lastSent && !hasPendingEdit) {
      setLocal(serverValue);
      setLastSent(serverValue);
    }
  }

  useEffect(() => {
    return () => {
      if (debounceRef.current) clearTimeout(debounceRef.current);
    };
  }, []);

  const commit = (value: string) => {
    if (value === lastSent) return;
    setLastSent(value);
    onCommitRef.current(value);
  };

  const onChange = (next: string) => {
    setLocal(next);
    if (debounceRef.current) clearTimeout(debounceRef.current);
    debounceRef.current = setTimeout(() => {
      debounceRef.current = null;
      commit(next);
    }, debounceMs);
  };

  const onBlur = () => {
    if (debounceRef.current) {
      clearTimeout(debounceRef.current);
      debounceRef.current = null;
    }
    commit(local);
  };

  return { value: local, onChange, onBlur };
}
