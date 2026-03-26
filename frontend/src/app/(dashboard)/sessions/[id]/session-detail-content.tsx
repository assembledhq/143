"use client";

import { useCallback, useMemo, useRef, useState, useEffect } from "react";
import { useQueryState } from "nuqs";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import {
  AlertTriangle,
  ArrowUp,
  ExternalLink,
  FileCode2,
  GitPullRequest,
  RefreshCw,
  CheckCircle2,
  XCircle,
  MinusCircle,
  Square,
  PanelRightOpen,
  PanelRightClose,
  ChevronDown,
  Paperclip,
  X,
  Loader2,
} from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { MarkdownContent } from "@/components/markdown";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, TableHeader, TableBody, TableRow, TableHead, TableCell } from "@/components/ui/table";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Textarea } from "@/components/ui/textarea";
import { ChatTimeline } from "@/components/chat-timeline";
import { api } from "@/lib/api";
import { AGENT_TYPE_OPTIONS } from "@/lib/model-constants";
import { SSE_EVENT, addSSEListener } from "@/lib/sse";
import { buildTimeline } from "@/lib/timeline";
import { parseDiffStats } from "@/lib/diff-parser";
import type { Session, SessionLog, SessionMessage, User, Validation } from "@/lib/types";
import { AuditLogTrigger } from "@/components/audit/audit-log-trigger";
import { ResizeHandle } from "@/components/resize-handle";
import { cn, sessionTitle } from "@/lib/utils";
import { DiffStatsBadge, FileTree, ReviewToolbar, DiffPane, SessionFooter, KeyboardHelpOverlay, CommentsSummary, RepoExplorer, type ViewMode, type DiffPaneHandle } from "@/components/code-review";
import { useDiffKeyboardNav } from "@/hooks/use-diff-keyboard-nav";
import { useReviewComments } from "@/hooks/use-review-comments";
import { useDiffViewState } from "@/hooks/use-diff-view-state";
import { useReviewedFiles } from "@/hooks/use-reviewed-files";

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

const agentTypeLabels: Record<string, string> = {
  claude_code: "Claude Code",
  codex: "Codex",
  gemini_cli: "Gemini CLI",
  custom: "Custom",
};

function formatDuration(startedAt?: string, completedAt?: string): string {
  if (!startedAt) return "-";
  const start = new Date(startedAt);
  const end = completedAt ? new Date(completedAt) : new Date();
  const diffMs = end.getTime() - start.getTime();
  const diffSecs = Math.floor(diffMs / 1000);
  if (diffSecs < 60) return `${diffSecs}s`;
  const mins = Math.floor(diffSecs / 60);
  const secs = diffSecs % 60;
  return `${mins}m ${secs}s`;
}

function formatTimestamp(dateStr?: string): string {
  if (!dateStr) return "-";
  return new Date(dateStr).toLocaleString();
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
  if (!result) return <Badge variant="secondary" className="text-[11px]">skipped</Badge>;
  if (result === "pass") return <Badge variant="secondary" className="bg-emerald-50 text-emerald-700 dark:bg-emerald-950/30 dark:text-emerald-400 border-emerald-200/50 dark:border-emerald-800/30 text-[11px]">pass</Badge>;
  if (result === "fail") return <Badge variant="secondary" className="bg-destructive/10 text-destructive border-destructive/20 text-[11px]">fail</Badge>;
  return <Badge variant="secondary" className="text-[11px]">{result}</Badge>;
}

// ---------------------------------------------------------------------------
// Detail panel tabs (shown in right sidebar)
// ---------------------------------------------------------------------------

type DetailTab = "overview" | "changes" | "validation";

function OverviewTab({ session, members }: { session: Session; members: User[] }) {
  const queryClient = useQueryClient();
  const retryMutation = useMutation({
    mutationFn: () => api.issues.triggerFix(session.issue_id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["session", session.id] });
    },
  });

  const status = statusConfig[session.status] || statusConfig.pending;
  const terminalStatuses = new Set(["completed", "pr_created", "failed", "cancelled", "skipped"]);
  const isActive = !terminalStatuses.has(session.status);

  return (
    <div className="space-y-4">
      <Card>
        <CardContent className="pt-6">
          <div className="grid grid-cols-2 gap-5 text-sm">
            <div>
              <span className="text-xs font-medium text-muted-foreground/70 tracking-wider">Status</span>
              <div className="mt-1">
                <span className={`inline-flex items-center rounded-full px-2 py-0.5 text-[11px] font-medium ${status.color}`}>
                  {isActive && (
                    <span className="relative mr-1.5 flex h-2 w-2">
                      <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-blue-400 opacity-75" />
                      <span className="relative inline-flex rounded-full h-2 w-2 bg-blue-500" />
                    </span>
                  )}
                  {status.label}
                </span>
              </div>
            </div>
            <div>
              <span className="text-xs font-medium text-muted-foreground/70 tracking-wider">Agent Type</span>
              <p className="mt-1 font-medium">{agentTypeLabels[session.agent_type] || session.agent_type}</p>
            </div>
            <div>
              <span className="text-xs font-medium text-muted-foreground/70 tracking-wider">Triggered by</span>
              <div className="mt-1">
                {/* Infer PM-triggered from pm_plan_id + no user — consider adding an explicit triggered_by_type field */}
                {session.pm_plan_id && !session.triggered_by_user_id ? (
                  <span className="inline-flex items-center gap-1.5 font-medium">
                    <span className="inline-flex items-center rounded-full bg-primary/10 px-2 py-0.5 text-[11px] font-medium text-primary">
                      PM Agent
                    </span>
                  </span>
                ) : session.triggered_by_user_id ? (
                  <p className="font-medium">
                    {members.find((m) => m.id === session.triggered_by_user_id)?.name || "Unknown user"}
                  </p>
                ) : (
                  <p className="font-medium text-muted-foreground">System</p>
                )}
              </div>
            </div>
            <div>
              <span className="text-xs font-medium text-muted-foreground/70 tracking-wider">Duration</span>
              <p className="mt-1 font-medium">{formatDuration(session.started_at, session.completed_at)}</p>
            </div>
            <div>
              <span className="text-xs font-medium text-muted-foreground/70 tracking-wider">Started</span>
              <p className="mt-1">{formatTimestamp(session.started_at)}</p>
            </div>
            <div>
              <span className="text-xs font-medium text-muted-foreground/70 tracking-wider">Completed</span>
              <p className="mt-1">{formatTimestamp(session.completed_at)}</p>
            </div>
          </div>
        </CardContent>
      </Card>

      {session.result_summary && (
        <Card>
          <CardHeader className="pb-2">
            <CardTitle className="text-sm">Result</CardTitle>
          </CardHeader>
          <CardContent>
            <MarkdownContent content={session.result_summary} className="text-sm" />
          </CardContent>
        </Card>
      )}

      {session.pm_plan_id && (session.pm_reasoning || session.pm_approach) && (
        <Card>
          <CardHeader className="pb-2">
            <CardTitle className="text-sm">PM context</CardTitle>
          </CardHeader>
          <CardContent className="space-y-3 text-sm">
            {session.pm_reasoning && (
              <div>
                <p className="text-xs font-medium text-muted-foreground">Why this was prioritized</p>
                <p>{session.pm_reasoning}</p>
              </div>
            )}
            {session.pm_approach && (
              <div>
                <p className="text-xs font-medium text-muted-foreground">Suggested approach</p>
                <p>{session.pm_approach}</p>
              </div>
            )}
          </CardContent>
        </Card>
      )}

      {session.status === "failed" && (session.failure_explanation || session.error) && (
        <Card className="border-destructive/20 dark:border-destructive/30">
          <CardHeader className="pb-2">
            <CardTitle className="text-sm text-destructive flex items-center gap-2">
              <XCircle className="h-4 w-4" />
              Failure details
            </CardTitle>
          </CardHeader>
          <CardContent className="space-y-3">
            {session.failure_category && (
              <Badge variant="secondary" className="bg-destructive/10 text-destructive border-destructive/20 text-[11px]">
                {session.failure_category}
              </Badge>
            )}
            <p className="text-sm">{session.failure_explanation || session.error}</p>
            {session.failure_next_steps && session.failure_next_steps.length > 0 && (
              <div>
                <p className="text-xs font-medium text-muted-foreground mb-1">Next steps</p>
                <ul className="list-disc list-inside text-sm space-y-1">
                  {session.failure_next_steps.map((step, i) => (
                    <li key={i}>{step}</li>
                  ))}
                </ul>
              </div>
            )}
            {session.failure_retry_advised && (
              <Button
                size="sm"
                variant="outline"
                onClick={() => retryMutation.mutate()}
                disabled={retryMutation.isPending}
              >
                <RefreshCw className={`mr-1.5 h-3 w-3 ${retryMutation.isPending ? "animate-spin" : ""}`} />
                {retryMutation.isPending ? "Retrying..." : "Retry"}
              </Button>
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
    queryFn: () => api.sessions.getValidation(sessionId),
  });

  if (isLoading) {
    return <div className="py-8 text-center text-sm text-muted-foreground">Loading validation...</div>;
  }

  if (error) {
    return <div className="py-8 text-center text-sm text-muted-foreground">No validation data available.</div>;
  }

  const validation = data?.data;
  if (!validation) {
    return <div className="py-8 text-center text-sm text-muted-foreground">No validation data available.</div>;
  }

  const overallStatus = validation.status;

  return (
    <div className="space-y-4">
      <div className="flex items-center gap-2">
        <span className="text-sm font-medium">Overall:</span>
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

const prStatusColor: Record<string, string> = {
  open: "bg-emerald-500/10 text-emerald-700 dark:text-emerald-400",
  merged: "bg-purple-500/10 text-purple-700 dark:text-purple-400",
  closed: "bg-red-500/10 text-red-700 dark:text-red-400",
};

function PRCard({ sessionId }: { sessionId: string }) {
  const { data: prData, isLoading: prLoading } = useQuery({
    queryKey: ["session", sessionId, "pr"],
    queryFn: () => api.sessions.getPR(sessionId),
  });

  const pr = prData?.data;
  if (prLoading) return <div className="py-2 text-center text-sm text-muted-foreground">Loading PR...</div>;
  if (!pr) return null;

  return (
    <Card className="mx-4 mt-3">
      <CardContent className="pt-4 pb-3 space-y-3">
        <div className="flex items-start justify-between">
          <div className="min-w-0">
            <h3 className="text-sm font-medium truncate">{pr.title}</h3>
            <p className="text-xs text-muted-foreground mt-0.5">{pr.github_repo} #{pr.github_pr_number}</p>
          </div>
          <a href={pr.github_pr_url} target="_blank" rel="noopener noreferrer">
            <Button variant="outline" size="sm">
              <ExternalLink className="mr-1.5 h-3 w-3" />
              GitHub
            </Button>
          </a>
        </div>
        <div className="flex items-center gap-3 text-sm">
          <div>
            <span className="text-muted-foreground">Status: </span>
            <Badge variant="secondary" className={`text-[11px] ${prStatusColor[pr.status] || "bg-muted text-muted-foreground"}`}>
              {pr.status}
            </Badge>
          </div>
          {pr.review_status && (
            <div>
              <span className="text-muted-foreground">Review: </span>
              <Badge variant="secondary" className="text-[11px]">{pr.review_status}</Badge>
            </div>
          )}
          <div className="min-w-0">
            <span className="text-muted-foreground">Branch: </span>
            <code className="text-xs bg-muted px-1 py-0.5 rounded">{pr.branch_name}</code>
          </div>
        </div>
      </CardContent>
    </Card>
  );
}

function ChangesTab({
  session,
  sessionId,
  maximized,
  onToggleMaximize,
}: {
  session: Session;
  sessionId: string;
  maximized: boolean;
  onToggleMaximize: () => void;
}) {
  const queryClient = useQueryClient();
  const [activeFileIndex, setActiveFileIndex] = useState(0);
  const [showFileTree, setShowFileTree] = useState(true);
  const [showKeyboardHelp, setShowKeyboardHelp] = useState(false);
  const [explorerMode, setExplorerMode] = useState(false);
  const [explorerInitialPath, setExplorerInitialPath] = useState<string | undefined>(undefined);
  const [activeCommentLine, setActiveCommentLine] = useState<{
    filePath: string;
    lineNumber: number;
    side: "old" | "new";
  } | null>(null);
  const [viewMode, setViewMode] = useState<ViewMode>(() => {
    if (typeof window !== "undefined") {
      return (localStorage.getItem("diff-view-mode") as ViewMode) || "unified";
    }
    return "unified";
  });
  const diffPaneRef = useRef<DiffPaneHandle>(null);

  // --- Pass selection & diff parsing (extracted to hook) ---
  const {
    files,
    filteredFiles,
    passes,
    passRange,
    setPassRange,
    diffSearchQuery,
    setDiffSearchQuery,
  } = useDiffViewState(session);

  // --- Reviewed files (extracted to hook) ---
  const { reviewedFiles, toggleReviewed: handleToggleReviewed } = useReviewedFiles(sessionId);

  // --- Review comments ---
  const {
    comments,
    commentsByLine,
    createComment,
    updateComment,
    deleteComment,
  } = useReviewComments(sessionId);

  // Use backend endpoint to compile and send review comments in one call.
  // The backend formats the comments and sends the message directly.
  // If the backend cannot send (e.g., session not idle), it returns sent=false
  // and the frontend falls back to sending manually.
  const sendToAgentMutation = useMutation({
    mutationFn: async () => {
      const resp = await api.sessions.sendReviewComments(sessionId);
      if (!resp.data.sent) {
        // Fallback: backend could not send directly, send manually
        const message = resp.data.message;
        return api.sessions.sendMessage(sessionId, message);
      }
      return resp;
    },
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["session", sessionId] });
      queryClient.invalidateQueries({ queryKey: ["session", sessionId, "messages"] });
    },
    onError: (err: Error) => {
      console.error("Failed to send review comments to agent:", err.message);
    },
  });

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
      // Scroll to the file containing the comment
      // Search filteredFiles since DiffPane receives filteredFiles
      const fileIndex = filteredFiles.findIndex((f) => f.newPath === filePath);
      if (fileIndex >= 0) {
        setActiveFileIndex(fileIndex);
        diffPaneRef.current?.scrollToFile(fileIndex);
      }
    },
    [filteredFiles]
  );

  // --- View & nav ---
  const handleViewModeChange = useCallback((mode: ViewMode) => {
    setViewMode(mode);
    if (typeof window !== "undefined") {
      localStorage.setItem("diff-view-mode", mode);
    }
  }, []);

  const handleFileSelect = useCallback((index: number) => {
    setActiveFileIndex(index);
    diffPaneRef.current?.scrollToFile(index);
  }, []);

  const toggleViewMode = useCallback(() => {
    handleViewModeChange(viewMode === "unified" ? "split" : "unified");
  }, [viewMode, handleViewModeChange]);

  const toggleFileTree = useCallback(() => {
    setShowFileTree((v) => !v);
  }, []);

  const handleNextHunk = useCallback(() => {
    diffPaneRef.current?.scrollToNextHunk();
  }, []);

  const handlePrevHunk = useCallback(() => {
    diffPaneRef.current?.scrollToPrevHunk();
  }, []);

  const handleJumpToFile = useCallback(() => {
    diffPaneRef.current?.scrollToFile(activeFileIndex);
  }, [activeFileIndex]);

  const toggleShowHelp = useCallback(() => {
    setShowKeyboardHelp((v) => !v);
  }, []);

  const handleBrowseRepo = useCallback(() => {
    setExplorerMode(true);
    setExplorerInitialPath(undefined);
  }, []);

  const handleBackToDiff = useCallback(() => {
    setExplorerMode(false);
    setExplorerInitialPath(undefined);
  }, []);

  const handleBrowseFile = useCallback((filePath: string) => {
    setExplorerInitialPath(filePath);
    setExplorerMode(true);
  }, []);

  const toggleExplorer = useCallback(() => {
    setExplorerMode((v) => !v);
    setExplorerInitialPath(undefined);
  }, []);

  // `c` key: open comment input on the first changed line of the active file
  const handleAddCommentOnSelectedLine = useCallback(() => {
    const activeFile = filteredFiles[activeFileIndex];
    if (!activeFile) return;
    // Find the first add or remove line to comment on
    for (const hunk of activeFile.hunks) {
      for (const line of hunk.lines) {
        if (line.type === "add" && line.newLineNumber != null) {
          handleAddComment(activeFile.newPath, line.newLineNumber, "new");
          return;
        }
        if (line.type === "remove" && line.oldLineNumber != null) {
          handleAddComment(activeFile.newPath, line.oldLineNumber, "old");
          return;
        }
      }
    }
  }, [filteredFiles, activeFileIndex, handleAddComment]);

  useDiffKeyboardNav({
    fileCount: files.length,
    activeFileIndex,
    onFileChange: handleFileSelect,
    onToggleFileTree: toggleFileTree,
    onToggleViewMode: toggleViewMode,
    onSetViewMode: handleViewModeChange,
    onToggleMaximize,
    onNextHunk: handleNextHunk,
    onPrevHunk: handlePrevHunk,
    onJumpToFile: handleJumpToFile,
    onShowHelp: toggleShowHelp,
    onToggleExplorer: toggleExplorer,
    onAddCommentOnSelectedLine: handleAddCommentOnSelectedLine,
    enabled: activeCommentLine === null && !explorerMode,
  });

  const hasDiff = files.length > 0;

  // If explorer mode, render the repo explorer instead of the diff view
  if (explorerMode) {
    return (
      <div className="flex flex-col h-full">
        <RepoExplorer
          sessionId={sessionId}
          diffFiles={files}
          onBack={handleBackToDiff}
          initialPath={explorerInitialPath}
        />
      </div>
    );
  }

  return (
    <div className="flex flex-col h-full">
      {/* Toolbar */}
      {hasDiff && (
        <ReviewToolbar
          viewMode={viewMode}
          onViewModeChange={handleViewModeChange}
          maximized={maximized}
          onToggleMaximize={onToggleMaximize}
          showFileTree={showFileTree}
          onToggleFileTree={toggleFileTree}
          onBrowseRepo={handleBrowseRepo}
          passes={passes}
          selectedPassRange={passRange}
          onPassRangeChange={setPassRange}
          searchQuery={diffSearchQuery}
          onSearchChange={setDiffSearchQuery}
        />
      )}

      {/* Comments summary */}
      {comments.length > 0 && (
        <CommentsSummary
          comments={comments}
          onCommentClick={handleCommentClick}
          onSendToAgent={() => sendToAgentMutation.mutate()}
          isSending={sendToAgentMutation.isPending}
        />
      )}

      {/* PR Card */}
      <PRCard sessionId={sessionId} />

      {/* Main content area */}
      {hasDiff ? (
        <div className="flex flex-1 min-h-0">
          {/* File tree */}
          {showFileTree && (
            <div className="w-[220px] shrink-0 border-r border-border overflow-hidden">
              <FileTree
                files={filteredFiles}
                activeFileIndex={activeFileIndex}
                onFileSelect={handleFileSelect}
                reviewedFiles={reviewedFiles}
                onToggleReviewed={handleToggleReviewed}
              />
            </div>
          )}
          {/* Diff pane */}
          <DiffPane
            ref={diffPaneRef}
            files={filteredFiles}
            viewMode={viewMode}
            sessionId={sessionId}
            activeFileIndex={activeFileIndex}
            commentsByLine={commentsByLine}
            activeCommentLine={activeCommentLine}
            onAddComment={handleAddComment}
            onSubmitComment={handleSubmitComment}
            onCancelComment={handleCancelComment}
            onUpdateComment={updateComment}
            onDeleteComment={deleteComment}
            onBrowseFile={handleBrowseFile}
          />
        </div>
      ) : (
        <div className="flex-1 flex items-center justify-center py-12">
          <div className="text-center space-y-2 max-w-[280px]">
            <FileCode2 className="h-8 w-8 text-muted-foreground/40 mx-auto" />
            <p className="text-sm font-medium text-muted-foreground">
              No changes yet
            </p>
            <p className="text-xs text-muted-foreground/60">
              {session.status === "running" || session.status === "pending"
                ? "Changes will appear here as the agent modifies files."
                : "This session did not produce any file changes."}
            </p>
          </div>
        </div>
      )}

      {/* Keyboard help overlay */}
      <KeyboardHelpOverlay open={showKeyboardHelp} onClose={toggleShowHelp} />
    </div>
  );
}

// ---------------------------------------------------------------------------
// Main chat panel
// ---------------------------------------------------------------------------

const MAX_SSE_RECONNECT_ATTEMPTS = 5;
const BASE_SSE_RECONNECT_DELAY_MS = 1000;

function ChatPanel({ session, sessionId, isActive, onDiffClick }: { session: Session; sessionId: string; isActive: boolean; onDiffClick?: () => void }) {
  const queryClient = useQueryClient();
  const [message, setMessage] = useState("");
  const [selectedModel, setSelectedModel] = useState("");
  const [streamedLogs, setStreamedLogs] = useState<SessionLog[]>([]);
  const [attachments, setAttachments] = useState<string[]>([]);
  const [isUploading, setIsUploading] = useState(false);
  const [uploadError, setUploadError] = useState<string | null>(null);
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const uploadInputRef = useRef<HTMLInputElement>(null);
  const scrollRef = useRef<HTMLDivElement>(null);
  const seenLogIds = useRef<Set<number>>(new Set());
  const reconnectAttempts = useRef(0);
  const reconnectTimer = useRef<ReturnType<typeof setTimeout>>(null);
  const apiBase = process.env.NEXT_PUBLIC_API_URL || "";

  const isIdle = session.status === "idle";
  const isRunning = session.status === "running";
  // Allow messaging in any state except "skipped" (never ran, no workspace)
  // and "pending" (agent not started yet). The backend will reject statuses
  // it cannot handle, so this is safe to be permissive.
  const canSendMessage = session.status !== "skipped" && session.status !== "pending";

  const availableModels = useMemo(() => {
    const agentType = AGENT_TYPE_OPTIONS.find((a) => a.key === session.agent_type);
    return agentType?.models ?? [];
  }, [session.agent_type]);

  const { data: messagesData } = useQuery({
    queryKey: ["session", sessionId, "messages"],
    queryFn: () => api.sessions.getMessages(sessionId),
    refetchInterval: isRunning ? 3000 : false,
  });

  const { data: logsData } = useQuery({
    queryKey: ["session", sessionId, "logs"],
    queryFn: () => api.sessions.getLogs(sessionId),
    refetchInterval: isActive ? 3000 : false,
  });

  // Fetch the linked issue to display its description as the initial prompt.
  const { data: issueData } = useQuery({
    queryKey: ["issue", session.issue_id],
    queryFn: () => api.issues.get(session.issue_id),
    enabled: !!session.issue_id,
  });

  // Merge fetched logs with streamed logs, deduplicating by ID.
  const allLogs = useMemo(() => {
    const fetched = logsData?.data || [];
    const idSet = new Set(fetched.map((l) => l.id));
    const extra = streamedLogs.filter((l) => !idSet.has(l.id));
    return [...fetched, ...extra];
  }, [logsData?.data, streamedLogs]);

  const messages = messagesData?.data;

  // Prepend the issue description as a synthetic user message for turn 0
  // so the initial prompt is visible in the timeline.
  const allMessages = useMemo(() => {
    const issueDescription = issueData?.data?.description;
    const msgs = messages || [];
    if (!issueDescription) return msgs;
    // Only prepend if there's no user message for turn 0 already.
    const hasTurn0UserMsg = msgs.some((m) => m.role === "user" && m.turn_number === 0);
    if (hasTurn0UserMsg) return msgs;
    const syntheticMsg: SessionMessage = {
      id: -1,
      session_id: sessionId,
      org_id: session.org_id,
      turn_number: 0,
      role: "user",
      content: issueDescription,
      created_at: session.created_at,
    };
    return [syntheticMsg, ...msgs];
  }, [messages, issueData?.data?.description, sessionId, session.org_id, session.created_at]);

  const timelineEntries = useMemo(
    () => buildTimeline(allMessages, allLogs),
    [allMessages, allLogs]
  );

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
      });

      addSSEListener(eventSource, SSE_EVENT.DONE, (updated) => {
        queryClient.setQueryData(["session", sessionId], { data: updated });
        eventSource?.close();
        queryClient.invalidateQueries({ queryKey: ["session", sessionId, "logs"] });
        queryClient.invalidateQueries({ queryKey: ["session", sessionId, "messages"] });
      });

      eventSource.onerror = () => {
        eventSource?.close();
        queryClient.invalidateQueries({ queryKey: ["session", sessionId, "logs"] });

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

  const MAX_FILE_SIZE = 10 * 1024 * 1024; // 10 MB

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
    mutationFn: () => api.sessions.sendMessage(sessionId, message, attachments.length > 0 ? attachments : undefined, selectedModel || undefined),
    onSuccess: () => {
      setMessage("");
      setAttachments([]);
      if (textareaRef.current) {
        textareaRef.current.style.height = "auto";
      }
      queryClient.invalidateQueries({ queryKey: ["session", sessionId] });
      queryClient.invalidateQueries({ queryKey: ["session", sessionId, "messages"] });
    },
  });

  const endMutation = useMutation({
    mutationFn: () => api.sessions.endSession(sessionId),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["session", sessionId] });
    },
  });

  const { data: prData } = useQuery({
    queryKey: ["session", sessionId, "pr"],
    queryFn: () => api.sessions.getPR(sessionId).catch((err) => {
      // 404 means no PR exists yet — treat as empty data, not an error.
      if (err?.code === "NOT_FOUND") return { data: null };
      throw err;
    }),
  });
  const hasPR = !!prData?.data;
  const hasDiff = !!session.diff_stats;
  const canCreatePR = hasDiff && !hasPR && !isRunning;

  const [prQueued, setPRQueued] = useState(false);
  const createPRMutation = useMutation({
    mutationFn: () => api.sessions.createPR(sessionId),
    onSuccess: () => {
      setPRQueued(true);
      // Clear the queued banner after 30s if the PR hasn't appeared yet.
      setTimeout(() => setPRQueued(false), 30_000);
      queryClient.invalidateQueries({ queryKey: ["session", sessionId] });
      queryClient.invalidateQueries({ queryKey: ["session", sessionId, "pr"] });
    },
  });

  // Auto-resize textarea
  useEffect(() => {
    const el = textareaRef.current;
    if (!el) return;
    el.style.height = "auto";
    el.style.height = `${Math.min(el.scrollHeight, 200)}px`;
  }, [message]);

  // Scroll to bottom when timeline updates
  useEffect(() => {
    if (scrollRef.current) {
      scrollRef.current.scrollTop = scrollRef.current.scrollHeight;
    }
  }, [timelineEntries.length]);

  const hasContent = message.trim() || attachments.length > 0;

  function handleKeyDown(e: React.KeyboardEvent) {
    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault();
      if (hasContent && canSendMessage && !sendMutation.isPending) {
        sendMutation.mutate();
      }
    }
  }

  return (
    <div className="flex flex-col h-full">
      {/* Unified timeline */}
      <div ref={scrollRef} className="flex-1 overflow-y-auto space-y-2 p-4">
        {timelineEntries.length === 0 && !isRunning && (
          <div className="text-center py-8 text-sm text-muted-foreground">
            No activity yet. The session is processing its initial turn.
          </div>
        )}
        <ChatTimeline entries={timelineEntries} isRunning={isRunning} diffStats={session.diff_stats} onDiffClick={onDiffClick} />
      </div>

      {/* PR queued indicator */}
      {prQueued && !hasPR && (
        <div className="flex items-center gap-2 px-4 py-2 text-xs text-muted-foreground border-t bg-muted/30">
          <GitPullRequest className="h-3 w-3 shrink-0" />
          PR creation queued — it will appear shortly.
        </div>
      )}

      {/* Error display */}
      {(() => {
        const firstError = uploadError || [sendMutation, endMutation, createPRMutation].find(m => m.error)?.error;
        if (!firstError) return null;
        const msg = typeof firstError === "string" ? firstError : (firstError instanceof Error ? firstError.message : "An error occurred");
        return (
          <div className="flex items-center gap-2 px-4 py-2 text-xs text-destructive border-t bg-destructive/5">
            <AlertTriangle className="h-3 w-3 shrink-0" />
            {msg}
          </div>
        );
      })()}

      {/* Input bar */}
      <div className="border-t border-border p-3 bg-background">
        <div className="rounded-xl border border-border bg-muted/30 focus-within:border-ring focus-within:ring-1 focus-within:ring-ring">
          <Textarea
            ref={textareaRef}
            value={message}
            onChange={(e) => setMessage(e.target.value)}
            onKeyDown={handleKeyDown}
            placeholder={canSendMessage ? (isRunning ? "Send a message to the agent..." : "Send a follow-up message...") : "Session is not active"}
            disabled={!canSendMessage || sendMutation.isPending}
            className="min-h-[44px] max-h-[200px] resize-none border-none bg-transparent shadow-none focus-visible:ring-0"
          />

          {/* Attachment previews */}
          {(attachments.length > 0 || isUploading) && (
            <div className="flex flex-wrap items-center gap-2 px-3 pb-2">
              {attachments.map((url) => {
                const pathname = url.split("?")[0].split("#")[0];
                const isImage = /\.(png|jpe?g|gif|webp|svg)$/i.test(pathname);
                const fileName = pathname.split("/").pop() || "file";
                return (
                  <div key={url} className="relative group">
                    {isImage ? (
                      <img
                        src={url}
                        alt={fileName}
                        className="h-16 w-16 rounded-md object-cover border border-border"
                      />
                    ) : (
                      <div className="h-16 px-3 flex items-center rounded-md border border-border bg-muted text-xs text-muted-foreground">
                        {fileName}
                      </div>
                    )}
                    <button
                      type="button"
                      onClick={() => removeAttachment(url)}
                      className="absolute -top-1.5 -right-1.5 h-5 w-5 rounded-full bg-background border border-border flex items-center justify-center opacity-0 group-hover:opacity-100 transition-opacity"
                      aria-label={`Remove ${fileName}`}
                    >
                      <X className="h-3 w-3" />
                    </button>
                  </div>
                );
              })}
              {isUploading && (
                <div className="h-16 w-16 rounded-md border border-border bg-muted flex items-center justify-center">
                  <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />
                </div>
              )}
            </div>
          )}

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
                <SelectTrigger className="h-8 w-auto gap-1.5 border-none bg-transparent px-2 text-[13px] text-muted-foreground shadow-none hover:text-foreground focus:ring-0" aria-label="Model override">
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

            <div className="ml-auto flex items-center gap-1">
              {canCreatePR && (
                <Button
                  size="icon"
                  variant="ghost"
                  className="h-8 w-8 shrink-0 rounded-lg text-muted-foreground hover:text-foreground"
                  title="Create PR"
                  disabled={createPRMutation.isPending}
                  onClick={() => createPRMutation.mutate()}
                >
                  <GitPullRequest className="h-3.5 w-3.5" />
                </Button>
              )}
              {isRunning ? (
                <Button
                  size="icon"
                  variant="outline"
                  className="h-8 w-8 shrink-0 rounded-lg"
                  title="Stop session"
                  disabled={endMutation.isPending}
                  onClick={() => endMutation.mutate()}
                >
                  <Square className="h-3 w-3" />
                </Button>
              ) : (
                <Button
                  size="icon"
                  variant="default"
                  className="h-8 w-8 shrink-0 rounded-lg"
                  title="Send message"
                  disabled={!hasContent || !canSendMessage || sendMutation.isPending}
                  onClick={() => sendMutation.mutate()}
                >
                  <ArrowUp className="h-4 w-4" />
                </Button>
              )}
            </div>
          </div>
        </div>
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
  const terminalStatuses = new Set(["completed", "pr_created", "failed", "cancelled", "skipped"]);
  const [reviewParam, setReviewParam] = useQueryState("review");
  const maximized = reviewParam === "maximized";
  const [detailTab, setDetailTab] = useState<DetailTab>(maximized ? "changes" : "overview");
  const [showDetailPanel, setShowDetailPanel] = useState(true);
  const [detailWidth, setDetailWidth] = useState(DEFAULT_DETAIL);

  const handleDetailResize = useCallback((delta: number) => {
    // Negative delta = dragging left = panel gets wider
    setDetailWidth((w) => Math.min(MAX_DETAIL, Math.max(MIN_DETAIL, w - delta)));
  }, []);

  const openChangesTab = useCallback(() => {
    setDetailTab("changes");
    setShowDetailPanel(true);
  }, []);

  const toggleMaximize = useCallback(() => {
    if (maximized) {
      setReviewParam(null);
    } else {
      setReviewParam("maximized");
      setDetailTab("changes");
      setShowDetailPanel(true);
    }
  }, [maximized, setReviewParam]);

  const { data, isLoading, error } = useQuery({
    queryKey: ["session", id],
    queryFn: () => api.sessions.get(id),
  });

  const { data: membersData } = useQuery({
    queryKey: ["team", "members"],
    queryFn: () => api.team.listMembers(),
  });

  const session = data?.data;
  const members = membersData?.data ?? [];
  const isActive = session ? !terminalStatuses.has(session.status) : false;
  const isMultiTurn = session && session.current_turn > 0;

  const sessionDiff = session?.diff;
  const diffStats = useMemo(() => {
    if (!sessionDiff) return null;
    return parseDiffStats(sessionDiff);
  }, [sessionDiff]);

  // Comment count for the footer — React Query deduplicates with ChangesTab's query
  const { openCount: footerOpenCommentCount } = useReviewComments(id);

  if (isLoading) {
    return (
      <div className="flex items-center justify-center h-full">
        <div className="text-sm text-muted-foreground">Loading session...</div>
      </div>
    );
  }

  if (error || !session) {
    return (
      <div className="flex items-center justify-center h-full">
        <div className="text-sm text-muted-foreground">Failed to load session details.</div>
      </div>
    );
  }

  const status = statusConfig[session.status] || statusConfig.pending;

  const detailTabs: { value: DetailTab; label: string; count?: number }[] = [
    { value: "overview", label: "Overview" },
    { value: "changes", label: "Changes", count: diffStats?.filesChanged },
    { value: "validation", label: "Validation" },
  ];

  return (
    <div className="flex h-full">
      {/* Main chat area */}
      <div className={cn("flex-1 min-w-0 flex flex-col", maximized && "hidden")}>
        {/* Session header bar */}
        <div className="border-b border-border px-4 py-3 bg-background flex items-center justify-between shrink-0">
          <div className="min-w-0 flex-1">
            <div className="flex items-center gap-2">
              <h1 className="text-sm font-semibold text-foreground truncate">
                {sessionTitle(session)}
              </h1>
              <span className={`inline-flex items-center rounded-full px-2 py-0.5 text-[11px] font-medium shrink-0 ${status.color}`}>
                {status.label}
              </span>
              {diffStats && (
                <DiffStatsBadge
                  added={diffStats.added}
                  removed={diffStats.removed}
                  filesChanged={diffStats.filesChanged}
                  onClick={openChangesTab}
                />
              )}
            </div>
            <p className="text-[12px] text-muted-foreground mt-0.5">
              {agentTypeLabels[session.agent_type] || session.agent_type}
              {isMultiTurn && ` \u00B7 Turn ${session.current_turn}`}
            </p>
          </div>
          <div className="flex items-center gap-2 shrink-0">
            <AuditLogTrigger
              filters={{ session_id: session.id }}
              members={members}
              title="Session activity"
            />
            <Button
              variant="ghost"
              size="icon"
              className="h-8 w-8"
              onClick={() => setShowDetailPanel(!showDetailPanel)}
              title={showDetailPanel ? "Hide details" : "Show details"}
            >
              {showDetailPanel ? <PanelRightClose className="h-4 w-4" /> : <PanelRightOpen className="h-4 w-4" />}
            </Button>
          </div>
        </div>

        {/* Chat panel */}
        <div className="flex-1 min-h-0">
          <ChatPanel session={session} sessionId={id} isActive={isActive} onDiffClick={openChangesTab} />
        </div>

        {/* Session footer bar */}
        <SessionFooter
          status={session.status}
          currentTurn={session.current_turn}
          diffStats={diffStats}
          onDiffClick={openChangesTab}
          openCommentCount={footerOpenCommentCount}
          onCommentsClick={openChangesTab}
        />
      </div>

      {/* Detail panel (collapsible right sidebar) */}
      {(showDetailPanel || maximized) && (
        <>
        {!maximized && <ResizeHandle onResize={handleDetailResize} />}
        <div
          style={maximized ? undefined : { width: detailWidth }}
          className={cn(
            "border-l border-border bg-muted/20 flex flex-col shrink-0 overflow-hidden",
            maximized && "flex-1 border-l-0"
          )}
        >
          {/* Detail tabs */}
          <div className="flex items-center gap-0 border-b border-border px-2 shrink-0">
            {detailTabs.map((tab) => (
              <button
                key={tab.value}
                className={cn(
                  "relative px-3 py-2.5 text-[12px] font-medium transition-colors",
                  detailTab === tab.value
                    ? "text-foreground"
                    : "text-muted-foreground hover:text-foreground/80"
                )}
                onClick={() => setDetailTab(tab.value)}
              >
                <span className="inline-flex items-center gap-1.5">
                  {tab.label}
                  {tab.count != null && tab.count > 0 && (
                    <span className={cn(
                      "inline-flex items-center justify-center min-w-[18px] h-[18px] rounded-full px-1 text-[10px] font-semibold leading-none",
                      detailTab === tab.value
                        ? "bg-primary/15 text-primary"
                        : "bg-muted text-muted-foreground"
                    )}>
                      {tab.count}
                    </span>
                  )}
                </span>
                {detailTab === tab.value && (
                  <span className="absolute bottom-0 left-3 right-3 h-0.5 bg-[image:var(--gradient-primary)] rounded-full" />
                )}
              </button>
            ))}
          </div>

          {/* Detail content */}
          {detailTab === "changes" ? (
            <ChangesTab
              session={session}
              sessionId={id}
              maximized={maximized}
              onToggleMaximize={toggleMaximize}
            />
          ) : (
            <div className="flex-1 overflow-y-auto p-4">
              {detailTab === "overview" && <OverviewTab session={session} members={members} />}
              {detailTab === "validation" && <ValidationTab sessionId={id} />}
            </div>
          )}
        </div>
        </>
      )}
    </div>
  );
}
