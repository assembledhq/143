import { describe, expect, it } from "vitest";

import { agentDisplayLabel, availableAgentModelGroups, pmUsableResolvedCredentials } from "./agents";
import type { CodingAuth, CodingCredentialSummary, ResolvedCredential, UserCredentialSummary } from "./types";

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

const personalClaudeSubscription: CodingCredentialSummary = {
  id: "cc-claude",
  org_id: "org-1",
  user_id: "user-1",
  scope: "personal",
  priority: 1,
  agent: "claude_code",
  auth_type: "subscription",
  provider: "anthropic_subscription",
  label: "Personal Claude",
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

  it("treats unified personal subscription rows as available for session agents", () => {
    const groups = availableAgentModelGroups([], null, [personalClaudeSubscription], "codex");
    expect(groups.map((g) => g.key)).toEqual(["codex", "claude_code"]);
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

describe("agentDisplayLabel", () => {
  it("returns the provider label for selectable agent types", () => {
    expect(agentDisplayLabel("codex")).toBe("Codex");
    expect(agentDisplayLabel("claude_code")).toBe("Claude Code");
  });

  it("falls back to display-only labels and then the raw key", () => {
    expect(agentDisplayLabel("pm_agent")).toBe("PM Agent");
    expect(agentDisplayLabel("unknown_agent")).toBe("unknown_agent");
  });
});

describe("pmUsableResolvedCredentials", () => {
  it("excludes personal-only credentials because PM runs without a user id", () => {
    const credentials = pmUsableResolvedCredentials([claudeCred], []);
    const groups = availableAgentModelGroups(credentials, null, [], "codex");

    expect(credentials).toEqual([]);
    expect(groups.map((g) => g.key)).toEqual(["codex"]);
  });

  it("retains org and team-default resolved credentials for PM runs", () => {
    const credentials = pmUsableResolvedCredentials(
      [
        { provider: "anthropic", source: "org", masked_key: "sk-ant-org-***" },
        { provider: "gemini", source: "team_default", masked_key: "gemini-team-***" },
      ],
      [],
    );
    const groups = availableAgentModelGroups(credentials, null, [], "codex");

    expect(groups.map((g) => g.key)).toEqual(["codex", "claude_code", "gemini_cli"]);
  });

  it("adds team defaults even when a personal credential shadows them in resolved credentials", () => {
    const teamDefault: UserCredentialSummary = {
      provider: "anthropic",
      configured: true,
      is_team_default: true,
      masked_key: "sk-ant-team-***",
    };

    const credentials = pmUsableResolvedCredentials([claudeCred], [teamDefault]);
    const groups = availableAgentModelGroups(credentials, null, [], "codex");

    expect(credentials).toEqual([
      { provider: "anthropic", source: "team_default", masked_key: "sk-ant-team-***" },
    ]);
    expect(groups.map((g) => g.key)).toEqual(["codex", "claude_code"]);
  });
});
