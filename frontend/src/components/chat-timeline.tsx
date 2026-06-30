"use client";

import { memo, useState, useCallback } from "react";
import { useQuery } from "@tanstack/react-query";
import { ChevronRight, AlertTriangle, FileCode2, FileText, ClipboardList, Check, PenLine, FolderTree, Loader2, Square } from "lucide-react";
import Image from "next/image";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Separator } from "@/components/ui/separator";
import {
  Tooltip,
  TooltipContent,
  TooltipProvider,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import { LazyMarkdownContent } from "@/components/lazy-markdown-content";
import { CopyButton } from "@/components/copy-button";
import { HumanInputRequestCard } from "@/components/human-input-request-card";
import { LinearIcon } from "@/components/linear-icon";
import { looksLikeLinearRef } from "@/lib/linear-refs";
import { PLAN_MODE_PREFIX } from "@/lib/timeline";
import type { TimelineEntry } from "@/lib/timeline";
import type { HumanInputAnswerBody, HumanInputRequest, SessionInputReference, SessionMessage, SessionLog } from "@/lib/types";
import { formatDateTime, isImageURL, fileNameFromURL } from "@/lib/utils";
import { deriveToolDisplay, formatToolInput } from "@/lib/tool-label";
import { ImageLightbox } from "@/components/image-lightbox";
import { api } from "@/lib/api";
import { queryKeys } from "@/lib/query-keys";

// Shared max-width for chat bubbles (user, assistant, plan) so they line up
// consistently across the timeline.
const BUBBLE_MAX_WIDTH = "max-w-[92%]";

function LogMessageDetail({
  log,
  className,
  loadingLabel = "Loading full output...",
}: {
  log: SessionLog;
  className: string;
  loadingLabel?: string;
}) {
  if (log.message_truncated !== true) {
    return <pre className={className}>{log.message}</pre>;
  }

  return (
    <FetchedLogMessageDetail
      log={log}
      className={className}
      loadingLabel={loadingLabel}
    />
  );
}

function FetchedLogMessageDetail({
  log,
  className,
  loadingLabel,
}: {
  log: SessionLog;
  className: string;
  loadingLabel: string;
}) {
  const detailQuery = useQuery({
    queryKey: queryKeys.sessions.logDetail(log.session_id, log.id),
    queryFn: () => api.sessions.getLogDetail(log.session_id, log.id),
    staleTime: Infinity,
  });
  const message = detailQuery.data?.data.message ?? log.message;

  return (
    <div className="space-y-1">
      <pre className={className}>{message}</pre>
      {detailQuery.isLoading && (
        <div className="flex items-center gap-1.5 text-xs text-muted-foreground">
          <Loader2 className="h-3 w-3 animate-spin" />
          {loadingLabel}
        </div>
      )}
      {detailQuery.isError && (
        <div className="text-xs text-destructive">Failed to load full output</div>
      )}
    </div>
  );
}

function safeDate(dateStr: string): Date | null {
  const d = new Date(dateStr);
  return isNaN(d.getTime()) ? null : d;
}

function formatTimestamp(dateStr: string): string {
  const date = safeDate(dateStr);
  if (!date) return "";
  return date.toLocaleTimeString(undefined, {
    hour: "numeric",
    minute: "2-digit",
  });
}

function formatAbsoluteDateTime(dateStr: string): string {
  return formatDateTime(dateStr, {
    fallback: "",
    weekday: true,
    year: true,
    seconds: true,
    timeZoneName: true,
  });
}

function formatDaySeparatorLabel(dateStr: string): string {
  const date = safeDate(dateStr);
  if (!date) return "";
  const now = new Date();
  const startOfToday = new Date(now).setHours(0, 0, 0, 0);
  const startOfDate = new Date(date).setHours(0, 0, 0, 0);
  const diffDays = Math.floor((startOfToday - startOfDate) / 86_400_000);
  if (diffDays === 0) return "Today";
  if (diffDays === 1) return "Yesterday";
  const sameYear = date.getFullYear() === now.getFullYear();
  return date.toLocaleDateString(undefined, {
    weekday: "long",
    month: "short",
    day: "numeric",
    ...(sameYear ? {} : { year: "numeric" }),
  });
}

function TimestampLabel({
  dateStr,
  formatter,
  className,
}: {
  dateStr: string;
  formatter: (s: string) => string;
  className?: string;
}) {
  const absolute = formatAbsoluteDateTime(dateStr);
  const label = formatter(dateStr);
  if (!absolute) {
    return <span className={className}>{label}</span>;
  }
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span className={className}>{label}</span>
      </TooltipTrigger>
      <TooltipContent>{absolute}</TooltipContent>
    </Tooltip>
  );
}

function DaySeparator({ dateStr }: { dateStr: string }) {
  const label = formatDaySeparatorLabel(dateStr);
  if (!label) return null;
  return (
    <div className="my-4 flex items-center gap-3 px-1">
      <Separator className="flex-1 bg-border/70" />
      <div className="rounded-full border border-border/60 bg-muted/35 px-2.5 py-1 text-xs font-medium tabular-nums text-muted-foreground shadow-xs">
        {label}
      </div>
      <Separator className="flex-1 bg-border/70" />
    </div>
  );
}

const ToolGroupEntry = memo(function ToolGroupEntry({ toolUse, toolResult }: { toolUse: SessionLog; toolResult?: SessionLog }) {
  const [open, setOpen] = useState(false);
  const { label } = deriveToolDisplay(toolUse);
  const inputDetail = formatToolInput(toolUse);

  return (
    <div className="mx-2 min-w-0">
      <button
        onClick={() => setOpen(!open)}
        className="flex items-center gap-2 w-full text-left py-1.5 px-2 rounded-md hover:bg-muted/50 transition-colors text-xs group"
      >
        <ChevronRight className={`h-3 w-3 text-muted-foreground shrink-0 transition-transform duration-150 ${open ? "rotate-90" : ""}`} />
        <span className="text-foreground truncate min-w-0">{label}</span>
        <TimestampLabel
          dateStr={toolUse.created_at}
          formatter={formatTimestamp}
          className="ml-auto text-muted-foreground/60 text-xs tabular-nums shrink-0"
        />
      </button>
      {open && (
        <div className="ml-7 mt-1 mb-2 space-y-1.5 min-w-0">
          {inputDetail && (
            <div className="rounded-md border border-border bg-muted/30 p-2 min-w-0 max-w-full">
              <pre className="text-xs font-mono whitespace-pre-wrap break-all text-foreground/80 max-w-full">
                {inputDetail}
              </pre>
            </div>
          )}
          {toolResult ? (
            <div className="rounded-md border border-border bg-muted/30 p-2 min-w-0 max-w-full">
              <LogMessageDetail
                log={toolResult}
                className="text-xs font-mono whitespace-pre-wrap break-all text-muted-foreground max-w-full"
              />
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
});

const ErrorEntry = memo(function ErrorEntry({ log }: { log: SessionLog }) {
  const [expanded, setExpanded] = useState(false);
  const isLong = log.message.length > 200 || log.message_truncated === true;
  const displayMessage = !isLong || expanded ? log.message : log.message.slice(0, 200) + "...";

  return (
    <div className="mx-2 min-w-0 max-w-full rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2">
      <div className="flex items-start gap-2 min-w-0">
        <AlertTriangle className="h-3.5 w-3.5 text-destructive shrink-0 mt-0.5" />
        <div className="flex-1 min-w-0">
          {expanded ? (
            <LogMessageDetail
              log={log}
              className="text-xs font-mono whitespace-pre-wrap break-all text-destructive max-w-full"
            />
          ) : (
            <pre className="text-xs font-mono whitespace-pre-wrap break-all text-destructive max-w-full">
              {displayMessage}
            </pre>
          )}
          {isLong && (
            <button
              onClick={() => setExpanded(!expanded)}
              className="text-xs text-destructive hover:underline mt-1"
            >
              {expanded ? "Show less" : "Show more"}
            </button>
          )}
        </div>
        <TimestampLabel
          dateStr={log.created_at}
          formatter={formatTimestamp}
          className="text-xs text-muted-foreground shrink-0"
        />
      </div>
    </div>
  );
});

const HiddenLogEntry = memo(function HiddenLogEntry({ log }: { log: SessionLog }) {
  const [expanded, setExpanded] = useState(false);
  const canExpand = log.message_truncated === true;

  return (
    <div className="flex items-start gap-2 px-2 py-0.5 text-xs font-mono text-muted-foreground/70 min-w-0">
      <TimestampLabel
        dateStr={log.created_at}
        formatter={formatTimestamp}
        className="shrink-0 w-[52px]"
      />
      <Badge
        variant="secondary"
        className="shrink-0 text-xs px-1 py-0 bg-muted text-muted-foreground/60"
      >
        {log.level}
      </Badge>
      <div className="min-w-0 flex-1 break-all">
        {expanded ? (
          <LogMessageDetail
            log={log}
            className="text-xs font-mono whitespace-pre-wrap break-all text-muted-foreground/70 max-w-full"
          />
        ) : (
          <span>{log.message}</span>
        )}
        {canExpand && (
          <button
            type="button"
            onClick={() => setExpanded((value) => !value)}
            className="ml-2 text-xs text-muted-foreground hover:text-foreground hover:underline"
          >
            {expanded ? "Show preview" : "Load full output"}
          </button>
        )}
      </div>
    </div>
  );
});

function HiddenLogsGroup({ logs }: { logs: SessionLog[] }) {
  const [open, setOpen] = useState(false);

  if (logs.length === 0) return null;

  return (
    <div className="mx-2 min-w-0">
      <button
        onClick={() => setOpen(!open)}
        className="flex items-center gap-2 w-full text-left py-1.5 px-2 rounded-md hover:bg-muted/50 transition-colors text-xs group"
      >
        <ChevronRight className={`h-3 w-3 text-muted-foreground shrink-0 transition-transform duration-150 ${open ? "rotate-90" : ""}`} />
        <span className="text-muted-foreground">
          {logs.length} log {logs.length === 1 ? "entry" : "entries"}
        </span>
        <TimestampLabel
          dateStr={logs[0].created_at}
          formatter={formatTimestamp}
          className="ml-auto text-muted-foreground/60 text-xs tabular-nums shrink-0"
        />
      </button>
      {open && (
        <div className="ml-7 mt-1 mb-2 min-w-0 max-w-full rounded-md border border-border bg-muted/30 py-1 max-h-[300px] overflow-y-auto">
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
    return date.toLocaleTimeString(undefined, {
      hour: "numeric",
      minute: "2-digit",
    });
  }
  return date.toLocaleDateString(undefined, {
    month: "short",
    day: "numeric",
    hour: "numeric",
    minute: "2-digit",
  });
}

export const AttachmentGrid = memo(function AttachmentGrid({ attachments }: { attachments: string[] }) {
  const [lightboxSrc, setLightboxSrc] = useState<string | null>(null);

  const closeLightbox = useCallback(() => setLightboxSrc(null), []);

  if (!attachments || attachments.length === 0) return null;

  const images = attachments.filter(isImageURL);
  const files = attachments.filter((a) => !isImageURL(a));

  return (
    <>
      {lightboxSrc && (
        <ImageLightbox
          open
          src={lightboxSrc}
          alt="Attachment"
          onOpenChange={(open) => {
            if (!open) {
              closeLightbox();
            }
          }}
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
              <Image
                src={url}
                alt="Attached image"
                width={96}
                height={96}
                unoptimized
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
});

function AssistantBubble({ children }: { children: React.ReactNode }) {
  return (
    <div className="flex justify-start">
      <div className={`${BUBBLE_MAX_WIDTH} min-w-0 rounded-lg px-3 py-2 text-sm bg-muted break-words`}>
        {children}
      </div>
    </div>
  );
}

function referenceTagLabel(reference: SessionInputReference): string {
  if (reference.kind === "app" && looksLikeLinearRef(reference.token ?? reference.id ?? reference.display)) {
    return reference.id?.trim() || reference.display.trim() || reference.token?.trim() || "Linear issue";
  }
  return reference.display.trim() || reference.path?.trim() || reference.id?.trim() || reference.token?.trim() || "Reference";
}

const ReferenceTags = memo(function ReferenceTags({ references }: { references: SessionInputReference[] }) {
  if (references.length === 0) {
    return null;
  }

  return (
    <div className="mt-2 flex flex-wrap gap-1.5">
      {references.map((reference) => {
        const label = referenceTagLabel(reference);
        const key = `${reference.kind}:${reference.path ?? reference.id ?? reference.token ?? reference.display}`;
        const isLinear = reference.kind === "app" && looksLikeLinearRef(reference.token ?? reference.id ?? reference.display);

        return (
          <Badge
            key={key}
            variant="secondary"
            className="gap-1.5 rounded-full border border-white/20 bg-white/12 px-2 py-0.5 text-xs font-medium text-white"
          >
            {isLinear ? (
              <>
                <LinearIcon className="h-3 w-3 shrink-0 dark:invert-0" />
                <span className="uppercase tracking-wide text-white/80">Linear</span>
              </>
            ) : reference.kind === "directory" ? (
              <FolderTree className="h-3 w-3 shrink-0 text-white/80" />
            ) : (
              <FileCode2 className="h-3 w-3 shrink-0 text-white/80" />
            )}
            <span className="max-w-[14rem] truncate">{label}</span>
          </Badge>
        );
      })}
    </div>
  );
});

const MessageBubble = memo(function MessageBubble({ msg }: { msg: SessionMessage }) {
  // Strip plan mode prefix from user messages for display.
  const isPlanModeUser = msg.role === "user" && msg.content.startsWith(PLAN_MODE_PREFIX);
  const isSystemAutoRepair = msg.role === "user" && msg.source === "system_auto_repair";
  const displayContent = isPlanModeUser
    ? msg.content.slice(PLAN_MODE_PREFIX.length)
    : msg.content;

  if (msg.role === "user") {
    return (
      <div className="flex justify-end">
        <div className={`chat-user-bubble ${BUBBLE_MAX_WIDTH} min-w-0 rounded-lg px-3 py-2 text-sm bg-primary bg-[image:var(--gradient-primary)] text-white shadow-sm break-words`}>
          {isPlanModeUser && (
            <div className="flex items-center gap-1.5 mb-1.5">
              <ClipboardList className="h-3 w-3 text-white/80" />
              <span className="text-xs font-medium text-white/80 uppercase tracking-wide">Plan Mode</span>
            </div>
          )}
          {isSystemAutoRepair && (
            <div className="flex items-center gap-1.5 mb-1.5">
              <PenLine className="h-3 w-3 text-white/80" />
              <span className="text-xs font-medium text-white/80 uppercase tracking-wide">143 auto-repair</span>
            </div>
          )}
          {displayContent && <p className="whitespace-pre-wrap break-words">{displayContent}</p>}
          {msg.references && msg.references.length > 0 && (
            <ReferenceTags references={msg.references} />
          )}
          {msg.attachments && msg.attachments.length > 0 && (
            <AttachmentGrid attachments={msg.attachments} />
          )}
          <div className="mt-1 flex items-center justify-end gap-1">
            <TimestampLabel
              dateStr={msg.created_at}
              formatter={formatMessageTime}
              className="block text-xs text-white/70"
            />
            <CopyButton
              value={displayContent}
              label="Copy prompt"
              className="-mr-1 h-6 w-6 text-white/70 hover:bg-white/10 hover:text-white disabled:text-white/40"
            />
          </div>
        </div>
      </div>
    );
  }

  return (
    <AssistantBubble>
      <LazyMarkdownContent content={msg.content} />
      {msg.attachments && msg.attachments.length > 0 && (
        <AttachmentGrid attachments={msg.attachments} />
      )}
      <div className="mt-1 flex items-center gap-1">
        <TimestampLabel
          dateStr={msg.created_at}
          formatter={formatMessageTime}
          className="block text-xs text-muted-foreground"
        />
        <CopyButton
          value={msg.content}
          label="Copy final response"
          className="h-6 w-6 text-muted-foreground hover:text-foreground"
        />
      </div>
    </AssistantBubble>
  );
});

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
      <div className={`${BUBBLE_MAX_WIDTH} min-w-0 rounded-lg text-sm bg-muted border border-amber-200 dark:border-amber-800/50 break-words`}>
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

const CodeDiffSummary = memo(function CodeDiffSummary({
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
});

interface ChatTimelineProps {
  entries: TimelineEntry[];
  isRunning: boolean;
  // When the runtime was interrupted (worker drain / deploy) and is waiting to
  // resume, the thread is still "running" but the agent is not actively working.
  // Show a recovery label instead of "Agent is working…" so the spinner is honest.
  recoveryActive?: boolean;
  stoppingLabel?: string;
  stoppedLabel?: string;
  diffStats?: { added: number; removed: number; files_changed: number } | null;
  onDiffClick?: () => void;
  onApprovePlan?: () => void;
  onAdjustPlan?: () => void;
  humanInputSubmittingId?: string | null;
  autoOpenHumanInputId?: string | null;
  humanInputAnswerable?: boolean;
  onAnswerHumanInput?: (request: HumanInputRequest, body: HumanInputAnswerBody) => Promise<void> | void;
  onCancelHumanInput?: (request: HumanInputRequest) => Promise<void> | void;
  onDismissHumanInputAutoOpen?: (request: HumanInputRequest) => void;
  getEntryContainerProps?: (
    entry: TimelineEntry,
    index: number,
  ) => React.HTMLAttributes<HTMLDivElement> & Record<`data-${string}`, string | number | undefined>;
}

function ChatTimelineImpl({ entries, isRunning, recoveryActive = false, stoppingLabel, stoppedLabel, diffStats, onDiffClick, onApprovePlan, onAdjustPlan, humanInputSubmittingId, autoOpenHumanInputId, humanInputAnswerable = true, onAnswerHumanInput, onCancelHumanInput, onDismissHumanInputAutoOpen, getEntryContainerProps }: ChatTimelineProps) {
  // Separate visible entries (messages, tool groups, errors) from hidden logs.
  // Group consecutive hidden logs together so they share a single "Show more" toggle.
  const rendered: React.ReactNode[] = [];
  let hiddenBatch: Array<{ entry: Extract<TimelineEntry, { kind: "log" }>; index: number }> = [];
  let lastDay: string | null = null;

  function wrapEntry(node: React.ReactNode, entry: TimelineEntry, index: number, key: string) {
    const props = getEntryContainerProps?.(entry, index);
    if (!props) {
      return node;
    }

    return (
      <div key={`anchor-${key}`} {...props}>
        {node}
      </div>
    );
  }

  function flushHidden() {
    if (hiddenBatch.length > 0) {
      const first = hiddenBatch[0];
      rendered.push(
        wrapEntry(
          <HiddenLogsGroup
            key={`hidden-${first.entry.data.id}`}
            logs={hiddenBatch.map((item) => item.entry.data)}
          />,
          first.entry,
          first.index,
          `hidden-${first.entry.data.id}`,
        )
      );
      hiddenBatch = [];
    }
  }

  function maybeEmitDaySeparator(dateStr: string) {
    const date = safeDate(dateStr);
    if (!date) return;
    const day = date.toDateString();
    if (day === lastDay) return;
    lastDay = day;
    rendered.push(<DaySeparator key={`day-${day}`} dateStr={dateStr} />);
  }

  for (const [index, entry] of entries.entries()) {
    if (entry.kind === "log") {
      hiddenBatch.push({ entry, index });
      continue;
    }

    flushHidden();

    const entryDateStr =
      entry.kind === "tool_group" ? entry.toolUse.created_at : entry.data.created_at;
    maybeEmitDaySeparator(entryDateStr);

    switch (entry.kind) {
      case "message":
        rendered.push(
          wrapEntry(
            <MessageBubble key={`msg-${entry.data.id}`} msg={entry.data} />,
            entry,
            index,
            `msg-${entry.data.id}`,
          ),
        );
        break;
      case "assistant_output":
        rendered.push(
          wrapEntry(
            <AssistantBubble key={`aout-${entry.data.id}`}>
              <LazyMarkdownContent content={entry.data.message} />
            </AssistantBubble>,
            entry,
            index,
            `aout-${entry.data.id}`,
          ),
        );
        break;
      case "plan_output":
        rendered.push(
          wrapEntry(
            <PlanOutputBubble
              key={`plan-${entry.data.id}`}
              onApprove={onApprovePlan}
              onAdjust={onAdjustPlan}
              isRunning={isRunning}
            >
              <LazyMarkdownContent content={entry.data.message} />
            </PlanOutputBubble>,
            entry,
            index,
            `plan-${entry.data.id}`,
          ),
        );
        break;
      case "plan_message":
        rendered.push(
          wrapEntry(
            <PlanOutputBubble
              key={`planmsg-${entry.data.id}`}
              onApprove={onApprovePlan}
              onAdjust={onAdjustPlan}
              isRunning={isRunning}
            >
              <LazyMarkdownContent content={entry.data.content} />
              <TimestampLabel
                dateStr={entry.data.created_at}
                formatter={formatMessageTime}
                className="block text-xs mt-1 text-muted-foreground"
              />
            </PlanOutputBubble>,
            entry,
            index,
            `planmsg-${entry.data.id}`,
          ),
        );
        break;
      case "tool_group":
        rendered.push(
          wrapEntry(
            <ToolGroupEntry
              key={`tool-${entry.toolUse.id}`}
              toolUse={entry.toolUse}
              toolResult={entry.toolResult}
            />,
            entry,
            index,
            `tool-${entry.toolUse.id}`,
          ),
        );
        break;
      case "error":
        rendered.push(
          wrapEntry(
            <ErrorEntry key={`err-${entry.data.id}`} log={entry.data} />,
            entry,
            index,
            `err-${entry.data.id}`,
          ),
        );
        break;
      case "human_input":
        rendered.push(
          wrapEntry(
            <HumanInputRequestCard
              key={`human-input-${entry.data.id}`}
              request={entry.data}
              autoOpen={autoOpenHumanInputId === entry.data.id}
              answerable={humanInputAnswerable}
              submitting={humanInputSubmittingId === entry.data.id}
              onAnswer={(body) => onAnswerHumanInput?.(entry.data, body)}
              onCancel={onCancelHumanInput ? () => onCancelHumanInput(entry.data) : undefined}
              onAutoOpenDismiss={() => onDismissHumanInputAutoOpen?.(entry.data)}
            />,
            entry,
            index,
            `human-input-${entry.data.id}`,
          ),
        );
        break;
    }
  }

  flushHidden();

  if (isRunning && stoppingLabel) {
    rendered.push(
      <div key="stopping" className="flex justify-start">
        <div className="bg-muted rounded-lg px-3 py-2 text-sm">
          <span className="flex items-center gap-2 text-muted-foreground">
            <Loader2 className="h-3 w-3 animate-spin" aria-hidden />
            {stoppingLabel}
          </span>
        </div>
      </div>
    );
  } else if (isRunning && recoveryActive) {
    rendered.push(
      <div key="recovering" className="flex justify-start">
        <div className="bg-muted rounded-lg px-3 py-2 text-sm">
          <span className="flex items-center gap-2 text-muted-foreground">
            <Loader2 className="h-3 w-3 animate-spin" aria-hidden />
            Resuming after maintenance...
          </span>
        </div>
      </div>
    );
  } else if (isRunning) {
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
  } else if (stoppedLabel) {
    rendered.push(
      <div key="stopped" className="flex justify-start">
        <div className="bg-muted rounded-lg px-3 py-2 text-sm">
          <span className="flex items-center gap-2 text-muted-foreground">
            <Square className="h-2.5 w-2.5 fill-current" aria-hidden />
            {stoppedLabel}
          </span>
        </div>
      </div>
    );
  }

  // Keep the diff summary at the very bottom of the session UI.
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

  return (
    <TooltipProvider delayDuration={200}>
      {rendered}
    </TooltipProvider>
  );
}

function diffStatsEqual(
  a: ChatTimelineProps["diffStats"],
  b: ChatTimelineProps["diffStats"],
): boolean {
  if (a === b) return true;
  if (!a || !b) return !a && !b;
  return a.added === b.added && a.removed === b.removed && a.files_changed === b.files_changed;
}

export const ChatTimeline = memo(ChatTimelineImpl, (prev, next) => {
  return (
    prev.entries === next.entries &&
    prev.isRunning === next.isRunning &&
    prev.recoveryActive === next.recoveryActive &&
    prev.stoppingLabel === next.stoppingLabel &&
    prev.stoppedLabel === next.stoppedLabel &&
    prev.onDiffClick === next.onDiffClick &&
    prev.onApprovePlan === next.onApprovePlan &&
    prev.onAdjustPlan === next.onAdjustPlan &&
    prev.humanInputSubmittingId === next.humanInputSubmittingId &&
    prev.autoOpenHumanInputId === next.autoOpenHumanInputId &&
    prev.humanInputAnswerable === next.humanInputAnswerable &&
    prev.onAnswerHumanInput === next.onAnswerHumanInput &&
    prev.onCancelHumanInput === next.onCancelHumanInput &&
    prev.onDismissHumanInputAutoOpen === next.onDismissHumanInputAutoOpen &&
    prev.getEntryContainerProps === next.getEntryContainerProps &&
    diffStatsEqual(prev.diffStats, next.diffStats)
  );
});
