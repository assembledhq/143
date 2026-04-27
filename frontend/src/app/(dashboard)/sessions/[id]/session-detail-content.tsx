"use client";

import { useCallback, useDeferredValue, useEffect, useMemo, useRef, useState } from "react";
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
  Slash,
  Square,
  PanelRightOpen,
  PanelRightClose,
  PanelBottomOpen,
  Clock,
  MessageSquare,
  Paperclip,
  Pencil,
} from "lucide-react";
import { toast } from "sonner";
import { Badge } from "@/components/ui/badge";
import { MarkdownContent } from "@/components/markdown";
import { Button } from "@/components/ui/button";
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
  SheetTitle,
} from "@/components/ui/sheet";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { ChatTimeline } from "@/components/chat-timeline";
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
import { SSE_EVENT, addSSEListener } from "@/lib/sse";
import { buildTimeline, buildTimelineFromResponse } from "@/lib/timeline";
import { parseDiffStats, type DiffFile } from "@/lib/diff-parser";
import { formatReviewMessage } from "@/lib/format-review-message";
import {
  readStoredSessionScrollPosition,
  resolveInitialSessionAnchor,
  writeStoredSessionScrollPosition,
} from "@/lib/session-open-position";
import type { ListResponse, Session, SessionDetail, SessionInputCommand, SessionInputReference, SessionLog, SessionMessage, SessionReviewComment, User, Validation, CodexAuthStatus, PullRequestHealthResponse, SingleResponse } from "@/lib/types";
import { AuditLogTrigger } from "@/components/audit/audit-log-trigger";
import { ResizeHandle } from "@/components/resize-handle";
import { DiffStatsBadge, FileTree, SessionFooter, CommentsSummary, ReviewDiffView, PassSelector, type DiffPassEntry, type PassRange } from "@/components/code-review";
import { useReviewComments } from "@/hooks/use-review-comments";
import { useDiffViewState } from "@/hooks/use-diff-view-state";
import { CodexDeviceCodeModal } from "@/components/codex-device-code-modal";
import { AgentBadge } from "@/components/agent-badge";
import { PreviewPanel } from "@/components/preview/preview-panel";
import { PendingAttachmentStrip } from "@/components/pending-attachment-strip";
import { PRHealthBanner } from "@/components/pr-health-banner";
import { ReviewButton } from "@/components/review-button";
import { MobileBackButton } from "@/components/mobile-back-button";
import { useAuth } from "@/hooks/use-auth";
import { cn, sessionTitle, formatTimeAgo } from "@/lib/utils";
import { activeSet } from "@/lib/session-status-groups";

const PREVIEW_ORIGIN_TEMPLATE =
  process.env.NEXT_PUBLIC_PREVIEW_ORIGIN_TEMPLATE ||
  "http://{id}.preview.localhost:9090";

const FAILURE_CATEGORY_CODEX_AUTH = "codex_auth_expired";
const PR_ERROR_TOAST_DURATION_MS = 10_000;
const PR_ERROR_TOAST_MESSAGE = "PR creation failed";

const statusConfig: Record<string, { color: string; label: string }> = {
  pending: { color: "bg-muted text-muted-foreground", label: "Pending" },
  running: { color: "bg-primary/10 text-primary", label: "Running" },
  idle: { color: "bg-sky-50 text-sky-700 dark:bg-sky-950/30 dark:text-sky-400", label: "Idle" },
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

const triggerPickerIconClassName = "h-4 w-4 shrink-0";
const directoryTriggerIcon = <FolderTree className={triggerPickerIconClassName} />;
const fileTriggerIcon = <FileCode2 className={triggerPickerIconClassName} />;
const slashTriggerIcon = <Slash className={triggerPickerIconClassName} />;

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

type PRActionErrorState = {
  code?: string;
  message: string;
};

const terminalSessionStatuses = new Set(["completed", "pr_created", "failed", "cancelled", "skipped"]);
const SNAPSHOT_UNAVAILABLE_PR_MESSAGE =
  "This session snapshot is unavailable. Send a new message to rebuild the sandbox, then create the PR again.";

function isPRAuthInterceptDetails(value: unknown): value is PRAuthInterceptDetails {
  if (!value || typeof value !== "object") return false;
  const details = value as Partial<PRAuthInterceptDetails>;
  return typeof details.connect_url === "string" &&
    typeof details.resume_token === "string" &&
    typeof details.can_fallback_to_app === "boolean";
}

function normalizeSnapshotPRMessage(message?: string | null): string {
  if (!message || /^session state expired\b/i.test(message)) {
    return SNAPSHOT_UNAVAILABLE_PR_MESSAGE;
  }
  return message;
}

function prErrorTitle(snapshotUnavailable: boolean, errorCode?: string): string {
  if (snapshotUnavailable || errorCode === "SNAPSHOT_EXPIRED") {
    return "PR snapshot unavailable";
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
    queryKey: ["codex-auth-status"],
    queryFn: () => api.codexAuth.status(),
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

  const triggeredByLabel = session.pm_plan_id && !session.triggered_by_user_id
    ? "PM Agent"
    : session.triggered_by_user_id
      ? members.find((m) => m.id === session.triggered_by_user_id)?.name || "Unknown user"
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
                <Button
                  size="xs"
                  variant="outline"
                  onClick={() => retryMutation.mutate()}
                  disabled={retryMutation.isPending}
                >
                  <RefreshCw className={`mr-1.5 h-3 w-3 ${retryMutation.isPending ? "animate-spin" : ""}`} />
                  {retryMutation.isPending ? "Retrying..." : "Retry"}
                </Button>
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
          {/* Review all button */}
          <div className="px-4 py-3">
            <button
              onClick={() => onOpenReview()}
              className="w-full flex items-center justify-center gap-2 px-3 py-2 rounded-md border border-border bg-background text-xs font-medium text-foreground hover:bg-muted/50 transition-colors"
            >
              <FileCode2 className="h-3.5 w-3.5" />
              Review {filteredFiles.length} {filteredFiles.length === 1 ? "file" : "files"}
            </button>
          </div>

          {/* File tree — always visible, it's the sidebar's purpose */}
          <div className="flex-1 overflow-hidden">
            <FileTree
              files={filteredFiles}
              activeFileIndex={activeFileIndex}
              onFileSelect={handleFileClick}
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
}) {
  useEffect(() => {
    const el = textareaRef.current;
    if (!el) return;
    el.style.height = "auto";
    el.style.height = `${Math.min(el.scrollHeight, 200)}px`;
  }, [message, textareaRef]);

  const composerCardRef = useRef<HTMLDivElement>(null);
  const [caretPosition, setCaretPosition] = useState(message.length);
  const [selectedTriggerIndex, setSelectedTriggerIndex] = useState(0);
  const [triggerDismissed, setTriggerDismissed] = useState(false);
  const [pickerPosition, setPickerPosition] = useState<TriggerPickerPosition | null>(null);

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
          icon: slashTriggerIcon,
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
      const card = composerCardRef.current;
      if (!card) return;
      const rect = card.getBoundingClientRect();
      const spacing = 8;
      const viewportHeight = window.innerHeight;
      const spaceAbove = rect.top - spacing;
      const spaceBelow = viewportHeight - rect.bottom - spacing;
      const side: "top" | "bottom" = spaceAbove >= 160 || spaceAbove >= spaceBelow ? "top" : "bottom";
      const availableHeight = Math.max(side === "top" ? spaceAbove : spaceBelow, 120);
      const top = side === "top"
        ? Math.max(spacing, rect.top - Math.min(280, availableHeight) - spacing)
        : Math.min(viewportHeight - spacing - Math.min(280, availableHeight), rect.bottom + spacing);
      setPickerPosition({
        left: rect.left,
        top,
        width: rect.width,
        maxHeight: Math.min(280, availableHeight),
        side,
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
  const sendDisabled = hasInvalidCommands || !hasContent || !canSendMessage || sendPending || isRunning;

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

  const firstError = uploadError || sendError;
  const errorMessage = typeof firstError === "string"
    ? firstError
    : firstError instanceof Error
      ? firstError.message
      : firstError
        ? "An error occurred"
        : null;

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

      <div className="border-t border-border p-3 bg-background shrink-0" ref={composerCardRef}>
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

        <div className={cn("rounded-xl border bg-muted/30 focus-within:border-ring focus-within:ring-1 focus-within:ring-ring", planMode ? "border-amber-200 dark:border-amber-800/50" : "border-border")}>
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
            onKeyDown={handleKeyDown}
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
                    : isRunning
                      ? "Agent is responding..."
                      : "Send a follow-up message..."
            }
            disabled={!canSendMessage || sendPending || isRunning}
            className="min-h-[44px] max-h-[200px] resize-none border-none bg-transparent shadow-none focus-visible:ring-0"
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
                    <Slash className="h-3 w-3" />
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

          <div className="flex items-center gap-1 px-2 pb-2">
            <Button
              type="button"
              size="icon"
              variant="ghost"
              className="h-8 w-8 shrink-0 rounded-lg text-muted-foreground hover:text-foreground"
              title="Attach files or images"
              disabled={!canSendMessage || isUploading}
              onClick={() => uploadInputRef.current?.click()}
            >
              {isUploading ? <Loader2 className="h-4 w-4 animate-spin" /> : <Paperclip className="h-4 w-4" />}
            </Button>
            <input
              ref={uploadInputRef}
              type="file"
              accept="image/*,.pdf,.txt,.md,.json,.csv"
              multiple
              className="hidden"
              onChange={onUpload}
            />

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
              {isRunning ? (
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
              ) : (
                <Button
                  size="icon"
                  variant={planMode ? "outline" : "default"}
                  className={cn("h-8 w-8 shrink-0 rounded-lg", planMode && "border-amber-300 dark:border-amber-700 text-amber-700 dark:text-amber-400 hover:bg-amber-50 dark:hover:bg-amber-950/30")}
                  title={planMode ? "Send plan request" : "Send message"}
                  disabled={!hasContent || !canSendMessage || sendPending || isRunning}
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
              )}
            </div>
          </div>
        </div>
      </div>
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

function buildPullRequestStreamURL(apiBase: string, activeOrgId: string | null): string {
  const searchParams = new URLSearchParams();
  if (activeOrgId) {
    searchParams.set("org_id", activeOrgId);
  }
  const qs = searchParams.toString();
  return `${apiBase}/api/v1/pull-requests/stream${qs ? `?${qs}` : ""}`;
}

function isNearBottom(el: HTMLElement): boolean {
  return el.scrollHeight - el.scrollTop - el.clientHeight < SCROLL_NEAR_BOTTOM_THRESHOLD;
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

function ChatPanel({
  session,
  sessionId,
  isActive,
  onDiffClick,
  onApprovePlan,
  onAdjustPlan,
  onRegisterScrollToLiveEdge,
}: {
  session: Session;
  sessionId: string;
  isActive: boolean;
  onDiffClick?: () => void;
  onApprovePlan?: () => void;
  onAdjustPlan?: () => void;
  onRegisterScrollToLiveEdge?: (scrollToLiveEdge: (() => void) | null) => void;
}) {
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
  const viewerScope = useMemo(
    () => (user ? { userId: user.id, orgId: user.org_id } : null),
    [user],
  );

  const isRunning = session.status === "running";
  const isSnapshotExpired = session.sandbox_state === "destroyed";
  const canSendMessage = session.status !== "skipped" && session.status !== "pending" && !isSnapshotExpired;

  const timelineQuery = useQuery({
    queryKey: ["session", sessionId, "timeline"],
    queryFn: () => api.sessions.getTimeline(sessionId),
    refetchInterval: isActive ? 3000 : false,
  });

  // Fetch the linked primary issue to display its description as the initial prompt.
  const primaryIssueId = session.primary_issue_id ?? undefined;
  const hasIssue = !!primaryIssueId;
  const issueQuery = useQuery({
    queryKey: ["issue", primaryIssueId],
    queryFn: () => api.issues.get(primaryIssueId!),
    enabled: hasIssue,
  });

  const baseTimelineEntries = useMemo(() => {
    const entries = buildTimelineFromResponse(timelineQuery.data?.data || []);
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
  }, [timelineQuery.data?.data, issueQuery.data?.data?.description, sessionId, session.org_id, session.created_at]);

  const timelineEntries = useMemo(() => {
    const fetchedLogIds = new Set<number>();
    const assistantTranscriptByTurn = new Map<number, Set<string>>();
    const planModeSeedMessages: SessionMessage[] = [];
    const normalizeTranscriptContent = (content: string) =>
      content
        .replace(/\r\n/g, "\n")
        .split("\n")
        .map((line) => line.replace(/[ \t\r]+$/g, ""))
        .join("\n")
        .replace(/\n+$/g, "");

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

    const overlayLogs = streamedLogs.filter((log) => {
      if (fetchedLogIds.has(log.id)) return false;
      if (log.level !== "output") return true;
      if (log.metadata?.type === "tool_result") return true;
      if (log.metadata?.type === "assistant_final" && log.metadata?.duplicate_of_transcript === true) return false;
      return !assistantTranscriptByTurn.get(log.turn_number)?.has(normalizeTranscriptContent(log.message));
    });

    if (overlayLogs.length === 0) return baseTimelineEntries;
    const overlayEntries = buildTimeline(planModeSeedMessages, overlayLogs).filter((entry) => entry.kind !== "message");
    return [...baseTimelineEntries, ...overlayEntries];
  }, [baseTimelineEntries, streamedLogs]);
  const hasLoadedTimelineInputs = timelineQuery.isFetched && (!hasIssue || issueQuery.isFetched);
  // Skeleton only while we'd reasonably expect content: timeline still loading,
  // or session is active. Terminal sessions with empty timelines must not shimmer forever.
  const showLoadingSkeleton =
    timelineEntries.length === 0 &&
    session.status !== "pending" &&
    (!hasLoadedTimelineInputs || activeSet.has(session.status));

  const persistScrollPosition = useCallback((scrollTop: number) => {
    if (typeof window === "undefined" || !viewerScope) return;
    writeStoredSessionScrollPosition(window.localStorage, sessionId, viewerScope, scrollTop);
  }, [sessionId, viewerScope]);

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
      return [...prev, ...toAdd];
    });
  }, []);

  useEffect(() => {
    if (!isActive) return;

    let eventSource: EventSource | null = null;
    let cancelled = false;

    function connect() {
      if (cancelled) return;

      eventSource = new EventSource(
        `${apiBase}/api/v1/sessions/${sessionId}/logs/stream`,
        { withCredentials: true }
      );

      eventSource.onopen = () => {
        reconnectAttempts.current = 0;
      };

      addSSEListener(eventSource, SSE_EVENT.LOG, (log) => {
        mergeLogs([log]);
      });

      addSSEListener(eventSource, SSE_EVENT.STATUS, (updated) => {
        queryClient.setQueryData(["session", sessionId], { data: updated });
        // When the session transitions out of running (e.g. sandbox creation
        // failure reverts to idle), fetch the latest messages so any error
        // message posted by the backend is displayed immediately.
        if (updated.status !== "running") {
          queryClient.invalidateQueries({ queryKey: ["session", sessionId, "timeline"] });
        }
      });

      addSSEListener(eventSource, SSE_EVENT.DONE, (updated) => {
        queryClient.setQueryData(["session", sessionId], { data: updated });
        eventSource?.close();
        queryClient.invalidateQueries({ queryKey: ["session", sessionId, "timeline"] });
      });

      eventSource.onerror = () => {
        eventSource?.close();
        queryClient.invalidateQueries({ queryKey: ["session", sessionId, "timeline"] });

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
  }, [sessionId, apiBase, isActive, mergeLogs, queryClient]);

  // Track whether the user is scrolled near the bottom.
  const handleScroll = useCallback(() => {
    const el = scrollRef.current;
    if (!el) return;
    syncScrollState(el);
    schedulePersistScrollPosition(el.scrollTop);
  }, [schedulePersistScrollPosition, syncScrollState]);

  useEffect(() => {
    initialAnchorAppliedRef.current = false;
  }, [sessionId]);

  useEffect(() => {
    const currentScrollEl = scrollRef.current;
    return () => {
      if (saveScrollTimerRef.current) {
        clearTimeout(saveScrollTimerRef.current);
      }
      if (currentScrollEl) {
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
        : readStoredSessionScrollPosition(window.localStorage, sessionId, viewerScope);
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
  }, [hasLoadedTimelineInputs, isRunning, scrollToLiveEdgePosition, sessionId, syncScrollState, timelineEntries, viewerScope]);

  // Only auto-scroll to bottom when new entries arrive if the user is already near the bottom.
  useEffect(() => {
    if (scrollRef.current && isNearBottomRef.current) {
      scrollToLiveEdgePosition();
    }
  }, [scrollToLiveEdgePosition, timelineEntries.length]);

  return (
    <div className="relative flex flex-col h-full">
      {showJumpToLatest && (
        <div className="absolute bottom-24 right-4 z-20">
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
            getEntryContainerProps={(_, index) =>
              ({
                "data-session-entry-index": index,
              }) as React.HTMLAttributes<HTMLDivElement> & Record<`data-${string}`, string | number | undefined>
            }
          />
        )}
        {session.status === "pending" && (
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

// ---------------------------------------------------------------------------
// Main component
// ---------------------------------------------------------------------------

const MIN_DETAIL = 280;
const MAX_DETAIL = 600;
const DEFAULT_DETAIL = 384;

export function SessionDetailContent({ id }: { id: string }) {
  const router = useRouter();
  const terminalStatuses = new Set(["completed", "pr_created", "failed", "cancelled", "skipped"]);
  const [reviewParam, setReviewParam] = useQueryState("review");
  const [previewParam, setPreviewParam] = useQueryState("preview");
  const [resumePRParam, setResumePRParam] = useQueryState("resume_pr");
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
  const [detailWidth, setDetailWidth] = useState(DEFAULT_DETAIL);
  const [activeFileIndex, setActiveFileIndex] = useState(0);
  const [isEditingTitle, setIsEditingTitle] = useState(false);
  const [draftTitle, setDraftTitle] = useState("");

  const handleDetailResize = useCallback((delta: number) => {
    setDetailWidth((w) => Math.min(MAX_DETAIL, Math.max(MIN_DETAIL, w - delta)));
  }, []);

  // --- Enter review mode ---
  const openReview = useCallback((fileIndex?: number) => {
    if (fileIndex !== undefined) setActiveFileIndex(fileIndex);
    setReviewParam("active");
    setDetailTab("changes");
    setShowDetailPanel(true);
    // On mobile the right panel lives in a bottom sheet — auto-open it so the
    // file tree is reachable when entering review. Gate on viewport because
    // the Sheet's overlay isn't viewport-aware (md:hidden only hides
    // SheetContent, not SheetOverlay) — opening on desktop would dim the
    // screen behind a hidden sheet.
    if (typeof window !== "undefined" && window.matchMedia("(max-width: 767px)").matches) {
      setMobileDetailOpen(true);
    }
  }, [setReviewParam]);

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
  const [pendingRepairAction, setPendingRepairAction] = useState<"fix_tests" | "resolve_conflicts" | null>(null);
  const [repairActionError, setRepairActionError] = useState<string | null>(null);
  const [pendingReviewMode, setPendingReviewMode] = useState<import("@/lib/types").SessionReviewMode | null>(null);
  const [prAuthPrompt, setPRAuthPrompt] = useState<PRAuthInterceptDetails | null>(null);
  const resumeAttemptRef = useRef<string | null>(null);
  const apiBase = process.env.NEXT_PUBLIC_API_URL || "";

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

      // Poll while PR creation is in flight so the state machine advances
      // without waiting for the user to navigate. Keep polling during the
      // optimistic local phases too, since the best-effort queued write can
      // legitimately lag the 202 response.
      return serverInFlight || waitingForServer ? 2000 : false;
    },
  });

  const { data: membersData } = useQuery({
    queryKey: ["team", "members"],
    queryFn: () => api.team.listMembers(),
  });

  const session = data?.data;
  const members = membersData?.data ?? [];
  const isActive = session ? !terminalStatuses.has(session.status) : false;
  const isRunning = session?.status === "running";
  const currentTitle = session ? sessionTitle(session) : "";
  const { user } = useAuth();
  const canRequestReview = user?.role === "admin" || user?.role === "member";

  const queryClient = useQueryClient();

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

  // PR state for the detail-panel header button
  const { data: prData } = useQuery({
    queryKey: ["session", id, "pr"],
    queryFn: () => api.sessions.getPR(id),
    refetchInterval: (q) => {
      const serverState = data?.data?.pr_creation_state;
      const optimisticWaitingForPR =
        localPRState !== "idle" && serverState !== "failed" && serverState !== "succeeded";
      const waitingForPR =
        optimisticWaitingForPR ||
        serverState === "queued" ||
        serverState === "pushing" ||
        serverState === "succeeded";

      return waitingForPR && !q.state.data?.data ? 2000 : false;
    },
  });
  const pullRequestId = prData?.data?.id;
  const { data: prHealthData, isLoading: isPRHealthLoading } = useQuery({
    queryKey: ["pull-request", pullRequestId, "health"],
    queryFn: () => api.pullRequests.getHealth(pullRequestId!),
    enabled: !!pullRequestId && prData?.data?.status === "open",
  });
  const prHealth = prHealthData?.data;
  const prStatus = prData?.data?.status;
  const closedPRNumber = prData?.data?.github_pr_number;
  const closedPRSummary = closedPRNumber
    ? `PR #${closedPRNumber} was closed without merging.`
    : "This pull request was closed without merging.";

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
  // Session-native review affordance. Capabilities are server-driven so the
  // button only appears when the agent's adapter implements ReviewCapableAdapter
  // and the session is in a state that can run another turn (idle/paused with
  // a non-empty diff). Key on status only so the button flips visibility when
  // the session transitions running → idle, without burning a refetch on every
  // assistant SSE tick (which advances last_activity_at).
  const { data: reviewCapabilitiesData } = useQuery({
    queryKey: ["session", id, "review-capabilities", session?.status],
    queryFn: () => api.sessions.getReviewCapabilities(id),
    enabled: !!session && canRequestReview,
  });
  const reviewCapabilities = reviewCapabilitiesData?.data;
  const startReviewMutation = useMutation({
    mutationFn: (mode: import("@/lib/types").SessionReviewMode) => api.sessions.startReview(id, mode),
    onMutate: (mode) => {
      setPendingReviewMode(mode);
    },
    onSuccess: () => {
      // Stay in the current view; the assistant message will stream into the
      // session timeline via SSE just like any other turn. Bust caches so the
      // status flips from idle → running immediately.
      void queryClient.invalidateQueries({ queryKey: ["session", id] });
      void queryClient.invalidateQueries({ queryKey: ["session", id, "messages"] });
      void queryClient.invalidateQueries({ queryKey: ["session", id, "timeline"] });
      setPendingReviewMode(null);
    },
    onError: (err) => {
      setPendingReviewMode(null);
      const message = err instanceof ApiError ? err.message : "Failed to start review";
      toast.error(message);
    },
  });
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
      setPendingRepairAction(action);
    },
    onSuccess: (response) => {
      setPendingRepairAction(null);
      void queryClient.invalidateQueries({ queryKey: ["pull-request", pullRequestId, "health"] });
      void queryClient.invalidateQueries({ queryKey: ["session", id] });
      void queryClient.invalidateQueries({ queryKey: ["session", id, "timeline"] });
      void queryClient.invalidateQueries({ queryKey: ["session", id, "pr"] });

      if (response.data.session_id !== id) {
        router.push(`/sessions/${response.data.session_id}`);
      }
    },
    onError: (err) => {
      setPendingRepairAction(null);
      setRepairActionError(err instanceof ApiError ? err.message : "Failed to open repair session");
    },
  });
  useEffect(() => {
    if (!pullRequestId || prData?.data?.status !== "open") {
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
  }, [apiBase, prData?.data?.status, pullRequestId, queryClient]);
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
  const showExpiredPRAction = hasSessionChanges && !hasSnapshot && !hasPR && !isRunning;

  const { data: ghStatus } = useQuery({
    queryKey: ["github-status"],
    queryFn: () => api.githubStatus.get(),
    enabled: canCreatePR,
    staleTime: 5 * 60 * 1000,
  });

  const clearPRResumeParams = useCallback(() => {
    void setResumePRParam(null);
    if (githubPRParam === "connected") {
      void setGithubPRParam(null);
    }
  }, [githubPRParam, setGithubPRParam, setResumePRParam]);

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
        setPRAuthPrompt(err.details);
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

  useEffect(() => {
    if (!canCreatePR || !resumePRParam) return;
    if (resumeAttemptRef.current === resumePRParam) return;
    resumeAttemptRef.current = resumePRParam;
    createPRMutation.mutate({ authorMode: "user", resumeToken: resumePRParam });
  }, [canCreatePR, createPRMutation, resumePRParam]);

  const sessionDiff = session?.diff;
  const diffStats = useMemo(() => {
    if (!sessionDiff) return null;
    return parseDiffStats(sessionDiff);
  }, [sessionDiff]);

  // --- Shared review state (lifted from old ChangesTab) ---

  // Hooks can't be called conditionally, so provide a stub when session hasn't loaded yet.
  // useDiffViewState only reads `diff` and `diff_history` — the stub satisfies that contract.
  const diffViewState = useDiffViewState(session ?? { diff: null, diff_history: [] } as unknown as Session);
  const { files: allDiffFiles, filteredFiles, passes, passRange, setPassRange, diffSearchQuery, setDiffSearchQuery } = diffViewState;

  const {
    comments,
    commentsByLine,
    openCount: footerOpenCommentCount,
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
  const chatPanelScrollToLiveEdgeRef = useRef<(() => void) | null>(null);
  const openComments = useMemo(() => comments.filter((comment) => !comment.resolved), [comments]);
  const composerCanSendMessage = session?.status !== "skipped" && session?.status !== "pending" && session?.sandbox_state !== "destroyed";
  const composerIsRunning = session?.status === "running";
  const composerIsSnapshotExpired = session?.sandbox_state === "destroyed";
  const composerIsClaudeCode = session?.agent_type === "claude_code";
  const composerLacksHeadlessResume = session ? (AGENTS_BY_KEY[session.agent_type]?.lacksHeadlessResume ?? false) : false;
  const composerAvailableModels = useMemo(() => {
    if (!session) {
      return [];
    }
    const agentType = AGENTS.find((agent) => agent.key === session.agent_type);
    return agentType?.models ?? [];
  }, [session]);

  async function handleComposerUpload(event: React.ChangeEvent<HTMLInputElement>) {
    const fileList = event.target.files;
    if (!fileList || fileList.length === 0) return;

    const files = Array.from(fileList);
    const oversized = files.filter((file) => file.size > MAX_FILE_SIZE);
    if (oversized.length > 0) {
      setComposerUploadError(`File${oversized.length > 1 ? "s" : ""} too large (max 10 MB): ${oversized.map((file) => file.name).join(", ")}`);
      event.target.value = "";
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
      event.target.value = "";
    }
  }

  const handleRemoveComposerAttachment = useCallback((url: string) => {
    setComposerAttachments((previous) => previous.filter((attachment) => attachment !== url));
  }, []);

  const sendMutation = useMutation({
    mutationFn: (opts: { planMode?: boolean; overrideMessage?: string } = {}) => {
      setComposerUploadError(null);
      const draftMessage = opts.overrideMessage ?? composerMessage;
      const formattedMessage = openComments.length > 0
        ? formatReviewMessage(openComments, filteredFiles, draftMessage)
        : draftMessage;
      const isPlanRequest = opts.planMode ?? composerPlanMode;

      return api.sessions.sendMessage(id, {
        message: formattedMessage,
        images: composerAttachments.length > 0 ? composerAttachments : undefined,
        references: composerReferences.length > 0 ? composerReferences : undefined,
        commands: composerCommands.length > 0 ? composerCommands : undefined,
        planMode: isPlanRequest,
        model: composerSelectedModel || undefined,
      });
    },
    onSuccess: () => {
      setComposerMessage("");
      setComposerAttachments([]);
      setComposerReferences([]);
      setComposerCommands([]);
      setComposerPlanMode(false);
      if (composerTextareaRef.current) {
        composerTextareaRef.current.style.height = "auto";
      }
      chatPanelScrollToLiveEdgeRef.current?.();
      queryClient.invalidateQueries({ queryKey: ["session", id] });
      queryClient.invalidateQueries({ queryKey: ["session", id, "timeline"] });
    },
  });

  const cancelMutation = useMutation({
    mutationFn: () => api.sessions.cancelSession(id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["session", id] });
    },
  });

  const handleApprovePlan = useCallback(() => {
    if (!composerCanSendMessage || sendMutation.isPending) return;
    sendMutation.mutate({
      planMode: false,
      overrideMessage: "The plan looks good. Please proceed with executing the implementation plan above. Make all the changes as described.",
    });
  }, [composerCanSendMessage, sendMutation]);

  const handleAdjustPlan = useCallback(() => {
    setComposerMessage("Please adjust the plan: ");
    setComposerPlanMode(false);
    composerTextareaRef.current?.focus();
  }, []);

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

  const changesCount = diffStats?.filesChanged;
  const showValidationTab = !session.triggered_by_user_id;
  const prState = session.pr_creation_state;
  const snapshotExpired = !session.snapshot_key;
  const serverSnapshotUnavailable = /^session state expired\b/i.test(session.pr_creation_error || "");
  const localSnapshotUnavailable = localPRActionError?.code === "SNAPSHOT_EXPIRED";
  const snapshotUnavailable = snapshotExpired || localSnapshotUnavailable || serverSnapshotUnavailable;
  const snapshotExpiredMessage = normalizeSnapshotPRMessage(
    localSnapshotUnavailable ? localPRActionError.message : session.pr_creation_error
  );
  const ghBlocked = ghStatus?.pr_authorship_mode === "user_required" && !ghStatus?.connected;
  const succeededButNoPR = prState === "succeeded" && !hasPR;
  const prActionError = hasPR
    ? null
    : (localSnapshotUnavailable ? snapshotExpiredMessage : localPRActionError?.message) ||
      (snapshotUnavailable ? snapshotExpiredMessage : null) ||
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
    prActionTitle = snapshotExpiredMessage;
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

  const prErrorNotice = prActionError ? {
    title: prErrorTitle(snapshotUnavailable, localPRActionError?.code),
    description: prActionError,
    action: prActionDisabled ? undefined : {
      label: prActionLabel,
      onClick: () => createPRMutation.mutate(undefined),
    },
  } : null;
  const trimmedDraftTitle = draftTitle.trim();
  const canSaveTitle = trimmedDraftTitle.length > 0 && trimmedDraftTitle !== currentTitle && !updateSessionMutation.isPending;

  // Right-panel content. Rendered inline on desktop and inside a bottom sheet
  // on mobile — the same JSX in both places so tab state stays consistent.
  const panelTabsEl = (
    <Tabs
      value={detailTab}
      onValueChange={(v) => handleDetailTabClick(v as DetailTab)}
      className="flex flex-col flex-1 min-h-0 gap-0"
    >
      <div className="border-b border-border px-2 py-2 shrink-0">
        <div className="flex items-center gap-2">
          <TabsList variant="line" size="sm" className="border-b-0 flex-1">
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
          {canRequestReview && (
            <ReviewButton
              capabilities={reviewCapabilities}
              pendingMode={pendingReviewMode}
              onReview={(mode) => startReviewMutation.mutate(mode)}
            />
          )}
          {hasPR && prData?.data?.github_pr_url ? (
            <>
              {prStatus === "closed" && (
                <Badge variant="secondary" className="h-7 px-2 text-xs">
                  PR closed
                </Badge>
              )}
              <a href={prData.data.github_pr_url} target="_blank" rel="noopener noreferrer">
                <Button variant="outline" size="sm" className="h-7 text-xs gap-1.5">
                  <ExternalLink className="h-3 w-3" />
                  View PR
                </Button>
              </a>
            </>
          ) : showPRAction && !prErrorNotice ? (
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
          ) : null}
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
          filteredFiles={filteredFiles}
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
        />
      </TabsContent>
      <TabsContent value="overview" className="flex-1 overflow-y-auto scrollbar-hide p-4">
        <div className="space-y-4">
          {pullRequestId && prStatus === "open" && (
            prHealth ? (
              <PRHealthBanner
                health={prHealth}
                pendingAction={pendingRepairAction}
                repairError={repairActionError}
                onFixTests={() => startRepairMutation.mutate("fix_tests")}
                onResolveConflicts={() => startRepairMutation.mutate("resolve_conflicts")}
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
                    <p className="text-sm text-foreground">{closedPRSummary}</p>
                    <p className="text-sm text-muted-foreground">
                      This pull request is no longer active. Create a follow-up revision if you want to ship a new attempt.
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
        {/* Session header bar */}
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
                <Button
                  size="icon"
                  variant="ghost"
                  aria-label="Save title"
                  disabled={!canSaveTitle}
                  onClick={() => updateSessionMutation.mutate(trimmedDraftTitle)}
                >
                  <Check className="h-4 w-4" />
                </Button>
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
          </div>
          {/* Desktop toggle: hides/shows the inline right panel. */}
          <Button
            variant="ghost"
            size="icon"
            className={cn("hidden md:inline-flex h-8 w-8 shrink-0", centerMode === "review" && showDetailPanel && "opacity-30 cursor-not-allowed")}
            disabled={centerMode === "review" && showDetailPanel}
            onClick={() => setShowDetailPanel(!showDetailPanel)}
            title={
              centerMode === "review" && showDetailPanel
                ? "File tree required during review"
                : showDetailPanel ? "Hide details" : "Show details"
            }
          >
            {showDetailPanel ? <PanelRightClose className="h-4 w-4" /> : <PanelRightOpen className="h-4 w-4" />}
          </Button>
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

        {/* Center content — either chat or diff review */}
        <div className="flex-1 min-h-0 relative">
          {/* Chat panel — always mounted to preserve scroll, SSE connections, etc. */}
          <div className={cn("h-full", centerMode !== "chat" && "hidden")}>
            <ChatPanel
              session={session}
              sessionId={id}
              isActive={isActive}
              onDiffClick={() => openReview()}
              onApprovePlan={handleApprovePlan}
              onAdjustPlan={handleAdjustPlan}
              onRegisterScrollToLiveEdge={(scrollToLiveEdge) => {
                chatPanelScrollToLiveEdgeRef.current = scrollToLiveEdge;
              }}
            />
          </div>
          {/* Review diff view — mounted only when active */}
          {centerMode === "review" && (
            <div className="h-full animate-in fade-in duration-150 flex flex-col">
              <div className="flex-1 min-h-0">
                <ReviewDiffView
                  sessionId={id}
                  files={filteredFiles}
                  allFiles={allDiffFiles}
                  activeFileIndex={activeFileIndex}
                  onFileChange={setActiveFileIndex}
                  onBack={exitReview}
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

        {session.agent_type !== "pm_agent" && (
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
              onRemoveAttachment={handleRemoveComposerAttachment}
              openComments={openComments}
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
              onSend={() => sendMutation.mutate({})}
              textareaRef={composerTextareaRef}
              uploadInputRef={composerUploadInputRef}
              references={composerReferences}
              onReferencesChange={setComposerReferences}
              commands={composerCommands}
              onCommandsChange={setComposerCommands}
              repositoryId={session.repository_id}
              branch={session.target_branch}
              agentType={session.agent_type}
            />
          </>
        )}

        {/* Session footer bar */}
        <SessionFooter
          status={session.status}
          currentTurn={session.current_turn}
          diffStats={diffStats}
          onDiffClick={centerMode === "review" ? undefined : () => openReview()}
          openCommentCount={footerOpenCommentCount}
          onCommentsClick={centerMode === "review" ? undefined : () => openReview()}
        />
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
          className="md:hidden h-[85vh] max-h-[85vh] min-h-[60vh] p-0 flex flex-col gap-0 bg-background"
        >
          <SheetTitle className="sr-only">Session details</SheetTitle>
          {panelTabsEl}
        </SheetContent>
      </Sheet>
      <AlertDialog
        open={!!prAuthPrompt}
        onOpenChange={(open) => {
          if (!open) setPRAuthPrompt(null);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Open this pull request as yourself?</AlertDialogTitle>
            <AlertDialogDescription>
              Authorize GitHub once to open PRs as you. If you skip this, 143 can still open the PR as the app.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            {prAuthPrompt?.can_fallback_to_app ? (
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
            <AlertDialogAction
              onClick={(event) => {
                event.preventDefault();
                if (!prAuthPrompt) return;
                api.githubStatus.connect(prAuthPrompt.resume_token);
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
