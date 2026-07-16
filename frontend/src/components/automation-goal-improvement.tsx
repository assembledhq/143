"use client";

import { useRef, useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import Link from "next/link";
import {
  ChevronDown,
  ExternalLink,
  History,
  Loader2,
  RefreshCw,
  Sparkles,
} from "lucide-react";
import { ApiError, api } from "@/lib/api";
import type { Automation, AutomationGoalImprovement } from "@/lib/types";
import { formatDateTime } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import { ButtonGroup } from "@/components/ui/button-group";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";

interface AutomationGoalImprovementProps {
  automationId?: string;
  name: string;
  goal: string;
  repositoryId?: string;
  scope?: string;
  config?: Record<string, unknown>;
  disabled?: boolean;
  onDraftApply?: (goal: string) => void;
  onSavedApply?: (automation: Automation) => void;
}

export function AutomationGoalImprovementControl({
  automationId,
  name,
  goal,
  repositoryId,
  scope,
  config,
  disabled,
  onDraftApply,
  onSavedApply,
}: AutomationGoalImprovementProps) {
  const [proposal, setProposal] = useState<AutomationGoalImprovement | null>(
    null,
  );
  const [historyOpen, setHistoryOpen] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [lastMode, setLastMode] = useState<"fast" | "deep">("fast");
  const [reviewNotesOpen, setReviewNotesOpen] = useState(false);
  const [editedGoalIsEmpty, setEditedGoalIsEmpty] = useState(false);
  const editedGoalRef = useRef<HTMLTextAreaElement>(null);
  const lastImproveAtRef = useRef(0);
  const shouldPoll =
    proposal?.id &&
    (proposal.status === "pending" || proposal.status === "running");

  const proposalQuery = useQuery({
    queryKey: ["automation-goal-improvement", proposal?.id],
    queryFn: () => api.automations.getGoalImprovement(proposal!.id),
    enabled: Boolean(shouldPoll),
    refetchInterval: (query) => {
      const status = query.state.data?.data.status;
      return status === "pending" || status === "running" ? 3000 : false;
    },
  });
  const proposalIsTerminal =
    proposal?.status &&
    proposal.status !== "pending" &&
    proposal.status !== "running";
  const displayedProposal = proposalIsTerminal
    ? proposal
    : (proposalQuery.data?.data ?? proposal);
  const historyQuery = useQuery({
    queryKey: ["automation-goal-improvements", automationId],
    queryFn: () => api.automations.listGoalImprovements(automationId!, 10),
    enabled: Boolean(automationId && historyOpen),
  });

  const improveMutation = useMutation({
    mutationFn: (mode: "fast" | "deep") => {
      setLastMode(mode);
      setError(null);
      if (automationId) {
        return api.automations.improveGoalSaved(automationId, {
          mode,
          include_recent_runs: 10,
        });
      }
      return api.automations.improveGoalDraft({
        mode,
        name,
        goal,
        repository_id: repositoryId,
        scope,
        config,
      });
    },
    onSuccess: (response) => {
      setProposal(response.data);
      setReviewNotesOpen(false);
      setEditedGoalIsEmpty(false);
    },
    onError: (err) => setError(errorMessage(err)),
  });

  const applyMutation = useMutation({
    mutationFn: () => {
      const proposedGoal =
        editedGoalRef.current?.value ?? displayedProposal?.proposed_goal ?? "";
      if (!displayedProposal?.id || !proposedGoal.trim()) {
        throw new Error("Improvement has no proposed goal.");
      }
      if (!automationId) {
        onDraftApply?.(proposedGoal);
        return Promise.resolve(null);
      }
      return api.automations.applyGoalImprovement(
        automationId,
        displayedProposal.id,
        {
          expected_base_goal_hash: displayedProposal.base_goal_hash,
          proposed_goal: proposedGoal,
        },
      );
    },
    onSuccess: (response) => {
      if (response) {
        onSavedApply?.(response.data);
      }
      setProposal(null);
      setHistoryOpen(false);
      setError(null);
    },
    onError: (err) => setError(errorMessage(err)),
  });

  const cancelMutation = useMutation({
    mutationFn: () => {
      if (!displayedProposal?.id) {
        throw new Error("Improvement has no proposal to cancel.");
      }
      return api.automations.cancelGoalImprovement(displayedProposal.id);
    },
    onSuccess: (response) => {
      setProposal(response.data);
      setError(null);
    },
    onError: (err) => setError(errorMessage(err)),
  });

  const isPending =
    improveMutation.isPending ||
    applyMutation.isPending ||
    cancelMutation.isPending;
  const canImprove = !disabled && goal.trim().length > 0 && !isPending;
  const deepDisabled = !automationId && !repositoryId;
  const isRunningProposal =
    displayedProposal?.status === "running" ||
    displayedProposal?.status === "pending";
  const analysisSessionID = displayedProposal?.analysis_session_id;
  const sessionQuery = useQuery({
    queryKey: ["automation-goal-improvement-session", analysisSessionID],
    queryFn: () => api.sessions.get(analysisSessionID!),
    enabled: Boolean(isRunningProposal && analysisSessionID),
    refetchInterval: isRunningProposal ? 3000 : false,
  });
  const threadQuery = useQuery({
    queryKey: ["automation-goal-improvement-session-threads", analysisSessionID],
    queryFn: () => api.sessions.listThreads(analysisSessionID!),
    enabled: Boolean(isRunningProposal && analysisSessionID),
    refetchInterval: isRunningProposal ? 3000 : false,
  });
  const transcriptThreadID = threadQuery.data?.data[0]?.id;
  const transcriptQuery = useQuery({
    queryKey: [
      "automation-goal-improvement-session-transcript",
      analysisSessionID,
      transcriptThreadID,
    ],
    queryFn: () =>
      api.sessions.getThreadTranscriptWindow(
        analysisSessionID!,
        transcriptThreadID!,
        { position: "latest", limitTurns: 1 },
      ),
    enabled: Boolean(isRunningProposal && analysisSessionID && transcriptThreadID),
    refetchInterval: isRunningProposal ? 3000 : false,
  });
  const transcriptPreview = latestTranscriptPreview(transcriptQuery.data?.data);

  const requestImprove = (mode: "fast" | "deep") => {
    const now = Date.now();
    if (now - lastImproveAtRef.current < 750) {
      return;
    }
    lastImproveAtRef.current = now;
    improveMutation.mutate(mode);
  };

  return (
    <>
      <ButtonGroup size="sm">
        <Button
          type="button"
          variant="outline"
          size="sm"
          className="rounded-r-none"
          disabled={!canImprove}
          onClick={() => requestImprove("fast")}
        >
          {improveMutation.isPending ? (
            <Loader2 className="h-4 w-4 animate-spin" />
          ) : (
            <Sparkles className="h-4 w-4" />
          )}
          Improve goal
        </Button>
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button
              type="button"
              variant="outline"
              size="sm"
              className="rounded-l-none border-l-0 px-2"
              disabled={!canImprove}
              aria-label="Goal improvement options"
            >
              <ChevronDown className="h-4 w-4" />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end">
            <DropdownMenuItem onClick={() => requestImprove("fast")}>
              Fast improve
            </DropdownMenuItem>
            <DropdownMenuItem
              disabled={deepDisabled}
              onClick={() => requestImprove("deep")}
            >
              Deep improve with agent
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
        {automationId && (
          <Button
            type="button"
            variant="ghost"
            size="icon"
            disabled={disabled}
            aria-label="Goal improvement history"
            onClick={() => setHistoryOpen(true)}
          >
            <History className="h-4 w-4" />
          </Button>
        )}
      </ButtonGroup>
      {error && <p className="text-xs text-destructive">{error}</p>}
      <Dialog
        open={proposal !== null}
        onOpenChange={(open) => !open && setProposal(null)}
      >
        <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-2xl">
          <DialogHeader>
            <DialogTitle>
              {isRunningProposal ? "Improving goal" : "Review improved goal"}
            </DialogTitle>
            <DialogDescription>
              {isRunningProposal
                ? "The analysis agent is reviewing the repository and recent automation evidence."
                : "Apply replaces the current goal text. Discard leaves it unchanged."}
            </DialogDescription>
          </DialogHeader>
          {displayedProposal && isRunningProposal && (
            <div className="flex items-center gap-3 rounded-md border border-border p-4 text-sm text-muted-foreground">
              <Loader2 className="h-4 w-4 animate-spin text-foreground" />
              <div className="space-y-1">
                <p className="font-medium text-foreground">
                  Deep improvement is running
                </p>
                {sessionQuery.data?.data && (
                  <p className="text-xs text-muted-foreground">
                    Session status: {sessionQuery.data.data.status}
                  </p>
                )}
                {transcriptPreview && (
                  <p className="line-clamp-2 text-xs text-muted-foreground">
                    {transcriptPreview}
                  </p>
                )}
                {displayedProposal.analysis_session_id && (
                  <Button
                    variant="link"
                    size="sm"
                    className="h-auto p-0"
                    asChild
                  >
                    <Link
                      href={`/sessions/${displayedProposal.analysis_session_id}`}
                    >
                      Open analysis session
                      <ExternalLink className="h-3.5 w-3.5" />
                    </Link>
                  </Button>
                )}
              </div>
            </div>
          )}
          {displayedProposal && displayedProposal.status === "failed" && (
            <div className="rounded-md border border-destructive/30 bg-destructive/10 p-4 text-sm text-destructive">
              {displayedProposal.error_message ?? "Deep improvement failed."}
            </div>
          )}
          {displayedProposal && displayedProposal.status === "completed" && (
            <div className="space-y-3">
              <div className="space-y-1.5">
                <Label
                  htmlFor="revised-goal"
                  className="text-xs font-medium text-muted-foreground"
                >
                  Revised goal
                </Label>
                <Textarea
                  key={displayedProposal.id}
                  id="revised-goal"
                  ref={editedGoalRef}
                  defaultValue={displayedProposal.proposed_goal ?? ""}
                  rows={12}
                  className="min-h-56 resize-y text-sm leading-6"
                  onChange={(e) =>
                    setEditedGoalIsEmpty(e.target.value.trim() === "")
                  }
                />
                <p className="text-xs text-muted-foreground">
                  Edit directly, then apply when it reads correctly.
                </p>
              </div>
              <Button
                type="button"
                variant="ghost"
                size="sm"
                className="px-0 text-muted-foreground"
                onClick={() => setReviewNotesOpen((open) => !open)}
              >
                <ChevronDown
                  className={
                    reviewNotesOpen
                      ? "h-4 w-4 rotate-180 transition-transform"
                      : "h-4 w-4 transition-transform"
                  }
                />
                {reviewNotesOpen ? "Hide review notes" : "Show review notes"}
              </Button>
              {reviewNotesOpen && (
                <div className="space-y-4 rounded-md border border-border bg-muted/20 p-3">
                  <div className="space-y-1.5">
                    <p className="text-xs font-medium text-muted-foreground">
                      Current goal
                    </p>
                    <Textarea
                      value={goal}
                      readOnly
                      rows={5}
                      className="resize-y bg-background text-sm"
                    />
                  </div>
                  <GoalDiff
                    current={goal}
                    proposed={displayedProposal.proposed_goal ?? ""}
                  />
                  <div className="grid gap-3 md:grid-cols-2">
                    <ReviewList
                      title="Changes"
                      items={displayedProposal.proposal?.changes}
                    />
                    <ReviewList
                      title="Warnings"
                      items={displayedProposal.warnings}
                      empty="No warnings."
                    />
                  </div>
                  {displayedProposal.proposal?.rationale && (
                    <p className="text-sm text-muted-foreground">
                      {displayedProposal.proposal.rationale}
                    </p>
                  )}
                  {displayedProposal.confidence && (
                    <p className="text-xs text-muted-foreground">
                      Confidence: {displayedProposal.confidence}
                    </p>
                  )}
                </div>
              )}
            </div>
          )}
          {displayedProposal && displayedProposal.status === "canceled" && (
            <div className="rounded-md border border-border bg-muted/20 p-4 text-sm text-muted-foreground">
              {displayedProposal.error_message ?? "Goal improvement canceled."}
            </div>
          )}
          <DialogFooter>
            {error && error.includes("Regenerate from the current goal") && (
              <Button
                type="button"
                variant="secondary"
                disabled={improveMutation.isPending}
                onClick={() => requestImprove(lastMode)}
              >
                {improveMutation.isPending ? (
                  <Loader2 className="h-4 w-4 animate-spin" />
                ) : (
                  <RefreshCw className="h-4 w-4" />
                )}
                Regenerate
              </Button>
            )}
            <Button
              type="button"
              variant="outline"
              onClick={() => setProposal(null)}
            >
              {isRunningProposal ? "Close" : "Discard"}
            </Button>
            {isRunningProposal && (
              <Button
                type="button"
                variant="destructive"
                disabled={cancelMutation.isPending}
                onClick={() => cancelMutation.mutate()}
              >
                {cancelMutation.isPending && (
                  <Loader2 className="h-4 w-4 animate-spin" />
                )}
                Cancel
              </Button>
            )}
            <Button
              type="button"
              disabled={
                applyMutation.isPending ||
                displayedProposal?.status !== "completed" ||
                !displayedProposal?.proposed_goal ||
                editedGoalIsEmpty
              }
              onClick={() => applyMutation.mutate()}
            >
              {applyMutation.isPending && (
                <Loader2 className="h-4 w-4 animate-spin" />
              )}
              Apply
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
      <Dialog open={historyOpen} onOpenChange={setHistoryOpen}>
        <DialogContent className="sm:max-w-2xl">
          <DialogHeader>
            <DialogTitle>Goal improvement history</DialogTitle>
            <DialogDescription>
              Recent proposals for this automation.
            </DialogDescription>
          </DialogHeader>
          <div className="max-h-96 space-y-2 overflow-auto">
            {historyQuery.isLoading && (
              <div className="flex items-center gap-2 text-sm text-muted-foreground">
                <Loader2 className="h-4 w-4 animate-spin" />
                Loading proposals
              </div>
            )}
            {historyQuery.data?.data.length === 0 && (
              <p className="text-sm text-muted-foreground">No proposals yet.</p>
            )}
            {historyQuery.data?.data.map((item) => (
              <Button
                key={item.id}
                type="button"
                variant="ghost"
                className="h-auto w-full justify-start rounded-md border border-border p-3 text-left"
                onClick={() => {
                  setProposal(item);
                  setHistoryOpen(false);
                  setReviewNotesOpen(false);
                  setEditedGoalIsEmpty(false);
                }}
              >
                <div className="min-w-0 space-y-1">
                  <div className="flex flex-wrap items-center gap-2 text-sm font-medium text-foreground">
                    <span>{item.mode}</span>
                    <span className="text-muted-foreground">/</span>
                    <span>{item.status}</span>
                    {item.applied_at && (
                      <span className="text-muted-foreground">applied</span>
                    )}
                  </div>
                  <p className="truncate text-xs text-muted-foreground">
                    {formatDateTime(item.created_at)}
                  </p>
                </div>
              </Button>
            ))}
          </div>
        </DialogContent>
      </Dialog>
    </>
  );
}

function ReviewList({
  title,
  items,
  empty = "None.",
}: {
  title: string;
  items?: string[];
  empty?: string;
}) {
  const values = items?.filter(Boolean) ?? [];
  return (
    <div className="space-y-1.5">
      <p className="text-xs font-medium text-muted-foreground">{title}</p>
      {values.length > 0 ? (
        <ul className="list-disc space-y-1 pl-4 text-sm text-foreground">
          {values.map((item) => (
            <li key={item}>{item}</li>
          ))}
        </ul>
      ) : (
        <p className="text-sm text-muted-foreground">{empty}</p>
      )}
    </div>
  );
}

function GoalDiff({
  current,
  proposed,
}: {
  current: string;
  proposed: string;
}) {
  const currentLines = compactLines(current);
  const proposedLines = compactLines(proposed);
  const currentSet = new Set(currentLines);
  const proposedSet = new Set(proposedLines);
  const removed = currentLines.filter((line) => !proposedSet.has(line));
  const added = proposedLines.filter((line) => !currentSet.has(line));

  if (removed.length === 0 && added.length === 0) {
    return null;
  }

  return (
    <div className="space-y-1.5">
      <p className="text-xs font-medium text-muted-foreground">
        Proposed changes
      </p>
      <div className="max-h-48 overflow-auto rounded-md border border-border bg-muted/20 p-3 font-mono text-xs">
        {removed.slice(0, 8).map((line, index) => (
          <p key={`removed-${index}-${line}`} className="text-destructive">
            - {line}
          </p>
        ))}
        {added.slice(0, 8).map((line, index) => (
          <p key={`added-${index}-${line}`} className="text-foreground">
            + {line}
          </p>
        ))}
      </div>
    </div>
  );
}

function compactLines(value: string): string[] {
  return value
    .split(/\n+/)
    .map((line) => line.trim())
    .filter(Boolean);
}

function latestTranscriptPreview(
  turns?: Array<{ entries: Array<{ summary?: string; content?: string }> }>,
): string | null {
  if (!turns?.length) {
    return null;
  }
  for (let i = turns.length - 1; i >= 0; i -= 1) {
    const entries = turns[i]?.entries ?? [];
    for (let j = entries.length - 1; j >= 0; j -= 1) {
      const text = entries[j]?.summary || entries[j]?.content;
      if (text?.trim()) {
        return text.trim();
      }
    }
  }
  return null;
}

function errorMessage(err: unknown): string {
  if (err instanceof ApiError) {
    if (err.code === "STALE_GOAL") {
      return "The automation goal changed since this proposal was generated. Regenerate from the current goal.";
    }
    return err.message;
  }
  if (err instanceof Error) return err.message;
  return "Goal improvement failed.";
}
