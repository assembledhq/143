// Visual marker for an agent type — the agent's brand icon next to its label.
// Brand SVGs live in /public/agents/<key>.svg (sourced from simple-icons) and
// are tinted to the agent's brand color via CSS mask-image. Agents without a
// brand icon fall back to the colored monogram square. Agent types not in the
// settings registry (e.g. "pm_agent", "custom") fall back to plain text.
import { cn } from "@/lib/utils";
import { AGENTS_BY_KEY, AGENT_DISPLAY_LABELS } from "@/lib/agents";

// Agent keys that have a brand SVG available at /agents/<key>.svg.
const AGENTS_WITH_ICON = new Set(["codex", "claude_code", "amp", "opencode"]);

export function AgentBadge({
  agentType,
  hideLabel = false,
  className = "",
  labelClassName = "text-sm",
}: {
  agentType: string;
  hideLabel?: boolean;
  className?: string;
  labelClassName?: string;
}) {
  const meta = AGENTS_BY_KEY[agentType];

  if (!meta) {
    const label = AGENT_DISPLAY_LABELS[agentType] ?? agentType;
    return <span className={`${labelClassName} text-muted-foreground`}>{label}</span>;
  }

  const hasIcon = AGENTS_WITH_ICON.has(agentType);

  return (
    <span className="inline-flex items-center gap-1.5 align-middle">
      {hasIcon ? (
        <span
          className={cn("inline-block h-5 w-5 shrink-0", className)}
          style={{
            backgroundColor: meta.color,
            WebkitMaskImage: `url(/agents/${agentType}.svg)`,
            maskImage: `url(/agents/${agentType}.svg)`,
            WebkitMaskRepeat: "no-repeat",
            maskRepeat: "no-repeat",
            WebkitMaskSize: "contain",
            maskSize: "contain",
            WebkitMaskPosition: "center",
            maskPosition: "center",
          }}
          aria-hidden="true"
        />
      ) : (
        <span
          className={cn(
            "flex h-5 w-5 shrink-0 items-center justify-center rounded text-xs font-semibold leading-none text-white",
            className,
          )}
          style={{ backgroundColor: meta.color }}
          aria-hidden="true"
        >
          {meta.short}
        </span>
      )}
      {!hideLabel && <span className={labelClassName}>{meta.label}</span>}
    </span>
  );
}
