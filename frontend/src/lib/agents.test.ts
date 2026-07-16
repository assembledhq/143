import { describe, expect, it } from "vitest";

import {
  AGENTS,
  AGENTS_BY_KEY,
  agentDisplayLabel,
  agentTypeForModel,
  availableAgentModelGroups,
  pmUsableResolvedCredentials,
} from "./agents";
import type { CodingCredentialSummary, ResolvedCredential } from "./types";

const codexCred: ResolvedCredential = {
  provider: "openai",
  source: "personal",
  masked_key: "sk-***",
};

const claudeCred: ResolvedCredential = {
  provider: "anthropic",
  source: "personal",
  masked_key: "sk-ant-***",
};

const ampCodingAuth: CodingCredentialSummary = {
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

// makeCredential builds a unified coding-credential row with sensible
// defaults; tests override only the dimensions they exercise.
function makeCredential(
  overrides: Partial<CodingCredentialSummary>,
): CodingCredentialSummary {
  return {
    id: "cc-1",
    org_id: "org-1",
    scope: "org",
    priority: 1,
    agent: "codex",
    auth_type: "api_key",
    provider: "openai",
    label: "Credential",
    status: "healthy",
    is_default: false,
    created_at: "2026-03-20T00:00:00Z",
    updated_at: "2026-03-20T00:00:00Z",
    ...overrides,
  };
}

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
  it("orders OpenCode as the third main coding agent", () => {
    expect(AGENTS.slice(0, 3).map((agent) => agent.key)).toEqual([
      "codex",
      "claude_code",
      "opencode",
    ]);
  });

  it("includes only the default agent when no creds resolve", () => {
    const groups = availableAgentModelGroups([], null, [], "codex");
    expect(groups.map((g) => g.key)).toEqual(["codex"]);
  });

  it("returns user-available agents and keeps the default first", () => {
    const groups = availableAgentModelGroups(
      [codexCred, claudeCred],
      null,
      [],
      "claude_code",
    );
    expect(groups.map((g) => g.key)).toEqual(["claude_code", "codex"]);
  });

  it("relabels the Amp group as 'Amp modes' so mode rows aren't mistaken for model IDs", () => {
    const groups = availableAgentModelGroups(
      [],
      null,
      [ampCodingAuth],
      "codex",
    );
    const amp = groups.find((g) => g.key === "amp");
    expect(amp?.label).toBe("Amp modes");
  });

  it("treats unified personal subscription rows as available for session agents", () => {
    const groups = availableAgentModelGroups(
      [],
      null,
      [personalClaudeSubscription],
      "codex",
    );
    expect(groups.map((g) => g.key)).toEqual(["codex", "claude_code"]);
  });

  it("treats explicit OpenCode credential rows as available for OpenCode", () => {
    const groups = availableAgentModelGroups(
      [],
      null,
      [
        makeCredential({
          agent: "opencode",
          provider: "opencode",
          label: "OpenCode",
        }),
      ],
      "codex",
    );
    expect(groups.map((g) => g.key)).toContain("opencode");
    expect(AGENTS_BY_KEY.opencode.providerKey).toBe("opencode");
  });

  it("orgAgentConfig surfaces agents whose API key is set even without user creds (PM scope)", () => {
    const groups = availableAgentModelGroups([], null, [], "codex", {
      orgAgentConfig: {
        claude_code: { ANTHROPIC_API_KEY: "sk-ant-***" },
      },
    });
    expect(groups.map((g) => g.key)).toEqual(["codex", "claude_code"]);
  });

  it("session scope (no orgAgentConfig) hides agents the user can't run", () => {
    const groups = availableAgentModelGroups([], null, [], "codex");
    expect(groups.map((g) => g.key)).not.toContain("claude_code");
  });

  it("ignores org agent_config entries that point at the wrong env var", () => {
    const groups = availableAgentModelGroups([], null, [], "codex", {
      orgAgentConfig: {
        claude_code: { ANTHROPIC_BASE_URL: "https://example.com" },
      },
    });
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

describe("agentTypeForModel", () => {
  it("returns the correct agent for curated OpenCode models", () => {
    expect(agentTypeForModel("anthropic/claude-haiku-4-5")).toBe("opencode");
    expect(agentTypeForModel("anthropic/claude-opus-4-8")).toBe("opencode");
  });

  it("returns undefined for unknown provider/model strings so callers fall back to their default agent", () => {
    // xai/grok-code-fast is not in any curated list; it could be a custom Pi
    // or custom OpenCode model — the caller owns that context.
    expect(agentTypeForModel("xai/grok-code-fast")).toBeUndefined();
  });

  it("routes OpenCode logical ids to OpenCode but leaves bare first-party names with their owner", () => {
    expect(agentTypeForModel("glm-5.2")).toBe("opencode");
    expect(agentTypeForModel("kimi-k2.6")).toBe("opencode");
    expect(agentTypeForModel("openrouter/~z-ai/glm-5.2")).toBe("opencode");
    // Bare names owned by a first-party agent keep that owner.
    expect(agentTypeForModel("gpt-5.6-sol")).toBe("codex");
    expect(agentTypeForModel("gpt-5.6-luna")).toBe("codex");
    expect(agentTypeForModel("gpt-5.6-luna-fast")).toBe("codex");
    expect(agentTypeForModel("gpt-5.5")).toBe("codex");
    expect(agentTypeForModel("claude-fable-5")).toBe("claude_code");
  });

  it("exposes explicit OpenCode custom model metadata", () => {
    expect(AGENTS_BY_KEY.opencode.envVars).toEqual(
      expect.arrayContaining([
        expect.objectContaining({
          name: "OPENCODE_MODEL_CUSTOM",
          placeholder: "provider/model (e.g. xai/grok-code-fast)",
        }),
      ]),
    );
  });
});

describe("pmUsableResolvedCredentials", () => {
  it("excludes personal-scoped rows because PM runs without a user id", () => {
    const credentials = pmUsableResolvedCredentials([
      makeCredential({
        scope: "personal",
        user_id: "user-1",
        agent: "claude_code",
        provider: "anthropic",
      }),
    ]);
    const groups = availableAgentModelGroups(credentials, null, [], "codex");

    expect(credentials).toEqual([]);
    expect(groups.map((g) => g.key)).toEqual(["codex"]);
  });

  it("retains org-scoped rows from the resolved stack for PM runs", () => {
    const credentials = pmUsableResolvedCredentials([
      makeCredential({
        id: "cc-anthropic",
        agent: "claude_code",
        provider: "anthropic",
      }),
      makeCredential({
        id: "cc-opencode",
        agent: "opencode",
        provider: "opencode",
        priority: 2,
      }),
    ]);
    const groups = availableAgentModelGroups(credentials, null, [], "codex");

    expect(credentials).toEqual([
      { provider: "anthropic", source: "org" },
      { provider: "opencode", source: "org" },
    ]);
    expect(groups.map((g) => g.key)).toEqual([
      "codex",
      "claude_code",
      "opencode",
    ]);
  });

  it("keeps org rows even when a personal row shadows them in the resolved stack", () => {
    const credentials = pmUsableResolvedCredentials([
      makeCredential({
        id: "cc-personal",
        scope: "personal",
        user_id: "user-1",
        agent: "claude_code",
        provider: "anthropic",
      }),
      makeCredential({
        id: "cc-org",
        agent: "claude_code",
        provider: "anthropic",
        priority: 2,
      }),
    ]);
    const groups = availableAgentModelGroups(credentials, null, [], "codex");

    expect(credentials).toEqual([{ provider: "anthropic", source: "org" }]);
    expect(groups.map((g) => g.key)).toEqual(["codex", "claude_code"]);
  });

  it("maps subscription rows onto the agent's provider key", () => {
    const credentials = pmUsableResolvedCredentials([
      makeCredential({
        agent: "codex",
        auth_type: "subscription",
        provider: "openai_subscription",
      }),
      makeCredential({
        id: "cc-2",
        agent: "claude_code",
        auth_type: "subscription",
        provider: "anthropic_subscription",
        priority: 2,
      }),
    ]);
    const groups = availableAgentModelGroups(credentials, null, [], "codex");

    expect(credentials).toEqual([
      { provider: "openai", source: "org" },
      { provider: "anthropic", source: "org" },
    ]);
    expect(groups.map((g) => g.key)).toEqual(["codex", "claude_code"]);
  });

  it("collapses multiple org rows for the same provider into the highest-priority row", () => {
    const credentials = pmUsableResolvedCredentials([
      makeCredential({
        id: "cc-1",
        agent: "codex",
        provider: "openai",
        priority: 1,
      }),
      makeCredential({
        id: "cc-2",
        agent: "codex",
        auth_type: "subscription",
        provider: "openai_subscription",
        priority: 2,
      }),
    ]);

    expect(credentials).toEqual([{ provider: "openai", source: "org" }]);
  });
});
