"use client";

import { useEffect, useRef, useState, type ChangeEvent } from "react";
import type { UseAutosaveResult } from "./useAutosave";

export interface UseAutosaveNumericFieldOptions<TVars> {
  serverValue: number;
  autosave: UseAutosaveResult<TVars>;
  toPatch: (clamped: number) => TVars;
  clamp?: (raw: number) => number;
  /**
   * How long to wait after the last keystroke before dispatching the save.
   * Defaults to 400ms, matching the text-input convention.
   */
  debounceMs?: number;
}

export interface UseAutosaveNumericFieldResult {
  value: string;
  onChange: (event: ChangeEvent<HTMLInputElement>) => void;
  onBlur: () => void;
}

/**
 * Binds a numeric `<input>` to an autosave scope.
 *
 * - Tracks the raw string the user is typing in local state so partially
 *   typed values like "" or "1" render cleanly without being clobbered by
 *   optimistic refetches.
 * - Self-debounces change events by `debounceMs` (default 400ms) so rapid
 *   typing doesn't spam the scope's autosave queue. The surrounding
 *   `useAutosave` should be left at `debounceMs: 0`; the numeric helper
 *   handles pacing at the field level so selects/radios on the same scope
 *   still fire instantly.
 * - On blur, cancels the debounce timer, flushes the last known value
 *   immediately, and snaps invalid/empty input back to the authoritative
 *   server value.
 * - When the server value updates for reasons other than the caller's own
 *   save (error rollback, another tab, a refetch), the local display
 *   resyncs via the "store info from previous renders" pattern so no
 *   effect-driven setState is needed.
 */
export function useAutosaveNumericField<TVars>({
  serverValue,
  autosave,
  toPatch,
  clamp,
  debounceMs = 400,
}: UseAutosaveNumericFieldOptions<TVars>): UseAutosaveNumericFieldResult {
  const [trackedServer, setTrackedServer] = useState(serverValue);
  const [local, setLocal] = useState(() => String(serverValue));
  const [lastSent, setLastSent] = useState(serverValue);

  const debounceTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const pendingValueRef = useRef<number | null>(null);

  // Resync when the server value changes for reasons other than our own
  // save (rollback, another tab, refetch). Guard: only overwrite local if
  // the new server value differs from what we last sent — otherwise we'd
  // stomp mid-edit state on every successful save.
  if (serverValue !== trackedServer) {
    setTrackedServer(serverValue);
    if (serverValue !== lastSent) {
      setLocal(String(serverValue));
      setLastSent(serverValue);
    }
  }

  useEffect(() => {
    return () => {
      if (debounceTimerRef.current) {
        clearTimeout(debounceTimerRef.current);
        debounceTimerRef.current = null;
      }
    };
  }, []);

  const dispatch = (clamped: number) => {
    setLastSent(clamped);
    autosave.save(toPatch(clamped));
  };

  const onChange = (event: ChangeEvent<HTMLInputElement>) => {
    const raw = event.target.value;
    setLocal(raw);
    if (raw.trim() === "") return;
    const parsed = Number.parseInt(raw, 10);
    if (Number.isNaN(parsed)) return;
    const clamped = clamp ? clamp(parsed) : parsed;
    pendingValueRef.current = clamped;
    if (debounceTimerRef.current) clearTimeout(debounceTimerRef.current);
    debounceTimerRef.current = setTimeout(() => {
      debounceTimerRef.current = null;
      const value = pendingValueRef.current;
      pendingValueRef.current = null;
      if (value !== null) dispatch(value);
    }, debounceMs);
  };

  const onBlur = () => {
    if (debounceTimerRef.current) {
      clearTimeout(debounceTimerRef.current);
      debounceTimerRef.current = null;
    }
    const parsed = Number.parseInt(local, 10);
    if (Number.isNaN(parsed)) {
      setLocal(String(serverValue));
      setLastSent(serverValue);
      pendingValueRef.current = null;
      return;
    }
    const clamped = clamp ? clamp(parsed) : parsed;
    if (String(clamped) !== local) setLocal(String(clamped));
    pendingValueRef.current = null;
    if (clamped !== lastSent) dispatch(clamped);
  };

  return { value: local, onChange, onBlur };
}
