// Single source of truth for coding agents — labels, brand colors, monogram
// badges, models, and per-agent env vars used in the settings UI. The backend
// equivalent lives in internal/models/agent_model_constants.go and
// internal/models/org_settings.go (AgentType constants); keep these in sync.

import {
  AVAILABLE_AMP_MODES,
  AVAILABLE_CLAUDE_CODE_MODELS,
  AVAILABLE_CODEX_MODELS,
  AVAILABLE_GEMINI_CLI_MODELS,
  AVAILABLE_PI_MODELS,
} from "@/lib/model-constants";
import type { CodexAuthStatus, CodingAuth, ResolvedCredential, UserCredentialSummary } from "@/lib/types";

type CodingAuthAvailability = Pick<CodingAuth, "agent" | "status">;

export interface AgentEnvVar {
  name: string;
  label: string;
  sensitive?: boolean;
  placeholder?: string;
  options?: string[];
  advanced?: boolean;
  helpText?: string;
  // hideInSetup suppresses this var in the first-run setup flow.
  // The full settings screen always shows it.
  hideInSetup?: boolean;
}

export interface AgentMeta {
  key: string;
  label: string;
  short: string;       // 2-letter monogram shown inside <AgentBadge>
  color: string;       // brand hex used as the badge background
  description: string;
  providerKey: string;
  models: readonly string[];
  envVars: AgentEnvVar[];
  note?: string;       // small inline note shown in the settings card
  // lacksHeadlessResume is true for agents whose CLI has no flag to resume a
  // prior conversation by ID.
  lacksHeadlessResume?: boolean;
}

export const AGENTS: readonly AgentMeta[] = [
  {
    key: "codex",
    label: "Codex",
    short: "CX",
    color: "#10a37f",
    description: "OpenAI Codex (GPT-5 models)",
    providerKey: "openai",
    models: AVAILABLE_CODEX_MODELS,
    envVars: [
      { name: "OPENAI_API_KEY", label: "API Key", sensitive: true },
      { name: "OPENAI_MODEL", label: "Default model", options: [...AVAILABLE_CODEX_MODELS] },
      { name: "OPENAI_BASE_URL", label: "Base URL", placeholder: "Custom API endpoint (optional)", advanced: true },
    ],
  },
  {
    key: "claude_code",
    label: "Claude Code",
    short: "CC",
    color: "#cc785c",
    description: "Anthropic Claude (Fable, Opus, Sonnet, Haiku)",
    providerKey: "anthropic",
    models: AVAILABLE_CLAUDE_CODE_MODELS,
    envVars: [
      { name: "ANTHROPIC_API_KEY", label: "API Key", sensitive: true },
      { name: "ANTHROPIC_MODEL", label: "Default model", options: [...AVAILABLE_CLAUDE_CODE_MODELS] },
      { name: "ANTHROPIC_BASE_URL", label: "Base URL", placeholder: "Custom API endpoint (optional)", advanced: true, hideInSetup: true },
    ],
  },
  {
    key: "gemini_cli",
    label: "Gemini CLI",
    short: "GE",
    color: "#4285f4",
    description: "Google Gemini (Pro, Flash)",
    providerKey: "gemini",
    models: AVAILABLE_GEMINI_CLI_MODELS,
    envVars: [
      { name: "GEMINI_API_KEY", label: "API Key", sensitive: true },
      { name: "GEMINI_MODEL", label: "Default model", options: [...AVAILABLE_GEMINI_CLI_MODELS] },
    ],
  },
  {
    key: "amp",
    label: "Amp",
    short: "AM",
    color: "#ff5c00",
    description: "Sourcegraph Amp (mode-based agent)",
    providerKey: "amp",
    models: AVAILABLE_AMP_MODES,
    envVars: [
      { name: "AMP_API_KEY", label: "API Key", sensitive: true, placeholder: "amp_..." },
      { name: "AMP_MODE", label: "Default mode", options: [...AVAILABLE_AMP_MODES] },
    ],
  },
  {
    key: "pi",
    label: "Pi",
    short: "PI",
    color: "#7c3aed",
    description: "Pi coding agent with dedicated Pi auth",
    providerKey: "pi",
    models: AVAILABLE_PI_MODELS,
    note: "Pi uses its own API key. Choose a default model if you want Pi to start from a specific provider/model pair.",
    envVars: [
      { name: "PI_API_KEY", label: "API Key", sensitive: true, placeholder: "pi_..." },
      { name: "PI_MODEL", label: "Default model", options: [...AVAILABLE_PI_MODELS] },
      {
        name: "PI_MODEL_CUSTOM",
        label: "Custom model override",
        placeholder: "provider/model (e.g. moonshot/kimi-k2)",
        advanced: true,
        helpText: "Wins over Default model. Pi accepts any provider/model the upstream supports.",
      },
    ],
  },
] as const;

export const AGENTS_BY_KEY: Readonly<Record<string, AgentMeta>> = Object.fromEntries(
  AGENTS.map((agent) => [agent.key, agent]),
);

// Display-only labels for agent_type values that exist on sessions but are
// not user-selectable in the settings UI (so they're intentionally absent
// from AGENTS). AgentBadge consults this as a fallback before rendering the
// raw key.
export const AGENT_DISPLAY_LABELS: Readonly<Record<string, string>> = {
  pm_agent: "PM Agent",
  custom: "Custom",
};

export function agentDisplayLabel(agentType: string): string {
  return AGENTS_BY_KEY[agentType]?.label ?? AGENT_DISPLAY_LABELS[agentType] ?? agentType;
}

// Resolve the agent type key for a given model string.
export function agentTypeForModel(model: string): string | undefined {
  return AGENTS.find((a) => a.models.includes(model))?.key;
}

// True when the user has the credentials needed to run the given agent.
// Codex can auth via OAuth (codexAuthStatus.completed) or an OPENAI_API_KEY
// resolved credential; every other agent requires its provider's resolved
// credential. Source of truth for the new-session picker filter, the
// AgentKeyRequiredBanner gate, and setup-readiness checks.
export function isAgentConnected(
  agentType: string,
  resolvedCredentials: readonly ResolvedCredential[],
  codexAuthStatus?: CodexAuthStatus | null,
): boolean {
  if (agentType === "codex" && codexAuthStatus?.status === "completed") return true;
  const agent = AGENTS_BY_KEY[agentType];
  if (!agent) return false;
  return resolvedCredentials.some(
    (c) => c.provider === agent.providerKey && c.source !== "none",
  );
}

function codingAuthStatusAllowsSelection(status: CodingAuth["status"]): boolean {
  return status === "healthy" || status === "rate_limited";
}

export function isAgentAvailable(
  agentType: string,
  resolvedCredentials: readonly ResolvedCredential[],
  codexAuthStatus?: CodexAuthStatus | null,
  codingAuths: readonly CodingAuthAvailability[] = [],
): boolean {
  if (isAgentConnected(agentType, resolvedCredentials, codexAuthStatus)) return true;
  return codingAuths.some(
    (row) => row.agent === agentType && codingAuthStatusAllowsSelection(row.status),
  );
}

export interface AgentModelGroup {
  key: string;
  label: string;
  models: readonly string[];
}

export interface AvailableAgentModelGroupsOptions {
  // orgAgentConfig: when provided, agents whose sensitive env var (the API key)
  // is set in the org-level agent_config also pass the availability filter.
  // PM dropdowns pass this because the PM runs server-side using org keys —
  // an admin without personal credentials should still see provider groups
  // the org has configured. The session picker omits this since sessions run
  // under the user's own credentials.
  orgAgentConfig?: Record<string, Record<string, string>>;
}

// availableAgentModelGroups returns model groups suitable for the session and
// PM model dropdowns. An agent passes the availability filter when any of:
//   1. isAgentAvailable returns true (user has resolved creds / Codex OAuth /
//      coding auth for this agent),
//   2. orgAgentConfig has the agent's API key set (PM-only, see options),
//   3. it is the default agent (always retained so the dropdown is never empty).
// Groups are sorted with the default agent first; Amp's label is rewritten to
// "Amp modes" so users can see those rows are agent modes, not model IDs.
export function availableAgentModelGroups(
  resolvedCredentials: readonly ResolvedCredential[],
  codexAuthStatus: CodexAuthStatus | null | undefined,
  codingAuths: readonly CodingAuthAvailability[],
  defaultAgentType: string,
  options: AvailableAgentModelGroupsOptions = {},
): AgentModelGroup[] {
  const orgAgentConfig = options.orgAgentConfig;
  const orgConfiguredAgent = (agent: AgentMeta): boolean => {
    if (!orgAgentConfig) return false;
    const apiKeyVar = agent.envVars.find((v) => v.sensitive)?.name;
    if (!apiKeyVar) return false;
    return Boolean(orgAgentConfig[agent.key]?.[apiKeyVar]);
  };
  const filtered = AGENTS.filter(
    (agent) =>
      isAgentAvailable(agent.key, resolvedCredentials, codexAuthStatus, codingAuths) ||
      orgConfiguredAgent(agent) ||
      agent.key === defaultAgentType,
  );
  filtered.sort((a, b) => {
    if (a.key === defaultAgentType) return -1;
    if (b.key === defaultAgentType) return 1;
    return AGENTS.indexOf(a) - AGENTS.indexOf(b);
  });
  return filtered.map((agent) => ({
    key: agent.key,
    label: agent.key === "amp" ? `${agent.label} modes` : agent.label,
    models: agent.models,
  }));
}

// PM jobs run server-side without a user id, so they cannot use the current
// admin's personal credentials. Keep org/team-default resolved credentials,
// then add explicit team defaults because the resolved endpoint reports only
// the first source for a provider and a personal credential can shadow a
// PM-usable team default.
export function pmUsableResolvedCredentials(
  resolvedCredentials: readonly ResolvedCredential[],
  teamDefaults: readonly UserCredentialSummary[],
): ResolvedCredential[] {
  const byProvider = new Map<string, ResolvedCredential>();

  for (const credential of resolvedCredentials) {
    if (credential.source !== "org" && credential.source !== "team_default") {
      continue;
    }
    byProvider.set(credential.provider, credential);
  }

  for (const credential of teamDefaults) {
    if (!credential.configured || !credential.is_team_default) {
      continue;
    }
    byProvider.set(credential.provider, {
      provider: credential.provider,
      source: "team_default",
      masked_key: credential.masked_key,
    });
  }

  return Array.from(byProvider.values());
}
