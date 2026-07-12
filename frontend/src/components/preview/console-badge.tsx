"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { AlertTriangle, ChevronDown, ChevronUp, Info, AlertCircle } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { ScrollArea } from "@/components/ui/scroll-area";
import { cn } from "@/lib/utils";
import { api } from "@/lib/api";
import type { ConsoleMessage } from "@/lib/preview-types";
import { useLiveHealth } from "@/components/live-event-provider";
import { useDocumentVisible } from "@/hooks/use-document-visible";
import { liveRefreshInterval } from "@/lib/live-refresh-policy";

interface ConsoleBadgeProps {
  sessionId: string;
}

const LEVEL_CONFIG: Record<
  ConsoleMessage["level"],
  { icon: typeof AlertTriangle; color: string; label: string }
> = {
  error: {
    icon: AlertTriangle,
    color: "text-destructive",
    label: "Error",
  },
  warning: {
    icon: AlertCircle,
    color: "text-warning",
    label: "Warning",
  },
  info: {
    icon: Info,
    color: "text-info",
    label: "Info",
  },
  log: {
    icon: Info,
    color: "text-muted-foreground",
    label: "Log",
  },
};

export function ConsoleBadge({ sessionId }: ConsoleBadgeProps) {
  const documentVisible = useDocumentVisible();
  const liveHealth = useLiveHealth();
  const [expanded, setExpanded] = useState(false);
  const containerRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!expanded) return;
    const handleClickOutside = (e: MouseEvent) => {
      if (containerRef.current && !containerRef.current.contains(e.target as Node)) {
        setExpanded(false);
      }
    };
    document.addEventListener("mousedown", handleClickOutside);
    return () => document.removeEventListener("mousedown", handleClickOutside);
  }, [expanded]);

  const { data: rawMessages } = useQuery({
    queryKey: ["preview-console", sessionId],
    queryFn: ({ signal }) =>
      api.sessions.preview.console(sessionId, { signal, timeoutMs: 5000 }),
    refetchInterval: liveRefreshInterval(["preview-console", sessionId], "active-detail", liveHealth, documentVisible),
    retry: false,
  });
  const messages = useMemo(
    () => (Array.isArray(rawMessages) ? rawMessages : []),
    [rawMessages],
  );

  const { errorCount, warnCount } = useMemo(() => {
    let errors = 0;
    let warnings = 0;
    for (const m of messages) {
      if (m.level === "error") errors++;
      else if (m.level === "warning") warnings++;
    }
    return { errorCount: errors, warnCount: warnings };
  }, [messages]);

  if (messages.length === 0) return null;

  return (
    <div className="relative" ref={containerRef}>
      <Button
        variant="ghost"
        size="sm"
        onClick={() => setExpanded(!expanded)}
        className="flex items-center gap-1 h-auto p-0"
      >
        {errorCount > 0 ? (
          <Badge variant="destructive" className="text-xs gap-1">
            <AlertTriangle className="size-3" />
            {errorCount} error{errorCount !== 1 ? "s" : ""}
          </Badge>
        ) : warnCount > 0 ? (
          <Badge
            variant="secondary"
            className="text-xs gap-1 bg-warning/15 text-warning border-warning/20"
          >
            <AlertCircle className="size-3" />
            {warnCount} warning{warnCount !== 1 ? "s" : ""}
          </Badge>
        ) : (
          <Badge variant="secondary" className="text-xs gap-1">
            <Info className="size-3" />
            {messages.length} message{messages.length !== 1 ? "s" : ""}
          </Badge>
        )}
        {expanded ? (
          <ChevronUp className="size-3 text-muted-foreground" />
        ) : (
          <ChevronDown className="size-3 text-muted-foreground" />
        )}
      </Button>

      {expanded && (
        <div className="absolute top-full left-0 mt-1 z-50 w-[400px] rounded-lg border bg-background shadow-lg">
          <div className="flex items-center justify-between p-2 border-b">
            <span className="text-xs font-medium">
              Console ({messages.length})
            </span>
            <Button
              size="icon-xs"
              variant="ghost"
              onClick={() => setExpanded(false)}
            >
              <ChevronUp className="size-3" />
            </Button>
          </div>
          <ScrollArea className="max-h-[300px]">
            <div className="divide-y">
              {messages.map((msg, idx) => {
                const config = LEVEL_CONFIG[msg.level];
                const Icon = config.icon;
                return (
                  <div
                    key={`${idx}-${msg.time}-${msg.level}`}
                    className={cn(
                      "flex gap-2 p-2 text-xs",
                      msg.level === "error" && "bg-destructive/5"
                    )}
                  >
                    <Icon className={cn("size-3 mt-0.5 shrink-0", config.color)} />
                    <div className="min-w-0 flex-1 space-y-0.5">
                      <p className="font-mono text-xs break-all whitespace-pre-wrap">
                        {msg.text}
                      </p>
                      {msg.source && (
                        <p className="text-xs text-muted-foreground truncate">
                          {msg.source}
                          {msg.line_no != null ? `:${msg.line_no}` : ""}
                        </p>
                      )}
                    </div>
                    <span className="text-xs text-muted-foreground whitespace-nowrap">
                      {new Date(msg.time).toLocaleTimeString()}
                    </span>
                  </div>
                );
              })}
            </div>
          </ScrollArea>
        </div>
      )}
    </div>
  );
}
