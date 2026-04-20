// Visual marker for an agent type — a small colored monogram next to the
// agent's label. Falls back to a plain text label for unknown agent types
// (e.g. "pm_agent", "custom") so callers don't need to special-case them.
import { AGENTS_BY_KEY } from "@/lib/agents";

export function AgentBadge({
  agentType,
  hideLabel = false,
  className = "",
}: {
  agentType: string;
  hideLabel?: boolean;
  className?: string;
}) {
  const meta = AGENTS_BY_KEY[agentType];

  if (!meta) {
    return <span className="text-sm text-muted-foreground">{agentType}</span>;
  }

  return (
    <span className="inline-flex items-center gap-2 align-middle">
      <span
        className={`flex h-5 w-5 shrink-0 items-center justify-center rounded text-xs font-semibold leading-none text-white ${className}`}
        style={{ backgroundColor: meta.color }}
        aria-hidden="true"
      >
        {meta.short}
      </span>
      {!hideLabel && <span className="text-sm">{meta.label}</span>}
    </span>
  );
}
