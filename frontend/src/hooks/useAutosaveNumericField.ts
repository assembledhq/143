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
   * Defaults to 400ms, matching the text-input convention. This differs
   * from `useAutosave`'s own default of 0ms: `useAutosave` assumes discrete
   * toggle/select events, whereas numeric fields are typed one character at
   * a time and need the field-level pacing here.
   */
  debounceMs?: number;
}

export interface UseAutosaveNumericFieldResult {
  value: string;
  onChange: (event: ChangeEvent<HTMLInputElement>) => void;
  onBlur: () => void;
  /** Atomically update the displayed value and save without going through the
   *  debounce path. Useful for programmatic steps (e.g. +/− buttons) where the
   *  caller has already computed the desired final value. */
  setValueAndSave: (n: number) => void;
}

/**
 * Binds a numeric `<input>` to an autosave scope.
 *
 * - Tracks the raw string the user is typing in local state so partially
 *   typed values like "" or "1" render cleanly without being clobbered by
 *   optimistic refetches.
 * - Self-debounces change events by `debounceMs` (default 400ms) so rapid
 *   typing doesn't spam the scope's autosave queue. The surrounding
 *   `useAutosave` MUST be left at `debounceMs: 0`; the numeric helper handles
 *   pacing at the field level so selects/radios on the same scope still fire
 *   instantly. The hook emits a dev-only `console.warn` if the outer
 *   `useAutosave` is misconfigured with a non-zero debounce.
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

  // Guard against the double-debounce footgun: if the outer `useAutosave` also
  // debounces, keystrokes wait out BOTH windows before hitting the network,
  // which is almost never what the caller wants. Warn once per hook instance
  // in dev; production silently tolerates it to avoid noisy console warnings
  // on live traffic. The check lives in an effect because `react-hooks/refs`
  // forbids reading `ref.current` during render.
  const hasWarnedRef = useRef(false);
  useEffect(() => {
    if (
      process.env.NODE_ENV !== "production" &&
      !hasWarnedRef.current &&
      autosave.debounceMs > 0
    ) {
      hasWarnedRef.current = true;
      console.warn(
        `useAutosaveNumericField: the passed useAutosave hook is configured with debounceMs=${autosave.debounceMs}. ` +
          `The field already self-debounces (${debounceMs}ms); set the outer useAutosave to debounceMs: 0 to avoid compounding both windows.`,
      );
    }
  }, [autosave.debounceMs, debounceMs]);

  // Hold `toPatch` and `clamp` in refs so the debounce timer reads the
  // latest closures at fire time. Without this, a timer armed during
  // render N would call render N's `toPatch`, which may close over stale
  // props (e.g. `repoSettings.pm` that has since been optimistically
  // updated). Assignment lives in an effect per react-hooks/refs.
  const toPatchRef = useRef(toPatch);
  const clampRef = useRef(clamp);
  useEffect(() => {
    toPatchRef.current = toPatch;
    clampRef.current = clamp;
  });

  // Resync when the server value changes for reasons other than our own
  // save (rollback, another tab, refetch). Two guards:
  //   1. Only overwrite if the new server value differs from what we last
  //      sent — otherwise we'd stomp mid-edit state on every successful save.
  //   2. Don't overwrite while the user has uncommitted typed input. A
  //      divergence between `local` and `String(lastSent)` means the user
  //      has typed something the debounce hasn't dispatched yet; that
  //      intent is more recent than any incoming server refetch. Using
  //      state rather than a ref keeps render-body lint rules happy.
  if (serverValue !== trackedServer) {
    setTrackedServer(serverValue);
    const hasPendingEdit = local !== String(lastSent);
    if (serverValue !== lastSent && !hasPendingEdit) {
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
    autosave.save(toPatchRef.current(clamped));
  };

  const onChange = (event: ChangeEvent<HTMLInputElement>) => {
    const raw = event.target.value;
    setLocal(raw);
    if (raw.trim() === "") return;
    const parsed = Number.parseInt(raw, 10);
    if (Number.isNaN(parsed)) return;
    const clamped = clampRef.current ? clampRef.current(parsed) : parsed;
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
    const clamped = clampRef.current ? clampRef.current(parsed) : parsed;
    if (String(clamped) !== local) setLocal(String(clamped));
    pendingValueRef.current = null;
    if (clamped !== lastSent) dispatch(clamped);
  };

  const setValueAndSave = (n: number) => {
    if (debounceTimerRef.current) {
      clearTimeout(debounceTimerRef.current);
      debounceTimerRef.current = null;
    }
    pendingValueRef.current = null;
    const clamped = clampRef.current ? clampRef.current(n) : n;
    setLocal(String(clamped));
    dispatch(clamped);
  };

  return { value: local, onChange, onBlur, setValueAndSave };
}
