import { isHiddenLog, isToolResultMetadata, timelineEntryPresentationAt, type TimelineEntry } from "./timeline";
import type { SessionTranscriptPhase, SessionTranscriptTurn, ThreadInboxDeliveryState } from "./types";

// Delivery states in which a steering message has not yet been applied to a
// running phase. Such a message is kept out of the transcript so its content
// is never attributed to work that has not happened yet; the failure states
// stay actionable through the recoverable-inbox notice instead.
//
// Enumerated rather than derived from a missing applied_at: applied_at is only
// written when an inbox batch actually starts, so treating "no applied_at" as
// "not applied" also hides entries that will never reach a phase at all. An
// unrecognised state must fail open and keep the message visible - dropping
// user-authored content from every surface is the worse failure.
const UNAPPLIED_DELIVERY_STATES = new Set<ThreadInboxDeliveryState>([
  "pending",
  "delivering",
  "delivered",
  "acked",
  "unknown_delivery",
  "dead_letter",
]);

function isUnappliedSteering(entry: TimelineEntry): boolean {
  if (entry.kind !== "message" || entry.data.role !== "user") return false;
  const state = entry.data.delivery_state;
  if (!state || entry.data.applied_at) return false;
  return UNAPPLIED_DELIVERY_STATES.has(state);
}

export interface TimelineActivityPhase extends SessionTranscriptPhase {
  turnNumber: number;
  entries: TimelineEntry[];
  inferredHistorical: false;
  provisionalToolCallCount?: number;
  latestActivityLabel?: string;
}

// Single source for the tool count the capsule label shows and analytics
// buckets, so the reported number can never drift from the rendered one. While
// a phase runs the server counter can lag the entries already streamed in, so
// the locally derived count wins; once the phase closes the server is
// authoritative and the count settles to it.
export function activityToolCount(activity: TimelineActivityPhase | InferredHistoricalActivity): number {
  if (activity.inferredHistorical) return activity.toolCallCount;
  if (activity.status === "running") return Math.max(activity.tool_call_count, activity.provisionalToolCallCount ?? 0);
  return activity.tool_call_count;
}

export function sanitizeActivityLabel(value: string, maxLength = 160): string {
  let sanitized = value
    .replace(/\u001b\[[0-?]*[ -/]*[@-~]/g, "")
    .replace(/[\u0000-\u0008\u000b\u000c\u000e-\u001f\u007f]/g, "")
    .replace(/\b([a-z][a-z0-9+.-]*:\/\/)[^\s/@]+:[^\s/@]+@/gi, "$1[redacted]@")
    // A sensitive word anywhere in a query parameter name, but only on `_`/`-`
    // segment boundaries: `apikey`, `token_secret` and `x-amz-signature` all
    // redact, while `monkey`, `keyboard`, `author` and `sslmode` do not.
    .replace(
      /([?&](?:[a-z0-9]+[_-])*(?:access_token|api[_-]?key|auth|authorization|credential|key|passwd|password|secret|sig|signature|token)(?:[_-][a-z0-9]+)*=)[^&#\s]*/gi,
      "$1[redacted]",
    )
    .replace(/\b(?:Bearer\s+)?(?:sk-[A-Za-z0-9_-]{12,}|gh[pousr]_[A-Za-z0-9_]{12,}|glpat-[A-Za-z0-9_-]{12,}|xox[baprse]-[A-Za-z0-9-]{12,}|xapp-[A-Za-z0-9-]{12,})\b/gi, "[redacted]")
    // Long-lived (AKIA) and STS/temporary (ASIA) AWS access key IDs. Bare AWS
    // secret keys are deliberately not matched: they are unprefixed 40-char
    // base64 and a pattern loose enough to catch them would redact ordinary
    // hashes and paths. They are covered only in `NAME=value` form below.
    .replace(/\b(?:AKIA|ASIA)[A-Z0-9]{16}\b/g, "[redacted]")
    // Bare `NAME=value` outside a query string, so the value runs to the next
    // whitespace — a secret may legitimately contain `&` or `#` and must not
    // survive in part. The lookahead skips values the query-parameter rule
    // above already redacted; without it this would re-match `?SECRET=[redacted]`
    // and swallow every parameter after it.
    .replace(
      /\b((?:[A-Z][A-Z0-9_]*_)?(?:API_?KEY|KEY|TOKEN|SECRET|PASSWORD|PASSWD|PRIVATE_KEY))\s*[:=]\s*(?!\[redacted\])[^\s]+/gi,
      "$1=[redacted]",
    )
    .replace(/\s+/g, " ")
    .trim();
  if (sanitized.length > maxLength) sanitized = `${sanitized.slice(0, Math.max(0, maxLength - 1)).trimEnd()}…`;
  return sanitized;
}

// Logs the backend already classified as not-for-display stay in the entry list
// for the audit trail, but must not be promoted into capsule status text:
// unmatched tool results would surface raw result payloads, and hidden logs are
// benign runtime diagnostics the transcript deliberately suppresses. Both
// predicates come from timeline.ts so this cannot drift from what the
// transcript itself hides.
function isLabelableLog(entry: Extract<TimelineEntry, { kind: "log" }>): boolean {
  return !isToolResultMetadata(entry.data.metadata) && !isHiddenLog(entry.data);
}

function activityLabel(entry: TimelineEntry): string | undefined {
  if (entry.kind === "tool_group") return sanitizeActivityLabel(entry.toolUse.message);
  if (entry.kind === "log" && isLabelableLog(entry)) return sanitizeActivityLabel(entry.data.message);
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
  const value = timelineEntryPresentationAt(entry);
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
    if (isUnappliedSteering(entry)) {
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
    // A running phase's server counter can lag the entries already streamed in,
    // so derive the count locally. Closed phases keep the server's total.
    if (phase.status === "running") {
      phase.provisionalToolCallCount = phase.entries.reduce(
        (count, entry) => count + (entry.kind === "tool_group" ? 1 : 0),
        0,
      );
    }
    if (emittedPhases.has(phase.id)) continue;
    const node: ActivityTimelineNode = { kind: "phase", phase };
    const insertAt = nodes.findIndex((candidate) => nodeTime(candidate) > nodeTime(node));
    if (insertAt < 0) nodes.push(node);
    else nodes.splice(insertAt, 0, node);
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
