// diff-parser.ts — Parses unified diff text into structured data.

export interface DiffStats {
  added: number;
  removed: number;
  filesChanged: number;
}

export interface DiffFile {
  oldPath: string;
  newPath: string;
  hunks: DiffHunk[];
  stats: { added: number; removed: number };
  language: string;
}

export interface DiffHunk {
  oldStart: number;
  oldCount: number;
  newStart: number;
  newCount: number;
  header: string;
  lines: DiffLine[];
}

export interface DiffLine {
  type: "add" | "remove" | "context";
  content: string;
  oldLineNumber: number | null;
  newLineNumber: number | null;
}

const EXTENSION_LANGUAGE_MAP: Record<string, string> = {
  ts: "typescript",
  tsx: "tsx",
  js: "javascript",
  jsx: "jsx",
  go: "go",
  py: "python",
  rb: "ruby",
  rs: "rust",
  java: "java",
  kt: "kotlin",
  swift: "swift",
  c: "c",
  cpp: "cpp",
  h: "c",
  hpp: "cpp",
  cs: "csharp",
  css: "css",
  scss: "scss",
  html: "html",
  json: "json",
  yaml: "yaml",
  yml: "yaml",
  toml: "toml",
  xml: "xml",
  md: "markdown",
  sql: "sql",
  sh: "bash",
  bash: "bash",
  zsh: "bash",
  dockerfile: "dockerfile",
  makefile: "makefile",
  graphql: "graphql",
  proto: "protobuf",
  svelte: "svelte",
  vue: "vue",
};

export function inferLanguage(filePath: string): string {
  const ext = filePath.split(".").pop()?.toLowerCase() ?? "";
  return EXTENSION_LANGUAGE_MAP[ext] ?? "text";
}

/** Lightweight stats-only parse — avoids allocating file/hunk/line objects. */
export function parseDiffStats(diff: string): DiffStats {
  let added = 0;
  let removed = 0;
  let filesChanged = 0;

  const lines = diff.split("\n");
  for (const line of lines) {
    if (line.startsWith("diff --git")) {
      filesChanged++;
    } else if (line.startsWith("+") && !line.startsWith("+++")) {
      added++;
    } else if (line.startsWith("-") && !line.startsWith("---")) {
      removed++;
    }
  }

  return { added, removed, filesChanged };
}

/** Parses a unified diff string into structured DiffFile objects. */
export function parseDiff(raw: string): DiffFile[] {
  const files: DiffFile[] = [];
  // Split on "diff --git" boundaries
  const fileSections = raw.split(/^diff --git /m).filter(Boolean);

  for (const section of fileSections) {
    const lines = section.split("\n");
    let oldPath = "";
    let newPath = "";
    let headerEnd = 0;

    // Parse file paths from the header
    for (let i = 0; i < lines.length; i++) {
      const line = lines[i];
      if (line.startsWith("--- ")) {
        const path = line.slice(4);
        oldPath = path.startsWith("a/") ? path.slice(2) : path;
      } else if (line.startsWith("+++ ")) {
        const path = line.slice(4);
        newPath = path.startsWith("b/") ? path.slice(2) : path;
        headerEnd = i + 1;
        break;
      }
    }

    // If we couldn't parse paths from --- / +++, try the first line
    if (!newPath) {
      const firstLine = lines[0];
      const match = firstLine.match(/^a\/(.*?) b\/(.*)$/);
      if (match) {
        oldPath = match[1];
        newPath = match[2];
      }
      // Find where hunks start
      for (let i = 0; i < lines.length; i++) {
        if (lines[i].startsWith("@@")) {
          headerEnd = i;
          break;
        }
      }
    }

    const filePath = newPath || oldPath || "unknown";
    const hunks = parseHunks(lines.slice(headerEnd));

    let added = 0;
    let removed = 0;
    for (const hunk of hunks) {
      for (const line of hunk.lines) {
        if (line.type === "add") added++;
        else if (line.type === "remove") removed++;
      }
    }

    files.push({
      oldPath,
      newPath: filePath,
      hunks,
      stats: { added, removed },
      language: inferLanguage(filePath),
    });
  }

  return files;
}

/**
 * Serialize hunks to a stable string for comparison between passes.
 * Two files with identical serialized hunks have the same diff content.
 */
function serializeHunks(hunks: DiffHunk[]): string {
  return hunks
    .map(
      (h) =>
        `${h.oldStart},${h.oldCount},${h.newStart},${h.newCount}|${h.lines
          .map((l) => `${l.type}:${l.content}`)
          .join("\n")}`
    )
    .join("\n---\n");
}

/**
 * Compute the delta between two parsed diffs from different passes.
 * Returns only files whose diff content changed between the older and newer pass.
 *
 * - Files in `newer` but not `older`: included entirely (new changes in this pass).
 * - Files in both where hunks differ: included with the newer hunks.
 * - Files in `older` but not `newer`: included as empty-hunk entries (reverted).
 */
export function computeDiffDelta(
  older: DiffFile[],
  newer: DiffFile[]
): DiffFile[] {
  const olderByPath = new Map(older.map((f) => [f.newPath, f]));
  const newerByPath = new Map(newer.map((f) => [f.newPath, f]));
  const delta: DiffFile[] = [];

  for (const [path, newerFile] of newerByPath) {
    const olderFile = olderByPath.get(path);
    if (!olderFile) {
      delta.push(newerFile);
    } else if (
      serializeHunks(olderFile.hunks) !== serializeHunks(newerFile.hunks)
    ) {
      delta.push(newerFile);
    }
  }

  // Files reverted in the newer pass
  for (const [path, olderFile] of olderByPath) {
    if (!newerByPath.has(path)) {
      delta.push({
        oldPath: olderFile.oldPath,
        newPath: olderFile.newPath,
        hunks: [],
        stats: { added: 0, removed: 0 },
        language: olderFile.language,
      });
    }
  }

  return delta;
}

const HUNK_HEADER_RE = /^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@(.*)$/;

function parseHunks(lines: string[]): DiffHunk[] {
  const hunks: DiffHunk[] = [];
  let currentHunk: DiffHunk | null = null;

  for (const line of lines) {
    if (!line.startsWith("@@")) {
      // Fast path: only @@ lines can be hunk headers, skip regex for all others.
      if (currentHunk) {
        if (line.startsWith("+")) {
          currentHunk.lines.push({
            type: "add",
            content: line.slice(1),
            oldLineNumber: null,
            newLineNumber: 0,
          });
        } else if (line.startsWith("-")) {
          currentHunk.lines.push({
            type: "remove",
            content: line.slice(1),
            oldLineNumber: 0,
            newLineNumber: null,
          });
        } else if (line.startsWith("\\")) {
          // "\ No newline at end of file" — skip
        } else {
          currentHunk.lines.push({
            type: "context",
            content: line.startsWith(" ") ? line.slice(1) : line,
            oldLineNumber: 0,
            newLineNumber: 0,
          });
        }
      }
      continue;
    }

    const hunkMatch = line.match(HUNK_HEADER_RE);

    if (hunkMatch) {
      currentHunk = {
        oldStart: parseInt(hunkMatch[1], 10),
        oldCount: hunkMatch[2] != null ? parseInt(hunkMatch[2], 10) : 1,
        newStart: parseInt(hunkMatch[3], 10),
        newCount: hunkMatch[4] != null ? parseInt(hunkMatch[4], 10) : 1,
        header: line,
        lines: [],
      };
      hunks.push(currentHunk);
    }
  }

  // Now assign real line numbers
  for (const hunk of hunks) {
    let oldLine = hunk.oldStart;
    let newLine = hunk.newStart;

    for (const line of hunk.lines) {
      switch (line.type) {
        case "context":
          line.oldLineNumber = oldLine++;
          line.newLineNumber = newLine++;
          break;
        case "add":
          line.newLineNumber = newLine++;
          line.oldLineNumber = null;
          break;
        case "remove":
          line.oldLineNumber = oldLine++;
          line.newLineNumber = null;
          break;
      }
    }
  }

  return hunks;
}

/**
 * Extract a diff hunk context (~3 lines before and after) around a specific line.
 * Used when formatting review comments for the agent. The target line is marked
 * so review messages do not leave the agent guessing which surrounding line the
 * comment applies to.
 */
export function getDiffHunkContext(
  files: DiffFile[],
  filePath: string,
  lineNumber: number,
  side: "old" | "new"
): string | null {
  const file = files.find((f) => f.newPath === filePath || f.oldPath === filePath);
  if (!file) return null;

  for (const hunk of file.hunks) {
    const targetIdx = hunk.lines.findIndex((line) => {
      const ln = side === "new" ? line.newLineNumber : line.oldLineNumber;
      return ln === lineNumber;
    });
    if (targetIdx === -1) continue;

    const start = Math.max(0, targetIdx - 3);
    const end = Math.min(hunk.lines.length, targetIdx + 4);
    const contextLines = hunk.lines.slice(start, end);

    const formatted = contextLines.map((line) => {
      const prefix = line.type === "add" ? "+" : line.type === "remove" ? "-" : " ";
      const reviewedLineNumber = side === "new" ? line.newLineNumber : line.oldLineNumber;
      const fallbackLineNumber = side === "new" ? line.oldLineNumber : line.newLineNumber;
      const displayedLineNumber = reviewedLineNumber ?? fallbackLineNumber;
      const lineSide = reviewedLineNumber !== null ? side : side === "new" ? "old" : "new";
      const marker = reviewedLineNumber === lineNumber ? ">>>" : "   ";
      const lineLabel = displayedLineNumber === null ? "?" : String(displayedLineNumber);
      return `${marker} ${lineSide} ${lineLabel} ${prefix} ${line.content}`;
    });

    return formatted.join("\n");
  }
  return null;
}
