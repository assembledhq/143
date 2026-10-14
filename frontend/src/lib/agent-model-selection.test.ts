import { describe, expect, it } from "vitest";

import {
  agentModelSelectionValue,
  parseAgentModelSelectionValue,
} from "@/lib/agent-model-selection";

describe("agent model selection values", () => {
  it.each([
    {
      name: "a normal provider model id",
      agent: "codex",
      model: "gpt-5.4",
    },
    {
      name: "a model id containing the selection delimiter",
      agent: "opencode",
      model: "provider::model",
    },
    {
      name: "an unknown current agent",
      agent: "",
      model: "custom-model",
    },
  ])("round trips $name", ({ agent, model }) => {
    const value = agentModelSelectionValue(agent, model);

    expect(parseAgentModelSelectionValue(value)).toEqual({ agent, model });
  });
});
