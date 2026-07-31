import { describe, expect, it } from "vitest";
import {
  ORG_DEFAULT_CODING_AGENT_MODELS,
  ORG_DEFAULT_CODING_AGENT_REASONING,
  getOrgDefaultCodingAgentModel,
  getOrgDefaultCodingAgentReasoning,
} from "@/lib/org-coding-agent-defaults";
import type { OrgSettings } from "@/lib/types";

const settings = (partial: Partial<OrgSettings>) => partial as OrgSettings;

describe("getOrgDefaultCodingAgentModel", () => {
  it("falls back to the platform default when the key is absent", () => {
    expect(getOrgDefaultCodingAgentModel(settings({}), "codex")).toBe(ORG_DEFAULT_CODING_AGENT_MODELS.codex);
    expect(getOrgDefaultCodingAgentModel(undefined, "claude_code")).toBe(ORG_DEFAULT_CODING_AGENT_MODELS.claude_code);
  });

  it("honors an explicitly stored opt-out", () => {
    expect(getOrgDefaultCodingAgentModel(settings({ coding_agent_model_defaults: { codex: "" } }), "codex")).toBe("");
  });

  it("returns the stored model", () => {
    expect(getOrgDefaultCodingAgentModel(settings({ coding_agent_model_defaults: { codex: "gpt-5.5" } }), "codex")).toBe("gpt-5.5");
  });

  it("has no platform default for agents outside the curated list", () => {
    expect(getOrgDefaultCodingAgentModel(settings({}), "amp")).toBe("");
    expect(getOrgDefaultCodingAgentModel(settings({ coding_agent_model_defaults: { amp: "deep" } }), "amp")).toBe("deep");
  });
});

describe("getOrgDefaultCodingAgentReasoning", () => {
  it("falls back to the platform default when the key is absent", () => {
    expect(getOrgDefaultCodingAgentReasoning(settings({}), "codex")).toBe(ORG_DEFAULT_CODING_AGENT_REASONING.codex);
    expect(getOrgDefaultCodingAgentReasoning(settings({}), "claude_code")).toBe(ORG_DEFAULT_CODING_AGENT_REASONING.claude_code);
  });

  it("honors an explicitly stored opt-out", () => {
    expect(getOrgDefaultCodingAgentReasoning(settings({ coding_agent_reasoning_defaults: { codex: "" } }), "codex")).toBe("");
  });

  it("drops a level the agent cannot honor", () => {
    // "max" is Claude-Code-only; surfacing it for Codex would submit a value the
    // API rejects with a 400.
    expect(getOrgDefaultCodingAgentReasoning(settings({ coding_agent_reasoning_defaults: { codex: "max" } }), "codex")).toBe("");
    expect(getOrgDefaultCodingAgentReasoning(settings({ coding_agent_reasoning_defaults: { claude_code: "max" } }), "claude_code")).toBe("max");
  });

  it("returns nothing for agents that do not support reasoning effort", () => {
    expect(getOrgDefaultCodingAgentReasoning(settings({}), "amp")).toBe("");
  });
});
