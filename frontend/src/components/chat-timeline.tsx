"use client";

import { useState, useCallback, useEffect } from "react";
import { ChevronRight, AlertTriangle, FileCode2, X, FileText, ClipboardList, Check, PenLine } from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { MarkdownContent } from "@/components/markdown";
import { PLAN_MODE_PREFIX } from "@/lib/timeline";
import type { TimelineEntry } from "@/lib/timeline";
import type { SessionMessage, SessionLog } from "@/lib/types";
import { isImageURL, fileNameFromURL } from "@/lib/utils";
import { deriveToolDisplay, formatToolInput } from "@/lib/tool-label";

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
  const { label } = deriveToolDisplay(toolUse);
  const inputDetail = formatToolInput(toolUse);

  return (
    <div className="mx-2">
      <button
        onClick={() => setOpen(!open)}
        className="flex items-center gap-2 w-full text-left py-1.5 px-2 rounded-md hover:bg-muted/50 transition-colors text-xs group"
      >
        <ChevronRight className={`h-3 w-3 text-muted-foreground shrink-0 transition-transform duration-150 ${open ? "rotate-90" : ""}`} />
        <span className="text-foreground truncate min-w-0">{label}</span>
        <span className="ml-auto text-muted-foreground/60 text-xs tabular-nums shrink-0">
          {formatTimestamp(toolUse.created_at)}
        </span>
      </button>
      {open && (
        <div className="ml-7 mt-1 mb-2 space-y-1.5">
          {inputDetail && (
            <div className="rounded-md border border-border bg-muted/30 p-2 overflow-x-auto">
              <pre className="text-xs font-mono whitespace-pre-wrap break-all text-foreground/80">
                {inputDetail}
              </pre>
            </div>
          )}
          {toolResult ? (
            <div className="rounded-md border border-border bg-muted/30 p-2 overflow-x-auto">
              <pre className="text-xs font-mono whitespace-pre-wrap break-all text-muted-foreground">
                {toolResult.message}
              </pre>
            </div>
          ) : (
            !inputDetail && (
              <div className="text-xs text-muted-foreground italic">
                No result captured
              </div>
            )
          )}
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
              className="text-xs text-red-600 dark:text-red-400 hover:underline mt-1"
            >
              {expanded ? "Show less" : "Show more"}
            </button>
          )}
        </div>
        <span className="text-xs text-muted-foreground shrink-0">
          {formatTimestamp(log.created_at)}
        </span>
      </div>
    </div>
  );
}

function HiddenLogEntry({ log }: { log: SessionLog }) {
  return (
    <div className="flex items-start gap-2 px-2 py-0.5 text-xs font-mono text-muted-foreground/70">
      <span className="shrink-0 w-[52px]">{formatTimestamp(log.created_at)}</span>
      <Badge
        variant="secondary"
        className="shrink-0 text-xs px-1 py-0 bg-muted text-muted-foreground/60"
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
        <span className="ml-auto text-muted-foreground/60 text-xs tabular-nums shrink-0">
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

// ---------------------------------------------------------------------------
// Image lightbox overlay
// ---------------------------------------------------------------------------

function ImageLightbox({ src, alt, onClose }: { src: string; alt: string; onClose: () => void }) {
  useEffect(() => {
    function handleKeyDown(e: KeyboardEvent) {
      if (e.key === "Escape") onClose();
    }
    document.addEventListener("keydown", handleKeyDown);
    return () => document.removeEventListener("keydown", handleKeyDown);
  }, [onClose]);

  return (
    <div
      className="fixed inset-0 z-50 flex items-center justify-center bg-black/70 backdrop-blur-sm"
      onClick={onClose}
    >
      <button
        onClick={onClose}
        className="absolute top-4 right-4 h-8 w-8 rounded-full bg-background/80 flex items-center justify-center hover:bg-background transition-colors"
        aria-label="Close"
      >
        <X className="h-4 w-4" />
      </button>
      <img
        src={src}
        alt={alt}
        className="max-w-[90vw] max-h-[90vh] rounded-lg shadow-2xl object-contain"
        onClick={(e) => e.stopPropagation()}
      />
    </div>
  );
}

// ---------------------------------------------------------------------------
// Attachment thumbnails for messages
// ---------------------------------------------------------------------------

function AttachmentGrid({ attachments }: { attachments: string[] }) {
  const [lightboxSrc, setLightboxSrc] = useState<string | null>(null);

  const closeLightbox = useCallback(() => setLightboxSrc(null), []);

  if (!attachments || attachments.length === 0) return null;

  const images = attachments.filter(isImageURL);
  const files = attachments.filter((a) => !isImageURL(a));

  return (
    <>
      {lightboxSrc && (
        <ImageLightbox
          src={lightboxSrc}
          alt="Attachment"
          onClose={closeLightbox}
        />
      )}

      {images.length > 0 && (
        <div className="flex flex-wrap gap-2 mt-2">
          {images.map((url) => (
            <button
              key={url}
              type="button"
              onClick={() => setLightboxSrc(url)}
              className="relative group rounded-md overflow-hidden border border-border/50 hover:border-border transition-colors"
            >
              <img
                src={url}
                alt="Attached image"
                className="h-24 w-24 object-cover"
              />
              <div className="absolute inset-0 bg-black/0 group-hover:bg-black/10 transition-colors" />
            </button>
          ))}
        </div>
      )}

      {files.length > 0 && (
        <div className="flex flex-wrap gap-1.5 mt-2">
          {files.map((url) => {
            const fileName = fileNameFromURL(url);
            return (
              <a
                key={url}
                href={url}
                target="_blank"
                rel="noopener noreferrer"
                className="inline-flex items-center gap-1.5 rounded-md border border-border/50 bg-background/50 px-2 py-1 text-xs text-muted-foreground hover:text-foreground hover:border-border transition-colors"
              >
                <FileText className="h-3 w-3 shrink-0" />
                {fileName}
              </a>
            );
          })}
        </div>
      )}
    </>
  );
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
  // Strip plan mode prefix from user messages for display.
  const isPlanModeUser = msg.role === "user" && msg.content.startsWith(PLAN_MODE_PREFIX);
  const displayContent = isPlanModeUser
    ? msg.content.slice(PLAN_MODE_PREFIX.length)
    : msg.content;

  if (msg.role === "user") {
    return (
      <div className="flex justify-end">
        <div className="max-w-[80%] rounded-lg px-3 py-2 text-sm bg-primary bg-[image:var(--gradient-primary)] text-white shadow-sm">
          {isPlanModeUser && (
            <div className="flex items-center gap-1.5 mb-1.5">
              <ClipboardList className="h-3 w-3 text-white/80" />
              <span className="text-xs font-medium text-white/80 uppercase tracking-wide">Plan Mode</span>
            </div>
          )}
          {displayContent && <p className="whitespace-pre-wrap">{displayContent}</p>}
          {msg.attachments && msg.attachments.length > 0 && (
            <AttachmentGrid attachments={msg.attachments} />
          )}
          <p className="text-xs mt-1 text-white/70">
            {formatMessageTime(msg.created_at)}
          </p>
        </div>
      </div>
    );
  }

  return (
    <AssistantBubble>
      <MarkdownContent content={msg.content} />
      {msg.attachments && msg.attachments.length > 0 && (
        <AttachmentGrid attachments={msg.attachments} />
      )}
      <p className="text-xs mt-1 text-muted-foreground">
        {formatMessageTime(msg.created_at)}
      </p>
    </AssistantBubble>
  );
}

function PlanOutputBubble({
  children,
  onApprove,
  onAdjust,
  isRunning,
}: {
  children: React.ReactNode;
  onApprove?: () => void;
  onAdjust?: () => void;
  isRunning: boolean;
}) {
  return (
    <div className="flex justify-start">
      <div className="max-w-[80%] rounded-lg text-sm bg-muted border border-amber-200 dark:border-amber-800/50">
        <div className="flex items-center gap-1.5 px-3 pt-2 pb-1">
          <ClipboardList className="h-3.5 w-3.5 text-amber-600 dark:text-amber-400" />
          <span className="text-xs font-medium text-amber-700 dark:text-amber-400">Implementation Plan</span>
        </div>
        <div className="px-3 py-2">{children}</div>
        {(onApprove || onAdjust) && !isRunning && (
          <div className="flex items-center gap-2 px-3 pb-2.5 pt-1 border-t border-amber-200/50 dark:border-amber-800/30">
            {onApprove && (
              <Button
                size="sm"
                variant="default"
                className="h-7 text-xs gap-1.5"
                onClick={onApprove}
              >
                <Check className="h-3 w-3" />
                Approve Plan
              </Button>
            )}
            {onAdjust && (
              <Button
                size="sm"
                variant="outline"
                className="h-7 text-xs gap-1.5"
                onClick={onAdjust}
              >
                <PenLine className="h-3 w-3" />
                Adjust Plan
              </Button>
            )}
          </div>
        )}
      </div>
    </div>
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
      <Button
        variant="ghost"
        onClick={onClick}
        className="flex items-center gap-3 rounded-lg border border-border bg-muted/50 px-4 py-2.5 text-sm hover:bg-muted group text-left h-auto"
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
      </Button>
    </div>
  );
}

interface ChatTimelineProps {
  entries: TimelineEntry[];
  isRunning: boolean;
  diffStats?: { added: number; removed: number; files_changed: number } | null;
  onDiffClick?: () => void;
  onApprovePlan?: () => void;
  onAdjustPlan?: () => void;
}

export function ChatTimeline({ entries, isRunning, diffStats, onDiffClick, onApprovePlan, onAdjustPlan }: ChatTimelineProps) {
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
      case "plan_output":
        rendered.push(
          <PlanOutputBubble
            key={`plan-${entry.data.id}`}
            onApprove={onApprovePlan}
            onAdjust={onAdjustPlan}
            isRunning={isRunning}
          >
            <MarkdownContent content={entry.data.message} />
          </PlanOutputBubble>
        );
        break;
      case "plan_message":
        rendered.push(
          <PlanOutputBubble
            key={`planmsg-${entry.data.id}`}
            onApprove={onApprovePlan}
            onAdjust={onAdjustPlan}
            isRunning={isRunning}
          >
            <MarkdownContent content={entry.data.content} />
            <p className="text-xs mt-1 text-muted-foreground">
              {formatMessageTime(entry.data.created_at)}
            </p>
          </PlanOutputBubble>
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
