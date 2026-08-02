"use client";

import { WifiOff } from "lucide-react";
import { useEffect, useRef, useSyncExternalStore } from "react";
import { Card, CardContent } from "@/components/ui/card";
import { notify } from "@/lib/notify";

function subscribeToConnectivity(onStoreChange: () => void) {
  window.addEventListener("online", onStoreChange);
  window.addEventListener("offline", onStoreChange);

  return () => {
    window.removeEventListener("online", onStoreChange);
    window.removeEventListener("offline", onStoreChange);
  };
}

function getOnlineSnapshot() {
  return navigator.onLine;
}

function getServerOnlineSnapshot() {
  return true;
}

export function ConnectivityNotice() {
  const isOnline = useSyncExternalStore(
    subscribeToConnectivity,
    getOnlineSnapshot,
    getServerOnlineSnapshot,
  );
  const previousOnlineRef = useRef(true);

  useEffect(() => {
    if (isOnline && !previousOnlineRef.current) {
      notify.success("Back online", {
        description: "Live data and network actions are available again.",
      });
    }
    previousOnlineRef.current = isOnline;
  }, [isOnline]);

  if (isOnline) return null;

  return (
    <Card
      variant="elevated"
      role="status"
      aria-live="polite"
      className="pointer-events-none fixed inset-x-3 top-[calc(env(safe-area-inset-top)+4rem)] z-40 mx-auto max-w-md border-warning/35 bg-card md:top-4"
    >
      <CardContent className="flex items-start gap-3 px-3 py-2.5">
        <WifiOff className="mt-0.5 size-4 shrink-0 text-warning" aria-hidden="true" />
        <div className="min-w-0">
          <p className="text-sm font-medium text-foreground">You&apos;re offline</p>
          <p className="text-xs text-muted-foreground">
            You can keep reading this view. Some data and actions will be available after you reconnect.
          </p>
        </div>
      </CardContent>
    </Card>
  );
}
