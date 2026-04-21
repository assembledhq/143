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
  // inheritsProviderKeys is true for meta-agents (e.g. Pi) that route to other
  // providers and have no dedicated credential store. The personal/team/org
  // credential UIs skip these agents because there is no key to save — they
  // reuse whatever the org has configured for the real provider agents.
  inheritsProviderKeys?: boolean;
  // lacksHeadlessResume is true for agents whose CLI has no flag to resume a
  // prior conversation by ID. Follow-up turns replay only the new user message
  // against the restored filesystem; earlier chat context is not sent back to
  // the CLI. The session UI shows a banner so users include any context they
  // need the agent to remember.
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
    description: "Anthropic Claude (Opus, Sonnet, Haiku)",
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
    lacksHeadlessResume: true,
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
    description: "Pi — meta-agent that routes to many providers",
    providerKey: "pi",
    models: AVAILABLE_PI_MODELS,
    inheritsProviderKeys: true,
    lacksHeadlessResume: true,
    note: "Pi reuses keys from your other configured agents by default. Set values here to override.",
    envVars: [
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

// Resolve the agent type key for a given model string.
export function agentTypeForModel(model: string): string | undefined {
  return AGENTS.find((a) => a.models.includes(model))?.key;
}
