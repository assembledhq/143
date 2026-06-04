export type StackAgent = "codex" | "claude_code" | "gemini_cli" | "amp" | "pi";
export type ModalProvider = StackAgent;
export type ApiKeyProvider = StackAgent;
export type PersonalProvider = "openai" | "anthropic" | "gemini" | "amp" | "pi";

// PERSONAL_PROVIDER_TO_AGENT is the single source of truth for the personal
// page's provider → agent mapping. Typed as Record<PersonalProvider, StackAgent>
// so TypeScript fails to compile when PersonalProvider grows (or shrinks) and
// this map does not — preventing the historical drift between the dialog's
// provider keys and the unified API's agent field.
export const PERSONAL_PROVIDER_TO_AGENT: Record<PersonalProvider, StackAgent> = {
  openai: "codex",
  anthropic: "claude_code",
  gemini: "gemini_cli",
  amp: "amp",
  pi: "pi",
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
    key: "gemini_cli",
    label: "Gemini CLI",
    iconSrc: "/agents/gemini_cli.svg",
    supportsSubscription: false,
    supportsStackOrder: true,
  },
  {
    key: "amp",
    label: "Amp",
    iconSrc: "/agents/amp.svg",
    supportsSubscription: false,
    supportsStackOrder: true,
  },
  {
    key: "pi",
    label: "Pi",
    iconSrc: "/agents/pi.svg",
    supportsSubscription: false,
    supportsStackOrder: true,
  },
];

export const PERSONAL_PROVIDER_OPTIONS: Array<{
  key: PersonalProvider;
  label: string;
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
  { key: "gemini", label: "Gemini CLI", iconSrc: "/agents/gemini_cli.svg", supportsSubscription: false },
  { key: "amp", label: "Amp", iconSrc: "/agents/amp.svg", supportsSubscription: false },
  { key: "pi", label: "Pi", iconSrc: "/agents/pi.svg", supportsSubscription: false },
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
    case "gemini_cli":
    case "gemini":
      return {
        label: "Gemini CLI",
        description: "Open the Google AI Studio API key management page to create or manage Gemini keys.",
        href: "https://aistudio.google.com/apikey",
        linkLabel: "Google AI Studio API keys",
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
  }
}
