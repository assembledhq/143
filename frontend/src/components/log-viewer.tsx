"use client";

import { useEffect, useRef, useState, useCallback } from "react";
import { AlertTriangle, RefreshCw } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { api } from "@/lib/api";
import type { SessionLog } from "@/lib/types";

const levelColors: Record<string, string> = {
  info: "bg-gray-100 text-gray-800",
  error: "bg-red-100 text-red-800",
  warn: "bg-yellow-100 text-yellow-800",
  tool_use: "bg-blue-100 text-blue-800",
  question: "bg-yellow-100 text-yellow-800",
  debug: "bg-gray-100 text-gray-600",
};

function formatTimestamp(dateStr: string): string {
  const date = new Date(dateStr);
  return date.toLocaleTimeString("en-US", {
    hour12: false,
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  });
}

interface LogViewerProps {
  runId: string;
  isActive: boolean;
}

const MAX_RECONNECT_ATTEMPTS = 5;
const BASE_RECONNECT_DELAY_MS = 1000;

export function LogViewer({ runId, isActive }: LogViewerProps) {
  const [logs, setLogs] = useState<SessionLog[]>([]);
  const [loading, setLoading] = useState(true);
  const [streaming, setStreaming] = useState(false);
  const [fetchError, setFetchError] = useState<string | null>(null);
  const [streamError, setStreamError] = useState<string | null>(null);
  const scrollRef = useRef<HTMLDivElement>(null);
  const apiBase = process.env.NEXT_PUBLIC_API_URL || "";
  const reconnectAttempts = useRef(0);
  const reconnectTimer = useRef<ReturnType<typeof setTimeout>>(null);
  const seenIds = useRef<Set<number>>(new Set());

  // Merge new logs into state, deduplicating by ID.
  const mergeLogs = useCallback((newLogs: SessionLog[]) => {
    setLogs((prev) => {
      const toAdd: SessionLog[] = [];
      for (const log of newLogs) {
        if (!seenIds.current.has(log.id)) {
          seenIds.current.add(log.id);
          toAdd.push(log);
        }
      }
      if (toAdd.length === 0) return prev;
      return [...prev, ...toAdd];
    });
  }, []);

  const fetchLogs = useCallback(async (signal?: AbortSignal) => {
    setLoading(true);
    setFetchError(null);
    try {
      const response = await api.sessions.getLogs(runId);
      if (!signal?.aborted) {
        const fetched = response.data || [];
        seenIds.current = new Set(fetched.map((l) => l.id));
        setLogs(fetched);
      }
    } catch (err) {
      if (!signal?.aborted) {
        setFetchError(
          err instanceof Error ? err.message : "Failed to load logs"
        );
      }
    } finally {
      if (!signal?.aborted) {
        setLoading(false);
      }
    }
  }, [runId]);

  // Fetch logs via REST API on mount (works for both active and completed runs).
  useEffect(() => {
    const controller = new AbortController();
    fetchLogs(controller.signal);
    return () => {
      controller.abort();
    };
  }, [fetchLogs]);

  // Start SSE streaming only for active runs.
  useEffect(() => {
    if (!isActive) {
      setStreaming(false);
      return;
    }

    let eventSource: EventSource | null = null;

    function connect() {
      eventSource = new EventSource(
        `${apiBase}/api/v1/sessions/${runId}/logs/stream`,
        { withCredentials: true }
      );

      eventSource.onopen = () => {
        setStreaming(true);
        setStreamError(null);
        reconnectAttempts.current = 0;
      };

      eventSource.onmessage = (event) => {
        try {
          const log: SessionLog = JSON.parse(event.data);
          mergeLogs([log]);
        } catch {
          // ignore unparseable messages
        }
      };

      // Listen for the "done" event sent when the run reaches terminal status.
      eventSource.addEventListener("done", () => {
        setStreaming(false);
        eventSource?.close();
      });

      eventSource.onerror = () => {
        setStreaming(false);
        eventSource?.close();

        if (reconnectAttempts.current < MAX_RECONNECT_ATTEMPTS) {
          const delay =
            BASE_RECONNECT_DELAY_MS *
            Math.pow(2, reconnectAttempts.current);
          reconnectAttempts.current += 1;
          reconnectTimer.current = setTimeout(connect, delay);
        } else {
          setStreamError("Log stream disconnected. Retries exhausted.");
        }
      };
    }

    connect();

    return () => {
      eventSource?.close();
      if (reconnectTimer.current) {
        clearTimeout(reconnectTimer.current);
      }
    };
  }, [runId, apiBase, isActive, mergeLogs]);

  useEffect(() => {
    if (scrollRef.current) {
      scrollRef.current.scrollTop = scrollRef.current.scrollHeight;
    }
  }, [logs]);

  if (loading) {
    return (
      <div className="text-center py-8 text-sm text-muted-foreground">
        Loading logs...
      </div>
    );
  }

  if (fetchError && logs.length === 0) {
    return (
      <div className="flex flex-col items-center gap-3 py-8 text-sm text-muted-foreground">
        <AlertTriangle className="h-8 w-8 text-muted-foreground/50" />
        <p>{fetchError}</p>
        <Button
          variant="outline"
          size="sm"
          onClick={() => fetchLogs()}
        >
          <RefreshCw className="mr-1.5 h-3 w-3" />
          Retry
        </Button>
      </div>
    );
  }

  return (
    <div className="space-y-2">
      {isActive && streaming && (
        <div className="flex items-center gap-2 text-xs text-muted-foreground">
          <span className="relative flex h-2 w-2">
            <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-green-400 opacity-75" />
            <span className="relative inline-flex rounded-full h-2 w-2 bg-green-500" />
          </span>
          Streaming...
        </div>
      )}
      {streamError && (
        <div className="flex items-center gap-2 text-xs text-destructive">
          <AlertTriangle className="h-3 w-3 shrink-0" />
          {streamError}
        </div>
      )}
      <div
        ref={scrollRef}
        className="h-[500px] overflow-y-auto rounded-md border border-border bg-muted/30 p-3 font-mono text-xs"
      >
        {logs.length === 0 ? (
          <div className="text-center py-8 text-muted-foreground">
            No log entries yet.
          </div>
        ) : (
          <div className="space-y-1">
            {logs.map((log) => (
              <div key={log.id} className="flex items-start gap-2">
                <span className="shrink-0 text-muted-foreground w-[60px]">
                  {formatTimestamp(log.created_at)}
                </span>
                <Badge
                  variant="secondary"
                  className={`shrink-0 text-[10px] px-1.5 py-0 ${levelColors[log.level] || levelColors.info}`}
                >
                  {log.level}
                </Badge>
                <span className="break-all">{log.message}</span>
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  );
}
