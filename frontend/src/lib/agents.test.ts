import { describe, expect, it } from "vitest";

import { availableAgentModelGroups } from "./agents";
import type { CodingAuth, ResolvedCredential } from "./types";

const codexCred: ResolvedCredential = {
  provider: "openai",
  source: "user",
  masked_key: "sk-***",
};

const claudeCred: ResolvedCredential = {
  provider: "anthropic",
  source: "user",
  masked_key: "sk-ant-***",
};

const ampCodingAuth: CodingAuth = {
  id: "ca-amp",
  org_id: "org-1",
  priority: 0,
  agent: "amp",
  auth_type: "api_key",
  label: "Amp",
  scope: "org",
  provider: "amp",
  status: "healthy",
  is_default: true,
  created_at: "2026-03-20T00:00:00Z",
  updated_at: "2026-03-20T00:00:00Z",
};

describe("availableAgentModelGroups", () => {
  it("includes only the default agent when no creds resolve", () => {
    const groups = availableAgentModelGroups([], null, [], "codex");
    expect(groups.map((g) => g.key)).toEqual(["codex"]);
  });

  it("returns user-available agents and keeps the default first", () => {
    const groups = availableAgentModelGroups([codexCred, claudeCred], null, [], "claude_code");
    expect(groups.map((g) => g.key)).toEqual(["claude_code", "codex"]);
  });

  it("relabels the Amp group as 'Amp modes' so mode rows aren't mistaken for model IDs", () => {
    const groups = availableAgentModelGroups([], null, [ampCodingAuth], "codex");
    const amp = groups.find((g) => g.key === "amp");
    expect(amp?.label).toBe("Amp modes");
  });

  it("orgAgentConfig surfaces agents whose API key is set even without user creds (PM scope)", () => {
    const groups = availableAgentModelGroups(
      [],
      null,
      [],
      "codex",
      {
        orgAgentConfig: {
          claude_code: { ANTHROPIC_API_KEY: "sk-ant-***" },
        },
      },
    );
    expect(groups.map((g) => g.key)).toEqual(["codex", "claude_code"]);
  });

  it("session scope (no orgAgentConfig) hides agents the user can't run", () => {
    const groups = availableAgentModelGroups([], null, [], "codex");
    expect(groups.map((g) => g.key)).not.toContain("claude_code");
  });

  it("ignores org agent_config entries that point at the wrong env var", () => {
    const groups = availableAgentModelGroups(
      [],
      null,
      [],
      "codex",
      {
        orgAgentConfig: {
          claude_code: { ANTHROPIC_BASE_URL: "https://example.com" },
        },
      },
    );
    expect(groups.map((g) => g.key)).toEqual(["codex"]);
  });
});
