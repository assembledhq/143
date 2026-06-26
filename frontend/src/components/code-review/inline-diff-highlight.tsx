import { Fragment, type ReactNode } from "react";
import { cn } from "@/lib/utils";
import type { DiffLine } from "@/lib/diff-parser";

const MAX_INLINE_DIFF_CELLS = 100_000;

export interface InlineDiffRange {
  start: number;
  end: number;
}

export type InlineDiffRangeMap = Map<number, InlineDiffRange[]>;

function pushRange(ranges: InlineDiffRange[], start: number, end: number) {
  if (end <= start) return;

  const previous = ranges[ranges.length - 1];
  if (previous && previous.end === start) {
    previous.end = end;
    return;
  }

  ranges.push({ start, end });
}

function getHighlightClassName(type: "add" | "remove") {
  return type === "add"
    ? "bg-green-200/80 dark:bg-green-800/45"
    : "bg-red-200/80 dark:bg-red-800/45";
}

function diffChangedMiddle(before: string, after: string): {
  beforeStart: number;
  beforeEnd: number;
  afterStart: number;
  afterEnd: number;
} {
  let start = 0;
  const maxPrefix = Math.min(before.length, after.length);
  while (start < maxPrefix && before[start] === after[start]) {
    start++;
  }

  let beforeEnd = before.length;
  let afterEnd = after.length;
  while (
    beforeEnd > start &&
    afterEnd > start &&
    before[beforeEnd - 1] === after[afterEnd - 1]
  ) {
    beforeEnd--;
    afterEnd--;
  }

  return {
    beforeStart: start,
    beforeEnd,
    afterStart: start,
    afterEnd,
  };
}

function diffCharacterRanges(before: string, after: string): {
  before: InlineDiffRange[];
  after: InlineDiffRange[];
} {
  const middle = diffChangedMiddle(before, after);
  const beforeMiddle = before.slice(middle.beforeStart, middle.beforeEnd);
  const afterMiddle = after.slice(middle.afterStart, middle.afterEnd);

  if (beforeMiddle.length === 0 && afterMiddle.length === 0) {
    return { before: [], after: [] };
  }

  if (beforeMiddle.length * afterMiddle.length > MAX_INLINE_DIFF_CELLS) {
    return {
      before:
        middle.beforeEnd > middle.beforeStart
          ? [{ start: middle.beforeStart, end: middle.beforeEnd }]
          : [],
      after:
        middle.afterEnd > middle.afterStart
          ? [{ start: middle.afterStart, end: middle.afterEnd }]
          : [],
    };
  }

  const beforeChars = beforeMiddle.split("");
  const afterChars = afterMiddle.split("");
  const lengths: number[][] = Array.from({ length: beforeChars.length + 1 }, () =>
    Array(afterChars.length + 1).fill(0)
  );

  for (let i = beforeChars.length - 1; i >= 0; i--) {
    for (let j = afterChars.length - 1; j >= 0; j--) {
      lengths[i][j] =
        beforeChars[i] === afterChars[j]
          ? lengths[i + 1][j + 1] + 1
          : Math.max(lengths[i + 1][j], lengths[i][j + 1]);
    }
  }

  const beforeRanges: InlineDiffRange[] = [];
  const afterRanges: InlineDiffRange[] = [];
  let beforeIndex = 0;
  let afterIndex = 0;

  while (beforeIndex < beforeChars.length && afterIndex < afterChars.length) {
    if (beforeChars[beforeIndex] === afterChars[afterIndex]) {
      beforeIndex++;
      afterIndex++;
    } else if (lengths[beforeIndex + 1][afterIndex] >= lengths[beforeIndex][afterIndex + 1]) {
      pushRange(
        beforeRanges,
        middle.beforeStart + beforeIndex,
        middle.beforeStart + beforeIndex + 1
      );
      beforeIndex++;
    } else {
      pushRange(
        afterRanges,
        middle.afterStart + afterIndex,
        middle.afterStart + afterIndex + 1
      );
      afterIndex++;
    }
  }

  pushRange(
    beforeRanges,
    middle.beforeStart + beforeIndex,
    middle.beforeStart + beforeChars.length
  );
  pushRange(
    afterRanges,
    middle.afterStart + afterIndex,
    middle.afterStart + afterChars.length
  );

  return {
    before: beforeRanges,
    after: afterRanges,
  };
}

/**
 * Computes GitHub-style intra-line highlights for paired remove/add blocks.
 * Standalone additions or deletions keep the full-line diff color only.
 */
export function buildInlineDiffRanges(lines: DiffLine[]): InlineDiffRangeMap {
  const ranges: InlineDiffRangeMap = new Map();
  let i = 0;

  while (i < lines.length) {
    const line = lines[i];

    if (line.type !== "remove") {
      i++;
      continue;
    }

    const removes: { line: DiffLine; index: number }[] = [];
    while (i < lines.length && lines[i].type === "remove") {
      removes.push({ line: lines[i], index: i });
      i++;
    }

    const adds: { line: DiffLine; index: number }[] = [];
    while (i < lines.length && lines[i].type === "add") {
      adds.push({ line: lines[i], index: i });
      i++;
    }

    const pairedCount = Math.min(removes.length, adds.length);
    for (let j = 0; j < pairedCount; j++) {
      const changedRanges = diffCharacterRanges(
        removes[j].line.content,
        adds[j].line.content
      );
      if (changedRanges.before.length > 0) {
        ranges.set(removes[j].index, changedRanges.before);
      }
      if (changedRanges.after.length > 0) {
        ranges.set(adds[j].index, changedRanges.after);
      }
    }
  }

  return ranges;
}

function readHtmlEntity(html: string, start: number): { value: string; end: number } | null {
  if (html[start] !== "&") return null;
  const semicolon = html.indexOf(";", start + 1);
  if (semicolon === -1 || semicolon - start > 12) return null;
  return {
    value: html.slice(start, semicolon + 1),
    end: semicolon + 1,
  };
}

export function applyInlineDiffRangesToHtml(
  html: string,
  ranges: InlineDiffRange[],
  type: "add" | "remove"
): string {
  if (ranges.length === 0) return html;

  const className = `rounded-[2px] ${getHighlightClassName(type)}`;
  const sortedRanges = [...ranges]
    .filter((range) => range.end > range.start)
    .sort((a, b) => a.start - b.start);
  let rangeIndex = 0;
  let textIndex = 0;
  let htmlIndex = 0;
  let highlighted = false;
  let output = "";

  const syncHighlightState = () => {
    while (rangeIndex < sortedRanges.length && textIndex >= sortedRanges[rangeIndex].end) {
      if (highlighted) {
        output += "</span>";
        highlighted = false;
      }
      rangeIndex++;
    }

    const range = sortedRanges[rangeIndex];
    if (!highlighted && range && textIndex >= range.start && textIndex < range.end) {
      output += `<span class="${className}">`;
      highlighted = true;
    }
  };

  while (htmlIndex < html.length) {
    if (html[htmlIndex] === "<") {
      if (highlighted) {
        output += "</span>";
        highlighted = false;
      }
      const tagEnd = html.indexOf(">", htmlIndex);
      if (tagEnd === -1) {
        output += html.slice(htmlIndex);
        htmlIndex = html.length;
      } else {
        output += html.slice(htmlIndex, tagEnd + 1);
        htmlIndex = tagEnd + 1;
      }
      continue;
    }

    syncHighlightState();

    const entity = readHtmlEntity(html, htmlIndex);
    if (entity) {
      output += entity.value;
      htmlIndex = entity.end;
    } else {
      output += html[htmlIndex];
      htmlIndex++;
    }
    textIndex++;
  }

  if (highlighted) {
    output += "</span>";
  }

  return output;
}

export function InlineHighlightedText({
  content,
  ranges,
  type,
}: {
  content: string;
  ranges: InlineDiffRange[];
  type: "add" | "remove";
}) {
  const className = getHighlightClassName(type);
  const parts: ReactNode[] = [];
  let cursor = 0;

  for (const [index, range] of ranges.entries()) {
    const start = Math.max(cursor, Math.min(range.start, content.length));
    const end = Math.max(start, Math.min(range.end, content.length));

    if (start > cursor) {
      parts.push(<Fragment key={`text-${index}`}>{content.slice(cursor, start)}</Fragment>);
    }
    if (end > start) {
      parts.push(
        <span key={`highlight-${index}`} className={cn("rounded-[2px]", className)}>
          {content.slice(start, end)}
        </span>
      );
    }
    cursor = end;
  }

  if (cursor < content.length) {
    parts.push(<Fragment key="text-tail">{content.slice(cursor)}</Fragment>);
  }

  return <>{parts.length > 0 ? parts : "\u00A0"}</>;
}
