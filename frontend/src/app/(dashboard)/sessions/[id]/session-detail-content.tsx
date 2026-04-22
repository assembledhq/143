"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useQueryState } from "nuqs";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import {
  AlertTriangle,
  ArrowDown,
  ArrowUp,
  ClipboardList,
  ExternalLink,
  Eye,
  FileCode2,
  GitPullRequest,
  Loader2,
  RefreshCw,
  CheckCircle2,
  XCircle,
  MinusCircle,
  Square,
  PanelRightOpen,
  PanelRightClose,
  Clock,
  MessageSquare,
  Paperclip,
} from "lucide-react";
import { toast } from "sonner";
import { Badge } from "@/components/ui/badge";
import { MarkdownContent } from "@/components/markdown";
import { Button } from "@/components/ui/button";
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
import { Textarea } from "@/components/ui/textarea";
import { ChatTimeline } from "@/components/chat-timeline";
import { api, ApiError } from "@/lib/api";
import { AGENTS, AGENTS_BY_KEY } from "@/lib/agents";
import { SSE_EVENT, addSSEListener } from "@/lib/sse";
import { buildTimeline, buildTimelineFromResponse } from "@/lib/timeline";
import { parseDiffStats, type DiffFile } from "@/lib/diff-parser";
import { formatReviewMessage } from "@/lib/format-review-message";
import {
  readStoredSessionScrollPosition,
  resolveInitialSessionAnchor,
  writeStoredSessionScrollPosition,
} from "@/lib/session-open-position";
import type { Session, SessionLog, SessionMessage, SessionReviewComment, User, Validation, CodexAuthStatus, SingleResponse } from "@/lib/types";
import { AuditLogTrigger } from "@/components/audit/audit-log-trigger";
import { ResizeHandle } from "@/components/resize-handle";
import { DiffStatsBadge, FileTree, SessionFooter, CommentsSummary, ReviewDiffView, PassSelector, type DiffPassEntry, type PassRange } from "@/components/code-review";
import { useReviewComments } from "@/hooks/use-review-comments";
import { useDiffViewState } from "@/hooks/use-diff-view-state";
import { CodexDeviceCodeModal } from "@/components/codex-device-code-modal";
import { AgentBadge } from "@/components/agent-badge";
import { PreviewPanel } from "@/components/preview/preview-panel";
import { PendingAttachmentStrip } from "@/components/pending-attachment-strip";
import { useAuth } from "@/hooks/use-auth";
import { cn, sessionTitle, formatTimeAgo } from "@/lib/utils";

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

/** Returns true if the session has been pending for more than 2 minutes. */
function isPendingTooLong(createdAt: string): boolean {
  return Date.now() - new Date(createdAt).getTime() > 2 * 60 * 1000;
}


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

const terminalSessionStatuses = new Set(["completed", "pr_created", "failed", "cancelled", "skipped"]);

function isPRAuthInterceptDetails(value: unknown): value is PRAuthInterceptDetails {
  if (!value || typeof value !== "object") return false;
  const details = value as Partial<PRAuthInterceptDetails>;
  return typeof details.connect_url === "string" &&
    typeof details.resume_token === "string" &&
    typeof details.can_fallback_to_app === "boolean";
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
// Review comment input bar (shown at bottom during review mode)
// ---------------------------------------------------------------------------

function ReviewCommentInput({
  sessionId,
  comments,
  diffFiles,
  canSendMessage,
}: {
  sessionId: string;
  comments: SessionReviewComment[];
  diffFiles: DiffFile[];
  canSendMessage: boolean;
}) {
  const [message, setMessage] = useState("");
  const queryClient = useQueryClient();
  const textareaRef = useRef<HTMLTextAreaElement>(null);

  const openComments = useMemo(() => comments.filter((c) => !c.resolved), [comments]);

  const sendMutation = useMutation({
    mutationFn: () => {
      const formatted = formatReviewMessage(openComments, diffFiles, message);
      return api.sessions.sendMessage(sessionId, formatted);
    },
    onSuccess: () => {
      setMessage("");
      if (textareaRef.current) {
        textareaRef.current.style.height = "auto";
      }
      queryClient.invalidateQueries({ queryKey: ["session", sessionId] });
      queryClient.invalidateQueries({ queryKey: ["session", sessionId, "timeline"] });
    },
  });

  // Auto-resize textarea
  useEffect(() => {
    const el = textareaRef.current;
    if (!el) return;
    el.style.height = "auto";
    el.style.height = `${Math.min(el.scrollHeight, 200)}px`;
  }, [message]);

  const hasContent = message.trim() || openComments.length > 0;

  function handleKeyDown(e: React.KeyboardEvent) {
    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault();
      if (hasContent && canSendMessage && !sendMutation.isPending) {
        sendMutation.mutate();
      }
    }
  }

  return (
    <div className="border-t border-border p-3 bg-background shrink-0">
      <div className={cn("rounded-xl border border-border bg-muted/30 focus-within:border-ring focus-within:ring-1 focus-within:ring-ring")}>
        {/* Open comments as chips */}
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
                  <span className="text-muted-foreground/40">&mdash;</span>
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
          onChange={(e) => setMessage(e.target.value)}
          onKeyDown={handleKeyDown}
          placeholder={
            openComments.length > 0
              ? "Add instructions or send comments to agent..."
              : "Ask to make changes, @mention files..."
          }
          disabled={!canSendMessage || sendMutation.isPending}
          className="min-h-[44px] max-h-[200px] resize-none border-none bg-transparent shadow-none focus-visible:ring-0"
        />

        <div className="flex items-center gap-1 px-2 pb-2">
          {openComments.length > 0 && (
            <span className="text-xs text-muted-foreground">
              {openComments.length} comment{openComments.length > 1 ? "s" : ""} attached
            </span>
          )}
          <div className="ml-auto">
            <Button
              size="icon"
              variant="default"
              className="h-8 w-8 shrink-0 rounded-lg"
              title="Send to agent"
              disabled={!hasContent || !canSendMessage || sendMutation.isPending}
              onClick={() => sendMutation.mutate()}
            >
              {sendMutation.isPending ? (
                <Loader2 className="h-4 w-4 animate-spin" />
              ) : (
                <ArrowUp className="h-4 w-4" />
              )}
            </Button>
          </div>
        </div>
      </div>

      {sendMutation.error && (
        <div className="flex items-center gap-2 mt-2 text-xs text-destructive">
          <AlertTriangle className="h-3 w-3 shrink-0" />
          {sendMutation.error instanceof Error ? sendMutation.error.message : "Failed to send"}
        </div>
      )}
    </div>
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

function isNearBottom(el: HTMLElement): boolean {
  return el.scrollHeight - el.scrollTop - el.clientHeight < SCROLL_NEAR_BOTTOM_THRESHOLD;
}

function ChatPanel({ session, sessionId, isActive, onDiffClick }: { session: Session; sessionId: string; isActive: boolean; onDiffClick?: () => void }) {
  const queryClient = useQueryClient();
  const { user } = useAuth();
  const [message, setMessage] = useState("");
  const [planMode, setPlanMode] = useState(false);
  const [selectedModel, setSelectedModel] = useState("");
  const [streamedLogs, setStreamedLogs] = useState<SessionLog[]>([]);
  const [attachments, setAttachments] = useState<string[]>([]);
  const [isUploading, setIsUploading] = useState(false);
  const [uploadError, setUploadError] = useState<string | null>(null);
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const uploadInputRef = useRef<HTMLInputElement>(null);
  const scrollRef = useRef<HTMLDivElement>(null);
  const isNearBottomRef = useRef(false);
  const initialAnchorAppliedRef = useRef(false);
  const saveScrollTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const seenLogIds = useRef<Set<number>>(new Set());
  const reconnectAttempts = useRef(0);
  const reconnectTimer = useRef<ReturnType<typeof setTimeout>>(null);
  const apiBase = process.env.NEXT_PUBLIC_API_URL || "";
  const isClaudeCode = session.agent_type === "claude_code";
  const [showJumpToLatest, setShowJumpToLatest] = useState(false);
  // Sourced from the AGENTS registry so the per-agent flag lives in one place
  // (see lacksHeadlessResume on AgentMeta). When true, follow-up runs replay
  // only the new user message against the restored filesystem — prior
  // conversation context is not sent back to the CLI. See runStreamingAgent
  // in internal/services/agent/adapters/stream_parser.go.
  const lacksHeadlessResume = AGENTS_BY_KEY[session.agent_type]?.lacksHeadlessResume ?? false;
  const viewerScope = useMemo(
    () => (user ? { userId: user.id, orgId: user.org_id } : null),
    [user],
  );

  const isRunning = session.status === "running";
  const isSnapshotExpired = session.sandbox_state === "destroyed";
  // Allow messaging in any state except "skipped" (never ran, no workspace),
  // "pending" (agent not started yet), and sessions whose sandbox snapshot has
  // expired (sandbox_state === "destroyed") — the environment no longer exists.
  const canSendMessage = session.status !== "skipped" && session.status !== "pending" && !isSnapshotExpired;

  const availableModels = useMemo(() => {
    const agentType = AGENTS.find((a) => a.key === session.agent_type);
    return agentType?.models ?? [];
  }, [session.agent_type]);

  const timelineQuery = useQuery({
    queryKey: ["session", sessionId, "timeline"],
    queryFn: () => api.sessions.getTimeline(sessionId),
    refetchInterval: isActive ? 3000 : false,
  });

  // Fetch the linked issue to display its description as the initial prompt.
  // Manual sessions have no issue — issue_id comes back as the zero UUID.
  const hasIssue = !!session.issue_id && session.issue_id !== "00000000-0000-0000-0000-000000000000";
  const issueQuery = useQuery({
    queryKey: ["issue", session.issue_id],
    queryFn: () => api.issues.get(session.issue_id),
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

    for (const entry of baseTimelineEntries) {
      switch (entry.kind) {
        case "message":
          if (entry.data.role === "user" && entry.data.content.startsWith("[PLAN_MODE]\n")) {
            planModeSeedMessages.push(entry.data);
          }
          if (entry.data.role === "assistant") {
            const contents = assistantTranscriptByTurn.get(entry.data.turn_number) ?? new Set<string>();
            contents.add(entry.data.content);
            assistantTranscriptByTurn.set(entry.data.turn_number, contents);
          }
          break;
        case "plan_message": {
          const contents = assistantTranscriptByTurn.get(entry.data.turn_number) ?? new Set<string>();
          contents.add(entry.data.content);
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
      return !assistantTranscriptByTurn.get(log.turn_number)?.has(log.message);
    });

    if (overlayLogs.length === 0) return baseTimelineEntries;
    const overlayEntries = buildTimeline(planModeSeedMessages, overlayLogs).filter((entry) => entry.kind !== "message");
    return [...baseTimelineEntries, ...overlayEntries];
  }, [baseTimelineEntries, streamedLogs]);
  const hasLoadedTimelineInputs = timelineQuery.isFetched && (!hasIssue || issueQuery.isFetched);

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

  const scrollToLiveEdge = useCallback(() => {
    const el = scrollRef.current;
    if (!el) return;
    el.scrollTop = el.scrollHeight;
    isNearBottomRef.current = true;
    setShowJumpToLatest(false);
  }, []);

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

  async function handleFileUpload(event: React.ChangeEvent<HTMLInputElement>) {
    const fileList = event.target.files;
    if (!fileList || fileList.length === 0) return;

    const files = Array.from(fileList);
    const oversized = files.filter((f) => f.size > MAX_FILE_SIZE);
    if (oversized.length > 0) {
      setUploadError(`File${oversized.length > 1 ? "s" : ""} too large (max 10 MB): ${oversized.map((f) => f.name).join(", ")}`);
      event.target.value = "";
      return;
    }

    setIsUploading(true);
    setUploadError(null);
    try {
      const results = await Promise.all(
        files.map((file) => api.uploads.upload(file))
      );
      setAttachments((prev) => [...prev, ...results.map((r) => r.url)]);
    } catch (err) {
      setUploadError(err instanceof Error ? err.message : "Upload failed");
    } finally {
      setIsUploading(false);
      event.target.value = "";
    }
  }

  function removeAttachment(url: string) {
    setAttachments((prev) => prev.filter((a) => a !== url));
  }

  const sendMutation = useMutation({
    mutationFn: (opts: { planMode?: boolean; overrideMessage?: string } = {}) => {
      setUploadError(null);
      const msg = opts.overrideMessage ?? message;
      const isPlan = opts.planMode ?? planMode;
      return api.sessions.sendMessage(sessionId, msg, attachments.length > 0 ? attachments : undefined, isPlan, selectedModel || undefined);
    },
    onSuccess: () => {
      setMessage("");
      setAttachments([]);
      setPlanMode(false);
      if (textareaRef.current) {
        textareaRef.current.style.height = "auto";
      }
      // Scroll to bottom after sending a message so the user sees the response.
      scrollToLiveEdge();
      queryClient.invalidateQueries({ queryKey: ["session", sessionId] });
      queryClient.invalidateQueries({ queryKey: ["session", sessionId, "timeline"] });
    },
  });

  const endMutation = useMutation({
    mutationFn: () => api.sessions.endSession(sessionId),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["session", sessionId] });
    },
  });

  const cancelMutation = useMutation({
    mutationFn: () => api.sessions.cancelSession(sessionId),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["session", sessionId] });
    },
  });
  // Auto-resize textarea
  useEffect(() => {
    const el = textareaRef.current;
    if (!el) return;
    el.style.height = "auto";
    el.style.height = `${Math.min(el.scrollHeight, 200)}px`;
  }, [message]);

  // Track whether the user is scrolled near the bottom.
  const handleScroll = useCallback(() => {
    const el = scrollRef.current;
    if (!el) return;
    syncScrollState(el);
    schedulePersistScrollPosition(el.scrollTop);
  }, [schedulePersistScrollPosition, syncScrollState]);

  useEffect(() => {
    initialAnchorAppliedRef.current = false;
    setShowJumpToLatest(false);
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

    scrollToLiveEdge();
    initialAnchorAppliedRef.current = true;
  }, [hasLoadedTimelineInputs, isRunning, scrollToLiveEdge, sessionId, syncScrollState, timelineEntries, viewerScope]);

  // Only auto-scroll to bottom when new entries arrive if the user is already near the bottom.
  useEffect(() => {
    if (scrollRef.current && isNearBottomRef.current) {
      scrollToLiveEdge();
    }
  }, [scrollToLiveEdge, timelineEntries.length]);

  const hasContent = message.trim() || attachments.length > 0;

  // Plan mode callbacks for the timeline approve/adjust buttons.
  const handleApprovePlan = useCallback(() => {
    if (!canSendMessage || sendMutation.isPending) return;
    sendMutation.mutate({ planMode: false, overrideMessage: "The plan looks good. Please proceed with executing the implementation plan above. Make all the changes as described." });
  }, [canSendMessage, sendMutation]);

  const handleAdjustPlan = useCallback(() => {
    setMessage("Please adjust the plan: ");
    setPlanMode(false);
    textareaRef.current?.focus();
  }, []);

  function handleKeyDown(e: React.KeyboardEvent) {
    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault();
      if (hasContent && canSendMessage && !sendMutation.isPending && !isRunning) {
        sendMutation.mutate({});
      }
    }
    // Shift+Tab toggles plan mode for Claude Code sessions.
    if (e.key === "Tab" && e.shiftKey && isClaudeCode && canSendMessage) {
      e.preventDefault();
      setPlanMode((prev) => !prev);
    }
  }

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
        {timelineEntries.length === 0 && !isRunning && session.status !== "pending" && (
          <div className="flex items-center justify-center py-12">
            <div className="text-center space-y-2 max-w-[320px]">
              {session.status === "pending" ? (
                <>
                  <Loader2 className="h-8 w-8 text-muted-foreground/40 mx-auto animate-spin" />
                  <p className="text-xs font-medium text-muted-foreground">Waiting to start</p>
                  <p className="text-xs text-muted-foreground/60">
                    This session is queued and waiting for an available slot. Queued {formatTimeAgo(session.created_at)}.
                  </p>
                  {isPendingTooLong(session.created_at) && (
                    <div className="mt-3 rounded-md bg-amber-50 dark:bg-amber-950/30 border border-amber-200 dark:border-amber-800 px-3 py-2 text-left">
                      <p className="text-xs font-medium text-amber-700 dark:text-amber-400 flex items-center gap-1.5">
                        <AlertTriangle className="h-3 w-3 shrink-0" />
                        Taking longer than expected
                      </p>
                      <p className="text-xs text-amber-600 dark:text-amber-500 mt-1">
                        This session has been waiting for over 2 minutes. It may be blocked by other running sessions or an internal issue. Try cancelling and retrying if it doesn&apos;t start soon.
                      </p>
                    </div>
                  )}
                </>
              ) : (
                <>
                  <MessageSquare className="h-8 w-8 text-muted-foreground/40 mx-auto" />
                  <p className="text-xs font-medium text-muted-foreground">No activity yet</p>
                  <p className="text-xs text-muted-foreground/60">The session is processing its initial turn.</p>
                </>
              )}
            </div>
          </div>
        )}
        <ChatTimeline
          entries={timelineEntries}
          isRunning={isRunning}
          diffStats={session.diff_stats}
          onDiffClick={onDiffClick}
          onApprovePlan={canSendMessage ? handleApprovePlan : undefined}
          onAdjustPlan={canSendMessage ? handleAdjustPlan : undefined}
          getEntryContainerProps={(_, index) =>
            ({
              "data-session-entry-index": index,
            }) as React.HTMLAttributes<HTMLDivElement> & Record<`data-${string}`, string | number | undefined>
          }
        />
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

      {/* Error display */}
      {(() => {
        const firstError = uploadError || [sendMutation, endMutation, cancelMutation].find(m => m.error)?.error;
        if (!firstError) return null;
        const msg = typeof firstError === "string" ? firstError : (firstError instanceof Error ? firstError.message : "An error occurred");
        return (
          <div className="flex items-center gap-2 px-4 py-2 text-xs text-destructive border-t bg-destructive/5">
            <AlertTriangle className="h-3 w-3 shrink-0" />
            {msg}
          </div>
        );
      })()}

      {/* Snapshot expired banner */}
      {isSnapshotExpired && (
        <div className="flex items-center gap-2 px-4 py-2.5 text-xs border-t bg-amber-50 dark:bg-amber-950/20 border-amber-200 dark:border-amber-800/40 text-amber-800 dark:text-amber-300">
          <Clock className="h-3.5 w-3.5 shrink-0" />
          <span>
            This session&apos;s environment has expired. Sessions can be continued for up to 30 days after their last activity. To make further changes, please start a new session.
          </span>
        </div>
      )}

      {/* No-headless-resume agents: warn users that follow-ups don't carry prior context. */}
      {lacksHeadlessResume && canSendMessage && !isSnapshotExpired && (
        <div className="flex items-center gap-2 px-4 py-2.5 text-xs border-t bg-sky-50 dark:bg-sky-950/20 border-sky-200 dark:border-sky-800/40 text-sky-800 dark:text-sky-300">
          <AlertTriangle className="h-3.5 w-3.5 shrink-0" />
          <span>
            {AGENTS_BY_KEY[session.agent_type]?.label ?? session.agent_type} doesn&apos;t support headless conversation resume. Follow-up messages run against the restored filesystem, but earlier chat context is not replayed — include anything you need the agent to remember.
          </span>
        </div>
      )}

      {/* Input bar — hidden for PM agent sessions (PM agent doesn't accept interactive input) */}
      {session.agent_type !== "pm_agent" && <div className="border-t border-border p-3 bg-background">
        {/* Plan mode indicator */}
        {planMode && (
          <div className="flex items-center gap-2 mb-2 px-1">
            <div className="flex items-center gap-1.5 rounded-full bg-amber-500/10 border border-amber-200 dark:border-amber-800/50 px-2.5 py-1">
              <ClipboardList className="h-3 w-3 text-amber-600 dark:text-amber-400" />
              <span className="text-xs font-medium text-amber-700 dark:text-amber-400">Plan Mode</span>
              <button
                onClick={() => setPlanMode(false)}
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
          <Textarea
            ref={textareaRef}
            value={message}
            onChange={(e) => setMessage(e.target.value)}
            onKeyDown={handleKeyDown}
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
            disabled={!canSendMessage || sendMutation.isPending || isRunning}
            className="min-h-[44px] max-h-[200px] resize-none border-none bg-transparent shadow-none focus-visible:ring-0"
          />

          <PendingAttachmentStrip
            attachments={attachments}
            isUploading={isUploading}
            onRemove={removeAttachment}
            size="md"
            className="px-3 pb-2"
          />

          <div className="flex items-center gap-1 px-2 pb-2">
            {/* File upload button */}
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
              onChange={handleFileUpload}
            />

            {availableModels.length > 0 && (
              <Select value={selectedModel} onValueChange={setSelectedModel}>
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
                onClick={() => setPlanMode(true)}
                className="flex items-center gap-1 h-8 px-2 text-xs text-muted-foreground hover:text-foreground transition-colors rounded-md"
                title="Switch to plan mode (Shift+Tab)"
              >
                <ClipboardList className="h-3.5 w-3.5" />
                <span>Plan</span>
              </button>
            )}

            <div className="ml-auto flex items-center gap-1">
              {isRunning ? (
                <Button
                  size="icon"
                  variant="outline"
                  className="h-8 w-8 shrink-0 rounded-lg"
                  title="Cancel session"
                  disabled={cancelMutation.isPending}
                  onClick={() => cancelMutation.mutate()}
                >
                  <Square className="h-3 w-3" />
                </Button>
              ) : (
                <Button
                  size="icon"
                  variant={planMode ? "outline" : "default"}
                  className={cn("h-8 w-8 shrink-0 rounded-lg", planMode && "border-amber-300 dark:border-amber-700 text-amber-700 dark:text-amber-400 hover:bg-amber-50 dark:hover:bg-amber-950/30")}
                  title={planMode ? "Send plan request" : "Send message"}
                  disabled={!hasContent || !canSendMessage || sendMutation.isPending || isRunning}
                  onClick={() => sendMutation.mutate({})}
                >
                  {planMode ? <ClipboardList className="h-4 w-4" /> : <ArrowUp className="h-4 w-4" />}
                </Button>
              )}
            </div>
          </div>
        </div>
      </div>}
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
  const [detailWidth, setDetailWidth] = useState(DEFAULT_DETAIL);
  const [activeFileIndex, setActiveFileIndex] = useState(0);

  const handleDetailResize = useCallback((delta: number) => {
    setDetailWidth((w) => Math.min(MAX_DETAIL, Math.max(MIN_DETAIL, w - delta)));
  }, []);

  // --- Enter review mode ---
  const openReview = useCallback((fileIndex?: number) => {
    if (fileIndex !== undefined) setActiveFileIndex(fileIndex);
    setReviewParam("active");
    setDetailTab("changes");
    setShowDetailPanel(true);
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
  const [localPRActionError, setLocalPRActionError] = useState<string | null>(null);
  const [prAuthPrompt, setPRAuthPrompt] = useState<PRAuthInterceptDetails | null>(null);
  const resumeAttemptRef = useRef<string | null>(null);

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

  const queryClient = useQueryClient();

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
    onSuccess: () => {
      setLocalPRActionError(null);
      setLocalPRState("queued");
      queryClient.invalidateQueries({ queryKey: ["session", id] });
      queryClient.invalidateQueries({ queryKey: ["session", id, "pr"] });
    },
    onError: (err) => {
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
      setLocalPRActionError(msg);
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

  return (
    <div className="flex h-full">
      {/* Center area: chat or review diff view */}
      <div className="flex-1 min-w-0 flex flex-col">
        {/* Session header bar */}
        <div className="border-b border-border px-4 py-3 bg-background flex items-center justify-between shrink-0">
          <div className="min-w-0 flex-1 flex items-center gap-2">
            <h1 className="text-sm font-medium text-foreground truncate">
              {sessionTitle(session)}
            </h1>
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
          <Button
            variant="ghost"
            size="icon"
            className={cn("h-8 w-8 shrink-0", centerMode === "review" && showDetailPanel && "opacity-30 cursor-not-allowed")}
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
        </div>

        {/* Center content — either chat or diff review */}
        <div className="flex-1 min-h-0 relative">
          {/* Chat panel — always mounted to preserve scroll, SSE connections, etc. */}
          <div className={cn("h-full", centerMode !== "chat" && "hidden")}>
            <ChatPanel session={session} sessionId={id} isActive={isActive} onDiffClick={() => openReview()} />
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
              <ReviewCommentInput
                sessionId={id}
                comments={comments}
                diffFiles={filteredFiles}
                canSendMessage={session.status !== "skipped" && session.status !== "pending" && session.sandbox_state !== "destroyed"}
              />
            </div>
          )}
        </div>

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

      {/* Detail panel (collapsible right sidebar) */}
      {showDetailPanel && (
        <>
        <ResizeHandle onResize={handleDetailResize} />
        <div
          style={{ width: detailWidth }}
          className="border-l border-border bg-muted/20 flex flex-col shrink-0 overflow-hidden"
        >
          {/* Detail tabs */}
          <Tabs
            value={detailTab}
            onValueChange={(v) => handleDetailTabClick(v as DetailTab)}
            className="flex flex-col flex-1 min-h-0 gap-0"
          >
            <div className="flex items-center border-b border-border px-2 shrink-0">
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
                <TabsTrigger value="preview">
                  <Eye className="h-3 w-3 mr-1" />
                  Preview
                </TabsTrigger>
              </TabsList>
              {(() => {
                if (hasPR && prData?.data?.github_pr_url) {
                  return (
                    <a href={prData.data.github_pr_url} target="_blank" rel="noopener noreferrer">
                      <Button variant="outline" size="sm" className="h-7 text-xs gap-1.5">
                        <ExternalLink className="h-3 w-3" />
                        View PR
                      </Button>
                    </a>
                  );
                }
                const prState = session.pr_creation_state;
                const showPRAction =
                  canCreatePR || showExpiredPRAction || queueingPR || creatingPR || finalizingPR || prState === "failed";
                if (!showPRAction) return null;
                const snapshotExpired = !session.snapshot_key;
                const snapshotExpiredMessage =
                  session.pr_creation_error || "Session state expired — re-run to create a PR.";
                const ghBlocked = ghStatus?.pr_authorship_mode === "user_required" && !ghStatus?.connected;
                const succeededButNoPR = prState === "succeeded" && !hasPR;
                const prActionError =
                  localPRActionError ||
                  (snapshotExpired ? snapshotExpiredMessage : null) ||
                  (prState === "failed" ? session.pr_creation_error || PR_ERROR_TOAST_MESSAGE : null);

                let label = "Create PR";
                let spinning = false;
                let disabled = false;
                let title: string | undefined;

                if (queueingPR) {
                  label = "Queueing PR\u2026";
                  spinning = true;
                  disabled = true;
                  title = "Sending the PR request to the queue";
                } else if (creatingPR) {
                  label = "Creating PR\u2026";
                  spinning = true;
                  disabled = true;
                  title = "Pushing changes and opening the pull request";
                } else if (snapshotExpired) {
                  label = "Create PR";
                  disabled = true;
                  title = snapshotExpiredMessage;
                } else if (succeededButNoPR) {
                  label = "Finalizing PR\u2026";
                  spinning = true;
                  disabled = true;
                } else if (prState === "failed") {
                  label = "Retry";
                  title = session.pr_creation_error || "PR creation failed";
                } else if (ghBlocked) {
                  disabled = true;
                  title = "Connect your GitHub account to create PRs";
                }

                return (
                  <div className="flex flex-col items-end gap-1 py-1">
                    <Button
                      variant="outline"
                      size="sm"
                      className="h-7 text-xs gap-1.5"
                      disabled={disabled}
                      title={title}
                      onClick={() => createPRMutation.mutate(undefined)}
                    >
                      {spinning ? (
                        <Loader2 className="h-3 w-3 animate-spin" />
                      ) : prState === "failed" || localPRActionError ? (
                        <AlertTriangle className="h-3 w-3" />
                      ) : (
                        <GitPullRequest className="h-3 w-3" />
                      )}
                      {label}
                    </Button>
                    {prActionError && (
                      <div className="max-w-64 text-right text-xs leading-4 text-destructive">
                        {prActionError}
                      </div>
                    )}
                  </div>
                );
              })()}
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
              <OverviewTab session={session} members={members} />
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
        </div>
        </>
      )}
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
