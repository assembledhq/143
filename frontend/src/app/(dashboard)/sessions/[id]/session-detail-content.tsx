"use client";

import { forwardRef, memo, useCallback, useDeferredValue, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import dynamic from "next/dynamic";
import { useRouter } from "next/navigation";
import { useQueryState } from "nuqs";
import { useInfiniteQuery, useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import {
  AlertTriangle,
  ArrowDown,
  ArrowUp,
  ClipboardList,
  ExternalLink,
  FileCode2,
  FolderTree,
  GitPullRequest,
  GitBranch,
  ChevronDown,
  Loader2,
  RefreshCw,
  CheckCircle2,
  Check,
  XCircle,
  X,
  Plus,
  Minus,
  Square,
  Settings2,
  PanelRightOpen,
  PanelRightClose,
  Clock,
  MessageSquare,
  Pencil,
} from "lucide-react";
import { LinearIcon } from "@/components/linear-icon";
import { looksLikeLinearRef } from "@/lib/linear-refs";
import { getClipboardFiles } from "@/lib/clipboard-files";
import { notify as toast } from "@/lib/notify";
import { Badge } from "@/components/ui/badge";
import { LazyMarkdownContent } from "@/components/lazy-markdown-content";
import { Button } from "@/components/ui/button";
import { ButtonGroup } from "@/components/ui/button-group";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
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
import { Tabs, TabsList, TabsTrigger, TabsContent } from "@/components/ui/tabs";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/components/ui/collapsible";
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
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import { Textarea } from "@/components/ui/textarea";
import { ChatTimeline } from "@/components/chat-timeline";
import { ContextHeader } from "@/components/context-header";
import { StatusLabel, type StatusTone } from "@/components/status-label";
import { SessionComposerAttachmentMenu } from "@/components/session-composer-attachment-menu";
import { SessionComposerTriggerPicker, flattenGroups, type TriggerPickerGroup, type TriggerPickerPosition } from "@/components/session-composer-trigger-picker";
import { useSessionComposerSlashCommands } from "@/hooks/use-session-composer-slash-commands";
import { useFileDropzone } from "@/hooks/use-file-dropzone";
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
import { applyPlanModePrefix, buildTimeline, flattenTimelineResponse, flattenTranscriptWindows, sortTimelineEntries, type TimelineEntry } from "@/lib/timeline";
import { formatReviewMessage } from "@/lib/format-review-message";
import {
  classifyPRSnapshotState,
  prErrorTitle,
  snapshotPRMessage,
} from "@/lib/session-pr-snapshot";
import {
  readStoredSessionActiveThread,
  readStoredSessionAnchorPosition,
  readStoredSessionScrollPosition,
  resolveInitialSessionThreadId,
  resolveInitialSessionAnchor,
  type SessionAnchorPosition,
  type SessionScrollViewerScope,
  writeStoredSessionAnchorPosition,
  writeStoredSessionActiveThread,
  writeStoredSessionScrollPosition,
} from "@/lib/session-open-position";
import { readCachedViewerScope } from "@/lib/viewer-scope-cache";
import {
  readStoredViewedThreadIds,
  writeStoredViewedThreadIds,
} from "@/lib/session-thread-views";
import { applySessionDetailToSessionListCaches } from "@/lib/session-list-cache";
import type { ChangesetSplitStatus, ChangesetSummary, CodingCredentialSummary, HumanInputAnswerBody, HumanInputRequest, ListResponse, PRReadinessBypass, PRReadinessCheck, PRReadinessEnforcement, PRReadinessPolicyConfig, PRReadinessRun, ReviewLoopFixMode, Session, SessionDetail, SessionInputCommand, SessionInputReference, SessionLog, SessionMessage, SessionReviewComment, SessionReviewLoop, SessionRetryMode, SessionStatus, SessionThread, SessionThreadFileEvent, SessionTimelineEntry, ThreadInboxEvent, ThreadRuntimeEvent, ThreadStatus, User, CodexAuthStatus, PullRequestHealthResponse, PullRequestStatus, SessionWorkspaceGenerationChangedEvent, SingleResponse, SessionTranscriptWindowResponse, SessionTranscriptTurn, SessionTranscriptEntry } from "@/lib/types";
import { AgentTabStrip, computeThreadOverlap } from "./agent-tab-strip";
import { AuditLogTrigger } from "@/components/audit/audit-log-trigger";
import { ResizeHandle } from "@/components/resize-handle";
import { DiffStatsBadge } from "@/components/code-review/diff-stats-badge";
import { LinkedIssueChips } from "./linked-issue-chips";
import { useReviewComments } from "@/hooks/use-review-comments";
import { useSessionScopedReset } from "@/hooks/use-session-scoped-reset";
import { useDiffViewState } from "@/hooks/use-diff-view-state";
import { CodexDeviceCodeModal } from "@/components/codex-device-code-modal";
import { AgentBadge } from "@/components/agent-badge";
import { FlatModelOptions } from "@/components/model-option-groups";
import { useOpenCodeAvailability } from "@/hooks/use-opencode-models";
import { PendingAttachmentStrip } from "@/components/pending-attachment-strip";
import { PRHealthBanner, prHealthAllowsMerge } from "@/components/pr-health-banner";
import { SessionKeyboardHelpOverlay } from "@/components/session-keyboard-help-overlay";
import { ErrorBoundary } from "@/components/error-boundary";
import { useAuth } from "@/hooks/use-auth";
import { useMediaQuery } from "@/hooks/use-media-query";
import { useDocumentVisible } from "@/hooks/use-document-visible";
import { usePageTitle } from "@/hooks/use-page-title";
import {
  useSessionKeyboardShortcuts,
  type SessionDetailTab,
  type UseSessionKeyboardShortcutsOptions,
} from "@/hooks/use-session-keyboard-shortcuts";
import { prMergedAccent } from "@/lib/pr-status-styles";
import { continueFromPRBranchMessage, deriveCreatePRActionState, derivePushChangesActionState, hasRepairableFailedChecks, prHealthBlocksPRActions } from "@/lib/session-pr-action-state";
import { cn, sessionTitle, formatTimeAgo } from "@/lib/utils";
import { isProvisionalSessionDetail } from "@/lib/session-detail-cache";
import { useReconcileOptimisticAction } from "./use-optimistic-pr-action";
import { pollMs } from "@/lib/poll-intervals";
import { activeSet, workingStatusesSet } from "@/lib/session-status-groups";
import { MobileSessionTopBar } from "./mobile-session-top-bar";
import { RecoverableInboxNotice } from "./recoverable-inbox-notice";
import { SessionDetailLoadingSkeleton, SessionTimelineSkeleton } from "./session-detail-loading-skeleton";
import {
  applyThreadInboxEventToThreads,
  applyThreadRuntimeEventToThreads,
  buildChromeThreads,
  capLiveSessionLogMessage,
  deriveEffectivePRStatus,
  formatDuration,
  getDisplayStatus,
  getInitialComposerSelectedModel,
  getPendingEditableThreadUpdate,
  getPullRequestHealthRefetchInterval,
  hasMeaningfulDuration,
  invalidateSessionHumanInputRequests,
  liveLogsForTimeline,
  messageReconciliationKey,
  mergePendingMessages,
  mergeSessionDetailStatusUpdate,
  mergeSessionLogListResponse,
  statusConfig,
  trackInFlightAgentUpdate,
  type PendingThreadPreview,
} from "./session-detail-state";

const loadReviewDiffView = () =>
  import("@/components/code-review/review-diff-view").then((m) => ({ default: m.ReviewDiffView }));

function sessionStatusTone(status: SessionStatus, prStatus?: PullRequestStatus | null): StatusTone {
  if (status === "failed") return "destructive";
  if (status === "awaiting_input") return "warning";
  if (status === "needs_human_guidance") return "attention";
  if (status === "pr_created" && prStatus === "closed") return "neutral";
  if (status === "completed" || status === "pr_created" || prStatus === "merged") return "success";
  if (status === "running" || status === "idle") return "primary";
  return "neutral";
}

// Defer the diff viewer until the user actually opens review mode. Saves
// review-specific code from the initial session-detail bundle for the common
// case of just chatting with the agent.
const ReviewDiffView = dynamic(
  loadReviewDiffView,
  {
    ssr: false,
    loading: () => <div className="h-full w-full bg-muted/20 animate-pulse rounded-lg" />,
  },
);

const ChangesTab = dynamic(
  () => import("./session-changes-tab").then((m) => ({ default: m.ChangesTab })),
  {
    ssr: false,
    loading: () => <div className="h-full w-full bg-muted/20 animate-pulse" />,
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

function PreviewTabErrorFallback() {
  return (
    <Card className="border-destructive/20 bg-destructive/5">
      <CardContent className="flex items-start gap-3 p-4">
        <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-destructive" />
        <div className="min-w-0 space-y-1">
          <p className="text-sm font-medium text-destructive">
            Preview panel could not be rendered
          </p>
          <p className="text-xs text-muted-foreground">
            The rest of the session is still available. Refresh the page to try the preview again.
          </p>
        </div>
      </CardContent>
    </Card>
  );
}

const FAILURE_CATEGORY_CODEX_AUTH = "codex_auth_expired";
const PR_ERROR_TOAST_DURATION_MS = 10_000;
const PR_ERROR_TOAST_MESSAGE = "PR creation failed";
const MAX_RESOLVE_REVIEW_COMMENTS_PER_MESSAGE = 50;
const SESSION_DETAIL_ACTIVE_REFETCH_INTERVAL_MS = pollMs(3000);

const EDITABLE_THREAD_AGENTS: ReadonlyArray<{ key: string; label: string }> =
  AGENTS.map((agent) => ({ key: agent.key, label: agent.label }));

type PendingFollowUpMessage = SessionMessage & {
  client_id: number;
};

function reviewLoopThreadLabel(agentType: string): string {
  switch (agentType) {
    case "claude_code":
      return "Claude Review";
    case "codex":
      return "Codex Review";
    default:
      return "Review";
  }
}

function buildReviewLoopThreadPreview(loop: SessionReviewLoop, session?: Session): PendingThreadPreview | null {
  if (!loop.thread_id) {
    return null;
  }
  return {
    id: loop.thread_id,
    session_id: loop.session_id,
    org_id: loop.org_id,
    agent_type: loop.agent_type,
    label: reviewLoopThreadLabel(loop.agent_type),
    status: "running",
    current_turn: 1,
    created_at: loop.started_at,
    cost_cents: 0,
    pending_message_count: 0,
    cancel_requested_at: undefined,
    model_override: session?.agent_type === loop.agent_type ? session.model_override : undefined,
  };
}

type SessionOriginDisplay = {
  badge: string;
  title: string;
  detail?: string;
};

function getSessionOriginDisplay(session: Session): SessionOriginDisplay | null {
  switch (session.origin) {
    case "automation":
      return {
        badge: "Automation",
        title: "Created by automation",
        detail: session.automation_run_id ? "Automation run" : "Scheduled or manually triggered automation",
      };
    case "project":
      return {
        badge: "Project",
        title: "Created from project work",
        detail: "Started as part of a tracked project task",
      };
    case "issue_trigger":
      return {
        badge: "Issue",
        title: "Created from issue intake",
        detail: "Started automatically from issue workflow",
      };
    case "revision":
      return {
        badge: "Revision",
        title: "Created from a prior session",
        detail: "Follow-up run spun out from an earlier session",
      };
    default:
      return null;
  }
}

const triggerPickerIconClassName = "h-4 w-4 shrink-0";
const directoryTriggerIcon = <FolderTree className={triggerPickerIconClassName} />;
const fileTriggerIcon = <FileCode2 className={triggerPickerIconClassName} />;

// AgentThreadTabs lived here in Phase 1. The replacement now lives in
// agent-tab-strip.tsx (AgentTabStrip) and adds overlap badges, tab actions,
// and cost surfacing. Status helpers moved with it; the statusConfig table
// is still owned by this file and passed in as a prop.

// ---------------------------------------------------------------------------
// Detail panel tabs (shown in right sidebar)
// ---------------------------------------------------------------------------

type DetailTab = SessionDetailTab;
type PRAuthorMode = "auto" | "user" | "app";

type PRAuthInterceptDetails = {
  connect_url?: string;
  resume_token?: string;
  merge_when_ready?: boolean;
  can_fallback_to_app?: boolean;
};

// PRAuthPromptState is a discriminated union so merge prompts don't carry
// create-PR-only fields. Create/push prompts may come from backend auth
// interception or be synthesized from the current GitHub status before the
// backend has rejected the action.
type PRAuthPromptState =
  | ({ purpose: "create_pr"; mergeWhenReady?: boolean } & PRAuthInterceptDetails)
  | ({ purpose: "create_branch" } & PRAuthInterceptDetails)
  | ({ purpose: "push_changes" } & PRAuthInterceptDetails)
  | { purpose: "merge_pr" };

type PRActionErrorState = {
  code?: string;
  message: string;
};

const terminalSessionStatuses = new Set<SessionStatus>(["completed", "pr_created", "failed", "cancelled", "skipped"]);

function isPRAuthInterceptDetails(value: unknown): value is PRAuthInterceptDetails {
  if (!value || typeof value !== "object") return false;
  const details = value as Partial<PRAuthInterceptDetails>;
  return typeof details.connect_url === "string" &&
    typeof details.resume_token === "string" &&
    typeof details.can_fallback_to_app === "boolean";
}

function isRuntimeRecoveryActive(session: Session): boolean {
  return session.recovery_state === "queued" || session.recovery_state === "recovering";
}

function RuntimeRecoveryNotice({ border = "border-t" }: { border?: "border-t" | "border-b" | "border" }) {
  return (
    <div className={`flex items-center gap-2 px-4 py-2.5 text-xs ${border} bg-info/10 border-info/30 text-info`}>
      <Loader2 className="h-3.5 w-3.5 shrink-0 animate-spin" />
      <span>
        <span className="font-medium">Restoring runtime from checkpoint</span>
        <span className="mx-1">·</span>
        <span>Follow-up messages will be queued and delivered after the runtime is restored.</span>
      </span>
    </div>
  );
}

function readinessIsStale(readiness: PRReadinessRun | undefined, session: Session) {
  return !!readiness && (
    readiness.evaluated_workspace_revision !== session.workspace_revision ||
    (readiness.evaluated_snapshot_key ?? "") !== (session.snapshot_key ?? "")
  );
}

function ReadinessCheckGroup({
  title,
  checks,
  empty,
  onAction,
  actionDisabled,
}: {
  title: string;
  checks: PRReadinessCheck[];
  empty: string;
  onAction?: (check: PRReadinessCheck) => void;
  actionDisabled?: (check: PRReadinessCheck) => boolean;
}) {
  return (
    <div className="space-y-1">
      <div className="font-medium text-foreground">{title}</div>
      {checks.length === 0 ? (
        <div className="text-muted-foreground">{empty}</div>
      ) : (
        <div className="space-y-1">
          {checks.map((check) => (
            <ReadinessCheckRow key={check.id || check.check_key || check.check_type} check={check} onAction={onAction} actionDisabled={actionDisabled?.(check) ?? false} />
          ))}
        </div>
      )}
    </div>
  );
}

function ReadinessCheckList({
  checks,
  onAction,
  actionDisabled,
}: {
  checks: PRReadinessCheck[];
  onAction?: (check: PRReadinessCheck) => void;
  actionDisabled?: (check: PRReadinessCheck) => boolean;
}) {
  if (checks.length === 0) return null;
  return (
    <div className="space-y-1">
      {checks.map((check) => (
        <ReadinessCheckRow key={check.id || check.check_key || check.check_type} check={check} onAction={onAction} actionDisabled={actionDisabled?.(check) ?? false} />
      ))}
    </div>
  );
}

function ReadinessCheckRow({ check, onAction, actionDisabled }: { check: PRReadinessCheck; onAction?: (check: PRReadinessCheck) => void; actionDisabled: boolean }) {
  const [open, setOpen] = useState(false);
  const evidence = formatReadinessCheckDetails(check.details);
  return (
    <div className="grid grid-cols-[16px_1fr] gap-2 text-muted-foreground">
      <ReadinessCheckStatusIcon status={check.status} className="mt-0.5 h-3.5 w-3.5" />
      <div className="min-w-0 flex-1 space-y-1">
        <div className="flex flex-wrap items-center gap-2">
          <span className="text-foreground">{check.title}</span>
          {check.provenance && check.provenance !== "builtin" ? (
            <span className="text-xs uppercase text-muted-foreground">{readinessCheckProvenanceLabel(check)}</span>
          ) : null}
          {check.action ? (
            <Button
              size="xs"
              variant="outline"
              disabled={actionDisabled}
              onClick={() => onAction?.(check)}
            >
              {check.action}
            </Button>
          ) : null}
          {evidence ? (
            <Collapsible open={open} onOpenChange={setOpen}>
              <CollapsibleTrigger asChild>
                <Button size="xs" variant="ghost" aria-label={`${open ? "Hide" : "Show"} evidence for ${check.title}`}>
                  {open ? "Hide evidence" : "Evidence"}
                </Button>
              </CollapsibleTrigger>
              <CollapsibleContent className="basis-full">
                <pre className="mt-1 max-h-40 overflow-auto rounded-md bg-muted px-2 py-1 text-xs leading-relaxed text-muted-foreground">
                  {evidence}
                </pre>
              </CollapsibleContent>
            </Collapsible>
          ) : null}
        </div>
        {check.summary ? <div className="text-xs">{check.summary}</div> : null}
      </div>
    </div>
  );
}

function ReadinessCheckStatusIcon({ status, className }: { status: PRReadinessCheck["status"]; className?: string }) {
  if (status === "passed") return <CheckCircle2 className={cn("text-success", className)} />;
  if (status === "failed" || status === "error") return <AlertTriangle className={cn("text-destructive", className)} />;
  if (status === "warning") return <AlertTriangle className={cn("text-warning", className)} />;
  return <Clock className={cn("text-muted-foreground", className)} />;
}

function formatReadinessCheckDetails(value: unknown) {
  if (!value || (typeof value === "object" && Object.keys(value as Record<string, unknown>).length === 0)) {
    return "";
  }
  if (typeof value === "string") {
    return value;
  }
  try {
    return JSON.stringify(value, null, 2);
  } catch {
    return String(value);
  }
}

function readinessCheckProvenanceLabel(check: PRReadinessCheck) {
  if (check.provenance === "repo_config" || check.source === "repo_config") {
    return ".143/config.json";
  }
  if (check.provenance === "org_settings") {
    return check.source === "repository" ? "repo settings" : "org settings";
  }
  return check.provenance;
}

function staleReadinessCheck(readiness: PRReadinessRun, session: Session, enforcementForCheck: (check: PRReadinessCheck) => PRReadinessEnforcement): PRReadinessCheck {
  const freshness = readiness.checks?.find((check) => check.check_type === "freshness");
  const base: PRReadinessCheck = freshness ?? {
    id: "__stale_readiness__",
    org_id: readiness.org_id,
    run_id: readiness.id,
    session_id: readiness.session_id,
    check_key: "__stale_readiness__",
    check_type: "freshness",
    status: "failed",
    enforcement: "blocking",
    effective_enforcement: "blocking",
    title: "Readiness is stale",
    summary: "Workspace files changed after this readiness result was produced.",
    action: "Re-run",
    created_at: readiness.updated_at,
  };
  const enforcement = enforcementForCheck(base);
  return {
    ...base,
    id: "__stale_readiness__",
    check_key: "__stale_readiness__",
    status: "failed",
    enforcement: enforcement === "off" ? "blocking" : enforcement,
    effective_enforcement: enforcement === "off" ? "blocking" : enforcement,
    title: "Readiness is stale",
    summary: "Workspace files changed after this readiness result was produced.",
    details: {
      current_workspace_revision: session.workspace_revision,
      evaluated_workspace_revision: readiness.evaluated_workspace_revision,
      current_snapshot_key: session.snapshot_key,
      evaluated_snapshot_key: readiness.evaluated_snapshot_key,
    },
    action: "Re-run",
  };
}

function isDerivedStaleReadinessCheck(check: PRReadinessCheck) {
  return check.check_key === "__stale_readiness__";
}

type ReadinessPacket = {
  what_changed?: { changed_files?: string[]; diff_stats?: unknown };
  why_changed?: { linked_issue_count?: number; issue_less_reason?: string };
  checked_at?: string;
  risk_flags?: string[];
  unknowns?: string[];
  bypasses?: PRReadinessBypass[];
};

function readinessPacket(value: unknown): ReadinessPacket | null {
  if (!value || typeof value !== "object") return null;
  return value as ReadinessPacket;
}

function enforcementForRole(check: PRReadinessCheck, role?: string | null): PRReadinessEnforcement | undefined {
  if (!check.enforcement_by_role) return undefined;
  if (role === "admin") return check.enforcement_by_role.admin;
  if (role === "member") return check.enforcement_by_role.engineer;
  if (role === "builder") return check.enforcement_by_role.builder;
  return undefined;
}

function policyRequiresRoleReadiness(config: PRReadinessPolicyConfig, role: "builder" | "engineer" | "admin") {
  if (role === "builder" && config.enabled_for_builders === false) {
    return false;
  }
  return Object.values(config.checks ?? {}).some((check) => {
    const enforcement = check.enforcement;
    if (!enforcement) return false;
    return enforcement[role] !== undefined && enforcement[role] !== "off";
  });
}

const ReadinessPacketSummary = forwardRef<HTMLDivElement, { packet: ReadinessPacket }>(function ReadinessPacketSummary({ packet }, ref) {
  const changedFiles = Array.isArray(packet.what_changed?.changed_files) ? packet.what_changed.changed_files : [];
  const riskFlags = Array.isArray(packet.risk_flags) ? packet.risk_flags : [];
  const unknowns = Array.isArray(packet.unknowns) ? packet.unknowns : [];
  const bypasses = Array.isArray(packet.bypasses) ? packet.bypasses : [];

  return (
    <div ref={ref} className="space-y-2 border-t border-border pt-3">
      <div className="font-medium text-foreground">Review packet</div>
      <div className="flex flex-wrap gap-2 text-muted-foreground">
        <Badge variant="outline">{changedFiles.length} files</Badge>
        {packet.why_changed?.linked_issue_count !== undefined ? (
          <Badge variant="outline">{packet.why_changed.linked_issue_count} linked issues</Badge>
        ) : null}
        {riskFlags.slice(0, 4).map((flag) => (
          <Badge key={flag} variant="secondary">{flag.replaceAll("_", " ")}</Badge>
        ))}
        {unknowns.slice(0, 4).map((unknown) => (
          <Badge key={unknown} variant="outline">unknown {unknown.replaceAll("_", " ")}</Badge>
        ))}
        {bypasses.length ? <Badge variant="destructive">{bypasses.length} bypass</Badge> : null}
      </div>
      {packet.why_changed?.issue_less_reason ? (
        <div className="text-muted-foreground">{packet.why_changed.issue_less_reason}</div>
      ) : null}
      {packet.checked_at ? <div className="text-muted-foreground">Checked {formatTimeAgo(packet.checked_at)}</div> : null}
    </div>
  );
});

function readinessStatusIcon(readiness: PRReadinessRun | undefined, stale: boolean | undefined, running: boolean) {
  if (running) return <Loader2 className="h-3.5 w-3.5 animate-spin text-muted-foreground" />;
  if (!readiness) return <ClipboardList className="h-3.5 w-3.5 text-muted-foreground" />;
  if (stale || readiness.status === "blocked" || readiness.status === "failed") return <AlertTriangle className="h-3.5 w-3.5 text-warning" />;
  if (readiness.status === "warnings") return <AlertTriangle className="h-3.5 w-3.5 text-warning" />;
  return <CheckCircle2 className="h-3.5 w-3.5 text-success" />;
}

function OverviewTab({ session, members, prStatus }: { session: Session; members: User[]; prStatus?: PullRequestStatus | null }) {
  const queryClient = useQueryClient();
  const [showDeviceCodeModal, setShowDeviceCodeModal] = useState(false);
  const [showStartOverRetryDialog, setShowStartOverRetryDialog] = useState(false);

  const isCodexAuthFailure = session.failure_category === FAILURE_CATEGORY_CODEX_AUTH;

  const { data: codexAuthResponse } = useQuery<SingleResponse<CodexAuthStatus>>({
    queryKey: ["codex-auth-status", "personal"],
    queryFn: () => api.codexAuth.status(undefined, "personal"),
    enabled: isCodexAuthFailure,
  });
  const isCodexAuthenticated = codexAuthResponse?.data?.status === "completed";

  const retryMutation = useMutation({
    mutationFn: (mode: SessionRetryMode) => api.sessions.retry(session.id, { mode }),
    onSuccess: () => {
      setShowStartOverRetryDialog(false);
      queryClient.invalidateQueries({ queryKey: ["session", session.id] });
    },
  });
  const recoveryActive = isRuntimeRecoveryActive(session);
  const checkpointRetryUnavailable = !session.snapshot_key || session.sandbox_state === "destroyed" || recoveryActive;

  const status = getDisplayStatus(session.status, prStatus);
  const isActive = !terminalSessionStatuses.has(session.status);
  const isDeployRecovery = session.runtime_stop_reason === "deploy_budget_expired";
  const originDisplay = getSessionOriginDisplay(session);
  const branchLabel = session.working_branch || session.target_branch;
  const repoBranchLabel = session.repository_full_name && branchLabel
    ? `${session.repository_full_name} · ${branchLabel}`
    : session.repository_full_name || branchLabel;

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
        <Card className="border-l-2 border-l-success bg-success/5">
          <CardHeader className="pb-2">
            <CardTitle className="text-xs flex items-center gap-2">
              <CheckCircle2 className="h-3.5 w-3.5 text-success" />
              Result
            </CardTitle>
          </CardHeader>
          <CardContent>
            <LazyMarkdownContent content={session.result_summary} className="text-xs" />
          </CardContent>
        </Card>
      )}

      {isDeployRecovery && (
        <Card className="border-l-2 border-l-warning bg-warning/5">
          <CardHeader className="pb-2">
            <CardTitle className="text-xs flex items-center gap-2">
              <AlertTriangle className="h-3.5 w-3.5 text-warning" />
              Resumed after deploy
            </CardTitle>
          </CardHeader>
          <CardContent>
            <p className="text-xs text-muted-foreground">
              This turn was checkpointed because its old worker reached the deploy runtime ceiling, then requeued onto a replacement worker.
            </p>
          </CardContent>
        </Card>
      )}

      {/* Failure card — shown prominently at top for failed sessions */}
      {recoveryActive && <RuntimeRecoveryNotice border="border" />}
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
                <ButtonGroup size="xs">
                  <DisabledTooltip
                    disabled={retryMutation.isPending || checkpointRetryUnavailable}
                    content={recoveryActive ? "Runtime recovery is already in progress." : checkpointRetryUnavailable ? "No saved progress is available." : "Retrying session..."}
                  >
                    <Button
                      size="xs"
                      variant="outline"
                      className="rounded-r-none border-r-0"
                      onClick={() => retryMutation.mutate("checkpoint")}
                      disabled={retryMutation.isPending || checkpointRetryUnavailable}
                    >
                      <RefreshCw className={`mr-1.5 h-3 w-3 ${retryMutation.isPending ? "animate-spin" : ""}`} />
                      {retryMutation.isPending ? "Retrying..." : "Retry"}
                    </Button>
                  </DisabledTooltip>
                  <DropdownMenu>
                    <DropdownMenuTrigger asChild>
                      <Button
                        size="xs"
                        variant="outline"
                        className="rounded-l-none px-2"
                        aria-label="More retry actions"
                        disabled={retryMutation.isPending}
                      >
                        <ChevronDown className="h-3 w-3" />
                      </Button>
                    </DropdownMenuTrigger>
                    <DropdownMenuContent align="end">
                      <DropdownMenuItem onClick={() => setShowStartOverRetryDialog(true)}>
                        Start over from beginning
                      </DropdownMenuItem>
                    </DropdownMenuContent>
                  </DropdownMenu>
                </ButtonGroup>
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
              <p className="text-xs text-success flex items-center gap-1.5">
                <CheckCircle2 className="h-3.5 w-3.5" />
                {checkpointRetryUnavailable
                  ? "ChatGPT connected — open the retry menu and choose Start over from beginning."
                  : "ChatGPT connected — click Retry to continue this session."}
              </p>
            )}
          </CardContent>
          <AlertDialog open={showStartOverRetryDialog} onOpenChange={setShowStartOverRetryDialog}>
            <AlertDialogContent>
              <AlertDialogHeader>
                <AlertDialogTitle>Start over from beginning?</AlertDialogTitle>
                <AlertDialogDescription>
                  This clears the current visible retry result and starts the session again from its original base.
                </AlertDialogDescription>
              </AlertDialogHeader>
              <AlertDialogFooter>
                <AlertDialogCancel disabled={retryMutation.isPending}>Cancel</AlertDialogCancel>
                <AlertDialogAction
                  onClick={() => retryMutation.mutate("start_over")}
                  disabled={retryMutation.isPending}
                >
                  Start over
                </AlertDialogAction>
              </AlertDialogFooter>
            </AlertDialogContent>
          </AlertDialog>
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
                <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-info/60 opacity-75" />
                <span className="relative inline-flex rounded-full h-2 w-2 bg-info" />
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

        {originDisplay && (
          <div className="flex items-center gap-x-2 gap-y-1 flex-wrap text-xs text-muted-foreground">
            <Badge variant="outline" className="h-5 rounded-full px-2 text-xs font-medium">
              {originDisplay.badge}
            </Badge>
            <span className="font-medium text-foreground">{originDisplay.title}</span>
            {originDisplay.detail && (
              <>
                <span aria-hidden="true" className="text-muted-foreground/50">·</span>
                <span>{originDisplay.detail}</span>
              </>
            )}
          </div>
        )}

        {repoBranchLabel && (
          <div
            data-testid="session-overview-repo-branch"
            className="text-xs text-muted-foreground break-words"
          >
            {repoBranchLabel}
          </div>
        )}

        {/* Timestamps + audit — secondary reference data, kept separate from long repo/branch labels */}
        <div
          data-testid="session-overview-timing"
          className="flex items-center gap-x-1.5 gap-y-1 flex-wrap text-xs text-muted-foreground"
        >
          {terminalSessionStatuses.has(session.status) &&
            !((session.status === "failed" || session.status === "cancelled") &&
              !hasMeaningfulDuration(session.started_at, session.completed_at)) && (
            <span>
              {formatDuration(session.started_at, session.completed_at)}
              <span aria-hidden="true" className="ml-1.5 text-muted-foreground/50">·</span>
            </span>
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

// ---------------------------------------------------------------------------
// Shared session composer (used in both chat and review mode)
// ---------------------------------------------------------------------------

function SessionComposer({
  sessionId,
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
  unavailableReason,
  placeholderOverride,
}: {
  sessionId: string;
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
  unavailableReason?: string;
  placeholderOverride?: string;
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
    if (isMobile || !canSendMessage) {
      return;
    }
    textareaRef.current?.focus();
  }, [isMobile, canSendMessage, textareaRef]);

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

  // Resolve the OpenCode transport ("· OpenRouter") for the selected model so
  // the in-session picker shows the route that runs given current keys. Shares
  // the resolved coding-credential cache with the rest of the page.
  const { data: composerResolvedCredsResponse } = useQuery<ListResponse<CodingCredentialSummary>>({
    queryKey: queryKeys.codingCredentials.list("resolved"),
    queryFn: () => api.codingCredentials.list("resolved"),
  });
  const openCodeAvailability = useOpenCodeAvailability(composerResolvedCredsResponse?.data ?? []);

  const fileMentionsQuery = useQuery<ListResponse<SessionInputReference>>({
    queryKey: queryKeys.sessions.composerFiles(sessionId, deferredMentionQuery),
    queryFn: () => api.sessions.composerFiles(sessionId, deferredMentionQuery),
    enabled: showMentionPicker,
    staleTime: 30 * 1000,
    // Keep the previous query's results rendered while the next keystroke's
    // request is in flight so the picker doesn't blank out between queries.
    placeholderData: (previous) => previous,
  });
  const fileMentions = useMemo(() => fileMentionsQuery.data?.data ?? [], [fileMentionsQuery.data]);

  const mentionWarmQueryClient = useQueryClient();
  useEffect(() => {
    // Warm the backend's mention index as soon as the composer mounts: the
    // empty-q request returns [] immediately but kicks off the workspace
    // walk server-side, so the index is (usually) hot by the time the user
    // opens the @-picker with a real query.
    if (!repositoryId) return;
    void mentionWarmQueryClient.prefetchQuery({
      queryKey: queryKeys.sessions.composerFiles(sessionId, ""),
      queryFn: () => api.sessions.composerFiles(sessionId, ""),
      staleTime: 30 * 1000,
    });
  }, [mentionWarmQueryClient, sessionId, repositoryId]);

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
  const defaultUnavailablePlaceholder = isSnapshotExpired
    ? "Session environment has expired and can no longer be continued"
    : "Session is not active";
  const unavailableMessage = unavailableReason ?? (
    isSnapshotExpired
      ? "Session environment has expired and can no longer be continued."
      : "Session is not active."
  );
  const attachDisabledReason = isUploading
    ? "Uploading attachments..."
    : !canSendMessage
      ? unavailableMessage
      : undefined;
  const cancelDisabledReason = cancelPending ? "Stopping agent..." : undefined;
  const sendDisabledReason = hasInvalidCommands
    ? `${invalidCommandTokens.join(", ")} ${invalidCommandTokens.length === 1 ? "is" : "are"} not valid for this agent. Remove the chip${invalidCommandTokens.length === 1 ? "" : "s"} to continue.`
    : !hasContent
      ? "Add a message, attachment, or review comment before sending."
      : !canSendMessage
        ? unavailableMessage
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
    if (e.key === "Escape" && message.trim().length === 0) {
      e.preventDefault();
      e.currentTarget.blur();
      return;
    }
    if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
      e.preventDefault();
      if (!sendDisabled) {
        onSend();
      }
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

  const fileDropzone = useFileDropzone({
    enabled: canSendMessage,
    onFilesDropped: onPasteFiles,
    onAfterDrop: () => {
      requestAnimationFrame(() => {
        const el = textareaRef.current;
        if (!el) return;
        el.focus();
      });
    },
  });

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
              <FlatModelOptions
                models={availableModels}
                agentType={agentType}
                openCodeAvailability={openCodeAvailability}
                selectedModel={selectedModel}
              />
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
        className="shrink-0 border-t border-border bg-background/95 p-3"
        ref={composerCardRef}
        data-testid="session-composer-shell"
      >
        {planMode && (
          <div className="flex items-center gap-2 mb-2 px-1">
            <div className="flex items-center gap-1.5 rounded-full border border-warning/20 bg-warning/8 px-2.5 py-1">
              <ClipboardList className="h-3 w-3 text-warning" />
              <span className="text-xs font-medium text-warning">Plan mode</span>
              <Button
                type="button"
                variant="ghost"
                size="icon-xs"
                onClick={() => onPlanModeChange(false)}
                className="ml-0.5 h-4 w-4 rounded-full text-warning/65 hover:bg-warning/10 hover:text-warning"
                title="Exit plan mode"
              >
                &times;
              </Button>
            </div>
            <span className="text-xs text-muted-foreground">Agent will create a plan for review before making changes</span>
          </div>
        )}

        <div
          ref={composerInputSurfaceRef}
          data-testid="session-composer-input-surface"
          {...fileDropzone.dropzoneProps}
          className={cn(
            "rounded-xl border bg-surface-raised shadow-[0_10px_30px_rgb(36_34_28_/_8%)] transition-[border-color,box-shadow] focus-within:border-ring focus-within:ring-2 focus-within:ring-ring/15",
            planMode ? "border-warning/25" : "border-border-strong",
            fileDropzone.isDragActive && "border-primary/40 bg-primary/5 ring-1 ring-primary/30",
          )}
        >
          {openComments.length > 0 && (
            <div className="flex min-w-0 flex-wrap gap-1.5 px-3 pt-2.5 pb-1">
              {openComments.map((c) => {
                const fileName = c.file_path.split("/").pop() ?? c.file_path;
                return (
                  <div
                    key={c.id}
                    className="inline-flex max-w-full min-w-0 items-center gap-1.5 rounded-md border border-border bg-background px-2 py-1 text-xs"
                  >
                    <MessageSquare className="h-3 w-3 text-muted-foreground shrink-0" />
                    <span className="min-w-0 truncate font-mono text-muted-foreground">
                      {fileName}:{c.line_number}
                    </span>
                    <span className="text-muted-foreground/40">-</span>
                    <span className="min-w-0 max-w-[200px] truncate">
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
              placeholderOverride ?? (
                !canSendMessage
                  ? unavailableReason ?? defaultUnavailablePlaceholder
                  : planMode
                    ? "Describe what you want to plan..."
                    : targetLabel
                      ? `Send a message to ${targetLabel}...`
                      : "Send a follow-up message..."
              )
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
                    size="icon-compact"
                    className="rounded-full"
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
                      isInvalid && "border-warning/60 bg-warning/10 text-warning",
                    )}
                    data-invalid={isInvalid || undefined}
                    title={isInvalid ? `${command.token} is a ${command.agent_type} command. Switch agent or remove it.` : undefined}
                  >
                    <span className="max-w-[14rem] truncate">{command.token}</span>
                    <Button
                      type="button"
                      variant="ghost"
                      size="icon-compact"
                      className="rounded-full"
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
            <p className="px-3 pb-2 text-xs text-warning" role="alert">
              {invalidCommandTokens.join(", ")} {invalidCommandTokens.length === 1 ? "is" : "are"} not valid for this agent. Remove the chip{invalidCommandTokens.length === 1 ? "" : "s"} to continue.
            </p>
          )}

          <PendingAttachmentStrip
            attachments={attachments}
            isUploading={isUploading}
            onRemove={onRemoveAttachment}
            size="md"
            className="px-3 pt-2 pb-2"
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

                {availableModels.length > 0 && (
                  <Select value={selectedModel} onValueChange={onSelectedModelChange}>
                    <SelectTrigger className="h-8 w-auto gap-1.5 border-none bg-transparent px-2 text-xs text-muted-foreground shadow-none hover:text-foreground focus:ring-0" aria-label="Model override">
                      <SelectValue placeholder="Default model" />
                    </SelectTrigger>
                    <SelectContent>
                      <FlatModelOptions
                        models={availableModels}
                        agentType={agentType}
                        openCodeAvailability={openCodeAvailability}
                        selectedModel={selectedModel}
                      />
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
              accept="image/png,image/jpeg,image/gif,image/webp,.heic,.heif,.pdf,.txt,.md,.json,.csv"
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
const BASE_SSE_RECONNECT_DELAY_MS = pollMs(1000);
const MAX_FILE_SIZE = 10 * 1024 * 1024; // 10 MB
const SCROLL_NEAR_BOTTOM_THRESHOLD = 100;
const SCROLL_POSITION_SAVE_DEBOUNCE_MS = 150;
// Sliding window for live SSE logs that may not be visible in persisted log
// queries yet. The buffer lives in React Query so remounting the chat panel
// cannot drop the transcript during the SSE-to-DB handoff.
const STREAMED_LOGS_MAX = 2000;

function isNearBottom(el: HTMLElement): boolean {
  return el.scrollHeight - el.scrollTop - el.clientHeight < SCROLL_NEAR_BOTTOM_THRESHOLD;
}

function sessionLiveLogsQueryKey(sessionId: string): readonly unknown[] {
  return ["session", sessionId, "logs", "live"];
}

function threadLiveLogsQueryKey(sessionId: string, threadId: string): readonly unknown[] {
  return [...queryKeys.sessions.threadLogs(sessionId, threadId), "live"];
}

// Page parameter for the transcript window infinite query. The first page is
// position="latest" (or "around" when restoring a saved anchor); subsequent
// pages page backwards via `before` cursors. Newer turns are loaded separately
// through newerThreadMessagePages, mirroring the legacy message-window flow.
type TranscriptWindowPageParam =
  | { position: "latest" }
  | { position: "around"; anchorEntryId: string }
  | { position: "around"; anchorMessageId: number }
  | { before: string };

type TranscriptWindowInfiniteData = {
  pages: SessionTranscriptWindowResponse[];
  pageParams: unknown[];
};

function transcriptAnchorKey(anchor?: SessionAnchorPosition | null): string | null {
  if (!anchor) return null;
  return anchor.anchor.kind === "entry"
    ? `entry:${anchor.anchor.id}`
    : `message:${anchor.anchor.id}`;
}

function transcriptWindowQueryKey(sessionId: string, threadId: string, anchorKey?: string | null): readonly unknown[] {
  return queryKeys.sessions.threadTranscript(sessionId, threadId, anchorKey);
}

function transcriptInitialPageParam(anchor?: SessionAnchorPosition | null): TranscriptWindowPageParam {
  if (!anchor) return { position: "latest" };
  return anchor.anchor.kind === "entry"
    ? { position: "around", anchorEntryId: anchor.anchor.id }
    : { position: "around", anchorMessageId: anchor.anchor.id };
}

function transcriptEntryIDSelector(entryID: string): string {
  const escaped = typeof CSS !== "undefined" && typeof CSS.escape === "function"
    ? CSS.escape(entryID)
    : entryID.replace(/["\\]/g, "\\$&");
  return `[data-session-entry-id="${escaped}"]`;
}

// flattenTranscriptPages returns the turns of the infinite query (newest page
// first → older pages) followed by manually-loaded newer pages, all in a single
// flat list. Order does not matter for rendering: buildTimeline +
// sortTimelineEntries re-sort everything by created_at.
function flattenTranscriptPages(
  pages: SessionTranscriptWindowResponse[] | undefined,
  newerPages: SessionTranscriptWindowResponse[],
): SessionTranscriptTurn[] {
  const turns: SessionTranscriptTurn[] = [];
  for (const page of pages ?? []) turns.push(...page.data);
  for (const page of newerPages) turns.push(...page.data);
  return turns;
}

// appendMessageToTranscriptCache injects a freshly-sent message into the first
// (newest) cached transcript page so the optimistic message can be dropped from
// local state without a flicker before the next /transcript refetch lands.
function appendMessageToTranscriptCache(
  previous: TranscriptWindowInfiniteData | undefined,
  message: SessionMessage,
  fallbackStatus: ThreadStatus,
): TranscriptWindowInfiniteData {
  const entry: SessionTranscriptEntry = {
    id: `msg_${message.id}`,
    kind: "message",
    created_at: message.created_at,
    message_id: message.id,
    role: message.role,
    content: message.content,
    message,
  };
  const pages = previous?.pages ?? [];
  if (pages.length === 0) {
    return {
      pages: [
        {
          data: [{ turn_number: message.turn_number, started_at: message.created_at, entries: [entry] }],
          meta: { position: "latest", has_older: false, has_newer: false, thread_status: fallbackStatus, live_edge_message_id: message.id },
        },
      ],
      pageParams: [{ position: "latest" }],
    };
  }
  const firstPage = pages[0];
  const turns = [...firstPage.data];
  const turnIndex = turns.findIndex((turn) => turn.turn_number === message.turn_number);
  if (turnIndex >= 0) {
    const entries = turns[turnIndex].entries.filter((existing) => existing.message_id !== message.id);
    turns[turnIndex] = { ...turns[turnIndex], entries: [...entries, entry] };
  } else {
    turns.push({ turn_number: message.turn_number, started_at: message.created_at, entries: [entry] });
  }
  return {
    pages: [
      {
        ...firstPage,
        data: turns,
        meta: { ...firstPage.meta, live_edge_message_id: message.id },
      },
      ...pages.slice(1),
    ],
    pageParams: previous?.pageParams ?? [{ position: "latest" }],
  };
}

interface DefaultEntryAnchorOptions {
  activeThreadId: string | null | undefined;
  sessionId: string;
  /** True only when there is no stored anchor, an active thread, and the session is not running. */
  isEligible: boolean;
  firstThreadWindow: SessionTranscriptWindowResponse | undefined;
  timelineEntries: TimelineEntry[];
  scrollRef: React.RefObject<HTMLDivElement | null>;
  syncScrollState: (el: HTMLDivElement) => void;
  activeThreadTranscriptQueryKey: readonly unknown[] | null;
  queryClient: ReturnType<typeof useQueryClient>;
  scrollToLiveEdgePosition: () => void;
  initialAnchorAppliedRef: React.MutableRefObject<boolean>;
  initialAnchorCancelledRef: React.MutableRefObject<boolean>;
}

/**
 * Handles scrolling to the latest assistant transcript entry when reopening an
 * inactive thread that has no stored scroll anchor. Owns the synchronous
 * layout-effect fast path (when the target element is already in the DOM) and
 * the three progressive fallbacks: a requestAnimationFrame retry, a remote
 * "around" fetch, and a live-edge fallback on error.
 *
 * Returns `tryApply(el)` to be called from the parent's initial-anchor effect;
 * it returns `true` when it has handled (or scheduled) the scroll so the
 * caller can return early.
 */
function useDefaultEntryAnchor({
  activeThreadId,
  sessionId,
  isEligible,
  firstThreadWindow,
  timelineEntries,
  scrollRef,
  syncScrollState,
  activeThreadTranscriptQueryKey,
  queryClient,
  scrollToLiveEdgePosition,
  initialAnchorAppliedRef,
  initialAnchorCancelledRef,
}: DefaultEntryAnchorOptions): (el: HTMLDivElement) => boolean {
  const fetchRef = useRef<string | null>(null);
  const appliedRef = useRef(false);

  const latestAssistantEntryID = firstThreadWindow?.meta.latest_assistant_entry_id;

  useEffect(() => {
    fetchRef.current = null;
    appliedRef.current = false;
  }, [activeThreadId, sessionId]);

  // Synchronous fast path: scroll as soon as the target node is in the DOM.
  useLayoutEffect(() => {
    if (!isEligible || appliedRef.current || !latestAssistantEntryID) return;
    const el = scrollRef.current;
    if (!el) return;
    const target = el.querySelector<HTMLElement>(transcriptEntryIDSelector(latestAssistantEntryID));
    if (!target) return;
    el.scrollTop = target.offsetTop;
    syncScrollState(el);
    initialAnchorAppliedRef.current = true;
    appliedRef.current = true;
  }, [initialAnchorAppliedRef, isEligible, latestAssistantEntryID, scrollRef, syncScrollState, timelineEntries]);

  return useCallback((el: HTMLDivElement): boolean => {
    if (!isEligible || !latestAssistantEntryID) return false;

    // DOM target is already present.
    const target = el.querySelector<HTMLElement>(transcriptEntryIDSelector(latestAssistantEntryID));
    if (target) {
      el.scrollTop = target.offsetTop;
      syncScrollState(el);
      initialAnchorAppliedRef.current = true;
      return true;
    }

    // Target is in the entry list but the DOM hasn't updated yet — retry after paint.
    if (timelineEntries.some((entry) => entry.transcriptEntryId === latestAssistantEntryID)) {
      requestAnimationFrame(() => {
        if (initialAnchorAppliedRef.current || initialAnchorCancelledRef.current) return;
        const currentEl = scrollRef.current;
        if (!currentEl) return;
        const delayedTarget = currentEl.querySelector<HTMLElement>(transcriptEntryIDSelector(latestAssistantEntryID));
        if (!delayedTarget) return;
        currentEl.scrollTop = delayedTarget.offsetTop;
        syncScrollState(currentEl);
        initialAnchorAppliedRef.current = true;
        appliedRef.current = true;
      });
      return true;
    }

    // Entry isn't in the current page — fetch an "around" window.
    const fetchKey = `${activeThreadId}:${latestAssistantEntryID}`;
    if (activeThreadTranscriptQueryKey && fetchRef.current !== fetchKey) {
      fetchRef.current = fetchKey;
      void api.sessions.getThreadTranscriptWindow(sessionId, activeThreadId!, {
        position: "around",
        anchorEntryId: latestAssistantEntryID,
      }).then((window) => {
        queryClient.setQueryData<TranscriptWindowInfiniteData>(
          activeThreadTranscriptQueryKey,
          { pages: [window], pageParams: [{ position: "around", anchorEntryId: latestAssistantEntryID }] },
        );
      }).catch((error) => {
        toast.error(error instanceof ApiError ? error.message : "Failed to load latest assistant message");
        scrollToLiveEdgePosition();
        initialAnchorAppliedRef.current = true;
      });
      return true;
    }

    return false;
  }, [
    activeThreadId,
    activeThreadTranscriptQueryKey,
    initialAnchorAppliedRef,
    initialAnchorCancelledRef,
    isEligible,
    latestAssistantEntryID,
    queryClient,
    scrollRef,
    scrollToLiveEdgePosition,
    sessionId,
    syncScrollState,
    timelineEntries,
  ]);
}

type ChatPanelProps = {
  session: Session;
  sessionId: string;
  activeThread?: SessionThread;
  isActive: boolean;
  isStopRequested: boolean;
  stopOutcome: "checkpointed" | null;
  viewerScope: SessionScrollViewerScope | null;
  optimisticMessages: SessionMessage[];
  onDiffClick?: () => void;
  onApprovePlan?: () => void;
  onAdjustPlan?: () => void;
  onRegisterScrollToLiveEdge?: (scrollToLiveEdge: (() => void) | null) => void;
  onRegisterKeyboardControls?: (controls: SessionTranscriptKeyboardControls | null) => void;
};

type SessionTranscriptKeyboardControls = NonNullable<UseSessionKeyboardShortcutsOptions["transcript"]>;

function ChatPanel({
  session,
  sessionId,
  activeThread,
  isActive,
  isStopRequested,
  stopOutcome,
  viewerScope,
  optimisticMessages,
  onDiffClick,
  onApprovePlan,
  onAdjustPlan,
  onRegisterScrollToLiveEdge,
  onRegisterKeyboardControls,
}: ChatPanelProps) {
  const queryClient = useQueryClient();
  const [dismissedHumanInputIds, setDismissedHumanInputIds] = useState<Set<string>>(() => new Set());
  const [newerThreadMessagePages, setNewerThreadMessagePages] = useState<SessionTranscriptWindowResponse[]>([]);
  const [isFetchingNewerThreadMessages, setIsFetchingNewerThreadMessages] = useState(false);
  const scrollRef = useRef<HTMLDivElement>(null);
  const isNearBottomRef = useRef(false);
  const initialAnchorAppliedRef = useRef(false);
  const initialAnchorCancelledRef = useRef(false);
  const olderMessagesPrependSnapshotRef = useRef<{ scrollHeight: number; scrollTop: number } | null>(null);
  const saveScrollTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const reconnectAttempts = useRef(0);
  const reconnectTimer = useRef<ReturnType<typeof setTimeout>>(null);
  const apiBase = process.env.NEXT_PUBLIC_API_URL || "";
  const [showJumpToLatest, setShowJumpToLatest] = useState(false);
  const isDocumentVisible = useDocumentVisible();

  const activeThreadId = activeThread?.id;
  const isRunning = activeThread ? activeThread.status === "running" : session.status === "running";
  // A worker drain / deploy interruption leaves the thread "running" while the
  // runtime waits to resume. Surface that as "Resuming after maintenance…" in the
  // timeline spinner instead of an honest-looking "Agent is working…".
  const recoveryActive = isRuntimeRecoveryActive(session);
  const isPending = activeThread ? activeThread.status === "pending" : session.status === "pending";
  const isSnapshotExpired = session.sandbox_state === "destroyed";
  const canSendMessage = session.status !== "skipped" && session.status !== "pending" && !isSnapshotExpired;
  // `pending` covers both the normal environment-setup window and a session
  // that is queued because the org is at its concurrency limit. The two look
  // identical on the session row, so consult the runtime capacity signal to
  // decide which message to show instead of assuming every pending session is
  // capacity-blocked. Gate on the agent-run dimension specifically rather than
  // the conflated `state`, which also flips to "limited" when only the
  // unrelated preview limit is reached.
  const runtimeStatusQuery = useQuery({
    queryKey: queryKeys.settings.runtimeStatus,
    queryFn: () => api.settings.getRuntimeStatus(),
    enabled: isPending,
    staleTime: 15_000,
    refetchInterval: 15_000,
  });
  const capacity = runtimeStatusQuery.data?.data.capacity;
  const isCapacityLimited = capacity != null && capacity.active_agent_runs >= capacity.max_concurrent_agent_runs;
  const maxConcurrentRuns = capacity?.max_concurrent_agent_runs;
  const initialThreadAnchorPosition = useMemo<SessionAnchorPosition | null>(() => {
    if (!activeThreadId || !viewerScope || typeof window === "undefined") return null;
    return readStoredSessionAnchorPosition(window.localStorage, sessionId, viewerScope, activeThreadId);
  }, [activeThreadId, sessionId, viewerScope]);
  const initialThreadAnchorKey = useMemo(
    () => transcriptAnchorKey(initialThreadAnchorPosition),
    [initialThreadAnchorPosition],
  );
  const activeThreadTranscriptQueryKey = useMemo(
    () => activeThreadId ? transcriptWindowQueryKey(sessionId, activeThreadId, initialThreadAnchorKey) : null,
    [activeThreadId, initialThreadAnchorKey, sessionId],
  );

  const timelineQuery = useQuery({
    queryKey: ["session", sessionId, "timeline"],
    queryFn: () => api.sessions.getTimeline(sessionId),
    enabled: !activeThreadId,
    refetchInterval: isActive && !activeThreadId ? SESSION_DETAIL_ACTIVE_REFETCH_INTERVAL_MS : false,
  });

  // Single transcript-window query feeding the legacy ChatTimeline. It replaces
  // the previous two-query (messages + per-turn logs) coupling: the backend
  // returns turns whose entries embed the full message/log/human-input records,
  // which we flatten back into the {messages, logs} buildTimeline() expects.
  // getNextPageParam pages backwards (older); newer turns load via
  // newerThreadMessagePages, exactly as the message-window flow did.
  const threadTranscriptQuery = useInfiniteQuery<
    SessionTranscriptWindowResponse,
    Error,
    { pages: SessionTranscriptWindowResponse[]; pageParams: unknown[] },
    readonly unknown[],
    TranscriptWindowPageParam
  >({
    queryKey: activeThreadTranscriptQueryKey ?? ["session", sessionId, "thread", "none", "transcript"],
    queryFn: ({ pageParam }) =>
      api.sessions.getThreadTranscriptWindow(sessionId, activeThreadId!, pageParam),
    initialPageParam: transcriptInitialPageParam(initialThreadAnchorPosition),
    getNextPageParam: (lastPage) =>
      lastPage.meta.has_older && lastPage.meta.next_older_cursor
        ? { before: lastPage.meta.next_older_cursor }
        : undefined,
    enabled: !!activeThreadId && !!viewerScope,
    refetchInterval: activeThread && workingStatusesSet.has(activeThread.status) ? SESSION_DETAIL_ACTIVE_REFETCH_INTERVAL_MS : false,
  });

  const threadTranscriptTurns = useMemo(
    () => flattenTranscriptPages(threadTranscriptQuery.data?.pages, newerThreadMessagePages),
    [newerThreadMessagePages, threadTranscriptQuery.data?.pages],
  );
  const {
    messages: threadMessages,
    logs: threadTranscriptLogs,
    messageEntryIds: threadMessageEntryIds,
    logEntryIds: threadLogEntryIds,
    humanInputEntryIds: threadHumanInputEntryIds,
  } = useMemo(
    () => flattenTranscriptWindows(threadTranscriptTurns),
    [threadTranscriptTurns],
  );
  const newestThreadWindow = newerThreadMessagePages.at(-1) ?? threadTranscriptQuery.data?.pages[0];
  const hasNewerThreadMessages = !!activeThreadId && !!newestThreadWindow?.meta.has_newer;
  const nextNewerThreadCursor = newestThreadWindow?.meta.next_newer_cursor;
  const liveLogsQueryKey = useMemo(
    () => activeThreadId
      ? threadLiveLogsQueryKey(sessionId, activeThreadId)
      : sessionLiveLogsQueryKey(sessionId),
    [activeThreadId, sessionId],
  );

  const liveLogsQuery = useQuery({
    queryKey: liveLogsQueryKey,
    queryFn: () => ({ data: [], meta: {} }) satisfies ListResponse<SessionLog>,
    enabled: false,
    initialData: { data: [], meta: {} } satisfies ListResponse<SessionLog>,
  });
  const clearCurrentLiveLogs = useCallback(() => {
    queryClient.setQueryData<ListResponse<SessionLog>>(
      liveLogsQueryKey,
      { data: [], meta: {} } satisfies ListResponse<SessionLog>,
    );
  }, [liveLogsQueryKey, queryClient]);

  const humanInputStatusFilter = activeThreadId ? undefined : "pending";
  const humanInputQuery = useQuery({
    queryKey: queryKeys.sessions.humanInputRequests(sessionId, humanInputStatusFilter ?? null, activeThreadId ?? null),
    queryFn: () => api.sessions.getHumanInputRequests(sessionId, { status: humanInputStatusFilter, threadId: activeThreadId ?? null }),
    refetchInterval: isActive && (session.status === "awaiting_input" || activeThread?.status === "awaiting_input") ? SESSION_DETAIL_ACTIVE_REFETCH_INTERVAL_MS : false,
  });
  const pendingHumanInputs = useMemo(() => {
    const requests = humanInputQuery.data?.data ?? [];
    return requests.filter((request) => {
      if (request.status !== "pending") return false;
      if (activeThreadId) return request.thread_id === activeThreadId;
      return !request.thread_id;
    });
  }, [activeThreadId, humanInputQuery.data?.data]);
  const canAnswerHumanInput = session.status === "awaiting_input" || activeThread?.status === "awaiting_input";
  const autoOpenHumanInputId = canAnswerHumanInput
    ? pendingHumanInputs.find((request) => !dismissedHumanInputIds.has(request.id))?.id ?? null
    : null;

  const invalidateActiveThreadTranscript = useCallback(() => {
    if (!activeThreadId) return;
    queryClient.invalidateQueries({ queryKey: queryKeys.sessions.threadTranscript(sessionId, activeThreadId) });
  }, [activeThreadId, queryClient, sessionId]);

  const invalidateHumanInput = useCallback(() => {
    invalidateSessionHumanInputRequests(queryClient, sessionId);
    queryClient.invalidateQueries({ queryKey: ["session", sessionId] });
    queryClient.invalidateQueries({ queryKey: ["session", sessionId, "timeline"] });
    if (activeThreadId) {
      queryClient.invalidateQueries({ queryKey: queryKeys.sessions.threadTranscript(sessionId, activeThreadId) });
    }
  }, [activeThreadId, queryClient, sessionId]);

  const answerHumanInputMutation = useMutation({
    mutationFn: ({ request, body }: { request: HumanInputRequest; body: HumanInputAnswerBody }) =>
      api.sessions.answerHumanInputRequest(sessionId, request.id, body),
    onSuccess: () => {
      invalidateHumanInput();
    },
    onError: (error) => {
      toast.error(error instanceof ApiError ? error.message : "Failed to answer request");
    },
  });

  const cancelHumanInputMutation = useMutation({
    mutationFn: (request: HumanInputRequest) => api.sessions.cancelHumanInputRequest(sessionId, request.id),
    onSuccess: () => {
      invalidateHumanInput();
    },
    onError: (error) => {
      toast.error(error instanceof ApiError ? error.message : "Failed to cancel request");
    },
  });

  const handleAnswerHumanInput = useCallback(async (request: HumanInputRequest, body: HumanInputAnswerBody) => {
    await answerHumanInputMutation.mutateAsync({ request, body });
    setDismissedHumanInputIds((current) => new Set(current).add(request.id));
  }, [answerHumanInputMutation]);

  const handleCancelHumanInput = useCallback(async (request: HumanInputRequest) => {
    await cancelHumanInputMutation.mutateAsync(request);
    setDismissedHumanInputIds((current) => new Set(current).add(request.id));
  }, [cancelHumanInputMutation]);

  const handleDismissHumanInputAutoOpen = useCallback((request: HumanInputRequest) => {
    setDismissedHumanInputIds((current) => {
      if (current.has(request.id)) return current;
      return new Set(current).add(request.id);
    });
  }, []);

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
    const includeLiveLogs = optimisticForCurrentView.length > 0 ||
      (activeThread ? workingStatusesSet.has(activeThread.status) : workingStatusesSet.has(session.status));
    const visibleLiveLogs = liveLogsForTimeline(
      includeLiveLogs,
      liveLogsQuery.data?.data ?? [],
    );
    if (activeThreadId) {
      // Logs already arrive scoped to the loaded turns inside the transcript
      // window; merge the live SSE buffer on top, de-duplicating by id.
      const loadedThreadLogs = mergeSessionLogListResponse(
        { data: threadTranscriptLogs, meta: {} },
        visibleLiveLogs,
      ).data;
      const threadHumanInputEntries: TimelineEntry[] = (humanInputQuery.data?.data ?? [])
        .filter((request) => request.thread_id === activeThreadId)
        .map((request) => ({
          kind: "human_input" as const,
          data: request,
          transcriptEntryId: threadHumanInputEntryIds.get(request.id),
        }));
      return sortTimelineEntries([
        ...buildTimeline(
          mergePendingMessages(threadMessages, optimisticForCurrentView),
          loadedThreadLogs,
          { messageEntryIds: threadMessageEntryIds, logEntryIds: threadLogEntryIds },
        ),
        ...threadHumanInputEntries,
      ]);
    }
    const flattenedTimeline = flattenTimelineResponse(timelineQuery.data?.data ?? []);
    const sessionLogs = mergeSessionLogListResponse(
      { data: flattenedTimeline.logs, meta: {} },
      visibleLiveLogs,
    ).data;
    const entries = sortTimelineEntries([...buildTimeline(
      mergePendingMessages(flattenedTimeline.messages, optimisticForCurrentView),
      sessionLogs,
    ), ...flattenedTimeline.humanInputs.map((request) => ({ kind: "human_input" as const, data: request }))]);
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
  }, [activeThread, activeThreadId, optimisticMessages, threadMessages, threadTranscriptLogs, threadMessageEntryIds, threadLogEntryIds, threadHumanInputEntryIds, liveLogsQuery.data?.data, timelineQuery.data?.data, issueQuery.data?.data?.description, sessionId, session.org_id, session.created_at, session.status, humanInputQuery.data?.data]);

  const baseTimelineHumanInputIds = useMemo(() => {
    const humanInputIds = new Set<string>();

    for (const entry of baseTimelineEntries) {
      if (entry.kind === "human_input") {
        humanInputIds.add(entry.data.id);
      }
    }

    return humanInputIds;
  }, [baseTimelineEntries]);

  const timelineEntries = useMemo(() => {
    const humanInputEntries: TimelineEntry[] = pendingHumanInputs
      .filter((request) => !baseTimelineHumanInputIds.has(request.id))
      .map((request) => ({ kind: "human_input", data: request }));

    return sortTimelineEntries([...baseTimelineEntries, ...humanInputEntries]);
  }, [baseTimelineEntries, baseTimelineHumanInputIds, pendingHumanInputs]);
  const hasLoadedTimelineInputs = activeThreadId
    ? threadTranscriptQuery.isFetched
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
  const showFreshThreadShell =
    !!activeThread &&
    activeThread.status === "idle" &&
    activeThread.current_turn === 0 &&
    timelineEntries.length === 0 &&
    !showLoadingSkeleton;

  const persistScrollPosition = useCallback((scrollTop: number) => {
    if (typeof window === "undefined" || !viewerScope) return;
    writeStoredSessionScrollPosition(window.localStorage, sessionId, viewerScope, scrollTop, activeThreadId);
  }, [activeThreadId, sessionId, viewerScope]);

  const findFirstVisibleTranscriptAnchor = useCallback((el: HTMLDivElement) => {
    const containerTop = el.getBoundingClientRect().top;
    const nodes = Array.from(el.querySelectorAll<HTMLElement>("[data-session-entry-id]"));
    for (const node of nodes) {
      const rect = node.getBoundingClientRect();
      if (rect.bottom < containerTop) continue;
      const id = node.dataset.sessionEntryId;
      if (!id) continue;
      return {
        anchor: { kind: "entry" as const, id },
        offsetPx: Math.max(0, containerTop - rect.top),
        scrollTopFallback: el.scrollTop,
      };
    }
    return null;
  }, []);

  const persistCurrentScrollPosition = useCallback((el: HTMLDivElement) => {
    if (typeof window === "undefined" || !viewerScope) return;
    const anchorPosition = activeThreadId ? findFirstVisibleTranscriptAnchor(el) : null;
    if (anchorPosition) {
      writeStoredSessionAnchorPosition(window.localStorage, sessionId, viewerScope, anchorPosition, activeThreadId);
      return;
    }
    writeStoredSessionScrollPosition(window.localStorage, sessionId, viewerScope, el.scrollTop, activeThreadId);
  }, [activeThreadId, findFirstVisibleTranscriptAnchor, sessionId, viewerScope]);

  const schedulePersistScrollPosition = useCallback((scrollTop: number) => {
    if (saveScrollTimerRef.current) {
      clearTimeout(saveScrollTimerRef.current);
    }
    saveScrollTimerRef.current = setTimeout(() => {
      const el = scrollRef.current;
      if (el) {
        persistCurrentScrollPosition(el);
      } else {
        persistScrollPosition(scrollTop);
      }
      saveScrollTimerRef.current = null;
    }, SCROLL_POSITION_SAVE_DEBOUNCE_MS);
  }, [persistCurrentScrollPosition, persistScrollPosition]);

  const cancelPendingInitialAnchorRestore = useCallback(() => {
    if (initialAnchorAppliedRef.current) return;
    initialAnchorCancelledRef.current = true;
    initialAnchorAppliedRef.current = true;
  }, []);

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

  const tryApplyDefaultEntryAnchor = useDefaultEntryAnchor({
    activeThreadId,
    sessionId,
    isEligible: !!activeThreadId && !initialThreadAnchorPosition && !isRunning,
    firstThreadWindow: threadTranscriptQuery.data?.pages[0],
    timelineEntries,
    scrollRef,
    syncScrollState,
    activeThreadTranscriptQueryKey,
    queryClient,
    scrollToLiveEdgePosition,
    initialAnchorAppliedRef,
    initialAnchorCancelledRef,
  });

  // Jump to the true bottom and keep it pinned across late layout growth. Lazy
  // markdown/code/images expand a few frames after the first scroll, so a single
  // scrollTop assignment can leave the view stranded above the real bottom.
  // Re-scroll on successive animation frames until the scroll height stops
  // changing (bounded so a perpetually-growing transcript can't loop forever).
  const settleBottomRafRef = useRef<number | null>(null);
  const settleScrollToBottom = useCallback(() => {
    if (settleBottomRafRef.current != null) {
      cancelAnimationFrame(settleBottomRafRef.current);
      settleBottomRafRef.current = null;
    }
    scrollToLiveEdgePosition();
    let lastHeight = scrollRef.current?.scrollHeight ?? -1;
    let stableFrames = 0;
    let frames = 0;
    const step = () => {
      const el = scrollRef.current;
      if (!el || ++frames > 30) {
        settleBottomRafRef.current = null;
        return;
      }
      el.scrollTop = el.scrollHeight;
      isNearBottomRef.current = true;
      if (el.scrollHeight === lastHeight) {
        stableFrames += 1;
      } else {
        stableFrames = 0;
        lastHeight = el.scrollHeight;
      }
      if (stableFrames >= 3) {
        settleBottomRafRef.current = null;
        return;
      }
      settleBottomRafRef.current = requestAnimationFrame(step);
    };
    settleBottomRafRef.current = requestAnimationFrame(step);
  }, [scrollToLiveEdgePosition]);

  useEffect(() => () => {
    if (settleBottomRafRef.current != null) {
      cancelAnimationFrame(settleBottomRafRef.current);
    }
  }, []);

  const scrollToLiveEdge = useCallback(() => {
    cancelPendingInitialAnchorRestore();
    if (activeThreadId && hasNewerThreadMessages) {
      setIsFetchingNewerThreadMessages(true);
      void api.sessions.getThreadTranscriptWindow(sessionId, activeThreadId, {
        position: "latest",
      }).then((window) => {
        const latestData = { pages: [window], pageParams: [{ position: "latest" }] } satisfies TranscriptWindowInfiniteData;
        if (activeThreadTranscriptQueryKey) {
          queryClient.setQueryData<TranscriptWindowInfiniteData>(
            activeThreadTranscriptQueryKey,
            latestData,
          );
        }
        queryClient.setQueryData<TranscriptWindowInfiniteData>(
          transcriptWindowQueryKey(sessionId, activeThreadId),
          latestData,
        );
        setNewerThreadMessagePages([]);
        settleScrollToBottom();
        setShowJumpToLatest(false);
      }).catch((error) => {
        toast.error(error instanceof ApiError ? error.message : "Failed to load latest messages");
      }).finally(() => {
        setIsFetchingNewerThreadMessages(false);
      });
      return;
    }
    settleScrollToBottom();
    const el = scrollRef.current;
    if (el) {
      if (saveScrollTimerRef.current) {
        clearTimeout(saveScrollTimerRef.current);
        saveScrollTimerRef.current = null;
      }
      persistScrollPosition(el.scrollTop);
    }
    setShowJumpToLatest(false);
  }, [activeThreadId, activeThreadTranscriptQueryKey, cancelPendingInitialAnchorRestore, hasNewerThreadMessages, persistScrollPosition, queryClient, settleScrollToBottom, sessionId]);

  const focusTranscript = useCallback(() => {
    scrollRef.current?.focus({ preventScroll: true });
  }, []);

  const scrollTranscriptByStep = useCallback((direction: 1 | -1) => {
    const el = scrollRef.current;
    if (!el) return;
    el.scrollBy?.({ top: direction * TRANSCRIPT_STEP_PX, behavior: "smooth" });
    if (typeof el.scrollBy !== "function") {
      el.scrollTop += direction * TRANSCRIPT_STEP_PX;
    }
  }, []);

  const scrollTranscriptByPage = useCallback((direction: 1 | -1) => {
    const el = scrollRef.current;
    if (!el) return;
    const distance = Math.max(
      TRANSCRIPT_PAGE_MIN_PX,
      Math.floor(el.clientHeight * TRANSCRIPT_PAGE_VIEWPORT_RATIO),
    );
    el.scrollBy?.({ top: direction * distance, behavior: "smooth" });
    if (typeof el.scrollBy !== "function") {
      el.scrollTop += direction * distance;
    }
  }, []);

  const scrollTranscriptToTop = useCallback(() => {
    const el = scrollRef.current;
    if (!el) return;
    el.scrollTo?.({ top: 0, behavior: "smooth" });
    if (typeof el.scrollTo !== "function") {
      el.scrollTop = 0;
    }
  }, []);

  const loadOlderThreadMessages = useCallback(() => {
    const el = scrollRef.current;
    if (el) {
      olderMessagesPrependSnapshotRef.current = {
        scrollHeight: el.scrollHeight,
        scrollTop: el.scrollTop,
      };
    }
    void threadTranscriptQuery.fetchNextPage();
  }, [threadTranscriptQuery]);

  const loadNewerThreadMessages = useCallback(() => {
    if (!activeThreadId || !nextNewerThreadCursor || isFetchingNewerThreadMessages) return;
    setIsFetchingNewerThreadMessages(true);
    void api.sessions.getThreadTranscriptWindow(sessionId, activeThreadId, {
      after: nextNewerThreadCursor,
    }).then((window) => {
      setNewerThreadMessagePages((pages) => [...pages, window]);
    }).catch((error) => {
      toast.error(error instanceof ApiError ? error.message : "Failed to load newer messages");
    }).finally(() => {
      setIsFetchingNewerThreadMessages(false);
    });
  }, [activeThreadId, isFetchingNewerThreadMessages, nextNewerThreadCursor, sessionId]);

  useLayoutEffect(() => {
    const snapshot = olderMessagesPrependSnapshotRef.current;
    const el = scrollRef.current;
    if (!snapshot || !el || threadTranscriptQuery.isFetchingNextPage) {
      return;
    }
    olderMessagesPrependSnapshotRef.current = null;
    el.scrollTop = snapshot.scrollTop + (el.scrollHeight - snapshot.scrollHeight);
  }, [threadMessages.length, threadTranscriptQuery.isFetchingNextPage]);

  const getEntryContainerProps = useCallback(
    (_entry: TimelineEntry, index: number) =>
      ({
        "data-session-entry-index": index,
        ...(_entry.transcriptEntryId ? { "data-session-entry-id": _entry.transcriptEntryId } : {}),
        ...(_entry.kind === "message" ? { "data-session-message-id": _entry.data.id } : {}),
      }) as React.HTMLAttributes<HTMLDivElement> & Record<`data-${string}`, string | number | undefined>,
    [],
  );

  useEffect(() => {
    onRegisterScrollToLiveEdge?.(scrollToLiveEdge);
    return () => onRegisterScrollToLiveEdge?.(null);
  }, [onRegisterScrollToLiveEdge, scrollToLiveEdge]);

  useEffect(() => {
    onRegisterKeyboardControls?.({
      focus: focusTranscript,
      scrollByStep: scrollTranscriptByStep,
      scrollByPage: scrollTranscriptByPage,
      scrollToTop: scrollTranscriptToTop,
      scrollToLatest: scrollToLiveEdge,
    });
    return () => onRegisterKeyboardControls?.(null);
  }, [
    focusTranscript,
    onRegisterKeyboardControls,
    scrollToLiveEdge,
    scrollTranscriptByPage,
    scrollTranscriptByStep,
    scrollTranscriptToTop,
  ]);

  // SSE streaming for real-time logs when the session is active.
  const mergeLogs = useCallback((newLogs: SessionLog[]) => {
    if (newLogs.length === 0) return;
    const cappedLogs = newLogs.map(capLiveSessionLogMessage);

    const logsByThread = new Map<string, SessionLog[]>();
    for (const log of cappedLogs) {
      if (!log.thread_id) continue;
      const threadLogs = logsByThread.get(log.thread_id) ?? [];
      threadLogs.push(log);
      logsByThread.set(log.thread_id, threadLogs);
    }
    for (const [threadID, threadLogs] of logsByThread) {
      queryClient.setQueryData<ListResponse<SessionLog>>(
        threadLiveLogsQueryKey(sessionId, threadID),
        (existing) => mergeSessionLogListResponse(existing, threadLogs, STREAMED_LOGS_MAX),
      );
    }

    if (activeThreadId) {
      return;
    }

    queryClient.setQueryData<ListResponse<SessionLog>>(
      sessionLiveLogsQueryKey(sessionId),
      (existing) => mergeSessionLogListResponse(existing, cappedLogs, STREAMED_LOGS_MAX),
    );
  }, [activeThreadId, queryClient, sessionId]);

  const mergeSessionStatusUpdate = useCallback((updated: SessionDetail) => {
    queryClient.setQueryData<SingleResponse<SessionDetail>>(["session", sessionId], (existing) => {
      return mergeSessionDetailStatusUpdate(existing, updated);
    });
    applySessionDetailToSessionListCaches(queryClient, updated);
  }, [queryClient, sessionId]);

  const mergeThreadInboxUpdate = useCallback((event: ThreadInboxEvent) => {
    queryClient.setQueryData<SingleResponse<SessionDetail>>(["session", sessionId], (existing) => {
      if (!existing || existing.data.id !== event.session_id) return existing;
      return {
        ...existing,
        data: {
          ...existing.data,
          threads: applyThreadInboxEventToThreads(existing.data.threads ?? [], event),
        },
      };
    });
  }, [queryClient, sessionId]);

  const mergeThreadRuntimeUpdate = useCallback((event: ThreadRuntimeEvent) => {
    queryClient.setQueryData<SingleResponse<SessionDetail>>(["session", sessionId], (existing) => {
      if (!existing || existing.data.id !== event.session_id) return existing;
      return {
        ...existing,
        data: {
          ...existing.data,
          threads: applyThreadRuntimeEventToThreads(existing.data.threads ?? [], event),
        },
      };
    });
  }, [queryClient, sessionId]);

  const mergeWorkspaceGenerationUpdate = useCallback((event: SessionWorkspaceGenerationChangedEvent) => {
    queryClient.setQueryData<SingleResponse<SessionDetail>>(["session", sessionId], (existing) => {
      if (!existing || existing.data.id !== event.session_id) return existing;
      return {
        ...existing,
        data: {
          ...existing.data,
          workspace_revision: event.workspace_revision,
          workspace_revision_updated_at: event.workspace_revision_updated_at,
        },
      };
    });
    queryClient.invalidateQueries({ queryKey: ["preview-status", sessionId] });
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
        queryClient.invalidateQueries({ queryKey: ["session", sessionId] });
      };

      addSSEListener(eventSource, SSE_EVENT.LOG, (log) => {
        if (!activeThreadId || log.thread_id === activeThreadId) {
          mergeLogs([log]);
        }
        if (log.level === "human_input") {
          invalidateSessionHumanInputRequests(queryClient, sessionId);
        }
      });

      addSSEListener(eventSource, SSE_EVENT.HUMAN_INPUT_CREATED, () => {
        invalidateSessionHumanInputRequests(queryClient, sessionId);
        queryClient.invalidateQueries({ queryKey: ["session", sessionId, "timeline"] });
        invalidateActiveThreadTranscript();
      });

      addSSEListener(eventSource, SSE_EVENT.HUMAN_INPUT_UPDATED, () => {
        invalidateSessionHumanInputRequests(queryClient, sessionId);
        queryClient.invalidateQueries({ queryKey: ["session", sessionId, "timeline"] });
        if (activeThreadId) {
          queryClient.invalidateQueries({ queryKey: queryKeys.sessions.threadTranscript(sessionId, activeThreadId) });
        }
      });

      addSSEListener(eventSource, SSE_EVENT.STATUS, (updated) => {
        mergeSessionStatusUpdate(updated);
        if ((!updated.threads || updated.threads.length === 0) && updated.status === "running") {
          queryClient.invalidateQueries({ queryKey: ["session", sessionId] });
        }
        // When the session transitions out of running (e.g. sandbox creation
        // failure reverts to idle), fetch the latest messages so any error
        // message posted by the backend is displayed immediately.
        if (updated.status !== "running") {
          clearCurrentLiveLogs();
          queryClient.invalidateQueries({ queryKey: ["session", sessionId, "timeline"] });
          queryClient.invalidateQueries({ queryKey: ["pull-request"] });
          invalidateSessionHumanInputRequests(queryClient, sessionId);
          if (activeThreadId) {
            queryClient.invalidateQueries({ queryKey: queryKeys.sessions.threadTranscript(sessionId, activeThreadId) });
          }
        }
      });

      addSSEListener(eventSource, SSE_EVENT.THREAD_INBOX_QUEUED, mergeThreadInboxUpdate);
      addSSEListener(eventSource, SSE_EVENT.THREAD_INBOX_CLEARED, mergeThreadInboxUpdate);
      addSSEListener(eventSource, SSE_EVENT.THREAD_RUNTIME_UPDATED, mergeThreadRuntimeUpdate);
      addSSEListener(eventSource, SSE_EVENT.SESSION_WORKSPACE_GENERATION_CHANGED, mergeWorkspaceGenerationUpdate);

      addSSEListener(eventSource, SSE_EVENT.DONE, (updated) => {
        mergeSessionStatusUpdate(updated);
        eventSource?.close();
        clearCurrentLiveLogs();
        queryClient.invalidateQueries({ queryKey: ["session", sessionId, "timeline"] });
        queryClient.invalidateQueries({ queryKey: ["pull-request"] });
        invalidateSessionHumanInputRequests(queryClient, sessionId);
        if (activeThreadId) {
          queryClient.invalidateQueries({ queryKey: queryKeys.sessions.threadTranscript(sessionId, activeThreadId) });
        }
      });

      eventSource.onerror = () => {
        eventSource?.close();
        queryClient.invalidateQueries({ queryKey: ["session", sessionId, "timeline"] });
        if (activeThreadId) {
          queryClient.invalidateQueries({ queryKey: queryKeys.sessions.threadTranscript(sessionId, activeThreadId) });
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
  }, [sessionId, apiBase, isActive, isDocumentVisible, mergeLogs, mergeSessionStatusUpdate, mergeThreadInboxUpdate, mergeThreadRuntimeUpdate, mergeWorkspaceGenerationUpdate, queryClient, activeThreadId, clearCurrentLiveLogs, invalidateActiveThreadTranscript]);

  // Track whether the user is scrolled near the bottom.
  const handleScroll = useCallback(() => {
    const el = scrollRef.current;
    if (!el) return;
    if (hasLoadedTimelineInputs) {
      cancelPendingInitialAnchorRestore();
    }
    syncScrollState(el);
    if (activeThreadId && isNearBottom(el) && hasNewerThreadMessages && !isFetchingNewerThreadMessages) {
      loadNewerThreadMessages();
    }
    schedulePersistScrollPosition(el.scrollTop);
  }, [activeThreadId, cancelPendingInitialAnchorRestore, hasLoadedTimelineInputs, hasNewerThreadMessages, isFetchingNewerThreadMessages, loadNewerThreadMessages, schedulePersistScrollPosition, syncScrollState]);

  useEffect(() => {
    initialAnchorAppliedRef.current = false;
    initialAnchorCancelledRef.current = false;
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
    if (
      !hasLoadedTimelineInputs ||
      initialAnchorAppliedRef.current ||
      initialAnchorCancelledRef.current ||
      !viewerScope
    ) return;

    const el = scrollRef.current;
    if (!el) return;

    const firstThreadWindow = threadTranscriptQuery.data?.pages[0];
    if (
      activeThreadId &&
      initialThreadAnchorPosition &&
      firstThreadWindow?.meta.anchor_found
    ) {
      const targetSelector = initialThreadAnchorPosition.anchor.kind === "entry"
        ? transcriptEntryIDSelector(initialThreadAnchorPosition.anchor.id)
        : `[data-session-message-id="${initialThreadAnchorPosition.anchor.id}"]`;
      const target = el.querySelector<HTMLElement>(targetSelector);
      if (target) {
        el.scrollTop = target.offsetTop + initialThreadAnchorPosition.offsetPx;
        syncScrollState(el);
        initialAnchorAppliedRef.current = true;
        return;
      }
    }

    if (tryApplyDefaultEntryAnchor(el)) return;

    const ignoreStoredScrollTop =
      activeThreadId &&
      !!initialThreadAnchorPosition &&
      firstThreadWindow?.meta.anchor_found === false;
    const storedScrollTop =
      ignoreStoredScrollTop || typeof window === "undefined"
        ? null
        : readStoredSessionScrollPosition(window.localStorage, sessionId, viewerScope, activeThreadId);
    const anchor = resolveInitialSessionAnchor({
      entries: timelineEntries,
      isActive: isRunning,
      storedScrollTop,
    });

    if (anchor.kind === "saved_position") {
      const maxScrollTop = Math.max(0, el.scrollHeight - el.clientHeight);
      if (
        activeThreadId &&
        threadTranscriptQuery.hasNextPage &&
        !threadTranscriptQuery.isFetchingNextPage &&
        anchor.scrollTop > maxScrollTop
      ) {
        void threadTranscriptQuery.fetchNextPage();
        return;
      }
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
  }, [activeThreadId, hasLoadedTimelineInputs, initialThreadAnchorPosition, isRunning, scrollToLiveEdgePosition, sessionId, syncScrollState, threadTranscriptQuery, timelineEntries, tryApplyDefaultEntryAnchor, viewerScope]);

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
      <div
        ref={scrollRef}
        onScroll={handleScroll}
        tabIndex={0}
        aria-label="Session conversation"
        data-session-transcript-scroll="true"
        className="flex-1 overflow-y-auto overscroll-contain space-y-2 p-4 outline-none focus-visible:ring-2 focus-visible:ring-ring/30"
      >
        {showLoadingSkeleton ? (
          <SessionTimelineSkeleton />
        ) : (
          <>
            {activeThreadId && threadTranscriptQuery.hasNextPage ? (
              <div className="flex justify-center pb-2">
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  onClick={loadOlderThreadMessages}
                  disabled={threadTranscriptQuery.isFetchingNextPage}
                >
                  {threadTranscriptQuery.isFetchingNextPage ? (
                    <Loader2 className="h-4 w-4 animate-spin" />
                  ) : (
                    <ArrowUp className="h-4 w-4" />
                  )}
                  Load older
                </Button>
              </div>
            ) : null}
            {showFreshThreadShell ? <FreshThreadShell /> : null}
            <ChatTimeline
              entries={timelineEntries}
              isRunning={isRunning}
              recoveryActive={recoveryActive}
              stoppingLabel={isStopRequested ? "Stopping agent..." : undefined}
              stoppedLabel={
                session.status === "cancelled" || activeThread?.status === "cancelled"
                  ? "Session stopped"
                  : stopOutcome === "checkpointed"
                    ? "Stopped. You can send a follow-up when ready."
                    : undefined
              }
              diffStats={session.diff_stats}
              onDiffClick={onDiffClick}
              onApprovePlan={canSendMessage ? onApprovePlan : undefined}
              onAdjustPlan={canSendMessage ? onAdjustPlan : undefined}
              humanInputSubmittingId={
                answerHumanInputMutation.isPending
                  ? answerHumanInputMutation.variables?.request.id ?? null
                  : cancelHumanInputMutation.isPending
                    ? cancelHumanInputMutation.variables?.id ?? null
                    : null
              }
              autoOpenHumanInputId={autoOpenHumanInputId}
              humanInputAnswerable={canAnswerHumanInput}
              onAnswerHumanInput={handleAnswerHumanInput}
              onCancelHumanInput={handleCancelHumanInput}
              onDismissHumanInputAutoOpen={handleDismissHumanInputAutoOpen}
              getEntryContainerProps={getEntryContainerProps}
            />
            {activeThreadId && hasNewerThreadMessages ? (
              <div className="flex justify-center pt-2">
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  onClick={loadNewerThreadMessages}
                  disabled={isFetchingNewerThreadMessages}
                >
                  {isFetchingNewerThreadMessages ? (
                    <Loader2 className="h-4 w-4 animate-spin" />
                  ) : (
                    <ArrowDown className="h-4 w-4" />
                  )}
                  Load newer
                </Button>
              </div>
            ) : null}
          </>
        )}
        {isPending && (
          isCapacityLimited ? (
            <PendingCapacityNotice maxConcurrentRuns={maxConcurrentRuns} />
          ) : (
            <div className="flex items-center justify-center py-12">
              <div className="text-center space-y-2 max-w-[280px]">
                <Loader2 className="h-8 w-8 text-muted-foreground/40 mx-auto animate-spin" />
                <p className="text-xs font-medium text-muted-foreground">Setting up environment</p>
                <p className="text-xs text-muted-foreground/60">Preparing the container and getting the agent ready to run.</p>
              </div>
            </div>
          )
        )}
      </div>
    </div>
  );
}

function PendingCapacityNotice({ maxConcurrentRuns }: { maxConcurrentRuns?: number }) {
  const limitText = maxConcurrentRuns && maxConcurrentRuns > 0
    ? `Your organization is already at its max concurrency limit of ${maxConcurrentRuns} running ${maxConcurrentRuns === 1 ? "session" : "sessions"}.`
    : "Your organization is already at its max concurrency limit for running sessions.";

  return (
    <div className="flex justify-center py-8">
      <Card className="w-full max-w-[34rem] border-amber-200/70 bg-amber-50/70 shadow-none dark:border-amber-900/60 dark:bg-amber-950/20">
        <CardContent className="flex gap-3 p-4">
          <div className="mt-0.5 flex h-9 w-9 shrink-0 items-center justify-center rounded-md border border-amber-200 bg-background text-amber-700 dark:border-amber-900/70 dark:text-amber-300">
            <Clock className="h-4 w-4" aria-hidden />
          </div>
          <div className="min-w-0 space-y-1.5">
            <div className="flex flex-wrap items-center gap-2">
              <p className="text-sm font-semibold text-foreground">Waiting for capacity</p>
              <Badge variant="outline" className="border-amber-300/80 bg-background/70 text-amber-800 dark:border-amber-800 dark:text-amber-300">
                Max concurrency reached
              </Badge>
            </div>
            <p className="text-sm text-muted-foreground">{limitText}</p>
            <p className="text-sm text-muted-foreground">
              This session will start automatically when another session finishes or the limit is raised.
            </p>
          </div>
        </CardContent>
      </Card>
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
    previous.viewerScope?.userId === next.viewerScope?.userId &&
    previous.viewerScope?.orgId === next.viewerScope?.orgId &&
    previous.optimisticMessages === next.optimisticMessages &&
    previous.isStopRequested === next.isStopRequested &&
    previous.stopOutcome === next.stopOutcome &&
    previous.onDiffClick === next.onDiffClick &&
    previous.onApprovePlan === next.onApprovePlan &&
    previous.onAdjustPlan === next.onAdjustPlan &&
    previous.onRegisterScrollToLiveEdge === next.onRegisterScrollToLiveEdge &&
    previous.onRegisterKeyboardControls === next.onRegisterKeyboardControls &&
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

function FreshThreadShell() {
  return (
    <Card className="w-full max-w-[92%] border-border/60 bg-muted/20 shadow-none">
      <CardContent className="flex flex-col gap-3 p-4">
        <span className="text-sm font-medium text-foreground">New tab</span>
        <div className="space-y-1">
          <p className="text-sm text-foreground">No context in this tab yet.</p>
          <p className="text-sm text-muted-foreground">
            Send a task or add context to get started.
          </p>
        </div>
      </CardContent>
    </Card>
  );
}

// ---------------------------------------------------------------------------
// Main component
// ---------------------------------------------------------------------------

const MIN_DETAIL = 280;
const MAX_DETAIL = 600;
const DEFAULT_DETAIL = 384;
const MOBILE_REVIEW_MEDIA_QUERY = "(max-width: 767px)";
const SESSION_HEADER_HEIGHT_CLASSNAME = "h-14";
// Transcript keyboard scroll tuning. Step matches a comfortable line-pair
// jump; page distance follows browser conventions (~85% viewport with a
// floor for very short panels).
const TRANSCRIPT_STEP_PX = 72;
const TRANSCRIPT_PAGE_MIN_PX = 160;
const TRANSCRIPT_PAGE_VIEWPORT_RATIO = 0.85;
const REVIEW_AGENT_KEYS = ["codex", "claude_code", "amp", "pi"] as const;
export const CHANGESET_SPLIT_MIN_ADDITIONS = 750;

export function shouldOfferChangesetSplit(additions?: number): boolean {
  return additions !== undefined && additions >= CHANGESET_SPLIT_MIN_ADDITIONS;
}

export function PullRequestList({
  changesets,
  selectedID,
  onSelect,
}: {
  changesets: ChangesetSummary[];
  selectedID: string;
  onSelect: (id: string) => void;
}) {
  if (changesets.length <= 1) return null;

  return (
    <Card className="border-border/60" data-testid="pull-request-list">
      <CardHeader className="p-3 pb-2">
        <CardTitle className="text-sm">Pull requests</CardTitle>
      </CardHeader>
      <CardContent className="space-y-1 p-2 pt-0">
        {changesets.map((changeset, index) => {
          const selected = changeset.id === selectedID;
          const pr = changeset.pull_request;
          return (
            <Button
              key={changeset.id}
              type="button"
              variant={selected ? "secondary" : "ghost"}
              className="h-auto w-full justify-start gap-2 px-2 py-2 text-left"
              aria-pressed={selected}
              onClick={() => onSelect(changeset.id)}
            >
              <span className="flex h-6 w-6 shrink-0 items-center justify-center rounded-full bg-muted text-xs font-medium">
                {index + 1}
              </span>
              <span className="min-w-0 flex-1">
                <span className="block truncate text-sm font-medium">{changeset.title}</span>
                <span className="block truncate text-xs text-muted-foreground">
                  {pr ? `#${pr.github_pr_number} · ${pr.status}` : changeset.status.replaceAll("_", " ")}
                </span>
                <span className="block truncate text-xs text-muted-foreground">
                  {changeset.base_branch} → {changeset.working_branch ?? "not materialized"}
                </span>
                {changeset.has_unpushed_changes && <span className="block text-xs text-amber-600">Unpushed changes</span>}
                {changeset.active_lease_holder_label && (
                  <span className="block truncate text-xs text-blue-600">
                    {changeset.active_lease_holder_type === "agent_turn" ? "Being edited in" : "In use by"} {changeset.active_lease_holder_label}
                  </span>
                )}
              </span>
              <span className="flex shrink-0 items-center gap-1">
                {pr?.ci_status && <Badge variant="outline" className="h-5 px-1 text-xs">CI {pr.ci_status}</Badge>}
                {changeset.stacked_on_changeset_id && <GitBranch className="h-3.5 w-3.5 text-muted-foreground" />}
              </span>
            </Button>
          );
        })}
      </CardContent>
    </Card>
  );
}

export function ChangesetSplitPlanner({
  sessionID,
  changesets,
  additions,
}: {
  sessionID: string;
  changesets: ChangesetSummary[];
  additions?: number;
}) {
  const queryClient = useQueryClient();
  const splitKey = ["session", sessionID, "changeset-split"] as const;
  const splitQuery = useQuery({
    queryKey: splitKey,
    queryFn: () => api.sessions.getChangesetSplitStatus(sessionID),
    enabled: true,
    retry: false,
  });
  const refresh = async () => {
    await queryClient.invalidateQueries({ queryKey: splitKey });
    await queryClient.invalidateQueries({ queryKey: queryKeys.sessions.detail(sessionID) });
  };
  const action = useMutation({
    mutationFn: async (run: () => Promise<unknown>) => run(),
    onSuccess: refresh,
    onError: (error) => toast.error(error instanceof Error ? error.message : "Split action failed"),
  });
  if (splitQuery.isError) {
    if (!shouldOfferChangesetSplit(additions)) return null;
    return (
      <Card className="border-border/60">
        <CardContent className="flex items-center justify-between gap-3 p-4">
          <div><p className="text-sm font-medium">Need smaller pull requests?</p><p className="text-xs text-muted-foreground">Freeze the current diff and split it into reviewable branches.</p></div>
          <Button size="sm" variant="outline" disabled={action.isPending} onClick={() => action.mutate(() => api.sessions.initializeChangesetSplit(sessionID))}>Split into PRs</Button>
        </CardContent>
      </Card>
    );
  }
  const status: ChangesetSplitStatus | undefined = splitQuery.data?.data;
  if (!status) return null;
  if (status.status === "accepted") {
    return <Card className="border-border/60"><CardContent className="p-4"><p className="text-sm font-medium">Split accepted</p><p className="text-xs text-muted-foreground">The original session diff is archived and the pull request branches are now the session rollup.</p></CardContent></Card>;
  }
  const candidates = changesets.filter((changeset) => !changeset.is_primary);
  const ownerByPath = new Map(status.assignments.flatMap((assignment) => assignment.paths.map((path) => [path, assignment.changeset_id] as const)));
  const omittedPaths = new Set(status.omissions.map((omission) => omission.path));
  const assign = async (path: string, nextOwner: string) => {
    const previous = ownerByPath.get(path);
    if (previous && previous !== nextOwner) {
      const paths = status.assignments.find((assignment) => assignment.changeset_id === previous)?.paths.filter((item) => item !== path) ?? [];
      await api.sessions.replaceChangesetSplitPaths(sessionID, previous, paths);
    }
    if (nextOwner === "__omit") {
      await api.sessions.replaceChangesetSplitOmissions(sessionID, [
        ...status.omissions.filter((omission) => omission.path !== path).map(({ path: omittedPath, reason }) => ({ path: omittedPath, reason })),
        { path, reason: "Explicitly omitted while accepting the split" },
      ]);
      return;
    }
    if (omittedPaths.has(path)) {
      await api.sessions.replaceChangesetSplitOmissions(sessionID, status.omissions.filter((omission) => omission.path !== path).map(({ path: omittedPath, reason }) => ({ path: omittedPath, reason })));
    }
    const paths = status.assignments.find((assignment) => assignment.changeset_id === nextOwner)?.paths ?? [];
    await api.sessions.replaceChangesetSplitPaths(sessionID, nextOwner, [...paths, path]);
  };
  return (
    <Card className="border-border/60" data-testid="changeset-split-planner">
      <CardHeader className="p-4 pb-2"><CardTitle className="flex items-center justify-between text-sm"><span>Split progress</span><Badge variant={status.complete ? "default" : "secondary"}>{status.verification === "verified" ? "Verified" : "Planning"}</Badge></CardTitle></CardHeader>
      <CardContent className="space-y-3 p-4 pt-1">
        <p className="text-xs text-muted-foreground">{status.source_paths.length - status.unassigned_paths.length} of {status.source_paths.length} files accounted for</p>
        <div className="max-h-64 space-y-1 overflow-y-auto">
          {status.source_paths.map((path) => (
            <div key={path} className="flex items-center gap-2 rounded-md border border-border p-2">
              <span className="min-w-0 flex-1 truncate text-xs" title={path}>{path}</span>
              <Select value={ownerByPath.get(path) ?? (omittedPaths.has(path) ? "__omit" : "unassigned")} onValueChange={(value) => value !== "unassigned" && action.mutate(() => assign(path, value))}>
                <SelectTrigger className="h-8 w-44 text-xs"><SelectValue /></SelectTrigger>
                <SelectContent><SelectItem value="unassigned">Unassigned</SelectItem><SelectItem value="__omit">Omit with confirmation</SelectItem>{candidates.map((changeset) => <SelectItem key={changeset.id} value={changeset.id}>{changeset.title}</SelectItem>)}</SelectContent>
              </Select>
            </div>
          ))}
        </div>
        {(status.duplicates.length > 0 || status.conflicts.length > 0 || status.unexpected_paths.length > 0) && <ErrorNotice title={`${status.duplicates.length} duplicate, ${status.conflicts.length} conflicting, and ${status.unexpected_paths.length} unexpected files require attention.`} />}
        <div className="space-y-1">
          {candidates.map((changeset, index) => (
            <div key={changeset.id} className="flex items-center gap-2 text-xs">
              <span className="min-w-0 flex-1 truncate">PR {index + 1}: {changeset.title}</span>
              {index > 0 && !changeset.worktree_path && !candidates[index - 1].worktree_path && <Button size="sm" variant="ghost" className="h-7 px-2 text-xs" disabled={action.isPending} onClick={() => action.mutate(() => api.sessions.foldChangeset(sessionID, changeset.id, candidates[index - 1].id))}>Fold up</Button>}
              <Button size="icon" variant="ghost" className="h-7 w-7" disabled={index === 0 || action.isPending} aria-label={`Move ${changeset.title} up`} onClick={() => action.mutate(() => { const ids = candidates.map((item) => item.id); [ids[index - 1], ids[index]] = [ids[index], ids[index - 1]]; return api.sessions.reorderChangesets(sessionID, ids); })}><ArrowUp className="h-3.5 w-3.5" /></Button>
              <Button size="icon" variant="ghost" className="h-7 w-7" disabled={index === candidates.length - 1 || action.isPending} aria-label={`Move ${changeset.title} down`} onClick={() => action.mutate(() => { const ids = candidates.map((item) => item.id); [ids[index], ids[index + 1]] = [ids[index + 1], ids[index]]; return api.sessions.reorderChangesets(sessionID, ids); })}><ArrowDown className="h-3.5 w-3.5" /></Button>
            </div>
          ))}
        </div>
        <div className="flex flex-wrap gap-2">
          <Button size="sm" variant="outline" disabled={action.isPending} onClick={() => action.mutate(() => api.sessions.createChangeset(sessionID, { title: `Pull request ${candidates.length + 1}` }))}><Plus className="h-3.5 w-3.5" />Add PR</Button>
          {candidates.filter((changeset) => !changeset.worktree_path).map((changeset) => <Button key={changeset.id} size="sm" variant="outline" disabled={action.isPending} onClick={() => action.mutate(() => api.sessions.materializeChangeset(sessionID, changeset.id))}>Materialize {changeset.title}</Button>)}
          <Button size="sm" variant="outline" disabled={action.isPending || candidates.some((changeset) => !changeset.worktree_path)} onClick={() => action.mutate(() => api.sessions.verifyChangesetSplit(sessionID))}>Verify split</Button>
          <Button size="sm" disabled={action.isPending || !status.complete} onClick={() => action.mutate(() => api.sessions.acceptChangesetSplit(sessionID))}>Accept split</Button>
        </div>
      </CardContent>
    </Card>
  );
}

function getDefaultReviewAgentType(sessionAgentType?: string): string {
  return REVIEW_AGENT_KEYS.find((agentType) => agentType !== sessionAgentType) ?? sessionAgentType ?? "codex";
}

export function SessionDetailContent({ id }: { id: string }) {
  const router = useRouter();
  const { user, isLoading: isAuthLoading } = useAuth();
  const canListTeamMembers = user?.role === "admin" || user?.role === "member";
  const canShipPR = user?.role === "admin" || user?.role === "member" || user?.role === "builder";
  const canManagePR = user?.role === "admin" || user?.role === "member";
  const terminalStatuses = new Set<SessionStatus>(["completed", "pr_created", "failed", "cancelled", "skipped"]);
  const [reviewParam, setReviewParam] = useQueryState("review");
  const [previewParam, setPreviewParam] = useQueryState("preview");
  const [resumePRParam, setResumePRParam] = useQueryState("resume_pr");
  const [resumeActionParam, setResumeActionParam] = useQueryState("resume_action");
  const [githubPRParam, setGithubPRParam] = useQueryState("github_pr");
  const [changesetParam, setChangesetParam] = useQueryState("changeset");
  const [centerMode, setCenterMode] = useState<"chat" | "review">(
    reviewParam === "active" ? "review" : "chat"
  );
  const [hasMountedChatPanel, setHasMountedChatPanel] = useState(
    reviewParam !== "active"
  );
  const [detailTab, setDetailTab] = useState<DetailTab>(
    previewParam === "1" ? "preview" : "overview"
  );
  const [showDetailPanel, setShowDetailPanel] = useState(true);
  // Mobile bottom sheet — separate state so the desktop inline panel can
  // default open while the mobile sheet defaults closed (no SSR-unsafe
  // matchMedia needed).
  const [mobileDetailOpen, setMobileDetailOpen] = useState(false);
  const [mobileReviewComposerOpen, setMobileReviewComposerOpen] = useState(false);
  const [mobileRenameOpen, setMobileRenameOpen] = useState(false);
  const [keyboardHelpOpen, setKeyboardHelpOpen] = useState(false);
  const [reviewConfigOpen, setReviewConfigOpen] = useState(false);
  const [reviewPasses, setReviewPasses] = useState(2);
  const [reviewAgentType, setReviewAgentType] = useState<string>("codex");
  const [reviewFixMode, setReviewFixMode] = useState<ReviewLoopFixMode>("minimal");
  const [detailWidth, setDetailWidth] = useState(DEFAULT_DETAIL);
  const [activeFileIndex, setActiveFileIndex] = useState(0);
  // null means "follow the saved user-settings preference"; a boolean means
  // the user toggled full screen in this session (and we persist it).
  const [diffFullScreenOverride, setDiffFullScreenOverride] = useState<boolean | null>(null);
  const [isEditingTitle, setIsEditingTitle] = useState(false);
  const [draftTitle, setDraftTitle] = useState("");
  const [isMobileReviewViewport, setIsMobileReviewViewport] = useState(false);
  const previousReviewParamRef = useRef(reviewParam);
  const suppressNextReviewParamClearRef = useRef(false);
  const lastReviewSyncIdRef = useRef<string | undefined>(undefined);
  useEffect(() => {
    if (reviewParam === "active") {
      setCenterMode("review");
    } else if (previousReviewParamRef.current === "active") {
      if (suppressNextReviewParamClearRef.current) {
        suppressNextReviewParamClearRef.current = false;
      } else {
        setHasMountedChatPanel(true);
        setCenterMode("chat");
      }
    }
    previousReviewParamRef.current = reviewParam;
  }, [reviewParam]);

  useEffect(() => {
    const urlParams =
      typeof window === "undefined"
        ? null
        : new URLSearchParams(window.location.search);
    const urlReviewParam = urlParams?.get("review") ?? null;
    const urlPreviewParam = urlParams?.get("preview") ?? null;
    // On a session switch the new session's review state comes solely from its
    // own URL; only the initial mount may fall back to the remembered param
    // (e.g. an empty search string during hydration). Without this guard the
    // stale ref carries the previous session's "active" param into the next
    // session and leaves it stuck in review mode.
    const isSessionSwitch =
      lastReviewSyncIdRef.current !== undefined && lastReviewSyncIdRef.current !== id;
    lastReviewSyncIdRef.current = id;
    const nextReviewParam = isSessionSwitch
      ? urlReviewParam
      : typeof window === "undefined" || window.location.search === ""
        ? previousReviewParamRef.current
        : urlReviewParam;
    const isDirectReview = nextReviewParam === "active";
    const isDirectPreview = urlPreviewParam === "1";
    if (!isSessionSwitch && !isDirectReview && !isDirectPreview) {
      return;
    }
    setHasMountedChatPanel(!isDirectReview);
    previousReviewParamRef.current = nextReviewParam;
    setDetailTab(isDirectPreview ? "preview" : isDirectReview ? "changes" : "overview");
    // Drive centerMode deterministically on session change. Without this, a
    // session left in review mode bleeds "review" into the next session,
    // because the reviewParam clear path is gated by
    // suppressNextReviewParamClearRef (set by openReview) and gets skipped.
    suppressNextReviewParamClearRef.current = false;
    setCenterMode(isDirectReview ? "review" : "chat");
  }, [id]);

  useEffect(() => {
    const syncReviewModeFromHistory = () => {
      const nextReviewParam = new URLSearchParams(window.location.search).get("review");
      suppressNextReviewParamClearRef.current = false;
      previousReviewParamRef.current = nextReviewParam;
      if (nextReviewParam !== "active") {
        setHasMountedChatPanel(true);
      }
      setCenterMode(nextReviewParam === "active" ? "review" : "chat");
    };

    window.addEventListener("popstate", syncReviewModeFromHistory);
    return () => window.removeEventListener("popstate", syncReviewModeFromHistory);
  }, []);

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
  const selectedIsPrimaryRef = useRef(true);

  // --- Enter review mode ---
  const openReview = useCallback((fileIndex?: number) => {
    if (!selectedIsPrimaryRef.current) {
      setDetailTab("changes");
      setShowDetailPanel(true);
      if (isMobileReviewViewport) {
        setMobileDetailOpen(true);
      }
      return;
    }
    if (fileIndex !== undefined) setActiveFileIndex(fileIndex);
    setCenterMode("review");
    suppressNextReviewParamClearRef.current = true;
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
  const openMobileReviewComposer = useCallback(() => {
    setMobileReviewComposerOpen(true);
  }, []);

  // --- Exit review mode ---
  const exitReview = useCallback(() => {
    suppressNextReviewParamClearRef.current = false;
    setHasMountedChatPanel(true);
    setCenterMode("chat");
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
  const [localBranchState, setLocalBranchState] = useState<"idle" | "submitting" | "queued">("idle");
  const [localBranchActionError, setLocalBranchActionError] = useState<PRActionErrorState | null>(null);
  const [localPushState, setLocalPushState] = useState<"idle" | "submitting" | "queued">("idle");
  const [localPushActionError, setLocalPushActionError] = useState<PRActionErrorState | null>(null);
  // True while any PR-level action (create PR / push / create branch) is mid
  // flight from this tab. Drives background reconciliation on the session
  // detail query (see Fix below) so a backgrounded or refocused tab still
  // converges to the action's terminal state instead of spinning forever.
  // Mirrored from the optimistic phases via an effect because the detail query
  // is declared before those phases are reconciled against the server.
  const [anyActionInFlight, setAnyActionInFlight] = useState(false);
  const [pendingPRAction, setPendingPRAction] = useState<"fix_tests" | "resolve_conflicts" | "merge" | null>(null);
  const [pendingMergeWhenReady, setPendingMergeWhenReady] = useState(false);
  const [repairActionError, setRepairActionError] = useState<string | null>(null);
  const [prAuthPrompt, setPRAuthPrompt] = useState<PRAuthPromptState | null>(null);
  const resumeAttemptRef = useRef<string | null>(null);
  useEffect(() => {
    const inFlight = localPRState !== "idle" || localPushState !== "idle" || localBranchState !== "idle";
    setAnyActionInFlight((prev) => (prev === inFlight ? prev : inFlight));
  }, [localPRState, localPushState, localBranchState]);
  const apiBase = process.env.NEXT_PUBLIC_API_URL || "";
  const isDocumentVisible = useDocumentVisible();

  const resetSessionChromeAndActionState = useCallback(() => {
    setMobileDetailOpen(false);
    setMobileReviewComposerOpen(false);
    setMobileRenameOpen(false);
    setKeyboardHelpOpen(false);
    setReviewConfigOpen(false);
    setReviewPasses(2);
    setReviewFixMode("minimal");
    setActiveFileIndex(0);
    setIsEditingTitle(false);
    setDraftTitle("");
    setLocalPRState("idle");
    setLocalPRActionError(null);
    setLocalBranchState("idle");
    setLocalBranchActionError(null);
    setLocalPushState("idle");
    setLocalPushActionError(null);
    setPendingPRAction(null);
    setPendingMergeWhenReady(false);
    setRepairActionError(null);
    setPRAuthPrompt(null);
    resumeAttemptRef.current = null;
  }, []);
  useSessionScopedReset(id, [
    { name: "session chrome and action state", reset: resetSessionChromeAndActionState },
  ]);

  const { data, isLoading, error } = useQuery({
    queryKey: queryKeys.sessions.detail(id),
    queryFn: () => api.sessions.get(id),
    // Sidebar navigation may seed this key with list-row data so the detail
    // shell can open immediately. Always treat that cache entry as stale so
    // the authoritative detail payload replaces it on mount.
    staleTime: 0,
    refetchInterval: (q) => {
      const s = q.state.data?.data;
      if (!s) return false;
      if (isProvisionalSessionDetail(s)) return false;
      const sessionVolatile = workingStatusesSet.has(s.status);
      const threadVolatile = (s.threads ?? []).some((thread) => workingStatusesSet.has(thread.status));
      const serverInFlight = s.pr_creation_state === "queued" || s.pr_creation_state === "pushing";
      const waitingForServer = localPRState !== "idle" &&
        s.pr_creation_state !== "failed" &&
        s.pr_creation_state !== "succeeded";
      const pushInFlight = s.pr_push_state === "queued" || s.pr_push_state === "pushing";
      const waitingForPushServer = localPushState !== "idle" &&
        s.pr_push_state !== "failed" &&
        s.pr_push_state !== "succeeded";
      const branchInFlight = s.branch_creation_state === "queued" || s.branch_creation_state === "pushing";
      const waitingForBranchServer = localBranchState !== "idle" &&
        s.branch_creation_state !== "failed" &&
        s.branch_creation_state !== "succeeded";

      // Poll while PR creation OR push-changes is in flight so the state
      // machine advances without waiting for the user to navigate. Keep
      // polling during the optimistic local phases too, since the best-effort
      // queued write can legitimately lag the 202 response.
      if (serverInFlight || waitingForServer || pushInFlight || waitingForPushServer || branchInFlight || waitingForBranchServer) {
        return pollMs(2000);
      }
      return sessionVolatile || threadVolatile ? SESSION_DETAIL_ACTIVE_REFETCH_INTERVAL_MS : false;
    },
    // While a PR-level action is in flight, keep converging even if the tab is
    // backgrounded or refocused. For a terminal (completed) session there is no
    // live SSE channel — the only signal that the action settled is this poll,
    // and the global defaults (refetchIntervalInBackground=false,
    // refetchOnWindowFocus=false) would otherwise pause it and leave the button
    // spinning until a manual reload. Scoped to the in-flight window so idle
    // sessions keep the original no-background-work behavior.
    refetchIntervalInBackground: anyActionInFlight,
    refetchOnWindowFocus: anyActionInFlight,
  });

  const { data: membersData } = useQuery({
    queryKey: ["team", "members"],
    queryFn: () => api.team.listMembers(),
    enabled: canListTeamMembers,
  });

  const viewerScope = useMemo<SessionScrollViewerScope | null>(
    () => (user ? { userId: user.id, orgId: getActiveOrgId() ?? user.org_id } : null),
    [user],
  );
  const rawSession = data?.data;
  const isProvisionalSession = isProvisionalSessionDetail(rawSession);
  const session = isProvisionalSession ? undefined : rawSession;
  const changesets = session?.changesets ?? [];
  const primaryChangeset = changesets.find((changeset) => changeset.is_primary) ?? changesets[0];
  const [selectedChangesetID, setSelectedChangesetID] = useState<string | null>(changesetParam);
  const selectedChangeset = changesets.find((changeset) => changeset.id === selectedChangesetID) ?? primaryChangeset;
  const stackTopChangeset = changesets.filter((changeset) => changeset.status !== "abandoned").at(-1);
  const hasMultipleChangesets = changesets.length > 1;
  const changesetLifecycleMutation = useMutation({
    mutationFn: (action: () => Promise<unknown>) => action(),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["session", id] });
      toast.success("Pull request action queued");
    },
    onError: (error) => toast.error(error instanceof Error ? error.message : "Pull request action failed"),
  });
  const changesetPreviewMutation = useMutation({
    mutationFn: (target: ChangesetSummary) => {
      if (!session?.repository_id || !target.working_branch) throw new Error("Pull request branch is unavailable");
      return api.previews.create({
        repository_id: session.repository_id,
        branch: target.working_branch,
        commit_sha: target.head_sha,
        source: { type: "session", external_id: target.id },
      });
    },
    onError: (error) => toast.error(error instanceof Error ? error.message : "Preview could not be started"),
  });
  const selectedIsPrimary = selectedChangeset?.is_primary !== false;
  selectedIsPrimaryRef.current = selectedIsPrimary;
  // The primary changeset keeps the legacy null-tolerant PR lookup: sending a
  // changeset_id would route the backend to GetByChangesetID, which cannot
  // match legacy PR rows whose changeset_id is still NULL. Only non-primary
  // slots, whose PRs always carry a changeset_id, are looked up by changeset.
  const selectedBranchChangesetID = selectedIsPrimary ? undefined : selectedChangeset?.id;
  const changesetSessionIDRef = useRef(id);
  useEffect(() => {
    const syncSelectionFromURL = () => setSelectedChangesetID(new URL(window.location.href).searchParams.get("changeset"));
    window.addEventListener("popstate", syncSelectionFromURL);
    return () => window.removeEventListener("popstate", syncSelectionFromURL);
  }, []);
  useEffect(() => {
    if (changesetSessionIDRef.current === id) return;
    changesetSessionIDRef.current = id;
    setSelectedChangesetID(null);
    void setChangesetParam(null);
  }, [id, setChangesetParam]);
  useEffect(() => {
    if (selectedIsPrimary || selectedChangeset?.worktree_path || centerMode !== "review") return;
    exitReview();
    setDetailTab("changes");
    setShowDetailPanel(true);
    if (isMobileReviewViewport) {
      setMobileDetailOpen(true);
    }
  }, [centerMode, exitReview, isMobileReviewViewport, selectedChangeset?.worktree_path, selectedIsPrimary]);
  // Tab title from whatever payload is available — the provisional row's
  // title matches what the user just clicked, so don't wait for the
  // authoritative detail to label the tab.
  usePageTitle(rawSession ? sessionTitle(rawSession) : null, "Session");
  const members = membersData?.data ?? [];
  const shouldLoadDiff = (
    !isProvisionalSession &&
    (centerMode === "review" ||
      detailTab === "changes")
  );
  const diffRevisionKey = useMemo(() => {
    if (!session) return null;
    const diffIdentity = session.latest_diff_snapshot_id ?? session.diff_collected_at ?? "";
    return [
      diffIdentity,
      session.diff_stats?.added ?? "",
      session.diff_stats?.removed ?? "",
      session.diff_stats?.files_changed ?? "",
    ].join(":");
  }, [session]);
  const fetchedDiffBeforeRevisionRef = useRef(false);
  const observedDiffRevisionKeyRef = useRef<string | null>(null);
  const {
    data: diffData,
    isLoading: isDiffLoading,
    isFetching: isDiffFetching,
    isError: isDiffError,
    isFetchedAfterMount: isDiffFetchedAfterMount,
    error: diffError,
    refetch: refetchDiff,
  } = useQuery({
    queryKey: queryKeys.sessions.diff(id, selectedBranchChangesetID),
    queryFn: () => {
      if (!diffRevisionKey) {
        fetchedDiffBeforeRevisionRef.current = true;
      }
      return api.sessions.getDiff(id, selectedBranchChangesetID);
    },
    enabled: shouldLoadDiff,
    staleTime: Infinity,
    refetchOnWindowFocus: false,
    retry: false,
  });
  const resetSessionDiffRevisionState = useCallback(() => {
    fetchedDiffBeforeRevisionRef.current = false;
    observedDiffRevisionKeyRef.current = null;
  }, []);
  useSessionScopedReset(id, [
    { name: "session diff revision state", reset: resetSessionDiffRevisionState },
  ]);
  useEffect(() => {
    if (centerMode === "review") {
      void loadReviewDiffView();
    }
  }, [centerMode]);
  const retryDiffLoad = useCallback(() => {
    fetchedDiffBeforeRevisionRef.current = false;
    void refetchDiff();
  }, [refetchDiff]);
  const sessionDiffPayload = diffData?.data;
  useEffect(() => {
    if (!shouldLoadDiff || !diffRevisionKey) {
      return;
    }

    if (observedDiffRevisionKeyRef.current === diffRevisionKey) {
      return;
    }

    const isInitialRevision = observedDiffRevisionKeyRef.current === null;
    observedDiffRevisionKeyRef.current = diffRevisionKey;

    if (isInitialRevision && fetchedDiffBeforeRevisionRef.current) {
      return;
    }
    if (isInitialRevision && (!sessionDiffPayload || isDiffFetchedAfterMount)) {
      return;
    }

    fetchedDiffBeforeRevisionRef.current = false;
    void refetchDiff();
  }, [diffRevisionKey, isDiffFetchedAfterMount, refetchDiff, sessionDiffPayload, shouldLoadDiff]);
  const threads = useMemo(() => session?.threads ?? [], [session?.threads]);
  const [pendingThreadPreview, setPendingThreadPreview] = useState<PendingThreadPreview | null>(null);
  const chromeThreads = useMemo(
    () => buildChromeThreads(threads, pendingThreadPreview),
    [pendingThreadPreview, threads],
  );
  const nonInteractiveThreadIds = useMemo(
    () => new Set(pendingThreadPreview?.id === "__pending-thread__" ? [pendingThreadPreview.id] : []),
    [pendingThreadPreview],
  );
  const [activeThreadId, setActiveThreadId] = useState<string | null>(null);
  const [hasResolvedInitialThreadSelection, setHasResolvedInitialThreadSelection] = useState(false);
  const [viewedThreadIds, setViewedThreadIds] = useState<Set<string>>(() => (
    typeof window === "undefined" ? new Set() : readStoredViewedThreadIds(window.localStorage, id)
  ));
  const [viewedThreadIdsLoadedForSessionId, setViewedThreadIdsLoadedForSessionId] = useState<string | null>(() => (
    typeof window === "undefined" ? null : id
  ));
  const activeThread = chromeThreads.find((thread) => thread.id === activeThreadId) ?? null;
  const activeThreadIndex = activeThread ? chromeThreads.findIndex((thread) => thread.id === activeThread.id) : -1;
  const isActive = session ? !terminalStatuses.has(session.status) : false;
  const isRunning = session?.status === "running";
  const currentTitle = session ? sessionTitle(session) : "";

  const queryClient = useQueryClient();

  // Warm the stored active thread's first message window in parallel with the
  // session detail fetch. Without this, the messages request can't start
  // until the detail payload arrives, the thread-selection effect runs, and
  // ChatPanel mounts — a full extra round trip on every page open even
  // though the server answers in single-digit milliseconds. The stored
  // thread id (and, pre-auth, the cached viewer scope) is a hint: if it
  // turns out stale, the normal resolution flow corrects the selection and
  // this prefetch is just unused cache. ChatPanel's useInfiniteQuery shares
  // the exact query key, so React Query dedupes against the in-flight fetch.
  const didPrefetchThreadMessagesRef = useRef(false);
  const resetSessionPrefetchState = useCallback(() => {
    didPrefetchThreadMessagesRef.current = false;
  }, []);
  useSessionScopedReset(id, [
    { name: "session prefetch state", reset: resetSessionPrefetchState },
  ]);
  useEffect(() => {
    if (didPrefetchThreadMessagesRef.current || typeof window === "undefined") return;
    didPrefetchThreadMessagesRef.current = true;
    const scope: SessionScrollViewerScope | null = user
      ? { userId: user.id, orgId: getActiveOrgId() ?? user.org_id }
      : readCachedViewerScope(window.localStorage);
    if (!scope) return;
    const storedThreadId = readStoredSessionActiveThread(window.localStorage, id, scope);
    if (!storedThreadId) return;
    const anchor = readStoredSessionAnchorPosition(window.localStorage, id, scope, storedThreadId);
    // ChatPanel's transcript infinite query shares this exact key, so React
    // Query dedupes against the in-flight prefetch.
    void queryClient.prefetchInfiniteQuery({
      queryKey: transcriptWindowQueryKey(id, storedThreadId, transcriptAnchorKey(anchor)),
      queryFn: () =>
        api.sessions.getThreadTranscriptWindow(
          id,
          storedThreadId,
          transcriptInitialPageParam(anchor),
        ),
      initialPageParam: transcriptInitialPageParam(anchor),
    });
  }, [id, queryClient, user]);

  // Full-screen diff viewer. The preference lives on the user settings
  // document so it sticks across sessions; mobile review already fills the
  // viewport, so the mode is desktop-only.
  const isDiffFullScreen =
    !isMobileReviewViewport &&
    (diffFullScreenOverride ?? user?.settings?.diff_viewer_full_screen ?? false);
  const { mutate: persistDiffFullScreen } = useMutation({
    // PATCH /auth/me/settings is a merge patch, so the flag travels alone and
    // can't clobber settings edited concurrently elsewhere.
    mutationFn: (fullScreen: boolean) =>
      api.auth.updateSettings({ diff_viewer_full_screen: fullScreen }),
    onSuccess: (response) => {
      queryClient.setQueryData(["auth", "me"], { data: response.data });
    },
    onError: () => {
      toast.error("Couldn't save full screen preference");
    },
  });
  const toggleDiffFullScreen = useCallback(() => {
    const next = !isDiffFullScreen;
    setDiffFullScreenOverride(next);
    persistDiffFullScreen(next);
  }, [isDiffFullScreen, persistDiffFullScreen]);

  const activeThreadDelivery = activeThread?.inbox_delivery;
  const activeThreadHasRecoverableInbox =
    !!activeThreadDelivery &&
    (activeThreadDelivery.dead_letter_count > 0 || activeThreadDelivery.unknown_delivery_count > 0);
  const recoverableInboxThreadId =
    activeThreadHasRecoverableInbox && activeThread && !nonInteractiveThreadIds.has(activeThread.id)
      ? activeThread.id
      : null;
  const recoverableInboxQuery = useQuery({
    queryKey: recoverableInboxThreadId
      ? queryKeys.sessions.threadRecoverableInbox(id, recoverableInboxThreadId)
      : ["session", id, "thread", null, "recoverable-inbox"],
    queryFn: () => {
      if (!recoverableInboxThreadId) {
        throw new Error("Recoverable inbox query requires an active thread");
      }
      return api.sessions.listRecoverableThreadInboxEntries(id, recoverableInboxThreadId);
    },
    enabled: recoverableInboxThreadId !== null,
    // Recoverable entries don't change without either (a) user action, which
    // already invalidates the query in the retry mutation's onSuccess, or
    // (b) backend state changes (new failure, reaper marking unknown
    // delivery). Poll slowly to catch (b), and pause completely when the
    // tab is hidden — refetchIntervalInBackground=false (the default) stops
    // the interval; refetchOnWindowFocus=true picks up any changes when the
    // user returns.
    refetchInterval: recoverableInboxThreadId ? 30_000 : false,
    refetchIntervalInBackground: false,
    refetchOnWindowFocus: true,
  });
  const recoverableInboxEntries = useMemo(
    () => recoverableInboxQuery.data?.data ?? [],
    [recoverableInboxQuery.data?.data],
  );
  const retryRecoverableInboxMutation = useMutation({
    mutationFn: async ({ threadId, entryIds, replayUnknownDelivery }: { threadId: string; entryIds: string[]; replayUnknownDelivery?: boolean }) => {
      // Drive retries sequentially, not via Promise.all. The server-side
      // RetryRecoverable flips delivery_state and then enqueues a delivery
      // notification; firing N concurrent calls against the same thread
      // races those steps against each other for no benefit. The retry
      // budget is tiny (one entry per click for individual retries; the
      // current failed-entry list for "Retry all"), so the latency cost is
      // negligible compared to the consistency win.
      for (const entryId of entryIds) {
        await api.sessions.retryThreadInboxEntry(id, threadId, entryId, { replayUnknownDelivery });
      }
    },
    onSuccess: (_result, variables) => {
      queryClient.invalidateQueries({ queryKey: queryKeys.sessions.detail(id) });
      queryClient.invalidateQueries({ queryKey: queryKeys.sessions.threads(id) });
      queryClient.invalidateQueries({ queryKey: queryKeys.sessions.threadRecoverableInbox(id, variables.threadId) });
      toast.success(variables.entryIds.length === 1 ? "Message queued for retry" : "Messages queued for retry");
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to retry message delivery");
    },
  });
  const handleRetryRecoverableInboxEntry = useCallback((entryId: string, replayUnknownDelivery = false) => {
    if (!recoverableInboxThreadId) {
      return;
    }
    retryRecoverableInboxMutation.mutate({
      threadId: recoverableInboxThreadId,
      entryIds: [entryId],
      replayUnknownDelivery,
    });
  }, [recoverableInboxThreadId, retryRecoverableInboxMutation]);
  const handleRetryAllRecoverableInbox = useCallback(() => {
    if (!recoverableInboxThreadId || recoverableInboxEntries.length === 0) {
      return;
    }
    const failedEntryIds = recoverableInboxEntries.filter((entry) => entry.delivery_state === "dead_letter").map((entry) => entry.id);
    if (failedEntryIds.length === 0) {
      return;
    }
    retryRecoverableInboxMutation.mutate({
      threadId: recoverableInboxThreadId,
      entryIds: failedEntryIds,
      replayUnknownDelivery: false,
    });
  }, [recoverableInboxEntries, recoverableInboxThreadId, retryRecoverableInboxMutation]);
  const renderRecoverableInboxNotice = useCallback(() => {
    if (!activeThreadDelivery || !activeThreadHasRecoverableInbox) {
      return null;
    }
    return (
      <RecoverableInboxNotice
        summary={activeThreadDelivery}
        entries={recoverableInboxEntries}
        isLoading={recoverableInboxQuery.isFetching && recoverableInboxEntries.length === 0}
        isRetrying={retryRecoverableInboxMutation.isPending}
        onRetryEntry={handleRetryRecoverableInboxEntry}
        onRetryAll={handleRetryAllRecoverableInbox}
      />
    );
  }, [
    activeThreadDelivery,
    activeThreadHasRecoverableInbox,
    handleRetryAllRecoverableInbox,
    handleRetryRecoverableInboxEntry,
    recoverableInboxEntries,
    recoverableInboxQuery.isFetching,
    retryRecoverableInboxMutation.isPending,
  ]);
  const optimisticMessageIDRef = useRef(-1_000_000);
  const [optimisticMessages, setOptimisticMessages] = useState<PendingFollowUpMessage[]>([]);
  const [sessionStopRequest, setSessionStopRequest] = useState<{ sessionId: string; requestedAt: string } | null>(null);
  const [sessionStopOutcome, setSessionStopOutcome] = useState<"checkpointed" | null>(null);

  useEffect(() => {
    void queryClient.invalidateQueries({
      queryKey: ["preview-status", id],
    });
  }, [
    id,
    queryClient,
    session?.diff_collected_at,
    session?.latest_diff_snapshot_id,
    session?.workspace_revision,
  ]);

  const resetSessionRuntimeState = useCallback(() => {
    setHasResolvedInitialThreadSelection(false);
    setActiveThreadId(null);
    setPendingThreadPreview(null);
    setSessionStopRequest(null);
    setSessionStopOutcome(null);
    setOptimisticMessages([]);
    optimisticMessageIDRef.current = -1_000_000;
  }, []);
  useSessionScopedReset(id, [
    { name: "session runtime state", reset: resetSessionRuntimeState },
  ]);

  useEffect(() => {
    if (!session) {
      return;
    }
    if (session.status === "running") {
      setSessionStopOutcome(null);
    }
    if (!sessionStopRequest || sessionStopRequest.sessionId !== id) {
      return;
    }

    if (session.status === "cancelled" || activeThread?.status === "cancelled") {
      setSessionStopRequest(null);
      setSessionStopOutcome(null);
      return;
    }

    const activeThreadSettled = !activeThread || !workingStatusesSet.has(activeThread.status);
    if (session.status !== "running" && activeThreadSettled) {
      setSessionStopRequest(null);
      setSessionStopOutcome(
        session.status === "idle" && (session.sandbox_state === "snapshotted" || !!session.snapshot_key)
          ? "checkpointed"
          : null,
      );
    }
  }, [activeThread, id, session, sessionStopRequest]);

  useEffect(() => {
    if (!session) {
      return;
    }

    if (chromeThreads.length === 0) {
      if (activeThreadId !== null) {
        setActiveThreadId(null);
      }
      if (!hasResolvedInitialThreadSelection) {
        setHasResolvedInitialThreadSelection(true);
      }
      return;
    }

    if (!hasResolvedInitialThreadSelection) {
      if (!viewerScope || typeof window === "undefined") {
        if (isAuthLoading) {
          return;
        }
        setHasResolvedInitialThreadSelection(true);
        setActiveThreadId(threads[0].id);
        return;
      }

      const storedThreadId = readStoredSessionActiveThread(window.localStorage, id, viewerScope);
      const nextThreadId = resolveInitialSessionThreadId(threads, storedThreadId);
      setHasResolvedInitialThreadSelection(true);
      if (activeThreadId !== nextThreadId) {
        setActiveThreadId(nextThreadId);
      }
      return;
    }

    if (!activeThreadId || !chromeThreads.some((thread) => thread.id === activeThreadId)) {
      setActiveThreadId(chromeThreads[0].id);
    }
  }, [activeThreadId, chromeThreads, hasResolvedInitialThreadSelection, id, isAuthLoading, session, threads, viewerScope]);

  useEffect(() => {
    if (!hasResolvedInitialThreadSelection || !viewerScope || !activeThreadId || typeof window === "undefined") {
      return;
    }

    writeStoredSessionActiveThread(window.localStorage, id, viewerScope, activeThreadId);
  }, [activeThreadId, hasResolvedInitialThreadSelection, id, viewerScope]);

  useEffect(() => {
    if (typeof window === "undefined") {
      return;
    }
    setViewedThreadIds(readStoredViewedThreadIds(window.localStorage, id));
    setViewedThreadIdsLoadedForSessionId(id);
  }, [id]);

  useEffect(() => {
    if (!activeThread?.id) {
      return;
    }
    setViewedThreadIds((current) => {
      if (current.has(activeThread.id)) {
        return current;
      }
      return new Set(current).add(activeThread.id);
    });
  }, [activeThread?.id]);

  useEffect(() => {
    if (typeof window === "undefined" || viewedThreadIdsLoadedForSessionId !== id) {
      return;
    }
    const visibleThreadIDs = new Set(threads.map((thread) => thread.id));
    writeStoredViewedThreadIds(
      window.localStorage,
      id,
      [...viewedThreadIds].filter((threadId) => visibleThreadIDs.has(threadId)),
    );
  }, [id, threads, viewedThreadIds, viewedThreadIdsLoadedForSessionId]);

  type SendMutationArgs = {
    activeThreadId?: string;
	    body: {
	      message: string;
	      changesetId?: string;
	      clientMessageID?: string;
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
      changesetId?: string;
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
    queryKey: queryKeys.sessions.pr(id, selectedBranchChangesetID),
    queryFn: () => api.sessions.getPR(id, selectedBranchChangesetID),
    enabled: !isProvisionalSession,
    // Updates flow in via mutation invalidations and the session SSE stream
    // (pr_creation_state / pr_push_state); a small staleTime suppresses
    // redundant refetches on remount or unrelated cache invalidations.
    staleTime: 30_000,
  });
  const selectedPR = prData?.data ?? selectedChangeset?.pull_request;
  const pullRequestId = selectedPR?.id;
  const { data: prHealthData, isLoading: isPRHealthLoading } = useQuery({
    queryKey: ["pull-request", pullRequestId, "health"],
    queryFn: () => api.pullRequests.getHealth(pullRequestId!),
    enabled: !!pullRequestId && selectedPR?.status === "open",
    // Pushed via the PULL_REQUEST_UPDATED SSE event. The stream onopen handler
    // below also reconciles once because Redis pub/sub does not replay PR row
    // or health events missed while the tab was hidden or the EventSource was
    // reconnecting.
    staleTime: 30_000,
    refetchInterval: (query) => getPullRequestHealthRefetchInterval(query.state.data?.data),
  });
  const prHealth = prHealthData?.data;
  const prHealthActionsBlocked = prHealthBlocksPRActions(prHealth);
  const rawPRStatus = selectedPR?.status;
  const prStatus = deriveEffectivePRStatus(rawPRStatus, prHealth?.status);
  const prNumber = selectedPR?.github_pr_number;
  const closedPRNumber = prNumber;
  const closedPRLabel = closedPRNumber ? `PR #${closedPRNumber} closed` : "PR closed";
  const closedPRSummary = closedPRNumber
    ? `PR #${closedPRNumber} was closed without merging.`
    : "This pull request was closed without merging.";
  const mergedPRNumber = prNumber;
  const mergedPRLabel = mergedPRNumber ? `PR #${mergedPRNumber} merged` : "PR merged";
  const mergedPRSummary = mergedPRNumber
    ? `PR #${mergedPRNumber} was merged successfully.`
    : "This pull request was merged successfully.";

  // React to pr_creation_state transitions with toast feedback. Tracks the
  // previous value via ref so we fire once per transition rather than on
  // every render.
  const prevPRStateRef = useRef<string | undefined>(undefined);
  const prevPRUrlRef = useRef<string | undefined>(undefined);
  const prevPRPushStateRef = useRef<string | undefined>(undefined);
  const prevBranchStateRef = useRef<string | undefined>(undefined);
  const resetSessionTransitionRefs = useCallback(() => {
    prevPRStateRef.current = undefined;
    prevPRUrlRef.current = undefined;
    prevPRPushStateRef.current = undefined;
    prevBranchStateRef.current = undefined;
  }, []);
  useSessionScopedReset(id, [
    { name: "session transition refs", reset: resetSessionTransitionRefs },
  ]);

  // Optimistically mark a PR-level action's server column in flight in the
  // detail cache the moment the request is accepted (202). This keeps the
  // cached state consistent with reality (the server CAS'd the column to
  // queued) so the optimistic-action reconcile arms deterministically and a
  // stale prior terminal value (e.g. a previous failed push) doesn't briefly
  // flash through the button before the next fetch lands.
  const markSessionActionInFlight = useCallback(
    (field: "pr_creation_state" | "pr_push_state" | "branch_creation_state") => {
      queryClient.setQueryData<SingleResponse<SessionDetail>>(queryKeys.sessions.detail(id), (old) => {
        if (!old?.data) return old;
        if (old.data[field] === "queued" || old.data[field] === "pushing") return old;
        return { ...old, data: { ...old.data, [field]: "queued" } };
      });
    },
    [queryClient, id],
  );
  // Level-triggered reconciliation: clear each optimistic phase once the server
  // settles, independent of whether the client witnessed the transition edge.
  // This is what prevents a missed SSE event or a paused background poll from
  // stranding an action button on a spinner.
  const resolvePRAction = useCallback(() => setLocalPRState("idle"), []);
  const resolvePushAction = useCallback(() => setLocalPushState("idle"), []);
  const resolveBranchAction = useCallback(() => setLocalBranchState("idle"), []);
  useReconcileOptimisticAction({ phase: localPRState, serverState: session?.pr_creation_state, onResolved: resolvePRAction });
  useReconcileOptimisticAction({ phase: localPushState, serverState: session?.pr_push_state, onResolved: resolvePushAction });
  useReconcileOptimisticAction({ phase: localBranchState, serverState: session?.branch_creation_state, onResolved: resolveBranchAction });

  const prUrl = selectedPR?.github_pr_url;
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
        queryClient.invalidateQueries({ queryKey: ["session", id, "pr"] });
        toast.error(PR_ERROR_TOAST_MESSAGE, { duration: PR_ERROR_TOAST_DURATION_MS });
      }
    }
    prevPRStateRef.current = current;
  }, [session?.pr_creation_state, session?.pr_creation_error, prUrl, queryClient, id]);
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
  // Edge-triggered (one toast per transition); clearing the optimistic phase is
  // owned by useReconcileOptimisticAction above, which is level-triggered and
  // therefore robust to a missed transition.
  useEffect(() => {
    const prev = prevPRPushStateRef.current;
    const current = session?.pr_push_state;
    if (prev && current && prev !== current) {
      if (current === "succeeded") {
        queryClient.invalidateQueries({ queryKey: ["session", id, "pr"] });
        if (pullRequestId) {
          queryClient.invalidateQueries({ queryKey: ["pull-request", pullRequestId, "health"] });
        }
        toast.success("Changes pushed to PR", prUrl ? {
          action: { label: "View \u2197", onClick: () => window.open(prUrl, "_blank", "noopener,noreferrer") },
        } : undefined);
      } else if (current === "failed") {
        toast.error(session?.pr_push_error || "Push to PR failed", { duration: PR_ERROR_TOAST_DURATION_MS });
      }
    }
    prevPRPushStateRef.current = current;
  }, [session?.pr_push_state, session?.pr_push_error, prUrl, queryClient, id, pullRequestId]);
  useEffect(() => {
    const prev = prevBranchStateRef.current;
    const current = session?.branch_creation_state;
    if (prev && prev !== current) {
      if (current === "succeeded") {
        toast.success("Branch created", session?.branch_url ? {
          action: { label: "View \u2197", onClick: () => window.open(session.branch_url, "_blank", "noopener,noreferrer") },
        } : undefined);
      } else if (current === "failed") {
        toast.error(session?.branch_creation_error || "Failed to create branch", { duration: PR_ERROR_TOAST_DURATION_MS });
      }
    }
    prevBranchStateRef.current = current;
  }, [session?.branch_creation_state, session?.branch_creation_error, session?.branch_url]);
  const startRepairMutation = useMutation({
    mutationFn: async ({ action, pushChanges }: { action: "fix_tests" | "resolve_conflicts"; pushChanges: boolean }) => {
      if (!pullRequestId) {
        throw new Error("Pull request not found");
      }
      const body = activeThread?.id ? { thread_id: activeThread.id, push_changes: pushChanges } : { push_changes: pushChanges };
      return action === "fix_tests"
        ? api.pullRequests.fixTests(pullRequestId, body)
        : api.pullRequests.resolveConflicts(pullRequestId, body);
    },
    onMutate: ({ action }) => {
      setRepairActionError(null);
      setPendingPRAction(action);
    },
    onSuccess: async (response) => {
      const repairHealthQueryKey = ["pull-request", pullRequestId, "health"];
      void queryClient.invalidateQueries({ queryKey: ["session", id] });
      void queryClient.invalidateQueries({ queryKey: ["session", id, "timeline"] });
      void queryClient.invalidateQueries({ queryKey: ["session", id, "pr"] });

      if (response.data.session_id !== id) {
        router.push(`/sessions/${response.data.session_id}`);
        return;
      }
      if (response.data.thread_id && response.data.thread_id !== activeThreadId) {
        setActiveThreadId(response.data.thread_id);
      }
      try {
        await queryClient.refetchQueries({ queryKey: repairHealthQueryKey, type: "active" });
      } finally {
        setPendingPRAction(null);
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
    onError: (err, { action }) => {
      setPendingPRAction(null);
      if (err instanceof ApiError && (err.code === "REPAIR_ALREADY_IN_PROGRESS" || err.code === "REPAIR_SESSION_BUSY")) {
        const label = action === "fix_tests"
          ? "Fix tests session is already in progress"
          : "Resolve conflicts session is already in progress";
        toast.info(label);
        return;
      }
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
      toast.success(mergedPRLabel, prUrl ? {
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
  const mergeWhenReadyMutation = useMutation({
    mutationFn: async (action: "queue" | "cancel") => {
      if (!pullRequestId) {
        throw new Error("Pull request not found");
      }
      return action === "queue"
        ? api.pullRequests.queueMergeWhenReady(pullRequestId)
        : api.pullRequests.cancelMergeWhenReady(pullRequestId);
    },
    onMutate: () => {
      setRepairActionError(null);
      setPendingMergeWhenReady(true);
    },
    onSuccess: (_response, action) => {
      setPendingMergeWhenReady(false);
      void queryClient.invalidateQueries({ queryKey: ["pull-request", pullRequestId, "health"] });
      toast.success(action === "queue" ? "Merge when ready enabled" : "Merge when ready cancelled");
    },
    onError: (err) => {
      setPendingMergeWhenReady(false);
      const message = err instanceof ApiError ? err.message : "Failed to update merge when ready";
      toast.error(message);
    },
  });
  useEffect(() => {
    // Pause the PR health SSE stream while the tab is hidden — same reasoning
    // as the session log stream above. The onerror branch already invalidates
    // the health query on disconnect, so reconnecting on visibility refreshes
    // the cached health to whatever happened while we were away.
    if (!pullRequestId || selectedPR?.status !== "open" || !isDocumentVisible) {
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
        void queryClient.invalidateQueries({ queryKey: ["session", id, "pr"] });
        void queryClient.invalidateQueries({ queryKey: ["pull-request", pullRequestId, "health"] });
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

        void queryClient.invalidateQueries({ queryKey: ["session", id, "pr"] });
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
  }, [apiBase, selectedPR?.status, pullRequestId, queryClient, isDocumentVisible, id]);
  const previousSessionStatusRef = useRef<SessionStatus | undefined>(undefined);
  const resetSessionNotificationRefs = useCallback(() => {
    previousSessionStatusRef.current = undefined;
  }, []);
  useSessionScopedReset(id, [
    { name: "session notification refs", reset: resetSessionNotificationRefs },
  ]);
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
    if (session?.id) {
      api.sessions.recordView(id).then(() => {
        queryClient.invalidateQueries({ queryKey: ["sessions"] });
      }).catch((err) => {
        console.error("failed to record session view", err);
      });
    }
  }, [id, queryClient, session?.id]);

  const hasPR = !!selectedPR;
  const hasSnapshot = !!session?.snapshot_key;
  const hasSessionChanges = !!session?.diff || !!session?.diff_stats;
  const isTerminalSession = session ? terminalSessionStatuses.has(session.status) : false;
  const showExpiredPRAction = hasSessionChanges && !hasSnapshot && !hasPR && isTerminalSession;
  const canManageSession = user?.role === "admin" || user?.role === "member" || user?.role === "builder";
  const canUseNativeReviewLoop = !!session && session.agent_type !== "pm_agent";
  const sessionID = session?.id;
  const sessionAgentType = session?.agent_type;
  const reviewAgentOptions = useMemo(
    () => REVIEW_AGENT_KEYS.map((agentType) => AGENTS_BY_KEY[agentType]).filter(Boolean),
    [],
  );

  useEffect(() => {
    if (!sessionAgentType) {
      return;
    }
    setReviewAgentType(getDefaultReviewAgentType(sessionAgentType));
  }, [sessionID, sessionAgentType]);

  const { data: reviewLoopsData } = useQuery({
    queryKey: queryKeys.sessions.reviewLoops(id),
    queryFn: () => api.sessions.listReviewLoops(id),
    enabled: !!session,
  });
  const { data: readinessData } = useQuery({
    queryKey: queryKeys.sessions.readiness(id, selectedChangeset?.id),
    queryFn: () => api.sessions.getReadiness(id, selectedChangeset?.id),
    enabled: !!session && (selectedIsPrimary || !!selectedChangeset?.worktree_path),
    refetchInterval: (query) => {
      const status = query.state.data?.data.latest?.status;
      return status === "queued" || status === "running" ? pollMs(3000) : false;
    },
  });
  const runReadinessMutation = useMutation({
    mutationFn: () => api.sessions.runReadiness(id, selectedChangeset?.id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: queryKeys.sessions.readiness(id, selectedChangeset?.id) });
      toast.success("Readiness checks queued");
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Readiness checks could not be queued");
    },
  });
  const { data: readinessPolicyResponse } = useQuery({
    queryKey: queryKeys.settings.prReadinessPolicy(session?.repository_id ?? null),
    queryFn: () => api.settings.getPRReadinessPolicy(session?.repository_id ?? undefined),
    enabled: !!session && canManageSession,
  });
  const latestReviewLoop = reviewLoopsData?.data?.[0] ?? null;
  const builderRequiresReviewBeforePR = user?.role === "builder" && (
    readinessPolicyResponse?.data.config
      ? policyRequiresRoleReadiness(readinessPolicyResponse.data.config, "builder")
      : true
  );
  const latestReadiness = readinessData?.data.latest;
  const latestReadinessStale = !!latestReadiness && (selectedChangeset?.worktree_path
    ? (latestReadiness.evaluated_head_sha ?? "") !== (selectedChangeset.head_sha ?? "")
    : latestReadiness.evaluated_workspace_revision !== session?.workspace_revision ||
      (latestReadiness.evaluated_snapshot_key ?? "") !== (session?.snapshot_key ?? ""));
  const latestBypassedKeys = new Set((latestReadiness?.bypasses ?? []).flatMap((bypass) => bypass.bypassed_checks));
  const hasUnbypassedReadinessBlocker = (latestReadiness?.checks ?? []).some((check) => {
    const key = check.check_key || check.check_type;
    const enforcement = check.effective_enforcement || check.enforcement_by_role?.builder || check.enforcement;
    return (check.status === "failed" || check.status === "error") && enforcement === "blocking" && !latestBypassedKeys.has(key);
  });
  const readinessFresh = !!latestReadiness &&
    latestReadiness.status !== "queued" &&
    latestReadiness.status !== "running" &&
    latestReadiness.status !== "failed" &&
    !latestReadinessStale &&
    !hasUnbypassedReadinessBlocker;
  const builderReviewAllowsPR = !builderRequiresReviewBeforePR || readinessFresh;
  const readinessAutoRunOnCreatePR = readinessPolicyResponse?.data.config.auto_run?.on_create_pr === true;
  const readinessAutoRunCanQueue = builderRequiresReviewBeforePR &&
    readinessAutoRunOnCreatePR &&
    (!latestReadiness ||
      latestReadinessStale ||
      latestReadiness.status === "queued" ||
      latestReadiness.status === "running");
  const createPRAllowsSubmission = builderReviewAllowsPR || readinessAutoRunCanQueue;
  const canAttemptCreatePR = canShipPR && hasSnapshot && !hasPR && !isRunning && selectedIsPrimary;
  const canCreatePR = canAttemptCreatePR && createPRAllowsSubmission;
  const canCreateBranch = canAttemptCreatePR && builderReviewAllowsPR;
  const needsGitHubStatus = canCreatePR || (hasPR && selectedPR?.status === "open");
  const reviewLoopRunning = latestReviewLoop?.status === "running";
  const canStartReviewLoop = !!session && canManageSession && canUseNativeReviewLoop && hasSnapshot && !isRunning && !reviewLoopRunning;
  const reviewUnavailableReason = reviewLoopRunning
    ? "Review loop is running"
    : !hasSnapshot
      ? "A reusable sandbox snapshot is required before review"
      : isRunning
        ? "Review can start after the current turn finishes"
        : undefined;

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
    mutationFn: (options?: { draft?: boolean; authorMode?: PRAuthorMode; resumeToken?: string; mergeWhenReady?: boolean }) =>
      api.sessions.createPR(id, selectedChangeset ? { ...options, changesetId: selectedChangeset.id } : options),
    onMutate: () => {
      setLocalPRActionError(null);
      setLocalPRState("submitting");
    },
    onSuccess: (data, options) => {
      if (data.status === "readiness_queued") {
        setLocalPRActionError(null);
        setLocalPRState("idle");
        queryClient.invalidateQueries({ queryKey: queryKeys.sessions.readiness(id) });
        toast.success("Readiness checks queued");
        return;
      }
      setLocalPRActionError(null);
      setLocalPRState("queued");
      markSessionActionInFlight("pr_creation_state");
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
        setPRAuthPrompt({ ...err.details, purpose: "create_pr", mergeWhenReady: options?.mergeWhenReady || err.details.merge_when_ready === true });
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

  const createBranchMutation = useMutation({
    mutationFn: (options?: { authorMode?: PRAuthorMode; resumeToken?: string }) =>
      api.sessions.createBranch(id, options),
    onMutate: () => {
      setLocalBranchActionError(null);
      setLocalBranchState("submitting");
    },
    onSuccess: (_data, options) => {
      setLocalBranchActionError(null);
      setLocalBranchState("queued");
      markSessionActionInFlight("branch_creation_state");
      if (options?.resumeToken) {
        clearPRResumeParams();
      }
      queryClient.invalidateQueries({ queryKey: ["session", id] });
    },
    onError: (err, options) => {
      if (err instanceof ApiError &&
        (err.code === "GITHUB_PR_AUTHORSHIP_REQUIRED" || err.code === "GITHUB_PR_AUTHORSHIP_REAUTH_REQUIRED") &&
        isPRAuthInterceptDetails(err.details)) {
        setLocalBranchState("idle");
        setLocalBranchActionError(null);
        setPRAuthPrompt({ ...err.details, purpose: "create_branch" });
        clearPRResumeParams();
        return;
      }
      setLocalBranchState("idle");
      const msg = err instanceof Error ? err.message : "Failed to create branch";
      setLocalBranchActionError({
        code: err instanceof ApiError ? err.code : undefined,
        message: msg,
      });
      if (options?.resumeToken) {
        clearPRResumeParams();
      }
      toast.error(msg, { duration: PR_ERROR_TOAST_DURATION_MS });
    },
  });

  const submitCreatePR = useCallback((options?: { draft?: boolean; authorMode?: PRAuthorMode; resumeToken?: string; mergeWhenReady?: boolean }) => {
    createPRMutation.mutate(options);
  }, [createPRMutation]);

  const startReviewLoopMutation = useMutation({
    mutationFn: () =>
      api.sessions.startReviewLoop(id, {
        agent_type: reviewAgentType,
        model: session?.agent_type === reviewAgentType ? session?.model_override : undefined,
        max_passes: reviewPasses,
        fix_mode: reviewFixMode,
      }),
    onSuccess: (response) => {
      toast.success("Review loop started");
      setReviewConfigOpen(false);
      const reviewThread = buildReviewLoopThreadPreview(response.data, session);
      if (reviewThread) {
        setPendingThreadPreview(reviewThread);
        queryClient.setQueryData<SingleResponse<SessionDetail>>(queryKeys.sessions.detail(id), (existing) => {
          if (!existing) return existing;
          const existingThreads = existing.data.threads ?? [];
          return {
            ...existing,
            data: {
              ...existing.data,
              threads: [...existingThreads.filter((thread) => thread.id !== reviewThread.id), reviewThread],
            },
          };
        });
        setActiveThreadId(reviewThread.id);
        setComposerSelectedModel(getInitialComposerSelectedModel(reviewThread));
      }
      queryClient.invalidateQueries({ queryKey: queryKeys.sessions.reviewLoops(id) });
      queryClient.invalidateQueries({ queryKey: queryKeys.sessions.threads(id) });
      queryClient.invalidateQueries({ queryKey: queryKeys.sessions.detail(id) });
    },
    onError: (err) => {
      const msg = err instanceof Error ? err.message : "Review loop could not be started";
      toast.error(msg);
    },
  });
  const reviewActionDisabled = !canStartReviewLoop || startReviewLoopMutation.isPending;
  const reviewActionDisabledReason = startReviewLoopMutation.isPending
    ? "Starting review loop..."
    : reviewUnavailableReason;

  const readinessRunning =
    latestReadiness?.status === "queued" ||
    latestReadiness?.status === "running" ||
    runReadinessMutation.isPending;
  const readinessStale = !!session && readinessIsStale(latestReadiness, session);
  const readinessCheckDisabled = readinessRunning || isRunning || (!selectedIsPrimary && !selectedChangeset?.worktree_path);

  // Readiness findings, grouped with role-aware enforcement so the merged
  // Review card can surface blockers, bypasses, and the review packet inline.
  const [readinessBypassOpen, setReadinessBypassOpen] = useState(false);
  const [readinessBypassReason, setReadinessBypassReason] = useState("");
  const resetSessionReadinessBypassState = useCallback(() => {
    setReadinessBypassOpen(false);
    setReadinessBypassReason("");
  }, []);
  useSessionScopedReset(id, [
    { name: "session readiness bypass state", reset: resetSessionReadinessBypassState },
  ]);
  const readinessPacketRef = useRef<HTMLDivElement | null>(null);
  const readinessBypassMutation = useMutation({
    mutationFn: () => api.sessions.createReadinessBypass(id, readinessBypassReason),
    onSuccess: () => {
      setReadinessBypassOpen(false);
      setReadinessBypassReason("");
      queryClient.invalidateQueries({ queryKey: queryKeys.sessions.readiness(id) });
      toast.success("Readiness blocker bypassed");
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Readiness blocker could not be bypassed");
    },
  });
  const readinessChecks = latestReadiness?.checks ?? [];
  const readinessCheckKey = (check: PRReadinessCheck) => check.check_key || check.check_type;
  const readinessCheckEnforcement = (check: PRReadinessCheck) =>
    check.effective_enforcement || enforcementForRole(check, user?.role) || check.enforcement;
  const readinessStaleCheck = readinessStale && latestReadiness && session
    ? staleReadinessCheck(latestReadiness, session, readinessCheckEnforcement)
    : null;
  const readinessVisibleChecks = readinessStale
    ? readinessChecks.filter((check) => check.check_type !== "freshness")
    : readinessChecks;
  const readinessPassedChecks = readinessVisibleChecks.filter((check) => check.status === "passed");
  const readinessBlockedChecks = [
    ...readinessVisibleChecks.filter((check) =>
      (check.status === "failed" || check.status === "error") &&
      readinessCheckEnforcement(check) === "blocking" &&
      !latestBypassedKeys.has(readinessCheckKey(check))
    ),
    ...(readinessStaleCheck ? [readinessStaleCheck] : []),
  ];
  const readinessBypassedChecks = readinessChecks.filter((check) => latestBypassedKeys.has(readinessCheckKey(check)));
  const readinessWarningChecks = readinessVisibleChecks.filter((check) =>
    check.status === "warning" ||
    ((check.status === "failed" || check.status === "error") && readinessCheckEnforcement(check) !== "blocking")
  );
  const readinessBypassPolicy = readinessPolicyResponse?.data.config.bypass;
  const readinessBypassRoleAllowed = !readinessBypassPolicy || (
    readinessBypassPolicy.enabled !== false &&
    (readinessBypassPolicy.allowed_roles ?? ["admin", "member", "builder"]).includes(user?.role ?? "") &&
    (readinessBypassPolicy.scopes ?? ["completed_blocking_checks"]).includes("completed_blocking_checks")
  );
  const readinessNonBypassableChecks = new Set(readinessBypassPolicy?.non_bypassable_checks ?? []);
  const readinessBypassableBlocked = readinessBlockedChecks.filter((check) =>
    !readinessStale &&
    !isDerivedStaleReadinessCheck(check) &&
    readinessBypassRoleAllowed &&
    !readinessNonBypassableChecks.has(readinessCheckKey(check)) &&
    !readinessNonBypassableChecks.has(check.check_type)
  );
  const readinessReviewPacket = readinessPacket(latestReadiness?.review_packet);
  const handleReadinessCheckAction = (check: PRReadinessCheck) => {
    const action = (check.action ?? "").toLowerCase();
    if (!action) return;
    if (action.includes("re-run") || action.includes("run readiness")) {
      if (!readinessCheckDisabled) runReadinessMutation.mutate();
      return;
    }
    if (action.includes("view files") || action.includes("view changes")) {
      setDetailTab("changes");
      return;
    }
    if (action.includes("run review") || action.includes("fix with agent") || action.includes("view review")) {
      if (!reviewActionDisabled) setReviewConfigOpen(true);
      return;
    }
    if (action.includes("view packet")) {
      readinessPacketRef.current?.scrollIntoView({ block: "nearest" });
      return;
    }
    if (action.includes("configuration")) {
      router.push("/settings");
    }
  };
  const readinessCheckActionDisabled = (check: PRReadinessCheck) => {
    const action = (check.action ?? "").toLowerCase();
    if (action.includes("re-run") || action.includes("run readiness")) {
      return readinessCheckDisabled;
    }
    if (action.includes("run review") || action.includes("fix with agent") || action.includes("view review")) {
      return reviewActionDisabled;
    }
    return false;
  };
  const readinessNeedsActionChecks = [...readinessBlockedChecks, ...readinessWarningChecks];
  const readinessOptionalCount = readinessWarningChecks.length + readinessBypassedChecks.length;
  const readinessDetailCount = readinessNeedsActionChecks.length + readinessPassedChecks.length + readinessBypassedChecks.length + (readinessReviewPacket ? 1 : 0);
  const readinessPrimaryCopy = (() => {
    if (reviewLoopRunning) {
      return {
        title: `Fixing with ${AGENTS_BY_KEY[latestReviewLoop?.agent_type ?? ""]?.label ?? latestReviewLoop?.agent_type ?? "agent"}`,
        description: `Pass ${Math.min((latestReviewLoop?.completed_passes ?? 0) + 1, latestReviewLoop?.max_passes ?? 1)} of ${latestReviewLoop?.max_passes ?? 1}`,
      };
    }
    if (readinessRunning) {
      return { title: "Checking readiness", description: "Reviewing the latest changes before PR." };
    }
    if (!latestReadiness) {
      return { title: "Review before PR", description: "Run a quick readiness check before opening a PR." };
    }
    if (readinessStale) {
      return { title: "Not ready yet", description: "Files changed since the last check." };
    }
    if (latestReadiness.status === "failed") {
      return { title: "Check failed", description: "Readiness could not finish. Try running it again." };
    }
    if (readinessBlockedChecks.length > 0) {
      return {
        title: "Not ready yet",
        description: readinessBlockedChecks.length === 1
          ? `${readinessBlockedChecks[0].title} needs attention.`
          : `${readinessBlockedChecks.length} readiness checks need attention.`,
      };
    }
    if (readinessWarningChecks.length > 0) {
      return {
        title: "Ready with notes",
        description: readinessWarningChecks.length === 1
          ? "1 optional improvement is available."
          : `${readinessWarningChecks.length} optional improvements are available.`,
      };
    }
    return { title: "Ready for PR", description: "The latest checks passed." };
  })();
  const readinessPrimaryAction = (() => {
    if (reviewLoopRunning) return null;
    if (!latestReadiness || readinessStale || readinessRunning || latestReadiness.status === "failed") {
      return {
        label: latestReadiness ? "Re-check readiness" : "Check readiness",
        icon: readinessRunning ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <RefreshCw className="h-3.5 w-3.5" />,
        disabled: readinessCheckDisabled,
        onClick: () => runReadinessMutation.mutate(),
      };
    }
    if (readinessBlockedChecks.length > 0 && canUseNativeReviewLoop) {
      return {
        label: "Review & fix",
        icon: startReviewLoopMutation.isPending ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Settings2 className="h-3.5 w-3.5" />,
        disabled: reviewActionDisabled,
        onClick: () => setReviewConfigOpen(true),
      };
    }
    return null;
  })();
  const readinessSecondaryReviewAction = !reviewLoopRunning && !latestReadiness && canUseNativeReviewLoop
    ? {
      label: "Review & fix",
      icon: startReviewLoopMutation.isPending ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Settings2 className="h-3.5 w-3.5" />,
      disabled: reviewActionDisabled,
      onClick: () => setReviewConfigOpen(true),
    }
    : null;

  const pushChangesMutation = useMutation({
    mutationFn: (options?: { authorMode?: PRAuthorMode; resumeToken?: string }) =>
      api.sessions.pushChangesToPR(id, { ...options, changesetId: selectedChangeset?.id }),
    onMutate: () => {
      setLocalPushActionError(null);
      setLocalPushState("submitting");
    },
    onSuccess: (_data, options) => {
      setLocalPushActionError(null);
      setLocalPushState("queued");
      markSessionActionInFlight("pr_push_state");
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

  const continueFromPRBranchMutation = useMutation({
    mutationFn: async () => {
      const headRef = selectedPR?.head_ref ?? selectedPR?.branch_name;
      const message = continueFromPRBranchMessage(headRef);
      if (activeThread?.id) {
        const clientMessageID =
          typeof crypto !== "undefined" && "randomUUID" in crypto
            ? crypto.randomUUID()
            : `${id}:${activeThread.id}:continue-pr-branch:${Date.now()}:${Math.random()}`;
        return api.sessions.sendThreadMessage(id, activeThread.id, { message, clientMessageID });
      }
      return api.sessions.sendMessage(id, { message });
    },
    onSuccess: () => {
      setLocalPushActionError(null);
      void queryClient.invalidateQueries({ queryKey: ["session", id] });
      void queryClient.invalidateQueries({ queryKey: ["session", id, "timeline"] });
      if (activeThread?.id) {
        void queryClient.invalidateQueries({ queryKey: queryKeys.sessions.threadTranscript(id, activeThread.id) });
      }
      toast.success("Continuing from the PR branch");
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to continue from the PR branch");
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
    const action: "create_pr" | "create_branch" | "push_changes" =
      resumeActionParam === "push_changes"
        ? "push_changes"
        : resumeActionParam === "create_branch"
          ? "create_branch"
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
      if (!pushAvailable || !hasSnapshot || isRunning || !builderReviewAllowsPR) return;
      resumeAttemptRef.current = resumePRParam;
      pushChangesMutation.mutate({ authorMode: "user", resumeToken: resumePRParam });
      return;
    }
    if (action === "create_branch") {
      if (!canCreateBranch) return;
      resumeAttemptRef.current = resumePRParam;
      createBranchMutation.mutate({ authorMode: "user", resumeToken: resumePRParam });
      return;
    }
    if (!canCreatePR) return;
    resumeAttemptRef.current = resumePRParam;
    createPRMutation.mutate({ authorMode: "user", resumeToken: resumePRParam });
  }, [builderReviewAllowsPR, canCreateBranch, canCreatePR, createBranchMutation, createPRMutation, hasPR, hasSnapshot, isRunning, prStatus, pushChangesMutation, resumeActionParam, resumePRParam, session?.has_unpushed_changes]);

  const diffStats = useMemo(() => {
    const stats = session?.diff_stats ?? sessionDiffPayload?.diff_stats;
    if (!stats) return null;
    return {
      added: stats.added,
      removed: stats.removed,
      filesChanged: stats.files_changed,
    };
  }, [session?.diff_stats, sessionDiffPayload?.diff_stats]);
  const diffStatsFileCount = diffStats?.filesChanged ?? 0;
  const diffLoadErrorText = isDiffError
    ? diffError instanceof ApiError
      ? diffError.message
      : "Changes could not be loaded. Retry to fetch the diff again."
    : undefined;
  const diffTruncationNotice = useMemo(() => {
    if (!sessionDiffPayload?.diff_truncated && !sessionDiffPayload?.diff_history_truncated) return undefined;
    if (sessionDiffPayload.diff_truncated) {
      const originalCharCount = sessionDiffPayload.diff_chars;
      const maxCharCount = sessionDiffPayload.diff_max_chars;
      const historyText = sessionDiffPayload.diff_history_truncated ? " Diff pass history may be omitted." : "";
      if (
        typeof originalCharCount === "number"
        && typeof maxCharCount === "number"
        && originalCharCount > maxCharCount
      ) {
        return {
          title: "Large diff truncated",
          text: `This diff is very large, so the viewer is showing the first ${maxCharCount.toLocaleString()} of ${originalCharCount.toLocaleString()} characters.${historyText}`,
        };
      }
      return {
        title: "Large diff truncated",
        text: `This diff is very large, so the viewer is showing a bounded preview.${historyText}`,
      };
    }
    if (sessionDiffPayload.diff_history_truncated) {
      return {
        title: "Diff pass history truncated",
        text: "Diff pass history is too large to load for this view, so only the current diff is shown.",
      };
    }
    return undefined;
  }, [sessionDiffPayload?.diff_chars, sessionDiffPayload?.diff_history_truncated, sessionDiffPayload?.diff_max_chars, sessionDiffPayload?.diff_truncated]);

  // --- Shared review state (lifted from old ChangesTab) ---

  // Hooks can't be called conditionally, so provide a stub when session hasn't loaded yet.
  // useDiffViewState only reads `diff` and `diff_history` — the stub satisfies that contract.
  const diffSource = useMemo(
    () => ({
      diff: sessionDiffPayload?.diff ?? null,
      diff_history: sessionDiffPayload?.diff_history ?? [],
    }) as unknown as Session,
    [sessionDiffPayload?.diff, sessionDiffPayload?.diff_history]
  );
  const diffViewState = useDiffViewState(diffSource);
  const {
    files: diffFiles,
    filteredFiles,
    passes,
    passRange,
    setPassRange,
    diffSearchQuery,
    setDiffSearchQuery,
  } = diffViewState;
  const emptyDiffRecoveryKeyRef = useRef<string | null>(null);
  const resetSessionDiffRecoveryState = useCallback(() => {
    emptyDiffRecoveryKeyRef.current = null;
  }, []);
  useSessionScopedReset(id, [
    { name: "session diff recovery state", reset: resetSessionDiffRecoveryState },
  ]);
  useEffect(() => {
    if (
      !shouldLoadDiff ||
      !sessionDiffPayload ||
      isDiffLoading ||
      isDiffFetching ||
      isDiffError ||
      diffStatsFileCount === 0 ||
      diffFiles.length > 0 ||
      !diffRevisionKey
    ) {
      return;
    }

    if (emptyDiffRecoveryKeyRef.current === diffRevisionKey) {
      return;
    }

    emptyDiffRecoveryKeyRef.current = diffRevisionKey;
    void refetchDiff();
  }, [
    diffFiles.length,
    diffRevisionKey,
    diffStatsFileCount,
    isDiffError,
    isDiffFetching,
    isDiffLoading,
    refetchDiff,
    sessionDiffPayload,
    shouldLoadDiff,
  ]);
  const isDiffDisplayLoading =
    isDiffLoading ||
    (isDiffFetching && diffStatsFileCount > 0 && diffFiles.length === 0);

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
  const [composerChangesetID, setComposerChangesetID] = useState<string | null>(null);
  const [composerIsUploading, setComposerIsUploading] = useState(false);
  const [composerUploadError, setComposerUploadError] = useState<string | null>(null);
  const [addThreadOpen, setAddThreadOpen] = useState(false);
  const [newThreadAgentType, setNewThreadAgentType] = useState("codex");
  const [newThreadModel, setNewThreadModel] = useState("");
  const [newThreadLabel, setNewThreadLabel] = useState("");
  const focusComposerAfterThreadCreateRef = useRef(false);
  const addTabButtonRef = useRef<HTMLButtonElement>(null);
  const composerTextareaRef = useRef<HTMLTextAreaElement>(null);
  const composerUploadInputRef = useRef<HTMLInputElement>(null);
  // Tracks an in-flight agent-switch PATCH so the send-time PATCH can wait
  // for it before issuing its own update. Without this, a fast send right
  // after toggling the agent dropdown can race the agent-switch PATCH and
  // overwrite the new agent_type with the send-time {label, model} body.
  const inFlightAgentUpdateRef = useRef<Promise<unknown> | null>(null);
  const chatPanelScrollToLiveEdgeRef = useRef<(() => void) | null>(null);
  const [chatPanelKeyboardControls, setChatPanelKeyboardControls] = useState<SessionTranscriptKeyboardControls | null>(null);
  const resetSessionComposerState = useCallback(() => {
    setActiveCommentLine(null);
    setComposerMessage("");
    setComposerPlanMode(false);
    setComposerSelectedModel("");
    setComposerAttachments([]);
    setComposerReferences([]);
    setComposerCommands([]);
    setComposerChangesetID(null);
    setComposerIsUploading(false);
    setComposerUploadError(null);
    setAddThreadOpen(false);
    setNewThreadAgentType("codex");
    setNewThreadModel("");
    setNewThreadLabel("");
    focusComposerAfterThreadCreateRef.current = false;
    inFlightAgentUpdateRef.current = null;
    chatPanelScrollToLiveEdgeRef.current = null;
    setChatPanelKeyboardControls(null);
  }, []);
  useSessionScopedReset(id, [
    { name: "session composer state", reset: resetSessionComposerState },
  ]);
  // Open comments are the source of truth for what gets attached to the next
  // message — once a send succeeds, the backend marks them resolved in the
  // same transaction, the comments query is invalidated below, and the next
  // refetch flips them out of openComments. No local "dismissed" state is
  // needed (and would be wrong: it wouldn't survive page reloads).
  const attachedReviewComments = useMemo(
    () => comments.filter((comment) => !comment.resolved).slice(0, MAX_RESOLVE_REVIEW_COMMENTS_PER_MESSAGE),
    [comments],
  );
  const isRestoringActiveThread = threads.length > 0 && activeThread === null;
  // Composer gating: messages may be sent at any point while the session or
  // thread is running. The backend queues mid-turn sends and the orchestrator
  // drains the queue once the in-flight turn completes. Pending/skipped at
  // the session level and a destroyed sandbox still block — those are
  // genuinely unrecoverable, not just busy.
  const composerCanSendMessage = !isRestoringActiveThread &&
    session?.status !== "skipped" &&
    session?.status !== "pending" &&
    session?.sandbox_state !== "destroyed";
  const composerUnavailableReason = isRestoringActiveThread ? "Thread is still loading." : undefined;
  const composerPlaceholderOverride = isRestoringActiveThread ? "Loading thread..." : undefined;
  const composerIsRunning = activeThread ? activeThread.status === "running" : session?.status === "running";
  const runtimeRecoveryActive = session ? isRuntimeRecoveryActive(session) : false;
  const localStopRequested = sessionStopRequest?.sessionId === id && composerIsRunning;
  const threadStopRequested = !!activeThread?.cancel_requested_at && workingStatusesSet.has(activeThread.status);
  const isStopRequested = localStopRequested || threadStopRequested;
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
  const selectedNewThreadAgent = AGENTS_BY_KEY[newThreadAgentType] ?? AGENTS[0];
  const selectedNewThreadModels = selectedNewThreadAgent?.models ?? [];
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
  const buildDialogThreadRequest = useCallback(() => {
    const agent = AGENTS_BY_KEY[newThreadAgentType] ?? AGENTS[0];
    return {
      agent_type: agent.key,
      model: newThreadModel || undefined,
      label: newThreadLabel.trim() || `${agent.label} ${threads.length + 1}`,
    };
  }, [newThreadAgentType, newThreadLabel, newThreadModel, threads.length]);
  const pendingEditableThreadUpdate = useMemo(() => {
    return getPendingEditableThreadUpdate(activeThread ?? undefined, activeThreadIsEditable, composerSelectedModel);
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
        return {
          response: {
            ...response,
            data: response.data.message,
          },
          resolvedIDs: vars.resolvedIDs,
        };
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
      setComposerChangesetID(null);
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
        // Inject the confirmed message into the newest transcript page so the
        // optimistic copy can be dropped without a gap; the next /transcript
        // refetch (triggered by SSE/invalidations) supersedes this patch.
        queryClient.setQueriesData<TranscriptWindowInfiniteData>(
          { queryKey: queryKeys.sessions.threadTranscript(id, vars.activeThreadId) },
          (previous) => appendMessageToTranscriptCache(previous, response.data, activeThread?.status ?? "idle"),
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
      invalidateSessionHumanInputRequests(queryClient, id);
      if (vars.activeThreadId) {
        queryClient.invalidateQueries({ queryKey: queryKeys.sessions.threadTranscript(id, vars.activeThreadId) });
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
      setComposerChangesetID(context.composerSnapshot.changesetId ?? null);
    },
  });

  const queueSend = useCallback((opts: { planMode?: boolean; overrideMessage?: string } = {}) => {
    if (!session) return;
    if (threads.length > 0 && !activeThread) return;

    const draftMessage = opts.overrideMessage ?? composerMessage;
    const userFacingMessage = attachedReviewComments.length > 0
      ? formatReviewMessage(attachedReviewComments, filteredFiles, draftMessage)
      : draftMessage;
    const isPlanRequest = opts.planMode ?? composerPlanMode;
    const formattedMessage = applyPlanModePrefix(userFacingMessage, isPlanRequest);
    const resolvedIDs = attachedReviewComments.map((comment) => comment.id);
    setSessionStopOutcome(null);
    const optimisticID = optimisticMessageIDRef.current;
    optimisticMessageIDRef.current -= 1;
    const clientMessageID =
      typeof crypto !== "undefined" && "randomUUID" in crypto
        ? crypto.randomUUID()
        : `${id}:${activeThread?.id ?? "session"}:${Date.now()}:${Math.random()}`;

    sendMutation.mutate({
      activeThreadId: activeThread?.id,
      body: {
        message: userFacingMessage,
        changesetId: composerChangesetID ?? undefined,
        clientMessageID,
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
        changesetId: composerChangesetID ?? undefined,
      },
    });
  }, [
    activeThread,
    attachedReviewComments,
    composerAttachments,
    composerCommands,
	composerChangesetID,
    composerMessage,
    composerPlanMode,
    composerReferences,
    composerSelectedModel,
    filteredFiles,
    id,
    pendingEditableThreadUpdate,
    session,
    sendMutation,
    threads.length,
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
    onMutate: () => {
      setSessionStopRequest({ sessionId: id, requestedAt: new Date().toISOString() });
      setSessionStopOutcome(null);
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["session", id] });
    },
    onError: (error) => {
      setSessionStopRequest(null);
      toast.error(error instanceof ApiError ? error.message : "Failed to stop session");
    },
  });
  const cancelSession = cancelMutation.mutate;
  const handleCancelSession = useCallback(() => {
    cancelSession();
  }, [cancelSession]);
  const stopAutoRepairMutation = useMutation({
    mutationFn: async ({ sessionId, threadId }: { sessionId: string; threadId?: string }) => {
      if (threadId) {
        await api.sessions.cancelThread(sessionId, threadId, { reason: "auto_repair_stop" });
        return;
      }
      await api.sessions.cancelSession(sessionId, { reason: "auto_repair_stop" });
    },
    onMutate: ({ sessionId }) => {
      if (sessionId === id) {
        setSessionStopRequest({ sessionId: id, requestedAt: new Date().toISOString() });
        setSessionStopOutcome(null);
      }
    },
    onSuccess: (_response, { sessionId }) => {
      void queryClient.invalidateQueries({ queryKey: ["session", sessionId] });
      void queryClient.invalidateQueries({ queryKey: ["session", sessionId, "timeline"] });
      if (pullRequestId) {
        void queryClient.invalidateQueries({ queryKey: ["pull-request", pullRequestId, "health"] });
      }
      toast.info("Auto-repair stop requested");
    },
    onError: (error, { sessionId }) => {
      if (sessionId === id) {
        setSessionStopRequest(null);
      }
      toast.error(error instanceof ApiError ? error.message : "Failed to stop auto-repair");
    },
  });
  const handleComposerSend = useCallback(() => {
    queueSend();
  }, [queueSend]);

  const archiveThreadMutation = useMutation({
    mutationFn: (threadId: string) => api.sessions.archiveThread(id, threadId),
    onSuccess: (response, archivedThreadID) => {
      queryClient.setQueryData<SingleResponse<SessionDetail>>(["session", id], (existing) => {
        if (!existing) return existing;
        const existingThreads = existing.data.threads ?? [];
        return {
          ...existing,
          data: {
            ...existing.data,
            threads: existingThreads.filter((thread) => thread.id !== archivedThreadID),
          },
        };
      });
      if (activeThreadId === archivedThreadID) {
        const fallback = threads.find((thread) => thread.id !== archivedThreadID);
        setActiveThreadId(fallback?.id ?? null);
      }
      queryClient.invalidateQueries({ queryKey: ["session", id] });
      toast.success(`Closed ${response.data.label}`);
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to close tab");
    },
  });
  const revertThreadMutation = useMutation({
    mutationFn: (threadId: string) => api.sessions.revertThread(id, threadId),
    onSuccess: (_data, threadId) => {
      toast.success("Revert prepared — see the tab transcript for the patch");
      queryClient.invalidateQueries({ queryKey: queryKeys.sessions.threadTranscript(id, threadId) });
      queryClient.invalidateQueries({ queryKey: ["session", id] });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to prepare revert");
    },
  });

  // Session-wide file event timeline powers tab-strip overlap badges. Polled
  // at the same cadence as the session detail so a user-perceptible "tab
  // touched a file" lands within one polling cycle.
  //
  // Polling is incremental: the first request fetches the whole timeline,
  // subsequent requests pass `?since=<latest observed_at>` so a long session
  // does not retransfer hundreds of events every 5 seconds. The accumulated
  // list lives in component state because React Query caches only the most
  // recent response, which is now a delta.
  const fileEventsSinceRef = useRef<string | undefined>(undefined);
  const [accumulatedFileEvents, setAccumulatedFileEvents] = useState<SessionThreadFileEvent[]>([]);
  const resetSessionFileEventState = useCallback(() => {
    fileEventsSinceRef.current = undefined;
    setAccumulatedFileEvents([]);
  }, []);
  useSessionScopedReset(id, [
    { name: "session file event state", reset: resetSessionFileEventState },
  ]);
  const fileEventsQuery = useQuery({
    queryKey: queryKeys.sessions.threadFileEvents(id),
    queryFn: () => api.sessions.listThreadFileEvents(id, fileEventsSinceRef.current),
    enabled: threads.length > 0,
    refetchInterval: threads.some((t) => t.status === "running" || t.status === "pending") ? pollMs(5000) : false,
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
    () => computeThreadOverlap(chromeThreads, accumulatedFileEvents),
    [chromeThreads, accumulatedFileEvents],
  );
  const visibleDiffFiles = diffFiles;
  const visibleFilteredFiles = filteredFiles;

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
    onMutate: (body) => {
      setPendingThreadPreview({
        id: "__pending-thread__",
        session_id: id,
        org_id: session?.org_id ?? "",
        agent_type: body.agent_type as SessionThread["agent_type"],
        label: body.label,
        status: "pending",
        current_turn: 0,
        created_at: new Date().toISOString(),
        cost_cents: 0,
        pending_message_count: 0,
        model_override: body.model,
      });
    },
    onSuccess: (response) => {
      setPendingThreadPreview(null);
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
      focusComposerAfterThreadCreateRef.current = true;
      setAddThreadOpen(false);
      setNewThreadLabel("");
      setNewThreadModel("");
      setActiveThreadId(response.data.id);
      setComposerSelectedModel(getInitialComposerSelectedModel(response.data));
    },
    onError: (err) => {
      setPendingThreadPreview(null);
      queryClient.invalidateQueries({ queryKey: ["session", id] });
      toast.error(err instanceof Error ? err.message : "Failed to create tab");
    },
  });

  useEffect(() => {
    if (!focusComposerAfterThreadCreateRef.current) {
      return;
    }

    const rafID = window.requestAnimationFrame(() => {
      focusComposerAfterThreadCreateRef.current = false;
      const shouldFocusComposer = session?.agent_type !== "pm_agent"
        && composerCanSendMessage
        && composerTextareaRef.current !== null
        && !composerTextareaRef.current.disabled;

      if (shouldFocusComposer) {
        composerTextareaRef.current?.focus();
        return;
      }

      addTabButtonRef.current?.focus();
    });

    return () => window.cancelAnimationFrame(rafID);
  }, [activeThread?.id, composerCanSendMessage, session?.agent_type]);

  const handleCreateThread = useCallback(() => {
    createThreadMutation.mutate(buildDefaultThreadRequest());
  }, [buildDefaultThreadRequest, createThreadMutation]);

  useEffect(() => {
    setNewThreadModel("");
  }, [newThreadAgentType]);

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
  const registerChatPanelKeyboardControls = useCallback((controls: SessionTranscriptKeyboardControls | null) => {
    setChatPanelKeyboardControls(controls);
  }, []);

  const changesCount = diffStats?.filesChanged;
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
  ]);
  const isDedicatedMobileReview = centerMode === "review" && isMobileReviewViewport;

  useEffect(() => {
    if (!isDedicatedMobileReview) {
      setMobileReviewComposerOpen(false);
    }
  }, [isDedicatedMobileReview]);

  const focusActiveDetailTab = useCallback(() => {
    requestAnimationFrame(() => {
      detailTabsRef.current?.querySelector<HTMLElement>('[role="tab"][data-state="active"]')?.focus();
    });
  }, []);

  const toggleDetailsFromKeyboard = useCallback(() => {
    if (centerMode === "review" && showDetailPanel) {
      return;
    }
    if (isMobileReviewViewport) {
      setMobileDetailOpen((open) => !open);
      focusActiveDetailTab();
      return;
    }
    setShowDetailPanel((open) => !open);
    if (!showDetailPanel) {
      focusActiveDetailTab();
    }
  }, [centerMode, focusActiveDetailTab, isMobileReviewViewport, showDetailPanel]);

  const closeDetailsFromKeyboard = useCallback(() => {
    if (isMobileReviewViewport) {
      setMobileDetailOpen(false);
      return;
    }
    if (!(centerMode === "review" && showDetailPanel)) {
      setShowDetailPanel(false);
    }
  }, [centerMode, isMobileReviewViewport, showDetailPanel]);

  const focusComposerFromKeyboard = useCallback(() => {
    composerTextareaRef.current?.focus();
    composerTextareaRef.current?.scrollIntoView({ block: "nearest" });
  }, []);

  const ghBlocked = ghStatus?.pr_authorship_mode === "user_required" && !ghStatus?.connected;
  const pushBranchDiverged =
    session?.pr_push_error_code === "branch_diverged" ||
    localPushActionError?.code === "PR_BRANCH_DIVERGED";

  const createPRFromKeyboard = useCallback(() => {
    if (localPRState !== "idle" || createPRMutation.isPending) {
      return;
    }
    if (ghBlocked) {
      setPRAuthPrompt({ purpose: "create_pr" });
      return;
    }
    submitCreatePR(undefined);
  }, [createPRMutation.isPending, ghBlocked, localPRState, submitCreatePR]);

  const createBranch = useCallback(() => {
    if (localBranchState !== "idle" || createBranchMutation.isPending || !canCreateBranch) {
      return;
    }
    if (ghBlocked) {
      setPRAuthPrompt({ purpose: "create_branch" });
      return;
    }
    createBranchMutation.mutate(undefined);
  }, [canCreateBranch, createBranchMutation, ghBlocked, localBranchState]);

  const createPRWithAutoMerge = useCallback(() => {
    if (localPRState !== "idle" || createPRMutation.isPending || !canCreatePR) {
      return;
    }
    if (ghBlocked) {
      setPRAuthPrompt({ purpose: "create_pr", mergeWhenReady: true });
      return;
    }
    submitCreatePR({ mergeWhenReady: true });
  }, [canCreatePR, createPRMutation.isPending, ghBlocked, localPRState, submitCreatePR]);

  const pushChangesFromKeyboard = useCallback(() => {
    if (localPushState !== "idle" || pushChangesMutation.isPending || continueFromPRBranchMutation.isPending) {
      return;
    }
    if (pushBranchDiverged) {
      continueFromPRBranchMutation.mutate();
      return;
    }
    if (ghBlocked) {
      setPRAuthPrompt({ purpose: "push_changes" });
      return;
    }
    pushChangesMutation.mutate(undefined);
  }, [continueFromPRBranchMutation, ghBlocked, localPushState, pushBranchDiverged, pushChangesMutation]);

  const viewPRFromKeyboard = useCallback(() => {
    if (!selectedPR?.github_pr_url) {
      return;
    }
    window.open(selectedPR.github_pr_url, "_blank", "noopener,noreferrer");
  }, [selectedPR?.github_pr_url]);

  useSessionKeyboardShortcuts({
    enabled: !isLoading && !!session,
    reviewMode: centerMode === "review",
    onShowHelp: () => setKeyboardHelpOpen(true),
    onFocusComposer: focusComposerFromKeyboard,
    transcript: chatPanelKeyboardControls,
    agentTabs: threads.length > 0 && activeThreadIndex >= 0 ? {
      activeIndex: activeThreadIndex,
      count: threads.length,
      onChange: (index) => {
        const next = threads[index];
        if (next) {
          setActiveThreadId(next.id);
          chatPanelKeyboardControls?.focus();
        }
      },
      onAdd: () => {
        if (!createThreadMutation.isPending) {
          createThreadMutation.mutate(buildDefaultThreadRequest());
        }
      },
    } : null,
    details: {
      open: isMobileReviewViewport ? mobileDetailOpen : showDetailPanel,
      required: centerMode === "review" && showDetailPanel,
      activeTab: detailTab,
      availableTabs: ["overview", "changes", "preview"] as const,
      onToggle: toggleDetailsFromKeyboard,
      onClose: closeDetailsFromKeyboard,
      onTabChange: handleDetailTabClick,
      onOpenReview: () => {
        if (visibleDiffFiles.length > 0) {
          openReview();
        }
      },
      onExitReview: exitReview,
    },
    pr: {
      canCreate: canCreatePR && localPRState === "idle" && !createPRMutation.isPending,
      canView: !!selectedPR?.github_pr_url,
      canPush: selectedIsPrimary && !prHealthActionsBlocked && canShipPR && builderReviewAllowsPR && hasPR && prStatus === "open" && !!session?.has_unpushed_changes && hasSnapshot && !isRunning && localPushState === "idle" && !pushChangesMutation.isPending && !continueFromPRBranchMutation.isPending,
      canFixTests: !prHealthActionsBlocked && canManagePR && hasRepairableFailedChecks(prHealth) && pendingPRAction === null,
      canResolveConflicts: !prHealthActionsBlocked && canManagePR && !!prHealth?.can_resolve_conflicts && pendingPRAction === null,
      canMerge: !prHealthActionsBlocked && canManagePR && prHealthAllowsMerge(prHealth) && pendingPRAction === null,
      onCreate: createPRFromKeyboard,
      onView: viewPRFromKeyboard,
      onPush: pushChangesFromKeyboard,
      onFixTests: () => startRepairMutation.mutate({ action: "fix_tests", pushChanges: true }),
      onResolveConflicts: () => startRepairMutation.mutate({ action: "resolve_conflicts", pushChanges: true }),
      onMerge: handleMergeAction,
    },
  });

  if (isLoading || (isProvisionalSession && !error)) {
    // Metadata-first paint: the provisional row seeded by the sidebar (or a
    // partially settled payload) already carries the title, status, and
    // agent. Show those immediately and confine the shimmer to the parts we
    // genuinely don't have yet, so opening a session never hides data the
    // client already holds.
    const provisionalStatus = rawSession ? getDisplayStatus(rawSession.status) : null;
    return (
      <SessionDetailLoadingSkeleton
        metadata={
          rawSession && provisionalStatus
            ? {
                title: sessionTitle(rawSession),
                statusLabel: provisionalStatus.label,
                statusColor: provisionalStatus.color,
                agentType: rawSession.agent_type,
              }
            : null
        }
      />
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

  const status = getDisplayStatus(session.status, prStatus);
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
  const prActionError = hasPR
    ? null
    : (localPRActionError?.code && snapshotState ? snapshotMessage : localPRActionError?.message) ||
      (snapshotUnavailable ? snapshotMessage : null) ||
      (prState === "failed" ? session.pr_creation_error || PR_ERROR_TOAST_MESSAGE : null);
  const createPRAction = deriveCreatePRActionState({
    canShipPR,
    hasPR,
    hasSessionChanges,
    hasSnapshot,
    isRunning,
    builderReviewAllowsPR: createPRAllowsSubmission,
    snapshotUnavailable,
    snapshotMessage,
    ghBlocked,
    queueingPR,
    creatingPR,
    finalizingPR,
    prState,
    prCreationError: session.pr_creation_error,
    localError: snapshotUnavailable ? undefined : localPRActionError?.message,
    hasRecoverableError: Boolean(prActionError),
  });
  const showPRAction = createPRAction.visible;
  const prActionLabel = createPRAction.label;
  const prActionSpinning = createPRAction.spinning;
  const prActionDisabled = createPRAction.disabled;
  const prActionTitle = createPRAction.disabledReason;

  const branchState = session.branch_creation_state;
  const queueingBranch = localBranchState === "submitting";
  const creatingBranch =
    (localBranchState === "queued" && branchState !== "failed" && branchState !== "succeeded") ||
    branchState === "queued" ||
    branchState === "pushing";
  const branchActionDisabled = !canCreateBranch || queueingBranch || creatingBranch || createBranchMutation.isPending;
  const branchActionLabel = queueingBranch
    ? "Queueing branch..."
    : creatingBranch
      ? "Creating branch..."
      : branchState === "failed" || localBranchActionError
        ? "Retry branch"
        : "Create branch";
  const branchActionTitle = !canCreateBranch && !queueingBranch && !creatingBranch
    ? "Run readiness checks successfully before creating a branch"
    : localBranchActionError?.message ||
    (branchState === "failed" ? session.branch_creation_error || "Branch creation failed" : undefined);
  const branchURL = !hasPR && branchState === "succeeded" ? session.branch_url : undefined;

  // Push-changes button derived state. Mirrors the PR creation block above
  // but operates on session.pr_push_state and the backend-derived
  // has_unpushed_changes signal. Rendered inside the PR health banner
  // alongside Resolve conflicts / Fix tests / Merge so all PR-level
  // actions live in one place, while still hiding Push changes when the
  // latest session head already matches the remote PR branch.
  const pushState = session.pr_push_state;
  const queueingPush = localPushState === "submitting";
  const pushingChanges =
    (localPushState === "queued" && pushState !== "failed" && pushState !== "succeeded") ||
    pushState === "queued" ||
    pushState === "pushing";
  const pushAction = derivePushChangesActionState({
    canShipPR,
    hasOpenPR: hasPR && prStatus === "open",
    hasUnpushedChanges: selectedChangeset?.worktree_path ? !!selectedChangeset.has_unpushed_changes : !!session.has_unpushed_changes,
    hasSnapshot: selectedChangeset?.worktree_path ? true : hasSnapshot,
    isRunning,
    builderReviewAllowsPR,
    snapshotUnavailable,
    snapshotMessage,
    ghBlocked,
    prHealthBlocked: prHealthActionsBlocked,
    queueingPush,
    pushingChanges,
    pushState,
    pushError: session.pr_push_error,
    pushErrorCode: session.pr_push_error_code,
    localError: localPushActionError?.message,
    localErrorCode: localPushActionError?.code,
  });
  const showPushAction = pushAction.visible;
  const pushActionLabel = pushAction.label;
  const pushActionSpinning = pushAction.spinning;
  const pushActionDisabled = pushAction.disabled || continueFromPRBranchMutation.isPending;
  const pushActionTitle = pushAction.disabledReason;
  const pushActionRequiresBranchSync = !!pushAction.requiresBranchSync;

  function handleMergeAction() {
    if (ghBlocked) {
      setPRAuthPrompt({ purpose: "merge_pr" });
      return;
    }
    mergeMutation.mutate();
  }

  function handleQueueMergeWhenReady() {
    if (ghBlocked) {
      setPRAuthPrompt({ purpose: "merge_pr" });
      return;
    }
    mergeWhenReadyMutation.mutate("queue");
  }

  function handleCancelMergeWhenReady() {
    mergeWhenReadyMutation.mutate("cancel");
  }

  const prErrorNotice = prActionError ? {
    title: prErrorTitle(snapshotState, localPRActionError?.code),
    description: prActionError,
    action: prActionDisabled ? undefined : {
      label: prActionLabel,
      onClick: () => submitCreatePR(undefined),
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
  const openAddThreadDialog = () => {
    setNewThreadAgentType(session.agent_type || "codex");
    setNewThreadModel("");
    setNewThreadLabel("");
    setAddThreadOpen(true);
  };
  const openMobileRenameDialog = () => {
    setDraftTitle(currentTitle);
    setMobileRenameOpen(true);
  };
  // Right-panel content. Rendered inline on desktop and inside a bottom sheet
  // on mobile — the same JSX in both places so tab state stays consistent.
  const panelTabsEl = (
    <Tabs
      value={detailTab}
      onValueChange={(v) => handleDetailTabClick(v as DetailTab)}
      className="flex flex-col flex-1 min-h-0 gap-0"
    >
      <div
        data-testid="session-detail-header"
        className={cn(
          "border-b border-border shrink-0",
          prErrorNotice ? "min-h-14" : SESSION_HEADER_HEIGHT_CLASSNAME,
        )}
      >
        <div
          data-testid="session-detail-header-bar"
          className={cn("flex items-center gap-2 min-w-0 px-2", SESSION_HEADER_HEIGHT_CLASSNAME)}
        >
          <div
            ref={detailTabsRef}
            aria-label="Session detail tabs"
            className={cn(
              "flex h-full min-w-0 flex-1 items-center overflow-x-auto overflow-y-hidden scrollbar-hide",
              detailTabsOverflow && "mask-fade-r",
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
              <TabsTrigger value="preview">Preview</TabsTrigger>
            </TabsList>
          </div>
          <div aria-label="Session detail actions" className="flex items-center justify-end gap-2 shrink-0 pl-2">
            {hasPR && selectedPR?.github_pr_url ? (
              <>
                {prStatus === "closed" && (
                  <Badge variant="secondary" className="h-7 px-2 text-xs">
                    {closedPRLabel}
                  </Badge>
                )}
                <Button asChild variant="outline" size="xs" className="gap-1.5" title="View PR (p v)">
                  <a href={selectedPR.github_pr_url} target="_blank" rel="noopener noreferrer">
                    <ExternalLink className="h-3 w-3" />
                    View PR
                  </a>
                </Button>
              </>
            ) : showPRAction && !prErrorNotice ? (
              <>
                {branchURL ? (
                  <Button asChild variant="outline" size="xs" className="gap-1.5" title="View branch">
                    <a href={branchURL} target="_blank" rel="noopener noreferrer">
                      <GitBranch className="h-3 w-3" />
                      View branch
                    </a>
                  </Button>
                ) : null}
                <DisabledTooltip disabled={prActionDisabled} content={prActionTitle}>
                  <ButtonGroup size="xs">
                    <Button
                      variant="outline"
                      size="xs"
                      className="rounded-r-none border-r-0 text-xs gap-1.5"
                      loading={prActionSpinning}
                      disabled={prActionDisabled}
                      title={prActionTitle ? `${prActionTitle} (p c)` : `${prActionLabel} (p c)`}
                      onClick={() => submitCreatePR(undefined)}
                    >
                      {!prActionSpinning && (prState === "failed" || localPRActionError ? (
                        <AlertTriangle className="h-3 w-3" />
                      ) : (
                        <GitPullRequest className="h-3 w-3" />
                      ))}
                      {prActionLabel}
                    </Button>
                    <DropdownMenu>
                      <DropdownMenuTrigger asChild>
                        <Button
                          variant="outline"
                          size="icon-xs"
                          className="rounded-l-none"
                          disabled={prActionDisabled}
                          aria-label="More publish actions"
                          title="More publish actions"
                        >
                          <ChevronDown className="h-3 w-3" />
                        </Button>
                      </DropdownMenuTrigger>
                      <DropdownMenuContent align="end">
                        <DropdownMenuItem
                          className="text-xs"
                          onClick={createBranch}
                          disabled={branchActionDisabled}
                          title={branchActionTitle}
                        >
                          {queueingBranch || creatingBranch ? (
                            <Loader2 className="h-3.5 w-3.5 animate-spin" />
                          ) : (
                            <GitBranch className="h-3.5 w-3.5" />
                          )}
                          {branchActionLabel}
                        </DropdownMenuItem>
                        <DropdownMenuItem
                          className="text-xs"
                          onClick={createPRWithAutoMerge}
                          disabled={prActionDisabled || createPRMutation.isPending}
                          title={prActionTitle}
                        >
                          {prActionSpinning ? (
                            <Loader2 className="h-3.5 w-3.5 animate-spin" />
                          ) : (
                            <GitPullRequest className="h-3.5 w-3.5" />
                          )}
                          Create PR and enable auto-merge
                        </DropdownMenuItem>
                      </DropdownMenuContent>
                    </DropdownMenu>
                  </ButtonGroup>
                </DisabledTooltip>
              </>
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
            !selectedIsPrimary
              ? "Changes for this pull request will be available after its branch is materialized."
              : isDiffDisplayLoading
              ? "Loading changes..."
              : session.status === "running" || session.status === "pending"
              ? "Changes will appear here as the agent modifies files."
              : "This session did not produce any file changes."
          }
          isMobile={isMobileReviewViewport}
          diffLoadErrorText={diffLoadErrorText}
          diffTruncationNotice={diffTruncationNotice}
          onRetryDiffLoad={retryDiffLoad}
        />
      </TabsContent>
      <TabsContent value="overview" className="flex-1 overflow-y-auto scrollbar-hide p-4">
        <div className="space-y-4">
          <PullRequestList
            changesets={changesets}
            selectedID={selectedChangeset?.id ?? ""}
            onSelect={(changesetID) => {
              setSelectedChangesetID(changesetID);
              void setChangesetParam(changesetID);
            }}
          />
          {hasMultipleChangesets && (
            <Card className="border-border/60" data-testid="stack-health">
              <CardContent className="flex items-center justify-between gap-3 p-4">
                <div>
                  <div className="text-sm font-medium">Stack health</div>
                  <p className="text-xs text-muted-foreground">{(session.changeset_stack_state ?? "coherent").replaceAll("-", " ")}</p>
                </div>
                {selectedChangeset && changesets.some((item) => item.status === "needs_restack") && (
                  <Button size="sm" variant="outline" disabled={changesetLifecycleMutation.isPending} onClick={() => changesetLifecycleMutation.mutate(() => api.sessions.restackChangesetDescendants(id, selectedChangeset.id))}>
                    Restack descendants
                  </Button>
                )}
              </CardContent>
            </Card>
          )}
          <ChangesetSplitPlanner
            sessionID={id}
            changesets={changesets}
            additions={session.diff_stats?.added}
          />
          {hasMultipleChangesets && selectedChangeset && (
            <Card className="border-border/60" data-testid="selected-pull-request-panel">
              <CardContent className="space-y-1 p-4">
                <div className="text-sm font-medium">{selectedChangeset.title}</div>
                {selectedChangeset.summary && <p className="text-xs text-muted-foreground">{selectedChangeset.summary}</p>}
                <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 pt-1 text-xs">
                  <dt className="text-muted-foreground">Base</dt><dd className="truncate">{selectedChangeset.base_branch}</dd>
                  <dt className="text-muted-foreground">Head</dt><dd className="truncate">{selectedChangeset.working_branch ?? "Not materialized"}</dd>
                  <dt className="text-muted-foreground">Target</dt><dd className="truncate">{selectedChangeset.target_branch}</dd>
                  <dt className="text-muted-foreground">State</dt><dd>{selectedChangeset.pull_request?.status ?? selectedChangeset.status.replaceAll("_", " ")}</dd>
                  {selectedChangeset.pull_request?.ci_status && <><dt className="text-muted-foreground">CI</dt><dd>{selectedChangeset.pull_request.ci_status}</dd></>}
                  {selectedChangeset.pull_request?.review_status && <><dt className="text-muted-foreground">Review</dt><dd>{selectedChangeset.pull_request.review_status.replaceAll("_", " ")}</dd></>}
                </dl>
                {selectedChangeset.restack_delta_kind ? (
                  <Card className="bg-muted/40">
                    <CardContent className="p-3 text-xs">
                    <p className="font-medium text-foreground">
                      Restack delta: {selectedChangeset.restack_delta_kind.replaceAll("_", " ")}
                    </p>
                    {selectedChangeset.restack_delta_summary ? (
                      <p className="mt-1 text-muted-foreground">{selectedChangeset.restack_delta_summary}</p>
                    ) : null}
                    {selectedChangeset.restack_confirmation_required ? (
                      <Button className="mt-2" size="sm" variant="outline" disabled={changesetLifecycleMutation.isPending} onClick={() => changesetLifecycleMutation.mutate(() => api.sessions.confirmChangesetRestack(id, selectedChangeset.id))}>
                        Confirm restack delta
                      </Button>
                    ) : null}
                    </CardContent>
                  </Card>
                ) : null}
                <div className="flex flex-wrap gap-2 pt-2">
                {!selectedChangeset.pull_request && (
                  <DisabledTooltip disabled={!!selectedChangeset.worktree_path} content="Create PR becomes available after branch materialization">
                    <Button type="button" size="sm" disabled={!selectedChangeset.worktree_path || changesetLifecycleMutation.isPending} onClick={() => changesetLifecycleMutation.mutate(() => api.sessions.publishChangeset(id, selectedChangeset.id))}>
                      <GitPullRequest className="h-3.5 w-3.5" />
                      Create PR
                    </Button>
                  </DisabledTooltip>
                )}
                <Button type="button" size="sm" variant="outline" disabled={!selectedChangeset.worktree_path} onClick={() => {
                  setComposerChangesetID(selectedChangeset.id);
                  if (selectedChangeset.status === "restack_conflict") {
                    setComposerMessage("Resolve the restack conflict while preserving this pull request's intent. Explain any semantic changes and do not push; I will review and confirm the result.");
                  } else if (selectedChangeset.status === "external_update_detected") {
                    setComposerMessage("Run `143-tools changesets import-remote --changeset " + selectedChangeset.id + "`, fetch this pull request's remote branch, and reconcile its remote commits with the local worktree without dropping either side's intended changes. Do not push; I will review and confirm the result.");
                  }
                  focusComposerFromKeyboard();
                }}>
                  {selectedChangeset.status === "restack_conflict"
                    ? "Resolve with agent"
                    : selectedChangeset.status === "external_update_detected"
                      ? "Reconcile with agent"
                      : "Ask agent"}
                </Button>
                {hasMultipleChangesets && <Button type="button" size="sm" variant="outline" disabled={changesetLifecycleMutation.isPending || changesets.some((item) => item.status !== "abandoned" && !item.worktree_path)} onClick={() => changesetLifecycleMutation.mutate(() => api.sessions.publishChangesetStack(id))}>Publish stack</Button>}
                </div>
                {!selectedChangeset.is_primary && !selectedChangeset.worktree_path && (
                  <p className="pt-2 text-xs text-muted-foreground" data-testid="branch-actions-unavailable">
                    Changes, preview, readiness, review, publishing, and agent editing become available after branch materialization.
                  </p>
                )}
              </CardContent>
            </Card>
          )}
          {pullRequestId && prStatus === "open" && (
            prHealth ? (
              <PRHealthBanner
                health={prHealth}
                currentSessionId={id}
                currentThreadId={activeThread?.id ?? null}
                pendingAction={pendingPRAction}
                repairError={repairActionError}
                mergeAuthRequired={ghBlocked}
                mergeWhenReadyPending={pendingMergeWhenReady}
                onFixTests={() => startRepairMutation.mutate({ action: "fix_tests", pushChanges: true })}
                onFixTestsWithoutPushing={() => startRepairMutation.mutate({ action: "fix_tests", pushChanges: false })}
                onResolveConflicts={() => startRepairMutation.mutate({ action: "resolve_conflicts", pushChanges: true })}
                onResolveConflictsWithoutPushing={() => startRepairMutation.mutate({ action: "resolve_conflicts", pushChanges: false })}
                onMerge={handleMergeAction}
                onQueueMergeWhenReady={handleQueueMergeWhenReady}
                onCancelMergeWhenReady={handleCancelMergeWhenReady}
                onOpenRepairSession={(sessionId, threadId) => {
                  if (sessionId === id && threadId) {
                    setActiveThreadId(threadId);
                    return;
                  }
                  router.push(`/sessions/${sessionId}`);
                }}
                onStopAutoRepair={(sessionId, threadId) => stopAutoRepairMutation.mutate({ sessionId, threadId })}
                stopAutoRepairPending={stopAutoRepairMutation.isPending}
                reviewAction={selectedIsPrimary && canManageSession && canUseNativeReviewLoop ? {
                  disabled: reviewActionDisabled,
                  spinning: startReviewLoopMutation.isPending || reviewLoopRunning,
                  title: reviewActionDisabledReason,
                  onClick: () => setReviewConfigOpen(true),
                } : undefined}
                pushChanges={showPushAction ? {
                  label: pushActionLabel,
                  disabled: pushActionDisabled,
                  spinning: pushActionSpinning || (pushActionRequiresBranchSync && continueFromPRBranchMutation.isPending),
                  showError: pushState === "failed" || !!localPushActionError,
                  title: pushActionTitle,
                  onClick: () => {
                    if (pushActionRequiresBranchSync) {
                      continueFromPRBranchMutation.mutate();
                      return;
                    }
                    pushChangesMutation.mutate(undefined);
                  },
                } : undefined}
              />
            ) : isPRHealthLoading ? (
              <Card className="border-border/60">
                <CardContent className="flex items-center gap-2 p-4 text-sm text-muted-foreground">
                  <Loader2 className="h-4 w-4 animate-spin" />
                  <span>Loading PR health...</span>
                </CardContent>
              </Card>
            ) : null
          )}
          {selectedIsPrimary && canManageSession && !hasPR && hasSessionChanges ? (
            <Card className="border-border/60">
              <CardContent className="space-y-3 p-4">
                <div className="flex flex-col gap-3">
                  <div className="flex min-w-0 flex-1 items-start gap-2">
                    <div className="flex h-8 w-8 shrink-0 items-center justify-center rounded-full bg-muted text-muted-foreground">
                      {reviewLoopRunning || readinessRunning ? (
                        <Loader2 className="h-4 w-4 animate-spin" />
                      ) : (
                        readinessStatusIcon(latestReadiness, readinessStale, false)
                      )}
                    </div>
                    <div className="min-w-0">
                      <p className="text-sm font-medium text-foreground">
                        {readinessPrimaryCopy.title}
                      </p>
                      <p className="text-xs leading-relaxed text-muted-foreground">
                        {readinessPrimaryCopy.description}
                      </p>
                    </div>
                  </div>
                  {readinessPrimaryAction || readinessSecondaryReviewAction ? (
                    <div className="flex shrink-0 flex-col gap-2 sm:flex-row sm:items-center">
                      {readinessPrimaryAction ? (
                        readinessPrimaryAction.label === "Review & fix" ? (
                          <DisabledTooltip disabled={readinessPrimaryAction.disabled} content={reviewActionDisabledReason}>
                            <Button
                              type="button"
                              variant="outline"
                              size="sm"
                              className="w-full gap-1.5 sm:w-fit"
                              disabled={readinessPrimaryAction.disabled}
                              title={reviewActionDisabledReason}
                              onClick={readinessPrimaryAction.onClick}
                            >
                              {readinessPrimaryAction.icon}
                              {readinessPrimaryAction.label}
                            </Button>
                          </DisabledTooltip>
                        ) : (
                          <Button
                            type="button"
                            variant="outline"
                            size="sm"
                            className="w-full gap-1.5 sm:w-fit"
                            disabled={readinessPrimaryAction.disabled}
                            onClick={readinessPrimaryAction.onClick}
                          >
                            {readinessPrimaryAction.icon}
                            {readinessPrimaryAction.label}
                          </Button>
                        )
                      ) : null}
                      {readinessSecondaryReviewAction ? (
                        <DisabledTooltip disabled={readinessSecondaryReviewAction.disabled} content={reviewActionDisabledReason}>
                          <Button
                            type="button"
                            variant="outline"
                            size="sm"
                            className="w-full gap-1.5 sm:w-fit"
                            disabled={readinessSecondaryReviewAction.disabled}
                            title={reviewActionDisabledReason}
                            onClick={readinessSecondaryReviewAction.onClick}
                          >
                            {readinessSecondaryReviewAction.icon}
                            {readinessSecondaryReviewAction.label}
                          </Button>
                        </DisabledTooltip>
                      ) : null}
                    </div>
                  ) : null}
                </div>
                {readinessRunning ? (
                  <div className="space-y-1 text-xs text-muted-foreground">
                    <div>Collecting diff</div>
                    <div>Running agent review</div>
                    <div>Checking risk signals</div>
                  </div>
                ) : null}
                {!readinessRunning && latestReadiness ? (
                  <Collapsible defaultOpen={readinessBlockedChecks.length > 0 && !readinessStale}>
                    <div className="flex items-center justify-between gap-3 border-t border-border pt-3">
                      <div className="text-xs text-muted-foreground">
                        {readinessBlockedChecks.length === 0 && readinessOptionalCount > 0
                          ? `${readinessOptionalCount} optional ${readinessOptionalCount === 1 ? "item" : "items"}`
                          : readinessDetailCount > 0
                            ? `${readinessDetailCount} readiness ${readinessDetailCount === 1 ? "detail" : "details"}`
                            : "No additional details"}
                      </div>
                      <CollapsibleTrigger asChild>
                        <Button type="button" variant="ghost" size="xs" className="gap-1.5" aria-label="Show readiness details">
                          Details
                          <ChevronDown className="h-3.5 w-3.5" />
                        </Button>
                      </CollapsibleTrigger>
                    </div>
                    <CollapsibleContent className="pt-3">
                      <div className="space-y-3 text-xs">
                        {readinessNeedsActionChecks.length > 0 ? (
                          <div className="space-y-1">
                            <div className="font-medium text-foreground">Needs attention</div>
                            <ReadinessCheckList checks={readinessNeedsActionChecks} onAction={handleReadinessCheckAction} actionDisabled={readinessCheckActionDisabled} />
                          </div>
                        ) : null}
                        <ReadinessCheckGroup title="Passed" checks={readinessPassedChecks} empty="None" onAction={handleReadinessCheckAction} actionDisabled={readinessCheckActionDisabled} />
                        <ReadinessCheckGroup title="Bypassed" checks={readinessBypassedChecks} empty="None" onAction={handleReadinessCheckAction} actionDisabled={readinessCheckActionDisabled} />
                        {readinessReviewPacket && <ReadinessPacketSummary ref={readinessPacketRef} packet={readinessReviewPacket} />}
                        {readinessBlockedChecks.length > 0 && !readinessStale && readinessBypassableBlocked.length > 0 && (
                          <Button size="xs" variant="outline" onClick={() => setReadinessBypassOpen(true)}>
                            Bypass blockers
                          </Button>
                        )}
                      </div>
                    </CollapsibleContent>
                  </Collapsible>
                ) : null}
              </CardContent>
            </Card>
          ) : null}
          <Dialog open={readinessBypassOpen} onOpenChange={setReadinessBypassOpen}>
            <DialogContent>
              <DialogHeader>
                <DialogTitle>Bypass readiness blockers</DialogTitle>
                <DialogDescription>Bypass applies only to the current completed readiness run and will be shown in the PR footer.</DialogDescription>
              </DialogHeader>
              <div className="space-y-2 rounded-md border border-border px-3 py-2 text-xs">
                <div className="font-medium text-foreground">Blockers being bypassed</div>
                <div className="space-y-1">
                  {readinessBypassableBlocked.map((check) => (
                    <div key={readinessCheckKey(check)}>
                      <div className="font-medium text-foreground">{check.title}</div>
                      {check.summary ? <div className="text-muted-foreground">{check.summary}</div> : null}
                    </div>
                  ))}
                </div>
              </div>
              <Textarea
                value={readinessBypassReason}
                onChange={(event) => setReadinessBypassReason(event.target.value)}
                rows={4}
                placeholder="Reason for bypass"
              />
              <DialogFooter>
                <Button variant="outline" onClick={() => setReadinessBypassOpen(false)}>Cancel</Button>
                <Button
                  variant="destructive"
                  disabled={readinessBypassMutation.isPending || readinessBypassReason.trim().length < 8}
                  onClick={() => readinessBypassMutation.mutate()}
                >
                  {readinessBypassMutation.isPending ? <Loader2 className="mr-1.5 h-3 w-3 animate-spin" /> : null}
                  Bypass
                </Button>
              </DialogFooter>
            </DialogContent>
          </Dialog>
          {pullRequestId && prStatus === "closed" && (
            <Card className="border-border/60">
              <CardContent className="p-4">
                <div className="flex items-start gap-3">
                  <div className="flex h-8 w-8 items-center justify-center rounded-full bg-muted text-muted-foreground">
                    <XCircle className="h-4 w-4" />
                  </div>
                  <div className="min-w-0 space-y-1">
                    <div className="text-sm font-medium text-foreground">{closedPRLabel}</div>
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
                    <div className="text-sm font-medium text-foreground">{mergedPRLabel}</div>
                    <p className="text-xs text-foreground">{mergedPRSummary}</p>
                    <p className="text-xs text-muted-foreground">
                      This change has landed. Open a follow-up session if you need to make another revision.
                    </p>
                  </div>
                </div>
              </CardContent>
            </Card>
          )}
          <OverviewTab session={session} members={members} prStatus={prStatus} />
        </div>
      </TabsContent>
      <TabsContent value="preview" className="flex-1 overflow-y-auto scrollbar-hide p-4">
        {selectedIsPrimary && !selectedChangeset?.worktree_path ? (
          <ErrorBoundary fallback={<PreviewTabErrorFallback />}>
            <PreviewPanel
              sessionId={id}
              previewOriginTemplate={PREVIEW_ORIGIN_TEMPLATE}
            />
          </ErrorBoundary>
        ) : selectedChangeset ? (
          <Card className="border-border/60">
            <CardContent className="space-y-3 p-4 text-sm">
              {!selectedChangeset.worktree_path ? (
                <p className="text-sm text-muted-foreground">Preview for this pull request will be available after its branch is materialized.</p>
              ) : (
                <>
              <div>
                <div className="font-medium">Preview {selectedChangeset.title}</div>
                <p className="text-xs text-muted-foreground">Runs from {selectedChangeset.working_branch ?? "the selected pull request branch"}. A stacked branch includes its ancestors.</p>
              </div>
              {changesetPreviewMutation.data?.data.preview_url || changesetPreviewMutation.data?.data.stable_url ? (
                <Button asChild size="sm"><a href={changesetPreviewMutation.data.data.preview_url ?? changesetPreviewMutation.data.data.stable_url} target="_blank" rel="noreferrer">Open preview</a></Button>
              ) : (
                <DisabledTooltip disabled={!!selectedChangeset.working_branch && !selectedChangeset.has_unpushed_changes} content={selectedChangeset.has_unpushed_changes ? "Push this pull request before previewing its branch" : "Materialize and publish this branch before previewing it"}>
                  <Button size="sm" disabled={!selectedChangeset.working_branch || !!selectedChangeset.has_unpushed_changes || changesetPreviewMutation.isPending} onClick={() => changesetPreviewMutation.mutate(selectedChangeset)}>
                    {changesetPreviewMutation.isPending ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : null}
                    Preview pull request
                  </Button>
                </DisabledTooltip>
              )}
              {stackTopChangeset && stackTopChangeset.id !== selectedChangeset.id && stackTopChangeset.working_branch && !stackTopChangeset.has_unpushed_changes ? (
                <Button size="sm" variant="outline" disabled={changesetPreviewMutation.isPending} onClick={() => changesetPreviewMutation.mutate(stackTopChangeset)}>
                  Preview stack top
                </Button>
              ) : null}
                </>
              )}
            </CardContent>
          </Card>
        ) : null}
      </TabsContent>
    </Tabs>
  );

  return (
    <div className="flex h-full">
      {/* Center area: chat or review diff view */}
      <div
        data-testid="session-conversation-workspace"
        className="flex-1 min-w-0 md:min-w-[440px] flex flex-col"
      >
        {!isDedicatedMobileReview ? (
          <>
            <MobileSessionTopBar
              sessionTitle={sessionTitle(session)}
              detailButtonLabel="Open session details"
              backTo="/sessions"
              threads={chromeThreads}
              activeThreadId={activeThread?.id ?? null}
              viewedThreadIds={viewedThreadIds}
              nonInteractiveThreadIds={nonInteractiveThreadIds}
              onOpenDetails={() => setMobileDetailOpen(true)}
              onActiveThreadChange={setActiveThreadId}
              onAddThread={openAddThreadDialog}
              onRenameSession={openMobileRenameDialog}
              onRevertThread={(tid) => revertThreadMutation.mutate(tid)}
              onArchiveThread={(tid) => archiveThreadMutation.mutate(tid)}
              archivePendingThreadId={archiveThreadMutation.isPending ? archiveThreadMutation.variables ?? null : null}
            />

            <ContextHeader
              data-testid="session-main-header"
              className={cn("hidden shrink-0 md:block", SESSION_HEADER_HEIGHT_CLASSNAME)}
              title={
              <div
                data-testid="session-header-summary"
                className="flex min-w-0 flex-1 items-center gap-2 overflow-hidden"
              >
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
                    <h1 className="truncate font-display text-base font-semibold tracking-[-0.025em] text-foreground">
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
                <StatusLabel
                  label={status.label}
                  tone={sessionStatusTone(session.status, prStatus)}
                  active={session.status === "running"}
                  className="shrink-0"
                />
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
              }
              actions={
              <div className="flex shrink-0 items-center gap-2" data-testid="session-header-actions">
                <DisabledTooltip disabled={centerMode === "review" && showDetailPanel} content={detailToggleTitle}>
                  <Button
                    variant="ghost"
                    size="icon"
                    className={cn(centerMode === "review" && showDetailPanel && "opacity-30 cursor-not-allowed", "h-8 w-8 shrink-0")}
                    disabled={centerMode === "review" && showDetailPanel}
                    onClick={() => setShowDetailPanel(!showDetailPanel)}
                    title={detailToggleTitle}
                    aria-keyshortcuts="d"
                  >
                    {showDetailPanel ? <PanelRightClose className="h-4 w-4" /> : <PanelRightOpen className="h-4 w-4" />}
                  </Button>
                </DisabledTooltip>
              </div>
              }
            />
          </>
        ) : null}

        {!isDedicatedMobileReview ? (
          <div className="hidden md:block">
          <AgentTabStrip
            threads={chromeThreads}
            activeThreadId={activeThread?.id ?? null}
            viewedThreadIds={viewedThreadIds}
            nonInteractiveThreadIds={nonInteractiveThreadIds}
            overlapsByThreadId={overlapsByThreadId}
            statusConfig={statusConfig}
            onActiveThreadChange={setActiveThreadId}
            onAddTab={handleCreateThread}
            addTabPending={createThreadMutation.isPending}
            onRevertThread={(tid) => revertThreadMutation.mutate(tid)}
            onArchiveThread={(tid) => archiveThreadMutation.mutate(tid)}
            archivePendingThreadId={archiveThreadMutation.isPending ? archiveThreadMutation.variables ?? null : null}
            addTabButtonRef={addTabButtonRef}
          />
          </div>
        ) : null}
        {/* Center content — either chat or diff review */}
        <div className="flex-1 min-h-0 relative">
          {/* Chat panel stays mounted after first chat exposure to preserve scroll and live transcript state. */}
          {hasMountedChatPanel ? (
            <div className={cn("h-full", centerMode !== "chat" && "hidden")}>
              {threads.length > 0 && activeThread === null ? (
                <div className="flex h-full items-center justify-center">
                  <div className="text-center space-y-2">
                    <Loader2 className="h-5 w-5 animate-spin text-muted-foreground/40 mx-auto" />
                    <p className="text-xs text-muted-foreground">Loading thread...</p>
                  </div>
                </div>
              ) : (
                <MemoizedChatPanel
                  key={activeThread ? `${id}:${activeThread.id}` : id}
                  session={session}
                  sessionId={id}
                  activeThread={activeThread ?? undefined}
                  isActive={isActive}
                  isStopRequested={isStopRequested}
                  stopOutcome={sessionStopOutcome}
                  viewerScope={viewerScope}
                  optimisticMessages={optimisticMessages}
                  onDiffClick={handleChatDiffClick}
                  onApprovePlan={handleApprovePlan}
                  onAdjustPlan={handleAdjustPlan}
                  onRegisterScrollToLiveEdge={registerChatPanelScrollToLiveEdge}
                  onRegisterKeyboardControls={registerChatPanelKeyboardControls}
                />
              )}
            </div>
          ) : null}
          {/* Review diff view — mounted only when active. Full screen lifts
              the same mounted subtree into a viewport overlay (z-40 stays
              below dialogs/sheets at z-50) so diff state survives toggling. */}
          {centerMode === "review" && (
            <div
              className={cn(
                "animate-in fade-in duration-150 flex flex-col",
                isDiffFullScreen ? "fixed inset-0 z-40 bg-background" : "h-full"
              )}
            >
              <div className="flex-1 min-h-0">
                {isDiffDisplayLoading ? (
                  <div className="h-full w-full bg-muted/20 animate-pulse rounded-lg" />
                ) : diffLoadErrorText ? (
                  <div className="flex h-full items-center justify-center p-6">
                    <div className="max-w-sm space-y-3 text-center">
                      <AlertTriangle className="mx-auto h-8 w-8 text-destructive/70" />
                      <div className="space-y-1">
                        <p className="text-sm font-medium text-foreground">Couldn&apos;t load changes</p>
                        <p className="text-xs text-muted-foreground">{diffLoadErrorText}</p>
                      </div>
                      <Button type="button" variant="outline" size="sm" onClick={retryDiffLoad}>
                        Retry
                      </Button>
                    </div>
                  </div>
                ) : (
                  <ReviewDiffView
                    sessionId={id}
                    files={visibleFilteredFiles}
                    allFiles={visibleDiffFiles}
                    activeFileIndex={activeFileIndex}
                    onFileChange={setActiveFileIndex}
                    onBack={exitReview}
                    isMobile={isMobileReviewViewport}
                    onOpenFileList={openMobileFilesList}
                    onOpenComposer={session.agent_type !== "pm_agent" ? openMobileReviewComposer : undefined}
                    commentsByLine={commentsByLine}
                    activeCommentLine={activeCommentLine}
                    onAddComment={handleAddComment}
                    onSubmitComment={handleSubmitComment}
                    onCancelComment={handleCancelComment}
                    onUpdateComment={updateComment}
                    onDeleteComment={deleteComment}
                    diffSearchQuery={diffSearchQuery}
                    onDiffSearchChange={setDiffSearchQuery}
                    isFullScreen={isDiffFullScreen}
                    onToggleFullScreen={toggleDiffFullScreen}
                  />
                )}
              </div>
            </div>
          )}
        </div>

        {session.agent_type !== "pm_agent" && !isDedicatedMobileReview && (
          <>
            {composerIsSnapshotExpired && (
              <div className="flex items-center gap-2 px-4 py-2.5 text-xs border-t bg-warning/10 border-warning/30 text-warning">
                <Clock className="h-3.5 w-3.5 shrink-0" />
                <span>
                  This session&apos;s environment has expired. Sessions can be continued for up to 30 days after their last activity. To make further changes, please start a new session.
                </span>
              </div>
            )}
            {runtimeRecoveryActive && !composerIsSnapshotExpired && (
              <RuntimeRecoveryNotice />
            )}
            {composerLacksHeadlessResume && composerCanSendMessage && !composerIsSnapshotExpired && (
              <div className="flex items-center gap-2 px-4 py-2.5 text-xs border-t bg-info/10 border-info/30 text-info">
                <AlertTriangle className="h-3.5 w-3.5 shrink-0" />
                <span>
                  {AGENTS_BY_KEY[session.agent_type]?.label ?? session.agent_type} doesn&apos;t support headless conversation resume. Follow-up messages run against the restored filesystem, but earlier chat context is not replayed — include anything you need the agent to remember.
                </span>
              </div>
            )}
            {renderRecoverableInboxNotice()}
            {composerChangesetID && (() => {
              const target = changesets.find((item) => item.id === composerChangesetID);
              return target ? (
                <div className="mb-2 flex items-center justify-between rounded-md border border-border bg-muted/40 px-3 py-2 text-xs" data-testid="composer-changeset-target">
                  <span>Editing PR: {target.title}</span>
                  <Button type="button" size="sm" variant="ghost" className="h-6 px-2 text-xs" onClick={() => setComposerChangesetID(null)}>Clear</Button>
                </div>
              ) : null;
            })()}
            <SessionComposer
              sessionId={session.id}
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
              cancelPending={cancelMutation.isPending || isStopRequested}
              uploadError={composerUploadError}
              onCancelSession={handleCancelSession}
              onSend={handleComposerSend}
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
              unavailableReason={composerUnavailableReason}
              placeholderOverride={composerPlaceholderOverride}
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
            data-testid="session-detail-panel"
            style={{ width: detailWidth }}
            className="relative z-10 bg-background flex flex-col shrink-0 overflow-hidden"
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
            Browse session details, changed files, and preview on mobile.
          </SheetDescription>
          {panelTabsEl}
        </SheetContent>
      </Sheet>
      <Dialog open={reviewConfigOpen} onOpenChange={setReviewConfigOpen}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>Review</DialogTitle>
            <DialogDescription>
              Run a selected agent&apos;s native review loop in this session&apos;s sandbox. The loop opens a dedicated review tab and keeps changes on the same branch.
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-3">
            <div className="space-y-2">
              <Label htmlFor="review-agent-type">Coding agent</Label>
              <Select
                value={reviewAgentType}
                onValueChange={setReviewAgentType}
                disabled={startReviewLoopMutation.isPending}
              >
                <SelectTrigger id="review-agent-type" aria-label="Review coding agent" className="h-9 w-full">
                  <SelectValue placeholder="Select coding agent" />
                </SelectTrigger>
                <SelectContent>
                  {reviewAgentOptions.map((agent) => (
                    <SelectItem key={agent.key} value={agent.key}>
                      {agent.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              <p className="text-xs text-muted-foreground">
                Reviews the current working tree, fixes findings, and stops early if the follow-up pass is clean.
              </p>
              {session.agent_type === reviewAgentType ? (
                <p className="text-xs text-muted-foreground">
                  This is the same agent used by the main session.
                </p>
              ) : (
                <p className="text-xs text-muted-foreground">
                  Separate from the main session&apos;s {AGENTS_BY_KEY[session.agent_type]?.label ?? session.agent_type} agent.
                </p>
              )}
            </div>
            <div className="space-y-2">
              <Label>Fix mode</Label>
              <RadioGroup
                value={reviewFixMode}
                onValueChange={(value) => setReviewFixMode(value as ReviewLoopFixMode)}
                className="grid gap-2"
                disabled={startReviewLoopMutation.isPending}
              >
                <label className="flex cursor-pointer items-start gap-3 rounded-md border border-input p-3 transition-colors hover:bg-muted/40 has-[[data-state=checked]]:border-primary has-[[data-state=checked]]:bg-primary/5">
                  <RadioGroupItem value="minimal" aria-label="Minimal fixes" className="mt-0.5" />
                  <span className="space-y-1">
                    <span className="block text-xs font-medium text-foreground">Minimal fixes</span>
                    <span className="block text-xs text-muted-foreground">
                      Fix only what is needed to clear the review while preserving the current scope.
                    </span>
                  </span>
                </label>
                <label className="flex cursor-pointer items-start gap-3 rounded-md border border-input p-3 transition-colors hover:bg-muted/40 has-[[data-state=checked]]:border-primary has-[[data-state=checked]]:bg-primary/5">
                  <RadioGroupItem value="exhaustive" aria-label="Fix every finding" className="mt-0.5" />
                  <span className="space-y-1">
                    <span className="block text-xs font-medium text-foreground">Fix every finding</span>
                    <span className="block text-xs text-muted-foreground">
                      Address every issue the review reports before starting the next review pass.
                    </span>
                  </span>
                </label>
              </RadioGroup>
            </div>
            <div className="space-y-2">
              <div className="flex items-center justify-between gap-3">
                <Label htmlFor="review-pass-count">Review passes</Label>
              </div>
              <div className="flex items-center gap-2">
                <Button
                  type="button"
                  variant="outline"
                  size="icon"
                  className="h-9 w-9 shrink-0"
                  aria-label="Decrease review passes"
                  disabled={reviewPasses <= 1 || startReviewLoopMutation.isPending}
                  onClick={() => setReviewPasses((current) => Math.max(1, current - 1))}
                >
                  <Minus className="h-3.5 w-3.5" />
                </Button>
                <Input
                  id="review-pass-count"
                  aria-label="Review passes"
                  type="number"
                  min={1}
                  max={5}
                  value={reviewPasses}
                  disabled={startReviewLoopMutation.isPending}
                  onChange={(event) => {
                    const next = Number.parseInt(event.target.value, 10);
                    if (Number.isNaN(next)) {
                      setReviewPasses(1);
                      return;
                    }
                    setReviewPasses(Math.min(5, Math.max(1, next)));
                  }}
                  className="h-9 text-center"
                />
                <Button
                  type="button"
                  variant="outline"
                  size="icon"
                  className="h-9 w-9 shrink-0"
                  aria-label="Increase review passes"
                  disabled={reviewPasses >= 5 || startReviewLoopMutation.isPending}
                  onClick={() => setReviewPasses((current) => Math.min(5, current + 1))}
                >
                  <Plus className="h-3.5 w-3.5" />
                </Button>
              </div>
            </div>
          </div>
          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              disabled={startReviewLoopMutation.isPending}
              onClick={() => setReviewConfigOpen(false)}
            >
              Cancel
            </Button>
            <Button
              type="button"
              disabled={reviewActionDisabled}
              onClick={() => startReviewLoopMutation.mutate()}
            >
              {startReviewLoopMutation.isPending ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : null}
              Start review
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
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
              <div className="flex items-center gap-2 px-4 py-3 text-xs border-b bg-warning/10 border-warning/30 text-warning">
                <Clock className="h-3.5 w-3.5 shrink-0" />
                <span>
                  This session&apos;s environment has expired. Sessions can be continued for up to 30 days after their last activity. To make further changes, please start a new session.
                </span>
              </div>
            ) : null}
            {runtimeRecoveryActive && !composerIsSnapshotExpired ? (
              <RuntimeRecoveryNotice border="border-b" />
            ) : null}
            {composerLacksHeadlessResume && composerCanSendMessage && !composerIsSnapshotExpired ? (
              <div className="flex items-center gap-2 px-4 py-3 text-xs border-b bg-info/10 border-info/30 text-info">
                <AlertTriangle className="h-3.5 w-3.5 shrink-0" />
                <span>
                  {AGENTS_BY_KEY[session.agent_type]?.label ?? session.agent_type} doesn&apos;t support headless conversation resume. Follow-up messages run against the restored filesystem, but earlier chat context is not replayed — include anything you need the agent to remember.
                </span>
              </div>
            ) : null}
            {renderRecoverableInboxNotice()}
            <SessionComposer
              sessionId={session.id}
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
              cancelPending={cancelMutation.isPending || isStopRequested}
              uploadError={composerUploadError}
              onCancelSession={handleCancelSession}
              onSend={handleComposerSend}
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
              unavailableReason={composerUnavailableReason}
              placeholderOverride={composerPlaceholderOverride}
            />
          </SheetContent>
        </Sheet>
      ) : null}
      <Dialog open={mobileRenameOpen} onOpenChange={setMobileRenameOpen}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>Rename session</DialogTitle>
            <DialogDescription>
              Update the session title without crowding the mobile header.
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-3">
            <Label htmlFor="mobile-session-title">Session title</Label>
            <Input
              id="mobile-session-title"
              aria-label="Session title"
              value={draftTitle}
              onChange={(e) => setDraftTitle(e.target.value)}
              disabled={updateSessionMutation.isPending}
            />
          </div>
          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={() => {
                setDraftTitle(currentTitle);
                setMobileRenameOpen(false);
              }}
              disabled={updateSessionMutation.isPending}
            >
              Cancel
            </Button>
            <DisabledTooltip disabled={!canSaveTitle} content={saveTitleDisabledReason}>
              <Button
                type="button"
                onClick={() => updateSessionMutation.mutate(trimmedDraftTitle, {
                  onSuccess: () => {
                    setMobileRenameOpen(false);
                  },
                })}
                disabled={!canSaveTitle}
              >
                Save title
              </Button>
            </DisabledTooltip>
          </DialogFooter>
        </DialogContent>
      </Dialog>
      <Dialog open={addThreadOpen} onOpenChange={setAddThreadOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Add agent tab</DialogTitle>
            <DialogDescription>
              Create a blank tab in this sandbox. It will not run until you send the first message.
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-4">
            <div className="space-y-2">
              <Label className="text-xs" htmlFor="new-thread-agent">Agent</Label>
              <Select value={newThreadAgentType} onValueChange={setNewThreadAgentType}>
                <SelectTrigger id="new-thread-agent" aria-label="Agent">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {AGENTS.map((agent) => (
                    <SelectItem key={agent.key} value={agent.key}>
                      {agent.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            {selectedNewThreadModels.length > 0 && (
              <div className="space-y-2">
                <Label className="text-xs" htmlFor="new-thread-model">Model</Label>
                <Select value={newThreadModel || "__default"} onValueChange={(value) => setNewThreadModel(value === "__default" ? "" : value)}>
                  <SelectTrigger id="new-thread-model" aria-label="Model">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="__default">Default model</SelectItem>
                    {selectedNewThreadModels.map((model) => (
                      <SelectItem key={model} value={model}>
                        {model}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
            )}
            <div className="space-y-2">
              <Label className="text-xs" htmlFor="new-thread-label">Tab label</Label>
              <Input
                id="new-thread-label"
                aria-label="Tab label"
                value={newThreadLabel}
                onChange={(event) => setNewThreadLabel(event.target.value)}
                placeholder={`${selectedNewThreadAgent?.label ?? "Agent"} ${threads.length + 1}`}
              />
            </div>
          </div>
          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={() => setAddThreadOpen(false)}
              disabled={createThreadMutation.isPending}
            >
              Cancel
            </Button>
            <Button
              type="button"
              onClick={() => createThreadMutation.mutate(buildDialogThreadRequest())}
              disabled={createThreadMutation.isPending}
            >
              {createThreadMutation.isPending && <Loader2 className="h-4 w-4 animate-spin" />}
              Create tab
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
      <SessionKeyboardHelpOverlay
        open={keyboardHelpOpen}
        onOpenChange={setKeyboardHelpOpen}
        canShipPR={canShipPR}
      />
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
                  : prAuthPrompt?.purpose === "create_branch"
                    ? "Create this branch as yourself?"
                  : "Open this pull request as yourself?"}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {prAuthPrompt?.purpose === "merge_pr"
                ? "Authorize GitHub once to merge pull requests as you."
                : prAuthPrompt?.purpose === "push_changes"
                  ? prAuthPrompt.can_fallback_to_app
                    ? "Authorize GitHub once to push as you. If you skip this, 143 can still push the commits as the app."
                    : "Authorize GitHub once to push as you."
                  : prAuthPrompt?.purpose === "create_branch"
                    ? prAuthPrompt.can_fallback_to_app
                      ? "Authorize GitHub once to create branches as you. If you skip this, 143 can still push the branch as the app."
                      : "Authorize GitHub once to create branches as you."
                  : prAuthPrompt?.can_fallback_to_app
                    ? "Authorize GitHub once to open PRs as you. If you skip this, 143 can still open the PR as the app."
                    : "Authorize GitHub once to open PRs as you."}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            {prAuthPrompt?.purpose === "create_pr" && prAuthPrompt.can_fallback_to_app ? (
              <AlertDialogCancel
                onClick={(event) => {
                  event.preventDefault();
                  setPRAuthPrompt(null);
                  createPRMutation.mutate({ authorMode: "app", mergeWhenReady: prAuthPrompt.mergeWhenReady });
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
            {prAuthPrompt?.purpose === "create_branch" && prAuthPrompt.can_fallback_to_app ? (
              <AlertDialogCancel
                onClick={(event) => {
                  event.preventDefault();
                  setPRAuthPrompt(null);
                  createBranchMutation.mutate({ authorMode: "app" });
                }}
              >
                Create as 143
              </AlertDialogCancel>
            ) : null}
            <AlertDialogAction
              onClick={(event) => {
                event.preventDefault();
                if (!prAuthPrompt) return;
                const resumeToken =
                  prAuthPrompt.purpose === "create_pr" || prAuthPrompt.purpose === "create_branch" || prAuthPrompt.purpose === "push_changes"
                    ? prAuthPrompt.resume_token
                    : undefined;
                api.githubStatus.connect(resumeToken || undefined);
              }}
            >
              Connect your GitHub account
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
