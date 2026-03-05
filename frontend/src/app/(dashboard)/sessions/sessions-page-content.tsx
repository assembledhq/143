"use client";

import { useState, useRef, useCallback, useEffect } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { CalendarClock, RefreshCw, Layers, Wrench, Plus, X, AlertCircle } from "lucide-react";
import Link from "next/link";
import { useQueryState, parseAsString } from "nuqs";
import { PageHeader } from "@/components/page-header";
import { EmptyState } from "@/components/empty-state";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import type { AgentSession } from "@/lib/types";

const sessionStatusConfig: Record<string, { color: string; label: string }> = {
  active: { color: "bg-blue-100 text-blue-800", label: "Active" },
  completed: { color: "bg-green-100 text-green-800", label: "Completed" },
  failed: { color: "bg-red-100 text-red-800", label: "Failed" },
};

const triggeredByLabels: Record<string, string> = {
  scheduled: "Scheduled",
  manual: "Manual",
  fix_this: "Fix This",
};

const statusFilterTabs = [
  { value: "all", label: "All" },
  { value: "active", label: "Active" },
  { value: "completed", label: "Completed" },
  { value: "failed", label: "Failed" },
];

function formatTimeAgo(dateStr: string): string {
  const date = new Date(dateStr);
  const now = new Date();
  const diffMs = now.getTime() - date.getTime();
  const diffMins = Math.floor(diffMs / 60000);
  if (diffMins < 1) return "just now";
  if (diffMins < 60) return `${diffMins}m ago`;
  const diffHours = Math.floor(diffMins / 60);
  if (diffHours < 24) return `${diffHours}h ago`;
  const diffDays = Math.floor(diffHours / 24);
  if (diffDays < 30) return `${diffDays}d ago`;
  return date.toLocaleDateString();
}

function filterSessions(sessions: AgentSession[], filter: string | null): AgentSession[] {
  if (!filter || filter === "all") return sessions;
  return sessions.filter((s) => s.status === filter);
}

function SessionRow({ session }: { session: AgentSession }) {
  const status = sessionStatusConfig[session.status] || sessionStatusConfig.active;
  const isActive = session.status === "active";

  return (
    <Link
      href={`/sessions/${session.id}`}
      className="flex items-center justify-between py-3 px-4 border-b border-border last:border-b-0 hover:bg-muted/50 transition-colors cursor-pointer"
    >
      <div className="flex-1 min-w-0">
        <div className="flex items-center gap-2">
          <span className={`inline-flex items-center rounded-full px-2 py-0.5 text-[11px] font-medium ${status.color}`}>
            {isActive && (
              <span className="relative mr-1.5 flex h-2 w-2">
                <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-blue-400 opacity-75" />
                <span className="relative inline-flex rounded-full h-2 w-2 bg-blue-500" />
              </span>
            )}
            {status.label}
          </span>
          <span className="text-sm font-medium text-foreground truncate">
            {session.title}
          </span>
        </div>
        <div className="mt-1 flex items-center gap-3 text-xs text-muted-foreground">
          <Badge variant="outline" className="text-[11px] px-1.5 py-0">
            {session.type === "plan" ? (
              <><Layers className="mr-1 h-3 w-3 inline" />PM Analysis</>
            ) : (
              <><Wrench className="mr-1 h-3 w-3 inline" />Manual</>
            )}
          </Badge>
          <span>{triggeredByLabels[session.triggered_by] || session.triggered_by}</span>
          <span>{session.task_count} task{session.task_count !== 1 ? "s" : ""}</span>
          {session.active_run_count > 0 && (
            <span className="text-blue-600">{session.active_run_count} running</span>
          )}
          {session.completed_run_count > 0 && (
            <span className="text-green-600">{session.completed_run_count} done</span>
          )}
          {session.failed_run_count > 0 && (
            <span className="text-red-600">{session.failed_run_count} failed</span>
          )}
          <span>{formatTimeAgo(session.created_at)}</span>
        </div>
      </div>
    </Link>
  );
}

function SessionSection({ title, sessions, badge }: { title: string; sessions: AgentSession[]; badge?: React.ReactNode }) {
  if (sessions.length === 0) return null;
  return (
    <Card>
      <CardContent className="p-0">
        <div className="flex items-center gap-2 px-4 py-3 border-b border-border bg-muted/30">
          <span className="text-xs font-medium text-muted-foreground uppercase tracking-wider">
            {title}
          </span>
          {badge}
          <span className="text-xs text-muted-foreground">({sessions.length})</span>
        </div>
        {sessions.map((session) => (
          <SessionRow key={session.id} session={session} />
        ))}
      </CardContent>
    </Card>
  );
}

export function SessionsPageContent() {
  const queryClient = useQueryClient();
  const [statusFilter, setStatusFilter] = useQueryState("status", parseAsString);
  const [isManualComposerOpen, setIsManualComposerOpen] = useState(false);
  const [manualMessage, setManualMessage] = useState("");
  const [imageInput, setImageInput] = useState("");
  const [manualImages, setManualImages] = useState<string[]>([]);

  const [isAnalyzing, setIsAnalyzing] = useState(false);
  const [analyzeError, setAnalyzeError] = useState<string | null>(null);
  const sessionCountBeforeAnalyze = useRef<number | null>(null);
  const analyzeTimeoutRef = useRef<NodeJS.Timeout | null>(null);

  const { data, isLoading, error } = useQuery({
    queryKey: ["sessions"],
    queryFn: () => api.sessions.list({ limit: 50 }),
    refetchInterval: isAnalyzing ? 2000 : 10000,
  });

  // Detect when a new session appears after triggering analysis
  const allSessionCount = data?.data?.length ?? 0;
  useEffect(() => {
    if (isAnalyzing && sessionCountBeforeAnalyze.current !== null && allSessionCount > sessionCountBeforeAnalyze.current) {
      setIsAnalyzing(false);
      sessionCountBeforeAnalyze.current = null;
      if (analyzeTimeoutRef.current) {
        clearTimeout(analyzeTimeoutRef.current);
        analyzeTimeoutRef.current = null;
      }
    }
  }, [isAnalyzing, allSessionCount]);

  // Cleanup timeout on unmount
  useEffect(() => {
    return () => {
      if (analyzeTimeoutRef.current) clearTimeout(analyzeTimeoutRef.current);
    };
  }, []);

  const analyzeMutation = useMutation({
    mutationFn: () => api.pm.analyze(),
    onSuccess: () => {
      setIsAnalyzing(true);
      queryClient.invalidateQueries({ queryKey: ["sessions"] });
      // Timeout after 90 seconds if no new session appears
      analyzeTimeoutRef.current = setTimeout(() => {
        setIsAnalyzing(false);
        setAnalyzeError("Analysis may have failed or is taking longer than expected. Check your server logs for details.");
        sessionCountBeforeAnalyze.current = null;
      }, 90000);
    },
    onError: () => {
      setAnalyzeError("Failed to start analysis. Make sure the backend is running.");
    },
  });

  const handleAnalyze = useCallback(() => {
    setAnalyzeError(null);
    sessionCountBeforeAnalyze.current = allSessionCount;
    analyzeMutation.mutate();
  }, [allSessionCount, analyzeMutation]);

  const createManualSessionMutation = useMutation({
    mutationFn: () => api.sessions.createManual({ message: manualMessage.trim(), images: manualImages }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["sessions"] });
      setManualMessage("");
      setManualImages([]);
      setImageInput("");
    },
  });

  function addImage() {
    const trimmed = imageInput.trim();
    if (!trimmed) {
      return;
    }
    setManualImages((prev) => [...prev, trimmed]);
    setImageInput("");
  }

  const allSessions = data?.data ?? [];
  const sessions = filterSessions(allSessions, statusFilter);

  const showGrouped = !statusFilter || statusFilter === "all";

  const activeSessions = allSessions.filter((s) => s.status === "active");
  const completedSessions = allSessions.filter((s) => s.status === "completed");
  const failedSessions = allSessions.filter((s) => s.status === "failed");

  return (
    <div className="space-y-6">
      <PageHeader
        title="Sessions"
        description="Each PM analysis cycle or manual fix creates a session."
        action={
          <div className="flex items-center gap-2">
            <Button size="sm" variant="outline" onClick={() => setIsManualComposerOpen((prev) => !prev)}>
              <Plus className="mr-2 h-4 w-4" />
              New Manual Session
            </Button>
            <Button
              size="sm"
              onClick={handleAnalyze}
              disabled={analyzeMutation.isPending || isAnalyzing}
              title="Review open issues, prioritize them, and kick off agent runs"
            >
              <RefreshCw className={`mr-2 h-4 w-4 ${analyzeMutation.isPending || isAnalyzing ? "animate-spin" : ""}`} />
              {analyzeMutation.isPending ? "Starting..." : isAnalyzing ? "Analyzing..." : "Analyze Issues"}
            </Button>
          </div>
        }
      />

      {isAnalyzing && (
        <Card className="border-blue-200 bg-blue-50 dark:border-blue-800 dark:bg-blue-950/30">
          <CardContent className="flex items-center gap-3 py-3">
            <RefreshCw className="h-4 w-4 animate-spin text-blue-600 dark:text-blue-400" />
            <p className="text-sm text-blue-800 dark:text-blue-300">
              Analysis in progress — reviewing issues and generating a plan. This may take a minute...
            </p>
          </CardContent>
        </Card>
      )}

      {analyzeError && (
        <Card className="border-red-200 bg-red-50 dark:border-red-800 dark:bg-red-950/30">
          <CardContent className="flex items-center gap-3 py-3">
            <AlertCircle className="h-4 w-4 shrink-0 text-red-600 dark:text-red-400" />
            <p className="text-sm text-red-800 dark:text-red-300 flex-1">{analyzeError}</p>
            <Button size="sm" variant="ghost" className="shrink-0 h-6 px-2" onClick={() => setAnalyzeError(null)}>
              <X className="h-3 w-3" />
            </Button>
          </CardContent>
        </Card>
      )}

      {isManualComposerOpen && (
        <Card>
          <CardContent className="space-y-4 py-4">
            <div className="space-y-2">
              <Label htmlFor="manual-session-message">Message</Label>
              <Textarea
                id="manual-session-message"
                aria-label="Message"
                value={manualMessage}
                onChange={(event) => setManualMessage(event.target.value)}
                placeholder="Describe what you want the agent to do..."
              />
            </div>

            <div className="space-y-2">
              <Label htmlFor="manual-session-image">Image URL (optional)</Label>
              <div className="flex items-center gap-2">
                <Input
                  id="manual-session-image"
                  value={imageInput}
                  onChange={(event) => setImageInput(event.target.value)}
                  placeholder="https://..."
                />
                <Button type="button" variant="outline" onClick={addImage}>
                  Add Image
                </Button>
              </div>
              {manualImages.length > 0 && (
                <div className="flex flex-wrap gap-2">
                  {manualImages.map((imageURL) => (
                    <Badge key={imageURL} variant="secondary" className="gap-1">
                      {imageURL}
                      <Button
                        type="button"
                        variant="ghost"
                        size="sm"
                        onClick={() => setManualImages((prev) => prev.filter((value) => value !== imageURL))}
                        className="h-4 px-0.5"
                        aria-label={`Remove ${imageURL}`}
                      >
                        <X className="h-3 w-3" />
                      </Button>
                    </Badge>
                  ))}
                </div>
              )}
            </div>

            <div className="flex items-center justify-end gap-2">
              <Button
                type="button"
                variant="outline"
                onClick={() => setIsManualComposerOpen(false)}
                disabled={createManualSessionMutation.isPending}
              >
                Cancel
              </Button>
              <Button
                type="button"
                onClick={() => createManualSessionMutation.mutate()}
                disabled={manualMessage.trim().length === 0 || createManualSessionMutation.isPending}
              >
                Start Session
              </Button>
            </div>

            {(createManualSessionMutation.isPending || createManualSessionMutation.isSuccess) && (
              <p className="text-xs text-muted-foreground">Starting session...</p>
            )}
          </CardContent>
        </Card>
      )}

      <div className="flex items-center gap-1">
        {statusFilterTabs.map((tab) => (
          <Button
            key={tab.value}
            variant={(statusFilter ?? "all") === tab.value ? "default" : "ghost"}
            size="sm"
            className="text-xs"
            onClick={() => setStatusFilter(tab.value === "all" ? null : tab.value)}
          >
            {tab.label}
            {tab.value === "active" && activeSessions.length > 0 && (
              <span className="ml-1.5 rounded-full bg-blue-500 text-white text-[10px] px-1.5 py-0">{activeSessions.length}</span>
            )}
            {tab.value === "failed" && failedSessions.length > 0 && (
              <span className="ml-1.5 rounded-full bg-red-500 text-white text-[10px] px-1.5 py-0">{failedSessions.length}</span>
            )}
          </Button>
        ))}
      </div>

      {isLoading && (
        <Card>
          <CardContent className="py-12 text-center text-sm text-muted-foreground">
            Loading sessions...
          </CardContent>
        </Card>
      )}

      {error && (
        <Card>
          <CardContent className="py-12 text-center text-sm text-muted-foreground">
            Failed to load sessions. Make sure the backend is running.
          </CardContent>
        </Card>
      )}

      {!isLoading && !error && allSessions.length === 0 && (
        <EmptyState
          icon={CalendarClock}
          title="No sessions yet"
          description="Sessions are created when the PM agent runs an analysis or when you manually fix an issue."
        />
      )}

      {!isLoading && !error && allSessions.length > 0 && showGrouped && (
        <div className="space-y-4">
          <SessionSection
            title="Active"
            sessions={activeSessions}
            badge={
              activeSessions.length > 0 ? (
                <span className="relative flex h-2 w-2">
                  <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-blue-400 opacity-75" />
                  <span className="relative inline-flex rounded-full h-2 w-2 bg-blue-500" />
                </span>
              ) : undefined
            }
          />
          <SessionSection title="Failed" sessions={failedSessions} />
          <SessionSection title="Completed" sessions={completedSessions} />
        </div>
      )}

      {!isLoading && !error && allSessions.length > 0 && !showGrouped && (
        <Card>
          <CardContent className="p-0">
            <div className="flex items-center justify-between px-4 py-3 border-b border-border bg-muted/30">
              <span className="text-xs font-medium text-muted-foreground uppercase tracking-wider">
                {sessions.length} session{sessions.length !== 1 ? "s" : ""}
              </span>
            </div>
            {sessions.length === 0 ? (
              <div className="py-8 text-center text-sm text-muted-foreground">
                No sessions match this filter.
              </div>
            ) : (
              sessions.map((session) => (
                <SessionRow key={session.id} session={session} />
              ))
            )}
          </CardContent>
        </Card>
      )}
    </div>
  );
}
