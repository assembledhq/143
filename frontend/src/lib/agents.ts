// Single source of truth for coding agents — labels, brand colors, monogram
// badges, models, and per-agent env vars used in the settings UI. The backend
// equivalent lives in internal/models/agent_model_constants.go and
// internal/models/org_settings.go (AgentType constants); keep these in sync.

import {
  AVAILABLE_AMP_MODES,
  AVAILABLE_CLAUDE_CODE_MODELS,
  AVAILABLE_CODEX_MODELS,
  AVAILABLE_OPENCODE_MODELS,
  AVAILABLE_PI_MODELS,
} from "@/lib/model-constants";
import type { CodexAuthStatus, CodingCredentialSummary, ResolvedCredential } from "@/lib/types";

type CodingAuthAvailability = Pick<CodingCredentialSummary, "agent" | "status">;

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
        placeholder: "provider/model (e.g. moonshot/kimi-k2.6)",
        advanced: true,
        helpText: "Wins over Default model. Pi accepts any provider/model the upstream supports.",
      },
    ],
  },
  {
    key: "opencode",
    label: "OpenCode",
    short: "OC",
    color: "#111827",
    description: "OpenCode multi-provider coding agent",
    providerKey: "opencode",
    models: AVAILABLE_OPENCODE_MODELS,
    note: "OpenCode uses explicit OpenCode-scoped keys. A key may target OpenCode native auth or a backing provider, but it is stored separately from Codex and Claude Code keys.",
    envVars: [
      { name: "OPENCODE_API_KEY", label: "API Key", sensitive: true, placeholder: "OpenCode or provider API key" },
      { name: "OPENCODE_MODEL", label: "Default model", options: [...AVAILABLE_OPENCODE_MODELS] },
      {
        name: "OPENCODE_MODEL_CUSTOM",
        label: "Custom model override",
        placeholder: "provider/model (e.g. xai/grok-code-fast)",
        advanced: true,
        helpText: "Wins over Default model. OpenCode accepts provider/model ids from its upstream catalog.",
      },
      { name: "OPENCODE_BACKING_PROVIDER", label: "Backing provider", placeholder: "opencode, openai, anthropic, gemini, or openrouter", advanced: true },
      { name: "OPENCODE_BASE_URL", label: "Base URL", placeholder: "Custom API endpoint (optional)", advanced: true },
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
  if (!model) return undefined;
  for (const agent of AGENTS) {
    if (agent.key === "pi") continue;
    if (agent.models.includes(model)) return agent.key;
  }
  if (AGENTS_BY_KEY.pi.models.includes(model)) return "pi";
  // Unknown provider/model strings (custom Pi or custom OpenCode models) cannot
  // be unambiguously classified — let callers fall back to their default agent.
  return undefined;
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

function codingAuthStatusAllowsSelection(status: CodingCredentialSummary["status"]): boolean {
  return status === "healthy" || status === "rate_limited";
}

// isAgentAvailable extends isAgentConnected with unified coding-credential
// rows (typically codingCredentials.list("resolved") or list("org")) — any
// usable row for the agent makes it selectable.
export function isAgentAvailable(
  agentType: string,
  resolvedCredentials: readonly ResolvedCredential[],
  codexAuthStatus?: CodexAuthStatus | null,
  codingCredentials: readonly CodingAuthAvailability[] = [],
): boolean {
  if (isAgentConnected(agentType, resolvedCredentials, codexAuthStatus)) return true;
  return codingCredentials.some(
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
  codingCredentials: readonly CodingAuthAvailability[],
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
      isAgentAvailable(agent.key, resolvedCredentials, codexAuthStatus, codingCredentials) ||
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
// admin's personal credentials. Keep only the org-scoped rows from the
// unified resolved stack (codingCredentials.list("resolved")), collapsing
// them into one provider-keyed ResolvedCredential per provider. Subscription
// rows ("openai_subscription" / "anthropic_subscription") are mapped to their
// agent's provider key so isAgentConnected's providerKey matching sees them.
export function pmUsableResolvedCredentials(
  resolvedCredentials: readonly CodingCredentialSummary[],
): ResolvedCredential[] {
  const byProvider = new Map<string, ResolvedCredential>();

  for (const row of resolvedCredentials) {
    if (row.scope !== "org") {
      continue;
    }
    const provider = AGENTS_BY_KEY[row.agent]?.providerKey ?? row.provider;
    // The resolved stack is ordered by priority — keep the first (highest
    // priority) row per provider.
    if (byProvider.has(provider)) {
      continue;
    }
    byProvider.set(provider, { provider, source: "org" });
  }

  return Array.from(byProvider.values());
}
