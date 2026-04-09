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
 * sorted by created_at, with tool_use/tool_result pairing. Legacy merged
 * assistant messages are filtered out when individual output logs exist.
 */
export function buildTimeline(
  messages: SessionMessage[],
  logs: SessionLog[]
): TimelineEntry[] {
  // For backward compatibility with older sessions where the backend merged
  // all assistant text blocks into one SessionMessage: if the message content
  // equals the concatenation of the turn's output logs, filter out the message
  // to avoid showing duplicate content. For new sessions (where the message
  // only contains the result event text), both are kept.
  const outputLogsByTurn = new Map<number, string[]>();
  for (const log of logs) {
    if (log.level === "output" && (!log.metadata || !log.metadata.type)) {
      const parts = outputLogsByTurn.get(log.turn_number) ?? [];
      parts.push(log.message);
      outputLogsByTurn.set(log.turn_number, parts);
    }
  }

  const filteredMessages = messages.filter((msg) => {
    if (msg.role !== "assistant") return true;
    const parts = outputLogsByTurn.get(msg.turn_number);
    if (!parts || parts.length === 0) return true;
    // If the message content is the concatenation of all output logs for this
    // turn, it's a legacy merged message — filter it out so the individual
    // logs show instead.
    const merged = parts.join("\n");
    return msg.content !== merged;
  });

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
    ...filteredMessages.map(
      (m) => ({ source: "message" as const, ts: m.created_at, data: m })
    ),
    ...logs.map(
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
