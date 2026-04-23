export type StackAgent = "codex" | "claude_code" | "gemini_cli" | "amp" | "pi";
export type ModalProvider = StackAgent;
export type ApiKeyProvider = StackAgent;
export type PersonalProvider = "openai" | "anthropic" | "gemini" | "amp" | "pi";

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
    supportsSubscription: false,
    supportsStackOrder: true,
  },
];

export const PERSONAL_PROVIDER_OPTIONS: Array<{
  key: PersonalProvider;
  label: string;
  iconSrc?: string;
}> = [
  { key: "openai", label: "Codex", iconSrc: "/agents/codex.svg" },
  { key: "anthropic", label: "Claude Code", iconSrc: "/agents/claude_code.svg" },
  { key: "gemini", label: "Gemini CLI", iconSrc: "/agents/gemini_cli.svg" },
  { key: "amp", label: "Amp", iconSrc: "/agents/amp.svg" },
  { key: "pi", label: "Pi" },
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
