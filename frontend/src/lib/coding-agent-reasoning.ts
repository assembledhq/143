import type { UserSettings } from "@/lib/types";

const SHARED_CODING_AGENT_REASONING_OPTIONS = [
  { value: "low", label: "Low" },
  { value: "medium", label: "Medium" },
  { value: "high", label: "High" },
  { value: "xhigh", label: "Extra High" },
] as const;

export const CODING_AGENT_REASONING_OPTIONS_BY_AGENT = {
  codex: {
    label: "Codex",
    options: SHARED_CODING_AGENT_REASONING_OPTIONS,
  },
  claude_code: {
    label: "Claude Code",
    options: [
      ...SHARED_CODING_AGENT_REASONING_OPTIONS,
      { value: "max", label: "Max" },
    ] as const,
  },
} as const;

export type CodingAgentReasoningAgent = keyof typeof CODING_AGENT_REASONING_OPTIONS_BY_AGENT;
export type CodingAgentReasoningEffort =
  | ""
  | (typeof CODING_AGENT_REASONING_OPTIONS_BY_AGENT.codex.options)[number]["value"]
  | (typeof CODING_AGENT_REASONING_OPTIONS_BY_AGENT.claude_code.options)[number]["value"];

export function supportsReasoningEffort(agentType: string): boolean {
  return agentType in CODING_AGENT_REASONING_OPTIONS_BY_AGENT;
}

export function getCodingAgentReasoningOptions(agentType: string) {
  if (!supportsReasoningEffort(agentType)) {
    return [];
  }
  return CODING_AGENT_REASONING_OPTIONS_BY_AGENT[agentType as CodingAgentReasoningAgent].options;
}

export function isCodingAgentReasoningEffortSupported(agentType: string, value: string): value is Exclude<CodingAgentReasoningEffort, ""> {
  return getCodingAgentReasoningOptions(agentType).some((option) => option.value === value);
}

export function toCodingAgentReasoningEffort(value: string): CodingAgentReasoningEffort {
  if (value === "low" || value === "medium" || value === "high" || value === "xhigh" || value === "max") {
    return value;
  }
  return "";
}

export function getCodingAgentReasoningDefaultsFromSettings(settings?: UserSettings | null): Partial<Record<CodingAgentReasoningAgent, Exclude<CodingAgentReasoningEffort, "">>> {
  const defaults = settings?.coding_agent_reasoning_defaults;
  if (!defaults) {
    return {};
  }

  const normalized: Partial<Record<CodingAgentReasoningAgent, Exclude<CodingAgentReasoningEffort, "">>> = {};
  for (const [agentType, value] of Object.entries(defaults)) {
    const effort = toCodingAgentReasoningEffort(value ?? "");
    if (supportsReasoningEffort(agentType) && isCodingAgentReasoningEffortSupported(agentType, effort)) {
      normalized[agentType as CodingAgentReasoningAgent] = effort;
    }
  }
  return normalized;
}

export function getDefaultCodingAgentReasoningForAgent(settings: UserSettings | null | undefined, agentType: string): CodingAgentReasoningEffort {
  const defaults = getCodingAgentReasoningDefaultsFromSettings(settings);
  const effort = defaults[agentType as CodingAgentReasoningAgent];
  return effort && isCodingAgentReasoningEffortSupported(agentType, effort) ? effort : "";
}
