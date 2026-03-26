import type { OrgSettings, PMDocument, PMPlan, PMStatus, CodexAuthStatus } from "@/lib/types";

export const DEFAULT_PRIORITY_WEIGHTS: NonNullable<OrgSettings["priority_weights"]> = {
  customer_impact: 0.35,
  severity: 0.25,
  recency: 0.2,
  revenue_risk: 0.2,
};

export function isAgentConnected(
  agentType: NonNullable<OrgSettings["default_agent_type"]>,
  agentConfig: Record<string, Record<string, string>>,
  agentDefaults: Record<string, Record<string, string>>,
  codexAuthStatus?: CodexAuthStatus | null,
): boolean {
  switch (agentType) {
    case "codex":
      return codexAuthStatus?.status === "completed"
        || Boolean(agentConfig.codex?.OPENAI_API_KEY)
        || Boolean(agentDefaults.codex?.OPENAI_API_KEY);
    case "claude_code":
      return Boolean(agentConfig.claude_code?.ANTHROPIC_API_KEY)
        || Boolean(agentDefaults.claude_code?.ANTHROPIC_API_KEY);
    case "gemini_cli":
      return Boolean(agentConfig.gemini_cli?.GEMINI_API_KEY)
        || Boolean(agentDefaults.gemini_cli?.GEMINI_API_KEY);
    default:
      return false;
  }
}

export type AutopilotHeroMode = "setup" | "first_analysis" | "recommendation" | "attention";

export interface AutopilotSetupState {
  agentConnected: boolean;
  githubReady: boolean;
  connectedCount: number;
  totalCount: number;
}

export interface AutopilotViewModel {
  heroMode: AutopilotHeroMode;
  heroTitle: string;
  heroBody: string;
  primaryActionLabel: string;
  autonomyLabel: string;
  philosophySummary: string;
  directionSummary: string;
  focusAreas: string[];
  avoidAreas: string[];
  weightsSummary: string;
  documentsSummary: string;
  evidence: Array<{ label: string; value: string }>;
}

function formatWeight(value?: number): string {
  return String(Math.round((value ?? 0) * 100));
}

export function getAutonomyLabel(level?: OrgSettings["autonomy_level"]): string {
  switch (level) {
    case "manual":
      return "Suggest";
    case "auto_all":
      return "Operate broadly";
    case "auto_simple":
    default:
      return "Act on low-risk";
  }
}

function getDirection(settings: OrgSettings): string {
  return settings.product_context?.direction?.trim()
    || settings.product_direction?.trim()
    || "";
}

function getDocumentsSummary(documents: PMDocument[]): string {
  return `${documents.length} attached`;
}

export function deriveAutopilotViewModel({
  settings,
  pmStatus,
  latestPlan,
  documents,
  setup,
}: {
  settings: OrgSettings;
  pmStatus: PMStatus;
  latestPlan: PMPlan | null;
  documents: PMDocument[];
  setup: AutopilotSetupState;
}): AutopilotViewModel {
  const autonomyLabel = getAutonomyLabel(settings.autonomy_level);
  const directionSummary = getDirection(settings) || "Not set yet";
  const philosophySummary = settings.product_context?.philosophy?.trim() || "Not set yet";
  const focusAreas = settings.product_context?.focus_areas ?? [];
  const avoidAreas = settings.product_context?.avoid_areas ?? [];
  const weights = settings.priority_weights ?? {};
  const weightsSummary = [
    `Impact ${formatWeight(weights.customer_impact)}`,
    `Severity ${formatWeight(weights.severity)}`,
    `Recency ${formatWeight(weights.recency)}`,
    `Revenue ${formatWeight(weights.revenue_risk)}`,
  ].join(" · ");
  const documentsSummary = getDocumentsSummary(documents);

  let heroMode: AutopilotHeroMode = "recommendation";
  let heroTitle = "Recommendation";
  let heroBody = latestPlan?.analysis?.trim()
    || "Autopilot will summarize what matters most after the next analysis.";
  let primaryActionLabel = "Run analysis";

  if (!setup.agentConnected || !setup.githubReady) {
    heroMode = "setup";
    heroTitle = "Autopilot needs a few connections before it can start analyzing.";
    heroBody = "Connect a coding agent and GitHub repositories, then run the first analysis.";
    primaryActionLabel = "Complete setup";
  } else if (pmStatus.last_error || pmStatus.last_run_status === "failed") {
    heroMode = "attention";
    heroTitle = "Attention needed";
    heroBody = pmStatus.last_error || "The last analysis failed. Review setup or rerun the PM agent.";
    primaryActionLabel = "Run analysis";
  } else if (!latestPlan) {
    heroMode = "first_analysis";
    heroTitle = "Run the first analysis and Autopilot will tell you what matters next.";
    heroBody = "Autopilot will group related issues, identify what is highest leverage, and suggest what your agents should work on first.";
    primaryActionLabel = "Run first analysis";
  }

  const evidence: Array<{ label: string; value: string }> = [
    { label: "Success rate", value: `${Math.round(pmStatus.success_rate ?? 0)}%` },
    { label: "Issues reviewed", value: String(pmStatus.issues_reviewed ?? latestPlan?.issues_reviewed ?? 0) },
    { label: "Delegated", value: String(pmStatus.total_delegated ?? 0) },
    { label: "Next run", value: pmStatus.next_run_in ?? "Not scheduled" },
  ];

  return {
    heroMode,
    heroTitle,
    heroBody,
    primaryActionLabel,
    autonomyLabel,
    philosophySummary,
    directionSummary,
    focusAreas,
    avoidAreas,
    weightsSummary,
    documentsSummary,
    evidence,
  };
}
