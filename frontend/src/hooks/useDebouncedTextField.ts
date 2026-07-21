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
  /**
   * Optional predicate marking a typed value as invalid (e.g. a required field
   * left blank). A rejected value is never committed — neither the debounce nor
   * the blur fires `onCommit`, and `lastSent` is not advanced, so it can't be
   * "remembered" as sent. On blur the field reverts to the last committed value
   * so a required field can't be left in a dropped/blank state. Mid-typing the
   * user still sees their input; rejection only suppresses the save and the
   * blur snaps it back. Omit for fields where an invalid value should stay
   * visible with its own error affordance (e.g. an over-length editor).
   */
  rejectValue?: (value: string) => boolean;
  preserveLocalOnServerChange?: boolean;
  /**
   * Optional semantic equality check for fields whose server canonicalizes
   * submitted text. For example, a prompt editor can treat `"policy "` and
   * `"policy"` as equal when the backend trims surrounding whitespace. This
   * prevents the canonical response from rewriting the active textarea while
   * still allowing genuinely different server values to resync it.
   */
  valuesEqual?: (left: string, right: string) => boolean;
}

export interface UseDebouncedTextFieldResult {
  value: string;
  onChange: (next: string) => void;
  onBlur: () => void;
  replace: (next: string) => void;
  flush: () => void;
  dirty: boolean;
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
  rejectValue,
  preserveLocalOnServerChange = false,
  valuesEqual = Object.is,
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
  const rejectValueRef = useRef(rejectValue);
  const valuesEqualRef = useRef(valuesEqual);
  useEffect(() => {
    onCommitRef.current = onCommit;
    rejectValueRef.current = rejectValue;
    valuesEqualRef.current = valuesEqual;
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
    const hasPendingEdit = !valuesEqual(local, lastSent);
    if (!valuesEqual(serverValue, lastSent) && !hasPendingEdit && !preserveLocalOnServerChange) {
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
    if (valuesEqualRef.current(value, lastSent)) return;
    // A rejected value is never sent and never recorded as `lastSent`, so it
    // can't poison the resync baseline or be mistaken for a saved value.
    if (rejectValueRef.current?.(value)) return;
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
    // Revert an invalid value on blur so a required field can't be left in a
    // dropped/blank state with the stale server value silently still in effect.
    if (rejectValueRef.current?.(local)) {
      if (local !== lastSent) setLocal(lastSent);
      return;
    }
    commit(local);
  };

  const replace = (next: string) => {
    if (debounceRef.current) {
      clearTimeout(debounceRef.current);
      debounceRef.current = null;
    }
    setLocal(next);
    setLastSent(next);
  };

  return { value: local, onChange, onBlur, replace, flush: onBlur, dirty: !valuesEqual(local, lastSent) };
}
