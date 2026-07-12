import type { QueryClient, QueryKey } from "@tanstack/react-query";

export const LIVE_SCHEMA_VERSION = 1;
export const LIVE_HEARTBEAT_TIMEOUT_MS = 45_000;

export type LiveEventType =
  | "session.created" | "session.updated" | "preview.updated"
  | "automation.updated" | "automation.run.updated"
  | "code_review.updated" | "pull_request.updated"
  | "eval_batch.updated" | "eval_bootstrap.updated";

export interface LiveEvent {
  schema_version: number;
  event_id: string;
  type: LiveEventType;
  scope: "resource" | "collection";
  org_id: string;
  resource_type: string;
  resource_id?: string;
  parent_type?: string;
  parent_id?: string;
  repository_id?: string;
  audience: "org" | "repository" | "resource";
  version?: number;
  causation_id?: string;
  changed_at: string;
  payload: {
    status_projection?: Record<string, unknown>;
    list_affected?: boolean;
    counts_affected?: boolean;
  };
}

interface LiveReady {
  schema_version: number;
  initial_sync_required: boolean;
  through_stream_id: string;
  bus_health_epoch: number;
}

interface LiveResync { cause: string; through_stream_id: string }

export type LiveHealth = "connecting" | "healthy" | "degraded" | "degraded-sustained" | "offline";

export type LiveQueryFamily =
  | "session.list" | "session.counts" | "session.detail" | "session.timeline" | "session.transcript" | "session.human-input" | "session.file-events"
  | "preview.list" | "preview.detail" | "automation.list" | "automation.detail" | "automation.runs" | "automation.stats"
  | "code-review.list" | "pull-request.detail" | "eval.list" | "eval.detail";
export type LiveQueryPriority = "critical" | "secondary" | "inactive";

export interface LiveQueryRegistration {
  queryKey: QueryKey;
  families: readonly LiveQueryFamily[];
  resourceId?: string;
  priority?: LiveQueryPriority;
  visible: boolean;
  resourceStreamOwnsDetail?: boolean;
}

const registrations = new WeakMap<QueryClient, Map<string, LiveQueryRegistration>>();
const dirtyQueries = new WeakMap<QueryClient, Set<string>>();

export function registerLiveQuery(queryClient: QueryClient, registration: LiveQueryRegistration): () => void {
  let values = registrations.get(queryClient);
  if (!values) { values = new Map(); registrations.set(queryClient, values); }
  const hash = JSON.stringify(registration.queryKey);
  values.set(hash, registration);
  return () => values?.delete(hash);
}

const effectRoots: Record<LiveEventType, readonly string[]> = {
  "session.created": ["sessions"],
  "session.updated": ["sessions", "session", "preview-status"],
  "preview.updated": ["previews", "preview", "preview-status", "preview-logs", "branch-preview", "branch-preview-pr"],
  "automation.updated": ["automations", "automation"],
  "automation.run.updated": ["automations", "automation", "automation-runs", "automation-stats"],
  "code_review.updated": ["code-reviews"],
  "pull_request.updated": ["session", "pull-request"],
  "eval_batch.updated": ["evals"],
  "eval_bootstrap.updated": ["evals"],
};

const recentClientMutations = new Map<string, number>();
export function registerClientMutation(id: string): void {
  const now = Date.now(); recentClientMutations.set(id, now);
  for (const [key, createdAt] of recentClientMutations) if (now - createdAt > 5 * 60_000) recentClientMutations.delete(key);
}

type ProjectionRecord = { resourceId: string; version: number; projection: Record<string, unknown>; roots: readonly string[] };
const projectionOverlays = new WeakMap<QueryClient, Map<string, ProjectionRecord>>();
const subscribedClients = new WeakSet<QueryClient>();
const projectingClients = new WeakSet<QueryClient>();

function emitLiveTelemetry(name: string, fields: Record<string, string | number | boolean> = {}): void {
  if (typeof window === "undefined") return;
  const sampleKey = localStorage.getItem("143:browser-client-id") ?? "unsampled";
  let sample = 0;
  for (let index = 0; index < sampleKey.length; index++) sample = (sample * 31 + sampleKey.charCodeAt(index)) >>> 0;
  if (sample % 10 !== 0) return;
  window.dispatchEvent(new CustomEvent("143:live-telemetry", { detail: { name, ...fields } }));
}

function rememberProjection(queryClient: QueryClient, event: LiveEvent): void {
  if (!event.resource_id || !event.version || !event.payload.status_projection) return;
  let overlays = projectionOverlays.get(queryClient);
  if (!overlays) { overlays = new Map(); projectionOverlays.set(queryClient, overlays); }
  const current = overlays.get(event.resource_id);
  if (!current || current.version < event.version) overlays.set(event.resource_id, {resourceId:event.resource_id,version:event.version,projection:event.payload.status_projection,roots:effectRoots[event.type]});
  if (subscribedClients.has(queryClient)) return;
  subscribedClients.add(queryClient);
  let applying = false;
  queryClient.getQueryCache().subscribe((notification) => {
    if (applying || projectingClients.has(queryClient) || notification.type !== "updated" || notification.query.state.status !== "success") return;
    const root = String(notification.query.queryKey[0]);
    const overlays = projectionOverlays.get(queryClient);
    const records = [...(overlays?.values() ?? [])].filter((record) => record.roots.includes(root));
    if (records.length === 0) return;
    const responseReceivedAt = performance.now();
    let next = notification.query.state.data; let changed = false;
    for (const record of records) {
      if (containsEntityVersion(next, record.resourceId, record.version)) {
        overlays?.delete(record.resourceId);
        continue;
      }
      const result = patchEntity(next,record.resourceId,record.version,record.projection); next=result[0]; changed ||= result[1];
    }
    if (changed) { applying=true; queryClient.setQueryData(notification.query.queryKey,next); applying=false; }
    requestAnimationFrame(() => emitLiveTelemetry("canonical_rendered", { duration_ms: Math.max(0, performance.now() - responseReceivedAt), root }));
  });
}

function containsEntityVersion(value: unknown, resourceId: string, minimumVersion: number): boolean {
  if (Array.isArray(value)) return value.some((item) => containsEntityVersion(item, resourceId, minimumVersion));
  if (!value || typeof value !== "object") return false;
  const record = value as Record<string, unknown>;
  if (record.id === resourceId) return typeof record.live_version === "number" && record.live_version >= minimumVersion;
  return Object.values(record).some((child) => containsEntityVersion(child, resourceId, minimumVersion));
}

function hasPendingProjection(queryClient: QueryClient, queryKey: QueryKey): boolean {
  const root = String(queryKey[0]);
  return [...(projectionOverlays.get(queryClient)?.values() ?? [])].some((record) => record.roots.includes(root));
}

export function decodeLiveEvent(raw: string): LiveEvent | null {
  try {
    const event = JSON.parse(raw) as Partial<LiveEvent>;
    if (event.schema_version !== LIVE_SCHEMA_VERSION || typeof event.event_id !== "string" ||
        typeof event.type !== "string" || !(event.type in effectRoots) || typeof event.org_id !== "string" ||
        (event.scope !== "resource" && event.scope !== "collection") ||
        (event.audience !== "org" && event.audience !== "repository" && event.audience !== "resource") ||
        typeof event.resource_type !== "string" || !isResourceTypeForEvent(event.type as LiveEventType, event.resource_type) ||
        (event.scope === "resource" && typeof event.resource_id !== "string") ||
        (event.scope === "collection" && event.resource_id !== undefined) ||
        (event.audience === "repository" && typeof event.repository_id !== "string") ||
        (event.audience === "resource" && typeof event.resource_id !== "string") ||
        typeof event.changed_at !== "string" || Number.isNaN(Date.parse(event.changed_at)) ||
        typeof event.payload !== "object" || event.payload === null ||
        (event.payload.status_projection !== undefined &&
          (typeof event.version !== "number" || !Number.isSafeInteger(event.version) || event.version <= 0))) return null;
    if (!validProjection(event as LiveEvent)) return null;
    return event as LiveEvent;
  } catch { return null; }
}

const resourceTypeByEvent: Record<LiveEventType, string> = {
  "session.created": "session", "session.updated": "session", "preview.updated": "preview",
  "automation.updated": "automation", "automation.run.updated": "automation_run",
  "code_review.updated": "code_review", "pull_request.updated": "pull_request",
  "eval_batch.updated": "eval_batch", "eval_bootstrap.updated": "eval_bootstrap",
};

function isResourceTypeForEvent(type: LiveEventType, resourceType: string): boolean {
  return resourceTypeByEvent[type] === resourceType;
}

function validProjection(event: LiveEvent): boolean {
  const projection = event.payload.status_projection;
  if (projection === undefined) return true;
  if (!projection || typeof projection !== "object" || Array.isArray(projection)) return false;
  const allowed: Partial<Record<LiveEventType, readonly string[]>> = {
    "session.updated": ["status", "pr_creation_state", "pr_push_state", "branch_creation_state"],
    "preview.updated": ["status", "freshness"],
    "automation.updated": ["enabled"],
    "automation.run.updated": ["status"],
  };
  const keys = allowed[event.type];
  if (!keys || Object.keys(projection).some((key) => !keys.includes(key))) return false;
  if (event.type === "automation.updated" && typeof projection.enabled !== "boolean") return false;
  return Object.entries(projection).every(([key, value]) => key === "enabled" || typeof value === "string");
}

function patchEntity(value: unknown, resourceId: string, version: number, projection: Record<string, unknown>): [unknown, boolean] {
  if (Array.isArray(value)) {
    let changed = false;
    const next = value.map((item) => { const [patched, didChange] = patchEntity(item, resourceId, version, projection); changed ||= didChange; return patched; });
    return [changed ? next : value, changed];
  }
  if (!value || typeof value !== "object") return [value, false];
  const record = value as Record<string, unknown>;
  if (record.id === resourceId) {
    const current = typeof record.live_version === "number" ? record.live_version : 0;
    if (current >= version) return [value, false];
    return [{ ...record, ...projection, live_version: version }, true];
  }
  let changed = false;
  const next: Record<string, unknown> = { ...record };
  for (const [key, child] of Object.entries(record)) {
    const [patched, didChange] = patchEntity(child, resourceId, version, projection);
    if (didChange) { next[key] = patched; changed = true; }
  }
  return [changed ? next : value, changed];
}

export function applyLiveProjection(queryClient: QueryClient, event: LiveEvent): boolean {
  if (!event.resource_id || !event.version || !event.payload.status_projection) return false;
  rememberProjection(queryClient,event);
  let applied = false;
  projectingClients.add(queryClient);
  try {
    for (const root of effectRoots[event.type]) {
      queryClient.setQueriesData({ predicate: (query) => query.queryKey[0] === root }, (old) => {
        const [next, changed] = patchEntity(old, event.resource_id!, event.version!, event.payload.status_projection!);
        applied ||= changed;
        return next;
      });
    }
  } finally {
    projectingClients.delete(queryClient);
  }
  return applied;
}

type DirtyState = { fetching: boolean; trailing: boolean; generation: number; cancelIssued: boolean; dirtyAt: number };

export class LiveInvalidationScheduler {
  private state = new Map<string, DirtyState>();
  constructor(private readonly queryClient: QueryClient) {}

  async dirty(event: LiveEvent): Promise<void> {
    const roots = effectRoots[event.type];
    const registered = registrations.get(this.queryClient);
    const dirty = dirtyQueries.get(this.queryClient) ?? new Set<string>();
    dirtyQueries.set(this.queryClient, dirty);
    for (const query of this.queryClient.getQueryCache().getAll()) {
      if (!roots.includes(String(query.queryKey[0]))) continue;
      const hash = JSON.stringify(query.queryKey);
      const registration = registered?.get(hash);
	  if (registration && !registration.families.some((family) => eventAffectsFamily(event, family))) continue;
      if (event.resource_id && registration?.resourceId && registration.resourceId !== event.resource_id && registration.resourceId !== event.parent_id) continue;
      if (event.resource_id && registration?.resourceStreamOwnsDetail && registration.resourceId === event.resource_id) continue;
      await this.queryClient.invalidateQueries({ queryKey: query.queryKey, exact: true, refetchType: "none" });
      dirty.add(hash);
      const scheduleState = this.state.get(hash);
      if (query.state.fetchStatus === "fetching" && !scheduleState?.cancelIssued) {
        const next = scheduleState ?? { fetching: false, trailing: false, generation: 0, cancelIssued: false, dirtyAt: performance.now() };
        next.cancelIssued = true;
        this.state.set(hash, next);
        await this.queryClient.cancelQueries({ queryKey: query.queryKey, exact: true }, { silent: true });
      }
      if (document.visibilityState !== "visible" || !registration?.visible || registration.priority === "inactive") {
        emitLiveTelemetry("hidden_refetch_suppressed", { family: registration?.families[0] ?? String(query.queryKey[0]) });
        continue;
      }
      await this.schedule(query.queryKey, registration.priority ?? "secondary");
    }
  }

  async catchUpVisible(force = false): Promise<void> {
    if (document.visibilityState !== "visible") return;
    const registered = registrations.get(this.queryClient);
    const dirty = dirtyQueries.get(this.queryClient);
    if (!registered) return;
    const visible = [...registered.entries()].filter(([hash, value]) => (force || dirty?.has(hash)) && value.visible && value.priority !== "inactive");
    visible.sort(([, left], [, right]) => (left.priority === "critical" ? -1 : 1) - (right.priority === "critical" ? -1 : 1));
    await Promise.all(visible.map(([, value]) => this.schedule(value.queryKey, value.priority ?? "secondary")));
  }

  private async schedule(queryKey: QueryKey, priority: LiveQueryPriority): Promise<void> {
    const hash = JSON.stringify(queryKey);
    const state = this.state.get(hash) ?? { fetching: false, trailing: false, generation: 0, cancelIssued: false, dirtyAt: performance.now() };
    state.generation++;
    if (state.fetching) { state.trailing = true; this.state.set(hash, state); return; }
    state.fetching = true; this.state.set(hash, state);
    try {
      do {
        state.trailing = false;
        if (priority === "secondary") await new Promise((resolve) => setTimeout(resolve, 250 + Math.floor(Math.random() * 251)));
        const startedAt = performance.now();
        emitLiveTelemetry("refetch_started", { latency_ms: Math.round(startedAt - state.dirtyAt), root: String(queryKey[0]) });
        await this.queryClient.refetchQueries({ queryKey, exact: true, type: "active" }, { cancelRefetch: false, throwOnError: true });
        const queryError = this.queryClient.getQueryState(queryKey)?.error;
        if (queryError) throw queryError;
        emitLiveTelemetry("refetch_completed", { duration_ms: Math.round(performance.now() - startedAt), root: String(queryKey[0]) });
        if (!hasPendingProjection(this.queryClient, queryKey)) dirtyQueries.get(this.queryClient)?.delete(hash);
      } while (state.trailing && document.visibilityState === "visible");
    } finally {
      state.fetching = false;
      this.state.delete(hash);
    }
  }
}

function eventAffectsFamily(event: LiveEvent, family: LiveQueryFamily): boolean {
  if (family === "session.counts" || family === "automation.stats") return event.payload.counts_affected !== false;
  if (family.endsWith(".list")) return event.payload.list_affected !== false;
  return true;
}

export interface LiveEventClientOptions {
  apiBase: string;
  orgId: string;
  queryClient: QueryClient;
  onHealth?: (health: LiveHealth) => void;
  onEventProcessed?: (event: LiveEvent, cursor: string) => void;
}

export class LiveEventClient {
  private source: EventSource | null = null;
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  private heartbeatTimer: ReturnType<typeof setTimeout> | null = null;
  private attempt = 0;
  private stopped = false;
  private resumeCursor = "";
  private scheduler: LiveInvalidationScheduler;
  private seen = new Set<string>();
  private pendingCheckpoint: { cursor: string; cause: string; jitterApplied: boolean } | null = null;
  private currentHealth: LiveHealth = "connecting";
  private healthyAt = 0;

  constructor(private readonly options: LiveEventClientOptions) {
    this.scheduler = new LiveInvalidationScheduler(options.queryClient);
    this.resumeCursor = localStorage.getItem(this.cursorKey()) ?? "";
  }

  start(): void {
    this.stopped = false;
    window.addEventListener("online", this.onOnline);
    window.addEventListener("offline", this.onOffline);
    document.addEventListener("visibilitychange", this.onVisibility);
    this.connect();
  }

  stop(): void {
    this.stopped = true; this.source?.close(); this.source = null;
    if (this.reconnectTimer) clearTimeout(this.reconnectTimer);
    if (this.heartbeatTimer) clearTimeout(this.heartbeatTimer);
    window.removeEventListener("online", this.onOnline); window.removeEventListener("offline", this.onOffline);
    document.removeEventListener("visibilitychange", this.onVisibility);
  }

  private cursorKey = () => `143:live-cursor:${this.options.orgId}`;
  private setHealth = (health: LiveHealth) => {
    if (this.currentHealth === "healthy" && health !== "healthy" && this.healthyAt > 0) {
      emitLiveTelemetry("connection_duration", { duration_ms: Math.round(performance.now() - this.healthyAt) });
      this.healthyAt = 0;
    }
    if (health === "healthy" && this.currentHealth !== "healthy") this.healthyAt = performance.now();
    this.currentHealth = health;
    emitLiveTelemetry("connection_health", { health, attempt: this.attempt });
    this.options.onHealth?.(health);
  };
  private onOnline = () => { this.attempt = 0; if (this.pendingCheckpoint) void this.synchronizeCheckpoint(); else this.connect(); };
  private onOffline = () => {
    this.source?.close();
    if (this.reconnectTimer) { clearTimeout(this.reconnectTimer); this.reconnectTimer = null; }
    if (this.heartbeatTimer) { clearTimeout(this.heartbeatTimer); this.heartbeatTimer = null; }
    this.setHealth("offline");
  };
  private onVisibility = () => {
    if (document.visibilityState !== "visible") return;
    if (this.pendingCheckpoint) { void this.synchronizeCheckpoint(); return; }
    emitLiveTelemetry("visibility_catch_up");
    void this.scheduler.catchUpVisible();
  };

  private url(): string {
    const url = new URL(`${this.options.apiBase}/api/v1/events/stream`, window.location.origin);
    url.searchParams.set("org_id", this.options.orgId);
    if (this.resumeCursor) url.searchParams.set("last_event_id", this.resumeCursor);
    return url.toString();
  }

  private connect(): void {
    if (this.stopped || !navigator.onLine) return;
    if (typeof EventSource === "undefined") {
      this.setHealth("degraded");
      return;
    }
    if (this.heartbeatTimer) { clearTimeout(this.heartbeatTimer); this.heartbeatTimer = null; }
    this.source?.close(); this.setHealth("connecting");
    const source = new EventSource(this.url(), { withCredentials: true }); this.source = source;
    source.addEventListener("live.ready", (message) => void this.onReady(message as MessageEvent));
    source.addEventListener("live.event", (message) => void this.onEvent(message as MessageEvent));
    source.addEventListener("live.heartbeat", () => this.armHeartbeat());
    source.addEventListener("live.degraded", () => { this.setHealth("degraded"); source.close(); this.reconnect(); });
    source.addEventListener("live.resync", (message) => void this.onResync(message as MessageEvent));
    source.addEventListener("server.draining", (message) => { source.close(); this.reconnect(this.retryAfter(message as MessageEvent)); });
    source.onerror = () => {
      source.close();
      this.setHealth("degraded");
      if (this.resumeCursor && this.attempt >= 1) {
        this.resumeCursor = "";
        localStorage.removeItem(this.cursorKey());
      }
      this.reconnect();
    };
  }

  private async onReady(message: MessageEvent): Promise<void> {
    const ready = JSON.parse(message.data) as LiveReady;
    if (ready.schema_version !== LIVE_SCHEMA_VERSION) { this.source?.close(); this.reconnect(); return; }
    if (ready.initial_sync_required) {
      this.source?.close();
      this.pendingCheckpoint = { cursor: ready.through_stream_id, cause: "initial_sync", jitterApplied: false };
      this.setHealth("degraded");
      if (document.visibilityState === "visible") await this.synchronizeCheckpoint();
      return;
    }
    this.attempt = 0; this.setHealth("healthy"); this.armHeartbeat();
  }

  private async onEvent(message: MessageEvent): Promise<void> {
    const cursor = message.lastEventId;
    this.armHeartbeat();
    const startedAt = performance.now();
    const event = decodeLiveEvent(message.data);
    if (!event) {
      emitLiveTelemetry("projection_rejected", { reason: "invalid_event" });
      this.source?.close();
      this.pendingCheckpoint = { cursor, cause: "invalid_event", jitterApplied: false };
      this.setHealth("degraded");
      if (document.visibilityState === "visible") await this.synchronizeCheckpoint();
      return;
    }
    if (this.seen.has(event.event_id)) { this.acknowledge(cursor); return; }
    this.seen.add(event.event_id); if (this.seen.size > 2048) this.seen.clear();
    const projectionApplied = applyLiveProjection(this.options.queryClient, event);
    emitLiveTelemetry("event_processed", { type: event.type, lag_ms: Math.max(0, Date.now() - Date.parse(event.changed_at)), projection_applied: projectionApplied });
    if (projectionApplied) requestAnimationFrame(() => emitLiveTelemetry("projection_rendered", { type: event.type, duration_ms: Math.max(0, performance.now() - startedAt) }));
    const isCausationEcho = !!event.causation_id && recentClientMutations.has(event.causation_id);
    if (!(isCausationEcho && projectionApplied)) await this.scheduler.dirty(event);
    this.acknowledge(cursor);
    this.options.onEventProcessed?.(event, cursor);
  }

  private async onResync(message: MessageEvent): Promise<void> {
    const resync = JSON.parse(message.data) as LiveResync;
    this.source?.close(); this.setHealth("degraded");
    this.pendingCheckpoint = { cursor: resync.through_stream_id, cause: resync.cause, jitterApplied: false };
    if (document.visibilityState === "visible") await this.synchronizeCheckpoint();
  }

  private async synchronizeCheckpoint(): Promise<void> {
    const checkpoint = this.pendingCheckpoint;
    if (this.stopped || !checkpoint || document.visibilityState !== "visible" || !navigator.onLine) return;
    try {
      if (!checkpoint.jitterApplied) {
        checkpoint.jitterApplied = true;
        const maximum = checkpoint.cause === "client_mailbox_overflow" ? 250 : checkpoint.cause === "initial_sync" ? 0 : 1_000;
        if (maximum > 0) await new Promise((resolve) => setTimeout(resolve, Math.floor(Math.random() * (maximum + 1))));
      }
      await this.scheduler.catchUpVisible(true);
      this.acknowledge(checkpoint.cursor);
      this.pendingCheckpoint = null;
      emitLiveTelemetry(checkpoint.cause === "initial_sync" ? "initial_sync_completed" : "resync_completed", { cause: checkpoint.cause });
      this.attempt = 0;
      this.connect();
    } catch {
      emitLiveTelemetry(checkpoint.cause === "initial_sync" ? "initial_sync_failed" : "resync_failed", { cause: checkpoint.cause });
      if (this.stopped) return;
      const cap = Math.min(15_000, 1_000 * 2 ** Math.min(this.attempt++, 4));
      if (!this.reconnectTimer) {
        this.reconnectTimer = setTimeout(() => { this.reconnectTimer = null; void this.synchronizeCheckpoint(); }, Math.floor(Math.random() * cap));
      }
    }
  }

  private acknowledge(cursor: string): void {
    if (!cursor || (this.resumeCursor && compareStreamIDs(cursor, this.resumeCursor) <= 0)) return;
    this.resumeCursor = cursor;
    localStorage.setItem(this.cursorKey(), cursor);
  }
  private armHeartbeat(): void { if (this.heartbeatTimer) clearTimeout(this.heartbeatTimer); this.heartbeatTimer = setTimeout(() => { this.source?.close(); this.setHealth("degraded"); this.reconnect(); }, LIVE_HEARTBEAT_TIMEOUT_MS); }
  private retryAfter(message: MessageEvent): number { try { return (JSON.parse(message.data) as { retry_after_ms?: number }).retry_after_ms ?? 0; } catch { return 0; } }
  private reconnect(explicitDelay?: number): void {
    if (this.stopped || this.reconnectTimer) return;
    const attempt = this.attempt++;
    const cap = Math.min(15_000, 1_000 * 2 ** Math.min(attempt, 4));
    const hiddenFallback = typeof BroadcastChannel === "undefined" && document.visibilityState === "hidden";
    const delay = explicitDelay ?? (hiddenFallback
      ? 30_000 + Math.floor(Math.random() * 90_001)
      : attempt < 5
      ? Math.floor(Math.random() * cap)
      : 30_000 + Math.floor(Math.random() * 90_001));
    this.reconnectTimer = setTimeout(() => { this.reconnectTimer = null; this.connect(); }, delay);
  }
}

function compareStreamIDs(left: string, right: string): number {
  const parse = (value: string): [bigint, bigint] | null => {
    const match = /^(\d+)-(\d+)$/.exec(value);
    return match ? [BigInt(match[1]), BigInt(match[2])] : null;
  };
  const a = parse(left); const b = parse(right);
  if (!a || !b) return left.localeCompare(right);
  if (a[0] !== b[0]) return a[0] < b[0] ? -1 : 1;
  return a[1] === b[1] ? 0 : a[1] < b[1] ? -1 : 1;
}
