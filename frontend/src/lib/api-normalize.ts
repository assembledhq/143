type APIRecord = Record<string, unknown>;

const TIMELINE_KINDS = new Set([
  "message",
  "assistant_output",
  "tool_group",
  "error",
  "log",
  "plan_output",
  "plan_message",
  "human_input",
]);

const TIMELINE_NESTED_TIMESTAMP_KEYS = [
  "message",
  "log",
  "tool_use",
  "tool_result",
  "human_input_request",
] as const;

function isRecord(value: unknown): value is APIRecord {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function stringField(record: APIRecord, key: string): string | undefined {
  const value = record[key];
  return typeof value === "string" && value.length > 0 ? value : undefined;
}

function withCreatedAt(record: APIRecord, fallback?: string): APIRecord {
  if (stringField(record, "created_at")) {
    return record;
  }

  const createdAt = stringField(record, "timestamp") ?? fallback;
  if (!createdAt) {
    return record;
  }

  return { ...record, created_at: createdAt };
}

function byteLength(value: string): number {
  if (typeof TextEncoder !== "undefined") {
    return new TextEncoder().encode(value).byteLength;
  }
  return value.length;
}

function withSessionLogMetrics(record: APIRecord): APIRecord {
  if (!isSessionLogShape(record)) return record;
  let next = record;
  if (typeof next.message_bytes !== "number") {
    next = { ...next, message_bytes: byteLength(record.message as string) };
  }
  if (typeof next.message_chars !== "number") {
    next = { ...next, message_chars: Array.from(record.message as string).length };
  }
  if (typeof next.message_truncated !== "boolean") {
    next = { ...next, message_truncated: false };
  }
  return next;
}

function isSessionLogShape(record: APIRecord): boolean {
  return (
    typeof record.id === "number" &&
    typeof record.session_id === "string" &&
    typeof record.level === "string" &&
    typeof record.message === "string" &&
    typeof record.turn_number === "number"
  );
}

function isTimelineEntryShape(record: APIRecord): boolean {
  return typeof record.kind === "string" && TIMELINE_KINDS.has(record.kind);
}

function normalizeValue(value: unknown): unknown {
  if (Array.isArray(value)) {
    let changed = false;
    const normalized = value.map((item) => {
      const next = normalizeValue(item);
      if (next !== item) changed = true;
      return next;
    });
    return changed ? normalized : value;
  }

  if (!isRecord(value)) {
    return value;
  }

  let record = value;
  for (const [key, child] of Object.entries(value)) {
    const normalizedChild = normalizeValue(child);
    if (normalizedChild !== child) {
      if (record === value) record = { ...value };
      record[key] = normalizedChild;
    }
  }

  if (isTimelineEntryShape(record)) {
    const entryCreatedAt = stringField(record, "created_at");
    if (entryCreatedAt) {
      for (const key of TIMELINE_NESTED_TIMESTAMP_KEYS) {
        const child = record[key];
        if (!isRecord(child)) continue;
        const normalizedChild = withCreatedAt(child, entryCreatedAt);
        if (normalizedChild !== child) {
          if (record === value) record = { ...value };
          record[key] = normalizedChild;
        }
      }
    }
  }

  if (isSessionLogShape(record)) {
    const normalizedLog = withSessionLogMetrics(withCreatedAt(record));
    if (normalizedLog !== record) {
      record = normalizedLog;
    }
  }

  return record;
}

export function normalizeAPIResponse<T>(value: T): T {
  return normalizeValue(value) as T;
}
