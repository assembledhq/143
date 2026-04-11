"use client";

import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { AlertTriangle, ChevronDown, ChevronUp, Info, AlertCircle } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { ScrollArea } from "@/components/ui/scroll-area";
import { cn } from "@/lib/utils";
import { api } from "@/lib/api";
import type { ConsoleMessage } from "@/lib/preview-types";

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
  warn: {
    icon: AlertCircle,
    color: "text-amber-600 dark:text-amber-400",
    label: "Warning",
  },
  info: {
    icon: Info,
    color: "text-blue-600 dark:text-blue-400",
    label: "Info",
  },
  log: {
    icon: Info,
    color: "text-muted-foreground",
    label: "Log",
  },
};

export function ConsoleBadge({ sessionId }: ConsoleBadgeProps) {
  const [expanded, setExpanded] = useState(false);

  const { data: messages = [] } = useQuery({
    queryKey: ["preview-console", sessionId],
    queryFn: () => api.sessions.preview.console(sessionId),
    refetchInterval: 5000,
  });

  const errorCount = messages.filter((m) => m.level === "error").length;
  const warnCount = messages.filter((m) => m.level === "warn").length;

  if (messages.length === 0) return null;

  return (
    <div className="relative">
      <button
        onClick={() => setExpanded(!expanded)}
        className="flex items-center gap-1"
      >
        {errorCount > 0 ? (
          <Badge variant="destructive" className="text-xs gap-1">
            <AlertTriangle className="size-3" />
            {errorCount} error{errorCount !== 1 ? "s" : ""}
          </Badge>
        ) : warnCount > 0 ? (
          <Badge
            variant="secondary"
            className="text-xs gap-1 bg-amber-500/15 text-amber-600 dark:text-amber-400 border-amber-500/20"
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
      </button>

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
              {messages.map((msg, i) => {
                const config = LEVEL_CONFIG[msg.level];
                const Icon = config.icon;
                return (
                  <div
                    key={i}
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
                          {msg.line_number != null ? `:${msg.line_number}` : ""}
                        </p>
                      )}
                    </div>
                    <span className="text-xs text-muted-foreground whitespace-nowrap">
                      {new Date(msg.timestamp).toLocaleTimeString()}
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
