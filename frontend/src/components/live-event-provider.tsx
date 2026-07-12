"use client";

import { useQueryClient } from "@tanstack/react-query";
import { createContext, useContext, useEffect, useState } from "react";
import { ACTIVE_ORG_CHANGED_EVENT, getActiveOrgId } from "@/lib/active-org";
import { api } from "@/lib/api";
import { applyLiveProjection, LiveEventClient, LiveInvalidationScheduler, type LiveEvent, type LiveHealth } from "@/lib/live-events";

const LiveHealthContext = createContext<LiveHealth>("connecting");

export function useLiveHealth(): LiveHealth { return useContext(LiveHealthContext); }

export function LiveEventProvider({ children }: { children: React.ReactNode }) {
  const queryClient = useQueryClient();
  const [health, setHealth] = useState<LiveHealth>("connecting");

  useEffect(() => {
    let client: LiveEventClient | null = null;
    let channel: BroadcastChannel | null = null;
    let electionTimer: ReturnType<typeof setTimeout> | null = null;
    let releaseLeadership: (() => void) | null = null;
    let disposed = false;
    let followerNeedsSync = false;
    let followerScheduler: LiveInvalidationScheduler | null = null;
    let sustainedTimer: ReturnType<typeof setTimeout> | null = null;
    let leaseTimer: ReturnType<typeof setInterval> | null = null;
    const tabId = crypto.randomUUID();
    let currentHealth: LiveHealth = "connecting";

    const publishHealth = (next: LiveHealth) => {
      currentHealth = next;
      setHealth(next);
      channel?.postMessage({ kind: "health", health: next });
      if (sustainedTimer) { clearTimeout(sustainedTimer); sustainedTimer = null; }
      if (next === "degraded") {
        if (document.visibilityState === "visible") void followerScheduler?.catchUpVisible(true);
        else followerNeedsSync = true;
        sustainedTimer = setTimeout(() => {
          if (currentHealth === "degraded") publishHealth("degraded-sustained");
        }, 120_000);
      }
    };

    const onVisibility = () => {
      if (!followerNeedsSync || document.visibilityState !== "visible") return;
      followerNeedsSync = false;
      void followerScheduler?.catchUpVisible(true);
    };

    const stopTransport = () => {
      client?.stop(); client = null;
      releaseLeadership?.(); releaseLeadership = null;
      channel?.close(); channel = null;
      if (electionTimer) clearTimeout(electionTimer);
      electionTimer = null;
      if (leaseTimer) clearInterval(leaseTimer);
      leaseTimer = null;
      if (sustainedTimer) clearTimeout(sustainedTimer);
      sustainedTimer = null;
    };

    const connect = () => {
      stopTransport();
      const orgId = getActiveOrgId();
      if (!orgId) return;
      const scheduler = new LiveInvalidationScheduler(queryClient);
      followerScheduler = scheduler;
      const supportsSharing = typeof BroadcastChannel !== "undefined";

      if (!supportsSharing) {
        client = new LiveEventClient({ apiBase: process.env.NEXT_PUBLIC_API_URL ?? "", orgId, queryClient, onHealth: publishHealth });
        client.start();
        return;
      }

      channel = new BroadcastChannel(`143:live-events:${orgId}`);
      channel.onmessage = (message: MessageEvent<{ kind: "event"; event: LiveEvent } | { kind: "health"; health: LiveHealth } | { kind: "hello" }>) => {
        if (message.data.kind === "hello") {
          if (client) channel?.postMessage({ kind: "health", health: currentHealth });
          return;
        }
        if (message.data.kind === "health") {
          currentHealth = message.data.health;
          setHealth(message.data.health);
          if (message.data.health === "degraded") {
            if (document.visibilityState === "visible") void scheduler.catchUpVisible(true);
            else followerNeedsSync = true;
          }
          if (message.data.health === "healthy") {
            if (document.visibilityState === "visible") void scheduler.catchUpVisible(true);
            else followerNeedsSync = true;
          }
          return;
        }
        applyLiveProjection(queryClient, message.data.event);
        void scheduler.dirty(message.data.event);
      };
      channel.postMessage({ kind: "hello" });

      const startLeader = () => {
        if (disposed || client) return;
        window.dispatchEvent(new CustomEvent("143:live-telemetry", { detail: { name: "leader_state", state: "leader" } }));
        const leaderClient = new LiveEventClient({
          apiBase: process.env.NEXT_PUBLIC_API_URL ?? "", orgId, queryClient,
          onHealth: publishHealth,
          onEventProcessed: (event) => channel?.postMessage({ kind: "event", event }),
        });
        client = leaderClient;
        leaderClient.start();
      };

      const elect = () => {
        if (disposed || client) return;
        void navigator.locks.request(`143:live-events:${orgId}`, { ifAvailable: true }, async (lock) => {
          if (!lock || disposed || client) return;
          let release!: () => void;
          const held = new Promise<void>((resolve) => { release = resolve; });
          releaseLeadership = release;
          startLeader();
          const leaderClient = client!;
          await held;
          leaderClient.stop();
          if (client === leaderClient) client = null;
        });
        electionTimer = setTimeout(elect, 2_000 + Math.floor(Math.random() * 2_000));
      };
      if (typeof navigator.locks?.request === "function") {
        elect();
      } else {
        const leaseKey = `143:live-events-leader:${orgId}`;
        const tryLease = () => {
          const now = Date.now();
          let lease: { owner: string; expiresAt: number } | null = null;
          try { lease = JSON.parse(localStorage.getItem(leaseKey) ?? "null") as { owner: string; expiresAt: number } | null; } catch { lease = null; }
          if (!lease || lease.expiresAt <= now || lease.owner === tabId) {
            localStorage.setItem(leaseKey, JSON.stringify({ owner: tabId, expiresAt: now + 6_000 }));
            startLeader();
          } else if (client) {
            client.stop(); client = null;
          }
        };
        tryLease();
        leaseTimer = setInterval(tryLease, 2_000);
        releaseLeadership = () => {
          try {
            const lease = JSON.parse(localStorage.getItem(leaseKey) ?? "null") as { owner?: string } | null;
            if (lease?.owner === tabId) localStorage.removeItem(leaseKey);
          } catch { /* an invalid lease is already treated as expired */ }
        };
      }
    };
    connect();
    document.addEventListener("visibilitychange", onVisibility);
    window.addEventListener(ACTIVE_ORG_CHANGED_EVENT, connect);
    return () => { disposed = true; document.removeEventListener("visibilitychange", onVisibility); window.removeEventListener(ACTIVE_ORG_CHANGED_EVENT, connect); stopTransport(); };
  }, [queryClient]);

  useEffect(() => {
    let samples: Array<{ name: string; [key: string]: string | number | boolean }> = [];
    const flush = () => {
      if (samples.length === 0) return;
      const pending = samples; samples = [];
      void api.liveEvents.telemetry(pending).catch((error) => {
        console.error("Failed to record live-event telemetry.", error);
      });
    };
    const onTelemetry = (event: Event) => {
      const detail = (event as CustomEvent<Record<string, string | number | boolean>>).detail;
      if (!detail || typeof detail.name !== "string") return;
      samples.push(detail as { name: string; [key: string]: string | number | boolean });
      if (samples.length >= 50) flush();
    };
    window.addEventListener("143:live-telemetry", onTelemetry);
    const timer = window.setInterval(flush, 5_000);
    return () => { window.removeEventListener("143:live-telemetry", onTelemetry); window.clearInterval(timer); flush(); };
  }, []);

  return <LiveHealthContext.Provider value={health}>{children}</LiveHealthContext.Provider>;
}
