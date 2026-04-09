import type { SessionMessage, SessionLog } from "./types";

/** Prefix added by the backend when a message is sent in plan mode. */
export const PLAN_MODE_PREFIX = "[PLAN_MODE]\n";

export type TimelineEntry =
  | { kind: "message"; data: SessionMessage }
  | { kind: "assistant_output"; data: SessionLog }
  | { kind: "tool_group"; toolUse: SessionLog; toolResult?: SessionLog }
  | { kind: "error"; data: SessionLog }
  | { kind: "log"; data: SessionLog }
  | { kind: "plan_output"; data: SessionLog; turnNumber: number }
  | { kind: "plan_message"; data: SessionMessage; turnNumber: number };

/**
 * Merges SessionMessage and SessionLog arrays into a unified timeline
 * sorted by created_at, with tool_use/tool_result pairing and deduplication
 * of output-level logs that duplicate assistant message content.
 */
export function buildTimeline(
  messages: SessionMessage[],
  logs: SessionLog[]
): TimelineEntry[] {
  // Output logs are kept as-is — the backend no longer merges individual
  // assistant text blocks into the SessionMessage, so each output log is a
  // unique piece of content that should be displayed as its own bubble.
  const filteredLogs = logs;

  // Detect which turn numbers are plan mode turns by checking user messages
  // for the plan mode prefix.
  const planModeTurns = new Set<number>();
  for (const msg of messages) {
    if (msg.role === "user" && msg.content.startsWith(PLAN_MODE_PREFIX)) {
      planModeTurns.add(msg.turn_number);
    }
  }

  // Tag and merge into a single sortable list.
  type Tagged =
    | { source: "message"; ts: string; data: SessionMessage }
    | { source: "log"; ts: string; data: SessionLog };

  const items: Tagged[] = [
    ...messages.map(
      (m) => ({ source: "message" as const, ts: m.created_at, data: m })
    ),
    ...filteredLogs.map(
      (l) => ({ source: "log" as const, ts: l.created_at, data: l })
    ),
  ];

  items.sort((a, b) => a.ts.localeCompare(b.ts));

  const entries: TimelineEntry[] = [];
  let i = 0;

  while (i < items.length) {
    const item = items[i];

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
      // Look ahead for a matching tool_result.
      const next = items[i + 1];
      if (
        next &&
        next.source === "log" &&
        next.data.metadata?.type === "tool_result"
      ) {
        entries.push({
          kind: "tool_group",
          toolUse: log,
          toolResult: next.data,
        });
        i += 2;
      } else {
        entries.push({ kind: "tool_group", toolUse: log });
        i++;
      }
      continue;
    }

    if (log.level === "error") {
      entries.push({ kind: "error", data: log });
      i++;
      continue;
    }

    // output-level logs without metadata type are assistant text responses
    // (e.g. agent_message from Codex CLI). Show them as visible output.
    if (log.level === "output" && (!log.metadata || !log.metadata.type)) {
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
