import type { SessionLog } from "./types";

export interface ToolDisplay {
  /** Short human-readable label like `Ran \`git log\`` or `Read models.go`. */
  label: string;
  /** Normalized canonical tool name used for the template (for debugging/analytics). */
  canonical: string;
}

const MAX_COMMAND_LEN = 60;
const MAX_PATTERN_LEN = 40;

/**
 * Canonicalizes agent-specific tool names into a common vocabulary so the
 * same template can serve Claude Code, Codex, OpenCode, and future agents.
 */
const TOOL_ALIAS: Record<string, string> = {
  // shell / command execution
  bash: "bash",
  shell: "bash",
  run_shell_command: "bash",
  command_execution: "bash",
  local_shell_call: "bash",

  // file read
  read: "read",
  read_file: "read",

  // file edit / write
  edit: "edit",
  multiedit: "edit",
  write: "edit",
  write_file: "edit",
  replace: "edit",
  apply_patch: "edit",
  str_replace_editor: "edit",

  // search
  grep: "grep",
  search_file_content: "grep",

  // glob
  glob: "glob",

  // web
  webfetch: "web_fetch",
  web_fetch: "web_fetch",

  // agent / task
  agent: "agent",
  task: "agent",

  // plan / todos
  todowrite: "plan",
  update_plan: "plan",
  exitplanmode: "plan",
};

function canonicalize(toolName: string): string {
  const key = toolName.toLowerCase();
  return TOOL_ALIAS[key] ?? toolName;
}

function basename(path: string): string {
  const clean = path.split("?")[0].split("#")[0];
  const last = clean.split("/").filter(Boolean).pop();
  return last ?? path;
}

function truncate(s: string, max: number): string {
  if (s.length <= max) return s;
  return s.slice(0, max - 1).trimEnd() + "…";
}

/**
 * Shortens a shell command for inline display. Keeps it roughly one terminal
 * line wide and collapses whitespace/newlines so multi-line heredocs don't
 * blow out the row.
 */
function shortCommand(cmd: string): string {
  const oneLine = cmd.replace(/\s+/g, " ").trim();
  return truncate(oneLine, MAX_COMMAND_LEN);
}

function hostname(url: string): string {
  try {
    return new URL(url).hostname;
  } catch {
    return url;
  }
}

function asString(v: unknown): string | undefined {
  return typeof v === "string" && v.length > 0 ? v : undefined;
}

function asRecord(v: unknown): Record<string, unknown> | undefined {
  return v && typeof v === "object" && !Array.isArray(v) ? (v as Record<string, unknown>) : undefined;
}

/**
 * Produces a scannable label and icon for a tool_use log entry, using the
 * same normalized metadata shape all adapters emit (`metadata.tool`,
 * `metadata.input`). Claude Code's Bash tool supplies an `input.description`
 * written by the model — when present that wins over derived templates.
 */
export function deriveToolDisplay(toolUse: SessionLog): ToolDisplay {
  const metadata = toolUse.metadata ?? {};
  const rawTool = asString(metadata.tool) ?? "unknown";
  const canonical = canonicalize(rawTool);
  const input = asRecord(metadata.input);

  // 1. Model-supplied description (Claude Bash) — most specific, use as-is.
  //    Only honor it for tools where it's actually a human-readable summary,
  //    not for Agent/Task where `description` is the subagent task name.
  const description = input && asString(input.description);
  if (description && canonical !== "agent") {
    return { label: `Ran ${description}`, canonical };
  }

  // 2. Per-canonical-tool templates.
  switch (canonical) {
    case "bash": {
      const cmd = input && asString(input.command);
      if (cmd) return { label: `Ran \`${shortCommand(cmd)}\``, canonical };
      return { label: "Ran shell command", canonical };
    }
    case "read": {
      const path = input && (asString(input.path) ?? asString(input.file_path) ?? asString(input.absolute_path));
      if (path) return { label: `Read ${basename(path)}`, canonical };
      return { label: "Read file", canonical };
    }
    case "edit": {
      const path = input && (asString(input.path) ?? asString(input.file_path) ?? asString(input.absolute_path));
      if (path) return { label: `Edited ${basename(path)}`, canonical };
      return { label: "Edited file", canonical };
    }
    case "grep": {
      const pattern = input && asString(input.pattern);
      if (pattern) return { label: `Searched for \`${truncate(pattern, MAX_PATTERN_LEN)}\``, canonical };
      return { label: "Searched files", canonical };
    }
    case "glob": {
      const pattern = input && asString(input.pattern);
      if (pattern) return { label: `Found files matching \`${truncate(pattern, MAX_PATTERN_LEN)}\``, canonical };
      return { label: "Listed files", canonical };
    }
    case "web_fetch": {
      const url = input && asString(input.url);
      if (url) return { label: `Fetched ${hostname(url)}`, canonical };
      return { label: "Fetched URL", canonical };
    }
    case "agent": {
      const desc = input && (asString(input.description) ?? asString(input.prompt));
      if (desc) return { label: `Ran agent: ${truncate(desc, 50)}`, canonical };
      return { label: "Ran subagent", canonical };
    }
    case "plan": {
      return { label: "Updated plan", canonical };
    }
    default:
      return { label: `Ran ${rawTool}`, canonical: rawTool };
  }
}

/**
 * Pretty-prints the tool input for the expanded detail panel. For Bash-style
 * tools we show just the raw command on its own line; for other tools we JSON
 * the whole input object so nothing is hidden.
 */
export function formatToolInput(toolUse: SessionLog): string | null {
  const metadata = toolUse.metadata ?? {};
  const rawTool = asString(metadata.tool) ?? "";
  const canonical = canonicalize(rawTool);
  const input = asRecord(metadata.input);
  if (!input) return null;

  if (canonical === "bash") {
    const cmd = asString(input.command);
    if (cmd) return `$ ${cmd}`;
  }

  try {
    return JSON.stringify(input, null, 2);
  } catch {
    return null;
  }
}
