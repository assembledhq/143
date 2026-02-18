"use client";

import { useEffect, useRef, useState } from "react";
import { Badge } from "@/components/ui/badge";
import type { AgentRunLog } from "@/lib/types";

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

export function LogViewer({ runId, isActive }: LogViewerProps) {
  const [logs, setLogs] = useState<AgentRunLog[]>([]);
  const [connected, setConnected] = useState(false);
  const scrollRef = useRef<HTMLDivElement>(null);
  const apiBase = process.env.NEXT_PUBLIC_API_URL || "";

  useEffect(() => {
    const eventSource = new EventSource(
      `${apiBase}/api/v1/runs/${runId}/logs`,
      { withCredentials: true }
    );

    eventSource.onopen = () => {
      setConnected(true);
    };

    eventSource.onmessage = (event) => {
      try {
        const log: AgentRunLog = JSON.parse(event.data);
        setLogs((prev) => [...prev, log]);
      } catch {
        // ignore unparseable messages
      }
    };

    eventSource.onerror = () => {
      setConnected(false);
      eventSource.close();
    };

    return () => {
      eventSource.close();
    };
  }, [runId, apiBase]);

  useEffect(() => {
    if (scrollRef.current) {
      scrollRef.current.scrollTop = scrollRef.current.scrollHeight;
    }
  }, [logs]);

  if (!connected && logs.length === 0) {
    return (
      <div className="text-center py-8 text-sm text-muted-foreground">
        Connecting to log stream...
      </div>
    );
  }

  return (
    <div className="space-y-2">
      {isActive && connected && (
        <div className="flex items-center gap-2 text-xs text-muted-foreground">
          <span className="relative flex h-2 w-2">
            <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-green-400 opacity-75" />
            <span className="relative inline-flex rounded-full h-2 w-2 bg-green-500" />
          </span>
          Streaming...
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
