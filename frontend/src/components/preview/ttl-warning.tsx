"use client";

import { useEffect, useRef, useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Clock, Plus, RefreshCw } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { ErrorText } from "@/components/ui/error-notice";
import { cn } from "@/lib/utils";
import { api } from "@/lib/api";

interface TTLWarningProps {
  expiresAt: string;
  sessionId: string;
  // When set, the preview has been flagged for imminent recycle. The banner
  // displays a countdown so the user can save state before the restart.
  recycleScheduledAt?: string | null;
}

function formatRemainingTime(expiresAt: string): {
  text: string;
  urgent: boolean;
  expired: boolean;
} {
  const now = Date.now();
  const expires = new Date(expiresAt).getTime();
  const remainingMs = expires - now;

  if (remainingMs <= 0) {
    return { text: "Expired", urgent: true, expired: true };
  }

  const minutes = Math.floor(remainingMs / 60000);
  const seconds = Math.floor((remainingMs % 60000) / 1000);

  // Only show when <= 5 minutes remaining (urgent)
  if (minutes > 5) {
    return { text: "", urgent: false, expired: false };
  }

  return {
    text: minutes > 0 ? `${minutes}m ${seconds}s` : `${seconds}s`,
    urgent: true,
    expired: false,
  };
}

function formatRecycleCountdown(recycleAt: string): {
  text: string;
  visible: boolean;
} {
  const remainingMs = new Date(recycleAt).getTime() - Date.now();
  if (remainingMs <= 0) return { text: "Restarting now", visible: true };
  const seconds = Math.ceil(remainingMs / 1000);
  if (seconds > 120) return { text: "", visible: false };
  if (seconds >= 60) {
    const minutes = Math.floor(seconds / 60);
    return { text: `${minutes}m ${seconds % 60}s`, visible: true };
  }
  return { text: `${seconds}s`, visible: true };
}

export function TTLWarning({
  expiresAt,
  sessionId,
  recycleScheduledAt,
}: TTLWarningProps) {
  const queryClient = useQueryClient();
  const [remaining, setRemaining] = useState(() =>
    formatRemainingTime(expiresAt)
  );
  const [recycleCountdown, setRecycleCountdown] = useState(() =>
    recycleScheduledAt
      ? formatRecycleCountdown(recycleScheduledAt)
      : { text: "", visible: false }
  );
  const [extendError, setExtendError] = useState<string | null>(null);
  const expiresAtRef = useRef(expiresAt);
  const recycleAtRef = useRef(recycleScheduledAt ?? null);

  useEffect(() => {
    expiresAtRef.current = expiresAt;
  }, [expiresAt]);

  useEffect(() => {
    recycleAtRef.current = recycleScheduledAt ?? null;
  }, [recycleScheduledAt]);

  // Update remaining time every second, but only when the warning is visible
  // (urgent TTL or recycle pending). Clear the interval when nothing needs
  // updating to avoid resource leaks.
  useEffect(() => {
    // Check immediately in case urgency changed
    const current = formatRemainingTime(expiresAtRef.current);
    setRemaining(current);
    const currentRecycle = recycleAtRef.current
      ? formatRecycleCountdown(recycleAtRef.current)
      : { text: "", visible: false };
    setRecycleCountdown(currentRecycle);

    if (!current.urgent && !currentRecycle.visible) return;

    const interval = setInterval(() => {
      const next = formatRemainingTime(expiresAtRef.current);
      setRemaining(next);
      const nextRecycle = recycleAtRef.current
        ? formatRecycleCountdown(recycleAtRef.current)
        : { text: "", visible: false };
      setRecycleCountdown(nextRecycle);
      if (!next.urgent && !nextRecycle.visible) clearInterval(interval);
    }, 1000);

    return () => clearInterval(interval);
  }, [expiresAt, recycleScheduledAt]);

  const extendMutation = useMutation({
    mutationFn: () => api.sessions.preview.setLifetime(sessionId, { duration_seconds: 30 * 60 }),
    onSuccess: () => {
      setExtendError(null);
      queryClient.invalidateQueries({
        queryKey: ["preview-status", sessionId],
      });
    },
    onError: (error) => {
      setExtendError(`Failed to extend: ${error.message}`);
    },
  });

  // Nothing to render when both the TTL is fine and no recycle is pending.
  if (!remaining.urgent && !recycleCountdown.visible) return null;

  return (
    <div
      className={cn(
        "flex items-center gap-1.5",
        remaining.expired && "animate-pulse"
      )}
    >
      {recycleCountdown.visible && (
        <Badge
          variant="secondary"
          className="text-xs gap-1 bg-warning/15 text-warning border-warning/20"
          data-testid="recycle-warning"
        >
          <RefreshCw className="size-3" />
          Restarting in {recycleCountdown.text}
        </Badge>
      )}
      {remaining.urgent && (
        <Badge
          variant="secondary"
          className={cn(
            "text-xs gap-1",
            remaining.expired
              ? "bg-destructive/15 text-destructive border-destructive/20"
              : "bg-warning/15 text-warning border-warning/20"
          )}
        >
          <Clock className="size-3" />
          {remaining.expired
            ? "Preview expired"
            : `Expires in ${remaining.text}`}
        </Badge>
      )}
      {remaining.urgent && !remaining.expired && (
        <Button
          size="xs"
          variant="outline"
          onClick={() => extendMutation.mutate()}
          disabled={extendMutation.isPending}
          loading={extendMutation.isPending}
        >
          <Plus className="size-3" />
          Extend
        </Button>
      )}
      {extendError && (
        <ErrorText>{extendError}</ErrorText>
      )}
    </div>
  );
}
