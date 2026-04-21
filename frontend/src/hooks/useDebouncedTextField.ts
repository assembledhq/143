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

  if (serverValue !== trackedServer) {
    setTrackedServer(serverValue);
    if (serverValue !== lastSent) {
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
    onCommit(value);
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
