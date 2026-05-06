"use client";

import { memo, useCallback, useDeferredValue, useEffect, useMemo, useRef, useState } from "react";
import dynamic from "next/dynamic";
import { useRouter } from "next/navigation";
import { useQueryState } from "nuqs";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import {
  AlertTriangle,
  ArrowDown,
  ArrowUp,
  ClipboardList,
  ExternalLink,
  FileCode2,
  FolderTree,
  GitPullRequest,
  Loader2,
  RefreshCw,
  CheckCircle2,
  Check,
  XCircle,
  X,
  MinusCircle,
  Square,
  PanelRightOpen,
  PanelRightClose,
  PanelBottomOpen,
  Clock,
  MessageSquare,
  Pencil,
} from "lucide-react";
import { LinearIcon } from "@/components/linear-icon";
import { looksLikeLinearRef } from "@/lib/linear-refs";
import { getClipboardFiles } from "@/lib/clipboard-files";
import { notify as toast } from "@/lib/notify";
import { Badge } from "@/components/ui/badge";
import { MarkdownContent } from "@/components/markdown";
import { Button } from "@/components/ui/button";
import { DisabledTooltip } from "@/components/ui/disabled-tooltip";
import { ErrorNotice } from "@/components/ui/error-notice";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, TableHeader, TableBody, TableRow, TableHead, TableCell } from "@/components/ui/table";
import { Tabs, TabsList, TabsTrigger, TabsContent } from "@/components/ui/tabs";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { ChatTimeline } from "@/components/chat-timeline";
import { SessionComposerAttachmentMenu } from "@/components/session-composer-attachment-menu";
import { SessionComposerTriggerPicker, flattenGroups, type TriggerPickerGroup, type TriggerPickerPosition } from "@/components/session-composer-trigger-picker";
import { useSessionComposerSlashCommands } from "@/hooks/use-session-composer-slash-commands";
import {
  COMPOSER_TRIGGER_SPECS,
  findActiveTrigger,
  insertCommandAtCaret,
  insertMentionAtCaret,
  removeCommandReference,
  removeMentionReference,
  syncCommandsWithMessage,
  syncReferencesWithMessage,
} from "@/lib/session-composer-mentions";
import { queryKeys } from "@/lib/query-keys";
import { api, ApiError } from "@/lib/api";
import { AGENTS, AGENTS_BY_KEY } from "@/lib/agents";
import { getActiveOrgId } from "@/lib/active-org";
import { maybeNotifySessionCompleted } from "@/lib/browser-notifications";
import {
  SSE_EVENT,
  addSSEListener,
  buildPullRequestStreamURL,
  buildSessionLogsStreamURL,
} from "@/lib/sse";
import { applyPlanModePrefix, buildTimeline, flattenTimelineResponse, sortTimelineEntries, type TimelineEntry } from "@/lib/timeline";
import { parseDiffStats, type DiffFile } from "@/lib/diff-parser";
import { formatReviewMessage } from "@/lib/format-review-message";
import {
  readStoredSessionScrollPosition,
  resolveInitialSessionAnchor,
  writeStoredSessionScrollPosition,
} from "@/lib/session-open-position";
import type { ListResponse, Session, SessionDetail, SessionInputCommand, SessionInputReference, SessionLog, SessionMessage, SessionReviewComment, SessionThread, SessionThreadFileEvent, SessionTimelineEntry, User, Validation, CodexAuthStatus, PullRequestHealthResponse, SingleResponse } from "@/lib/types";
import { AgentTabStrip, computeThreadOverlap } from "./agent-tab-strip";
import {
  ThreadAttributionFilter,
  useAttributionAllowedPaths,
  type ThreadAttributionFilterValue,
} from "./thread-attribution-filter";
import { AuditLogTrigger } from "@/components/audit/audit-log-trigger";
import { ResizeHandle } from "@/components/resize-handle";
import { DiffStatsBadge, FileTree, CommentsSummary, PassSelector, type DiffPassEntry, type PassRange } from "@/components/code-review";
import { LinkedIssueChips } from "./linked-issue-chips";
import { useReviewComments } from "@/hooks/use-review-comments";
import { useDiffViewState } from "@/hooks/use-diff-view-state";
import { CodexDeviceCodeModal } from "@/components/codex-device-code-modal";
import { AgentBadge } from "@/components/agent-badge";
import { PendingAttachmentStrip } from "@/components/pending-attachment-strip";
import { PRHealthBanner } from "@/components/pr-health-banner";
import { MobileBackButton } from "@/components/mobile-back-button";
import { useAuth } from "@/hooks/use-auth";
import { useMediaQuery } from "@/hooks/use-media-query";
import { useDocumentVisible } from "@/hooks/use-document-visible";
import { prMergedAccent } from "@/lib/pr-status-styles";
import { cn, sessionTitle, formatTimeAgo } from "@/lib/utils";
import { activeSet, workingStatusesSet } from "@/lib/session-status-groups";

// Defer the diff viewer (shiki + diff-parser) until the user actually opens
// review mode. Saves ~100KB+ from the initial session-detail bundle for the
// common case of just chatting with the agent.
const ReviewDiffView = dynamic(
  () => import("@/components/code-review/review-diff-view").then((m) => ({ default: m.ReviewDiffView })),
  {
    ssr: false,
    loading: () => <div className="h-full w-full bg-muted/20 animate-pulse rounded-lg" />,
  },
);

// Defer the preview panel (iframe wrapper + visual editing tooling) until
// the user actually opens the preview tab.
const PreviewPanel = dynamic(
  () => import("@/components/preview/preview-panel").then((m) => ({ default: m.PreviewPanel })),
  {
    ssr: false,
    loading: () => <div className="h-full w-full bg-muted/20 animate-pulse rounded-lg" />,
  },
);

const PREVIEW_ORIGIN_TEMPLATE =
  process.env.NEXT_PUBLIC_PREVIEW_ORIGIN_TEMPLATE ||
  "http://{id}.preview.localhost:9090";

const FAILURE_CATEGORY_CODEX_AUTH = "codex_auth_expired";
const PR_ERROR_TOAST_DURATION_MS = 10_000;
const PR_ERROR_TOAST_MESSAGE = "PR creation failed";
const MAX_RESOLVE_REVIEW_COMMENTS_PER_MESSAGE = 50;

const EDITABLE_THREAD_AGENTS: ReadonlyArray<{ key: string; label: string }> =
  AGENTS.map((agent) => ({ key: agent.key, label: agent.label }));

type PendingFollowUpMessage = SessionMessage & {
  client_id: number;
};

type PendingEditableThreadUpdate = {
  label: string;
  // null clears an existing override; a string sets it. Field is always
  // present on this path so the backend treats omission separately.
  model: string | null;
};

const statusConfig: Record<string, { color: string; label: string }> = {
  pending: { color: "bg-muted text-muted-foreground", label: "Pending" },
  running: { color: "bg-primary/10 text-primary", label: "Running" },
  idle: { color: "bg-primary/10 text-primary", label: "Idle" },
  awaiting_input: { color: "bg-amber-50 text-amber-700 dark:bg-amber-950/30 dark:text-amber-400", label: "Awaiting input" },
  needs_human_guidance: { color: "bg-orange-50 text-orange-700 dark:bg-orange-950/30 dark:text-orange-400", label: "Needs guidance" },
  completed: { color: "bg-emerald-50 text-emerald-700 dark:bg-emerald-950/30 dark:text-emerald-400", label: "Completed" },
  pr_created: { color: "bg-emerald-50 text-emerald-700 dark:bg-emerald-950/30 dark:text-emerald-400", label: "PR created" },
  failed: { color: "bg-destructive/10 text-destructive", label: "Failed" },
  cancelled: { color: "bg-muted text-muted-foreground", label: "Cancelled" },
  skipped: { color: "bg-muted text-muted-foreground", label: "Skipped" },
};

function hasMeaningfulDuration(startedAt?: string, completedAt?: string): boolean {
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

const triggerPickerIconClassName = "h-4 w-4 shrink-0";
const directoryTriggerIcon = <FolderTree className={triggerPickerIconClassName} />;
const fileTriggerIcon = <FileCode2 className={triggerPickerIconClassName} />;

const validationChecks: { key: string; label: string }[] = [
  { key: "direction_check", label: "Direction check" },
  { key: "correctness_check", label: "Correctness check" },
  { key: "quality_check", label: "Quality check" },
  { key: "security_scan", label: "Security scan" },
  { key: "regression_test_check", label: "Regression test check" },
  { key: "ci_check", label: "CI check" },
];

function checkResultBadge(result: string | null) {
  if (!result) return <Badge variant="secondary" className="text-xs">skipped</Badge>;
  if (result === "pass") return <Badge variant="secondary" className="bg-emerald-50 text-emerald-700 dark:bg-emerald-950/30 dark:text-emerald-400 border-emerald-200/50 dark:border-emerald-800/30 text-xs">pass</Badge>;
  if (result === "fail") return <Badge variant="secondary" className="bg-destructive/10 text-destructive border-destructive/20 text-xs">fail</Badge>;
  return <Badge variant="secondary" className="text-xs">{result}</Badge>;
}

// AgentThreadTabs lived here in Phase 1. The replacement now lives in
// agent-tab-strip.tsx (AgentTabStrip) and adds overlap badges, tab actions,
// and cost surfacing. Status helpers moved with it; the statusConfig table
// is still owned by this file and passed in as a prop.

// ---------------------------------------------------------------------------
// Detail panel tabs (shown in right sidebar)
// ---------------------------------------------------------------------------

type DetailTab = "overview" | "changes" | "validation" | "preview";
type PRAuthorMode = "auto" | "user" | "app";

type PRAuthInterceptDetails = {
  connect_url: string;
  resume_token: string;
  can_fallback_to_app: boolean;
};

// PRAuthPromptState is a discriminated union so merge prompts don't carry
// create-PR-only fields (connect_url, resume_token, can_fallback_to_app).
// create_pr and push_changes prompts come from the backend's auth interception
// payload; merge prompts are always synthesized client-side and connect via
// the hardcoded /github/connect endpoint with no resume token and no fallback.
type PRAuthPromptState =
  | ({ purpose: "create_pr" } & PRAuthInterceptDetails)
  | ({ purpose: "push_changes" } & PRAuthInterceptDetails)
  | { purpose: "merge_pr" };

type PRActionErrorState = {
  code?: string;
  message: string;
};

const terminalSessionStatuses = new Set(["completed", "pr_created", "failed", "cancelled", "skipped"]);
const SNAPSHOT_EXPIRED_PR_MESSAGE =
  "This session snapshot expired before a PR could be created. Send a new message to rebuild the sandbox, then create the PR again.";

function mergePendingMessages(
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

function messageReconciliationKey(message: SessionMessage): string {
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
const SNAPSHOT_NOT_CAPTURED_PR_MESSAGE =
  "This session finished without saving a reusable checkpoint for PR creation. Send a new message to rebuild the sandbox, then create the PR again.";
const SNAPSHOT_UNAVAILABLE_PR_MESSAGE =
  "This session had a saved checkpoint, but it is no longer available in storage. Send a new message to rebuild the sandbox, then create the PR again.";

type PRSnapshotState = "expired" | "not_captured" | "unavailable";

function isPRAuthInterceptDetails(value: unknown): value is PRAuthInterceptDetails {
  if (!value || typeof value !== "object") return false;
  const details = value as Partial<PRAuthInterceptDetails>;
  return typeof details.connect_url === "string" &&
    typeof details.resume_token === "string" &&
    typeof details.can_fallback_to_app === "boolean";
}

function classifyPRSnapshotState({
  sessionSnapshotKey,
  sessionSandboxState,
  serverMessage,
  localCode,
  allowImplicitMissingSnapshot = false,
}: {
  sessionSnapshotKey?: string | null;
  sessionSandboxState?: string | null;
  serverMessage?: string | null;
  localCode?: string;
  allowImplicitMissingSnapshot?: boolean;
}): PRSnapshotState | null {
  if (localCode === "SNAPSHOT_EXPIRED") return "expired";
  if (localCode === "SNAPSHOT_NOT_CAPTURED") return "not_captured";
  if (localCode === "SNAPSHOT_UNAVAILABLE") return "unavailable";
  if (serverMessage === SNAPSHOT_EXPIRED_PR_MESSAGE) return "expired";
  if (serverMessage === SNAPSHOT_NOT_CAPTURED_PR_MESSAGE) return "not_captured";
  if (serverMessage === SNAPSHOT_UNAVAILABLE_PR_MESSAGE) return "unavailable";
  if (/^session state expired\b/i.test(serverMessage || "")) return "unavailable";
  if (!sessionSnapshotKey) {
    if (!allowImplicitMissingSnapshot) return null;
    return sessionSandboxState === "destroyed" ? "expired" : "not_captured";
  }
  return null;
}

function snapshotPRMessage(state: PRSnapshotState | null, message?: string | null): string {
  if (message && !/^session state expired\b/i.test(message)) {
    return message;
  }
  switch (state) {
    case "expired":
      return SNAPSHOT_EXPIRED_PR_MESSAGE;
    case "not_captured":
      return SNAPSHOT_NOT_CAPTURED_PR_MESSAGE;
    case "unavailable":
      return SNAPSHOT_UNAVAILABLE_PR_MESSAGE;
    default:
      return SNAPSHOT_UNAVAILABLE_PR_MESSAGE;
  }
}

function prErrorTitle(snapshotState: PRSnapshotState | null, errorCode?: string): string {
  if (snapshotState === "expired" || errorCode === "SNAPSHOT_EXPIRED") {
    return "Session snapshot expired";
  }
  if (snapshotState === "not_captured" || errorCode === "SNAPSHOT_NOT_CAPTURED") {
    return "No reusable checkpoint saved";
  }
  if (snapshotState === "unavailable" || errorCode === "SNAPSHOT_UNAVAILABLE") {
    return "Saved checkpoint unavailable";
  }
  if (errorCode === "PR_RESUME_EXPIRED") {
    return "Couldn't resume PR creation";
  }
  return "Couldn't create the PR";
}

function OverviewTab({ session, members }: { session: Session; members: User[] }) {
  const queryClient = useQueryClient();
  const [showDeviceCodeModal, setShowDeviceCodeModal] = useState(false);

  const isCodexAuthFailure = session.failure_category === FAILURE_CATEGORY_CODEX_AUTH;

  const { data: codexAuthResponse } = useQuery<SingleResponse<CodexAuthStatus>>({
    queryKey: ["codex-auth-status", "personal"],
    queryFn: () => api.codexAuth.status(undefined, "personal"),
    enabled: isCodexAuthFailure,
  });
  const isCodexAuthenticated = codexAuthResponse?.data?.status === "completed";

  const retryMutation = useMutation({
    mutationFn: () => api.sessions.retry(session.id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["session", session.id] });
    },
  });

  const status = statusConfig[session.status] || statusConfig.pending;
  const isActive = !terminalSessionStatuses.has(session.status);

  const triggeredByMember = session.triggered_by_user_id
    ? members.find((m) => m.id === session.triggered_by_user_id)
    : undefined;
  const triggeredByLabel = session.pm_plan_id && !session.triggered_by_user_id
    ? "PM Agent"
    : session.triggered_by_user_id
      ? triggeredByMember?.name || triggeredByMember?.github_login || "Unknown user"
      : "System";

  return (
    <div className="space-y-4">
      {/* Result card — most important for completed sessions, shown first */}
      {session.result_summary && (
        <Card className="border-l-2 border-l-emerald-500 bg-emerald-50/30 dark:bg-emerald-950/10">
          <CardHeader className="pb-2">
            <CardTitle className="text-xs flex items-center gap-2">
              <CheckCircle2 className="h-3.5 w-3.5 text-emerald-600 dark:text-emerald-400" />
              Result
            </CardTitle>
          </CardHeader>
          <CardContent>
            <MarkdownContent content={session.result_summary} className="text-xs" />
          </CardContent>
        </Card>
      )}

      {/* Failure card — shown prominently at top for failed sessions */}
      {session.status === "failed" && (session.failure_explanation || session.error) && (
        <Card className="border-l-2 border-l-destructive border-destructive/20 dark:border-destructive/30">
          <CardHeader className="pb-0">
            <div className="flex items-center justify-between">
              <CardTitle className="text-xs text-destructive flex items-center gap-2">
                <XCircle className="h-3.5 w-3.5" />
                Failure details
                {session.failure_category && (
                  <Badge variant="secondary" className="bg-destructive/10 text-destructive border-destructive/20 text-xs">
                    {session.failure_category}
                  </Badge>
                )}
              </CardTitle>
              {session.failure_retry_advised && (
                <DisabledTooltip disabled={retryMutation.isPending} content="Retrying session...">
                  <Button
                    size="xs"
                    variant="outline"
                    onClick={() => retryMutation.mutate()}
                    disabled={retryMutation.isPending}
                  >
                    <RefreshCw className={`mr-1.5 h-3 w-3 ${retryMutation.isPending ? "animate-spin" : ""}`} />
                    {retryMutation.isPending ? "Retrying..." : "Retry"}
                  </Button>
                </DisabledTooltip>
              )}
            </div>
          </CardHeader>
          <CardContent className="pt-0 space-y-3">
            <p className="text-xs break-words">{session.failure_explanation || session.error}</p>
            {/* Show next steps only for non-codex-auth failures (codex auth has the reauth button instead) */}
            {!isCodexAuthFailure && session.failure_next_steps && session.failure_next_steps.length > 0 && (
              <div>
                <p className="text-xs font-medium text-muted-foreground mb-1">Next steps</p>
                <ul className="list-disc list-inside text-xs space-y-1">
                  {session.failure_next_steps.map((step, i) => (
                    <li key={i}>{step}</li>
                  ))}
                </ul>
              </div>
            )}
            {isCodexAuthFailure && !isCodexAuthenticated && (
              <Button
                size="sm"
                variant="outline"
                className="mt-1"
                onClick={() => setShowDeviceCodeModal(true)}
              >
                Re-authenticate with ChatGPT
              </Button>
            )}
            {isCodexAuthFailure && isCodexAuthenticated && (
              <p className="text-xs text-emerald-600 dark:text-emerald-400 flex items-center gap-1.5">
                <CheckCircle2 className="h-3.5 w-3.5" />
                ChatGPT connected — click Retry to re-run this session.
              </p>
            )}
          </CardContent>
        </Card>
      )}
      {showDeviceCodeModal && (
        <CodexDeviceCodeModal
          scope="personal"
          onClose={() => setShowDeviceCodeModal(false)}
          onConnected={() => {
            setShowDeviceCodeModal(false);
            queryClient.invalidateQueries({ queryKey: ["codex-auth-status"] });
          }}
        />
      )}

      {/* Session vitals — identity row (status + agent + who triggered) */}
      <div className="space-y-2">
        <div className="flex items-center gap-x-3 gap-y-1 flex-wrap text-xs">
          <span className={`inline-flex items-center rounded-full px-2 py-0.5 font-medium ${status.color}`}>
            {isActive && (
              <span className="relative mr-1.5 flex h-2 w-2">
                <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-blue-400 opacity-75" />
                <span className="relative inline-flex rounded-full h-2 w-2 bg-blue-500" />
              </span>
            )}
            {status.label}
          </span>
          <span className="inline-flex items-center gap-x-1.5 text-muted-foreground">
            <AgentBadge agentType={session.agent_type} labelClassName="text-xs" />
            <span aria-hidden="true" className="text-muted-foreground/50">·</span>
            <span>{triggeredByLabel}</span>
          </span>
        </div>

        {/* Timestamps + audit — secondary reference data, single unified row */}
        <div className="flex items-center gap-x-1.5 gap-y-1 flex-wrap text-xs text-muted-foreground">
          {terminalSessionStatuses.has(session.status) &&
            !((session.status === "failed" || session.status === "cancelled") &&
              !hasMeaningfulDuration(session.started_at, session.completed_at)) && (
            <>
              <span>{formatDuration(session.started_at, session.completed_at)}</span>
              <span aria-hidden="true" className="text-muted-foreground/50">·</span>
            </>
          )}
          <span>
            {!isActive && session.completed_at ? (
              session.status === "failed"
                ? <>Failed {formatTimeAgo(session.completed_at)}</>
                : session.status === "cancelled"
                  ? <>Cancelled {formatTimeAgo(session.completed_at)}</>
                  : <>Completed {formatTimeAgo(session.completed_at)}</>
            ) : session.started_at ? (
              <>Started {formatTimeAgo(session.started_at)}</>
            ) : (
              <>Queued {formatTimeAgo(session.created_at)}</>
            )}
          </span>
          <AuditLogTrigger
            filters={{ session_id: session.id }}
            members={members}
            title="Session activity"
            variant="inline"
          />
        </div>
      </div>

      {/* PM context */}
      {session.pm_plan_id && (session.pm_reasoning || session.pm_approach) && (
        <Card>
          <CardHeader className="pb-2">
            <CardTitle className="text-xs">PM context</CardTitle>
          </CardHeader>
          <CardContent className="space-y-3 text-xs">
            {session.pm_reasoning && (
              <div>
                <p className="text-xs font-medium text-muted-foreground">Why this was prioritized</p>
                <p className="break-words">{session.pm_reasoning}</p>
              </div>
            )}
            {session.pm_approach && (
              <div>
                <p className="text-xs font-medium text-muted-foreground">Suggested approach</p>
                <p className="break-words">{session.pm_approach}</p>
              </div>
            )}
          </CardContent>
        </Card>
      )}

    </div>
  );
}

function ValidationTab({ sessionId }: { sessionId: string }) {
  const { data, isLoading, error } = useQuery({
    queryKey: ["session", sessionId, "validation"],
    queryFn: () => api.sessions.getValidation(sessionId).catch((err) => {
      // 404 means no validation exists yet — treat as empty data, not an error.
      if (err?.code === "NOT_FOUND") return { data: null };
      throw err;
    }),
  });

  if (isLoading) {
    return (
      <div className="flex items-center justify-center py-12">
        <div className="text-center space-y-2">
          <Loader2 className="h-5 w-5 animate-spin text-muted-foreground/40 mx-auto" />
          <p className="text-xs text-muted-foreground">Loading validation...</p>
        </div>
      </div>
    );
  }

  const validation = data?.data;
  if (error || !validation) {
    return (
      <div className="flex items-center justify-center py-12">
        <div className="text-center space-y-2 max-w-[280px]">
          <CheckCircle2 className="h-8 w-8 text-muted-foreground/40 mx-auto" />
          <p className="text-xs font-medium text-muted-foreground">No validation data</p>
          <p className="text-xs text-muted-foreground/60">Validation checks will appear here once the session produces results.</p>
        </div>
      </div>
    );
  }

  const overallStatus = validation.status;

  return (
    <div className="space-y-4">
      <div className="flex items-center gap-2">
        <span className="text-xs font-medium">Overall:</span>
        {overallStatus === "passed" && (
          <Badge variant="secondary" className="bg-emerald-50 text-emerald-700 dark:bg-emerald-950/30 dark:text-emerald-400 border-emerald-200/50 dark:border-emerald-800/30">
            <CheckCircle2 className="mr-1 h-3 w-3" /> Passed
          </Badge>
        )}
        {overallStatus === "failed" && (
          <Badge variant="secondary" className="bg-destructive/10 text-destructive border-destructive/20">
            <XCircle className="mr-1 h-3 w-3" /> Failed
          </Badge>
        )}
        {overallStatus !== "passed" && overallStatus !== "failed" && (
          <Badge variant="secondary">
            <MinusCircle className="mr-1 h-3 w-3" /> {overallStatus}
          </Badge>
        )}
      </div>

      <Card>
        <CardContent className="p-0">
          <Table>
            <TableHeader>
              <TableRow className="bg-muted/20">
                <TableHead>Check</TableHead>
                <TableHead>Result</TableHead>
                <TableHead>Details</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {validationChecks.map(({ key, label }) => {
                const result = validation[key as keyof Validation] as string | null;
                const details = validation[`${key}_details` as keyof Validation] as string | null;
                return (
                  <TableRow key={key}>
                    <TableCell className="font-medium">{label}</TableCell>
                    <TableCell>{checkResultBadge(result)}</TableCell>
                    <TableCell className="text-muted-foreground">{details || "-"}</TableCell>
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
        </CardContent>
      </Card>
    </div>
  );
}

function ChangesTab({
  filteredFiles,
  activeFileIndex,
  onFileSelect,
  onOpenReview,
  comments,
  onCommentClick,
  passes,
  passRange,
  onPassRangeChange,
  emptyStatusText,
  isMobile,
  threads,
  attributionFilter,
  onAttributionFilterChange,
}: {
  filteredFiles: DiffFile[];
  activeFileIndex: number;
  onFileSelect: (index: number) => void;
  onOpenReview: (fileIndex?: number) => void;
  comments: SessionReviewComment[];
  onCommentClick: (filePath: string) => void;
  passes: DiffPassEntry[];
  passRange: PassRange | null;
  onPassRangeChange: (range: PassRange | null) => void;
  emptyStatusText: string;
  isMobile: boolean;
  threads: SessionThread[];
  attributionFilter: ThreadAttributionFilterValue;
  onAttributionFilterChange: (next: ThreadAttributionFilterValue) => void;
}) {
  const hasDiff = filteredFiles.length > 0;

  const handleFileClick = useCallback(
    (index: number) => {
      onFileSelect(index);
      onOpenReview(index);
    },
    [onFileSelect, onOpenReview]
  );

  return (
    <div className="flex flex-col h-full">
      {/* Pass selector */}
      {passes.length >= 2 && (
        <div className="px-4 py-3 border-b border-border">
          <PassSelector
            passes={passes}
            selectedRange={passRange}
            onRangeChange={onPassRangeChange}
          />
        </div>
      )}

      {/* Tab attribution filter — visible only when the session has more
          than one tab. Lets the user scope the diff to one tab's outputs,
          the overlap, or unattributed paths. */}
      {threads.length > 1 && (
        <div className="flex items-center justify-end gap-2 px-4 py-2 border-b border-border">
          <span className="text-xs text-muted-foreground">Filter by tab:</span>
          <ThreadAttributionFilter
            threads={threads}
            value={attributionFilter}
            onChange={onAttributionFilterChange}
          />
        </div>
      )}

      {/* Comments summary */}
      {comments.length > 0 && (
        <CommentsSummary
          comments={comments}
          onCommentClick={onCommentClick}
        />
      )}

      {/* Main content: file tree or empty state */}
      {hasDiff ? (
        <div className="flex flex-col flex-1 min-h-0">
          {!isMobile ? (
            <div className="px-4 py-3">
              <button
                onClick={() => onOpenReview()}
                className="w-full flex items-center justify-center gap-2 px-3 py-2 rounded-md border border-border bg-background text-xs font-medium text-foreground hover:bg-muted/50 transition-colors"
              >
                <FileCode2 className="h-3.5 w-3.5" />
                Review {filteredFiles.length} {filteredFiles.length === 1 ? "file" : "files"}
              </button>
            </div>
          ) : null}
          <div className="flex-1 overflow-hidden">
            <FileTree
              files={filteredFiles}
              activeFileIndex={activeFileIndex}
              onFileSelect={handleFileClick}
              variant={isMobile ? "sheet" : "sidebar"}
            />
          </div>
        </div>
      ) : (
        <div className="flex-1 flex items-center justify-center py-12">
          <div className="text-center space-y-2 max-w-[280px]">
            <FileCode2 className="h-8 w-8 text-muted-foreground/40 mx-auto" />
            <p className="text-xs font-medium text-muted-foreground">
              No changes yet
            </p>
            <p className="text-xs text-muted-foreground/60">
              {emptyStatusText}
            </p>
          </div>
        </div>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Shared session composer (used in both chat and review mode)
// ---------------------------------------------------------------------------

function SessionComposer({
  message,
  onMessageChange,
  planMode,
  onPlanModeChange,
  selectedModel,
  onSelectedModelChange,
  attachments,
  isUploading,
  onUpload,
  onPasteFiles,
  onAddAttachment,
  onRemoveAttachment,
  openComments,
  availableModels,
  canSendMessage,
  isRunning,
  isSnapshotExpired,
  isClaudeCode,
  sendPending,
  sendError,
  cancelPending,
  uploadError,
  onCancelSession,
  onSend,
  textareaRef,
  uploadInputRef,
  references,
  onReferencesChange,
  commands,
  onCommandsChange,
  repositoryId,
  branch,
  agentType,
  editableAgentType,
  editableAgents,
  onEditableAgentTypeChange,
  agentUpdatePending,
  targetLabel,
}: {
  message: string;
  onMessageChange: (value: string) => void;
  planMode: boolean;
  onPlanModeChange: (value: boolean) => void;
  selectedModel: string;
  onSelectedModelChange: (value: string) => void;
  attachments: string[];
  isUploading: boolean;
  onUpload: (event: React.ChangeEvent<HTMLInputElement>) => void;
  onPasteFiles: (files: File[]) => Promise<void>;
  onAddAttachment: (url: string) => void;
  onRemoveAttachment: (url: string) => void;
  openComments: SessionReviewComment[];
  availableModels: readonly string[];
  canSendMessage: boolean;
  isRunning: boolean;
  isSnapshotExpired: boolean;
  isClaudeCode: boolean;
  sendPending: boolean;
  sendError: unknown;
  cancelPending: boolean;
  uploadError: string | null;
  onCancelSession: () => void;
  onSend: () => void;
  textareaRef: { current: HTMLTextAreaElement | null };
  uploadInputRef: { current: HTMLInputElement | null };
  references: SessionInputReference[];
  onReferencesChange: (next: SessionInputReference[]) => void;
  commands: SessionInputCommand[];
  onCommandsChange: (next: SessionInputCommand[]) => void;
  repositoryId?: string;
  branch?: string;
  agentType: string;
  editableAgentType?: string;
  editableAgents?: readonly { key: string; label: string }[];
  onEditableAgentTypeChange?: (nextAgentType: string) => void;
  agentUpdatePending: boolean;
  targetLabel?: string;
}) {
  const isMobile = useMediaQuery("(max-width: 767px)");
  const [isTextareaFocused, setIsTextareaFocused] = useState(false);
  const mobileComposerExpanded = !isMobile
    || isTextareaFocused
    || message.length > 0
    || attachments.length > 0
    || openComments.length > 0
    || references.length > 0
    || commands.length > 0;

  useEffect(() => {
    const el = textareaRef.current;
    if (!el) return;
    if (!mobileComposerExpanded) {
      el.style.height = "44px";
      return;
    }
    el.style.height = "auto";
    el.style.height = `${Math.min(el.scrollHeight, 200)}px`;
  }, [message, textareaRef, mobileComposerExpanded]);

  const composerCardRef = useRef<HTMLDivElement>(null);
  const composerInputSurfaceRef = useRef<HTMLDivElement>(null);
  const linearInputRef = useRef<HTMLInputElement>(null);
  const [caretPosition, setCaretPosition] = useState(message.length);
  const [selectedTriggerIndex, setSelectedTriggerIndex] = useState(0);
  const [triggerDismissed, setTriggerDismissed] = useState(false);
  const [pickerPosition, setPickerPosition] = useState<TriggerPickerPosition | null>(null);
  const [mobileSettingsOpen, setMobileSettingsOpen] = useState(false);
  const [showImageInput, setShowImageInput] = useState(false);
  const [imageURL, setImageURL] = useState("");
  const [showLinearInput, setShowLinearInput] = useState(false);
  const [linearInput, setLinearInput] = useState("");
  const [linearInputError, setLinearInputError] = useState<string | null>(null);

  // Focus the Linear input the render after it mounts. Using a layout
  // effect (rather than the previous requestAnimationFrame inside the menu
  // item's onClick) guarantees the input is in the DOM before we focus —
  // the rAF version raced React's commit and silently dropped focus on the
  // first open in tight render budgets.
  useEffect(() => {
    if (showLinearInput) {
      linearInputRef.current?.focus();
    }
  }, [showLinearInput]);

  function addImageURL() {
    const trimmed = imageURL.trim();
    if (!trimmed) {
      return;
    }
    onAddAttachment(trimmed);
    setImageURL("");
    setShowImageInput(false);
    requestAnimationFrame(() => {
      textareaRef.current?.focus();
    });
  }

  function addLinearLink() {
    const trimmed = linearInput.trim();
    if (!trimmed) {
      return;
    }
    if (!looksLikeLinearRef(trimmed)) {
      // Block obvious garbage at submit time. The backend re-validates with
      // the org's team-key allowlist; this is a UX hint, not a security
      // boundary, so the regex matches detect.go's lax shape and we leave
      // the input open so the user can correct it.
      setLinearInputError("Enter a Linear URL (https://linear.app/...) or key like ACS-1234");
      return;
    }
    // Append the trimmed ref to the message body. SendMessage hands the body
    // to ResolveAndLinkMidSession which scans it for Linear refs and creates
    // the session_issue_link row asynchronously, so plain-text append is
    // exactly what the backend expects.
    const next = message.length === 0
      ? trimmed
      : `${message}${message.endsWith(" ") || message.endsWith("\n") ? "" : " "}${trimmed}`;
    onMessageChange(next);
    setLinearInput("");
    setLinearInputError(null);
    setShowLinearInput(false);
    requestAnimationFrame(() => {
      const el = textareaRef.current;
      if (!el) return;
      el.focus();
      el.setSelectionRange(next.length, next.length);
    });
  }

  const activeTrigger = useMemo(
    () => findActiveTrigger(message, caretPosition, COMPOSER_TRIGGER_SPECS),
    [message, caretPosition],
  );
  const activeMention = activeTrigger?.trigger === "@" ? activeTrigger : null;
  const activeCommand = activeTrigger?.trigger === "/" ? activeTrigger : null;
  const deferredMentionQuery = useDeferredValue(activeMention?.query ?? "");
  const deferredCommandQuery = useDeferredValue(activeCommand?.query ?? "");
  const triggerQueryKey = `${activeTrigger?.trigger ?? ""}:${repositoryId ?? ""}:${branch ?? ""}:${activeTrigger?.start ?? -1}:${activeTrigger?.query ?? ""}`;

  const showMentionPicker = !!repositoryId && activeMention !== null && !triggerDismissed;
  const showCommandPicker = activeCommand !== null && !triggerDismissed;
  const pickerOpen = showMentionPicker || showCommandPicker;

  const fileMentionsQuery = useQuery<ListResponse<SessionInputReference>>({
    queryKey: queryKeys.sessionComposer.files(repositoryId ?? "", branch ?? "", deferredMentionQuery),
    queryFn: () => api.sessionComposer.files(repositoryId ?? "", branch ?? "", deferredMentionQuery),
    enabled: showMentionPicker,
    staleTime: 30 * 1000,
  });
  const fileMentions = useMemo(() => fileMentionsQuery.data?.data ?? [], [fileMentionsQuery.data]);

  const slashCommandsQuery = useSessionComposerSlashCommands({
    agentType,
    query: deferredCommandQuery,
    repositoryId,
    branch,
    enabled: showCommandPicker,
  });
  const slashCommandGroups = useMemo(() => slashCommandsQuery.data?.groups ?? [], [slashCommandsQuery.data]);
  const slashCommandItems = useMemo(
    () => slashCommandGroups.flatMap((group) => group.items),
    [slashCommandGroups],
  );

  const pickerGroups = useMemo<TriggerPickerGroup[]>(() => {
    if (showMentionPicker) {
      return [
        {
          id: "mentions",
          label: "Files and directories",
          items: fileMentions.map((reference) => ({
            id: `${reference.kind}:${reference.path ?? reference.id ?? reference.display}`,
            primary: reference.display,
            icon: reference.kind === "directory" ? directoryTriggerIcon : fileTriggerIcon,
          })),
        },
      ];
    }
    if (showCommandPicker) {
      return slashCommandGroups.map((group) => ({
        id: group.source,
        label: group.label,
        items: group.items.map((command) => ({
          id: command.name,
          primary: command.token,
          secondary: command.description,
        })),
      }));
    }
    return [];
  }, [showMentionPicker, showCommandPicker, fileMentions, slashCommandGroups]);
  const flattenedPickerItems = useMemo(() => flattenGroups(pickerGroups), [pickerGroups]);

  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setTriggerDismissed(false);
    setSelectedTriggerIndex(0);
  }, [triggerQueryKey]);

  useEffect(() => {
    if (!pickerOpen) {
      // eslint-disable-next-line react-hooks/set-state-in-effect
      setPickerPosition(null);
      return;
    }
    function update() {
      const anchor = composerInputSurfaceRef.current ?? composerCardRef.current;
      if (!anchor) return;
      const rect = anchor.getBoundingClientRect();
      const spacing = 8;
      const viewportHeight = window.innerHeight;
      const availableHeight = Math.max(rect.top - spacing, 120);
      setPickerPosition({
        left: rect.left,
        bottom: viewportHeight - rect.top + spacing,
        width: rect.width,
        maxHeight: Math.min(280, availableHeight),
      });
    }
    update();
    window.addEventListener("resize", update);
    window.addEventListener("scroll", update, true);
    return () => {
      window.removeEventListener("resize", update);
      window.removeEventListener("scroll", update, true);
    };
  }, [pickerOpen, fileMentions.length, slashCommandItems.length]);

  function applyMention(reference: SessionInputReference) {
    if (!activeMention) return;
    const inserted = insertMentionAtCaret(message, activeMention, reference);
    onMessageChange(inserted.text);
    const exists = references.find((item) => (item.token ?? item.display) === (reference.token ?? reference.display));
    onReferencesChange(syncReferencesWithMessage(inserted.text, exists ? references : [...references, reference]));
    setCaretPosition(inserted.caret);
    setTriggerDismissed(false);
    requestAnimationFrame(() => {
      const el = textareaRef.current;
      if (!el) return;
      el.focus();
      el.setSelectionRange(inserted.caret, inserted.caret);
    });
  }

  function applyCommand(command: SessionInputCommand) {
    if (!activeCommand) return;
    const inserted = insertCommandAtCaret(message, activeCommand, command);
    onMessageChange(inserted.text);
    const exists = commands.find((item) => item.token === command.token);
    onCommandsChange(syncCommandsWithMessage(inserted.text, exists ? commands : [...commands, command]));
    setCaretPosition(inserted.caret);
    setTriggerDismissed(false);
    requestAnimationFrame(() => {
      const el = textareaRef.current;
      if (!el) return;
      el.focus();
      el.setSelectionRange(inserted.caret, inserted.caret);
    });
  }

  function handleMessageChange(next: string, caret: number) {
    onMessageChange(next);
    onReferencesChange(syncReferencesWithMessage(next, references));
    onCommandsChange(syncCommandsWithMessage(next, commands));
    setCaretPosition(caret);
  }

  function removeReference(reference: SessionInputReference) {
    const next = removeMentionReference(message, reference);
    onMessageChange(next);
    onReferencesChange(references.filter((item) => (item.token ?? item.display) !== (reference.token ?? reference.display)));
    setCaretPosition(next.length);
  }

  function removeCommand(command: SessionInputCommand) {
    const next = removeCommandReference(message, command);
    onMessageChange(next);
    onCommandsChange(commands.filter((item) => item.token !== command.token));
    setCaretPosition(next.length);
  }

  const invalidCommandTokens = useMemo(
    () => commands.filter((command) => command.agent_type !== agentType).map((command) => command.token),
    [commands, agentType],
  );
  const hasInvalidCommands = invalidCommandTokens.length > 0;

  const hasContent = message.trim() || attachments.length > 0 || openComments.length > 0;
  const sendDisabled = hasInvalidCommands || !hasContent || !canSendMessage || sendPending;
  const inactiveSessionMessage = isSnapshotExpired
    ? "Session environment has expired and can no longer be continued."
    : "Session is not active.";
  const attachDisabledReason = isUploading
    ? "Uploading attachments..."
    : !canSendMessage
      ? inactiveSessionMessage
      : undefined;
  const cancelDisabledReason = cancelPending ? "Cancelling session..." : undefined;
  const sendDisabledReason = hasInvalidCommands
    ? `${invalidCommandTokens.join(", ")} ${invalidCommandTokens.length === 1 ? "is" : "are"} not valid for this agent. Remove the chip${invalidCommandTokens.length === 1 ? "" : "s"} to continue.`
    : !hasContent
      ? "Add a message, attachment, or review comment before sending."
      : !canSendMessage
        ? inactiveSessionMessage
          : sendPending
            ? (planMode ? "Sending plan request..." : "Sending message...")
            : undefined;

  function handleKeyDown(e: React.KeyboardEvent<HTMLTextAreaElement>) {
    setCaretPosition(e.currentTarget.selectionStart ?? message.length);
    if (pickerOpen && flattenedPickerItems.length > 0) {
      if (e.key === "ArrowDown") {
        e.preventDefault();
        setSelectedTriggerIndex((previous) => (previous + 1) % flattenedPickerItems.length);
        return;
      }
      if (e.key === "ArrowUp") {
        e.preventDefault();
        setSelectedTriggerIndex((previous) => (previous - 1 + flattenedPickerItems.length) % flattenedPickerItems.length);
        return;
      }
      if (e.key === "Enter" && !e.shiftKey) {
        e.preventDefault();
        const selection = flattenedPickerItems[selectedTriggerIndex];
        if (!selection) return;
        if (showMentionPicker) {
          applyMention(fileMentions[selectedTriggerIndex]);
        } else if (showCommandPicker) {
          applyCommand(slashCommandItems[selectedTriggerIndex]);
        }
        return;
      }
    }
    if (pickerOpen && e.key === "Escape") {
      e.preventDefault();
      setTriggerDismissed(true);
      return;
    }
    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault();
      if (!sendDisabled) {
        onSend();
      }
    }
    if (e.key === "Tab" && e.shiftKey && isClaudeCode && canSendMessage) {
      e.preventDefault();
      onPlanModeChange(!planMode);
    }
  }

  async function handlePaste(event: React.ClipboardEvent<HTMLTextAreaElement>) {
    const files = getClipboardFiles(event.clipboardData);
    if (files.length === 0) {
      return;
    }

    event.preventDefault();
    await onPasteFiles(files);
    requestAnimationFrame(() => {
      const el = textareaRef.current;
      if (!el) return;
      el.focus();
    });
  }

  const firstError = uploadError || sendError;
  const errorMessage = typeof firstError === "string"
    ? firstError
    : firstError instanceof Error
      ? firstError.message
      : firstError
        ? "An error occurred"
        : null;
  const modelSummary = selectedModel || "Default model";
  const modeSummary = isClaudeCode && canSendMessage ? (planMode ? "Plan mode" : "Chat mode") : null;
  const commentSummary = openComments.length > 0
    ? `${openComments.length} comment${openComments.length > 1 ? "s" : ""} attached`
    : null;

  const settingsControls = (
    <div className="space-y-4">
      {editableAgents && editableAgents.length > 0 && editableAgentType && onEditableAgentTypeChange && (
        <div className="space-y-2">
          <Label className="text-xs uppercase tracking-[0.14em] text-muted-foreground">Agent</Label>
          <Select value={editableAgentType} onValueChange={onEditableAgentTypeChange} disabled={agentUpdatePending}>
            <SelectTrigger className="h-11 rounded-xl border-border/70 bg-background text-sm" aria-label="Agent">
              <SelectValue placeholder="Select agent" />
            </SelectTrigger>
            <SelectContent>
              {editableAgents.map((agent) => (
                <SelectItem key={agent.key} value={agent.key}>
                  {agent.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
      )}

      {availableModels.length > 0 && (
        <div className="space-y-2">
          <Label className="text-xs uppercase tracking-[0.14em] text-muted-foreground">Model</Label>
          <Select value={selectedModel} onValueChange={onSelectedModelChange}>
            <SelectTrigger className="h-11 rounded-xl border-border/70 bg-background text-sm" aria-label="Model override">
              <SelectValue placeholder="Default model" />
            </SelectTrigger>
            <SelectContent>
              {availableModels.map((model) => (
                <SelectItem key={model} value={model}>
                  {model}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
      )}

      {isClaudeCode && canSendMessage && (
        <div className="space-y-2">
          <Label className="text-xs uppercase tracking-[0.14em] text-muted-foreground">Mode</Label>
          <div className="grid grid-cols-2 gap-2">
            <Button
              type="button"
              variant={planMode ? "outline" : "default"}
              className="h-10 rounded-xl"
              onClick={() => onPlanModeChange(false)}
            >
              Chat
            </Button>
            <Button
              type="button"
              variant={planMode ? "default" : "outline"}
              className="h-10 rounded-xl"
              onClick={() => onPlanModeChange(true)}
            >
              Plan
            </Button>
          </div>
        </div>
      )}
    </div>
  );

  return (
    <>
      {errorMessage && (
        <div className="flex items-center gap-2 px-4 py-2 text-xs text-destructive border-t bg-destructive/5">
          <AlertTriangle className="h-3 w-3 shrink-0" />
          {errorMessage}
        </div>
      )}

      <SessionComposerTriggerPicker
        open={pickerOpen}
        position={pickerPosition}
        groups={pickerGroups}
        loading={showMentionPicker ? fileMentionsQuery.isFetching : slashCommandsQuery.isFetching}
        emptyLabel={showCommandPicker
          ? `No commands for /${activeCommand?.query ?? ""}`
          : `No matches for @${activeMention?.query ?? ""}`}
        selectedIndex={selectedTriggerIndex}
        onSelectedIndexChange={setSelectedTriggerIndex}
        onSelect={(item, group) => {
          const flatIndex = flattenedPickerItems.findIndex((entry) => entry.group.id === group.id && entry.item.id === item.id);
          if (flatIndex < 0) return;
          if (showMentionPicker) {
            applyMention(fileMentions[flatIndex]);
          } else if (showCommandPicker) {
            applyCommand(slashCommandItems[flatIndex]);
          }
        }}
      />

      <div
        className="border-t border-border p-3 bg-background shrink-0"
        ref={composerCardRef}
        data-testid="session-composer-shell"
      >
        {planMode && (
          <div className="flex items-center gap-2 mb-2 px-1">
            <div className="flex items-center gap-1.5 rounded-full bg-amber-500/10 border border-amber-200 dark:border-amber-800/50 px-2.5 py-1">
              <ClipboardList className="h-3 w-3 text-amber-600 dark:text-amber-400" />
              <span className="text-xs font-medium text-amber-700 dark:text-amber-400">Plan Mode</span>
              <button
                onClick={() => onPlanModeChange(false)}
                className="ml-1 text-amber-600/60 hover:text-amber-600 dark:text-amber-400/60 dark:hover:text-amber-400 text-xs"
                title="Exit plan mode"
              >
                &times;
              </button>
            </div>
            <span className="text-xs text-muted-foreground">Agent will create a plan for review before making changes</span>
          </div>
        )}

        <div
          ref={composerInputSurfaceRef}
          data-testid="session-composer-input-surface"
          className={cn("rounded-xl border bg-muted/30 focus-within:border-ring focus-within:ring-1 focus-within:ring-ring", planMode ? "border-amber-200 dark:border-amber-800/50" : "border-border")}
        >
          {openComments.length > 0 && (
            <div className="flex flex-wrap gap-1.5 px-3 pt-2.5 pb-1">
              {openComments.map((c) => {
                const fileName = c.file_path.split("/").pop() ?? c.file_path;
                return (
                  <div
                    key={c.id}
                    className="inline-flex items-center gap-1.5 rounded-md border border-border bg-background px-2 py-1 text-xs"
                  >
                    <MessageSquare className="h-3 w-3 text-muted-foreground shrink-0" />
                    <span className="font-mono text-muted-foreground">
                      {fileName}:{c.line_number}
                    </span>
                    <span className="text-muted-foreground/40">-</span>
                    <span className="truncate max-w-[200px]">
                      {c.body.length > 60 ? `${c.body.slice(0, 60)}...` : c.body}
                    </span>
                  </div>
                );
              })}
            </div>
          )}

          <Textarea
            ref={textareaRef}
            value={message}
            onChange={(e) => handleMessageChange(e.target.value, e.target.selectionStart ?? e.target.value.length)}
            onPaste={handlePaste}
            onKeyDown={handleKeyDown}
            onFocus={() => setIsTextareaFocused(true)}
            onBlur={() => setIsTextareaFocused(false)}
            onClick={(e) => setCaretPosition(e.currentTarget.selectionStart ?? message.length)}
            onKeyUp={(e) => setCaretPosition(e.currentTarget.selectionStart ?? message.length)}
            onSelect={(e) => setCaretPosition(e.currentTarget.selectionStart ?? message.length)}
            placeholder={
              isSnapshotExpired
                ? "Session environment has expired and can no longer be continued"
                : !canSendMessage
                  ? "Session is not active"
                  : planMode
                    ? "Describe what you want to plan..."
                    : targetLabel
                      ? `Send a message to ${targetLabel}...`
                      : "Send a follow-up message..."
            }
            disabled={!canSendMessage || sendPending}
            rows={isMobile ? 1 : undefined}
            data-mobile-composer-state={isMobile ? (mobileComposerExpanded ? "expanded" : "collapsed") : undefined}
            className={cn(
              "max-h-[200px] resize-none border-none bg-transparent shadow-none focus-visible:ring-0",
              isMobile
                ? mobileComposerExpanded
                  ? "min-h-[96px]"
                  : "min-h-[44px] overflow-hidden"
                : "min-h-[44px]",
            )}
          />

          {(references.length > 0 || commands.length > 0) && (
            <div className="flex flex-wrap gap-1.5 px-3 pb-2" aria-label="Selected references and commands">
              {references.map((reference) => (
                <Badge
                  key={`ref:${reference.kind}:${reference.path ?? reference.id ?? reference.display}`}
                  variant="secondary"
                  className="gap-1 rounded-full border-border/60 bg-muted/60 pl-2 pr-1"
                >
                  {reference.kind === "directory" ? <FolderTree className="h-3 w-3" /> : <FileCode2 className="h-3 w-3" />}
                  <span className="max-w-[14rem] truncate">{reference.display}</span>
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon"
                    className="h-5 w-5 rounded-full"
                    aria-label={`Remove ${reference.display}`}
                    onClick={() => removeReference(reference)}
                  >
                    <X className="h-3 w-3" />
                  </Button>
                </Badge>
              ))}
              {commands.map((command) => {
                const isInvalid = command.agent_type !== agentType;
                return (
                  <Badge
                    key={`cmd:${command.token}`}
                    variant="secondary"
                    className={cn(
                      "gap-1 rounded-full border-border/60 bg-muted/60 pl-2 pr-1",
                      isInvalid && "border-amber-500/60 bg-amber-100/40 text-amber-900 dark:bg-amber-900/30 dark:text-amber-100",
                    )}
                    data-invalid={isInvalid || undefined}
                    title={isInvalid ? `${command.token} is a ${command.agent_type} command. Switch agent or remove it.` : undefined}
                  >
                    <span className="max-w-[14rem] truncate">{command.token}</span>
                    <Button
                      type="button"
                      variant="ghost"
                      size="icon"
                      className="h-5 w-5 rounded-full"
                      aria-label={`Remove ${command.token}`}
                      onClick={() => removeCommand(command)}
                    >
                      <X className="h-3 w-3" />
                    </Button>
                  </Badge>
                );
              })}
            </div>
          )}
          {hasInvalidCommands && (
            <p className="px-3 pb-2 text-xs text-amber-600 dark:text-amber-300" role="alert">
              {invalidCommandTokens.join(", ")} {invalidCommandTokens.length === 1 ? "is" : "are"} not valid for this agent. Remove the chip{invalidCommandTokens.length === 1 ? "" : "s"} to continue.
            </p>
          )}

          <PendingAttachmentStrip
            attachments={attachments}
            isUploading={isUploading}
            onRemove={onRemoveAttachment}
            size="md"
            className="px-3 pb-2"
          />

          {showImageInput && (
            <div className="flex items-center gap-2 px-3 pb-2">
              <Input
                value={imageURL}
                onChange={(event) => setImageURL(event.target.value)}
                placeholder="https://example.com/screenshot.png"
                aria-label="Image URL"
                className="h-8"
              />
              <Button type="button" variant="outline" size="sm" onClick={addImageURL}>
                Add
              </Button>
            </div>
          )}

          {showLinearInput && (
            <div className="flex flex-col gap-1 px-3 pb-2">
              <div className="flex items-center gap-2">
                <LinearIcon className="h-4 w-4 shrink-0 text-muted-foreground" />
                <Input
                  ref={linearInputRef}
                  value={linearInput}
                  onChange={(event) => {
                    setLinearInput(event.target.value);
                    if (linearInputError) {
                      setLinearInputError(null);
                    }
                  }}
                  onKeyDown={(event) => {
                    if (event.key === "Enter") {
                      event.preventDefault();
                      addLinearLink();
                    } else if (event.key === "Escape") {
                      event.preventDefault();
                      setLinearInput("");
                      setLinearInputError(null);
                      setShowLinearInput(false);
                    }
                  }}
                  placeholder="ACS-1234 or https://linear.app/acme/issue/ACS-1234"
                  aria-label="Linear issue id or URL"
                  aria-invalid={linearInputError ? true : undefined}
                  className="h-8"
                />
                <Button type="button" variant="outline" size="sm" onClick={addLinearLink}>
                  Add
                </Button>
              </div>
              {linearInputError && (
                <p role="alert" className="pl-6 text-xs text-destructive">{linearInputError}</p>
              )}
            </div>
          )}

          <div className="px-2 pb-2">
            {isMobile ? (
              <>
                <div className="flex items-center gap-2">
                  <DisabledTooltip disabled={!canSendMessage} content={attachDisabledReason}>
                    <SessionComposerAttachmentMenu
                      disabled={!canSendMessage}
                      isUploading={isUploading}
                      buttonAriaLabel="Add files, photos, or a Linear issue"
                      buttonTitle="Add files, photos, or a Linear issue"
                      buttonClassName="h-8 w-8 shrink-0 rounded-lg text-muted-foreground hover:text-foreground"
                      onUploadFiles={() => uploadInputRef.current?.click()}
                      onAddImageURL={() => setShowImageInput(true)}
                      onAddLinearIssue={() => setShowLinearInput(true)}
                    />
                  </DisabledTooltip>
                  <Button
                    type="button"
                    variant="ghost"
                    size="sm"
                    aria-label="Session settings"
                    className="h-8 rounded-full border border-border/60 bg-background/70 px-3 text-xs text-foreground shadow-sm hover:bg-background"
                    onClick={() => setMobileSettingsOpen(true)}
                  >
                    <ClipboardList className="mr-1.5 h-3.5 w-3.5" />
                    Settings
                  </Button>
                  <div className="ml-auto flex items-center gap-1">
                    {isRunning && (
                      <DisabledTooltip disabled={cancelPending} content={cancelDisabledReason}>
                        <Button
                          size="icon"
                          variant="outline"
                          className="h-8 w-8 shrink-0 rounded-lg"
                          title="Cancel session"
                          disabled={cancelPending}
                          onClick={onCancelSession}
                        >
                          <Square className="h-3 w-3" />
                        </Button>
                      </DisabledTooltip>
                    )}
                    <DisabledTooltip disabled={sendDisabled} content={sendDisabledReason}>
                      <Button
                        size="icon"
                        variant={planMode ? "outline" : "default"}
                        className={cn("h-8 w-8 shrink-0 rounded-lg", planMode && "border-amber-300 dark:border-amber-700 text-amber-700 dark:text-amber-400 hover:bg-amber-50 dark:hover:bg-amber-950/30")}
                        title={planMode ? "Send plan request" : "Send message"}
                        disabled={sendDisabled}
                        onClick={onSend}
                      >
                        {sendPending ? (
                          <Loader2 className="h-4 w-4 animate-spin" />
                        ) : planMode ? (
                          <ClipboardList className="h-4 w-4" />
                        ) : (
                          <ArrowUp className="h-4 w-4" />
                        )}
                      </Button>
                    </DisabledTooltip>
                  </div>
                </div>
                <div className="mt-2 flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-muted-foreground">
                  <span className="font-medium text-foreground">{modelSummary}</span>
                  {modeSummary ? (
                    <>
                      <span aria-hidden="true">•</span>
                      <span>{modeSummary}</span>
                    </>
                  ) : null}
                  {commentSummary ? (
                    <>
                      <span aria-hidden="true">•</span>
                      <span>{commentSummary}</span>
                    </>
                  ) : null}
                </div>
              </>
            ) : (
              <div className="flex items-center gap-1">
                <DisabledTooltip disabled={!canSendMessage} content={attachDisabledReason}>
                  <SessionComposerAttachmentMenu
                    disabled={!canSendMessage}
                    isUploading={isUploading}
                    buttonAriaLabel="Add files, photos, or a Linear issue"
                    buttonTitle="Add files, photos, or a Linear issue"
                    buttonClassName="h-8 w-8 shrink-0 rounded-lg text-muted-foreground hover:text-foreground"
                    onUploadFiles={() => uploadInputRef.current?.click()}
                    onAddImageURL={() => setShowImageInput(true)}
                    onAddLinearIssue={() => setShowLinearInput(true)}
                  />
                </DisabledTooltip>

                {availableModels.length > 0 && (
                  <Select value={selectedModel} onValueChange={onSelectedModelChange}>
                    <SelectTrigger className="h-8 w-auto gap-1.5 border-none bg-transparent px-2 text-xs text-muted-foreground shadow-none hover:text-foreground focus:ring-0" aria-label="Model override">
                      <SelectValue placeholder="Default model" />
                    </SelectTrigger>
                    <SelectContent>
                      {availableModels.map((model) => (
                        <SelectItem key={model} value={model}>
                          {model}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                )}

                {editableAgents && editableAgents.length > 0 && editableAgentType && onEditableAgentTypeChange && (
                  <Select value={editableAgentType} onValueChange={onEditableAgentTypeChange} disabled={agentUpdatePending}>
                    <SelectTrigger className="h-8 w-auto gap-1.5 border-none bg-transparent px-2 text-xs text-muted-foreground shadow-none hover:text-foreground focus:ring-0" aria-label="Agent">
                      <SelectValue placeholder="Agent" />
                    </SelectTrigger>
                    <SelectContent>
                      {editableAgents.map((agent) => (
                        <SelectItem key={agent.key} value={agent.key}>
                          {agent.label}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                )}

                {isClaudeCode && canSendMessage && !planMode && (
                  <button
                    onClick={() => onPlanModeChange(true)}
                    className="flex items-center gap-1 h-8 px-2 text-xs text-muted-foreground hover:text-foreground transition-colors rounded-md"
                    title="Switch to plan mode (Shift+Tab)"
                  >
                    <ClipboardList className="h-3.5 w-3.5" />
                    <span>Plan</span>
                  </button>
                )}

                {openComments.length > 0 && (
                  <span className="text-xs text-muted-foreground">
                    {openComments.length} comment{openComments.length > 1 ? "s" : ""} attached
                  </span>
                )}

                <div className="ml-auto flex items-center gap-1">
                  {isRunning && (
                    <DisabledTooltip disabled={cancelPending} content={cancelDisabledReason}>
                      <Button
                        size="icon"
                        variant="outline"
                        className="h-8 w-8 shrink-0 rounded-lg"
                        title="Cancel session"
                        disabled={cancelPending}
                        onClick={onCancelSession}
                      >
                        <Square className="h-3 w-3" />
                      </Button>
                    </DisabledTooltip>
                  )}
                  <DisabledTooltip disabled={sendDisabled} content={sendDisabledReason}>
                    <Button
                      size="icon"
                      variant={planMode ? "outline" : "default"}
                      className={cn("h-8 w-8 shrink-0 rounded-lg", planMode && "border-amber-300 dark:border-amber-700 text-amber-700 dark:text-amber-400 hover:bg-amber-50 dark:hover:bg-amber-950/30")}
                      title={planMode ? "Send plan request" : "Send message"}
                      disabled={sendDisabled}
                      onClick={onSend}
                    >
                      {sendPending ? (
                        <Loader2 className="h-4 w-4 animate-spin" />
                      ) : planMode ? (
                        <ClipboardList className="h-4 w-4" />
                      ) : (
                        <ArrowUp className="h-4 w-4" />
                      )}
                    </Button>
                  </DisabledTooltip>
                </div>
              </div>
            )}
            <input
              ref={uploadInputRef}
              type="file"
              accept="image/*,.pdf,.txt,.md,.json,.csv"
              multiple
              className="hidden"
              onChange={onUpload}
            />
          </div>
        </div>
      </div>

      <Sheet open={isMobile && mobileSettingsOpen} onOpenChange={setMobileSettingsOpen}>
        <SheetContent
          side="bottom"
          hideCloseButton
          className="rounded-t-[1.75rem] border-border/70 px-4 pb-6 pt-5 sm:max-w-none"
        >
          <SheetHeader className="mb-4">
            <SheetTitle className="text-base">Session settings</SheetTitle>
            <SheetDescription>Adjust the follow-up model and mode without crowding the mobile composer.</SheetDescription>
          </SheetHeader>
          {settingsControls}
          <Button type="button" className="mt-5 h-11 w-full rounded-xl" onClick={() => setMobileSettingsOpen(false)}>
            Done
          </Button>
        </SheetContent>
      </Sheet>
    </>
  );
}

// ---------------------------------------------------------------------------
// Main chat panel
// ---------------------------------------------------------------------------

const MAX_SSE_RECONNECT_ATTEMPTS = 3;
const BASE_SSE_RECONNECT_DELAY_MS = 1000;
const MAX_FILE_SIZE = 10 * 1024 * 1024; // 10 MB
const SCROLL_NEAR_BOTTOM_THRESHOLD = 100;
const SCROLL_POSITION_SAVE_DEBOUNCE_MS = 150;
// Sliding window for the SSE log overlay buffer. The persisted logs are
// fetched separately via the timeline query; streamedLogs only holds the
// not-yet-persisted overlay that bridges the gap between an SSE push and the
// next DB fetch. A few thousand entries is enough headroom for any active
// session, and capping it bounds both memory and the per-event filter cost.
const STREAMED_LOGS_MAX = 2000;

function isNearBottom(el: HTMLElement): boolean {
  return el.scrollHeight - el.scrollTop - el.clientHeight < SCROLL_NEAR_BOTTOM_THRESHOLD;
}

function normalizeTranscriptContent(content: string): string {
  return content
    .replace(/\r\n/g, "\n")
    .split("\n")
    .map((line) => line.replace(/[ \t\r]+$/g, ""))
    .join("\n")
    .replace(/\n+$/g, "");
}

function SessionTimelineSkeleton() {
  const rows: { align: "left" | "right"; widths: string[] }[] = [
    { align: "right", widths: ["w-3/5", "w-2/5"] },
    { align: "left", widths: ["w-4/5", "w-3/4", "w-1/2"] },
    { align: "left", widths: ["w-2/3", "w-1/3"] },
    { align: "left", widths: ["w-3/4", "w-3/5"] },
  ];

  return (
    <div
      role="status"
      aria-live="polite"
      aria-label="Loading session activity"
      data-testid="session-timeline-skeleton"
      className="space-y-3 py-1"
    >
      {rows.map((row, i) => (
        <div
          key={i}
          className={`flex ${row.align === "right" ? "justify-end" : "justify-start"}`}
        >
          <div
            className={`max-w-[92%] min-w-[40%] rounded-lg px-3 py-2.5 space-y-2 animate-pulse ${
              row.align === "right" ? "bg-primary/10" : "bg-muted"
            }`}
          >
            {row.widths.map((w, j) => (
              <div
                key={j}
                className={`h-3 rounded ${w} ${
                  row.align === "right" ? "bg-primary/20" : "bg-muted-foreground/15"
                }`}
              />
            ))}
          </div>
        </div>
      ))}
      <span className="sr-only">Loading session activity…</span>
    </div>
  );
}

type ChatPanelProps = {
  session: Session;
  sessionId: string;
  activeThread?: SessionThread;
  isActive: boolean;
  optimisticMessages: SessionMessage[];
  onDiffClick?: () => void;
  onApprovePlan?: () => void;
  onAdjustPlan?: () => void;
  onRegisterScrollToLiveEdge?: (scrollToLiveEdge: (() => void) | null) => void;
};

function ChatPanel({
  session,
  sessionId,
  activeThread,
  isActive,
  optimisticMessages,
  onDiffClick,
  onApprovePlan,
  onAdjustPlan,
  onRegisterScrollToLiveEdge,
}: ChatPanelProps) {
  const queryClient = useQueryClient();
  const { user } = useAuth();
  const [streamedLogs, setStreamedLogs] = useState<SessionLog[]>([]);
  const scrollRef = useRef<HTMLDivElement>(null);
  const isNearBottomRef = useRef(false);
  const initialAnchorAppliedRef = useRef(false);
  const saveScrollTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const seenLogIds = useRef<Set<number>>(new Set());
  const reconnectAttempts = useRef(0);
  const reconnectTimer = useRef<ReturnType<typeof setTimeout>>(null);
  const apiBase = process.env.NEXT_PUBLIC_API_URL || "";
  const [showJumpToLatest, setShowJumpToLatest] = useState(false);
  const isDocumentVisible = useDocumentVisible();
  const viewerScope = useMemo(
    () => (user ? { userId: user.id, orgId: user.org_id } : null),
    [user],
  );

  const activeThreadId = activeThread?.id;
  const isRunning = activeThread ? activeThread.status === "running" : session.status === "running";
  const isSnapshotExpired = session.sandbox_state === "destroyed";
  const canSendMessage = session.status !== "skipped" && session.status !== "pending" && !isSnapshotExpired;

  const timelineQuery = useQuery({
    queryKey: ["session", sessionId, "timeline"],
    queryFn: () => api.sessions.getTimeline(sessionId),
    enabled: !activeThreadId,
    refetchInterval: isActive && !activeThreadId ? 3000 : false,
  });

  const threadMessagesQuery = useQuery({
    queryKey: activeThreadId ? queryKeys.sessions.threadMessages(sessionId, activeThreadId) : ["session", sessionId, "thread", "none", "messages"],
    queryFn: () => api.sessions.getThreadMessages(sessionId, activeThreadId!),
    enabled: !!activeThreadId,
    refetchInterval: activeThread && workingStatusesSet.has(activeThread.status) ? 3000 : false,
  });

  const threadLogsQuery = useQuery({
    queryKey: activeThreadId ? queryKeys.sessions.threadLogs(sessionId, activeThreadId) : ["session", sessionId, "thread", "none", "logs"],
    queryFn: () => api.sessions.getThreadLogs(sessionId, activeThreadId!),
    enabled: !!activeThreadId,
    refetchInterval: activeThread && workingStatusesSet.has(activeThread.status) ? 3000 : false,
  });

  // Fetch the linked primary issue to display its description as the initial prompt.
  const primaryIssueId = session.primary_issue_id ?? undefined;
  const hasIssue = !!primaryIssueId;
  const issueQuery = useQuery({
    queryKey: ["issue", primaryIssueId],
    queryFn: () => api.issues.get(primaryIssueId!),
    enabled: hasIssue && !activeThreadId,
  });

  const baseTimelineEntries = useMemo(() => {
    const optimisticForCurrentView = optimisticMessages.filter((message) =>
      activeThreadId ? message.thread_id === activeThreadId : !message.thread_id
    );
    if (activeThreadId) {
      return buildTimeline(
        mergePendingMessages(threadMessagesQuery.data?.data ?? [], optimisticForCurrentView),
        threadLogsQuery.data?.data ?? [],
      );
    }
    const flattenedTimeline = flattenTimelineResponse(timelineQuery.data?.data ?? []);
    const entries = buildTimeline(
      mergePendingMessages(flattenedTimeline.messages, optimisticForCurrentView),
      flattenedTimeline.logs,
    );
    const issueDescription = issueQuery.data?.data?.description;
    if (!issueDescription) return entries;
    const hasTurn0UserMsg = entries.some((entry) => entry.kind === "message" && entry.data.role === "user" && entry.data.turn_number === 0);
    if (hasTurn0UserMsg) return entries;
    const syntheticMsg: SessionMessage = {
      id: -1,
      session_id: sessionId,
      org_id: session.org_id,
      turn_number: 0,
      role: "user",
      content: issueDescription,
      created_at: session.created_at,
    };
    return [{ kind: "message" as const, data: syntheticMsg }, ...entries];
  }, [activeThreadId, optimisticMessages, threadMessagesQuery.data?.data, threadLogsQuery.data?.data, timelineQuery.data?.data, issueQuery.data?.data?.description, sessionId, session.org_id, session.created_at]);

  // Walk baseTimelineEntries once when it changes to derive the dedup keys
  // used to filter streamedLogs. Splitting this out of the timelineEntries
  // memo means each new SSE log event no longer triggers an O(N) walk over
  // the entire base timeline — only the O(M) filter over streamed logs.
  const baseTimelineDedupKeys = useMemo(() => {
    const fetchedLogIds = new Set<number>();
    const assistantTranscriptByTurn = new Map<number, Set<string>>();
    const planModeSeedMessages: SessionMessage[] = [];

    for (const entry of baseTimelineEntries) {
      switch (entry.kind) {
        case "message":
          if (entry.data.role === "user" && entry.data.content.startsWith("[PLAN_MODE]\n")) {
            planModeSeedMessages.push(entry.data);
          }
          if (entry.data.role === "assistant") {
            const contents = assistantTranscriptByTurn.get(entry.data.turn_number) ?? new Set<string>();
            contents.add(normalizeTranscriptContent(entry.data.content));
            assistantTranscriptByTurn.set(entry.data.turn_number, contents);
          }
          break;
        case "plan_message": {
          const contents = assistantTranscriptByTurn.get(entry.data.turn_number) ?? new Set<string>();
          contents.add(normalizeTranscriptContent(entry.data.content));
          assistantTranscriptByTurn.set(entry.data.turn_number, contents);
          break;
        }
        case "assistant_output":
        case "error":
        case "log":
        case "plan_output":
          fetchedLogIds.add(entry.data.id);
          break;
        case "tool_group":
          fetchedLogIds.add(entry.toolUse.id);
          if (entry.toolResult) {
            fetchedLogIds.add(entry.toolResult.id);
          }
          break;
      }
    }

    return { fetchedLogIds, assistantTranscriptByTurn, planModeSeedMessages };
  }, [baseTimelineEntries]);

  const timelineEntries = useMemo(() => {
    const { fetchedLogIds, assistantTranscriptByTurn, planModeSeedMessages } = baseTimelineDedupKeys;

    const overlayLogs = streamedLogs.filter((log) => {
      if (fetchedLogIds.has(log.id)) return false;
      if (log.level !== "output") return true;
      if (log.metadata?.type === "tool_result") return true;
      if (log.metadata?.type === "assistant_final" && log.metadata?.duplicate_of_transcript === true) return false;
      return !assistantTranscriptByTurn.get(log.turn_number)?.has(normalizeTranscriptContent(log.message));
    });

    if (overlayLogs.length === 0) return baseTimelineEntries;
    const overlayEntries = buildTimeline(planModeSeedMessages, overlayLogs).filter((entry) => entry.kind !== "message");
    return sortTimelineEntries([...baseTimelineEntries, ...overlayEntries]);
  }, [baseTimelineEntries, baseTimelineDedupKeys, streamedLogs]);
  const hasLoadedTimelineInputs = activeThreadId
    ? threadMessagesQuery.isFetched && threadLogsQuery.isFetched
    : timelineQuery.isFetched && (!hasIssue || issueQuery.isFetched);
  // Skeleton only while we'd reasonably expect content: data still loading, or
  // the relevant scope is actively working. For a thread-scoped view, "working"
  // is the selected thread's status — a freshly-created idle thread on an
  // otherwise-running session must show its empty-state composer, not a
  // perpetual skeleton. Terminal sessions with empty timelines must not
  // shimmer forever either.
  const expectingMoreContent = activeThread
    ? workingStatusesSet.has(activeThread.status)
    : activeSet.has(session.status);
  const showLoadingSkeleton =
    timelineEntries.length === 0 &&
    session.status !== "pending" &&
    (!hasLoadedTimelineInputs || expectingMoreContent);

  const persistScrollPosition = useCallback((scrollTop: number) => {
    if (typeof window === "undefined" || !viewerScope) return;
    writeStoredSessionScrollPosition(window.localStorage, sessionId, viewerScope, scrollTop, activeThreadId);
  }, [activeThreadId, sessionId, viewerScope]);

  const schedulePersistScrollPosition = useCallback((scrollTop: number) => {
    if (saveScrollTimerRef.current) {
      clearTimeout(saveScrollTimerRef.current);
    }
    saveScrollTimerRef.current = setTimeout(() => {
      persistScrollPosition(scrollTop);
      saveScrollTimerRef.current = null;
    }, SCROLL_POSITION_SAVE_DEBOUNCE_MS);
  }, [persistScrollPosition]);

  const syncScrollState = useCallback((el: HTMLDivElement) => {
    isNearBottomRef.current = isNearBottom(el);
    setShowJumpToLatest(!isNearBottomRef.current);
  }, []);

  const scrollToLiveEdgePosition = useCallback(() => {
    const el = scrollRef.current;
    if (!el) return;
    el.scrollTop = el.scrollHeight;
    isNearBottomRef.current = true;
  }, []);

  const scrollToLiveEdge = useCallback(() => {
    scrollToLiveEdgePosition();
    setShowJumpToLatest(false);
  }, [scrollToLiveEdgePosition]);

  const getEntryContainerProps = useCallback(
    (_entry: TimelineEntry, index: number) =>
      ({
        "data-session-entry-index": index,
      }) as React.HTMLAttributes<HTMLDivElement> & Record<`data-${string}`, string | number | undefined>,
    [],
  );

  useEffect(() => {
    onRegisterScrollToLiveEdge?.(scrollToLiveEdge);
    return () => onRegisterScrollToLiveEdge?.(null);
  }, [onRegisterScrollToLiveEdge, scrollToLiveEdge]);

  // SSE streaming for real-time logs when the session is active.
  const mergeLogs = useCallback((newLogs: SessionLog[]) => {
    setStreamedLogs((prev) => {
      const toAdd: SessionLog[] = [];
      for (const log of newLogs) {
        if (!seenLogIds.current.has(log.id)) {
          seenLogIds.current.add(log.id);
          toAdd.push(log);
        }
      }
      if (toAdd.length === 0) return prev;
      const next = [...prev, ...toAdd];
      // Drop oldest entries once we exceed the cap so a long-running session
      // can't grow the overlay buffer without bound. Older logs already exist
      // in the persisted timeline once the next refetch lands.
      if (next.length > STREAMED_LOGS_MAX) {
        return next.slice(next.length - STREAMED_LOGS_MAX);
      }
      return next;
    });
  }, []);

  const mergeSessionStatusUpdate = useCallback((updated: Session) => {
    queryClient.setQueryData<SingleResponse<SessionDetail>>(["session", sessionId], (existing) => {
      if (!existing) {
        return { data: { ...updated, threads: [] } };
      }
      return {
        ...existing,
        data: {
          ...existing.data,
          ...updated,
          threads: updated.threads ?? existing.data.threads ?? [],
        },
      };
    });
  }, [queryClient, sessionId]);

  useEffect(() => {
    // Pause the SSE stream while the tab is hidden. EventSource handlers fire
    // even in a hidden tab and trigger setState/re-renders on this large
    // component, which steals main-thread time from any tab the user just
    // switched to (notably "View PR" → github.com). On reconnect, the existing
    // onerror path already invalidates the timeline/thread queries so the user
    // sees fresh state when they return.
    if (!isActive || !isDocumentVisible) return;

    let eventSource: EventSource | null = null;
    let cancelled = false;

    function connect() {
      if (cancelled) return;

      eventSource = new EventSource(
        buildSessionLogsStreamURL(apiBase, sessionId, getActiveOrgId()),
        { withCredentials: true }
      );

      eventSource.onopen = () => {
        reconnectAttempts.current = 0;
      };

      addSSEListener(eventSource, SSE_EVENT.LOG, (log) => {
        if (!activeThreadId || log.thread_id === activeThreadId) {
          mergeLogs([log]);
        }
      });

      addSSEListener(eventSource, SSE_EVENT.STATUS, (updated) => {
        mergeSessionStatusUpdate(updated);
        // When the session transitions out of running (e.g. sandbox creation
        // failure reverts to idle), fetch the latest messages so any error
        // message posted by the backend is displayed immediately.
        if (updated.status !== "running") {
          queryClient.invalidateQueries({ queryKey: ["session", sessionId, "timeline"] });
          if (activeThreadId) {
            queryClient.invalidateQueries({ queryKey: queryKeys.sessions.threadMessages(sessionId, activeThreadId) });
            queryClient.invalidateQueries({ queryKey: queryKeys.sessions.threadLogs(sessionId, activeThreadId) });
          }
        }
      });

      addSSEListener(eventSource, SSE_EVENT.DONE, (updated) => {
        mergeSessionStatusUpdate(updated);
        eventSource?.close();
        queryClient.invalidateQueries({ queryKey: ["session", sessionId, "timeline"] });
        if (activeThreadId) {
          queryClient.invalidateQueries({ queryKey: queryKeys.sessions.threadMessages(sessionId, activeThreadId) });
          queryClient.invalidateQueries({ queryKey: queryKeys.sessions.threadLogs(sessionId, activeThreadId) });
        }
      });

      eventSource.onerror = () => {
        eventSource?.close();
        queryClient.invalidateQueries({ queryKey: ["session", sessionId, "timeline"] });
        if (activeThreadId) {
          queryClient.invalidateQueries({ queryKey: queryKeys.sessions.threadMessages(sessionId, activeThreadId) });
          queryClient.invalidateQueries({ queryKey: queryKeys.sessions.threadLogs(sessionId, activeThreadId) });
        }

        if (!cancelled && reconnectAttempts.current < MAX_SSE_RECONNECT_ATTEMPTS) {
          const delay =
            BASE_SSE_RECONNECT_DELAY_MS *
            Math.pow(2, reconnectAttempts.current);
          reconnectAttempts.current += 1;
          reconnectTimer.current = setTimeout(connect, delay);
        }
      };
    }

    connect();

    return () => {
      cancelled = true;
      eventSource?.close();
      if (reconnectTimer.current) {
        clearTimeout(reconnectTimer.current);
      }
    };
  }, [sessionId, apiBase, isActive, isDocumentVisible, mergeLogs, mergeSessionStatusUpdate, queryClient, activeThreadId]);

  // Track whether the user is scrolled near the bottom.
  const handleScroll = useCallback(() => {
    const el = scrollRef.current;
    if (!el) return;
    syncScrollState(el);
    schedulePersistScrollPosition(el.scrollTop);
  }, [schedulePersistScrollPosition, syncScrollState]);

  useEffect(() => {
    initialAnchorAppliedRef.current = false;
  }, [activeThreadId, sessionId]);

  useEffect(() => {
    const currentScrollEl = scrollRef.current;
    return () => {
      if (saveScrollTimerRef.current) {
        clearTimeout(saveScrollTimerRef.current);
      }
      if (currentScrollEl && initialAnchorAppliedRef.current) {
        persistScrollPosition(currentScrollEl.scrollTop);
      }
    };
  }, [persistScrollPosition]);

  useEffect(() => {
    if (!hasLoadedTimelineInputs || initialAnchorAppliedRef.current || !viewerScope) return;

    const el = scrollRef.current;
    if (!el) return;

    const storedScrollTop =
      typeof window === "undefined"
        ? null
        : readStoredSessionScrollPosition(window.localStorage, sessionId, viewerScope, activeThreadId);
    const anchor = resolveInitialSessionAnchor({
      entries: timelineEntries,
      isActive: isRunning,
      storedScrollTop,
    });

    if (anchor.kind === "saved_position") {
      el.scrollTop = anchor.scrollTop;
      syncScrollState(el);
      initialAnchorAppliedRef.current = true;
      return;
    }

    if (anchor.kind === "entry") {
      const target = el.querySelector<HTMLElement>(`[data-session-entry-index="${anchor.entryIndex}"]`);
      if (target) {
        el.scrollTop = target.offsetTop;
        syncScrollState(el);
        initialAnchorAppliedRef.current = true;
        return;
      }
    }

    scrollToLiveEdgePosition();
    initialAnchorAppliedRef.current = true;
  }, [activeThreadId, hasLoadedTimelineInputs, isRunning, scrollToLiveEdgePosition, sessionId, syncScrollState, timelineEntries, viewerScope]);

  // Only auto-scroll to bottom when new entries arrive if the user is already near the bottom.
  useEffect(() => {
    if (scrollRef.current && isNearBottomRef.current) {
      scrollToLiveEdgePosition();
    }
  }, [scrollToLiveEdgePosition, timelineEntries.length]);

  return (
    <div className="relative flex flex-col h-full">
      {showJumpToLatest && (
        <div className="absolute bottom-4 right-4 z-20">
          <Button
            type="button"
            size="sm"
            variant="secondary"
            className="rounded-full shadow-sm"
            onClick={scrollToLiveEdge}
          >
            <ArrowDown className="h-4 w-4" />
            Jump to latest
          </Button>
        </div>
      )}
      {/* Unified timeline */}
      <div ref={scrollRef} onScroll={handleScroll} className="flex-1 overflow-y-auto space-y-2 p-4">
        {showLoadingSkeleton ? (
          <SessionTimelineSkeleton />
        ) : (
          <ChatTimeline
            entries={timelineEntries}
            isRunning={isRunning}
            diffStats={session.diff_stats}
            onDiffClick={onDiffClick}
            onApprovePlan={canSendMessage ? onApprovePlan : undefined}
            onAdjustPlan={canSendMessage ? onAdjustPlan : undefined}
            getEntryContainerProps={getEntryContainerProps}
          />
        )}
        {(activeThread?.status === "pending" || (!activeThread && session.status === "pending")) && (
          <div className="flex items-center justify-center py-12">
            <div className="text-center space-y-2 max-w-[280px]">
              <Loader2 className="h-8 w-8 text-muted-foreground/40 mx-auto animate-spin" />
              <p className="text-xs font-medium text-muted-foreground">Setting up environment</p>
              <p className="text-xs text-muted-foreground/60">Preparing the container and getting the agent ready to run.</p>
            </div>
          </div>
        )}
      </div>
    </div>
  );
}

function sameDiffStats(
  a?: Session["diff_stats"] | null,
  b?: Session["diff_stats"] | null,
): boolean {
  if (a === b) {
    return true;
  }
  if (!a || !b) {
    return !a && !b;
  }
  return a.added === b.added && a.removed === b.removed && a.files_changed === b.files_changed;
}

function areChatPanelPropsEqual(previous: ChatPanelProps, next: ChatPanelProps): boolean {
  return previous.sessionId === next.sessionId &&
    previous.isActive === next.isActive &&
    previous.optimisticMessages === next.optimisticMessages &&
    previous.onDiffClick === next.onDiffClick &&
    previous.onApprovePlan === next.onApprovePlan &&
    previous.onAdjustPlan === next.onAdjustPlan &&
    previous.onRegisterScrollToLiveEdge === next.onRegisterScrollToLiveEdge &&
    previous.session.id === next.session.id &&
    previous.session.status === next.session.status &&
    previous.session.sandbox_state === next.session.sandbox_state &&
    previous.session.primary_issue_id === next.session.primary_issue_id &&
    previous.session.org_id === next.session.org_id &&
    previous.session.created_at === next.session.created_at &&
    sameDiffStats(previous.session.diff_stats, next.session.diff_stats) &&
    previous.activeThread?.id === next.activeThread?.id &&
    previous.activeThread?.status === next.activeThread?.status &&
    previous.activeThread?.current_turn === next.activeThread?.current_turn &&
    previous.activeThread?.label === next.activeThread?.label;
}

const MemoizedChatPanel = memo(ChatPanel, areChatPanelPropsEqual);

// ---------------------------------------------------------------------------
// Main component
// ---------------------------------------------------------------------------

const MIN_DETAIL = 280;
const MAX_DETAIL = 600;
const DEFAULT_DETAIL = 384;
const MOBILE_REVIEW_MEDIA_QUERY = "(max-width: 767px)";

export function SessionDetailContent({ id }: { id: string }) {
  const router = useRouter();
  const terminalStatuses = new Set(["completed", "pr_created", "failed", "cancelled", "skipped"]);
  const [reviewParam, setReviewParam] = useQueryState("review");
  const [previewParam, setPreviewParam] = useQueryState("preview");
  const [resumePRParam, setResumePRParam] = useQueryState("resume_pr");
  const [resumeActionParam, setResumeActionParam] = useQueryState("resume_action");
  const [githubPRParam, setGithubPRParam] = useQueryState("github_pr");
  const centerMode = reviewParam === "active" ? "review" : "chat";
  const [detailTab, setDetailTab] = useState<DetailTab>(
    previewParam === "1" ? "preview" : "overview"
  );
  const [showDetailPanel, setShowDetailPanel] = useState(true);
  // Mobile bottom sheet — separate state so the desktop inline panel can
  // default open while the mobile sheet defaults closed (no SSR-unsafe
  // matchMedia needed).
  const [mobileDetailOpen, setMobileDetailOpen] = useState(false);
  const [mobileReviewComposerOpen, setMobileReviewComposerOpen] = useState(false);
  const [detailWidth, setDetailWidth] = useState(DEFAULT_DETAIL);
  const [activeFileIndex, setActiveFileIndex] = useState(0);
  const [isEditingTitle, setIsEditingTitle] = useState(false);
  const [draftTitle, setDraftTitle] = useState("");
  const [isMobileReviewViewport, setIsMobileReviewViewport] = useState(false);

  useEffect(() => {
    if (typeof window === "undefined" || typeof window.matchMedia !== "function") {
      return;
    }

    const mediaQuery = window.matchMedia(MOBILE_REVIEW_MEDIA_QUERY);
    const syncViewport = (event?: MediaQueryListEvent) => {
      setIsMobileReviewViewport(event ? event.matches : mediaQuery.matches);
    };

    syncViewport();

    if (typeof mediaQuery.addEventListener === "function") {
      mediaQuery.addEventListener("change", syncViewport);
      return () => mediaQuery.removeEventListener("change", syncViewport);
    }

    mediaQuery.addListener(syncViewport);
    return () => mediaQuery.removeListener(syncViewport);
  }, []);

  const handleDetailResize = useCallback((delta: number) => {
    setDetailWidth((w) => Math.min(MAX_DETAIL, Math.max(MIN_DETAIL, w - delta)));
  }, []);

  // --- Enter review mode ---
  const openReview = useCallback((fileIndex?: number) => {
    if (fileIndex !== undefined) setActiveFileIndex(fileIndex);
    setReviewParam("active");
    setDetailTab("changes");
    setShowDetailPanel(true);
    // Mobile review should hand off directly into the diff reader. The detail
    // sheet stays available as a file index, but it should not remain on top
    // of the review surface when the user asks to view changes.
    if (isMobileReviewViewport) {
      setMobileDetailOpen(false);
    }
  }, [isMobileReviewViewport, setReviewParam]);
  const openMobileFilesList = useCallback(() => {
    setDetailTab("changes");
    setMobileDetailOpen(true);
  }, []);

  // --- Exit review mode ---
  const exitReview = useCallback(() => {
    setReviewParam(null);
  }, [setReviewParam]);

  // --- Handle detail tab click ---
  const handleDetailTabClick = useCallback((tab: DetailTab) => {
    setDetailTab(tab);
    setPreviewParam(tab === "preview" ? "1" : null);
    // Clicking a non-changes tab exits review mode
    if (tab !== "changes" && centerMode === "review") {
      exitReview();
    }
  }, [centerMode, exitReview, setPreviewParam]);
  const [localPRState, setLocalPRState] = useState<"idle" | "submitting" | "queued">("idle");
  const [localPRActionError, setLocalPRActionError] = useState<PRActionErrorState | null>(null);
  const [localPushState, setLocalPushState] = useState<"idle" | "submitting" | "queued">("idle");
  const [localPushActionError, setLocalPushActionError] = useState<PRActionErrorState | null>(null);
  const [pendingPRAction, setPendingPRAction] = useState<"fix_tests" | "resolve_conflicts" | "merge" | null>(null);
  const [repairActionError, setRepairActionError] = useState<string | null>(null);
  const [prAuthPrompt, setPRAuthPrompt] = useState<PRAuthPromptState | null>(null);
  const resumeAttemptRef = useRef<string | null>(null);
  const apiBase = process.env.NEXT_PUBLIC_API_URL || "";
  const isDocumentVisible = useDocumentVisible();

  const { data, isLoading, error } = useQuery({
    queryKey: ["session", id],
    queryFn: () => api.sessions.get(id),
    refetchInterval: (q) => {
      const s = q.state.data?.data;
      if (!s) return false;
      const serverInFlight = s.pr_creation_state === "queued" || s.pr_creation_state === "pushing";
      const waitingForServer = localPRState !== "idle" &&
        s.pr_creation_state !== "failed" &&
        s.pr_creation_state !== "succeeded";
      const pushInFlight = s.pr_push_state === "queued" || s.pr_push_state === "pushing";
      const waitingForPushServer = localPushState !== "idle" &&
        s.pr_push_state !== "failed" &&
        s.pr_push_state !== "succeeded";

      // Poll while PR creation OR push-changes is in flight so the state
      // machine advances without waiting for the user to navigate. Keep
      // polling during the optimistic local phases too, since the best-effort
      // queued write can legitimately lag the 202 response.
      return serverInFlight || waitingForServer || pushInFlight || waitingForPushServer ? 2000 : false;
    },
  });

  const { data: membersData } = useQuery({
    queryKey: ["team", "members"],
    queryFn: () => api.team.listMembers(),
  });

  const session = data?.data;
  const members = membersData?.data ?? [];
  const threads = useMemo(() => session?.threads ?? [], [session?.threads]);
  const [activeThreadId, setActiveThreadId] = useState<string | null>(null);
  const activeThread = threads.find((thread) => thread.id === activeThreadId) ?? threads[0];
  const activeThreadIndex = activeThread ? threads.findIndex((thread) => thread.id === activeThread.id) : -1;
  const isActive = session ? !terminalStatuses.has(session.status) : false;
  const isRunning = session?.status === "running";
  const currentTitle = session ? sessionTitle(session) : "";

  const queryClient = useQueryClient();
  const optimisticMessageIDRef = useRef(-1_000_000);
  const [optimisticMessages, setOptimisticMessages] = useState<PendingFollowUpMessage[]>([]);

  useEffect(() => {
    if (threads.length === 0) {
      if (activeThreadId !== null) {
        setActiveThreadId(null);
      }
      return;
    }
    if (!activeThreadId || !threads.some((thread) => thread.id === activeThreadId)) {
      setActiveThreadId(threads[0].id);
    }
  }, [activeThreadId, threads]);

  useEffect(() => {
    setOptimisticMessages([]);
  }, [id]);

  type SendMutationArgs = {
    activeThreadId?: string;
    body: {
      message: string;
      images?: string[];
      references?: SessionInputReference[];
      commands?: SessionInputCommand[];
      planMode?: boolean;
    };
    editableThreadUpdate?: {
      label: string;
      model: string | null;
    };
    model?: string;
    resolvedIDs: string[];
    optimisticMessage: PendingFollowUpMessage;
    composerSnapshot: {
      message: string;
      attachments: string[];
      references: SessionInputReference[];
      commands: SessionInputCommand[];
      planMode: boolean;
      selectedModel: string;
    };
  };

  const updateSessionMutation = useMutation({
    mutationFn: (title: string) => api.sessions.update(id, { title }),
    onSuccess: (response) => {
      queryClient.setQueryData<SingleResponse<SessionDetail>>(["session", id], (existing) => {
        if (!existing) return existing;
        return {
          ...existing,
          data: {
            ...existing.data,
            ...response.data,
            threads: existing.data.threads,
          },
        };
      });
      queryClient.invalidateQueries({ queryKey: ["session", id] });
      queryClient.invalidateQueries({ queryKey: ["session", id, "pr"] });
      queryClient.invalidateQueries({ queryKey: ["sessions"] });
      setIsEditingTitle(false);
      setDraftTitle(response.data.title ?? "");
    },
    onError: (err) => {
      const message = err instanceof ApiError ? err.message : "Failed to update session title";
      toast.error(message);
    },
  });

  // PR state for the detail-panel header button. Refetched on demand by the
  // pr_creation_state and pr_push_state effects below when the session SSE
  // reports a transition to `succeeded` (worker has written the PR row) or
  // `failed`. The session status stream is the source of truth for the state
  // machine, so a separate poll on this endpoint would just duplicate load on
  // Postgres without any latency win — Redis pub/sub already pushes the
  // transition within milliseconds, and the SSE polling fallback re-reads the
  // session row on a 1s tick when Redis is unavailable.
  const { data: prData } = useQuery({
    queryKey: ["session", id, "pr"],
    queryFn: () => api.sessions.getPR(id),
    // Updates flow in via mutation invalidations and the session SSE stream
    // (pr_creation_state / pr_push_state); a small staleTime suppresses
    // redundant refetches on remount or unrelated cache invalidations.
    staleTime: 30_000,
  });
  const pullRequestId = prData?.data?.id;
  const { data: prHealthData, isLoading: isPRHealthLoading } = useQuery({
    queryKey: ["pull-request", pullRequestId, "health"],
    queryFn: () => api.pullRequests.getHealth(pullRequestId!),
    enabled: !!pullRequestId && prData?.data?.status === "open",
    // Pushed via the PULL_REQUEST_UPDATED SSE event, so a longer staleTime is
    // safe — refetches on focus/remount won't fire a redundant network call.
    staleTime: 30_000,
  });
  const prHealth = prHealthData?.data;
  const prStatus = prData?.data?.status;
  const closedPRNumber = prData?.data?.github_pr_number;
  const closedPRSummary = closedPRNumber
    ? `PR #${closedPRNumber} was closed without merging.`
    : "This pull request was closed without merging.";
  const mergedPRNumber = prData?.data?.github_pr_number;
  const mergedPRSummary = mergedPRNumber
    ? `PR #${mergedPRNumber} was merged successfully.`
    : "This pull request was merged successfully.";

  // React to pr_creation_state transitions with toast feedback. Tracks the
  // previous value via ref so we fire once per transition rather than on
  // every render.
  const prevPRStateRef = useRef<string | undefined>(undefined);
  const prUrl = prData?.data?.github_pr_url;
  const serverPRState = session?.pr_creation_state;
  const localPRWaitingForServer =
    localPRState === "queued" &&
    serverPRState !== "failed" &&
    serverPRState !== "succeeded";
  const queueingPR = localPRState === "submitting";
  const creatingPR =
    !prUrl &&
    (localPRWaitingForServer || serverPRState === "queued" || serverPRState === "pushing");
  const finalizingPR = !prUrl && serverPRState === "succeeded";
  useEffect(() => {
    const prev = prevPRStateRef.current;
    const current = session?.pr_creation_state;
    if (prev && current && prev !== current) {
      if (current === "succeeded") {
        queryClient.invalidateQueries({ queryKey: ["session", id, "pr"] });
        toast.success("PR opened", prUrl ? {
          action: { label: "View \u2197", onClick: () => window.open(prUrl, "_blank", "noopener,noreferrer") },
        } : undefined);
      } else if (current === "failed") {
        toast.error(PR_ERROR_TOAST_MESSAGE, { duration: PR_ERROR_TOAST_DURATION_MS });
      }
    }
    prevPRStateRef.current = current;
  }, [session?.pr_creation_state, session?.pr_creation_error, prUrl, queryClient, id]);
  const prevPRUrlRef = useRef<string | undefined>(undefined);
  useEffect(() => {
    if (localPRState !== "idle" && prUrl && prevPRUrlRef.current !== prUrl && !session?.pr_creation_state) {
      toast.success("PR opened", {
        action: { label: "View \u2197", onClick: () => window.open(prUrl, "_blank", "noopener,noreferrer") },
      });
    }
    prevPRUrlRef.current = prUrl;
  }, [localPRState, prUrl, session?.pr_creation_state]);

  // React to pr_push_state transitions with toast feedback. Mirrors the
  // pr_creation_state effect above; kept separate so the two operations'
  // success/error messages don't collide when both fire on the same render.
  // Also clears localPushState when the server transitions out of in-flight
  // so the button returns to "Push changes" promptly without waiting for the
  // next polling tick.
  const prevPRPushStateRef = useRef<string | undefined>(undefined);
  useEffect(() => {
    const prev = prevPRPushStateRef.current;
    const current = session?.pr_push_state;
    if (prev && current && prev !== current) {
      if (current === "succeeded") {
        queryClient.invalidateQueries({ queryKey: ["session", id, "pr"] });
        if (pullRequestId) {
          queryClient.invalidateQueries({ queryKey: ["pull-request", pullRequestId, "health"] });
        }
        setLocalPushState("idle");
        toast.success("Changes pushed to PR", prUrl ? {
          action: { label: "View \u2197", onClick: () => window.open(prUrl, "_blank", "noopener,noreferrer") },
        } : undefined);
      } else if (current === "failed") {
        setLocalPushState("idle");
        toast.error(session?.pr_push_error || "Push to PR failed", { duration: PR_ERROR_TOAST_DURATION_MS });
      }
    }
    prevPRPushStateRef.current = current;
  }, [session?.pr_push_state, session?.pr_push_error, prUrl, queryClient, id, pullRequestId]);
  const startRepairMutation = useMutation({
    mutationFn: async (action: "fix_tests" | "resolve_conflicts") => {
      if (!pullRequestId) {
        throw new Error("Pull request not found");
      }
      return action === "fix_tests"
        ? api.pullRequests.fixTests(pullRequestId)
        : api.pullRequests.resolveConflicts(pullRequestId);
    },
    onMutate: (action) => {
      setRepairActionError(null);
      setPendingPRAction(action);
    },
    onSuccess: (response) => {
      setPendingPRAction(null);
      void queryClient.invalidateQueries({ queryKey: ["pull-request", pullRequestId, "health"] });
      void queryClient.invalidateQueries({ queryKey: ["session", id] });
      void queryClient.invalidateQueries({ queryKey: ["session", id, "timeline"] });
      void queryClient.invalidateQueries({ queryKey: ["session", id, "pr"] });

      if (response.data.session_id !== id) {
        router.push(`/sessions/${response.data.session_id}`);
        return;
      }
      // Same-session response: without explicit feedback the click looks
      // dead. Reused-in-flight is the common case (repair already running on
      // this session and the failing-tests count hasn't dropped yet, so the
      // button is still rendered) — surface a toast so the user knows the
      // request was handled.
      if (response.data.reused_in_flight) {
        const label = response.data.repair_action_type === "fix_tests"
          ? "Fix tests session is already in progress"
          : "Resolve conflicts session is already in progress";
        toast.info(label);
      }
    },
    onError: (err) => {
      setPendingPRAction(null);
      setRepairActionError(err instanceof ApiError ? err.message : "Failed to open repair session");
    },
  });
  // mergeMutation is intentionally separate from startRepairMutation: a
  // successful merge has no follow-up session to navigate to and surfaces a
  // distinct toast. Both mutations share the same pendingPRAction so the
  // banner can disable every button while one is in flight.
  const mergeMutation = useMutation({
    mutationFn: async () => {
      if (!pullRequestId) {
        throw new Error("Pull request not found");
      }
      return api.pullRequests.merge(pullRequestId);
    },
    onMutate: () => {
      setRepairActionError(null);
      setPendingPRAction("merge");
    },
    onSuccess: () => {
      setPendingPRAction(null);
      void queryClient.invalidateQueries({ queryKey: ["pull-request", pullRequestId, "health"] });
      void queryClient.invalidateQueries({ queryKey: ["session", id] });
      void queryClient.invalidateQueries({ queryKey: ["session", id, "pr"] });
      void queryClient.invalidateQueries({ queryKey: ["sessions"] });
      toast.success("PR merged", prUrl ? {
        action: { label: "View \u2197", onClick: () => window.open(prUrl, "_blank", "noopener,noreferrer") },
      } : undefined);
    },
    onError: (err) => {
      setPendingPRAction(null);
      // Surface merge failures via toast rather than the banner's repairError
      // slot. Merge errors (branch protection, head-SHA race, GitHub 503)
      // typically resolve themselves on the next health refetch — by the time
      // the user reads the message the banner has already updated, so an
      // in-banner error would feel stale alongside the new state.
      const message = err instanceof ApiError ? err.message : "Failed to merge pull request";
      toast.error(message);
    },
  });
  useEffect(() => {
    // Pause the PR health SSE stream while the tab is hidden — same reasoning
    // as the session log stream above. The onerror branch already invalidates
    // the health query on disconnect, so reconnecting on visibility refreshes
    // the cached health to whatever happened while we were away.
    if (!pullRequestId || prData?.data?.status !== "open" || !isDocumentVisible) {
      return;
    }

    let eventSource: EventSource | null = null;
    let cancelled = false;
    let reconnectAttempts = 0;
    let reconnectTimer: ReturnType<typeof setTimeout> | null = null;

    function connect() {
      if (cancelled) {
        return;
      }

      eventSource = new EventSource(buildPullRequestStreamURL(apiBase, getActiveOrgId()), { withCredentials: true });
      eventSource.onopen = () => {
        reconnectAttempts = 0;
      };
      addSSEListener(eventSource, SSE_EVENT.PULL_REQUEST_UPDATED, (event) => {
        if (event.pull_request_id !== pullRequestId) {
          return;
        }

        const cached = queryClient.getQueryData<SingleResponse<PullRequestHealthResponse>>(["pull-request", pullRequestId, "health"]);
        const cachedVersion = cached?.data?.health_version ?? 0;
        if (event.version < cachedVersion) {
          return;
        }

        void queryClient.invalidateQueries({ queryKey: ["pull-request", pullRequestId, "health"] });
      });
      eventSource.onerror = () => {
        eventSource?.close();
        void queryClient.invalidateQueries({ queryKey: ["pull-request", pullRequestId, "health"] });

        if (!cancelled && reconnectAttempts < MAX_SSE_RECONNECT_ATTEMPTS) {
          const delay = BASE_SSE_RECONNECT_DELAY_MS * Math.pow(2, reconnectAttempts);
          reconnectAttempts += 1;
          reconnectTimer = setTimeout(connect, delay);
        }
      };
    }

    connect();

    return () => {
      cancelled = true;
      eventSource?.close();
      if (reconnectTimer) {
        clearTimeout(reconnectTimer);
      }
    };
  }, [apiBase, prData?.data?.status, pullRequestId, queryClient, isDocumentVisible]);
  const previousSessionStatusRef = useRef<string | undefined>(undefined);
  useEffect(() => {
    const currentStatus = session?.status;
    if (!session?.id || !currentStatus) {
      return;
    }

    void maybeNotifySessionCompleted({
      previousStatus: previousSessionStatusRef.current,
      nextStatus: currentStatus,
      sessionId: session.id,
      title: session.title,
      visibilityState: document.visibilityState,
    });

    previousSessionStatusRef.current = currentStatus;
  }, [session?.id, session?.status, session?.title]);
  // Record that the user has viewed this session (for unread tracking).
  useEffect(() => {
    if (id) {
      api.sessions.recordView(id).then(() => {
        queryClient.invalidateQueries({ queryKey: ["sessions"] });
      }).catch(() => {});
    }
  }, [id, queryClient]);

  const hasPR = !!prData?.data;
  const hasSnapshot = !!session?.snapshot_key;
  const hasSessionChanges = !!session?.diff || !!session?.diff_stats;
  const canCreatePR = hasSnapshot && !hasPR && !isRunning;
  const isTerminalSession = terminalSessionStatuses.has(session?.status ?? "");
  const showExpiredPRAction = hasSessionChanges && !hasSnapshot && !hasPR && isTerminalSession;
  const needsGitHubStatus = canCreatePR || (hasPR && prData?.data?.status === "open");

  const { data: ghStatus } = useQuery({
    queryKey: ["github-status"],
    queryFn: () => api.githubStatus.get(),
    enabled: needsGitHubStatus,
    staleTime: 5 * 60 * 1000,
  });

  const clearPRResumeParams = useCallback(() => {
    void setResumePRParam(null);
    void setResumeActionParam(null);
    if (githubPRParam === "connected") {
      void setGithubPRParam(null);
    }
  }, [githubPRParam, setGithubPRParam, setResumePRParam, setResumeActionParam]);

  const createPRMutation = useMutation({
    mutationFn: (options?: { draft?: boolean; authorMode?: PRAuthorMode; resumeToken?: string }) =>
      api.sessions.createPR(id, options),
    onMutate: () => {
      setLocalPRActionError(null);
      setLocalPRState("submitting");
    },
    onSuccess: (_data, options) => {
      setLocalPRActionError(null);
      setLocalPRState("queued");
      if (options?.resumeToken) {
        clearPRResumeParams();
      }
      queryClient.invalidateQueries({ queryKey: ["session", id] });
      queryClient.invalidateQueries({ queryKey: ["session", id, "pr"] });
    },
    onError: (err, options) => {
      if (err instanceof ApiError &&
        (err.code === "GITHUB_PR_AUTHORSHIP_REQUIRED" || err.code === "GITHUB_PR_AUTHORSHIP_REAUTH_REQUIRED") &&
        isPRAuthInterceptDetails(err.details)) {
        setLocalPRState("idle");
        setLocalPRActionError(null);
        setPRAuthPrompt({ ...err.details, purpose: "create_pr" });
        clearPRResumeParams();
        return;
      }
      setLocalPRState("idle");
      const msg = err instanceof Error ? err.message : PR_ERROR_TOAST_MESSAGE;
      setLocalPRActionError({
        code: err instanceof ApiError ? err.code : undefined,
        message: msg,
      });
      if (options?.resumeToken) {
        clearPRResumeParams();
      }
      toast.error(PR_ERROR_TOAST_MESSAGE, { duration: PR_ERROR_TOAST_DURATION_MS });
    },
  });

  const pushChangesMutation = useMutation({
    mutationFn: (options?: { authorMode?: PRAuthorMode; resumeToken?: string }) =>
      api.sessions.pushChangesToPR(id, options),
    onMutate: () => {
      setLocalPushActionError(null);
      setLocalPushState("submitting");
    },
    onSuccess: (_data, options) => {
      setLocalPushActionError(null);
      setLocalPushState("queued");
      if (options?.resumeToken) {
        clearPRResumeParams();
      }
      queryClient.invalidateQueries({ queryKey: ["session", id] });
    },
    onError: (err, options) => {
      if (err instanceof ApiError &&
        (err.code === "GITHUB_PR_AUTHORSHIP_REQUIRED" || err.code === "GITHUB_PR_AUTHORSHIP_REAUTH_REQUIRED") &&
        isPRAuthInterceptDetails(err.details)) {
        setLocalPushState("idle");
        setLocalPushActionError(null);
        setPRAuthPrompt({ ...err.details, purpose: "push_changes" });
        clearPRResumeParams();
        return;
      }
      setLocalPushState("idle");
      const msg = err instanceof Error ? err.message : "Push to PR failed";
      setLocalPushActionError({
        code: err instanceof ApiError ? err.code : undefined,
        message: msg,
      });
      if (options?.resumeToken) {
        clearPRResumeParams();
      }
      toast.error(msg, { duration: PR_ERROR_TOAST_DURATION_MS });
    },
  });

  useEffect(() => {
    if (!resumePRParam) return;
    if (resumeAttemptRef.current === resumePRParam) return;
    // Prefer the action recorded in the resume_action URL param: it was
    // signed into the resume token at the originating endpoint and forwarded
    // by the OAuth callback, so the replay is deterministic regardless of
    // any state change during the GitHub round-trip (e.g. another tab
    // creating the PR, or the PR getting closed). Fall back to the current
    // PR state for legacy tokens that predate the resume_action param.
    const action: "create_pr" | "push_changes" =
      resumeActionParam === "push_changes"
        ? "push_changes"
        : resumeActionParam === "create_pr"
          ? "create_pr"
          : hasPR && prStatus === "open" && !!session?.has_unpushed_changes
            ? "push_changes"
            : "create_pr";
    if (action === "push_changes") {
      // Mirror the canCreatePR gate on the create branch: don't fire the
      // mutation until the session has a snapshot and isn't mid-turn.
      // Without this, the OAuth callback firing while the session is still
      // running would land an immediate 409 (or stale-snapshot error). The
      // effect re-runs when these dependencies flip, so the replay still
      // happens — just on the next tick when the session is actually ready.
      const pushAvailable = hasPR && prStatus === "open" && !!session?.has_unpushed_changes;
      if (!pushAvailable || !hasSnapshot || isRunning) return;
      resumeAttemptRef.current = resumePRParam;
      pushChangesMutation.mutate({ authorMode: "user", resumeToken: resumePRParam });
      return;
    }
    if (!canCreatePR) return;
    resumeAttemptRef.current = resumePRParam;
    createPRMutation.mutate({ authorMode: "user", resumeToken: resumePRParam });
  }, [canCreatePR, createPRMutation, hasPR, hasSnapshot, isRunning, prStatus, pushChangesMutation, resumeActionParam, resumePRParam, session?.has_unpushed_changes]);

  const sessionDiff = session?.diff;
  const diffStats = useMemo(() => {
    if (!sessionDiff) return null;
    return parseDiffStats(sessionDiff);
  }, [sessionDiff]);

  // --- Shared review state (lifted from old ChangesTab) ---

  // Hooks can't be called conditionally, so provide a stub when session hasn't loaded yet.
  // useDiffViewState only reads `diff` and `diff_history` — the stub satisfies that contract.
  const diffViewState = useDiffViewState(session ?? { diff: null, diff_history: [] } as unknown as Session);
  const {
    files: diffFiles,
    filteredFiles,
    passes,
    passRange,
    setPassRange,
    diffSearchQuery,
    setDiffSearchQuery,
  } = diffViewState;

  const {
    comments,
    commentsByLine,
    createComment,
    updateComment,
    deleteComment,
  } = useReviewComments(id);

  const [activeCommentLine, setActiveCommentLine] = useState<{
    filePath: string;
    lineNumber: number;
    side: "old" | "new";
  } | null>(null);

  const handleAddComment = useCallback(
    (filePath: string, lineNumber: number, side: "old" | "new") => {
      setActiveCommentLine({ filePath, lineNumber, side });
    },
    []
  );

  const handleSubmitComment = useCallback(
    (body: string) => {
      if (!activeCommentLine) return;
      createComment({
        file_path: activeCommentLine.filePath,
        line_number: activeCommentLine.lineNumber,
        side: activeCommentLine.side,
        body,
      });
      setActiveCommentLine(null);
    },
    [activeCommentLine, createComment]
  );

  const handleCancelComment = useCallback(() => {
    setActiveCommentLine(null);
  }, []);

  const handleCommentClick = useCallback(
    (filePath: string) => {
      const fileIndex = filteredFiles.findIndex((f) => f.newPath === filePath);
      if (fileIndex >= 0) {
        openReview(fileIndex);
      }
    },
    [filteredFiles, openReview]
  );

  const [composerMessage, setComposerMessage] = useState("");
  const [composerPlanMode, setComposerPlanMode] = useState(false);
  const [composerSelectedModel, setComposerSelectedModel] = useState("");
  const [composerAttachments, setComposerAttachments] = useState<string[]>([]);
  const [composerReferences, setComposerReferences] = useState<SessionInputReference[]>([]);
  const [composerCommands, setComposerCommands] = useState<SessionInputCommand[]>([]);
  const [composerIsUploading, setComposerIsUploading] = useState(false);
  const [composerUploadError, setComposerUploadError] = useState<string | null>(null);
  const composerTextareaRef = useRef<HTMLTextAreaElement>(null);
  const composerUploadInputRef = useRef<HTMLInputElement>(null);
  // Tracks an in-flight agent-switch PATCH so the send-time PATCH can wait
  // for it before issuing its own update. Without this, a fast send right
  // after toggling the agent dropdown can race the agent-switch PATCH and
  // overwrite the new agent_type with the send-time {label, model} body.
  const inFlightAgentUpdateRef = useRef<Promise<unknown> | null>(null);
  const chatPanelScrollToLiveEdgeRef = useRef<(() => void) | null>(null);
  // Open comments are the source of truth for what gets attached to the next
  // message — once a send succeeds, the backend marks them resolved in the
  // same transaction, the comments query is invalidated below, and the next
  // refetch flips them out of openComments. No local "dismissed" state is
  // needed (and would be wrong: it wouldn't survive page reloads).
  const attachedReviewComments = useMemo(
    () => comments.filter((comment) => !comment.resolved).slice(0, MAX_RESOLVE_REVIEW_COMMENTS_PER_MESSAGE),
    [comments],
  );
  // Composer gating: messages may be sent at any point while the session or
  // thread is running. The backend queues mid-turn sends and the orchestrator
  // drains the queue once the in-flight turn completes. Pending/skipped at
  // the session level and a destroyed sandbox still block — those are
  // genuinely unrecoverable, not just busy.
  const composerCanSendMessage = session?.status !== "skipped" &&
    session?.status !== "pending" &&
    session?.sandbox_state !== "destroyed";
  const composerIsRunning = activeThread ? activeThread.status === "running" : session?.status === "running";
  const composerIsSnapshotExpired = session?.sandbox_state === "destroyed";
  const composerAgentType = activeThread?.agent_type ?? session?.agent_type ?? "codex";
  const activeThreadIsEditable = !!activeThread && activeThread.status === "idle" && activeThread.current_turn === 0;
  const composerIsClaudeCode = composerAgentType === "claude_code";
  const composerLacksHeadlessResume = AGENTS_BY_KEY[composerAgentType]?.lacksHeadlessResume ?? false;
  const composerAvailableModels = useMemo(() => {
    if (!session) {
      return [];
    }
    const agentType = AGENTS.find((agent) => agent.key === composerAgentType);
    return agentType?.models ?? [];
  }, [composerAgentType, session]);
  const activeThreadLabel = activeThread?.label ?? (session ? AGENTS_BY_KEY[session.agent_type]?.label ?? session.agent_type : "agent");
  const buildThreadLabelForAgent = useCallback((agentType: string) => {
    const agent = AGENTS_BY_KEY[agentType] ?? AGENTS[0];
    const ordinal = activeThreadIndex >= 0 ? activeThreadIndex + 1 : threads.length + 1;
    return `${agent.label} ${ordinal}`;
  }, [activeThreadIndex, threads.length]);
  const buildDefaultThreadRequest = useCallback(() => {
    const agentType = activeThread?.agent_type ?? session?.agent_type ?? "codex";
    const agent = AGENTS_BY_KEY[agentType] ?? AGENTS[0];
    return {
      agent_type: agent.key,
      model: activeThread?.agent_type === agent.key ? activeThread.model_override : undefined,
      label: `${agent.label} ${threads.length + 1}`,
    };
  }, [activeThread?.agent_type, activeThread?.model_override, session?.agent_type, threads.length]);
  const pendingEditableThreadUpdate = useMemo(() => {
    return getPendingEditableThreadUpdate(activeThread, activeThreadIsEditable, composerSelectedModel);
  }, [activeThread, activeThreadIsEditable, composerSelectedModel]);

  async function uploadComposerFiles(files: File[]) {
    if (files.length === 0) return;

    const oversized = files.filter((file) => file.size > MAX_FILE_SIZE);
    if (oversized.length > 0) {
      setComposerUploadError(`File${oversized.length > 1 ? "s" : ""} too large (max 10 MB): ${oversized.map((file) => file.name).join(", ")}`);
      return;
    }

    setComposerIsUploading(true);
    setComposerUploadError(null);
    try {
      const results = await Promise.all(files.map((file) => api.uploads.upload(file)));
      setComposerAttachments((previous) => [...previous, ...results.map((result) => result.url)]);
    } catch (err) {
      setComposerUploadError(err instanceof Error ? err.message : "Upload failed");
    } finally {
      setComposerIsUploading(false);
    }
  }

  async function handleComposerUpload(event: React.ChangeEvent<HTMLInputElement>) {
    const fileList = event.target.files;
    if (!fileList || fileList.length === 0) return;

    await uploadComposerFiles(Array.from(fileList));
    event.target.value = "";
  }

  const handleRemoveComposerAttachment = useCallback((url: string) => {
    setComposerAttachments((previous) => previous.filter((attachment) => attachment !== url));
  }, []);

  const handleAddComposerAttachment = useCallback((url: string) => {
    setComposerAttachments((previous) => [...previous, url]);
  }, []);

  const sendMutation = useMutation({
    mutationFn: async (vars: SendMutationArgs) => {
      if (vars.activeThreadId) {
        // Drain a pending agent-switch PATCH first so the send-time PATCH
        // doesn't clobber the new agent_type. Swallow its rejection — the
        // agent-switch mutation already surfaces its own toast.
        if (inFlightAgentUpdateRef.current) {
          try {
            await inFlightAgentUpdateRef.current;
          } catch {
            // already surfaced by the agent-switch mutation
          }
        }
        if (vars.editableThreadUpdate) {
          await updateThreadMutation.mutateAsync({
            threadId: vars.activeThreadId,
            body: vars.editableThreadUpdate,
          });
        }
        const response = await api.sessions.sendThreadMessage(id, vars.activeThreadId, {
          ...vars.body,
          resolveReviewCommentIDs: vars.resolvedIDs.length > 0 ? vars.resolvedIDs : undefined,
        });
        return { response, resolvedIDs: vars.resolvedIDs };
      }

      const response = await api.sessions.sendMessage(id, {
        ...vars.body,
        model: vars.model,
        resolveReviewCommentIDs: vars.resolvedIDs.length > 0 ? vars.resolvedIDs : undefined,
      });
      return { response, resolvedIDs: vars.resolvedIDs };
    },
    onMutate: (vars) => {
      setComposerUploadError(null);
      setOptimisticMessages((previous) => [...previous, vars.optimisticMessage]);
      setComposerMessage("");
      setComposerAttachments([]);
      setComposerReferences([]);
      setComposerCommands([]);
      setComposerPlanMode(false);
      if (centerMode === "review") {
        exitReview();
      }
      if (composerTextareaRef.current) {
        composerTextareaRef.current.style.height = "auto";
      }
      chatPanelScrollToLiveEdgeRef.current?.();
      return {
        optimisticMessageID: vars.optimisticMessage.client_id,
        composerSnapshot: vars.composerSnapshot,
      };
    },
    onSuccess: ({ response, resolvedIDs }, vars, context) => {
      setOptimisticMessages((previous) => previous.filter((message) => message.client_id !== context?.optimisticMessageID));
      if (vars.activeThreadId) {
        queryClient.setQueryData<ListResponse<SessionMessage>>(
          queryKeys.sessions.threadMessages(id, vars.activeThreadId),
          (previous) => {
            const existing = previous?.data ?? [];
            const responseKey = messageReconciliationKey(response.data);
            const withoutDuplicate = existing.filter((message) =>
              message.id !== response.data.id && messageReconciliationKey(message) !== responseKey
            );
            return {
              data: [...withoutDuplicate, response.data],
              meta: previous?.meta ?? {},
            };
          },
        );
      } else {
        queryClient.setQueryData<ListResponse<SessionTimelineEntry>>(
          queryKeys.sessions.timeline(id),
          (previous) => {
            const existing = previous?.data ?? [];
            const responseKey = messageReconciliationKey(response.data);
            const withoutDuplicate = existing.filter((entry) =>
              entry.kind !== "message" ||
              (
                entry.message?.id !== response.data.id &&
                messageReconciliationKey(entry.message!) !== responseKey
              )
            );
            return {
              data: [
                ...withoutDuplicate,
                {
                  kind: "message",
                  created_at: response.data.created_at,
                  message: response.data,
                },
              ],
              meta: previous?.meta ?? {},
            };
          },
        );
      }
      queryClient.invalidateQueries({ queryKey: ["session", id] });
      queryClient.invalidateQueries({ queryKey: ["session", id, "timeline"] });
      if (vars.activeThreadId) {
        queryClient.invalidateQueries({ queryKey: queryKeys.sessions.threadMessages(id, vars.activeThreadId) });
        queryClient.invalidateQueries({ queryKey: queryKeys.sessions.threadLogs(id, vars.activeThreadId) });
      }
      // Backend resolved the attached review comments inside the same tx as
      // the message. Optimistically flip them to resolved=true in the cache
      // so the "N comments attached" banner disappears immediately, then
      // invalidate to reconcile with the canonical server state.
      if (resolvedIDs.length > 0) {
        const resolvedSet = new Set(resolvedIDs);
        queryClient.setQueryData<ListResponse<SessionReviewComment>>(
          ["session", id, "review-comments"],
          (previous) => {
            if (!previous) return previous;
            return {
              ...previous,
              data: previous.data.map((comment) =>
                resolvedSet.has(comment.id) ? { ...comment, resolved: true } : comment
              ),
            };
          }
        );
      }
      queryClient.invalidateQueries({ queryKey: ["session", id, "review-comments"] });
    },
    onError: (_err, _vars, context) => {
      setOptimisticMessages((previous) => previous.filter((message) => message.client_id !== context?.optimisticMessageID));
      if (!context) return;
      setComposerMessage(context.composerSnapshot.message);
      setComposerAttachments(context.composerSnapshot.attachments);
      setComposerReferences(context.composerSnapshot.references);
      setComposerCommands(context.composerSnapshot.commands);
      setComposerPlanMode(context.composerSnapshot.planMode);
      setComposerSelectedModel(context.composerSnapshot.selectedModel);
    },
  });

  const queueSend = useCallback((opts: { planMode?: boolean; overrideMessage?: string } = {}) => {
    if (!session) return;

    const draftMessage = opts.overrideMessage ?? composerMessage;
    const userFacingMessage = attachedReviewComments.length > 0
      ? formatReviewMessage(attachedReviewComments, filteredFiles, draftMessage)
      : draftMessage;
    const isPlanRequest = opts.planMode ?? composerPlanMode;
    const formattedMessage = applyPlanModePrefix(userFacingMessage, isPlanRequest);
    const resolvedIDs = attachedReviewComments.map((comment) => comment.id);
    const optimisticID = optimisticMessageIDRef.current;
    optimisticMessageIDRef.current -= 1;

    sendMutation.mutate({
      activeThreadId: activeThread?.id,
      body: {
        message: userFacingMessage,
        images: composerAttachments.length > 0 ? composerAttachments : undefined,
        references: composerReferences.length > 0 ? composerReferences : undefined,
        commands: composerCommands.length > 0 ? composerCommands : undefined,
        planMode: isPlanRequest,
      },
      editableThreadUpdate: activeThread?.id ? pendingEditableThreadUpdate : undefined,
      model: composerSelectedModel || undefined,
      resolvedIDs,
      optimisticMessage: {
        client_id: optimisticID,
        id: optimisticID,
        session_id: id,
        org_id: session.org_id,
        thread_id: activeThread?.id,
        turn_number: (activeThread?.current_turn ?? session.current_turn ?? 0) + 1,
        role: "user",
        content: formattedMessage,
        attachments: composerAttachments.length > 0 ? composerAttachments : undefined,
        references: composerReferences.length > 0 ? composerReferences : undefined,
        commands: composerCommands.length > 0 ? composerCommands : undefined,
        created_at: new Date().toISOString(),
      },
      composerSnapshot: {
        message: composerMessage,
        attachments: composerAttachments,
        references: composerReferences,
        commands: composerCommands,
        planMode: composerPlanMode,
        selectedModel: composerSelectedModel,
      },
    });
  }, [
    activeThread,
    attachedReviewComments,
    composerAttachments,
    composerCommands,
    composerMessage,
    composerPlanMode,
    composerReferences,
    composerSelectedModel,
    filteredFiles,
    id,
    pendingEditableThreadUpdate,
    session,
    sendMutation,
  ]);
  const queueSendRef = useRef(queueSend);
  const composerCanSendMessageRef = useRef(composerCanSendMessage);
  const sendPendingRef = useRef(sendMutation.isPending);

  useEffect(() => {
    queueSendRef.current = queueSend;
  }, [queueSend]);

  useEffect(() => {
    composerCanSendMessageRef.current = composerCanSendMessage;
  }, [composerCanSendMessage]);

  useEffect(() => {
    sendPendingRef.current = sendMutation.isPending;
  }, [sendMutation.isPending]);

  const cancelMutation = useMutation({
    mutationFn: () => api.sessions.cancelSession(id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["session", id] });
    },
  });

  // Thread-scoped mutations for Phase 3/4 actions. Kept as separate
  // mutations rather than one polymorphic call so React Query can scope
  // pending state per-action, which the menu reads to show a spinner only
  // on the in-flight item.
  const cancelThreadMutation = useMutation({
    mutationFn: (threadId: string) => api.sessions.cancelThread(id, threadId),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["session", id] });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to cancel tab");
    },
  });
  const forkThreadMutation = useMutation({
    mutationFn: (threadId: string) => api.sessions.forkThread(id, threadId),
    onSuccess: () => {
      toast.success("Fork queued — new session will appear in your list shortly");
      queryClient.invalidateQueries({ queryKey: ["sessions"] });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to fork tab");
    },
  });
  const revertThreadMutation = useMutation({
    mutationFn: (threadId: string) => api.sessions.revertThread(id, threadId),
    onSuccess: (_data, threadId) => {
      toast.success("Revert prepared — see the tab transcript for the patch");
      queryClient.invalidateQueries({ queryKey: queryKeys.sessions.threadMessages(id, threadId) });
      queryClient.invalidateQueries({ queryKey: ["session", id] });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to prepare revert");
    },
  });

  // Session-wide file event timeline powers the tab-strip overlap badges and
  // the Changes-view attribution filters. Polled at the same cadence as the
  // session detail so a user-perceptible "tab touched a file" lands within
  // one polling cycle.
  //
  // Polling is incremental: the first request fetches the whole timeline,
  // subsequent requests pass `?since=<latest observed_at>` so a long session
  // does not retransfer hundreds of events every 5 seconds. The accumulated
  // list lives in component state because React Query caches only the most
  // recent response, which is now a delta.
  const fileEventsSinceRef = useRef<string | undefined>(undefined);
  const [accumulatedFileEvents, setAccumulatedFileEvents] = useState<SessionThreadFileEvent[]>([]);
  const fileEventsQuery = useQuery({
    queryKey: queryKeys.sessions.threadFileEvents(id),
    queryFn: () => api.sessions.listThreadFileEvents(id, fileEventsSinceRef.current),
    enabled: threads.length > 0,
    refetchInterval: threads.some((t) => t.status === "running" || t.status === "pending") ? 5000 : false,
    staleTime: 2_000,
  });
  useEffect(() => {
    const incoming = fileEventsQuery.data?.data;
    if (!incoming || incoming.length === 0) return;
    setAccumulatedFileEvents((prev) => {
      const byId = new Map<number, SessionThreadFileEvent>();
      for (const e of prev) byId.set(e.id, e);
      for (const e of incoming) byId.set(e.id, e);
      return Array.from(byId.values()).sort((a, b) => b.observed_at.localeCompare(a.observed_at));
    });
    let max = fileEventsSinceRef.current;
    for (const e of incoming) {
      if (!max || e.observed_at > max) max = e.observed_at;
    }
    fileEventsSinceRef.current = max;
  }, [fileEventsQuery.data]);
  const overlapsByThreadId = useMemo(
    () => computeThreadOverlap(threads, accumulatedFileEvents),
    [threads, accumulatedFileEvents],
  );
  const [attributionFilter, setAttributionFilter] = useState<ThreadAttributionFilterValue>({ kind: "all" });
  const attributionAllowedPaths = useAttributionAllowedPaths(attributionFilter, accumulatedFileEvents);
  const visibleDiffFiles = useMemo(
    () =>
      attributionAllowedPaths == null
        ? diffFiles
        : diffFiles.filter((f) => attributionAllowedPaths.has(f.newPath) || attributionAllowedPaths.has(f.oldPath)),
    [attributionAllowedPaths, diffFiles],
  );
  const visibleFilteredFiles = useMemo(
    () =>
      attributionAllowedPaths == null
        ? filteredFiles
        : filteredFiles.filter((f) => attributionAllowedPaths.has(f.newPath) || attributionAllowedPaths.has(f.oldPath)),
    [attributionAllowedPaths, filteredFiles],
  );

  useEffect(() => {
    if (visibleFilteredFiles.length === 0) {
      if (activeFileIndex !== 0) {
        setActiveFileIndex(0);
      }
      return;
    }
    if (activeFileIndex >= visibleFilteredFiles.length) {
      setActiveFileIndex(visibleFilteredFiles.length - 1);
    }
  }, [activeFileIndex, visibleFilteredFiles.length]);

  const createThreadMutation = useMutation({
    mutationFn: (body: { agent_type: string; model?: string; label: string }) => api.sessions.createThread(id, body),
    onSuccess: (response) => {
      queryClient.setQueryData<SingleResponse<SessionDetail>>(["session", id], (existing) => {
        if (!existing) return existing;
        const existingThreads = existing.data.threads ?? [];
        return {
          ...existing,
          data: {
            ...existing.data,
            threads: [...existingThreads.filter((thread) => thread.id !== response.data.id), response.data],
          },
        };
      });
      setActiveThreadId(response.data.id);
      setComposerSelectedModel(getInitialComposerSelectedModel(response.data));
      queryClient.invalidateQueries({ queryKey: ["session", id] });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to create tab");
    },
  });
  const updateThreadMutation = useMutation({
    mutationFn: (vars: { threadId: string; body: { agent_type?: string; model?: string | null; label: string } }) =>
      api.sessions.updateThread(id, vars.threadId, vars.body),
    onSuccess: (response) => {
      queryClient.setQueryData<SingleResponse<SessionDetail>>(["session", id], (existing) => {
        if (!existing) return existing;
        const existingThreads = existing.data.threads ?? [];
        return {
          ...existing,
          data: {
            ...existing.data,
            threads: existingThreads.map((thread) => thread.id === response.data.id ? response.data : thread),
          },
        };
      });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to update tab");
    },
  });

  const handleEditableAgentTypeChange = useCallback((nextAgentType: string) => {
    if (!activeThread || !activeThreadIsEditable || nextAgentType === activeThread.agent_type) {
      return;
    }
    setComposerPlanMode(false);
    setComposerSelectedModel("");
    // Only regenerate the label if it matches the autogenerated shape
    // exactly — `${agent.label} <ordinal>`. A `startsWith` check would
    // wrongly rename user-customized labels like "Codex deep dive".
    const currentAgent = AGENTS_BY_KEY[activeThread.agent_type];
    const autogenSuffix = currentAgent ? activeThread.label.slice(currentAgent.label.length + 1) : "";
    const looksAutogenerated = !!currentAgent
      && activeThread.label.startsWith(`${currentAgent.label} `)
      && /^\d+$/.test(autogenSuffix);
    const nextLabel = looksAutogenerated ? buildThreadLabelForAgent(nextAgentType) : activeThread.label;
    const promise = updateThreadMutation.mutateAsync({
      threadId: activeThread.id,
      body: {
        agent_type: nextAgentType,
        label: nextLabel,
      },
    });
    trackInFlightAgentUpdate(inFlightAgentUpdateRef, promise);
  }, [activeThread, activeThreadIsEditable, buildThreadLabelForAgent, updateThreadMutation]);

  const handleApprovePlan = useCallback(() => {
    if (!composerCanSendMessageRef.current || sendPendingRef.current) return;
    queueSendRef.current({
      planMode: false,
      overrideMessage: "The plan looks good. Please proceed with executing the implementation plan above. Make all the changes as described.",
    });
  }, []);

  const handleAdjustPlan = useCallback(() => {
    setComposerMessage("Please adjust the plan: ");
    setComposerPlanMode(false);
    composerTextareaRef.current?.focus();
  }, []);
  const handleChatDiffClick = useCallback(() => {
    openReview();
  }, [openReview]);
  const registerChatPanelScrollToLiveEdge = useCallback((scrollToLiveEdge: (() => void) | null) => {
    chatPanelScrollToLiveEdgeRef.current = scrollToLiveEdge;
  }, []);

  const changesCount = diffStats?.filesChanged;
  const showValidationTab = !session?.triggered_by_user_id;
  const detailTabsRef = useRef<HTMLDivElement>(null);
  const [detailTabsOverflow, setDetailTabsOverflow] = useState(false);

  const checkDetailTabsOverflow = useCallback(() => {
    const el = detailTabsRef.current;
    if (el) {
      setDetailTabsOverflow(el.scrollWidth > el.clientWidth);
    }
  }, []);

  useEffect(() => {
    checkDetailTabsOverflow();

    const handleResize = () => checkDetailTabsOverflow();
    window.addEventListener("resize", handleResize);

    if (typeof ResizeObserver === "undefined") {
      return () => window.removeEventListener("resize", handleResize);
    }

    const observer = new ResizeObserver(() => {
      checkDetailTabsOverflow();
    });

    if (detailTabsRef.current) {
      observer.observe(detailTabsRef.current);
    }

    return () => {
      observer.disconnect();
      window.removeEventListener("resize", handleResize);
    };
  }, [
    changesCount,
    checkDetailTabsOverflow,
    hasPR,
    session?.id,
    session?.pr_creation_error,
    session?.pr_creation_state,
    showValidationTab,
  ]);
  const isDedicatedMobileReview = centerMode === "review" && isMobileReviewViewport;

  useEffect(() => {
    if (!isDedicatedMobileReview) {
      setMobileReviewComposerOpen(false);
    }
  }, [isDedicatedMobileReview]);

  if (isLoading) {
    return (
      <div className="flex items-center justify-center h-full">
        <div className="text-center space-y-2">
          <Loader2 className="h-5 w-5 animate-spin text-muted-foreground/40 mx-auto" />
          <p className="text-xs text-muted-foreground">Loading session...</p>
        </div>
      </div>
    );
  }

  if (error || !session) {
    return (
      <div className="flex items-center justify-center h-full">
        <div className="text-center space-y-2 max-w-[280px]">
          <AlertTriangle className="h-5 w-5 text-muted-foreground/40 mx-auto" />
          <p className="text-xs font-medium text-muted-foreground">Failed to load session</p>
          <p className="text-xs text-muted-foreground/60">The session could not be found or an error occurred.</p>
        </div>
      </div>
    );
  }

  const status = statusConfig[session.status] || statusConfig.pending;
  const prState = session.pr_creation_state;
  const snapshotState = classifyPRSnapshotState({
    sessionSnapshotKey: session.snapshot_key,
    sessionSandboxState: session.sandbox_state,
    serverMessage: session.pr_creation_error,
    localCode: localPRActionError?.code,
    allowImplicitMissingSnapshot: showExpiredPRAction,
  });
  const snapshotUnavailable = snapshotState !== null;
  const snapshotMessage = snapshotPRMessage(
    snapshotState,
    localPRActionError?.code ? localPRActionError.message : session.pr_creation_error,
  );
  const ghBlocked = ghStatus?.pr_authorship_mode === "user_required" && !ghStatus?.connected;
  const succeededButNoPR = prState === "succeeded" && !hasPR;
  const prActionError = hasPR
    ? null
    : (localPRActionError?.code && snapshotState ? snapshotMessage : localPRActionError?.message) ||
      (snapshotUnavailable ? snapshotMessage : null) ||
      (prState === "failed" ? session.pr_creation_error || PR_ERROR_TOAST_MESSAGE : null);
  const showPRAction =
    canCreatePR ||
    showExpiredPRAction ||
    queueingPR ||
    creatingPR ||
    finalizingPR ||
    prState === "failed" ||
    Boolean(prActionError);

  let prActionLabel = "Create PR";
  let prActionSpinning = false;
  let prActionDisabled = false;
  let prActionTitle: string | undefined;

  if (queueingPR) {
    prActionLabel = "Queueing PR…";
    prActionSpinning = true;
    prActionDisabled = true;
    prActionTitle = "Sending the PR request to the queue";
  } else if (creatingPR) {
    prActionLabel = "Creating PR…";
    prActionSpinning = true;
    prActionDisabled = true;
    prActionTitle = "Pushing changes and opening the pull request";
  } else if (snapshotUnavailable) {
    prActionDisabled = true;
    prActionTitle = snapshotMessage;
  } else if (localPRActionError) {
    prActionLabel = "Retry";
    prActionTitle = localPRActionError.message;
  } else if (succeededButNoPR) {
    prActionLabel = "Finalizing PR…";
    prActionSpinning = true;
    prActionDisabled = true;
  } else if (prState === "failed") {
    prActionLabel = "Retry";
    prActionTitle = session.pr_creation_error || "PR creation failed";
  } else if (ghBlocked) {
    prActionDisabled = true;
    prActionTitle = "Connect your GitHub account to create PRs";
  }

  // Push-changes button derived state. Mirrors the PR creation block above
  // but operates on session.pr_push_state and the backend-derived
  // has_unpushed_changes signal. Rendered inside the PR health banner
  // alongside Resolve conflicts / Fix tests / Merge so all PR-level
  // actions live in one place, while still hiding Push changes when the
  // latest session head already matches the remote PR branch.
  const pushAvailable = hasPR && prStatus === "open" && !!session.has_unpushed_changes;
  const pushState = session.pr_push_state;
  const queueingPush = localPushState === "submitting";
  const pushingChanges =
    (localPushState === "queued" && pushState !== "failed" && pushState !== "succeeded") ||
    pushState === "queued" ||
    pushState === "pushing";
  const canPushChanges = pushAvailable && hasSnapshot && !isRunning;
  const showPushAction = pushAvailable && (canPushChanges || queueingPush || pushingChanges || pushState === "failed" || localPushActionError);
  let pushActionLabel = "Push changes";
  let pushActionSpinning = false;
  let pushActionDisabled = false;
  let pushActionTitle: string | undefined;
  if (queueingPush) {
    pushActionLabel = "Queueing…";
    pushActionSpinning = true;
    pushActionDisabled = true;
    pushActionTitle = "Sending the push request to the queue";
  } else if (pushingChanges) {
    pushActionLabel = "Pushing…";
    pushActionSpinning = true;
    pushActionDisabled = true;
    pushActionTitle = "Pushing changes to the PR branch";
  } else if (snapshotUnavailable) {
    pushActionDisabled = true;
    pushActionTitle = snapshotMessage;
  } else if (localPushActionError) {
    pushActionLabel = "Retry";
    pushActionTitle = localPushActionError.message;
  } else if (pushState === "failed") {
    pushActionLabel = "Retry";
    pushActionTitle = session.pr_push_error || "Push to PR failed";
  } else if (ghBlocked) {
    pushActionDisabled = true;
    pushActionTitle = "Connect your GitHub account to push changes";
  } else if (isRunning) {
    pushActionDisabled = true;
    pushActionTitle = "Wait for the session to finish before pushing";
  }

  function handleMergeAction() {
    if (ghBlocked) {
      setPRAuthPrompt({ purpose: "merge_pr" });
      return;
    }
    mergeMutation.mutate();
  }

  const prErrorNotice = prActionError ? {
    title: prErrorTitle(snapshotState, localPRActionError?.code),
    description: prActionError,
    action: prActionDisabled ? undefined : {
      label: prActionLabel,
      onClick: () => createPRMutation.mutate(undefined),
    },
  } : null;
  const trimmedDraftTitle = draftTitle.trim();
  const canSaveTitle = trimmedDraftTitle.length > 0 && trimmedDraftTitle !== currentTitle && !updateSessionMutation.isPending;
  const saveTitleDisabledReason = updateSessionMutation.isPending
    ? "Saving title..."
    : trimmedDraftTitle.length === 0
      ? "Enter a title before saving."
      : trimmedDraftTitle === currentTitle
        ? "Enter a different title to save your changes."
        : undefined;
  const titleEditPendingReason = updateSessionMutation.isPending ? "Saving title..." : undefined;
  const detailToggleTitle = centerMode === "review" && showDetailPanel
    ? "File tree required during review"
    : showDetailPanel
      ? "Hide details"
      : "Show details";
  // Right-panel content. Rendered inline on desktop and inside a bottom sheet
  // on mobile — the same JSX in both places so tab state stays consistent.
  const panelTabsEl = (
    <Tabs
      value={detailTab}
      onValueChange={(v) => handleDetailTabClick(v as DetailTab)}
      className="flex flex-col flex-1 min-h-0 gap-0"
    >
      <div className="border-b border-border px-2 py-2 shrink-0">
        <div className="flex items-center gap-2 min-w-0">
          <div
            ref={detailTabsRef}
            aria-label="Session detail tabs"
            className={cn(
              "min-w-0 flex-1 overflow-x-auto overflow-y-hidden pb-1",
              detailTabsOverflow ? "mask-fade-r" : "scrollbar-hide",
            )}
          >
            <TabsList variant="line" size="sm" className="border-b-0 min-w-max">
              <TabsTrigger value="overview">Overview</TabsTrigger>
              <TabsTrigger value="changes">
                Changes
                {changesCount != null && changesCount > 0 && (
                  <Badge variant="secondary" className="ml-1 min-w-[18px] h-[18px] rounded-full px-1 text-xs font-semibold leading-none">
                    {changesCount}
                  </Badge>
                )}
              </TabsTrigger>
              {showValidationTab && (
                <TabsTrigger value="validation">Validation</TabsTrigger>
              )}
              <TabsTrigger value="preview">Preview</TabsTrigger>
            </TabsList>
          </div>
          <div aria-label="Session detail actions" className="flex items-center justify-end gap-2 shrink-0 pl-2">
            {hasPR && prData?.data?.github_pr_url ? (
              <>
                {prStatus === "closed" && (
                  <Badge variant="secondary" className="h-7 px-2 text-xs">
                    PR closed
                  </Badge>
                )}
                <Button asChild variant="outline" size="sm" className="h-7 text-xs gap-1.5">
                  <a href={prData.data.github_pr_url} target="_blank" rel="noopener noreferrer">
                    <ExternalLink className="h-3 w-3" />
                    View PR
                  </a>
                </Button>
              </>
            ) : showPRAction && !prErrorNotice ? (
              <DisabledTooltip disabled={prActionDisabled} content={prActionTitle}>
                <Button
                  variant="outline"
                  size="sm"
                  className="h-7 text-xs gap-1.5"
                  disabled={prActionDisabled}
                  title={prActionTitle}
                  onClick={() => createPRMutation.mutate(undefined)}
                >
                  {prActionSpinning ? (
                    <Loader2 className="h-3 w-3 animate-spin" />
                  ) : prState === "failed" || localPRActionError ? (
                    <AlertTriangle className="h-3 w-3" />
                  ) : (
                    <GitPullRequest className="h-3 w-3" />
                  )}
                  {prActionLabel}
                </Button>
              </DisabledTooltip>
            ) : null}
            <Button
              variant="ghost"
              size="icon"
              className="h-8 w-8 md:hidden"
              aria-label="Close details"
              onClick={() => setMobileDetailOpen(false)}
            >
              <X className="h-4 w-4" />
            </Button>
          </div>
        </div>
        {prErrorNotice && (
          <ErrorNotice
            className="mx-2 mt-2"
            title={prErrorNotice.title}
            description={prErrorNotice.description}
            action={prErrorNotice.action}
          />
        )}
      </div>

      <TabsContent value="changes" className="flex-1 min-h-0">
        <ChangesTab
          filteredFiles={visibleFilteredFiles}
          activeFileIndex={activeFileIndex}
          onFileSelect={setActiveFileIndex}
          onOpenReview={openReview}
          comments={comments}
          onCommentClick={handleCommentClick}
          passes={passes}
          passRange={passRange}
          onPassRangeChange={setPassRange}
          emptyStatusText={
            session.status === "running" || session.status === "pending"
              ? "Changes will appear here as the agent modifies files."
              : "This session did not produce any file changes."
          }
          isMobile={isMobileReviewViewport}
          threads={threads}
          attributionFilter={attributionFilter}
          onAttributionFilterChange={setAttributionFilter}
        />
      </TabsContent>
      <TabsContent value="overview" className="flex-1 overflow-y-auto scrollbar-hide p-4">
        <div className="space-y-4">
          {pullRequestId && prStatus === "open" && (
            prHealth ? (
              <PRHealthBanner
                health={prHealth}
                pendingAction={pendingPRAction}
                repairError={repairActionError}
                mergeAuthRequired={ghBlocked}
                onFixTests={() => startRepairMutation.mutate("fix_tests")}
                onResolveConflicts={() => startRepairMutation.mutate("resolve_conflicts")}
                onMerge={handleMergeAction}
                pushChanges={showPushAction ? {
                  label: pushActionLabel,
                  disabled: pushActionDisabled,
                  spinning: pushActionSpinning,
                  showError: pushState === "failed" || !!localPushActionError,
                  title: pushActionTitle,
                  onClick: () => pushChangesMutation.mutate(undefined),
                } : undefined}
              />
            ) : isPRHealthLoading ? (
              <Card className="border-border/60">
                <CardContent className="flex items-center gap-2 p-4 text-sm text-muted-foreground">
                  <Loader2 className="h-4 w-4 animate-spin" />
                  <span>Loading PR health…</span>
                </CardContent>
              </Card>
            ) : null
          )}
          {pullRequestId && prStatus === "closed" && (
            <Card className="border-border/60">
              <CardContent className="p-4">
                <div className="flex items-start gap-3">
                  <div className="flex h-8 w-8 items-center justify-center rounded-full bg-muted text-muted-foreground">
                    <XCircle className="h-4 w-4" />
                  </div>
                  <div className="min-w-0 space-y-1">
                    <div className="text-sm font-medium text-foreground">PR closed</div>
                    <p className="text-xs text-foreground">{closedPRSummary}</p>
                    <p className="text-xs text-muted-foreground">
                      This pull request is no longer active. Create a follow-up revision if you want to ship a new attempt.
                    </p>
                  </div>
                </div>
              </CardContent>
            </Card>
          )}
          {pullRequestId && prStatus === "merged" && (
            <Card className="border-border/60">
              <CardContent className="p-4">
                <div className="flex items-start gap-3">
                  <div aria-label="Merged PR status" className={cn("flex h-8 w-8 items-center justify-center rounded-full", prMergedAccent.bg, prMergedAccent.text)}>
                    <CheckCircle2 className="h-4 w-4" />
                  </div>
                  <div className="min-w-0 space-y-1">
                    <div className="text-sm font-medium text-foreground">PR merged</div>
                    <p className="text-xs text-foreground">{mergedPRSummary}</p>
                    <p className="text-xs text-muted-foreground">
                      This change has landed. Open a follow-up session if you need to make another revision.
                    </p>
                  </div>
                </div>
              </CardContent>
            </Card>
          )}
          <OverviewTab session={session} members={members} />
        </div>
      </TabsContent>
      {showValidationTab && (
        <TabsContent value="validation" className="flex-1 overflow-y-auto scrollbar-hide p-4">
          <ValidationTab sessionId={id} />
        </TabsContent>
      )}
      <TabsContent value="preview" className="flex-1 overflow-y-auto scrollbar-hide p-4">
        <PreviewPanel
          sessionId={id}
          previewOriginTemplate={PREVIEW_ORIGIN_TEMPLATE}
        />
      </TabsContent>
    </Tabs>
  );

  return (
    <div className="flex h-full">
      {/* Center area: chat or review diff view */}
      <div className="flex-1 min-w-0 flex flex-col">
        {!isDedicatedMobileReview ? (
        <div className="border-b border-border px-4 py-3 bg-background flex items-center justify-between shrink-0">
          <div className="min-w-0 flex-1 flex items-center gap-2">
            <MobileBackButton to="/sessions" label="Back to sessions" />
            {isEditingTitle ? (
              <div className="min-w-0 flex-1 flex items-center gap-2">
                <Input
                  aria-label="Session title"
                  value={draftTitle}
                  onChange={(e) => setDraftTitle(e.target.value)}
                  className="h-8 max-w-xl"
                  disabled={updateSessionMutation.isPending}
                />
                <DisabledTooltip disabled={!canSaveTitle} content={saveTitleDisabledReason}>
                  <Button
                    size="icon"
                    variant="ghost"
                    aria-label="Save title"
                    disabled={!canSaveTitle}
                    onClick={() => updateSessionMutation.mutate(trimmedDraftTitle)}
                  >
                    <Check className="h-4 w-4" />
                  </Button>
                </DisabledTooltip>
                <DisabledTooltip disabled={updateSessionMutation.isPending} content={titleEditPendingReason}>
                  <Button
                    size="icon"
                    variant="ghost"
                    aria-label="Cancel title"
                    disabled={updateSessionMutation.isPending}
                    onClick={() => {
                      setDraftTitle(currentTitle);
                      setIsEditingTitle(false);
                    }}
                  >
                    <X className="h-4 w-4" />
                  </Button>
                </DisabledTooltip>
              </div>
            ) : (
              <>
                <h1 className="text-sm font-medium text-foreground truncate">
                  {sessionTitle(session)}
                </h1>
                <Button
                  variant="ghost"
                  size="icon"
                  className="h-8 w-8 shrink-0"
                  aria-label="Edit session title"
                  onClick={() => {
                    setDraftTitle(currentTitle);
                    setIsEditingTitle(true);
                  }}
                >
                  <Pencil className="h-4 w-4" />
                </Button>
              </>
            )}
            <span className={`inline-flex items-center rounded-full px-2 py-0.5 text-xs font-medium shrink-0 ${status.color}`}>
              {status.label}
            </span>
            {diffStats && (
              <DiffStatsBadge
                added={diffStats.added}
                removed={diffStats.removed}
                className="shrink-0"
                onClick={() => openReview()}
              />
            )}
            <LinkedIssueChips session={session} />
          </div>
          {/* Desktop toggle: hides/shows the inline right panel. */}
          <DisabledTooltip disabled={centerMode === "review" && showDetailPanel} content={detailToggleTitle}>
            <Button
              variant="ghost"
              size="icon"
              className={cn("hidden md:inline-flex h-8 w-8 shrink-0", centerMode === "review" && showDetailPanel && "opacity-30 cursor-not-allowed")}
              disabled={centerMode === "review" && showDetailPanel}
              onClick={() => setShowDetailPanel(!showDetailPanel)}
              title={detailToggleTitle}
            >
              {showDetailPanel ? <PanelRightClose className="h-4 w-4" /> : <PanelRightOpen className="h-4 w-4" />}
            </Button>
          </DisabledTooltip>
          {/* Mobile toggle: opens the bottom sheet. */}
          <Button
            variant="ghost"
            size="icon"
            className="md:hidden h-9 w-9 shrink-0"
            onClick={() => setMobileDetailOpen(true)}
            aria-label="Open details"
            aria-controls="session-detail-sheet"
            aria-expanded={mobileDetailOpen}
          >
            <PanelBottomOpen className="h-5 w-5" />
          </Button>
        </div>
        ) : null}

        {!isDedicatedMobileReview ? (
          <AgentTabStrip
            threads={threads}
            activeThreadId={activeThread?.id ?? null}
            overlapsByThreadId={overlapsByThreadId}
            statusConfig={statusConfig}
            onActiveThreadChange={setActiveThreadId}
            onAddTab={() => createThreadMutation.mutate(buildDefaultThreadRequest())}
            addTabPending={createThreadMutation.isPending}
            onCancelThread={(tid) => cancelThreadMutation.mutate(tid)}
            onForkThread={(tid) => forkThreadMutation.mutate(tid)}
            onRevertThread={(tid) => revertThreadMutation.mutate(tid)}
            cancelPendingThreadId={cancelThreadMutation.isPending ? cancelThreadMutation.variables ?? null : null}
          />
        ) : null}
        {/* Center content — either chat or diff review */}
        <div className="flex-1 min-h-0 relative">
          {/* Chat panel — always mounted to preserve scroll, SSE connections, etc. */}
          <div className={cn("h-full", centerMode !== "chat" && "hidden")}>
            <MemoizedChatPanel
              key={activeThread ? `${id}:${activeThread.id}` : id}
              session={session}
              sessionId={id}
              activeThread={activeThread}
              isActive={isActive}
              optimisticMessages={optimisticMessages}
              onDiffClick={handleChatDiffClick}
              onApprovePlan={handleApprovePlan}
              onAdjustPlan={handleAdjustPlan}
              onRegisterScrollToLiveEdge={registerChatPanelScrollToLiveEdge}
            />
          </div>
          {/* Review diff view — mounted only when active */}
          {centerMode === "review" && (
            <div className="h-full animate-in fade-in duration-150 flex flex-col">
              <div className="flex-1 min-h-0">
                <ReviewDiffView
                  sessionId={id}
                  files={visibleFilteredFiles}
                  allFiles={visibleDiffFiles}
                  activeFileIndex={activeFileIndex}
                  onFileChange={setActiveFileIndex}
                  onBack={exitReview}
                  isMobile={isMobileReviewViewport}
                  onOpenFileList={openMobileFilesList}
                  onOpenComposer={session.agent_type !== "pm_agent" ? () => setMobileReviewComposerOpen(true) : undefined}
                  commentsByLine={commentsByLine}
                  activeCommentLine={activeCommentLine}
                  onAddComment={handleAddComment}
                  onSubmitComment={handleSubmitComment}
                  onCancelComment={handleCancelComment}
                  onUpdateComment={updateComment}
                  onDeleteComment={deleteComment}
                  diffSearchQuery={diffSearchQuery}
                  onDiffSearchChange={setDiffSearchQuery}
                />
              </div>
            </div>
          )}
        </div>

        {session.agent_type !== "pm_agent" && !isDedicatedMobileReview && (
          <>
            {composerIsSnapshotExpired && (
              <div className="flex items-center gap-2 px-4 py-2.5 text-xs border-t bg-amber-50 dark:bg-amber-950/20 border-amber-200 dark:border-amber-800/40 text-amber-800 dark:text-amber-300">
                <Clock className="h-3.5 w-3.5 shrink-0" />
                <span>
                  This session&apos;s environment has expired. Sessions can be continued for up to 30 days after their last activity. To make further changes, please start a new session.
                </span>
              </div>
            )}
            {composerLacksHeadlessResume && composerCanSendMessage && !composerIsSnapshotExpired && (
              <div className="flex items-center gap-2 px-4 py-2.5 text-xs border-t bg-sky-50 dark:bg-sky-950/20 border-sky-200 dark:border-sky-800/40 text-sky-800 dark:text-sky-300">
                <AlertTriangle className="h-3.5 w-3.5 shrink-0" />
                <span>
                  {AGENTS_BY_KEY[session.agent_type]?.label ?? session.agent_type} doesn&apos;t support headless conversation resume. Follow-up messages run against the restored filesystem, but earlier chat context is not replayed — include anything you need the agent to remember.
                </span>
              </div>
            )}
            <SessionComposer
              message={composerMessage}
              onMessageChange={setComposerMessage}
              planMode={composerPlanMode}
              onPlanModeChange={setComposerPlanMode}
              selectedModel={composerSelectedModel}
              onSelectedModelChange={setComposerSelectedModel}
              attachments={composerAttachments}
              isUploading={composerIsUploading}
              onUpload={handleComposerUpload}
              onPasteFiles={uploadComposerFiles}
              onAddAttachment={handleAddComposerAttachment}
              onRemoveAttachment={handleRemoveComposerAttachment}
              openComments={attachedReviewComments}
              availableModels={composerAvailableModels}
              canSendMessage={composerCanSendMessage}
              isRunning={composerIsRunning}
              isSnapshotExpired={composerIsSnapshotExpired}
              isClaudeCode={composerIsClaudeCode}
              sendPending={sendMutation.isPending}
              sendError={sendMutation.error}
              cancelPending={cancelMutation.isPending}
              uploadError={composerUploadError}
              onCancelSession={() => cancelMutation.mutate()}
              onSend={() => queueSend()}
              textareaRef={composerTextareaRef}
              uploadInputRef={composerUploadInputRef}
              references={composerReferences}
              onReferencesChange={setComposerReferences}
              commands={composerCommands}
              onCommandsChange={setComposerCommands}
              repositoryId={session.repository_id}
              branch={session.target_branch}
              agentType={composerAgentType}
              editableAgentType={activeThreadIsEditable ? composerAgentType : undefined}
              editableAgents={activeThreadIsEditable ? EDITABLE_THREAD_AGENTS : undefined}
              onEditableAgentTypeChange={activeThreadIsEditable ? handleEditableAgentTypeChange : undefined}
              agentUpdatePending={updateThreadMutation.isPending}
              targetLabel={activeThread ? activeThreadLabel : undefined}
            />
          </>
        )}
      </div>

      {/* Detail panel — inline on desktop, hidden on mobile (rendered as a
          bottom sheet below). */}
      {showDetailPanel && (
        <div className="hidden md:flex">
          <ResizeHandle onResize={handleDetailResize} />
          <div
            style={{ width: detailWidth }}
            className="border-l border-border bg-muted/20 flex flex-col shrink-0 overflow-hidden"
          >
            {panelTabsEl}
          </div>
        </div>
      )}

      {/* Detail panel — bottom sheet on mobile. */}
      <Sheet open={mobileDetailOpen} onOpenChange={setMobileDetailOpen}>
        <SheetContent
          side="bottom"
          id="session-detail-sheet"
          hideCloseButton
          className="md:hidden h-[85vh] max-h-[85vh] min-h-[60vh] p-0 flex flex-col gap-0 bg-background"
        >
          <SheetTitle className="sr-only">Session details</SheetTitle>
          <SheetDescription className="sr-only">
            Browse session details, changed files, validation, and preview on mobile.
          </SheetDescription>
          {panelTabsEl}
        </SheetContent>
      </Sheet>
      {session.agent_type !== "pm_agent" ? (
        <Sheet open={mobileReviewComposerOpen} onOpenChange={setMobileReviewComposerOpen}>
          <SheetContent
            side="bottom"
            className="max-h-[85vh] overflow-y-auto rounded-t-2xl p-0"
          >
            <SheetHeader className="border-b border-border px-4 py-4 text-left">
              <SheetTitle>Message agent</SheetTitle>
              <SheetDescription>
                Send follow-up guidance without leaving the mobile diff reader.
              </SheetDescription>
            </SheetHeader>
            {composerIsSnapshotExpired ? (
              <div className="flex items-center gap-2 px-4 py-3 text-xs border-b bg-amber-50 dark:bg-amber-950/20 border-amber-200 dark:border-amber-800/40 text-amber-800 dark:text-amber-300">
                <Clock className="h-3.5 w-3.5 shrink-0" />
                <span>
                  This session&apos;s environment has expired. Sessions can be continued for up to 30 days after their last activity. To make further changes, please start a new session.
                </span>
              </div>
            ) : null}
            {composerLacksHeadlessResume && composerCanSendMessage && !composerIsSnapshotExpired ? (
              <div className="flex items-center gap-2 px-4 py-3 text-xs border-b bg-sky-50 dark:bg-sky-950/20 border-sky-200 dark:border-sky-800/40 text-sky-800 dark:text-sky-300">
                <AlertTriangle className="h-3.5 w-3.5 shrink-0" />
                <span>
                  {AGENTS_BY_KEY[session.agent_type]?.label ?? session.agent_type} doesn&apos;t support headless conversation resume. Follow-up messages run against the restored filesystem, but earlier chat context is not replayed — include anything you need the agent to remember.
                </span>
              </div>
            ) : null}
            <SessionComposer
              message={composerMessage}
              onMessageChange={setComposerMessage}
              planMode={composerPlanMode}
              onPlanModeChange={setComposerPlanMode}
              selectedModel={composerSelectedModel}
              onSelectedModelChange={setComposerSelectedModel}
              attachments={composerAttachments}
              isUploading={composerIsUploading}
              onUpload={handleComposerUpload}
              onPasteFiles={uploadComposerFiles}
              onAddAttachment={handleAddComposerAttachment}
              onRemoveAttachment={handleRemoveComposerAttachment}
              openComments={attachedReviewComments}
              availableModels={composerAvailableModels}
              canSendMessage={composerCanSendMessage}
              isRunning={composerIsRunning}
              isSnapshotExpired={composerIsSnapshotExpired}
              isClaudeCode={composerIsClaudeCode}
              sendPending={sendMutation.isPending}
              sendError={sendMutation.error}
              cancelPending={cancelMutation.isPending}
              uploadError={composerUploadError}
              onCancelSession={() => cancelMutation.mutate()}
              onSend={() => queueSend()}
              textareaRef={composerTextareaRef}
              uploadInputRef={composerUploadInputRef}
              references={composerReferences}
              onReferencesChange={setComposerReferences}
              commands={composerCommands}
              onCommandsChange={setComposerCommands}
              repositoryId={session.repository_id}
              branch={session.target_branch}
              agentType={composerAgentType}
              editableAgentType={activeThreadIsEditable ? composerAgentType : undefined}
              editableAgents={activeThreadIsEditable ? EDITABLE_THREAD_AGENTS : undefined}
              onEditableAgentTypeChange={activeThreadIsEditable ? handleEditableAgentTypeChange : undefined}
              agentUpdatePending={updateThreadMutation.isPending}
              targetLabel={activeThread ? activeThreadLabel : undefined}
            />
          </SheetContent>
        </Sheet>
      ) : null}
      <AlertDialog
        open={!!prAuthPrompt}
        onOpenChange={(open) => {
          if (!open) setPRAuthPrompt(null);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {prAuthPrompt?.purpose === "merge_pr"
                ? "Merge this pull request as yourself?"
                : prAuthPrompt?.purpose === "push_changes"
                  ? "Push these changes as yourself?"
                  : "Open this pull request as yourself?"}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {prAuthPrompt?.purpose === "merge_pr"
                ? "Authorize GitHub once to merge pull requests as you."
                : prAuthPrompt?.purpose === "push_changes"
                  ? "Authorize GitHub once to push as you. If you skip this, 143 can still push the commits as the app."
                  : "Authorize GitHub once to open PRs as you. If you skip this, 143 can still open the PR as the app."}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            {prAuthPrompt?.purpose === "create_pr" && prAuthPrompt.can_fallback_to_app ? (
              <AlertDialogCancel
                onClick={(event) => {
                  event.preventDefault();
                  setPRAuthPrompt(null);
                  createPRMutation.mutate({ authorMode: "app" });
                }}
              >
                Create as 143
              </AlertDialogCancel>
            ) : null}
            {prAuthPrompt?.purpose === "push_changes" && prAuthPrompt.can_fallback_to_app ? (
              <AlertDialogCancel
                onClick={(event) => {
                  event.preventDefault();
                  setPRAuthPrompt(null);
                  pushChangesMutation.mutate({ authorMode: "app" });
                }}
              >
                Push as 143
              </AlertDialogCancel>
            ) : null}
            <AlertDialogAction
              onClick={(event) => {
                event.preventDefault();
                if (!prAuthPrompt) return;
                const resumeToken =
                  prAuthPrompt.purpose === "create_pr" || prAuthPrompt.purpose === "push_changes"
                    ? prAuthPrompt.resume_token
                    : undefined;
                api.githubStatus.connect(resumeToken || undefined);
              }}
            >
              Continue with GitHub
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
