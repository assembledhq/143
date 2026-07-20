"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import {
  hashKey,
  useQueryClient,
  type QueryClient,
  type QueryKey,
} from "@tanstack/react-query";
import { notify as toast } from "@/lib/notify";
import { captureError } from "@/lib/errors";

export type AutosaveStatus = "idle" | "saving" | "saved" | "error";

export interface UseAutosaveOptions<TVars> {
  mutationFn: (vars: TVars) => Promise<unknown>;
  queryKey: QueryKey;
  applyOptimistic: (previous: unknown, vars: TVars) => unknown;
  coalesce?: (queued: TVars, incoming: TVars) => TVars;
  debounceMs?: number;
  errorMessage?: string;
  invalidateOnSettled?: boolean;
  onError?: (error: unknown, vars: TVars) => void;
  onSuccess?: (vars: TVars) => void;
}

export interface UseAutosaveResult<TVars> {
  save: (vars: TVars) => void;
  flush: () => void;
  status: AutosaveStatus;
  /**
   * The debounce window this hook was configured with. Field helpers that
   * self-debounce (e.g. `useAutosaveNumericField`) read this to detect a
   * misconfiguration where both layers would debounce and emit a dev-only
   * warning. Not intended for caller use.
   */
  debounceMs: number;
}

const SAVED_LINGER_MS = 1500;
const ERROR_LINGER_MS = 3000;
const DEFAULT_ERROR_MESSAGE = "Couldn't save. Your change was reverted.";

/**
 * A single serialized mutation queue per queryKey, shared across all components
 * that autosave against the same cache scope. Guarantees at most one in-flight
 * mutation per scope, with coalescing of queued calls.
 *
 * `mutationFn`, `applyOptimistic`, and `errorMessage` are stored on the entry
 * and refreshed on every dispatch, so a coalesced follow-up always uses the
 * latest registered fns — not the closure from whichever caller started the
 * chain. `coalesce` is registered on first dispatch and must match across all
 * callers sharing a queryKey (see `dispatch` for the conflict check).
 */
interface QueueEntry {
  inFlight: boolean;
  pendingVars: unknown;
  hasPending: boolean;
  pendingOwnerIds: Set<string>;
  coalesce?: (queued: unknown, incoming: unknown) => unknown;
  mutationFn?: (vars: unknown) => Promise<unknown>;
  applyOptimistic?: (previous: unknown, vars: unknown) => unknown;
  errorMessage?: string;
  invalidateOnSettled: boolean;
  onError?: (error: unknown, vars: unknown) => void;
  onSuccess?: (vars: unknown) => void;
  listeners: Set<(status: QueueStatus, ownerIds: ReadonlySet<string>) => void>;
}

type QueueStatus = "idle" | "saving" | "saved" | "error";

const queues = new Map<string, QueueEntry>();
let autosaveListenerID = 0;

function isPlainObject(value: unknown): value is Record<string, unknown> {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    return false;
  }
  const proto = Object.getPrototypeOf(value);
  return proto === Object.prototype || proto === null;
}

function deepEqual(a: unknown, b: unknown): boolean {
  if (Object.is(a, b)) return true;

  if (Array.isArray(a) || Array.isArray(b)) {
    if (!Array.isArray(a) || !Array.isArray(b) || a.length !== b.length) {
      return false;
    }
    for (let i = 0; i < a.length; i += 1) {
      if (!deepEqual(a[i], b[i])) return false;
    }
    return true;
  }

  if (isPlainObject(a) || isPlainObject(b)) {
    if (!isPlainObject(a) || !isPlainObject(b)) return false;
    const keysA = Object.keys(a);
    const keysB = Object.keys(b);
    if (keysA.length !== keysB.length) return false;
    for (const key of keysA) {
      if (!Object.prototype.hasOwnProperty.call(b, key)) return false;
      if (!deepEqual(a[key], b[key])) return false;
    }
    return true;
  }

  return false;
}

function getQueue(key: QueryKey): QueueEntry {
  const id = hashKey(key);
  let entry = queues.get(id);
  if (!entry) {
    entry = {
      inFlight: false,
      pendingVars: undefined,
      hasPending: false,
      pendingOwnerIds: new Set(),
      invalidateOnSettled: true,
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

/**
 * Clear the module-level queue map. Tests only — never call from app code.
 * Exported so suites can guarantee isolation between cases when they reuse
 * a queryKey or when a test leaves an in-flight/linger timer dangling.
 */
export function __resetAutosaveQueuesForTests(): void {
  queues.clear();
  autosaveListenerID = 0;
}

function emit(entry: QueueEntry, status: QueueStatus, ownerIds: ReadonlySet<string>): void {
  for (const listener of entry.listeners) {
    listener(status, ownerIds);
  }
}

async function run(
  queryClient: QueryClient,
  entry: QueueEntry,
  vars: unknown,
  queryKey: QueryKey,
  ownerIds: Set<string>,
): Promise<void> {
  entry.inFlight = true;
  emit(entry, "saving", ownerIds);

  await queryClient.cancelQueries({ queryKey });
  const previous = queryClient.getQueryData(queryKey);
  const applyOptimistic = entry.applyOptimistic!;
  const mutationFn = entry.mutationFn!;
  const errorMessage = entry.errorMessage ?? DEFAULT_ERROR_MESSAGE;
  const invalidateOnSettled = entry.invalidateOnSettled;
  queryClient.setQueryData(queryKey, (current: unknown) => applyOptimistic(current, vars));

  try {
    await mutationFn(vars);
    entry.onSuccess?.(vars);
    emit(entry, "saved", ownerIds);
  } catch (err) {
    entry.onError?.(err, vars);
    captureError(err, { feature: "useAutosave" });
    queryClient.setQueryData(queryKey, previous);
    toast.error(errorMessage);
    emit(entry, "error", ownerIds);
  } finally {
    // NOTE: `inFlight` flips to false only after `invalidateQueries` settles.
    // Any `save()` call that races in between lands in the pending slot; the
    // follow-up dispatch below drains it.
    //
    // Pending drains even after an error: a pending payload represents fresh
    // user intent (e.g. the user kept typing while the previous save was
    // failing), not a retry of the failed vars. Suppressing it would silently
    // drop edits the user made after the failure. Rollback already reverted
    // the optimistic cache for the failed write, so the pending run starts
    // from the rolled-back baseline and applies only the new intent.
    entry.inFlight = false;
    if (invalidateOnSettled) {
      await queryClient.invalidateQueries({ queryKey });
    }
    if (entry.hasPending) {
      const next = entry.pendingVars;
      const nextOwnerIds = new Set(entry.pendingOwnerIds);
      entry.hasPending = false;
      entry.pendingVars = undefined;
      entry.pendingOwnerIds.clear();
      // Fire the coalesced follow-up through the entry's currently-registered
      // fns (refreshed on every dispatch), so a caller who queued mid-flight
      // with updated mutationFn/applyOptimistic drives the next run.
      void run(queryClient, entry, next, queryKey, nextOwnerIds);
    } else {
      maybeEvictQueue(hashKey(queryKey), entry);
    }
  }
}

/**
 * useAutosave — shared autosave primitive for settings surfaces.
 *
 * Behavior:
 * - Debounces calls to `save()` by `debounceMs` (0 for toggles/selects, ~400
 *   for text). NOTE: when wiring a field through `useAutosaveNumericField` or
 *   `useDebouncedTextField`, leave this at `0` — the field helpers already
 *   self-debounce and compounding the two windows silently doubles latency.
 *   `useAutosaveNumericField` emits a dev-only warning when it sees the outer
 *   `debounceMs > 0`; text-field callers need to keep this in mind manually.
 * - Coalesces concurrent saves per `queryKey` via the user-supplied `coalesce` fn.
 *   Only one mutation is in flight per `queryKey` at a time; additional calls
 *   merge into a pending payload that fires once the in-flight resolves.
 * - Optimistic update: applies `applyOptimistic` to the cache, rolls back on
 *   error, and surfaces a Sonner toast. Always invalidates on settle.
 * - Status cycles idle → saving → saved (1.5s linger) → idle. On error:
 *   idle → saving → error (3s linger) → idle.
 * - `flush()` fires any pending debounced payload immediately (for onBlur).
 * - Unmount flushes any pending debounced payload before clearing the timer
 *   so edits typed right before navigation aren't silently dropped. Already
 *   in-flight mutations are left to complete against the shared queue.
 *
 * Callers MUST pass `applyOptimistic` because the cache shape varies per
 * resource (e.g. `settings.data.settings.<field>` vs `repositories.data`).
 *
 * `coalesce` MUST be referentially stable across all callers of a given
 * `queryKey` — use a module-level export or `useCallback`, never an inline
 * arrow. Two callers that share a `queryKey` and pass different `coalesce`
 * identities will throw in dev. See
 * `frontend/src/app/(dashboard)/settings/AGENTS.md` (“Optimistic update
 * helpers”) for the full rule and the preferred helpers.
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
  invalidateOnSettled = true,
  onError,
  onSuccess,
}: UseAutosaveOptions<TVars>): UseAutosaveResult<TVars> {
  const queryClient = useQueryClient();
  const [status, setStatus] = useState<AutosaveStatus>("idle");
  const hookIDRef = useRef<string | null>(null);
  if (hookIDRef.current === null) {
    autosaveListenerID += 1;
    hookIDRef.current = `autosave-${autosaveListenerID}`;
  }

  // Keep latest options in refs so the stable callbacks below don't churn
  // when callers re-render. Assignment lives in an effect — matching
  // `useAutosaveNumericField` / `useDebouncedTextField` — so discarded
  // concurrent-mode renders don't leak stale bindings into the ref. User
  // events that trigger saves always fire AFTER effects commit, so the ref
  // values are current by the time `dispatch` reads them.
  const mutationFnRef = useRef(mutationFn);
  const applyOptimisticRef = useRef(applyOptimistic);
  const coalesceRef = useRef(coalesce);
  const errorMessageRef = useRef(errorMessage);
  const debounceMsRef = useRef(debounceMs);
  const invalidateOnSettledRef = useRef(invalidateOnSettled);
  const onErrorRef = useRef(onError);
  const onSuccessRef = useRef(onSuccess);
  // Intentional: NO dependency array. This effect must run after every
  // commit so the refs always mirror the freshest prop/callback values a
  // parent passed in. Adding deps (e.g. `[mutationFn, applyOptimistic, ...]`)
  // would be equivalent in this case but invites future churn if callers
  // pass new inline closures each render — with no deps, React just re-runs
  // the assignment and we never miss an update. Do not "fix" this by adding
  // a dep array.
  useEffect(() => {
    mutationFnRef.current = mutationFn;
    applyOptimisticRef.current = applyOptimistic;
    coalesceRef.current = coalesce;
    errorMessageRef.current = errorMessage;
    debounceMsRef.current = debounceMs;
    invalidateOnSettledRef.current = invalidateOnSettled;
    onErrorRef.current = onError;
    onSuccessRef.current = onSuccess;
  });

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
    const listener = (next: QueueStatus, ownerIds: ReadonlySet<string>) => {
      if (!ownerIds.has(hookIDRef.current!)) {
        return;
      }
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
      const current = queryClient.getQueryData(queryKey);
      if (current !== undefined) {
        const next = applyOptimisticRef.current(current, vars);
        // Guard against spurious autosaves: if applying `vars` would leave the
        // cache unchanged, we already reached the requested end state (either
        // from the server, from our own optimistic write, or from another
        // component sharing the query scope). Skip the network call entirely.
        if (deepEqual(current, next)) {
          return;
        }
      }

      const entry = getQueue(queryKey);
      // First dispatcher wins on coalesce: if two components share a queryKey
      // with different coalesce fns, the first one registered is used for all
      // follow-ups. This keeps merges deterministic rather than dependent on
      // which component happened to dispatch last.
      if (!entry.coalesce && coalesceRef.current) {
        entry.coalesce = coalesceRef.current as (a: unknown, b: unknown) => unknown;
      } else if (
        entry.coalesce &&
        coalesceRef.current &&
        entry.coalesce !== (coalesceRef.current as (a: unknown, b: unknown) => unknown)
      ) {
        // Two components share a queryKey but passed different coalesce fns.
        // In dev this is a bug — the queue's merge semantics depend on the
        // registered coalesce and silently ignoring the later one produces
        // mismatched cache writes. Throw loudly so the conflict surfaces
        // during development. In production, fall back to keeping the first
        // so we don't crash live sessions over a merge-strategy drift.
        const message = `useAutosave: multiple callers share queryKey ${serializedKey} but supplied different coalesce functions. Every component autosaving against the same queryKey must pass an identical (referentially-stable) coalesce fn.`;
        if (process.env.NODE_ENV !== "production") {
          throw new Error(message);
        }
        console.warn(message);
      }

      // Refresh the entry's fn bindings on every dispatch so a coalesced
      // follow-up uses the latest caller's mutationFn/applyOptimistic/error
      // message rather than whichever dispatcher started the in-flight chain.
      entry.mutationFn = (v) => mutationFnRef.current(v as TVars);
      entry.applyOptimistic = (prev, v) => applyOptimisticRef.current(prev, v as TVars);
      entry.errorMessage = errorMessageRef.current;
      entry.invalidateOnSettled = invalidateOnSettledRef.current;
      entry.onError = (error, vars) => onErrorRef.current?.(error, vars as TVars);
      entry.onSuccess = (vars) => onSuccessRef.current?.(vars as TVars);

      if (entry.inFlight) {
        if (entry.hasPending && entry.coalesce) {
          entry.pendingVars = entry.coalesce(entry.pendingVars, vars);
          entry.pendingOwnerIds.add(hookIDRef.current!);
        } else {
          entry.pendingVars = vars;
          entry.pendingOwnerIds.clear();
          entry.pendingOwnerIds.add(hookIDRef.current!);
        }
        entry.hasPending = true;
        return;
      }
      void run(queryClient, entry, vars, queryKey, new Set([hookIDRef.current!]));
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

  // On unmount, flush any pending debounced payload so an edit typed right
  // before navigation isn't silently dropped. The dispatch lands in the shared
  // module-level queue, so the server round-trip survives this component
  // tearing down. In-flight mutations already started are left to complete.
  //
  // StrictMode safety: in dev, React runs effects mount → cleanup → mount
  // again. On the simulated cleanup, `debouncedVarsRef.current` is still
  // `undefined` (refs persist across the cycle but no user event has run
  // yet), so the guard `pending !== undefined` means we don't dispatch a
  // spurious save during the dev double-invoke.
  useEffect(() => {
    return () => {
      if (debounceTimerRef.current) {
        clearTimeout(debounceTimerRef.current);
        debounceTimerRef.current = null;
      }
      const pending = debouncedVarsRef.current;
      debouncedVarsRef.current = undefined;
      if (pending !== undefined) dispatch(pending);
    };
  }, [dispatch]);

  return { save, flush, status, debounceMs };
}
