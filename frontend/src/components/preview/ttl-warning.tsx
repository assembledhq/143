"use client";

import { useEffect, useRef, useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Clock, Plus } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { api } from "@/lib/api";

interface TTLWarningProps {
  expiresAt: string;
  sessionId: string;
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

export function TTLWarning({ expiresAt, sessionId }: TTLWarningProps) {
  const queryClient = useQueryClient();
  const [remaining, setRemaining] = useState(() =>
    formatRemainingTime(expiresAt)
  );
  const [extendError, setExtendError] = useState<string | null>(null);
  const expiresAtRef = useRef(expiresAt);

  useEffect(() => {
    expiresAtRef.current = expiresAt;
  }, [expiresAt]);

  // Update remaining time every second, but only when the warning is visible
  // (urgent). Clear the interval when not urgent to avoid resource leaks.
  useEffect(() => {
    // Check immediately in case urgency changed
    const current = formatRemainingTime(expiresAtRef.current);
    setRemaining(current);

    if (!current.urgent) return;

    const interval = setInterval(() => {
      const next = formatRemainingTime(expiresAtRef.current);
      setRemaining(next);
      if (!next.urgent) clearInterval(interval);
    }, 1000);

    return () => clearInterval(interval);
  }, [expiresAt]);

  const extendMutation = useMutation({
    mutationFn: () => api.sessions.preview.extend(sessionId),
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

  // Only show when urgent or already expired
  if (!remaining.urgent) return null;

  return (
    <div
      className={cn(
        "flex items-center gap-1.5",
        remaining.expired && "animate-pulse"
      )}
    >
      <Badge
        variant="secondary"
        className={cn(
          "text-xs gap-1",
          remaining.expired
            ? "bg-destructive/15 text-destructive border-destructive/20"
            : "bg-amber-500/15 text-amber-600 dark:text-amber-400 border-amber-500/20"
        )}
      >
        <Clock className="size-3" />
        {remaining.expired ? "Preview expired" : `Expires in ${remaining.text}`}
      </Badge>
      {!remaining.expired && (
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
        <span className="text-xs text-destructive">{extendError}</span>
      )}
    </div>
  );
}
