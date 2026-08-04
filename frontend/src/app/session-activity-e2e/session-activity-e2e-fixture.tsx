"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useSearchParams } from "next/navigation";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { ChatTimeline } from "@/components/chat-timeline";
import { SessionActivityTimeline } from "@/components/session-activity-timeline";
import { useSessionActivityCapsulesEnabled } from "@/hooks/use-session-activity-capsules-enabled";
import { useSessionActivityDetail } from "@/hooks/use-session-activity-detail";
import { useTranscriptPrependCompensation } from "@/hooks/use-transcript-prepend-compensation";
import { SSE_EVENT, addSSEListener, type SessionLifecycleEvent } from "@/lib/sse";
import type { TimelineEntry } from "@/lib/timeline";
import type { ApplicationConfig, SessionLog, SessionMessage, SessionTranscriptTurn, SingleResponse } from "@/lib/types";
import { shouldInvalidateForActivityLifecycleEvent } from "@/app/(dashboard)/sessions/[id]/session-detail-state";

const startedAt = "2026-08-03T12:00:00Z";
const phaseID = "10000000-0000-0000-0000-000000000001";
const resumedPhaseID = "10000000-0000-0000-0000-000000000002";

interface FixtureTranscriptSnapshot {
  entries: TimelineEntry[];
  turns: SessionTranscriptTurn[];
  is_running: boolean;
}

function log(id: number, level: string, message: string, createdAt: string, activityPhaseID = phaseID): SessionLog {
  return {
    id, session_id: "fixture-session", thread_id: "fixture-thread", level, message,
    metadata: level === "tool_use" ? { type: "tool_use", tool: "shell", input: { command: "npm test" }, call_id: `call-${id}` } : null,
    turn_number: 1, created_at: createdAt, message_bytes: message.length,
    message_chars: message.length, message_truncated: false, activity_phase_id: activityPhaseID,
  };
}

function message(id: number, role: "user" | "assistant", content: string, createdAt: string, extra: Partial<SessionMessage> = {}): SessionMessage {
  return {
    id, session_id: "fixture-session", org_id: "fixture-org", thread_id: "fixture-thread",
    turn_number: 1, role, content, created_at: createdAt, ...extra,
  };
}

export function SessionActivityE2EFixture() {
  const queryClient = useQueryClient();
  const scrollRef = useRef<HTMLDivElement>(null);
  const [status, setStatus] = useState<"running" | "completed" | "interrupted">("running");
  const [resumedKind, setResumedKind] = useState<"inbox" | "human_input" | "recovery" | null>(null);
  const [resumedStatus, setResumedStatus] = useState<"running" | "completed">("running");
  const { detail, setDetail, mutation: detailMutation } = useSessionActivityDetail();
  const { enabled: capsulesEnabled } = useSessionActivityCapsulesEnabled();
  const [queued, setQueued] = useState(true);
  const [showHistorical, setShowHistorical] = useState(false);
  const [humanInputRequested, setHumanInputRequested] = useState(false);
  const [humanInputAnswered, setHumanInputAnswered] = useState(false);
  const [userScrollEpoch, setUserScrollEpoch] = useState(0);
  const [olderPageCount, setOlderPageCount] = useState(0);
  const apiLifecycleMode = useSearchParams().get("api-lifecycle") === "1";
  const lifecycleEventIDs = useRef(new Set<string>());
  const lifecycleTranscript = useQuery({
    queryKey: ["session-activity-e2e", "durable-transcript"],
    enabled: apiLifecycleMode,
    queryFn: async () => {
      const response = await fetch("/api/v1/session-activity-e2e/transcript", { cache: "no-store" });
      if (!response.ok) throw new Error(`fixture transcript request failed: ${response.status}`);
      return response.json() as Promise<FixtureTranscriptSnapshot>;
    },
  });
  useEffect(() => {
    if (!apiLifecycleMode) return;
    const source = new EventSource("/api/v1/session-activity-e2e/stream");
    const reconcile = (event: SessionLifecycleEvent) => {
      if (!shouldInvalidateForActivityLifecycleEvent(lifecycleEventIDs.current, event, "fixture-thread")) return;
      queryClient.invalidateQueries({ queryKey: ["session-activity-e2e", "durable-transcript"] });
    };
    addSSEListener(source, SSE_EVENT.ACTIVITY_PHASE_STARTED, reconcile);
    addSSEListener(source, SSE_EVENT.ACTIVITY_PHASE_TERMINAL, reconcile);
    addSSEListener(source, SSE_EVENT.INBOX_DELIVERY_ACKNOWLEDGED, reconcile);
    addSSEListener(source, SSE_EVENT.INBOX_DELIVERY_STARTED, reconcile);
    addSSEListener(source, SSE_EVENT.INBOX_DELIVERY_ABANDONED, reconcile);
    source.onerror = () => {
      queryClient.invalidateQueries({ queryKey: ["session-activity-e2e", "durable-transcript"] });
    };
    return () => source.close();
  }, [apiLifecycleMode, queryClient]);
  const capturePrependPosition = useTranscriptPrependCompensation({
    scrollContainerRef: scrollRef,
    isFetching: false,
    contentVersion: olderPageCount,
    detail,
  });

  const localEntries = useMemo<TimelineEntry[]>(() => {
    const toolUse = log(1, "tool_use", "Running npm test", "2026-08-03T12:00:02Z");
    const toolResult = log(2, "tool_result", "All tests passed", "2026-08-03T12:00:04Z");
    const values: TimelineEntry[] = [
      { kind: "message", data: message(1, "user", "Fix transcript scrolling", "2026-08-03T11:59:59Z"), transcriptEntryId: "msg_1" },
      { kind: "tool_group", toolUse, toolResult, transcriptEntryId: "tuse_1" },
    ];
    if (queued) {
      values.push({
        kind: "message",
        data: message(2, "user", "Also preserve anchors", "2026-08-03T12:00:03Z", {
          inbox_sequence: 2, delivery_state: "pending", accepted_at: "2026-08-03T12:00:03Z",
        }),
        transcriptEntryId: "msg_2",
      });
      values.push({
        kind: "message",
        data: message(6, "user", "Keep day separators stable", "2026-08-03T12:00:03.500Z", {
          inbox_sequence: 3, delivery_state: "pending", accepted_at: "2026-08-03T12:00:03.500Z",
        }),
        transcriptEntryId: "msg_6",
      });
    }
    if (resumedKind === "inbox") {
      values.push({
        kind: "message",
        data: message(2, "user", "Also preserve anchors", "2026-08-03T12:00:03Z", {
          inbox_sequence: 2, delivery_state: "acked", accepted_at: "2026-08-03T12:00:03Z",
          acknowledged_at: "2026-08-03T12:00:05Z", applied_at: "2026-08-03T12:00:07Z",
        }),
        transcriptEntryId: "msg_2",
      });
      values.push({
        kind: "message",
        data: message(6, "user", "Keep day separators stable", "2026-08-03T12:00:03.500Z", {
          inbox_sequence: 3, delivery_state: "acked", accepted_at: "2026-08-03T12:00:03.500Z",
          acknowledged_at: "2026-08-03T12:00:05Z", applied_at: "2026-08-03T12:00:07Z",
        }),
        transcriptEntryId: "msg_6",
      });
    }
    if (humanInputRequested) {
      values.push({
        kind: "message",
        data: message(7, "assistant", "Should restored anchors expand their activity?", "2026-08-03T12:00:06Z", { activity_phase_id: phaseID }),
        transcriptEntryId: "msg_7",
      });
    }
    if (humanInputAnswered) {
      values.push({
        kind: "message",
        data: message(8, "user", "Yes, expand before scrolling.", "2026-08-03T12:00:08Z"),
        transcriptEntryId: "msg_8",
      });
    }
    const completed = resumedKind ? resumedStatus === "completed" : status === "completed" && !humanInputRequested;
    if (completed) {
      values.push({
        kind: "message",
        data: message(5, "assistant", "Implemented the transcript fix.", "2026-08-03T12:00:13Z", { activity_phase_id: resumedKind ? resumedPhaseID : phaseID }),
        transcriptEntryId: "msg_5",
      });
    }
    if (showHistorical) {
      values.push({
        kind: "tool_group",
        toolUse: log(20, "tool_use", "Read historical transcript", "2026-08-03T12:00:14Z", ""),
        transcriptEntryId: "tuse_legacy",
      });
    }
    return values;
  }, [humanInputAnswered, humanInputRequested, queued, resumedKind, resumedStatus, showHistorical, status]);
  const localTurns = useMemo<SessionTranscriptTurn[]>(() => {
    const phases: NonNullable<SessionTranscriptTurn["phases"]> = [{
      id: phaseID,
      anchor_id: `aph_${phaseID}`,
      phase_number: 1,
      trigger_kind: "initial",
      status,
      boundary_reason: status === "completed" ? (resumedKind === "inbox" ? "steered" : humanInputRequested ? "human_input" : "final_response") : status === "interrupted" ? "maintenance" : undefined,
      started_at: startedAt,
      completed_at: status === "running" ? undefined : "2026-08-03T12:00:06Z",
      tool_call_count: 1,
    }];
    if (resumedKind) {
      phases.push({
        id: resumedPhaseID,
        anchor_id: `aph_${resumedPhaseID}`,
        phase_number: 2,
        trigger_kind: resumedKind === "recovery" ? "recovery" : "inbox_batch",
        status: resumedStatus,
        boundary_reason: resumedStatus === "completed" ? "final_response" : undefined,
        started_at: resumedKind === "inbox" ? "2026-08-03T12:00:07Z" : resumedKind === "human_input" ? "2026-08-03T12:00:09Z" : "2026-08-03T12:00:08Z",
        completed_at: resumedStatus === "completed" ? "2026-08-03T12:00:13Z" : undefined,
        tool_call_count: 0,
      });
    }
    return [{ turn_number: 1, started_at: "2026-08-03T11:59:59Z", phases, entries: [] }];
  }, [humanInputRequested, resumedKind, resumedStatus, status]);
  const entries = apiLifecycleMode ? (lifecycleTranscript.data?.entries ?? []) : localEntries;
  const turns = apiLifecycleMode ? (lifecycleTranscript.data?.turns ?? []) : localTurns;
  const isRunning = apiLifecycleMode
    ? (lifecycleTranscript.data?.is_running ?? false)
    : status === "running" || (resumedKind !== null && resumedStatus === "running");

  const completeActivePhase = () => {
    if (resumedKind) setResumedStatus("completed");
    else setStatus("completed");
  };

  const acknowledgeSteering = () => {
    setStatus("completed");
    setQueued(false);
    setResumedKind("inbox");
    setResumedStatus("running");
  };

  const interruptPhase = () => {
    setStatus("interrupted");
    setResumedKind(null);
  };

  const requestHumanInput = () => {
    setQueued(false);
    setStatus("completed");
    setHumanInputRequested(true);
  };

  const answerHumanInput = () => {
    if (!humanInputRequested) return;
    setHumanInputAnswered(true);
    setResumedKind("human_input");
    setResumedStatus("running");
  };

  const resumePhase = () => {
    if (status !== "interrupted") return;
    setResumedKind("recovery");
    setResumedStatus("running");
  };

  return (
    <main className="mx-auto min-h-screen max-w-2xl space-y-4 bg-background p-4 text-foreground">
      <Card>
        <CardHeader><CardTitle>Session activity deterministic fixture</CardTitle></CardHeader>
        <CardContent className="flex flex-wrap gap-2">
          <Button onClick={completeActivePhase}>Complete phase</Button>
          <Button variant="outline" onClick={interruptPhase}>Interrupt phase</Button>
          <Button variant="outline" onClick={resumePhase}>Resume phase</Button>
          <Button variant="outline" onClick={acknowledgeSteering}>Acknowledge steering</Button>
          <Button variant="outline" onClick={requestHumanInput}>Request human input</Button>
          <Button variant="outline" onClick={answerHumanInput}>Answer human input</Button>
          <Button variant="outline" onClick={() => setShowHistorical(true)}>Show historical activity</Button>
          <Button variant="outline" onClick={() => {
            capturePrependPosition();
            setOlderPageCount((count) => count + 1);
          }}>Load older activity</Button>
          <Button
            variant="outline"
            disabled={detailMutation.isPending}
            onClick={() => setDetail(detail === "compact" ? "detailed" : "compact")}
          >Activity detail: {detail}</Button>
          <Button variant="destructive" onClick={() => queryClient.setQueryData<SingleResponse<ApplicationConfig>>(
            ["application-config"],
            { data: { session_activity_capsules_enabled: !capsulesEnabled, revision: "fixture-local", updated_at: new Date().toISOString() } },
          )}>Capsules: {capsulesEnabled ? "on" : "off"}</Button>
          <Button variant="outline" onClick={() => document.documentElement.classList.toggle("dark")}>Toggle theme</Button>
        </CardContent>
      </Card>
      {apiLifecycleMode && lifecycleTranscript.isError ? (
        <Card role="alert"><CardContent className="p-3">Durable transcript fixture failed to load.</CardContent></Card>
      ) : null}
      <Card>
        <CardContent
          ref={scrollRef}
          data-testid="fixture-transcript-scroll"
          className="h-[620px] space-y-2 overflow-y-auto p-4"
          onWheel={() => setUserScrollEpoch((value) => value + 1)}
        >
          {Array.from({ length: olderPageCount * 8 }, (_, index) => (
            <Card key={`older-${index}`} data-testid="older-activity-row" className="h-20">
              <CardContent className="p-3">Older activity {index + 1}</CardContent>
            </Card>
          ))}
          <Card data-testid="current-transcript-marker">
            <CardContent className="p-3">Current transcript position</CardContent>
          </Card>
          {capsulesEnabled ? (
            <SessionActivityTimeline
              entries={entries}
              isRunning={isRunning}
              turns={turns}
              detailPreference={detail}
              anchorEntryId={typeof window !== "undefined" && window.location.hash === "#tuse_1" ? "tuse_1" : undefined}
              threadID="fixture-thread"
              scrollContainerRef={scrollRef}
              userScrollEpoch={userScrollEpoch}
              atLiveEdge={userScrollEpoch === 0}
            />
          ) : <ChatTimeline entries={entries} isRunning={isRunning} />}
          {Array.from({ length: 12 }, (_, index) => (
            <Card key={`later-${index}`} className="h-20">
              <CardContent className="p-3">Later activity {index + 1}</CardContent>
            </Card>
          ))}
        </CardContent>
      </Card>
    </main>
  );
}
