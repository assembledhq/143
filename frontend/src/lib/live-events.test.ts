import { QueryClient, QueryObserver } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { applyLiveProjection, decodeLiveEvent, LiveEventClient, LiveInvalidationScheduler, registerLiveQuery, type LiveEvent } from "./live-events";

const orgId = "11111111-1111-1111-1111-111111111111";
const resourceId = "22222222-2222-2222-2222-222222222222";

function event(overrides: Partial<LiveEvent> = {}): LiveEvent {
  return { schema_version:1, event_id:crypto.randomUUID(), type:"session.updated", scope:"resource", org_id:orgId, resource_type:"session", resource_id:resourceId, audience:"org", version:2, changed_at:new Date().toISOString(), payload:{status_projection:{status:"running"},list_affected:true}, ...overrides };
}

class MockEventSource {
  static instances: MockEventSource[] = [];
  listeners = new Map<string, Array<(event: MessageEvent) => void>>();
  onerror: (() => void) | null = null;
  closed = false;
  constructor(public url: string, public init?: EventSourceInit) { MockEventSource.instances.push(this); }
  addEventListener(name: string, listener: EventListenerOrEventListenerObject) { const fn = listener as (event:MessageEvent)=>void; this.listeners.set(name, [...(this.listeners.get(name) ?? []), fn]); }
  emit(name: string, data: unknown, lastEventId = "") { for (const listener of this.listeners.get(name) ?? []) listener(new MessageEvent(name,{data:JSON.stringify(data),lastEventId})); }
  close() { this.closed = true; }
}

describe("live event decoding and projections", () => {
  it("rejects unknown schemas and malformed payloads", () => {
    expect(decodeLiveEvent(JSON.stringify(event()))?.type).toBe("session.updated");
    expect(decodeLiveEvent(JSON.stringify(event({schema_version:2})))).toBeNull();
    expect(decodeLiveEvent("not-json")).toBeNull();
    expect(decodeLiveEvent(JSON.stringify(event({ scope: "collection" })))).toBeNull();
    expect(decodeLiveEvent(JSON.stringify(event({ version: undefined })))).toBeNull();
    expect(decodeLiveEvent(JSON.stringify(event({ resource_type: "preview" })))).toBeNull();
    expect(decodeLiveEvent(JSON.stringify(event({ payload: { status_projection: { made_up: "value" } } })))).toBeNull();
  });

  it("applies only newer projections across detail and list caches", () => {
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    queryClient.setQueryData(["session",resourceId], {data:{id:resourceId,status:"pending",live_version:1}});
    queryClient.setQueryData(["sessions"], {data:[{id:resourceId,status:"pending",live_version:1}]});
    expect(applyLiveProjection(queryClient,event())).toBe(true);
    expect(queryClient.getQueryData<{data:{status:string}}>(["session",resourceId])?.data.status).toBe("running");
    expect(applyLiveProjection(queryClient,event({version:1,payload:{status_projection:{status:"failed"}}}))).toBe(false);
    expect(queryClient.getQueryData<{data:{status:string}}>(["session",resourceId])?.data.status).toBe("running");
  });

  it("does not let an older REST success roll projected detail or list rows backward", () => {
    const queryClient = new QueryClient();
    queryClient.setQueryData(["session", resourceId], { data: { id: resourceId, status: "pending", live_version: 1, title: "Canonical title" } });
    queryClient.setQueryData(["sessions"], { data: [{ id: resourceId, status: "pending", live_version: 1 }] });
    applyLiveProjection(queryClient, event({ version: 4, payload: { status_projection: { status: "completed" } } }));
    queryClient.setQueryData(["session", resourceId], { data: { id: resourceId, status: "running", live_version: 2, title: "New title" } });
    queryClient.setQueryData(["sessions"], { data: [{ id: resourceId, status: "running", live_version: 2 }] });
    expect(queryClient.getQueryData<{ data: { status: string; title: string; live_version: number } }>(["session", resourceId])?.data).toEqual({ id: resourceId, status: "completed", live_version: 4, title: "New title" });
    expect(queryClient.getQueryData<{ data: Array<{ status: string; live_version: number }> }>(["sessions"])?.data[0]).toMatchObject({ status: "completed", live_version: 4 });
  });
});

describe("LiveInvalidationScheduler", () => {
  it("suppresses hidden event-triggered reads and catches up once visible", async () => {
    const queryClient = new QueryClient(); const queryFn = vi.fn().mockResolvedValue({data:[]});
    await queryClient.fetchQuery({queryKey:["sessions"],queryFn}); queryFn.mockClear();
    const observer = new QueryObserver(queryClient, { queryKey: ["sessions"], queryFn, staleTime: Infinity });
    const unsubscribeObserver = observer.subscribe(() => undefined);
    const unregister = registerLiveQuery(queryClient, { queryKey: ["sessions"], families: ["session.list"], priority: "critical", visible: true });
    const scheduler = new LiveInvalidationScheduler(queryClient);
    Object.defineProperty(document,"visibilityState",{configurable:true,value:"hidden"});
    await scheduler.dirty(event()); expect(queryFn).not.toHaveBeenCalled();
    Object.defineProperty(document,"visibilityState",{configurable:true,value:"visible"});
    await scheduler.catchUpVisible(); expect(queryFn).toHaveBeenCalledTimes(1);
    unregister(); unsubscribeObserver();
  });

  it("recovers scheduler state after a failed canonical refetch", async () => {
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const queryFn = vi.fn().mockResolvedValueOnce({ data: [] }).mockRejectedValueOnce(new Error("temporary")).mockResolvedValue({ data: [] });
    await queryClient.fetchQuery({ queryKey: ["sessions"], queryFn });
    const observer = new QueryObserver(queryClient, { queryKey: ["sessions"], queryFn, staleTime: Infinity });
    const unsubscribeObserver = observer.subscribe(() => undefined);
    const unregister = registerLiveQuery(queryClient, { queryKey: ["sessions"], families: ["session.list"], priority: "critical", visible: true });
    const scheduler = new LiveInvalidationScheduler(queryClient);
    await expect(scheduler.dirty(event())).rejects.toThrow("temporary");
    await scheduler.catchUpVisible(true);
    expect(queryFn).toHaveBeenCalledTimes(3);
    unregister(); unsubscribeObserver();
  });

  it("scopes automation-run events to the owning automation's runs list", async () => {
    const automationId = "33333333-3333-3333-3333-333333333333";
    Object.defineProperty(document, "visibilityState", { configurable: true, value: "visible" });
    const queryClient = new QueryClient(); const queryFn = vi.fn().mockResolvedValue({ data: [] });
    await queryClient.fetchQuery({ queryKey: ["automation-runs", automationId], queryFn }); queryFn.mockClear();
    const observer = new QueryObserver(queryClient, { queryKey: ["automation-runs", automationId], queryFn, staleTime: Infinity });
    const unsubscribeObserver = observer.subscribe(() => undefined);
    const unregister = registerLiveQuery(queryClient, { queryKey: ["automation-runs", automationId], families: ["automation.runs"], resourceId: automationId, priority: "critical", visible: true });
    const scheduler = new LiveInvalidationScheduler(queryClient);
    const runEvent = (parentId: string) => event({ type: "automation.run.updated", resource_type: "automation_run", resource_id: crypto.randomUUID(), parent_id: parentId, version: 1, payload: { status_projection: { status: "running" }, list_affected: true } });
    // A run under a different automation must not refetch this scoped list.
    await scheduler.dirty(runEvent("44444444-4444-4444-4444-444444444444"));
    expect(queryFn).not.toHaveBeenCalled();
    // A run under this automation (event.parent_id === registration.resourceId) must refetch.
    await scheduler.dirty(runEvent(automationId));
    expect(queryFn).toHaveBeenCalledTimes(1);
    unregister(); unsubscribeObserver();
  });

  it("routes pull request events to registered health queries", async () => {
    const queryClient = new QueryClient(); const queryFn = vi.fn().mockResolvedValue({ data: { id: resourceId } });
    await queryClient.fetchQuery({ queryKey: ["pull-request", resourceId, "health"], queryFn }); queryFn.mockClear();
    const observer = new QueryObserver(queryClient, { queryKey: ["pull-request", resourceId, "health"], queryFn, staleTime: Infinity });
    const unsubscribeObserver = observer.subscribe(() => undefined);
    const unregister = registerLiveQuery(queryClient, { queryKey: ["pull-request", resourceId, "health"], families: ["pull-request.detail"], resourceId, priority: "critical", visible: true });
    const scheduler = new LiveInvalidationScheduler(queryClient);
    await scheduler.dirty(event({ type: "pull_request.updated", resource_type: "pull_request", version: undefined, payload: { list_affected: true } }));
    expect(queryFn).toHaveBeenCalledTimes(1);
    unregister(); unsubscribeObserver();
  });

  it("cancels a stale list request once so a creation event cannot remove optimistic membership", async () => {
    const queryClient = new QueryClient();
    const optimistic = { data: [{ id: "new-session", live_version: 1 }] };
    queryClient.setQueryData(["sessions"], optimistic);
    let resolveRequest!: (value: { data: never[] }) => void;
    let calls = 0;
    const queryFn = vi.fn(({ signal }: { signal: AbortSignal }) => {
      calls++;
      if (calls > 1) return Promise.resolve(optimistic);
      return new Promise<{ data: never[] }>((resolve, reject) => {
        resolveRequest = resolve;
        signal.addEventListener("abort", () => reject(new DOMException("cancelled", "AbortError")), { once: true });
      });
    });
    const observer = new QueryObserver(queryClient, { queryKey: ["sessions"], queryFn, staleTime: Infinity });
    const unsubscribeObserver = observer.subscribe(() => undefined);
    const unregister = registerLiveQuery(queryClient, { queryKey: ["sessions"], families: ["session.list"], priority: "critical", visible: true });
    const request = observer.refetch();
    await Promise.resolve();
    const scheduler = new LiveInvalidationScheduler(queryClient);
    const dirty = scheduler.dirty(event({ type: "session.created", scope: "collection", resource_id: undefined, version: undefined, payload: { list_affected: true } }));
    resolveRequest({ data: [] });
    await request; await dirty;
    expect(queryClient.getQueryData(["sessions"])).toEqual(optimistic);
    unregister(); unsubscribeObserver();
  });
});

describe("LiveEventClient cursors and reconnect", () => {
  beforeEach(() => { vi.useFakeTimers(); MockEventSource.instances=[]; vi.stubGlobal("EventSource",MockEventSource); Object.defineProperty(navigator,"onLine",{configurable:true,value:true}); localStorage.clear(); });
  afterEach(() => { vi.useRealTimers(); vi.unstubAllGlobals(); });

  it("acknowledges processed event IDs and reconstructs the application cursor", async () => {
    const client = new LiveEventClient({apiBase:"https://api.example.test",orgId,queryClient:new QueryClient()}); client.start();
    const first = MockEventSource.instances[0]; expect(first.init?.withCredentials).toBe(true); expect(first.url).not.toContain("last_event_id");
    first.emit("live.event",event(),"123-0"); await Promise.resolve(); await Promise.resolve();
    expect(localStorage.getItem(`143:live-cursor:${orgId}`)).toBe("123-0");
    first.onerror?.(); await vi.runOnlyPendingTimersAsync();
    expect(MockEventSource.instances.at(-1)?.url).toContain("last_event_id=123-0"); client.stop();
  });

  it("preserves the acknowledged resume cursor across repeated transport failures", async () => {
    localStorage.setItem(`143:live-cursor:${orgId}`, "123-0");
    const randomSpy = vi.spyOn(Math, "random").mockReturnValue(0);
    try {
      const client = new LiveEventClient({ apiBase: "", orgId, queryClient: new QueryClient() });
      client.start();
      expect(MockEventSource.instances[0].url).toContain("last_event_id=123-0");

      MockEventSource.instances[0].onerror?.();
      await vi.runOnlyPendingTimersAsync();
      MockEventSource.instances.at(-1)?.onerror?.();
      await vi.runOnlyPendingTimersAsync();

      expect(localStorage.getItem(`143:live-cursor:${orgId}`)).toBe("123-0");
      expect(MockEventSource.instances.at(-1)?.url).toContain("last_event_id=123-0");
      client.stop();
    } finally {
      randomSpy.mockRestore();
    }
  });

  it("does not advance a resync checkpoint until canonical synchronization succeeds", async () => {
    const queryClient = new QueryClient();
    const queryFn = vi.fn().mockResolvedValueOnce({ data: [] }).mockRejectedValueOnce(new Error("sync failed")).mockResolvedValue({ data: [] });
    await queryClient.fetchQuery({ queryKey: ["sessions"], queryFn });
    const observer = new QueryObserver(queryClient, { queryKey: ["sessions"], queryFn, staleTime: Infinity });
    const unsubscribeObserver = observer.subscribe(() => undefined);
    const unregister = registerLiveQuery(queryClient, { queryKey: ["sessions"], families: ["session.list"], priority: "critical", visible: true });
    localStorage.setItem(`143:live-cursor:${orgId}`, "100-0");
    const client = new LiveEventClient({apiBase:"",orgId,queryClient}); client.start();
    MockEventSource.instances[0].emit("live.resync",{cause:"replay_window_missed",through_stream_id:"900-0"});
    await vi.runOnlyPendingTimersAsync();
    expect(localStorage.getItem(`143:live-cursor:${orgId}`)).toBe("100-0");
    await vi.runOnlyPendingTimersAsync();
    expect(localStorage.getItem(`143:live-cursor:${orgId}`)).toBe("900-0"); client.stop();
    unregister(); unsubscribeObserver();
  });

  it("retains the old checkpoint while hidden and synchronizes once visible", async () => {
    localStorage.setItem(`143:live-cursor:${orgId}`, "100-0");
    Object.defineProperty(document, "visibilityState", { configurable: true, value: "hidden" });
    const client = new LiveEventClient({ apiBase: "", orgId, queryClient: new QueryClient() });
    client.start();
    MockEventSource.instances[0].emit("live.resync", { cause: "replay_window_missed", through_stream_id: "900-0" });
    await Promise.resolve();
    expect(localStorage.getItem(`143:live-cursor:${orgId}`)).toBe("100-0");
    Object.defineProperty(document, "visibilityState", { configurable: true, value: "visible" });
    document.dispatchEvent(new Event("visibilitychange"));
    await vi.runOnlyPendingTimersAsync();
    expect(localStorage.getItem(`143:live-cursor:${orgId}`)).toBe("900-0");
    client.stop();
  });

  it("never moves a resume cursor backward during cursorless initial synchronization", async () => {
    const client = new LiveEventClient({ apiBase: "", orgId, queryClient: new QueryClient() }); client.start();
    const source = MockEventSource.instances[0];
    source.emit("live.event", event(), "101-0");
    await Promise.resolve(); await Promise.resolve();
    source.emit("live.ready", { schema_version: 1, initial_sync_required: true, through_stream_id: "100-0", bus_health_epoch: 1 });
    await Promise.resolve(); await Promise.resolve();
    expect(localStorage.getItem(`143:live-cursor:${orgId}`)).toBe("101-0");
    client.stop();
  });

  it("treats ordinary live events as liveness evidence", async () => {
    const client = new LiveEventClient({ apiBase: "", orgId, queryClient: new QueryClient() }); client.start();
    const source = MockEventSource.instances[0];
    source.emit("live.ready", { schema_version: 1, initial_sync_required: false, through_stream_id: "0-0", bus_health_epoch: 1 });
    await vi.advanceTimersByTimeAsync(40_000);
    source.emit("live.event", event(), "1-0");
    await vi.advanceTimersByTimeAsync(10_000);
    expect(MockEventSource.instances).toHaveLength(1);
    client.stop();
  });

  it("clears the previous heartbeat timer on reconnect so it cannot close the new stream", async () => {
    // Zero-jitter reconnect backoff so the new source is created deterministically
    // before the original 45s heartbeat deadline elapses.
    const randomSpy = vi.spyOn(Math, "random").mockReturnValue(0);
    try {
      const client = new LiveEventClient({ apiBase: "", orgId, queryClient: new QueryClient() }); client.start();
      const first = MockEventSource.instances[0];
      first.emit("live.ready", { schema_version: 1, initial_sync_required: false, through_stream_id: "0-0", bus_health_epoch: 1 });
      // Sit just shy of the 45s heartbeat deadline, then take a transient error that
      // reconnects while the original heartbeat timer is still pending.
      await vi.advanceTimersByTimeAsync(44_000);
      first.onerror?.();
      await vi.advanceTimersByTimeAsync(1); // fire the zero-delay reconnect -> new source
      const reconnected = MockEventSource.instances.at(-1)!;
      expect(reconnected).not.toBe(first);
      expect(MockEventSource.instances).toHaveLength(2);
      // Cross the original 45s deadline. The stale heartbeat must not fire against the
      // freshly connected source (which would close it and spawn a third instance).
      await vi.advanceTimersByTimeAsync(2_000);
      expect(reconnected.closed).toBe(false);
      expect(MockEventSource.instances).toHaveLength(2);
      client.stop();
    } finally {
      randomSpy.mockRestore();
    }
  });
});
