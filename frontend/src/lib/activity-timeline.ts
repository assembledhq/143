import type { TimelineEntry } from "./timeline";
import type { SessionTranscriptPhase, SessionTranscriptTurn } from "./types";

export interface TimelineActivityPhase extends SessionTranscriptPhase {
  turnNumber: number;
  entries: TimelineEntry[];
  inferredHistorical: false;
  latestActivityLabel?: string;
}

export function sanitizeActivityLabel(value: string, maxLength = 160): string {
  let sanitized = value
    .replace(/\u001b\[[0-?]*[ -/]*[@-~]/g, "")
    .replace(/[\u0000-\u0008\u000b\u000c\u000e-\u001f\u007f]/g, "")
    .replace(/\b(https?:\/\/)[^\s/@]+:[^\s/@]+@/gi, "$1[redacted]@")
    .replace(/([?&](?:access_token|api_key|key|password|secret|signature|token)=)[^&#\s]*/gi, "$1[redacted]")
    .replace(/\b(?:Bearer\s+)?(?:sk-[A-Za-z0-9_-]{12,}|gh[pousr]_[A-Za-z0-9_]{12,})\b/gi, "[redacted]")
    .replace(/\bAKIA[A-Z0-9]{16}\b/g, "[redacted]")
    .replace(/\b((?:[A-Z][A-Z0-9_]*_)?(?:API_?KEY|KEY|TOKEN|SECRET|PASSWORD|PASSWD|PRIVATE_KEY))\s*[:=]\s*[^\s]+/gi, "$1=[redacted]")
    .replace(/\s+/g, " ")
    .trim();
  if (sanitized.length > maxLength) sanitized = `${sanitized.slice(0, Math.max(0, maxLength - 1)).trimEnd()}…`;
  return sanitized;
}

function activityLabel(entry: TimelineEntry): string | undefined {
  if (entry.kind === "tool_group") return sanitizeActivityLabel(entry.toolUse.message);
  if (entry.kind === "log") return sanitizeActivityLabel(entry.data.message);
  return undefined;
}

export interface InferredHistoricalActivity {
  id: string;
  turnNumber: number;
  entries: TimelineEntry[];
  toolCallCount: number;
  inferredHistorical: true;
}

export interface TimelineBoundaryNotice {
  id: string;
  phaseID: string;
  kind: "interruption" | "recovery";
  label: string;
  createdAt: string;
}

export type ActivityTimelineNode =
  | { kind: "visible"; entry: TimelineEntry }
  | { kind: "phase"; phase: TimelineActivityPhase }
  | { kind: "boundary_notice"; notice: TimelineBoundaryNotice }
  | { kind: "historical_activity"; activity: InferredHistoricalActivity };

function phaseIDForEntry(entry: TimelineEntry): string | undefined {
  switch (entry.kind) {
    case "tool_group":
      return entry.toolUse.activity_phase_id ?? entry.toolResult?.activity_phase_id;
    default:
      return entry.data.activity_phase_id;
  }
}

function isCollapsibleActivity(entry: TimelineEntry): boolean {
  return entry.kind === "tool_group" || entry.kind === "log";
}

function entryTurn(entry: TimelineEntry): number {
  switch (entry.kind) {
    case "tool_group":
      return entry.toolUse.turn_number;
    case "plan_output":
    case "plan_message":
      return entry.turnNumber;
    default:
      return entry.data.turn_number;
  }
}

function entryKey(entry: TimelineEntry): string {
  if (entry.transcriptEntryId) return entry.transcriptEntryId;
  if (entry.kind === "tool_group") return `tool-${entry.toolUse.id}`;
  return `${entry.kind}-${entry.data.id}`;
}

function entryTime(entry: TimelineEntry): number {
  const value = entry.kind === "tool_group" ? entry.toolUse.created_at : entry.data.created_at;
  const parsed = Date.parse(value);
  return Number.isFinite(parsed) ? parsed : Number.MAX_SAFE_INTEGER;
}

function nodeTime(node: ActivityTimelineNode): number {
  if (node.kind === "visible") return entryTime(node.entry);
  if (node.kind === "boundary_notice") return Date.parse(node.notice.createdAt);
  if (node.kind === "phase") {
    return node.phase.entries.length > 0 ? entryTime(node.phase.entries[0]) : Date.parse(node.phase.started_at);
  }
  return entryTime(node.activity.entries[0]);
}

function interruptionNoticeLabel(reason?: string): string {
  switch (reason) {
    case "maintenance": return "Execution paused for maintenance.";
    case "runtime_lost": return "Execution paused because the runtime was lost.";
    case "capacity_suspended": return "Execution paused while runtime capacity was unavailable.";
    default: return "Execution was interrupted.";
  }
}

export function buildActivityTimelineNodes(entries: TimelineEntry[], turns: SessionTranscriptTurn[], threadID = ""): ActivityTimelineNode[] {
  const phases = new Map<string, TimelineActivityPhase>();
  for (const turn of turns) {
    for (const phase of turn.phases ?? []) {
      phases.set(phase.id, {
        ...phase,
        turnNumber: turn.turn_number,
        entries: [],
        inferredHistorical: false,
      });
    }
  }

  const nodes: ActivityTimelineNode[] = [];
  const emittedPhases = new Set<string>();
  let historical: InferredHistoricalActivity | null = null;
  const flushHistorical = () => {
    if (!historical) return;
    nodes.push({ kind: "historical_activity", activity: historical });
    historical = null;
  };

  for (const entry of entries) {
    if (entry.kind === "message" && entry.data.role === "user" && entry.data.delivery_state && !entry.data.applied_at) {
      continue;
    }
    const phaseID = phaseIDForEntry(entry);
    const phase = phaseID ? phases.get(phaseID) : undefined;
    if (phase && isCollapsibleActivity(entry)) {
      flushHistorical();
      phase.entries.push(entry);
      if (phase.status === "running") {
        const label = activityLabel(entry);
        if (label) phase.latestActivityLabel = label;
      }
      if (!emittedPhases.has(phase.id)) {
        emittedPhases.add(phase.id);
        nodes.push({ kind: "phase", phase });
      }
      continue;
    }
    if (!phaseID && isCollapsibleActivity(entry)) {
      const turnNumber = entryTurn(entry);
      if (!historical || historical.turnNumber !== turnNumber) {
        flushHistorical();
        historical = {
          id: `historical-${threadID ? `${threadID}-` : ""}${turnNumber}-${entryKey(entry)}`,
          turnNumber,
          entries: [],
          toolCallCount: 0,
          inferredHistorical: true,
        };
      }
      historical.entries.push(entry);
      if (entry.kind === "tool_group") historical.toolCallCount += 1;
      continue;
    }
    flushHistorical();
    nodes.push({ kind: "visible", entry });
  }
  flushHistorical();

  for (const phase of phases.values()) {
    if (!emittedPhases.has(phase.id)) {
      const node: ActivityTimelineNode = { kind: "phase", phase };
      const insertAt = nodes.findIndex((candidate) => nodeTime(candidate) > nodeTime(node));
      if (insertAt < 0) nodes.push(node);
      else nodes.splice(insertAt, 0, node);
    }
  }
  const decorated: ActivityTimelineNode[] = [];
  for (const node of nodes) {
    if (node.kind === "phase" && node.phase.trigger_kind === "recovery") {
      decorated.push({
        kind: "boundary_notice",
        notice: {
          id: `recovery-${node.phase.id}`,
          phaseID: node.phase.id,
          kind: "recovery",
          label: "Runtime recovered and execution resumed.",
          createdAt: node.phase.started_at,
        },
      });
    }
    decorated.push(node);
    if (node.kind === "phase" && node.phase.status === "interrupted") {
      decorated.push({
        kind: "boundary_notice",
        notice: {
          id: `interruption-${node.phase.id}`,
          phaseID: node.phase.id,
          kind: "interruption",
          label: interruptionNoticeLabel(node.phase.boundary_reason),
          createdAt: node.phase.completed_at ?? node.phase.started_at,
        },
      });
    }
  }
  return decorated;
}
