import type { SessionInputReference } from "@/lib/types";

export type ActiveMention = {
  start: number;
  end: number;
  query: string;
};

const referenceTrailingBoundary = `(?:$|\\s|[.,!?;:)"'\\]}])`;

function tokenForReference(reference: SessionInputReference): string {
  return reference.token ?? `@${reference.path ?? reference.id ?? reference.display}`;
}

function messageContainsReferenceToken(text: string, token: string): boolean {
  const escaped = token.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  return new RegExp(`(^|\\s)${escaped}(?=${referenceTrailingBoundary})`).test(text);
}

export function findActiveMention(text: string, caret: number): ActiveMention | null {
  if (caret < 0 || caret > text.length) {
    return null;
  }

  const beforeCaret = text.slice(0, caret);
  const mentionStart = beforeCaret.lastIndexOf("@");
  if (mentionStart < 0) {
    return null;
  }

  const charBefore = mentionStart === 0 ? "" : beforeCaret[mentionStart - 1];
  if (charBefore && !/\s/.test(charBefore)) {
    return null;
  }

  const query = beforeCaret.slice(mentionStart + 1);
  if (query.length === 0) {
    return { start: mentionStart, end: caret, query: "" };
  }
  if (/\s/.test(query)) {
    return null;
  }

  return { start: mentionStart, end: caret, query };
}

export function insertMentionAtCaret(text: string, mention: ActiveMention, reference: SessionInputReference): { text: string; caret: number } {
  const token = tokenForReference(reference);
  const suffix = text.slice(mention.end);
  const trailingSpace = suffix.startsWith(" ") ? "" : " ";
  const nextText = `${text.slice(0, mention.start)}${token}${trailingSpace}${suffix}`;
  const nextCaret = mention.start + token.length + trailingSpace.length;
  return { text: nextText, caret: nextCaret };
}

export function syncReferencesWithMessage(text: string, references: SessionInputReference[]): SessionInputReference[] {
  return references.filter((reference) => messageContainsReferenceToken(text, tokenForReference(reference)));
}

export function removeMentionReference(text: string, reference: SessionInputReference): string {
  const token = tokenForReference(reference);
  const escaped = token.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  return text
    .replace(new RegExp(`(^|\\s)${escaped}(?=${referenceTrailingBoundary})`, "g"), "$1")
    .replace(/[ \t]{2,}/g, " ")
    .replace(/\n{3,}/g, "\n\n")
    .trim();
}
