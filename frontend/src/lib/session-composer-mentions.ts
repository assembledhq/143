import type { SessionInputCommand, SessionInputReference } from "@/lib/types";

export type ActiveMention = {
  start: number;
  end: number;
  query: string;
};

export type TriggerCharacter = "@" | "/";

export type TriggerSpec = {
  char: TriggerCharacter;
  // When true, the trigger only fires at the start of a line (the first
  // non-whitespace character of the current line). Used for slash commands so
  // typing `dir/foo` does not open the picker.
  startOfLineOnly?: boolean;
  // When true, the matched query token may not contain whitespace. The mention
  // trigger uses this; commands accept a single token name and stop at the
  // first whitespace too (arguments come after the canonical token).
  singleToken?: boolean;
};

export type ActiveTrigger = ActiveMention & {
  trigger: TriggerCharacter;
};

const referenceTrailingBoundary = `(?:$|\\s|[.,!?;:)"'\\]}])`;

const DEFAULT_MENTION_TRIGGER: TriggerSpec = { char: "@", singleToken: true };
const DEFAULT_COMMAND_TRIGGER: TriggerSpec = { char: "/", startOfLineOnly: true, singleToken: true };

export const MENTION_TRIGGER_SPECS: TriggerSpec[] = [DEFAULT_MENTION_TRIGGER];
export const COMPOSER_TRIGGER_SPECS: TriggerSpec[] = [DEFAULT_MENTION_TRIGGER, DEFAULT_COMMAND_TRIGGER];

function tokenForReference(reference: SessionInputReference): string {
  return reference.token ?? `@${reference.path ?? reference.id ?? reference.display}`;
}

function tokenForCommand(command: SessionInputCommand): string {
  return command.token ?? `/${command.name}`;
}

function escapeRegex(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

function messageContainsToken(text: string, token: string): boolean {
  if (!token) {
    return false;
  }
  return new RegExp(`(^|\\s)${escapeRegex(token)}(?=${referenceTrailingBoundary})`).test(text);
}

function messageContainsCommandToken(text: string, token: string): boolean {
  if (!token) {
    return false;
  }
  return new RegExp(`(?:^|[\\r\\n])[ \\t]*${escapeRegex(token)}(?=$|[ \\t\\r\\n])`).test(text);
}

function isStartOfLine(text: string, index: number): boolean {
  if (index === 0) {
    return true;
  }
  for (let i = index - 1; i >= 0; i--) {
    const ch = text[i];
    if (ch === "\n" || ch === "\r") {
      return true;
    }
    if (ch !== " " && ch !== "\t") {
      return false;
    }
  }
  return true;
}

function detectTrigger(text: string, caret: number, spec: TriggerSpec): ActiveTrigger | null {
  if (caret < 0 || caret > text.length) {
    return null;
  }

  const beforeCaret = text.slice(0, caret);
  const triggerStart = beforeCaret.lastIndexOf(spec.char);
  if (triggerStart < 0) {
    return null;
  }

  if (spec.startOfLineOnly) {
    if (!isStartOfLine(text, triggerStart)) {
      return null;
    }
  } else {
    const charBefore = triggerStart === 0 ? "" : beforeCaret[triggerStart - 1];
    if (charBefore && !/\s/.test(charBefore)) {
      return null;
    }
  }

  const query = beforeCaret.slice(triggerStart + 1);
  if (query.length === 0) {
    return { start: triggerStart, end: caret, query: "", trigger: spec.char };
  }
  if (spec.singleToken && /\s/.test(query)) {
    return null;
  }

  return { start: triggerStart, end: caret, query, trigger: spec.char };
}

// findActiveTrigger walks the configured trigger set and returns the first
// match anchored at the caret position. The picker uses the returned trigger
// to choose its data source (mentions vs slash commands) and label.
export function findActiveTrigger(text: string, caret: number, triggers: TriggerSpec[]): ActiveTrigger | null {
  for (const spec of triggers) {
    const match = detectTrigger(text, caret, spec);
    if (match !== null) {
      return match;
    }
  }
  return null;
}

// findActiveMention is the legacy single-trigger entry point retained for
// existing callers and tests. Equivalent to findActiveTrigger with the @
// trigger only.
export function findActiveMention(text: string, caret: number): ActiveMention | null {
  const match = detectTrigger(text, caret, DEFAULT_MENTION_TRIGGER);
  if (!match) {
    return null;
  }
  return { start: match.start, end: match.end, query: match.query };
}

export function insertMentionAtCaret(text: string, mention: ActiveMention, reference: SessionInputReference): { text: string; caret: number } {
  return insertTokenAtCaret(text, mention, tokenForReference(reference));
}

export function insertCommandAtCaret(text: string, mention: ActiveMention, command: SessionInputCommand): { text: string; caret: number } {
  return insertTokenAtCaret(text, mention, tokenForCommand(command));
}

function insertTokenAtCaret(text: string, mention: ActiveMention, token: string): { text: string; caret: number } {
  const suffix = text.slice(mention.end);
  const trailingSpace = suffix.startsWith(" ") || suffix.startsWith("\n") ? "" : " ";
  const nextText = `${text.slice(0, mention.start)}${token}${trailingSpace}${suffix}`;
  const nextCaret = mention.start + token.length + trailingSpace.length;
  return { text: nextText, caret: nextCaret };
}

export function syncReferencesWithMessage(text: string, references: SessionInputReference[]): SessionInputReference[] {
  const next = references.filter((reference) => messageContainsToken(text, tokenForReference(reference)));
  return next.length === references.length ? references : next;
}

export function syncCommandsWithMessage(text: string, commands: SessionInputCommand[]): SessionInputCommand[] {
  const next = commands.filter((command) => messageContainsCommandToken(text, tokenForCommand(command)));
  return next.length === commands.length ? commands : next;
}

export function removeMentionReference(text: string, reference: SessionInputReference): string {
  return removeTokenFromText(text, tokenForReference(reference));
}

export function removeCommandReference(text: string, command: SessionInputCommand): string {
  return removeTokenFromText(text, tokenForCommand(command));
}

function removeTokenFromText(text: string, token: string): string {
  const escaped = escapeRegex(token);
  return text
    .replace(new RegExp(`(^|\\s)${escaped}(?=${referenceTrailingBoundary})`, "g"), "$1")
    .replace(/[ \t]{2,}/g, " ")
    .replace(/\n{3,}/g, "\n\n")
    .trim();
}
