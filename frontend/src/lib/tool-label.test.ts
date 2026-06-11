import { describe, it, expect } from "vitest";
import { deriveToolDisplay, formatToolInput } from "./tool-label";
import type { SessionLog } from "./types";

function makeLog(metadata: Record<string, unknown> | null): SessionLog {
  return {
    id: 1,
    session_id: "sess_test",
    level: "tool_use",
    message: "",
    metadata,
    turn_number: 1,
    created_at: "2026-04-20T00:00:00Z",
    message_bytes: 0,
    message_chars: 0,
    message_truncated: false,
  };
}

describe("deriveToolDisplay", () => {
  describe("Claude Code Bash tool", () => {
    it("uses model-supplied description when present", () => {
      const log = makeLog({
        tool: "Bash",
        input: { command: "git status", description: "Check git status" },
      });
      expect(deriveToolDisplay(log)).toEqual({
        label: "Ran Check git status",
        canonical: "bash",
      });
    });

    it("falls back to shortened command when no description", () => {
      const log = makeLog({ tool: "Bash", input: { command: "ls -la" } });
      expect(deriveToolDisplay(log).label).toBe("Ran `ls -la`");
    });

    it("truncates very long commands", () => {
      const longCmd = "git log --pretty=format:'%h %s' --author=wangjohn --since=2026-01-01 --oneline";
      const log = makeLog({ tool: "Bash", input: { command: longCmd } });
      const { label } = deriveToolDisplay(log);
      expect(label.startsWith("Ran `")).toBe(true);
      expect(label.endsWith("`")).toBe(true);
      // label body (between backticks) should not exceed 60 chars
      const body = label.slice("Ran `".length, -1);
      expect(body.length).toBeLessThanOrEqual(60);
    });

    it("collapses multiline commands to a single line", () => {
      const log = makeLog({
        tool: "Bash",
        input: { command: "cat <<'EOF'\nhello\nworld\nEOF" },
      });
      expect(deriveToolDisplay(log).label).not.toContain("\n");
    });
  });

  describe("Codex command_execution", () => {
    it("renders raw command with shell icon", () => {
      const log = makeLog({
        tool: "command_execution",
        input: { command: "go test ./..." },
      });
      expect(deriveToolDisplay(log)).toMatchObject({
        label: "Ran `go test ./...`",
        canonical: "bash",
      });
    });
  });

  describe("Gemini CLI run_shell_command", () => {
    it("maps to bash canonical", () => {
      const log = makeLog({
        tool: "run_shell_command",
        input: { command: "ls" },
      });
      expect(deriveToolDisplay(log)).toMatchObject({
        label: "Ran `ls`",
        canonical: "bash",
      });
    });
  });

  describe("Read tool", () => {
    it("shows basename for Claude Read path", () => {
      const log = makeLog({ tool: "Read", input: { file_path: "/repo/src/main.go" } });
      expect(deriveToolDisplay(log).label).toBe("Read main.go");
    });

    it("accepts `path` key (Gemini read_file)", () => {
      const log = makeLog({ tool: "read_file", input: { path: "/app/config.yaml" } });
      expect(deriveToolDisplay(log)).toMatchObject({
        label: "Read config.yaml",
      });
    });
  });

  describe("Edit/Write tools", () => {
    it("renders Edited label with basename", () => {
      const log = makeLog({ tool: "Edit", input: { file_path: "/repo/src/app.tsx" } });
      expect(deriveToolDisplay(log).label).toBe("Edited app.tsx");
    });

    it("handles write_file alias", () => {
      const log = makeLog({ tool: "write_file", input: { path: "/tmp/out.txt" } });
      expect(deriveToolDisplay(log)).toMatchObject({
        label: "Edited out.txt",
      });
    });
  });

  describe("Grep", () => {
    it("shows search pattern", () => {
      const log = makeLog({ tool: "Grep", input: { pattern: "TODO" } });
      expect(deriveToolDisplay(log).label).toBe("Searched for `TODO`");
    });

    it("accepts Gemini search_file_content alias", () => {
      const log = makeLog({ tool: "search_file_content", input: { pattern: "bug" } });
      expect(deriveToolDisplay(log).canonical).toBe("grep");
    });
  });

  describe("WebFetch", () => {
    it("shows hostname", () => {
      const log = makeLog({
        tool: "WebFetch",
        input: { url: "https://example.com/foo/bar" },
      });
      expect(deriveToolDisplay(log).label).toBe("Fetched example.com");
    });

    it("falls back cleanly on malformed URL", () => {
      const log = makeLog({ tool: "WebFetch", input: { url: "not-a-url" } });
      expect(deriveToolDisplay(log).label).toBe("Fetched not-a-url");
    });
  });

  describe("Agent / Task", () => {
    it("uses description as the subagent task, not the Ran prefix", () => {
      const log = makeLog({
        tool: "Agent",
        input: { description: "Audit branch readiness", prompt: "..." },
      });
      expect(deriveToolDisplay(log).label).toBe("Ran agent: Audit branch readiness");
    });
  });

  describe("fallbacks", () => {
    it("handles unknown tool names", () => {
      const log = makeLog({ tool: "frobnicate" });
      expect(deriveToolDisplay(log)).toEqual({
        label: "Ran frobnicate",
        canonical: "frobnicate",
      });
    });

    it("handles null metadata without crashing", () => {
      const log = makeLog(null);
      expect(deriveToolDisplay(log).label).toBe("Ran unknown");
    });

    it("handles missing input without crashing", () => {
      const log = makeLog({ tool: "Bash" });
      expect(deriveToolDisplay(log).label).toBe("Ran shell command");
    });

    it("handles malformed input (not an object)", () => {
      const log = makeLog({ tool: "Bash", input: "raw string" });
      expect(deriveToolDisplay(log).label).toBe("Ran shell command");
    });
  });
});

describe("formatToolInput", () => {
  it("shows bash command as shell-prefixed line", () => {
    const log = makeLog({ tool: "Bash", input: { command: "git status" } });
    expect(formatToolInput(log)).toBe("$ git status");
  });

  it("preserves multiline bash commands verbatim", () => {
    const log = makeLog({
      tool: "Bash",
      input: { command: "cat <<EOF\nhi\nEOF" },
    });
    expect(formatToolInput(log)).toBe("$ cat <<EOF\nhi\nEOF");
  });

  it("JSON-prints other tools", () => {
    const log = makeLog({ tool: "Read", input: { file_path: "/a.go" } });
    expect(formatToolInput(log)).toContain("\"file_path\": \"/a.go\"");
  });

  it("returns null when there is no input", () => {
    const log = makeLog({ tool: "Bash" });
    expect(formatToolInput(log)).toBeNull();
  });
});
