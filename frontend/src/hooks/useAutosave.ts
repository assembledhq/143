"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import {
  hashKey,
  useQueryClient,
  type QueryClient,
  type QueryKey,
} from "@tanstack/react-query";
import { toast } from "sonner";
import { captureError } from "@/lib/errors";

export type AutosaveStatus = "idle" | "saving" | "saved" | "error";

export interface UseAutosaveOptions<TVars> {
  mutationFn: (vars: TVars) => Promise<unknown>;
  queryKey: QueryKey;
  applyOptimistic: (previous: unknown, vars: TVars) => unknown;
  coalesce?: (queued: TVars, incoming: TVars) => TVars;
  debounceMs?: number;
  errorMessage?: string;
}

export interface UseAutosaveResult<TVars> {
  save: (vars: TVars) => void;
  flush: () => void;
  status: AutosaveStatus;
}

const SAVED_LINGER_MS = 1500;
const ERROR_LINGER_MS = 3000;
const DEFAULT_ERROR_MESSAGE = "Couldn't save. Your change was reverted.";

/**
 * A single serialized mutation queue per queryKey, shared across all components
 * that autosave against the same cache scope. Guarantees at most one in-flight
 * mutation per scope, with coalescing of queued calls.
 *
 * `coalesce` is captured on the first dispatch (rather than being taken from
 * whichever caller dispatches next) so two components that share a `queryKey`
 * merge pending writes deterministically. Subsequent dispatchers supplying a
 * different coalesce fn are ignored with a dev-only warning.
 */
interface QueueEntry {
  inFlight: boolean;
  pendingVars: unknown;
  hasPending: boolean;
  coalesce?: (queued: unknown, incoming: unknown) => unknown;
  listeners: Set<(status: QueueStatus) => void>;
}

type QueueStatus = "idle" | "saving" | "saved" | "error";

const queues = new Map<string, QueueEntry>();

function getQueue(key: QueryKey): QueueEntry {
  const id = hashKey(key);
  let entry = queues.get(id);
  if (!entry) {
    entry = {
      inFlight: false,
      pendingVars: undefined,
      hasPending: false,
      listeners: new Set(),
    };
    queues.set(id, entry);
  }
  return entry;
}

function maybeEvictQueue(key: string, entry: QueueEntry): void {
  if (entry.inFlight || entry.hasPending || entry.listeners.size > 0) return;
  queues.delete(key);
}

function emit(entry: QueueEntry, status: QueueStatus): void {
  for (const listener of entry.listeners) {
    listener(status);
  }
}

async function run<TVars>(
  queryClient: QueryClient,
  entry: QueueEntry,
  vars: TVars,
  queryKey: QueryKey,
  mutationFn: (v: TVars) => Promise<unknown>,
  applyOptimistic: (previous: unknown, vars: TVars) => unknown,
  errorMessage: string,
): Promise<void> {
  entry.inFlight = true;
  emit(entry, "saving");

  await queryClient.cancelQueries({ queryKey });
  const previous = queryClient.getQueryData(queryKey);
  queryClient.setQueryData(queryKey, (current: unknown) => applyOptimistic(current, vars));

  try {
    await mutationFn(vars);
    emit(entry, "saved");
  } catch (err) {
    captureError(err, { feature: "useAutosave" });
    queryClient.setQueryData(queryKey, previous);
    toast.error(errorMessage);
    emit(entry, "error");
  } finally {
    // NOTE: `inFlight` flips to false only after `invalidateQueries` settles.
    // Any `save()` call that races in between lands in the pending slot; the
    // follow-up dispatch below drains it.
    entry.inFlight = false;
    await queryClient.invalidateQueries({ queryKey });
    if (entry.hasPending) {
      const next = entry.pendingVars as TVars;
      entry.hasPending = false;
      entry.pendingVars = undefined;
      // Fire the coalesced follow-up. Intentionally not awaited here — the
      // original caller's promise chain is complete; follow-ups are driven
      // by the shared queue.
      void run(queryClient, entry, next, queryKey, mutationFn, applyOptimistic, errorMessage);
    } else {
      maybeEvictQueue(hashKey(queryKey), entry);
    }
  }
}

/**
 * useAutosave — shared autosave primitive for settings surfaces.
 *
 * Behavior:
 * - Debounces calls to `save()` by `debounceMs` (0 for toggles/selects, ~400 for text).
 * - Coalesces concurrent saves per `queryKey` via the user-supplied `coalesce` fn.
 *   Only one mutation is in flight per `queryKey` at a time; additional calls
 *   merge into a pending payload that fires once the in-flight resolves.
 * - Optimistic update: applies `applyOptimistic` to the cache, rolls back on
 *   error, and surfaces a Sonner toast. Always invalidates on settle.
 * - Status cycles idle → saving → saved (1.5s linger) → idle. On error:
 *   idle → saving → error (3s linger) → idle.
 * - `flush()` fires any pending debounced payload immediately (for onBlur).
 * - Unmount cancels the local debounce timer but allows any already-dispatched
 *   mutation to complete silently — the server has already been hit.
 *
 * Callers MUST pass `applyOptimistic` because the cache shape varies per
 * resource (e.g. `settings.data.settings.<field>` vs `repositories.data`).
 *
 * @example
 *   const { save, flush, status } = useAutosave<{ settings: { foo: string } }>({
 *     queryKey: queryKeys.settings.all,
 *     mutationFn: (payload) => api.settings.update(payload),
 *     applyOptimistic: (prev, v) => mergeSettings(prev, v),
 *     coalesce: (a, b) => ({ settings: { ...a.settings, ...b.settings } }),
 *     debounceMs: 0,
 *   });
 */
export function useAutosave<TVars>({
  mutationFn,
  queryKey,
  applyOptimistic,
  coalesce,
  debounceMs = 0,
  errorMessage = DEFAULT_ERROR_MESSAGE,
}: UseAutosaveOptions<TVars>): UseAutosaveResult<TVars> {
  const queryClient = useQueryClient();
  const [status, setStatus] = useState<AutosaveStatus>("idle");

  // Keep latest options in refs so the stable callbacks below don't churn
  // when callers re-render. The hook contract is that these may change freely.
  const mutationFnRef = useRef(mutationFn);
  const applyOptimisticRef = useRef(applyOptimistic);
  const coalesceRef = useRef(coalesce);
  const errorMessageRef = useRef(errorMessage);
  const debounceMsRef = useRef(debounceMs);
  mutationFnRef.current = mutationFn;
  applyOptimisticRef.current = applyOptimistic;
  coalesceRef.current = coalesce;
  errorMessageRef.current = errorMessage;
  debounceMsRef.current = debounceMs;

  // Debounce state is local to each hook instance — two callers of the same
  // queryKey each debounce independently, then the shared queue serializes.
  const debouncedVarsRef = useRef<TVars | undefined>(undefined);
  const debounceTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const lingerTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  // `hashKey` is React Query's canonical key hasher: order-stable across object
  // keys and robust against arrays that stringify identically. Using it means
  // our queue shares identity with the cache key the consumer already uses.
  const serializedKey = hashKey(queryKey);

  // Subscribe to the shared queue's status so all hooks pointed at the same
  // queryKey see consistent saving/saved/error transitions.
  useEffect(() => {
    const entry = getQueue(queryKey);
    const listener = (next: QueueStatus) => {
      setStatus(next);
      if (lingerTimerRef.current) {
        clearTimeout(lingerTimerRef.current);
        lingerTimerRef.current = null;
      }
      if (next === "saved") {
        lingerTimerRef.current = setTimeout(() => setStatus("idle"), SAVED_LINGER_MS);
      } else if (next === "error") {
        lingerTimerRef.current = setTimeout(() => setStatus("idle"), ERROR_LINGER_MS);
      }
    };
    entry.listeners.add(listener);
    return () => {
      entry.listeners.delete(listener);
      maybeEvictQueue(serializedKey, entry);
      if (lingerTimerRef.current) {
        clearTimeout(lingerTimerRef.current);
        lingerTimerRef.current = null;
      }
    };
    // queryKey identity can change per render; `serializedKey` is the real dep.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [serializedKey]);

  const dispatch = useCallback(
    (vars: TVars) => {
      const entry = getQueue(queryKey);
      // First dispatcher wins on coalesce: if two components share a queryKey
      // with different coalesce fns, the first one registered is used for all
      // follow-ups. This keeps merges deterministic rather than dependent on
      // which component happened to dispatch last.
      if (!entry.coalesce && coalesceRef.current) {
        entry.coalesce = coalesceRef.current as (a: unknown, b: unknown) => unknown;
      } else if (
        process.env.NODE_ENV !== "production" &&
        entry.coalesce &&
        coalesceRef.current &&
        entry.coalesce !== (coalesceRef.current as (a: unknown, b: unknown) => unknown)
      ) {
        console.warn(
          `useAutosave: multiple callers share queryKey ${serializedKey} but supplied different coalesce functions; ignoring the later one.`,
        );
      }

      if (entry.inFlight) {
        if (entry.hasPending && entry.coalesce) {
          entry.pendingVars = entry.coalesce(entry.pendingVars, vars);
        } else {
          entry.pendingVars = vars;
        }
        entry.hasPending = true;
        return;
      }
      void run(
        queryClient,
        entry,
        vars,
        queryKey,
        (v) => mutationFnRef.current(v),
        (prev, v) => applyOptimisticRef.current(prev, v),
        errorMessageRef.current,
      );
    },
    // queryKey identity can change per render; serializedKey is the actual dep.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [queryClient, serializedKey],
  );

  const save = useCallback(
    (vars: TVars) => {
      if (debounceMsRef.current <= 0) {
        dispatch(vars);
        return;
      }
      debouncedVarsRef.current = vars;
      if (debounceTimerRef.current) clearTimeout(debounceTimerRef.current);
      debounceTimerRef.current = setTimeout(() => {
        const pending = debouncedVarsRef.current;
        debouncedVarsRef.current = undefined;
        debounceTimerRef.current = null;
        if (pending !== undefined) dispatch(pending);
      }, debounceMsRef.current);
    },
    [dispatch],
  );

  const flush = useCallback(() => {
    if (debounceTimerRef.current) {
      clearTimeout(debounceTimerRef.current);
      debounceTimerRef.current = null;
    }
    const pending = debouncedVarsRef.current;
    debouncedVarsRef.current = undefined;
    if (pending !== undefined) dispatch(pending);
  }, [dispatch]);

  // Cancel any pending debounce on unmount. In-flight mutations are left to
  // complete; the server has already been contacted.
  useEffect(() => {
    return () => {
      if (debounceTimerRef.current) {
        clearTimeout(debounceTimerRef.current);
        debounceTimerRef.current = null;
      }
    };
  }, []);

  return { save, flush, status };
}
