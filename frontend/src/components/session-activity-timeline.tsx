"use client";

/* eslint-disable react-hooks/set-state-in-effect -- disclosure state must reconcile synchronously with external preference, thread, anchor, and lifecycle boundaries */

import { Fragment, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type RefObject } from "react";
import { ActivityCapsule } from "@/components/activity-capsule";
import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import { ChatTimeline, DaySeparator, type ChatTimelineProps } from "@/components/chat-timeline";
import { activityToolCount, buildActivityTimelineNodes } from "@/lib/activity-timeline";
import { timelineEntryPresentationAt } from "@/lib/timeline";
import type { SessionActivityDetail, SessionTranscriptTurn } from "@/lib/types";
import { recordSessionActivityEvent } from "@/lib/session-activity-events";
import { AlertTriangle, RotateCcw } from "lucide-react";

export interface SessionActivityTimelineProps extends ChatTimelineProps {
  turns: SessionTranscriptTurn[];
  detailPreference: SessionActivityDetail;
  anchorEntryId?: string;
  threadID: string;
  scrollContainerRef: RefObject<HTMLDivElement | null>;
  userScrollEpoch: number;
  atLiveEdge: boolean;
}

type InspectionState = "untouched" | "manual_expanded" | "manual_collapsed" | "child_open" | "text_selecting" | "viewport_inspecting";

export function SessionActivityTimeline({
  turns,
  detailPreference,
  anchorEntryId,
  threadID,
  scrollContainerRef,
  userScrollEpoch,
  atLiveEdge,
  ...timelineProps
}: SessionActivityTimelineProps) {
  const nodes = useMemo(() => buildActivityTimelineNodes(timelineProps.entries, turns, threadID), [threadID, timelineProps.entries, turns]);
  const activityForAnchor = useMemo(() => nodes.find((node) => (
    node.kind === "phase"
      ? node.phase.anchor_id === anchorEntryId || node.phase.entries.some((entry) => entry.transcriptEntryId === anchorEntryId)
      : node.kind === "historical_activity"
        ? node.activity.entries.some((entry) => entry.transcriptEntryId === anchorEntryId)
        : false
  )), [anchorEntryId, nodes]);
  const anchoredActivity = activityForAnchor?.kind === "phase"
    ? activityForAnchor.phase
    : activityForAnchor?.kind === "historical_activity"
      ? activityForAnchor.activity
      : undefined;
  const [overrides, setOverrides] = useState<Map<string, boolean>>(() => anchoredActivity ? new Map([[anchoredActivity.id, true]]) : new Map());
  const [inspection, setInspection] = useState<Map<string, InspectionState>>(() => new Map());
  const previousStatuses = useRef<Map<string, string>>(new Map());
  const pendingTerminalTransitions = useRef<Set<string>>(new Set());
  const viewportTimers = useRef<Map<string, number>>(new Map());
  const recordedAnchorExpansions = useRef<Set<string>>(new Set());
  const previousDetailPreference = useRef(detailPreference);
  const previousThreadID = useRef(threadID);
  useLayoutEffect(() => {
    if (previousDetailPreference.current === detailPreference) return;
    previousDetailPreference.current = detailPreference;
    // Preference changes intentionally reset ephemeral disclosure state.
    setOverrides(new Map());
    setInspection(new Map());
  }, [detailPreference]);
  useLayoutEffect(() => {
    if (previousThreadID.current === threadID) return;
    previousThreadID.current = threadID;
    // A thread switch defines a fresh local inspection scope.
    setOverrides(new Map());
    setInspection(new Map());
    previousStatuses.current = new Map();
    pendingTerminalTransitions.current = new Set();
    for (const timer of viewportTimers.current.values()) window.clearTimeout(timer);
    viewportTimers.current.clear();
    recordedAnchorExpansions.current.clear();
  }, [threadID]);
  useLayoutEffect(() => {
    if (!anchoredActivity) return;
    // The containing body must mount before the controller measures/scrolls.
    setOverrides((current) => current.get(anchoredActivity.id) === true
      ? current
      : new Map(current).set(anchoredActivity.id, true));
    if (!recordedAnchorExpansions.current.has(anchoredActivity.id)) {
      recordedAnchorExpansions.current.add(anchoredActivity.id);
      recordSessionActivityEvent({
        event: "anchor_expanded",
        detail: detailPreference,
        status: anchoredActivity.inferredHistorical ? "historical" : anchoredActivity.status,
        reason: anchoredActivity.inferredHistorical ? undefined : anchoredActivity.boundary_reason,
        trigger: "anchor",
        viewport_class: typeof window !== "undefined" && window.innerWidth < 768 ? "mobile" : "desktop",
      });
    }
  }, [anchoredActivity, detailPreference]);

  const expandedFor = useCallback((id: string, running: boolean) => {
    if (anchoredActivity?.id === id) return true;
    const override = overrides.get(id);
    if (override !== undefined) return override;
    if (detailPreference === "detailed" || running) return true;
    return false;
  }, [anchoredActivity?.id, detailPreference, overrides]);
  const setExpanded = useCallback((id: string, expanded: boolean) => {
    setOverrides((current) => {
      if (current.get(id) === expanded) return current;
      const next = new Map(current);
      next.set(id, expanded);
      return next;
    });
    setInspection((current) => {
      const state = expanded ? "manual_expanded" : "manual_collapsed";
      return current.get(id) === state ? current : new Map(current).set(id, state);
    });
  }, []);
  const protectInspection = useCallback((id: string, state: InspectionState) => {
    setInspection((current) => {
      const existing = current.get(id);
      // The first observed inspection cause wins until an explicit disclosure
      // action changes it. A delayed viewport timer must not overwrite a
      // manual or child-detail interaction that happened in the meantime.
      if (existing !== undefined) return current;
      return new Map(current).set(id, state);
    });
    setOverrides((current) => current.get(id) === true ? current : new Map(current).set(id, true));
  }, []);

  useLayoutEffect(() => {
    const nextStatuses = new Map<string, string>();
    const terminalChanges: Array<{ id: string; collapse: boolean }> = [];
    nodes.forEach((node, index) => {
      if (node.kind !== "phase") return;
      nextStatuses.set(node.phase.id, node.phase.status);
      const previous = previousStatuses.current.get(node.phase.id);
      if (node.phase.status === "running") {
        pendingTerminalTransitions.current.delete(node.phase.id);
        return;
      }
      if (previous === "running") pendingTerminalTransitions.current.add(node.phase.id);
      if (!pendingTerminalTransitions.current.has(node.phase.id)) return;
      const followingBoundaryRendered = nodes.slice(index + 1).some((candidate) => (
        candidate.kind === "visible" || candidate.kind === "queued_delivery" || candidate.kind === "boundary_notice"
      ));
      if (!followingBoundaryRendered) return;
      pendingTerminalTransitions.current.delete(node.phase.id);
      const untouched = (inspection.get(node.phase.id) ?? "untouched") === "untouched";
      terminalChanges.push({
        id: node.phase.id,
        collapse: detailPreference === "compact" && atLiveEdge && untouched && followingBoundaryRendered,
      });
    });
    previousStatuses.current = nextStatuses;
    const runningPhaseIDs = nodes.flatMap((node) => node.kind === "phase" && node.phase.status === "running" ? [node.phase.id] : []);
    if (terminalChanges.length === 0 && runningPhaseIDs.length === 0) return;
    for (const change of terminalChanges) {
      if (!change.collapse && detailPreference === "compact") {
        const phase = nodes.find((node) => node.kind === "phase" && node.phase.id === change.id);
        recordSessionActivityEvent({
          event: "auto_collapse_suppressed",
          detail: detailPreference,
          status: phase?.kind === "phase" ? phase.phase.status : undefined,
          reason: phase?.kind === "phase" ? phase.phase.boundary_reason : undefined,
          trigger: inspectionEventTrigger(inspection.get(change.id)),
          viewport_class: typeof window !== "undefined" && window.innerWidth < 768 ? "mobile" : "desktop",
        });
      }
    }
    // A running phase gets an explicit mounted-body hold after its first
    // commit. That hold survives the render which introduces its terminal
    // boundary; only this layout effect may then collapse it, after the
    // following visible boundary has mounted. This avoids reading refs during
    // render and prevents a one-frame scroll jump.
    setOverrides((current) => {
      const next = new Map(current);
      let changed = false;
      for (const id of runningPhaseIDs) {
        if (!next.has(id)) {
          next.set(id, true);
          changed = true;
        }
      }
      for (const change of terminalChanges) {
        const expanded = !change.collapse;
        if (next.get(change.id) !== expanded) {
          next.set(change.id, expanded);
          changed = true;
        }
      }
      return changed ? next : current;
    });
  }, [atLiveEdge, detailPreference, inspection, nodes]);

  useEffect(() => {
    if (userScrollEpoch === 0 || atLiveEdge) return;
    const container = scrollContainerRef.current;
    if (!container) return;
    const containerRect = container.getBoundingClientRect();
    for (const body of container.querySelectorAll<HTMLElement>("[data-activity-phase-body='true']")) {
      const id = body.dataset.activityPhaseId;
      if (!id || inspection.has(id)) continue;
      const rect = body.getBoundingClientRect();
      const visiblePixels = Math.max(0, Math.min(rect.bottom, containerRect.bottom) - Math.max(rect.top, containerRect.top));
      const existing = viewportTimers.current.get(id);
      if (visiblePixels < 48) {
        if (existing !== undefined) window.clearTimeout(existing);
        viewportTimers.current.delete(id);
        continue;
      }
      if (existing !== undefined) window.clearTimeout(existing);
      const timer = window.setTimeout(() => {
        const currentContainer = scrollContainerRef.current;
        const currentBody = currentContainer?.querySelector<HTMLElement>(`[data-activity-phase-body='true'][data-activity-phase-id='${CSS.escape(id)}']`);
        if (!currentContainer || !currentBody) return;
        const viewport = currentContainer.getBoundingClientRect();
        const currentRect = currentBody.getBoundingClientRect();
        const stillVisible = Math.max(0, Math.min(currentRect.bottom, viewport.bottom) - Math.max(currentRect.top, viewport.top));
        if (stillVisible >= 48) protectInspection(id, "viewport_inspecting");
        viewportTimers.current.delete(id);
      }, 250);
      viewportTimers.current.set(id, timer);
    }
  }, [atLiveEdge, inspection, protectInspection, scrollContainerRef, userScrollEpoch]);

  useEffect(() => () => {
    for (const timer of viewportTimers.current.values()) window.clearTimeout(timer);
    viewportTimers.current.clear();
  }, []);
  const {
    entries: timelineEntries,
    isRunning,
    recoveryActive,
    stoppingLabel,
    stoppedLabel,
    diffStats,
    ...entryProps
  } = timelineProps;
  const openedAt = useRef(0);
  const measuredPhaseStates = useRef<Set<string>>(new Set());
  const measuredWindowStates = useRef<Set<string>>(new Set());
  const measuredFinalResponses = useRef<Set<string>>(new Set());
  useLayoutEffect(() => {
    openedAt.current = Date.now();
    measuredPhaseStates.current.clear();
    measuredWindowStates.current.clear();
    measuredFinalResponses.current.clear();
  }, [threadID]);
  useLayoutEffect(() => {
    const container = scrollContainerRef.current;
    if (!container) return;
    const viewportClass = typeof window !== "undefined" && window.innerWidth < 768 ? "mobile" : "desktop";
    const nodeKey = nodes.map((node) => {
      if (node.kind === "phase") return `phase:${node.phase.id}:${node.phase.status}`;
      if (node.kind === "historical_activity") return `historical:${node.activity.id}`;
      if (node.kind === "queued_delivery") return `delivery:${node.delivery.id}:${node.delivery.deliveryState}`;
      if (node.kind === "boundary_notice") return `notice:${node.notice.id}`;
      return `entry:${node.entry.transcriptEntryId ?? node.entry.kind}`;
    }).join("|");
    if (!measuredWindowStates.current.has(nodeKey)) {
      measuredWindowStates.current.add(nodeKey);
      recordSessionActivityEvent({
        event: "transcript_window_rendered",
        detail: detailPreference,
        viewport_class: viewportClass,
        value_bucket: countValueBucket(nodes.length),
      });
    }
    for (const node of nodes) {
      if (node.kind !== "phase" || node.phase.status === "running") continue;
      const expanded = expandedFor(node.phase.id, false);
      const measurementKey = `${node.phase.id}:${expanded ? "expanded" : "collapsed"}`;
      if (measuredPhaseStates.current.has(measurementKey)) continue;
      const capsule = container.querySelector<HTMLElement>(`[data-activity-capsule='true'][data-activity-phase-id='${CSS.escape(node.phase.id)}']`);
      if (!capsule) continue;
      measuredPhaseStates.current.add(measurementKey);
      recordSessionActivityEvent({
        event: "completed_phase_rendered",
        detail: detailPreference,
        status: node.phase.status,
        reason: node.phase.boundary_reason,
        viewport_class: viewportClass,
        tool_count_bucket: toolCountBucket(node.phase.tool_call_count),
        duration_bucket: activityDurationBucket(node.phase),
        value_bucket: pixelValueBucket(capsule.getBoundingClientRect().height),
      });
    }
    const finalResponse = timelineEntries.findLast((entry) => entry.kind === "message" && entry.data.role === "assistant");
    if (finalResponse?.transcriptEntryId && !measuredFinalResponses.current.has(finalResponse.transcriptEntryId)) {
      const target = container.querySelector<HTMLElement>(`[data-session-entry-id='${CSS.escape(finalResponse.transcriptEntryId)}']`);
      if (target) {
        measuredFinalResponses.current.add(finalResponse.transcriptEntryId);
        const containerRect = container.getBoundingClientRect();
        recordSessionActivityEvent({
          event: "latest_final_response_positioned",
          detail: detailPreference,
          viewport_class: viewportClass,
          duration_bucket: elapsedValueBucket(Date.now() - openedAt.current),
          value_bucket: pixelValueBucket(Math.max(0, target.getBoundingClientRect().top - containerRect.top)),
        });
      }
    }
  }, [detailPreference, expandedFor, nodes, scrollContainerRef, timelineEntries]);
  const hasRunningPhase = nodes.some((node) => node.kind === "phase" && node.phase.status === "running");
  const globalEntryIndices = useMemo(() => new Map(
    timelineEntries.map((entry, index) => [entry.transcriptEntryId ?? `${entry.kind}-${index}`, index]),
  ), [timelineEntries]);
  const propsForEntries = (entries: typeof timelineEntries) => ({
    ...entryProps,
    getEntryContainerProps: timelineProps.getEntryContainerProps
      ? (entry: (typeof timelineEntries)[number], localIndex: number) => timelineProps.getEntryContainerProps!(
          entry,
          globalEntryIndices.get(entry.transcriptEntryId ?? `${entry.kind}-${localIndex}`) ?? localIndex,
        )
      : undefined,
    entries,
    isRunning: false,
  });
  const nodeDays = useMemo(() => activityNodeDayMetadata(nodes), [nodes]);
  const prepareNodeEntries = (entries: typeof timelineEntries, index: number) => {
    const day = nodeDays[index];
    const props = { ...propsForEntries(entries), initialDay: day.firstDay };
    return { props, separator: day.showSeparator && day.firstDate ? <DaySeparator dateStr={day.firstDate} /> : null };
  };

  return (
    <>
      {nodes.map((node, index) => {
        if (node.kind === "visible") {
          const prepared = prepareNodeEntries([node.entry], index);
          return (
            <Fragment key={node.entry.transcriptEntryId ?? `visible-${index}`}>
              {prepared.separator}
              <ChatTimeline {...prepared.props} />
            </Fragment>
          );
        }
        if (node.kind === "queued_delivery") {
          const prepared = prepareNodeEntries([node.delivery.entry], index);
          const label = node.delivery.deliveryState === "queued" ? "Queued" : node.delivery.deliveryState === "acknowledged" ? "Acknowledged" : "Delivery failed";
          return (
            <Fragment key={node.delivery.id}>
              {prepared.separator}
              <div className="space-y-1 rounded-lg border border-border bg-card p-2">
                <Badge variant={node.delivery.deliveryState === "abandoned" ? "destructive" : "secondary"}>{label}</Badge>
                <ChatTimeline {...prepared.props} />
              </div>
            </Fragment>
          );
        }
        if (node.kind === "boundary_notice") {
          const day = nodeDays[index];
          return (
            <Fragment key={node.notice.id}>
              {day.showSeparator && day.firstDate ? <DaySeparator dateStr={day.firstDate} /> : null}
              <Card
                role="status"
                className="flex items-center gap-2 border-info/30 bg-info/10 px-3 py-2 text-xs text-info"
                data-activity-boundary={node.notice.kind}
                data-activity-phase-id={node.notice.phaseID}
              >
                {node.notice.kind === "recovery"
                  ? <RotateCcw className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
                  : <AlertTriangle className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />}
                <span>{node.notice.label}</span>
              </Card>
            </Fragment>
          );
        }
        const activity = node.kind === "phase" ? node.phase : node.activity;
        const running = node.kind === "phase" && node.phase.status === "running";
        const prepared = prepareNodeEntries(activity.entries, index);
        return (
          <Fragment key={activity.id}>
            {prepared.separator}
            <ActivityCapsule
              activity={activity}
              expanded={expandedFor(activity.id, running)}
              onExpandedChange={(expanded) => {
                setExpanded(activity.id, expanded);
                recordSessionActivityEvent({
                  event: expanded ? "capsule_expanded" : "capsule_collapsed",
                  detail: detailPreference,
                  status: activity.inferredHistorical ? "historical" : activity.status,
                  reason: activity.inferredHistorical ? undefined : activity.boundary_reason,
                  trigger: "manual",
                  viewport_class: typeof window !== "undefined" && window.innerWidth < 768 ? "mobile" : "desktop",
                  tool_count_bucket: toolCountBucket(activityToolCount(activity)),
                  duration_bucket: activityDurationBucket(activity),
                });
              }}
              onInspect={() => protectInspection(activity.id, "text_selecting")}
            >
              <ChatTimeline {...prepared.props} onActivityInspect={() => protectInspection(activity.id, "child_open")} />
            </ActivityCapsule>
          </Fragment>
        );
      })}
      <ChatTimeline
        {...entryProps}
        entries={[]}
        isRunning={isRunning && !hasRunningPhase}
        recoveryActive={recoveryActive}
        stoppingLabel={stoppingLabel}
        stoppedLabel={stoppedLabel}
        diffStats={diffStats}
      />
    </>
  );
}

function toolCountBucket(count: number): "0" | "1" | "2-5" | "6-20" | "21+" {
  if (count <= 0) return "0";
  if (count === 1) return "1";
  if (count <= 5) return "2-5";
  if (count <= 20) return "6-20";
  return "21+";
}

function countValueBucket(count: number): "0" | "1-5" | "6-10" | "11-25" | "26-50" | "51-100" | "101+" {
  if (count <= 0) return "0";
  if (count <= 5) return "1-5";
  if (count <= 10) return "6-10";
  if (count <= 25) return "11-25";
  if (count <= 50) return "26-50";
  if (count <= 100) return "51-100";
  return "101+";
}

function pixelValueBucket(value: number): "0-47px" | "48-95px" | "96-191px" | "192-383px" | "384-767px" | "768px+" {
  if (!Number.isFinite(value) || value < 48) return "0-47px";
  if (value < 96) return "48-95px";
  if (value < 192) return "96-191px";
  if (value < 384) return "192-383px";
  if (value < 768) return "384-767px";
  return "768px+";
}

function elapsedValueBucket(value: number): "unknown" | "<10s" | "10-59s" | "1-5m" | "5-20m" | "20m+" {
  if (!Number.isFinite(value) || value < 0) return "unknown";
  if (value < 10_000) return "<10s";
  if (value < 60_000) return "10-59s";
  if (value < 300_000) return "1-5m";
  if (value < 1_200_000) return "5-20m";
  return "20m+";
}

function activityNodeDayMetadata(nodes: ReturnType<typeof buildActivityTimelineNodes>): Array<{
  firstDate?: string;
  firstDay?: string;
  showSeparator: boolean;
}> {
  let previousDay: string | undefined;
  return nodes.map((node) => {
    const entries = node.kind === "visible"
      ? [node.entry]
      : node.kind === "queued_delivery"
        ? [node.delivery.entry]
        : node.kind === "boundary_notice"
          ? []
        : node.kind === "phase"
          ? node.phase.entries
          : node.activity.entries;
    const fallbackDate = node.kind === "phase"
      ? node.phase.started_at
      : node.kind === "boundary_notice"
        ? node.notice.createdAt
        : undefined;
    const datedEntries = entries.map(timelineEntryPresentationAt);
    const firstDate = datedEntries[0] ?? fallbackDate;
    const firstDay = validDay(firstDate) ?? previousDay;
    const showSeparator = firstDay !== undefined && firstDay !== previousDay;
    const lastDay = datedEntries.reduce<string | undefined>((day, value) => validDay(value) ?? day, firstDay);
    previousDay = lastDay;
    return { firstDate, firstDay, showSeparator };
  });
}

function validDay(value?: string): string | undefined {
  if (!value) return undefined;
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? undefined : date.toDateString();
}

function inspectionEventTrigger(state?: InspectionState): "manual" | "child_open" | "text_selecting" | "viewport_inspecting" {
  if (state === "child_open" || state === "text_selecting" || state === "viewport_inspecting") return state;
  return "manual";
}

function activityDurationBucket(activity: { inferredHistorical: boolean; started_at?: string; completed_at?: string }): "unknown" | "<10s" | "10-59s" | "1-5m" | "5-20m" | "20m+" {
  if (activity.inferredHistorical || !activity.started_at || !activity.completed_at) return "unknown";
  const duration = Date.parse(activity.completed_at) - Date.parse(activity.started_at);
  if (!Number.isFinite(duration) || duration < 0) return "unknown";
  if (duration < 10_000) return "<10s";
  if (duration < 60_000) return "10-59s";
  if (duration < 300_000) return "1-5m";
  if (duration < 1_200_000) return "5-20m";
  return "20m+";
}
