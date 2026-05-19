import type { HumanInputRequest, SessionMessage, SessionLog, SessionTimelineEntry as SessionTimelineResponseEntry } from "./types";

/** Prefix added by the backend when a message is sent in plan mode. */
export const PLAN_MODE_PREFIX = "[PLAN_MODE]\n";

export function applyPlanModePrefix(content: string, planMode: boolean): string {
  if (!planMode || content.startsWith(PLAN_MODE_PREFIX)) {
    return content;
  }
  return `${PLAN_MODE_PREFIX}${content}`;
}

export type TimelineEntry =
  | { kind: "message"; data: SessionMessage }
  | { kind: "assistant_output"; data: SessionLog }
  | { kind: "tool_group"; toolUse: SessionLog; toolResult?: SessionLog }
  | { kind: "error"; data: SessionLog }
  | { kind: "log"; data: SessionLog }
  | { kind: "plan_output"; data: SessionLog; turnNumber: number }
  | { kind: "plan_message"; data: SessionMessage; turnNumber: number }
  | { kind: "human_input"; data: HumanInputRequest };

type TaggedTimelineItem =
  | { source: "message"; ts: string; data: SessionMessage }
  | { source: "log"; ts: string; data: SessionLog };

function isAssistantFinalMetadata(metadata: SessionLog["metadata"] | null | undefined): boolean {
  return metadata?.type === "assistant_final";
}

function isVisibleAssistantOutput(log: SessionLog): boolean {
  return log.level === "output" && (!log.metadata || !log.metadata.type || isAssistantFinalMetadata(log.metadata));
}

function metadataString(metadata: SessionLog["metadata"] | null | undefined, key: string): string | undefined {
  const value = metadata?.[key];
  return typeof value === "string" ? value : undefined;
}

function isToolResultLog(item: TaggedTimelineItem | undefined): item is Extract<TaggedTimelineItem, { source: "log" }> {
  return item?.source === "log" && item.data.metadata?.type === "tool_result";
}

function normalizeTranscriptContent(content: string): string {
  return content
    .replace(/\r\n/g, "\n")
    .split("\n")
    .map((line) => line.replace(/[ \t\r]+$/g, ""))
    .join("\n")
    .replace(/\n+$/g, "");
}

function duplicateOutputLogIds(messages: SessionMessage[], logs: SessionLog[]): Set<number> {
  const visibleByTurnAndContent = new Map<number, Map<string, SessionLog[]>>();
  for (const log of logs) {
    if (!isVisibleAssistantOutput(log)) continue;
    const turnMap = visibleByTurnAndContent.get(log.turn_number) ?? new Map<string, SessionLog[]>();
    const normalizedMessage = normalizeTranscriptContent(log.message);
    const group = turnMap.get(normalizedMessage) ?? [];
    group.push(log);
    turnMap.set(normalizedMessage, group);
    visibleByTurnAndContent.set(log.turn_number, turnMap);
  }

  const duplicateIDs = new Set<number>();
  for (const msg of messages) {
    if (msg.role !== "assistant") continue;
    const candidates = visibleByTurnAndContent.get(msg.turn_number)?.get(normalizeTranscriptContent(msg.content)) ?? [];
    if (candidates.length === 0) continue;
    const marked = candidates.filter((candidate) => candidate.metadata?.duplicate_of_transcript === true && isAssistantFinalMetadata(candidate.metadata));
    const suppress = marked.length > 0 ? marked : candidates;
    for (const log of suppress) {
      duplicateIDs.add(log.id);
    }
  }

  return duplicateIDs;
}

/**
 * Merges SessionMessage and SessionLog arrays into a unified timeline
 * sorted by created_at, with tool_use/tool_result pairing. Legacy merged
 * assistant messages are filtered out when individual output logs exist.
 */
export function buildTimeline(
  messages: SessionMessage[],
  logs: SessionLog[]
): TimelineEntry[] {
  const suppressedLogIds = duplicateOutputLogIds(messages, logs);

  // Detect which turn numbers are plan mode turns by checking user messages
  // for the plan mode prefix.
  const planModeTurns = new Set<number>();
  for (const msg of messages) {
    if (msg.role === "user" && msg.content.startsWith(PLAN_MODE_PREFIX)) {
      planModeTurns.add(msg.turn_number);
    }
  }

  // Tag and merge into a single sortable list.
  const items: TaggedTimelineItem[] = [
    ...messages.map(
      (m) => ({ source: "message" as const, ts: m.created_at, data: m })
    ),
    ...logs
      .filter((l) => !suppressedLogIds.has(l.id))
      .map(
      (l) => ({ source: "log" as const, ts: l.created_at, data: l })
      ),
  ];

  items.sort((a, b) => a.ts.localeCompare(b.ts));

  const entries: TimelineEntry[] = [];
  const consumedLogIds = new Set<number>();
  let i = 0;

  while (i < items.length) {
    const item = items[i];
    if (item.source === "log" && consumedLogIds.has(item.data.id)) {
      i++;
      continue;
    }

    if (item.source === "message") {
      const msg = item.data;
      // If this is an assistant message responding to a plan mode turn,
      // mark it as a plan_message so the UI can show approve/adjust buttons.
      if (msg.role === "assistant" && planModeTurns.has(msg.turn_number)) {
        entries.push({ kind: "plan_message", data: msg, turnNumber: msg.turn_number });
      } else {
        entries.push({ kind: "message", data: msg });
      }
      i++;
      continue;
    }

    const log = item.data;

    if (log.level === "tool_use") {
      const callId = metadataString(log.metadata, "call_id");
      let toolResult: SessionLog | undefined;
      if (callId) {
        for (let j = i + 1; j < items.length; j++) {
          const candidate = items[j];
          if (!isToolResultLog(candidate) || consumedLogIds.has(candidate.data.id)) {
            continue;
          }
          if (metadataString(candidate.data.metadata, "call_id") === callId) {
            toolResult = candidate.data;
            break;
          }
        }
      } else {
        const next = items[i + 1];
        if (isToolResultLog(next) && !consumedLogIds.has(next.data.id)) {
          toolResult = next.data;
        }
      }

      if (toolResult) {
        consumedLogIds.add(toolResult.id);
        entries.push({
          kind: "tool_group",
          toolUse: log,
          toolResult,
        });
      } else {
        entries.push({ kind: "tool_group", toolUse: log });
      }
      i++;
      continue;
    }

    if (log.level === "error") {
      entries.push({ kind: "error", data: log });
      i++;
      continue;
    }

    // output-level logs without metadata type are assistant text responses
    // (e.g. agent_message from Codex CLI). Show them as visible output.
    if (isVisibleAssistantOutput(log)) {
      // If this output belongs to a plan mode turn, mark it as plan output.
      if (planModeTurns.has(log.turn_number)) {
        entries.push({ kind: "plan_output", data: log, turnNumber: log.turn_number });
      } else {
        entries.push({ kind: "assistant_output", data: log });
      }
      i++;
      continue;
    }

    // debug, info, remaining output with metadata — hidden by default.
    entries.push({ kind: "log", data: log });
    i++;
  }

  return entries;
}

export function timelineEntryFromResponse(entry: SessionTimelineResponseEntry): TimelineEntry {
  switch (entry.kind) {
    case "message":
      return { kind: "message", data: entry.message! };
    case "assistant_output":
      return { kind: "assistant_output", data: entry.log! };
    case "tool_group":
      return { kind: "tool_group", toolUse: entry.tool_use!, toolResult: entry.tool_result };
    case "error":
      return { kind: "error", data: entry.log! };
    case "log":
      return { kind: "log", data: entry.log! };
    case "plan_output":
      return { kind: "plan_output", data: entry.log!, turnNumber: entry.turn_number! };
    case "plan_message":
      return { kind: "plan_message", data: entry.message!, turnNumber: entry.turn_number! };
    case "human_input":
      return { kind: "human_input", data: entry.human_input_request! };
  }
}

export function buildTimelineFromResponse(entries: SessionTimelineResponseEntry[]): TimelineEntry[] {
  return entries.map(timelineEntryFromResponse);
}

export function timelineEntryCreatedAt(entry: TimelineEntry): string {
  switch (entry.kind) {
    case "tool_group":
      return entry.toolUse.created_at;
    case "message":
    case "assistant_output":
    case "error":
    case "log":
    case "plan_output":
    case "plan_message":
    case "human_input":
      return entry.data.created_at;
  }
}

export function sortTimelineEntries(entries: TimelineEntry[]): TimelineEntry[] {
  return [...entries].sort((a, b) => timelineEntryCreatedAt(a).localeCompare(timelineEntryCreatedAt(b)));
}

export function flattenTimelineResponse(entries: SessionTimelineResponseEntry[]): {
  messages: SessionMessage[];
  logs: SessionLog[];
  humanInputs: HumanInputRequest[];
} {
  const messages: SessionMessage[] = [];
  const logs: SessionLog[] = [];
  const humanInputs: HumanInputRequest[] = [];

  for (const entry of entries) {
    switch (entry.kind) {
      case "message":
      case "plan_message":
        if (entry.message) {
          messages.push(entry.message);
        }
        break;
      case "assistant_output":
      case "error":
      case "log":
      case "plan_output":
        if (entry.log) {
          logs.push(entry.log);
        }
        break;
      case "tool_group":
        if (entry.tool_use) {
          logs.push(entry.tool_use);
        }
        if (entry.tool_result) {
          logs.push(entry.tool_result);
        }
        break;
      case "human_input":
        if (entry.human_input_request) {
          humanInputs.push(entry.human_input_request);
        }
        break;
    }
  }

  return { messages, logs, humanInputs };
}
