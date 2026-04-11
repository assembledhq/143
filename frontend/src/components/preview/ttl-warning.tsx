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

  if (minutes >= 60) {
    const hours = Math.floor(minutes / 60);
    const mins = minutes % 60;
    return {
      text: `${hours}h ${mins}m`,
      urgent: false,
      expired: false,
    };
  }

  if (minutes > 5) {
    return {
      text: `${minutes}m`,
      urgent: false,
      expired: false,
    };
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
  const expiresAtRef = useRef(expiresAt);

  useEffect(() => {
    expiresAtRef.current = expiresAt;
  }, [expiresAt]);

  // Update remaining time every second
  useEffect(() => {
    const interval = setInterval(() => {
      setRemaining(formatRemainingTime(expiresAtRef.current));
    }, 1000);

    return () => clearInterval(interval);
  }, []);

  const extendMutation = useMutation({
    mutationFn: () => api.sessions.preview.extend(sessionId),
    onSuccess: () => {
      queryClient.invalidateQueries({
        queryKey: ["preview-status", sessionId],
      });
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
          remaining.urgent && !remaining.expired
            ? "bg-amber-500/15 text-amber-600 dark:text-amber-400 border-amber-500/20"
            : remaining.expired
              ? "bg-destructive/15 text-destructive border-destructive/20"
              : ""
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
    </div>
  );
}
