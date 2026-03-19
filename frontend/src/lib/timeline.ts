import type { SessionMessage, SessionLog } from "./types";

export type TimelineEntry =
  | { kind: "message"; data: SessionMessage }
  | { kind: "assistant_output"; data: SessionLog }
  | { kind: "tool_group"; toolUse: SessionLog; toolResult?: SessionLog }
  | { kind: "error"; data: SessionLog }
  | { kind: "log"; data: SessionLog };

/**
 * Merges SessionMessage and SessionLog arrays into a unified timeline
 * sorted by created_at, with tool_use/tool_result pairing and deduplication
 * of output-level logs that duplicate assistant message content.
 */
export function buildTimeline(
  messages: SessionMessage[],
  logs: SessionLog[]
): TimelineEntry[] {
  const persistedAssistantTurns = new Set(
    messages
      .filter((message) => message.role === "assistant")
      .map((message) => message.turn_number)
  );

  // Filter out streamed assistant output only after the assistant message for
  // that turn has been persisted. Until then, the output log is the only
  // visible copy of the in-flight response.
  const filteredLogs = logs.filter((log) => {
    if (
      log.level === "output" &&
      (!log.metadata || !log.metadata.type) &&
      persistedAssistantTurns.has(log.turn_number)
    ) {
      return false;
    }
    return true;
  });

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
      entries.push({ kind: "message", data: item.data });
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
      entries.push({ kind: "assistant_output", data: log });
      i++;
      continue;
    }

    // debug, info, remaining output with metadata — hidden by default.
    entries.push({ kind: "log", data: log });
    i++;
  }

  return entries;
}
