import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/lib/active-org", () => ({
  ACTIVE_ORG_CHANGED_EVENT: "active-org-changed",
  getActiveOrgId: () => "11111111-1111-1111-1111-111111111111",
}));
vi.mock("@/lib/api", () => ({ api: { liveEvents: { telemetry: vi.fn().mockResolvedValue(undefined) } } }));

import { LiveEventProvider } from "./live-event-provider";

class TestEventSource {
  static instances: TestEventSource[] = [];
  onerror: (() => void) | null = null;
  constructor(public url: string) { TestEventSource.instances.push(this); }
  addEventListener() {}
  close() {}
}

class TestBroadcastChannel {
  static channels = new Map<string, Set<TestBroadcastChannel>>();
  onmessage: ((event: MessageEvent) => void) | null = null;
  constructor(private readonly name: string) {
    const channels = TestBroadcastChannel.channels.get(name) ?? new Set();
    channels.add(this); TestBroadcastChannel.channels.set(name, channels);
  }
  postMessage(data: unknown) {
    for (const channel of TestBroadcastChannel.channels.get(this.name) ?? []) {
      if (channel !== this) channel.onmessage?.(new MessageEvent("message", { data }));
    }
  }
  close() { TestBroadcastChannel.channels.get(this.name)?.delete(this); }
}

function provider() {
  const queryClient = new QueryClient();
  return render(<QueryClientProvider client={queryClient}><LiveEventProvider><div>child</div></LiveEventProvider></QueryClientProvider>);
}

describe("LiveEventProvider connection sharing", () => {
  beforeEach(() => {
    vi.useFakeTimers(); localStorage.clear(); TestEventSource.instances = []; TestBroadcastChannel.channels.clear();
    vi.stubGlobal("EventSource", TestEventSource); vi.stubGlobal("BroadcastChannel", TestBroadcastChannel);
    Object.defineProperty(navigator, "locks", { configurable: true, value: undefined });
    Object.defineProperty(navigator, "onLine", { configurable: true, value: true });
  });
  afterEach(() => { vi.useRealTimers(); vi.unstubAllGlobals(); });

  it("uses one EventSource and fails leadership over through the BroadcastChannel lease", async () => {
    const first = provider(); const second = provider();
    await vi.advanceTimersByTimeAsync(0);
    expect(TestEventSource.instances).toHaveLength(1);
    first.unmount();
    await vi.advanceTimersByTimeAsync(2_100);
    expect(TestEventSource.instances).toHaveLength(2);
    second.unmount();
  });
});
