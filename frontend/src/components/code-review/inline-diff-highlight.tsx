import { Fragment, type ReactNode } from "react";
import { cn } from "@/lib/utils";
import type { DiffLine } from "@/lib/diff-parser";

const MAX_INLINE_DIFF_CELLS = 100_000;
const MAX_TOKEN_DIFF_CELLS = 20_000;
const MIN_LINE_SIMILARITY_FOR_INLINE = 0.28;
const MAX_INLINE_RANGE_FRAGMENTS = 8;
const MAX_CHANGED_RATIO_FOR_FRAGMENTED_INLINE = 0.65;
const MAX_ANCHOR_FREQUENCY = 6;

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

type TokenKind = "word" | "number" | "string" | "space" | "operator" | "punct";

interface InlineToken {
  text: string;
  kind: TokenKind;
  start: number;
  end: number;
}

interface TokenAnchor {
  beforeIndex: number;
  afterIndex: number;
  score: number;
}

interface IndexedDiffLine {
  line: DiffLine;
  index: number;
}

interface TokenDiffRanges {
  before: InlineDiffRange[];
  after: InlineDiffRange[];
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

function classifyOperatorChar(char: string): boolean {
  return /[=+\-*/%<>!&|^~?:.]/.test(char);
}

function tokenizeLine(content: string): InlineToken[] {
  const tokens: InlineToken[] = [];
  let index = 0;

  while (index < content.length) {
    const start = index;
    const char = content[index];

    if (/\s/.test(char)) {
      index++;
      while (index < content.length && /\s/.test(content[index])) index++;
      tokens.push({ text: content.slice(start, index), kind: "space", start, end: index });
      continue;
    }

    if (/[A-Za-z_$]/.test(char)) {
      index++;
      while (index < content.length && /[A-Za-z0-9_$]/.test(content[index])) index++;
      tokens.push({ text: content.slice(start, index), kind: "word", start, end: index });
      continue;
    }

    if (/\d/.test(char)) {
      index++;
      while (index < content.length && /[A-Za-z0-9_.]/.test(content[index])) index++;
      tokens.push({ text: content.slice(start, index), kind: "number", start, end: index });
      continue;
    }

    if (char === "\"" || char === "'" || char === "`") {
      const quote = char;
      index++;
      while (index < content.length) {
        if (content[index] === "\\") {
          index = Math.min(index + 2, content.length);
          continue;
        }
        if (content[index] === quote) {
          index++;
          break;
        }
        index++;
      }
      tokens.push({ text: content.slice(start, index), kind: "string", start, end: index });
      continue;
    }

    if (classifyOperatorChar(char)) {
      index++;
      while (index < content.length && classifyOperatorChar(content[index])) index++;
      tokens.push({ text: content.slice(start, index), kind: "operator", start, end: index });
      continue;
    }

    index++;
    tokens.push({ text: content.slice(start, index), kind: "punct", start, end: index });
  }

  return tokens;
}

function isSignificantToken(token: InlineToken): boolean {
  if (token.kind === "word" || token.kind === "number" || token.kind === "string") {
    return token.text.length >= 2;
  }
  return token.kind === "operator" && token.text.length > 1;
}

function tokenKey(token: InlineToken): string {
  return `${token.kind}:${token.text}`;
}

function countSignificantTokens(tokens: InlineToken[]): Map<string, number> {
  const counts = new Map<string, number>();
  for (const token of tokens) {
    if (!isSignificantToken(token)) continue;
    const key = tokenKey(token);
    counts.set(key, (counts.get(key) ?? 0) + 1);
  }
  return counts;
}

function significantTokenSimilarity(before: InlineToken[], after: InlineToken[]): number {
  const beforeCounts = countSignificantTokens(before);
  const afterCounts = countSignificantTokens(after);
  let shared = 0;
  let total = 0;

  for (const [key, count] of beforeCounts) {
    total += count;
    shared += Math.min(count, afterCounts.get(key) ?? 0);
  }
  for (const count of afterCounts.values()) {
    total += count;
  }

  return total === 0 ? 0 : (2 * shared) / total;
}

function nonSpaceTokens(tokens: InlineToken[]): InlineToken[] {
  return tokens.filter((token) => token.kind !== "space");
}

function isCodeOperatorToken(token: InlineToken): boolean {
  return token.kind === "operator" && /[=+\-*/%<>!&|^~]/.test(token.text);
}

function hasOnlyShortNameTokens(tokens: InlineToken[]): boolean {
  return tokens.every((token) => {
    if (token.kind === "space" || token.kind === "operator" || token.kind === "punct") {
      return true;
    }
    return token.text.length <= 1;
  });
}

function shouldUseStructuralTokenFallback(
  beforeTokens: InlineToken[],
  afterTokens: InlineToken[]
): boolean {
  const beforeComparable = nonSpaceTokens(beforeTokens);
  const afterComparable = nonSpaceTokens(afterTokens);
  if (beforeComparable.length === 0 || afterComparable.length === 0) return false;
  if (beforeComparable.length <= 1 && afterComparable.length <= 1) return true;

  if ([...beforeComparable, ...afterComparable].some(isCodeOperatorToken)) return true;
  if (!hasOnlyShortNameTokens(beforeComparable) || !hasOnlyShortNameTokens(afterComparable)) {
    return false;
  }

  return [...beforeComparable, ...afterComparable].some((token) => {
    return token.kind === "punct" && /[{}()[\];,]/.test(token.text);
  });
}

function structuralTokenSimilarity(before: InlineToken[], after: InlineToken[]): number {
  if (!shouldUseStructuralTokenFallback(before, after)) return 0;

  const beforeComparable = nonSpaceTokens(before);
  const afterComparable = nonSpaceTokens(after);
  if (beforeComparable.length * afterComparable.length > MAX_TOKEN_DIFF_CELLS) return 0;

  const lengths: number[][] = Array.from({ length: beforeComparable.length + 1 }, () =>
    Array(afterComparable.length + 1).fill(0)
  );

  for (let i = beforeComparable.length - 1; i >= 0; i--) {
    for (let j = afterComparable.length - 1; j >= 0; j--) {
      lengths[i][j] =
        beforeComparable[i].kind === afterComparable[j].kind
          ? lengths[i + 1][j + 1] + 1
          : Math.max(lengths[i + 1][j], lengths[i][j + 1]);
    }
  }

  return (2 * lengths[0][0]) / (beforeComparable.length + afterComparable.length);
}

function shouldUseLongLineFallback(
  before: string,
  after: string,
  beforeTokens: InlineToken[],
  afterTokens: InlineToken[]
): boolean {
  const shorter = Math.min(before.length, after.length);
  const longer = Math.max(before.length, after.length);
  return (
    shorter >= 200 &&
    shorter / Math.max(1, longer) >= 0.75 &&
    nonSpaceTokens(beforeTokens).length <= 1 &&
    nonSpaceTokens(afterTokens).length <= 1
  );
}

function orderedHistogramAnchors(before: InlineToken[], after: InlineToken[]): TokenAnchor[] {
  const beforeCounts = countSignificantTokens(before);
  const afterCounts = countSignificantTokens(after);
  const afterByKey = new Map<string, number[]>();

  for (let j = 0; j < after.length; j++) {
    const token = after[j];
    if (!isSignificantToken(token)) continue;
    const key = tokenKey(token);
    const frequency = (beforeCounts.get(key) ?? 0) + (afterCounts.get(key) ?? 0);
    if (frequency > MAX_ANCHOR_FREQUENCY) continue;
    const indexes = afterByKey.get(key) ?? [];
    indexes.push(j);
    afterByKey.set(key, indexes);
  }

  const candidates: TokenAnchor[] = [];
  for (let i = 0; i < before.length; i++) {
    const token = before[i];
    if (!isSignificantToken(token)) continue;
    const key = tokenKey(token);
    const afterIndexes = afterByKey.get(key);
    if (!afterIndexes) continue;
    const frequency = (beforeCounts.get(key) ?? 0) + (afterCounts.get(key) ?? 0);
    for (const j of afterIndexes) {
      candidates.push({ beforeIndex: i, afterIndex: j, score: frequency });
    }
  }

  if (candidates.length === 0) return [];

  candidates.sort((a, b) => a.beforeIndex - b.beforeIndex || a.afterIndex - b.afterIndex);
  const bestChains: TokenAnchor[][] = [];
  const bestWeights: number[] = [];

  for (let i = 0; i < candidates.length; i++) {
    const candidate = candidates[i];
    let bestChain: TokenAnchor[] = [];
    let bestWeight = Number.POSITIVE_INFINITY;

    for (let j = 0; j < i; j++) {
      const previous = candidates[j];
      if (
        previous.beforeIndex >= candidate.beforeIndex ||
        previous.afterIndex >= candidate.afterIndex
      ) {
        continue;
      }

      const previousWeight = bestWeights[j];
      if (
        bestChains[j].length > bestChain.length ||
        (bestChains[j].length === bestChain.length && previousWeight < bestWeight)
      ) {
        bestChain = bestChains[j];
        bestWeight = previousWeight;
      }
    }

    bestChains[i] = [...bestChain, candidate];
    bestWeights[i] = (bestWeight === Number.POSITIVE_INFINITY ? 0 : bestWeight) + candidate.score;
  }

  let bestIndex = 0;
  for (let i = 1; i < bestChains.length; i++) {
    if (
      bestChains[i].length > bestChains[bestIndex].length ||
      (bestChains[i].length === bestChains[bestIndex].length &&
        bestWeights[i] < bestWeights[bestIndex])
    ) {
      bestIndex = i;
    }
  }

  return bestChains[bestIndex];
}

function tokenRangesFromAll(tokens: InlineToken[]): InlineDiffRange[] {
  const ranges: InlineDiffRange[] = [];
  for (const token of tokens) {
    if (token.kind === "space") continue;
    pushRange(ranges, token.start, token.end);
  }
  return ranges;
}

function tokenDiffByLCS(before: InlineToken[], after: InlineToken[]): TokenDiffRanges {
  if (before.length * after.length > MAX_TOKEN_DIFF_CELLS) {
    return {
      before: tokenRangesFromAll(before),
      after: tokenRangesFromAll(after),
    };
  }

  const lengths: number[][] = Array.from({ length: before.length + 1 }, () =>
    Array(after.length + 1).fill(0)
  );

  for (let i = before.length - 1; i >= 0; i--) {
    for (let j = after.length - 1; j >= 0; j--) {
      lengths[i][j] =
        tokenKey(before[i]) === tokenKey(after[j])
          ? lengths[i + 1][j + 1] + 1
          : Math.max(lengths[i + 1][j], lengths[i][j + 1]);
    }
  }

  const beforeRanges: InlineDiffRange[] = [];
  const afterRanges: InlineDiffRange[] = [];
  let beforeIndex = 0;
  let afterIndex = 0;

  while (beforeIndex < before.length && afterIndex < after.length) {
    if (tokenKey(before[beforeIndex]) === tokenKey(after[afterIndex])) {
      beforeIndex++;
      afterIndex++;
    } else if (lengths[beforeIndex + 1][afterIndex] >= lengths[beforeIndex][afterIndex + 1]) {
      if (before[beforeIndex].kind !== "space") {
        pushRange(beforeRanges, before[beforeIndex].start, before[beforeIndex].end);
      }
      beforeIndex++;
    } else {
      if (after[afterIndex].kind !== "space") {
        pushRange(afterRanges, after[afterIndex].start, after[afterIndex].end);
      }
      afterIndex++;
    }
  }

  for (; beforeIndex < before.length; beforeIndex++) {
    if (before[beforeIndex].kind !== "space") {
      pushRange(beforeRanges, before[beforeIndex].start, before[beforeIndex].end);
    }
  }
  for (; afterIndex < after.length; afterIndex++) {
    if (after[afterIndex].kind !== "space") {
      pushRange(afterRanges, after[afterIndex].start, after[afterIndex].end);
    }
  }

  return { before: beforeRanges, after: afterRanges };
}

function histogramTokenDiff(before: InlineToken[], after: InlineToken[]): TokenDiffRanges {
  if (before.length === 0) return { before: [], after: tokenRangesFromAll(after) };
  if (after.length === 0) return { before: tokenRangesFromAll(before), after: [] };

  const anchors = orderedHistogramAnchors(before, after);
  if (anchors.length === 0) {
    return tokenDiffByLCS(before, after);
  }

  const beforeRanges: InlineDiffRange[] = [];
  const afterRanges: InlineDiffRange[] = [];
  let beforeCursor = 0;
  let afterCursor = 0;

  for (const anchor of anchors) {
    const segment = histogramTokenDiff(
      before.slice(beforeCursor, anchor.beforeIndex),
      after.slice(afterCursor, anchor.afterIndex)
    );
    beforeRanges.push(...segment.before);
    afterRanges.push(...segment.after);
    beforeCursor = anchor.beforeIndex + 1;
    afterCursor = anchor.afterIndex + 1;
  }

  const tail = histogramTokenDiff(before.slice(beforeCursor), after.slice(afterCursor));
  beforeRanges.push(...tail.before);
  afterRanges.push(...tail.after);

  return { before: beforeRanges, after: afterRanges };
}

function totalRangeLength(ranges: InlineDiffRange[]): number {
  return ranges.reduce((sum, range) => sum + Math.max(0, range.end - range.start), 0);
}

function mergeNearbyRanges(content: string, ranges: InlineDiffRange[]): InlineDiffRange[] {
  const sortedRanges = [...ranges]
    .filter((range) => range.end > range.start)
    .sort((a, b) => a.start - b.start);
  const merged: InlineDiffRange[] = [];

  for (const range of sortedRanges) {
    const previous = merged[merged.length - 1];
    if (!previous) {
      merged.push({ ...range });
      continue;
    }

    const gap = content.slice(previous.end, range.start);
    if (range.start <= previous.end || (gap.length <= 3 && /^\s*$/.test(gap))) {
      previous.end = Math.max(previous.end, range.end);
      continue;
    }

    merged.push({ ...range });
  }

  return merged;
}

function coarsenNoisyRanges(
  ranges: TokenDiffRanges,
  before: string,
  after: string
): TokenDiffRanges {
  const normalizedRanges = {
    before: mergeNearbyRanges(before, ranges.before),
    after: mergeNearbyRanges(after, ranges.after),
  };
  const fragmentCount = normalizedRanges.before.length + normalizedRanges.after.length;
  const beforeChanged = totalRangeLength(normalizedRanges.before) / Math.max(1, before.length);
  const afterChanged = totalRangeLength(normalizedRanges.after) / Math.max(1, after.length);

  if (
    fragmentCount <= MAX_INLINE_RANGE_FRAGMENTS &&
    (beforeChanged <= MAX_CHANGED_RATIO_FOR_FRAGMENTED_INLINE ||
      afterChanged <= MAX_CHANGED_RATIO_FOR_FRAGMENTED_INLINE)
  ) {
    return normalizedRanges;
  }

  const middle = diffChangedMiddle(before, after);
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

function lineSimilarityFromTokens(
  beforeContent: string,
  afterContent: string,
  beforeTokens: InlineToken[],
  afterTokens: InlineToken[]
): number {
  if (shouldUseLongLineFallback(beforeContent, afterContent, beforeTokens, afterTokens)) {
    return MIN_LINE_SIMILARITY_FOR_INLINE;
  }
  return Math.max(
    significantTokenSimilarity(beforeTokens, afterTokens),
    structuralTokenSimilarity(beforeTokens, afterTokens)
  );
}

function alignChangedLinePairs(
  removes: IndexedDiffLine[],
  adds: IndexedDiffLine[]
): Array<{ remove: IndexedDiffLine; add: IndexedDiffLine }> {
  if (removes.length === 0 || adds.length === 0) return [];

  const removeTokens = removes.map((remove) => tokenizeLine(remove.line.content));
  const addTokens = adds.map((add) => tokenizeLine(add.line.content));
  const scores = removes.map((remove, ri) =>
    adds.map((add, ai) =>
      lineSimilarityFromTokens(
        remove.line.content,
        add.line.content,
        removeTokens[ri],
        addTokens[ai]
      )
    )
  );
  const dp: number[][] = Array.from({ length: removes.length + 1 }, () =>
    Array(adds.length + 1).fill(0)
  );

  for (let i = removes.length - 1; i >= 0; i--) {
    for (let j = adds.length - 1; j >= 0; j--) {
      const pairScore =
        scores[i][j] >= MIN_LINE_SIMILARITY_FOR_INLINE
          ? scores[i][j] + dp[i + 1][j + 1]
          : Number.NEGATIVE_INFINITY;
      dp[i][j] = Math.max(pairScore, dp[i + 1][j], dp[i][j + 1]);
    }
  }

  const pairs: Array<{ remove: IndexedDiffLine; add: IndexedDiffLine }> = [];
  let i = 0;
  let j = 0;
  while (i < removes.length && j < adds.length) {
    const pairScore =
      scores[i][j] >= MIN_LINE_SIMILARITY_FOR_INLINE
        ? scores[i][j] + dp[i + 1][j + 1]
        : Number.NEGATIVE_INFINITY;

    if (pairScore >= dp[i + 1][j] && pairScore >= dp[i][j + 1]) {
      pairs.push({ remove: removes[i], add: adds[j] });
      i++;
      j++;
    } else if (dp[i + 1][j] >= dp[i][j + 1]) {
      i++;
    } else {
      j++;
    }
  }

  return pairs;
}

function diffCharacterRanges(before: string, after: string): {
  before: InlineDiffRange[];
  after: InlineDiffRange[];
} {
  const beforeTokens = tokenizeLine(before);
  const afterTokens = tokenizeLine(after);
  if (
    lineSimilarityFromTokens(before, after, beforeTokens, afterTokens) <
      MIN_LINE_SIMILARITY_FOR_INLINE
  ) {
    return { before: [], after: [] };
  }

  const tokenRanges = histogramTokenDiff(beforeTokens, afterTokens);
  if (tokenRanges.before.length > 0 || tokenRanges.after.length > 0) {
    return coarsenNoisyRanges(tokenRanges, before, after);
  }

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

    const removes: IndexedDiffLine[] = [];
    while (i < lines.length && lines[i].type === "remove") {
      removes.push({ line: lines[i], index: i });
      i++;
    }

    const adds: IndexedDiffLine[] = [];
    while (i < lines.length && lines[i].type === "add") {
      adds.push({ line: lines[i], index: i });
      i++;
    }

    for (const pair of alignChangedLinePairs(removes, adds)) {
      const changedRanges = diffCharacterRanges(
        pair.remove.line.content,
        pair.add.line.content
      );
      if (changedRanges.before.length > 0) {
        ranges.set(pair.remove.index, changedRanges.before);
      }
      if (changedRanges.after.length > 0) {
        ranges.set(pair.add.index, changedRanges.after);
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
