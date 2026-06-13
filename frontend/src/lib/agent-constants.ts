// Re-exports the canonical AGENTS registry from @/lib/agents under the
// legacy AGENT_TYPES name so the existing call sites keep working.
import { AGENTS, type AgentEnvVar, type AgentMeta } from "@/lib/agents";

export type { AgentEnvVar };
export type AgentType = AgentMeta;

export const AGENT_TYPES: readonly AgentMeta[] = AGENTS;

export const KEY_PLACEHOLDERS: Record<string, string> = {
  anthropic: "sk-ant-...",
  openai: "sk-...",
  gemini: "AIza...",
  amp: "amp_...",
};

export function sourceLabel(source: string): string {
  switch (source) {
    case "personal": return "Your key";
    case "org": return "Organization";
    default: return "Not configured";
  }
}

export function sourceBadgeVariant(source: string): "success" | "secondary" | "outline" | "destructive" {
  switch (source) {
    case "personal": return "success";
    case "org": return "secondary";
    default: return "outline";
  }
}

export function providerDisplayName(providerKey: string): string {
  const agent = AGENT_TYPES.find((a) => a.providerKey === providerKey);
  return agent?.label ?? providerKey;
}
