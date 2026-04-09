// syntax-highlighter.ts — Thin wrapper around Shiki for lazy-loaded syntax highlighting.

import { useEffect, useMemo, useState } from "react";
import { useTheme } from "next-themes";

export type SyntaxTheme = "github-dark" | "github-light";

type Highlighter = Awaited<ReturnType<typeof import("shiki")["createHighlighter"]>>;

let highlighterPromise: Promise<Highlighter> | null = null;
const loadedLanguages = new Set<string>();

async function getHighlighter(): Promise<Highlighter> {
  if (!highlighterPromise) {
    highlighterPromise = import("shiki").then((shiki) =>
      shiki.createHighlighter({
        themes: ["github-dark", "github-light"],
        langs: [], // load on demand
      })
    );
  }
  return highlighterPromise;
}

/**
 * Highlight a block of code and return the inner HTML (tokens only, no wrapper).
 * Uses Shiki's `codeToTokens` for structured output we can split by line.
 */
export async function highlightLines(
  lines: string[],
  lang: string,
  theme: SyntaxTheme = "github-dark"
): Promise<string[]> {
  if (lines.length === 0) return [];

  try {
    const highlighter = await getHighlighter();

    // Load grammar lazily
    if (!loadedLanguages.has(lang)) {
      try {
        await highlighter.loadLanguage(lang as Parameters<typeof highlighter.loadLanguage>[0]);
        loadedLanguages.add(lang);
      } catch {
        loadedLanguages.add(lang); // prevent repeated attempts
        return lines.map(escapeHtml);
      }
    }

    // Highlight the full block as one string, then split back into lines.
    // This gives Shiki full context for multi-line tokens (e.g. template literals).
    const code = lines.join("\n");
    const result = highlighter.codeToTokens(code, {
      lang: lang as Parameters<typeof highlighter.codeToTokens>[1]["lang"],
      theme,
    });

    // result.tokens is an array of lines, each line is an array of tokens
    return result.tokens.map((lineTokens) =>
      lineTokens
        .map((token) => {
          const escaped = escapeHtml(token.content);
          if (token.color) {
            return `<span style="color:${token.color}">${escaped}</span>`;
          }
          return escaped;
        })
        .join("")
    );
  } catch {
    return lines.map(escapeHtml);
  }
}

function escapeHtml(str: string): string {
  return str
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;");
}

/**
 * Returns the Shiki theme name matching the current app theme (light/dark).
 */
export function useSyntaxTheme(): SyntaxTheme {
  const { resolvedTheme } = useTheme();
  return resolvedTheme === "light" ? "github-light" : "github-dark";
}

/**
 * React hook that highlights all lines in a file's diff at once.
 * Returns a flat array of highlighted HTML strings, one per line across all hunks.
 * The caller maps these back to hunk/line positions using the hunk structure.
 */
export function useFileHighlighting(
  allLineContents: string[],
  lang: string,
  theme?: SyntaxTheme,
  enabled: boolean = true
): string[] | null {
  const autoTheme = useSyntaxTheme();
  const resolvedTheme = theme ?? autoTheme;
  const [highlighted, setHighlighted] = useState<string[] | null>(null);

  // Compute a lightweight content key using a simple hash instead of joining
  // all lines into one large string on every render.
  const contentKey = useMemo(() => {
    if (allLineContents.length === 0) return "";
    let hash = 0;
    for (const line of allLineContents) {
      for (let i = 0; i < line.length; i++) {
        hash = ((hash << 5) - hash + line.charCodeAt(i)) | 0;
      }
      hash = ((hash << 5) - hash + 10) | 0; // newline separator
    }
    return `${lang}:${allLineContents.length}:${hash}`;
  }, [allLineContents, lang]);

  useEffect(() => {
    if (!enabled || allLineContents.length === 0) {
      setHighlighted(null);
      return;
    }

    let cancelled = false;

    highlightLines(allLineContents, lang, resolvedTheme).then((result) => {
      if (!cancelled) {
        setHighlighted(result);
      }
    });

    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps -- contentKey is a stable digest
  }, [contentKey, lang, resolvedTheme, enabled]);

  return highlighted;
}
