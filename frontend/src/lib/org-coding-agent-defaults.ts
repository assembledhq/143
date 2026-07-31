import { DEFAULT_CLAUDE_CODE_MODEL, DEFAULT_CODEX_MODEL } from "@/lib/model-constants";
import {
  isCodingAgentReasoningEffortSupported,
  type CodingAgentReasoningAgent,
  type CodingAgentReasoningEffort,
} from "@/lib/coding-agent-reasoning";
import type { OrgSettings } from "@/lib/types";

// Mirrors DefaultCodingAgentModelDefaults / DefaultCodingAgentReasoningDefaults
// in internal/models/agent_model_constants.go. ParseOrgSettings back-fills these
// server-side for every org that never saved the keys, but GET /api/v1/settings
// returns the *raw* settings blob — so without mirroring them the UI would show
// "no default" for the values sessions actually run with. Keep both sides in sync.
export const ORG_DEFAULT_CODING_AGENT_MODELS = {
  codex: DEFAULT_CODEX_MODEL,
  claude_code: DEFAULT_CLAUDE_CODE_MODEL,
} as const satisfies Record<CodingAgentReasoningAgent, string>;

export const ORG_DEFAULT_CODING_AGENT_REASONING = {
  codex: "high",
  claude_code: "max",
} as const satisfies Record<CodingAgentReasoningAgent, CodingAgentReasoningEffort>;

type OrgDefaultAgentKey = keyof NonNullable<OrgSettings["coding_agent_model_defaults"]>;

// Amp/Pi/OpenCode carry no platform-wide default, so they only ever surface an
// explicitly stored value.
function hasPlatformDefault(agentType: string): agentType is CodingAgentReasoningAgent {
  return agentType in ORG_DEFAULT_CODING_AGENT_MODELS;
}

/**
 * Effective org default model for an agent. An explicitly stored empty string is
 * the admin's "provider default" opt-out and is honored — only an absent key
 * falls back to the platform default, matching ParseOrgSettings' key-presence
 * check.
 */
export function getOrgDefaultCodingAgentModel(settings: OrgSettings | null | undefined, agentType: string): string {
  const stored = settings?.coding_agent_model_defaults?.[agentType as OrgDefaultAgentKey];
  if (stored !== undefined) {
    return stored;
  }
  return hasPlatformDefault(agentType) ? ORG_DEFAULT_CODING_AGENT_MODELS[agentType] : "";
}

/**
 * Effective org default reasoning effort for an agent. Levels the agent can't
 * honor are dropped rather than surfaced, so a stale stored value reads as "no
 * default" instead of failing session creation with a 400.
 */
export function getOrgDefaultCodingAgentReasoning(
  settings: OrgSettings | null | undefined,
  agentType: string,
): CodingAgentReasoningEffort {
  const stored = settings?.coding_agent_reasoning_defaults?.[agentType as CodingAgentReasoningAgent];
  const effort = stored !== undefined
    ? stored
    : hasPlatformDefault(agentType)
      ? ORG_DEFAULT_CODING_AGENT_REASONING[agentType]
      : "";
  return effort && isCodingAgentReasoningEffortSupported(agentType, effort) ? effort : "";
}
