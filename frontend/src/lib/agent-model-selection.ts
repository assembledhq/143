export interface AgentModelSelection {
  agent: string;
  model: string;
}

export function agentModelSelectionValue(agent: string, model: string): string {
  return `${agent}::${model}`;
}

export function parseAgentModelSelectionValue(value: string): AgentModelSelection {
  const [agent, ...modelParts] = value.split("::");
  return { agent, model: modelParts.join("::") };
}
