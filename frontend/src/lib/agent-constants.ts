import { AVAILABLE_CLAUDE_CODE_MODELS, AVAILABLE_CODEX_MODELS, AVAILABLE_GEMINI_CLI_MODELS } from "@/lib/model-constants";

export interface AgentEnvVar {
  name: string;
  label: string;
  sensitive?: boolean;
  placeholder?: string;
  options?: string[];
  advanced?: boolean;
}

export interface AgentType {
  key: string;
  label: string;
  description: string;
  providerKey: string;
  envVars: AgentEnvVar[];
}

export const AGENT_TYPES: AgentType[] = [
  {
    key: "codex",
    label: "Codex",
    description: "OpenAI Codex (GPT-5 models)",
    providerKey: "openai",
    envVars: [
      { name: "OPENAI_API_KEY", label: "API Key", sensitive: true },
      { name: "OPENAI_MODEL", label: "Default model", options: [...AVAILABLE_CODEX_MODELS] },
      { name: "OPENAI_BASE_URL", label: "Base URL", placeholder: "Custom API endpoint (optional)", advanced: true },
    ],
  },
  {
    key: "claude_code",
    label: "Claude Code",
    description: "Anthropic Claude (Opus, Sonnet, Haiku)",
    providerKey: "anthropic",
    envVars: [
      { name: "ANTHROPIC_API_KEY", label: "API Key", sensitive: true },
      { name: "ANTHROPIC_MODEL", label: "Default model", options: [...AVAILABLE_CLAUDE_CODE_MODELS] },
      { name: "ANTHROPIC_BASE_URL", label: "Base URL", placeholder: "Custom API endpoint (optional)", advanced: true },
    ],
  },
  {
    key: "gemini_cli",
    label: "Gemini CLI",
    description: "Google Gemini (Pro, Flash)",
    providerKey: "gemini",
    envVars: [
      { name: "GEMINI_API_KEY", label: "API Key", sensitive: true },
      { name: "GEMINI_MODEL", label: "Default model", options: [...AVAILABLE_GEMINI_CLI_MODELS] },
    ],
  },
];

export const KEY_PLACEHOLDERS: Record<string, string> = {
  anthropic: "sk-ant-...",
  openai: "sk-...",
  gemini: "AIza...",
};

export function sourceLabel(source: string): string {
  switch (source) {
    case "personal": return "Your key";
    case "team_default": return "Team default";
    case "org": return "Organization";
    default: return "Not configured";
  }
}

export function sourceBadgeVariant(source: string): "success" | "secondary" | "outline" | "destructive" {
  switch (source) {
    case "personal": return "success";
    case "team_default":
    case "org": return "secondary";
    default: return "outline";
  }
}

export function providerDisplayName(providerKey: string): string {
  const agent = AGENT_TYPES.find((a) => a.providerKey === providerKey);
  return agent?.label ?? providerKey;
}
