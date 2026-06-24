import { prMergedAccent } from "@/lib/pr-status-styles";
import { pollMs } from "@/lib/poll-intervals";
import { workingStatusesSet } from "@/lib/session-status-groups";
import type {
  ListResponse,
  PullRequestHealthResponse,
  PullRequestStatus,
  Session,
  SessionDetail,
  SessionLog,
  SessionMessage,
  SessionReviewLoop,
  SessionStatus,
  SessionThread,
  SingleResponse,
  ThreadInboxEvent,
  ThreadRuntimeEvent,
  ThreadStatus,
} from "@/lib/types";

type PendingEditableThreadUpdate = {
  label: string;
  // null clears an existing override; a string sets it. Field is always
  // present on this path so the backend treats omission separately.
  model: string | null;
};

type QueryInvalidator = {
  invalidateQueries: (filters: { queryKey: readonly unknown[] }) => unknown;
};

type DisplayStatusKey = SessionStatus | "pr_merged" | "pr_closed";

export const statusConfig: Record<DisplayStatusKey, { color: string; label: string }> = {
  pending: { color: "bg-muted text-muted-foreground", label: "Pending" },
  running: { color: "bg-primary/10 text-primary", label: "Running" },
  idle: { color: "bg-primary/10 text-primary", label: "Idle" },
  awaiting_input: { color: "bg-warning/10 text-warning", label: "Awaiting input" },
  needs_human_guidance: { color: "bg-attention/10 text-attention", label: "Needs guidance" },
  completed: { color: "bg-success/10 text-success", label: "Completed" },
  pr_created: { color: `${prMergedAccent.bg} ${prMergedAccent.text}`, label: "PR created" },
  pr_merged: { color: `${prMergedAccent.bg} ${prMergedAccent.text}`, label: "PR merged" },
  pr_closed: { color: "bg-muted text-muted-foreground", label: "PR closed" },
  failed: { color: "bg-destructive/10 text-destructive", label: "Failed" },
  cancelled: { color: "bg-muted text-muted-foreground", label: "Cancelled" },
  skipped: { color: "bg-muted text-muted-foreground", label: "Skipped" },
};

export type PendingThreadPreview = Pick<
  SessionThread,
  "id" | "session_id" | "org_id" | "agent_type" | "label" | "status" | "current_turn" | "created_at" | "cost_cents" | "pending_message_count" | "cancel_requested_at" | "model_override" | "inbox_delivery"
>;

const LIVE_LOG_MESSAGE_MAX_BYTES = 32 * 1024;

export function invalidateSessionHumanInputRequests(queryClient: QueryInvalidator, sessionId: string): void {
  queryClient.invalidateQueries({ queryKey: ["session", sessionId, "human-input-requests"] });
}

export function getDisplayStatus(sessionStatus: SessionStatus, prStatus?: PullRequestStatus | null): { color: string; label: string } {
  if (sessionStatus === "pr_created") {
    if (prStatus === "merged") {
      return statusConfig.pr_merged;
    }
    if (prStatus === "closed") {
      return statusConfig.pr_closed;
    }
  }
  return statusConfig[sessionStatus] || statusConfig.pending;
}

export function deriveEffectivePRStatus(prStatus?: PullRequestStatus | null, healthStatus?: PullRequestStatus | null): PullRequestStatus | undefined {
  if (healthStatus === "merged" || prStatus === "merged") {
    return "merged";
  }
  if (healthStatus === "closed" || prStatus === "closed") {
    return "closed";
  }
  return prStatus ?? undefined;
}

export function hasMeaningfulDuration(startedAt?: string, completedAt?: string): boolean {
  if (!startedAt || !completedAt) return false;
  return new Date(completedAt).getTime() - new Date(startedAt).getTime() >= 1000;
}

export function formatDuration(startedAt?: string, completedAt?: string): string {
  if (!startedAt) return "-";
  const start = new Date(startedAt);
  const end = completedAt ? new Date(completedAt) : new Date();
  const diffSecs = Math.floor((end.getTime() - start.getTime()) / 1000);
  if (diffSecs < 60) return `${diffSecs}s`;
  const mins = Math.floor(diffSecs / 60);
  if (mins < 60) return `${mins}m ${diffSecs % 60}s`;
  const hours = Math.floor(mins / 60);
  if (hours < 24) return `${hours}h ${mins % 60}m`;
  const days = Math.floor(hours / 24);
  return `${days}d ${hours % 24}h`;
}

export function getPendingEditableThreadUpdate(
  activeThread: SessionThread | undefined,
  activeThreadIsEditable: boolean,
  composerSelectedModel: string,
): PendingEditableThreadUpdate | undefined {
  if (!activeThread || !activeThreadIsEditable || composerSelectedModel === "") {
    return undefined;
  }
  const existingModel = activeThread.model_override ?? null;
  if (composerSelectedModel === existingModel) {
    return undefined;
  }
  return {
    label: activeThread.label,
    model: composerSelectedModel,
  };
}

export function getInitialComposerSelectedModel(thread: SessionThread): string {
  return thread.model_override ?? "";
}

export function hasCleanReviewLoopForSnapshot(loops: SessionReviewLoop[] | undefined, snapshotKey: string | undefined): boolean {
  if (!snapshotKey) return false;
  return (loops ?? []).some((loop) => loop.status === "clean" && loop.latest_checkpoint_key === snapshotKey);
}

export function getPullRequestHealthRefetchInterval(health: PullRequestHealthResponse | undefined): number | false {
  if (!health || health.sync_status === "blocked") {
    return false;
  }
  const mergeState = health.merge_state;
  const mergeWhenReadyState = health.merge_when_ready?.state;
  return mergeState === "mergeability_pending" || mergeState === "unknown" || mergeWhenReadyState === "queued" || mergeWhenReadyState === "merging" ? pollMs(5_000) : false;
}

function threadStatusForSessionStatus(status: Session["status"]): ThreadStatus | null {
  switch (status) {
    case "pending":
      return "pending";
    case "running":
      return "running";
    case "idle":
      return "idle";
    case "awaiting_input":
      return "awaiting_input";
    case "completed":
    case "pr_created":
      return "completed";
    case "failed":
      return "failed";
    case "cancelled":
      return "cancelled";
    default:
      return null;
  }
}

function reconcileThreadsForOmittedStatusUpdate(
  threads: SessionThread[],
  updated: Session,
): SessionThread[] {
  const threadStatus = threadStatusForSessionStatus(updated.status);
  if (!threadStatus || threadStatus === "running") {
    return threads;
  }

  return threads.map((thread) => {
    if (!workingStatusesSet.has(thread.status)) {
      return thread;
    }

    return {
      ...thread,
      status: threadStatus,
      completed_at: (
        threadStatus === "completed" ||
        threadStatus === "failed" ||
        threadStatus === "cancelled"
      ) ? updated.completed_at ?? thread.completed_at : thread.completed_at,
    };
  });
}

export function mergeSessionDetailStatusUpdate(
  existing: SingleResponse<SessionDetail> | undefined,
  updated: SessionDetail,
): SingleResponse<SessionDetail> {
  if (!existing) {
    return {
      data: {
        ...updated,
        threads: updated.threads ?? [],
      },
    };
  }
  const existingThreads = existing.data.threads ?? [];
  const hasThreadPayload = Array.isArray(updated.threads) && updated.threads.length > 0;
  const threads = hasThreadPayload
    ? updated.threads
    : reconcileThreadsForOmittedStatusUpdate(existingThreads, updated);
  return {
    ...existing,
    data: {
      ...existing.data,
      ...updated,
      threads,
    },
  };
}

export function applyThreadInboxEventToThreads(
  threads: SessionThread[],
  event: ThreadInboxEvent,
): SessionThread[] {
  return threads.map((thread) => (
    thread.id === event.thread_id
      ? { ...thread, pending_message_count: event.pending_message_count }
      : thread
  ));
}

export function applyThreadRuntimeEventToThreads(
  threads: SessionThread[],
  event: ThreadRuntimeEvent,
): SessionThread[] {
  return threads.map((thread) => {
    if (thread.id !== event.thread_id) return thread;
    return {
      ...thread,
      status: event.status,
      agent_session_id: event.agent_session_id ?? thread.agent_session_id,
      current_turn: event.current_turn,
      pending_message_count: event.pending_message_count,
      last_activity_at: event.last_activity_at ?? thread.last_activity_at,
      started_at: event.started_at ?? thread.started_at,
      completed_at: event.completed_at ?? thread.completed_at,
    };
  });
}

export function trackInFlightAgentUpdate(
  ref: { current: Promise<unknown> | null },
  promise: Promise<unknown>,
): void {
  ref.current = promise;
  promise.catch(() => undefined).then(() => {
    if (ref.current === promise) {
      ref.current = null;
    }
  });
}

function threadMatchesPendingPreview(thread: SessionThread, pending: PendingThreadPreview): boolean {
  return thread.id === pending.id || (
    thread.session_id === pending.session_id &&
    thread.agent_type === pending.agent_type &&
    thread.label === pending.label &&
    (thread.model_override ?? null) === (pending.model_override ?? null) &&
    new Date(thread.created_at) >= new Date(pending.created_at)
  );
}

export function buildChromeThreads(
  threads: SessionThread[],
  pendingThreadPreview: PendingThreadPreview | null,
): SessionThread[] {
  if (!pendingThreadPreview) {
    return threads;
  }
  if (threads.some((thread) => threadMatchesPendingPreview(thread, pendingThreadPreview))) {
    return threads;
  }
  return [...threads, pendingThreadPreview];
}

export function mergePendingMessages(
  baseMessages: SessionMessage[],
  pendingMessages: SessionMessage[],
): SessionMessage[] {
  if (pendingMessages.length === 0) {
    return baseMessages;
  }

  const merged = [...baseMessages];
  const seenIDs = new Set(baseMessages.map((message) => message.id));
  const seenKeys = new Set(baseMessages.map(messageReconciliationKey));
  for (const message of pendingMessages) {
    if (seenIDs.has(message.id) || seenKeys.has(messageReconciliationKey(message))) {
      continue;
    }
    merged.push(message);
    seenIDs.add(message.id);
    seenKeys.add(messageReconciliationKey(message));
  }
  return merged;
}

export function messageReconciliationKey(message: SessionMessage): string {
  return JSON.stringify({
    session_id: message.session_id,
    thread_id: message.thread_id ?? null,
    turn_number: message.turn_number,
    role: message.role,
    content: message.content,
    attachments: message.attachments ?? [],
    references: message.references ?? [],
    commands: message.commands ?? [],
  });
}

export function mergeSessionLogListResponse(
  existing: ListResponse<SessionLog> | undefined,
  incoming: SessionLog[],
  maxItems?: number,
): ListResponse<SessionLog> {
  const byID = new Map<number, SessionLog>();
  for (const log of existing?.data ?? []) {
    byID.set(log.id, log);
  }
  for (const log of incoming) {
    byID.set(log.id, log);
  }
  let data = Array.from(byID.values()).sort((a, b) => a.id - b.id);
  if (maxItems !== undefined && maxItems > 0 && data.length > maxItems) {
    data = data.slice(data.length - maxItems);
  }
  return {
    data,
    meta: existing?.meta ?? {},
  };
}

function truncateUTF8Bytes(value: string, maxBytes: number): string {
  const encoder = new TextEncoder();
  const bytes = encoder.encode(value);
  if (bytes.byteLength <= maxBytes) return value;

  const decoder = new TextDecoder("utf-8", { fatal: true });
  for (let end = maxBytes; end > 0; end -= 1) {
    try {
      return decoder.decode(bytes.slice(0, end));
    } catch {
      continue;
    }
  }
  return "";
}

export function capLiveSessionLogMessage(log: SessionLog): SessionLog {
  const originalBytes = new TextEncoder().encode(log.message).byteLength;
  if (originalBytes <= LIVE_LOG_MESSAGE_MAX_BYTES) {
    return {
      ...log,
      message_bytes: log.message_bytes ?? originalBytes,
      message_chars: log.message_chars ?? Array.from(log.message).length,
      message_truncated: log.message_truncated ?? false,
    };
  }

  return {
    ...log,
    message: truncateUTF8Bytes(log.message, LIVE_LOG_MESSAGE_MAX_BYTES),
    message_bytes: log.message_bytes ?? originalBytes,
    message_chars: log.message_chars ?? Array.from(log.message).length,
    message_truncated: true,
  };
}

export function liveLogsForTimeline(includeLiveLogs: boolean, logs: SessionLog[]): SessionLog[] {
  return includeLiveLogs ? logs : [];
}
