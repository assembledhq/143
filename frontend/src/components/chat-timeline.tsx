"use client";

import { useState } from "react";
import { ChevronRight, AlertTriangle, Wrench, FileCode2 } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { MarkdownContent } from "@/components/markdown";
import type { TimelineEntry } from "@/lib/timeline";
import type { SessionMessage, SessionLog } from "@/lib/types";

function safeDate(dateStr: string): Date | null {
  const d = new Date(dateStr);
  return isNaN(d.getTime()) ? null : d;
}

function formatTimestamp(dateStr: string): string {
  const date = safeDate(dateStr);
  if (!date) return "";
  return date.toLocaleTimeString("en-US", {
    hour: "numeric",
    minute: "2-digit",
  });
}

function ToolGroupEntry({ toolUse, toolResult }: { toolUse: SessionLog; toolResult?: SessionLog }) {
  const [open, setOpen] = useState(false);
  const toolName = (toolUse.metadata?.tool as string) || "unknown";

  return (
    <div className="mx-2">
      <button
        onClick={() => setOpen(!open)}
        className="flex items-center gap-2 w-full text-left py-1.5 px-2 rounded-md hover:bg-muted/50 transition-colors text-xs group"
      >
        <ChevronRight className={`h-3 w-3 text-muted-foreground shrink-0 transition-transform duration-150 ${open ? "rotate-90" : ""}`} />
        <Wrench className="h-3 w-3 text-blue-600 dark:text-blue-400 shrink-0" />
        <Badge
          variant="secondary"
          className="bg-blue-500/10 text-blue-700 dark:text-blue-400 text-[10px] px-1.5 py-0"
        >
          {toolName}
        </Badge>
        <span className="ml-auto text-muted-foreground/60 text-[10px] tabular-nums shrink-0">
          {formatTimestamp(toolUse.created_at)}
        </span>
      </button>
      {open && toolResult && (
        <div className="ml-7 mt-1 mb-2 rounded-md border border-border bg-muted/30 p-2 overflow-x-auto">
          <pre className="text-xs font-mono whitespace-pre-wrap break-all text-muted-foreground">
            {toolResult.message}
          </pre>
        </div>
      )}
      {open && !toolResult && (
        <div className="ml-7 mt-1 mb-2 text-xs text-muted-foreground italic">
          No result captured
        </div>
      )}
    </div>
  );
}

function ErrorEntry({ log }: { log: SessionLog }) {
  const [expanded, setExpanded] = useState(false);
  const isLong = log.message.length > 200;
  const displayMessage = !isLong || expanded ? log.message : log.message.slice(0, 200) + "...";

  return (
    <div className="mx-2 rounded-md border border-red-200 dark:border-red-900/50 bg-red-500/5 px-3 py-2">
      <div className="flex items-start gap-2">
        <AlertTriangle className="h-3.5 w-3.5 text-red-600 dark:text-red-400 shrink-0 mt-0.5" />
        <div className="flex-1 min-w-0">
          <pre className="text-xs font-mono whitespace-pre-wrap break-all text-red-700 dark:text-red-400">
            {displayMessage}
          </pre>
          {isLong && (
            <button
              onClick={() => setExpanded(!expanded)}
              className="text-[10px] text-red-600 dark:text-red-400 hover:underline mt-1"
            >
              {expanded ? "Show less" : "Show more"}
            </button>
          )}
        </div>
        <span className="text-[10px] text-muted-foreground shrink-0">
          {formatTimestamp(log.created_at)}
        </span>
      </div>
    </div>
  );
}

function HiddenLogEntry({ log }: { log: SessionLog }) {
  return (
    <div className="flex items-start gap-2 px-2 py-0.5 text-[11px] font-mono text-muted-foreground/70">
      <span className="shrink-0 w-[52px]">{formatTimestamp(log.created_at)}</span>
      <Badge
        variant="secondary"
        className="shrink-0 text-[9px] px-1 py-0 bg-muted text-muted-foreground/60"
      >
        {log.level}
      </Badge>
      <span className="break-all">{log.message}</span>
    </div>
  );
}

function HiddenLogsGroup({ logs }: { logs: SessionLog[] }) {
  const [open, setOpen] = useState(false);

  if (logs.length === 0) return null;

  return (
    <div className="mx-2">
      <button
        onClick={() => setOpen(!open)}
        className="flex items-center gap-2 w-full text-left py-1.5 px-2 rounded-md hover:bg-muted/50 transition-colors text-xs group"
      >
        <ChevronRight className={`h-3 w-3 text-muted-foreground shrink-0 transition-transform duration-150 ${open ? "rotate-90" : ""}`} />
        <span className="text-muted-foreground">
          {logs.length} log {logs.length === 1 ? "entry" : "entries"}
        </span>
        <span className="ml-auto text-muted-foreground/60 text-[10px] tabular-nums shrink-0">
          {formatTimestamp(logs[0].created_at)}
        </span>
      </button>
      {open && (
        <div className="ml-7 mt-1 mb-2 rounded-md border border-border bg-muted/30 py-1 overflow-x-auto max-h-[300px] overflow-y-auto">
          {logs.map((log) => (
            <HiddenLogEntry key={log.id} log={log} />
          ))}
        </div>
      )}
    </div>
  );
}

export function formatMessageTime(dateStr: string): string {
  const date = safeDate(dateStr);
  if (!date) return "";
  const now = new Date();
  const isToday =
    date.getFullYear() === now.getFullYear() &&
    date.getMonth() === now.getMonth() &&
    date.getDate() === now.getDate();
  if (isToday) {
    return date.toLocaleTimeString("en-US", {
      hour: "numeric",
      minute: "2-digit",
    });
  }
  return date.toLocaleDateString("en-US", {
    month: "short",
    day: "numeric",
    hour: "numeric",
    minute: "2-digit",
  });
}

function AssistantBubble({ children }: { children: React.ReactNode }) {
  return (
    <div className="flex justify-start">
      <div className="max-w-[80%] rounded-lg px-3 py-2 text-sm bg-muted">
        {children}
      </div>
    </div>
  );
}

function MessageBubble({ msg }: { msg: SessionMessage }) {
  if (msg.role === "user") {
    return (
      <div className="flex justify-end">
        <div className="max-w-[80%] rounded-lg px-3 py-2 text-sm bg-primary bg-[image:var(--gradient-primary)] text-white shadow-sm">
          <p className="whitespace-pre-wrap">{msg.content}</p>
          <p className="text-[10px] mt-1 text-white/70">
            {formatMessageTime(msg.created_at)}
          </p>
        </div>
      </div>
    );
  }

  return (
    <AssistantBubble>
      <MarkdownContent content={msg.content} />
      <p className="text-[10px] mt-1 text-muted-foreground">
        {formatMessageTime(msg.created_at)}
      </p>
    </AssistantBubble>
  );
}

function CodeDiffSummary({
  added,
  removed,
  filesChanged,
  onClick,
}: {
  added: number;
  removed: number;
  filesChanged: number;
  onClick?: () => void;
}) {
  return (
    <div className="flex justify-start">
      <button
        onClick={onClick}
        className="flex items-center gap-3 rounded-lg border border-border bg-muted/50 px-4 py-2.5 text-sm hover:bg-muted transition-colors group text-left"
      >
        <FileCode2 className="h-4 w-4 text-muted-foreground shrink-0" />
        <span className="font-mono text-xs flex items-center gap-1.5">
          <span className="text-green-600 dark:text-green-400 font-semibold">+{added}</span>
          <span className="text-muted-foreground/40">/</span>
          <span className="text-red-600 dark:text-red-400 font-semibold">-{removed}</span>
        </span>
        <span className="text-muted-foreground text-xs">
          {filesChanged} {filesChanged === 1 ? "file" : "files"} changed
        </span>
        <ChevronRight className="h-3.5 w-3.5 text-muted-foreground/50 group-hover:text-muted-foreground transition-colors ml-1" />
      </button>
    </div>
  );
}

interface ChatTimelineProps {
  entries: TimelineEntry[];
  isRunning: boolean;
  diffStats?: { added: number; removed: number; files_changed: number } | null;
  onDiffClick?: () => void;
}

export function ChatTimeline({ entries, isRunning, diffStats, onDiffClick }: ChatTimelineProps) {
  // Separate visible entries (messages, tool groups, errors) from hidden logs.
  // Group consecutive hidden logs together so they share a single "Show more" toggle.
  const rendered: React.ReactNode[] = [];
  let hiddenBatch: SessionLog[] = [];

  function flushHidden() {
    if (hiddenBatch.length > 0) {
      rendered.push(
        <HiddenLogsGroup key={`hidden-${hiddenBatch[0].id}`} logs={[...hiddenBatch]} />
      );
      hiddenBatch = [];
    }
  }

  for (const entry of entries) {
    if (entry.kind === "log") {
      hiddenBatch.push(entry.data);
      continue;
    }

    flushHidden();

    switch (entry.kind) {
      case "message":
        rendered.push(<MessageBubble key={`msg-${entry.data.id}`} msg={entry.data} />);
        break;
      case "assistant_output":
        rendered.push(
          <AssistantBubble key={`aout-${entry.data.id}`}>
            <MarkdownContent content={entry.data.message} />
          </AssistantBubble>
        );
        break;
      case "tool_group":
        rendered.push(
          <ToolGroupEntry
            key={`tool-${entry.toolUse.id}`}
            toolUse={entry.toolUse}
            toolResult={entry.toolResult}
          />
        );
        break;
      case "error":
        rendered.push(<ErrorEntry key={`err-${entry.data.id}`} log={entry.data} />);
        break;
    }
  }

  flushHidden();

  // Show diff summary after all timeline entries when changes exist
  if (diffStats && (diffStats.added > 0 || diffStats.removed > 0)) {
    rendered.push(
      <CodeDiffSummary
        key="diff-summary"
        added={diffStats.added}
        removed={diffStats.removed}
        filesChanged={diffStats.files_changed}
        onClick={onDiffClick}
      />
    );
  }

  if (isRunning) {
    rendered.push(
      <div key="working" className="flex justify-start">
        <div className="bg-muted rounded-lg px-3 py-2 text-sm">
          <span className="flex items-center gap-2 text-muted-foreground">
            <span className="relative flex h-2 w-2">
              <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-primary opacity-75" />
              <span className="relative inline-flex rounded-full h-2 w-2 bg-primary" />
            </span>
            Agent is working...
          </span>
        </div>
      </div>
    );
  }

  return <>{rendered}</>;
}
