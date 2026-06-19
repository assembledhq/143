import { describe, expect, it } from "vitest";
import {
  getCodingAgentReasoningDefaultsFromSettings,
  getDefaultCodingAgentReasoningForAgent,
} from "./coding-agent-reasoning";

describe("coding-agent reasoning settings", () => {
  it("reads per-agent defaults from user settings", () => {
    const settings = {
      coding_agent_reasoning_defaults: {
        codex: "xhigh",
        claude_code: "max",
      },
    } as const;

    expect(getDefaultCodingAgentReasoningForAgent(settings, "codex")).toBe("xhigh");
    expect(getDefaultCodingAgentReasoningForAgent(settings, "claude_code")).toBe("max");
  });

  it("drops values unsupported by the selected agent", () => {
    const settings = {
      coding_agent_reasoning_defaults: {
        codex: "max",
        claude_code: "max",
      },
    } as const;

    expect(getDefaultCodingAgentReasoningForAgent(settings, "codex")).toBe("");
    expect(getDefaultCodingAgentReasoningForAgent(settings, "claude_code")).toBe("max");
  });

  it("returns only supported defaults when normalizing the settings object", () => {
    const settings = {
      coding_agent_reasoning_defaults: {
        codex: "xhigh",
        claude_code: "max",
        opencode: "high",
      },
    } as unknown as Parameters<typeof getCodingAgentReasoningDefaultsFromSettings>[0];

    expect(getCodingAgentReasoningDefaultsFromSettings(settings)).toEqual({
      codex: "xhigh",
      claude_code: "max",
    });
  });
});
