import {
  AVAILABLE_OPENCODE_MODELS,
  OPENCODE_MODEL_GLM_5_2,
  OPENCODE_MODEL_OPENROUTER_GLM_5_2,
  OPENCODE_MODEL_GPT_5_4_MINI,
} from "@/lib/model-constants";
import type { ReleaseStage } from "@/components/release-stage-badge";

export type StackAgent = "codex" | "claude_code" | "amp" | "pi" | "opencode";
export type ModalProvider = StackAgent;
export type ApiKeyProvider = StackAgent;
export type PersonalProvider = "openai" | "anthropic" | "amp" | "pi" | "opencode";

// PERSONAL_PROVIDER_TO_AGENT is the single source of truth for the personal
// page's provider → agent mapping. Typed as Record<PersonalProvider, StackAgent>
// so TypeScript fails to compile when PersonalProvider grows (or shrinks) and
// this map does not — preventing the historical drift between the dialog's
// provider keys and the unified API's agent field.
export const PERSONAL_PROVIDER_TO_AGENT: Record<PersonalProvider, StackAgent> = {
  openai: "codex",
  anthropic: "claude_code",
  amp: "amp",
  pi: "pi",
  opencode: "opencode",
};

// personalProviderToAgent exposes the registry as a function for callers that
// prefer the historical helper shape. The TypeScript signature still enforces
// exhaustiveness via the Record above.
export function personalProviderToAgent(provider: PersonalProvider): StackAgent {
  return PERSONAL_PROVIDER_TO_AGENT[provider];
}

export const ORG_PROVIDER_OPTIONS: Array<{
  key: ModalProvider;
  label: string;
  badge?: ReleaseStage;
  iconSrc?: string;
  supportsSubscription: boolean;
  supportsStackOrder: boolean;
}> = [
  {
    key: "codex",
    label: "Codex",
    iconSrc: "/agents/codex.svg",
    supportsSubscription: true,
    supportsStackOrder: true,
  },
  {
    key: "claude_code",
    label: "Claude Code",
    iconSrc: "/agents/claude_code.svg",
    supportsSubscription: true,
    supportsStackOrder: true,
  },
  {
    key: "opencode",
    label: "OpenCode",
    iconSrc: "/agents/opencode.svg",
    supportsSubscription: false,
    supportsStackOrder: true,
  },
  {
    key: "amp",
    label: "Amp",
    badge: "beta",
    iconSrc: "/agents/amp.svg",
    supportsSubscription: false,
    supportsStackOrder: true,
  },
  {
    key: "pi",
    label: "Pi",
    badge: "beta",
    iconSrc: "/agents/pi.svg",
    supportsSubscription: false,
    supportsStackOrder: true,
  },
];

export const PERSONAL_PROVIDER_OPTIONS: Array<{
  key: PersonalProvider;
  label: string;
  badge?: ReleaseStage;
  iconSrc?: string;
  // Mirrors ORG_PROVIDER_OPTIONS.supportsSubscription. When true the
  // personal Add-auth modal exposes the subscription radio so users can
  // connect their own Codex / Claude Code OAuth flow as a personal-stack
  // credential. The OAuth modal handles the device-code or PKCE handshake;
  // the resulting row lands in coding_credentials with user_id set.
  supportsSubscription: boolean;
}> = [
  { key: "openai", label: "Codex", iconSrc: "/agents/codex.svg", supportsSubscription: true },
  { key: "anthropic", label: "Claude Code", iconSrc: "/agents/claude_code.svg", supportsSubscription: true },
  { key: "opencode", label: "OpenCode", iconSrc: "/agents/opencode.svg", supportsSubscription: false },
  { key: "amp", label: "Amp", badge: "beta", iconSrc: "/agents/amp.svg", supportsSubscription: false },
  { key: "pi", label: "Pi", badge: "beta", iconSrc: "/agents/pi.svg", supportsSubscription: false },
];

export function apiKeyHelp(provider: ApiKeyProvider | PersonalProvider) {
  switch (provider) {
    case "codex":
    case "openai":
      return {
        label: "Codex",
        description: "Open the OpenAI API key management page to create or manage a project or org key.",
        href: "https://platform.openai.com/api-keys",
        linkLabel: "OpenAI API key management",
      };
    case "claude_code":
    case "anthropic":
      return {
        label: "Claude Code",
        description: "Open the Claude API key management page to create or manage an Anthropic key.",
        href: "https://platform.claude.com/settings/keys",
        linkLabel: "Claude API key management",
      };
    case "amp":
      return {
        label: "Amp",
        description: "Find your Amp access token in Amp settings, or use amp login for interactive setup.",
        href: "https://ampcode.com/settings",
        linkLabel: "Amp settings",
      };
    case "pi":
      return {
        label: "Pi",
        description: "Create or manage your Pi API key in the Pi dashboard.",
        href: "https://pi.dev/",
        linkLabel: "Pi dashboard",
      };
    case "opencode":
      return {
        label: "OpenCode",
        description: "Use an OpenCode-scoped key. If it targets a backing provider, store it here rather than reusing Codex or Claude Code credentials.",
        href: "https://opencode.ai/docs",
        linkLabel: "OpenCode docs",
      };
  }
}

export type OpenCodeBackingProvider = "opencode" | "anthropic" | "openai" | "gemini" | "openrouter";
export type OpenCodePresetConfidence = "high" | "medium" | "low";

export const DEFAULT_OPENCODE_BACKING_PROVIDER: OpenCodeBackingProvider = "openrouter";

export interface OpenCodeKeyPresetDetection {
  provider: OpenCodeBackingProvider;
  confidence: OpenCodePresetConfidence;
  message: string;
}

export const OPENCODE_BACKING_PROVIDER_OPTIONS: Array<{ value: OpenCodeBackingProvider; label: string }> = [
  { value: "opencode", label: "OpenCode native" },
  { value: "anthropic", label: "OpenCode via Anthropic" },
  { value: "openai", label: "OpenCode via OpenAI" },
  { value: "gemini", label: "OpenCode via Gemini" },
  { value: "openrouter", label: "OpenCode via OpenRouter" },
];

export const OPENCODE_US_INFERENCE_HELP_TEXT = "Note: we recommend using OpenRouter routes, which are pinned to US based inference providers. Native OpenCode routes are not provider-pinned.";

export function openCodeBackingProviderLabel(provider: OpenCodeBackingProvider): string {
  return OPENCODE_BACKING_PROVIDER_OPTIONS.find((option) => option.value === provider)?.label ?? "OpenCode native";
}

export function openCodeCredentialLabel(provider: OpenCodeBackingProvider): string {
  return `${openCodeBackingProviderLabel(provider)} key`;
}

export function detectOpenCodeKeyPreset(apiKey: string): OpenCodeKeyPresetDetection {
  const trimmed = apiKey.trim();
  if (/^sk-or[-_]/i.test(trimmed) || /^sk-or-v\d+-/i.test(trimmed)) {
    return {
      provider: "openrouter",
      confidence: "high",
      message: "Detected OpenRouter key from the sk-or prefix.",
    };
  }
  if (/^sk-ant[-_]/i.test(trimmed)) {
    return {
      provider: "anthropic",
      confidence: "high",
      message: "Detected Anthropic key from the sk-ant prefix.",
    };
  }
  if (/^AIza/i.test(trimmed)) {
    return {
      provider: "gemini",
      confidence: "high",
      message: "Detected Gemini key from the AIza prefix.",
    };
  }
  if (/^sk-proj[-_]/i.test(trimmed)) {
    return {
      provider: "openai",
      confidence: "medium",
      message: "Detected OpenAI project key from the sk-proj prefix.",
    };
  }
  return {
    provider: DEFAULT_OPENCODE_BACKING_PROVIDER,
    confidence: "low",
    message: trimmed
      ? "This key shape is ambiguous. Defaulting to OpenCode via OpenRouter; you can change the provider."
      : "Paste a key to detect the provider preset.",
  };
}

export function openCodeModelsForBackingProvider(provider: OpenCodeBackingProvider): string[] {
  switch (provider) {
    case "opencode":
      return AVAILABLE_OPENCODE_MODELS.filter((model) => model.startsWith("opencode/"));
    case "anthropic":
      return AVAILABLE_OPENCODE_MODELS.filter((model) => model.startsWith("anthropic/"));
    case "openai":
      return AVAILABLE_OPENCODE_MODELS.filter((model) => model.startsWith("openai/"));
    case "gemini":
      return AVAILABLE_OPENCODE_MODELS.filter((model) => model.startsWith("google/"));
    case "openrouter":
      return AVAILABLE_OPENCODE_MODELS.filter((model) => model.startsWith("openrouter/"));
  }
}

export function openCodeDefaultModelForBackingProvider(provider: OpenCodeBackingProvider): string {
  if (provider === "opencode") {
    return OPENCODE_MODEL_GLM_5_2;
  }
  if (provider === "openrouter") {
    return OPENCODE_MODEL_OPENROUTER_GLM_5_2;
  }
  const models = openCodeModelsForBackingProvider(provider);
  return models[0] ?? OPENCODE_MODEL_GPT_5_4_MINI;
}

// openCodeAgentDefaults builds the agent_defaults map for an OpenCode
// credential. If a non-empty customModel is provided it wins over model and
// is stored as OPENCODE_MODEL_CUSTOM; otherwise only OPENCODE_MODEL is set.
export function openCodeAgentDefaults(model: string, customModel: string): Record<string, string> {
  const custom = customModel.trim();
  if (custom) {
    return { OPENCODE_MODEL: model, OPENCODE_MODEL_CUSTOM: custom };
  }
  return { OPENCODE_MODEL: model };
}
