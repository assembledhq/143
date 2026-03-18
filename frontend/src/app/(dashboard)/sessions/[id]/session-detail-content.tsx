"use client";

import { useCallback, useRef, useState, useEffect, type CSSProperties } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import {
  AlertTriangle,
  ArrowUp,
  ExternalLink,
  RefreshCw,
  CheckCircle2,
  XCircle,
  MinusCircle,
  Square,
  PanelRightOpen,
  PanelRightClose,
} from "lucide-react";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, TableHeader, TableBody, TableRow, TableHead, TableCell } from "@/components/ui/table";
import { Textarea } from "@/components/ui/textarea";
import { LogViewer } from "@/components/log-viewer";
import { DiffViewer } from "@/components/diff-viewer";
import { api } from "@/lib/api";
import { SSE_EVENT, addSSEListener } from "@/lib/sse";
import type { Session, SessionMessage, User, Validation } from "@/lib/types";
import { AuditLogTrigger } from "@/components/audit/audit-log-trigger";
import { ResizeHandle } from "@/components/resize-handle";
import { cn } from "@/lib/utils";

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

function confidenceColor(score: number): string {
  if (score > 0.8) return "text-emerald-600 dark:text-emerald-400";
  if (score >= 0.5) return "text-amber-600 dark:text-amber-400";
  return "text-destructive";
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

type DetailTab = "overview" | "changes" | "validation" | "logs";

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
            {session.triggered_by_user_id && (
              <div>
                <span className="text-xs font-medium text-muted-foreground/70 tracking-wider">Triggered by</span>
                <p className="mt-1 font-medium">
                  {members.find((m) => m.id === session.triggered_by_user_id)?.name || "Unknown user"}
                </p>
              </div>
            )}
            {session.confidence_score != null && (
              <div>
                <span className="text-xs font-medium text-muted-foreground/70 tracking-wider">Confidence</span>
                <p className={`mt-1 font-medium ${confidenceColor(session.confidence_score)}`}>
                  {(session.confidence_score * 100).toFixed(0)}%
                </p>
              </div>
            )}
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
            <p className="text-sm">{session.result_summary}</p>
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

function ChangesTab({ session, sessionId }: { session: Session; sessionId: string }) {
  const { data: prData, isLoading: prLoading } = useQuery({
    queryKey: ["session", sessionId, "pr"],
    queryFn: () => api.sessions.getPR(sessionId),
  });

  const pr = prData?.data;

  const prStatusColor: Record<string, string> = {
    open: "bg-emerald-500/10 text-emerald-700 dark:text-emerald-400",
    merged: "bg-purple-500/10 text-purple-700 dark:text-purple-400",
    closed: "bg-red-500/10 text-red-700 dark:text-red-400",
  };

  return (
    <div className="space-y-4">
      {pr && (
        <Card>
          <CardContent className="pt-6 space-y-4">
            <div className="flex items-start justify-between">
              <div>
                <h3 className="text-sm font-medium">{pr.title}</h3>
                <p className="text-xs text-muted-foreground mt-1">{pr.github_repo} #{pr.github_pr_number}</p>
              </div>
              <a href={pr.github_pr_url} target="_blank" rel="noopener noreferrer">
                <Button variant="outline" size="sm">
                  <ExternalLink className="mr-1.5 h-3 w-3" />
                  View on GitHub
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
              <div>
                <span className="text-muted-foreground">Branch: </span>
                <code className="text-xs bg-muted px-1 py-0.5 rounded">{pr.branch_name}</code>
              </div>
            </div>

            {pr.body && (
              <div className="text-sm text-muted-foreground border-t border-border pt-3">
                <p className="whitespace-pre-wrap">{pr.body}</p>
              </div>
            )}
          </CardContent>
        </Card>
      )}

      {prLoading && (
        <div className="text-center text-sm text-muted-foreground py-2">Loading PR details...</div>
      )}

      {session.diff ? (
        <DiffViewer diff={session.diff} />
      ) : (
        <div className="py-8 text-center text-sm text-muted-foreground">
          No diff available for this session.
        </div>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// Main chat panel
// ---------------------------------------------------------------------------

function ChatPanel({ session, sessionId }: { session: Session; sessionId: string }) {
  const queryClient = useQueryClient();
  const [message, setMessage] = useState("");
  const textareaRef = useRef<HTMLTextAreaElement>(null);
  const scrollRef = useRef<HTMLDivElement>(null);

  const { data: messagesData } = useQuery({
    queryKey: ["session", sessionId, "messages"],
    queryFn: () => api.sessions.getMessages(sessionId),
    refetchInterval: session.status === "running" ? 3000 : false,
  });

  const messages = messagesData?.data || [];
  const isIdle = session.status === "idle";
  const isRunning = session.status === "running";

  const sendMutation = useMutation({
    mutationFn: () => api.sessions.sendMessage(sessionId, message),
    onSuccess: () => {
      setMessage("");
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

  // Auto-resize textarea
  useEffect(() => {
    const el = textareaRef.current;
    if (!el) return;
    el.style.height = "auto";
    el.style.height = `${Math.min(el.scrollHeight, 200)}px`;
  }, [message]);

  // Scroll to bottom when messages update
  useEffect(() => {
    if (scrollRef.current) {
      scrollRef.current.scrollTop = scrollRef.current.scrollHeight;
    }
  }, [messages]);

  function handleKeyDown(e: React.KeyboardEvent) {
    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault();
      if (message.trim() && isIdle && !sendMutation.isPending) {
        sendMutation.mutate();
      }
    }
  }

  return (
    <div className="flex flex-col h-full">
      {/* Message thread */}
      <div ref={scrollRef} className="flex-1 overflow-y-auto space-y-3 p-4">
        {messages.length === 0 && !isRunning && (
          <div className="text-center py-8 text-sm text-muted-foreground">
            No messages yet. The session is processing its initial turn.
          </div>
        )}
        {messages.map((msg: SessionMessage) => (
          <div
            key={msg.id}
            className={`flex ${msg.role === "user" ? "justify-end" : "justify-start"}`}
          >
            <div
              className={cn(
                "max-w-[80%] rounded-lg px-3 py-2 text-sm",
                msg.role === "user"
                  ? "bg-primary text-primary-foreground"
                  : "bg-muted"
              )}
            >
              <p className="whitespace-pre-wrap">{msg.content}</p>
              <p className={cn(
                "text-[10px] mt-1",
                msg.role === "user" ? "text-primary-foreground/70" : "text-muted-foreground"
              )}>
                Turn {msg.turn_number}
              </p>
            </div>
          </div>
        ))}
        {isRunning && (
          <div className="flex justify-start">
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
        )}
      </div>

      {/* Error display */}
      {(sendMutation.error || endMutation.error) && (
        <div className="flex items-center gap-2 px-4 py-2 text-xs text-destructive border-t bg-destructive/5">
          <AlertTriangle className="h-3 w-3 shrink-0" />
          {sendMutation.error instanceof Error ? sendMutation.error.message : endMutation.error instanceof Error ? endMutation.error.message : "An error occurred"}
        </div>
      )}

      {/* Input bar */}
      <div className="border-t border-border p-3 bg-background">
        <div className="flex items-end gap-2">
          <Textarea
            ref={textareaRef}
            value={message}
            onChange={(e) => setMessage(e.target.value)}
            onKeyDown={handleKeyDown}
            placeholder={isIdle ? "Send a follow-up message..." : isRunning ? "Agent is working..." : "Session is not active"}
            disabled={!isIdle || sendMutation.isPending}
            className="min-h-[44px] max-h-[200px] resize-none"
          />
          <div className="flex flex-col gap-1">
            <Button
              size="icon"
              variant="default"
              className="h-8 w-8 shrink-0"
              disabled={!message.trim() || !isIdle || sendMutation.isPending}
              onClick={() => sendMutation.mutate()}
            >
              <ArrowUp className="h-4 w-4" />
            </Button>
            {isIdle && (
              <Button
                size="icon"
                variant="outline"
                className="h-8 w-8 shrink-0"
                title="End session"
                disabled={endMutation.isPending}
                onClick={() => endMutation.mutate()}
              >
                <Square className="h-3 w-3" />
              </Button>
            )}
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
  const queryClient = useQueryClient();
  const [detailTab, setDetailTab] = useState<DetailTab>("overview");
  const [showDetailPanel, setShowDetailPanel] = useState(true);
  const [detailWidth, setDetailWidth] = useState(DEFAULT_DETAIL);

  const handleDetailResize = useCallback((delta: number) => {
    // Negative delta = dragging left = panel gets wider
    setDetailWidth((w) => Math.min(MAX_DETAIL, Math.max(MIN_DETAIL, w - delta)));
  }, []);

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

  // Update the session query cache when we receive status updates via SSE.
  const handleSessionUpdate = useCallback(
    (updated: Session) => {
      queryClient.setQueryData(["session", id], { data: updated });
    },
    [queryClient, id]
  );

  // Subscribe to session status changes via SSE.
  const apiBase = process.env.NEXT_PUBLIC_API_URL || "";
  useEffect(() => {
    if (!isActive) return;

    const eventSource = new EventSource(
      `${apiBase}/api/v1/sessions/${id}/logs/stream`,
      { withCredentials: true }
    );

    addSSEListener(eventSource, SSE_EVENT.STATUS, handleSessionUpdate);
    addSSEListener(eventSource, SSE_EVENT.DONE, (session) => {
      handleSessionUpdate(session);
      eventSource.close();
    });

    eventSource.onerror = () => {
      eventSource.close();
    };

    return () => {
      eventSource.close();
    };
  }, [id, apiBase, isActive, handleSessionUpdate]);

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

  const detailTabs: { value: DetailTab; label: string }[] = [
    { value: "overview", label: "Overview" },
    { value: "changes", label: "Changes" },
    { value: "validation", label: "Validation" },
    { value: "logs", label: "Logs" },
  ];

  return (
    <div className="flex h-full">
      {/* Main chat area */}
      <div className="flex-1 min-w-0 flex flex-col">
        {/* Session header bar */}
        <div className="border-b border-border px-4 py-3 bg-background flex items-center justify-between shrink-0">
          <div className="min-w-0 flex-1">
            <div className="flex items-center gap-2">
              <h1 className="text-sm font-semibold text-foreground truncate">
                {session.result_summary || `Session ${session.id.slice(0, 8)}`}
              </h1>
              <span className={`inline-flex items-center rounded-full px-2 py-0.5 text-[11px] font-medium shrink-0 ${status.color}`}>
                {status.label}
              </span>
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
          <ChatPanel session={session} sessionId={id} />
        </div>
      </div>

      {/* Detail panel (collapsible right sidebar) */}
      {showDetailPanel && (
        <>
        <ResizeHandle onResize={handleDetailResize} />
        <div style={{ width: detailWidth }} className="border-l border-border bg-muted/20 flex flex-col shrink-0 overflow-hidden">
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
                {tab.label}
                {detailTab === tab.value && (
                  <span className="absolute bottom-0 left-3 right-3 h-0.5 bg-[image:var(--gradient-primary)] rounded-full" />
                )}
              </button>
            ))}
          </div>

          {/* Detail content */}
          <div className="flex-1 overflow-y-auto p-4">
            {detailTab === "overview" && <OverviewTab session={session} members={members} />}
            {detailTab === "changes" && <ChangesTab session={session} sessionId={id} />}
            {detailTab === "validation" && <ValidationTab sessionId={id} />}
            {detailTab === "logs" && <LogViewer runId={id} isActive={isActive} />}
          </div>
        </div>
        </>
      )}
    </div>
  );
}
