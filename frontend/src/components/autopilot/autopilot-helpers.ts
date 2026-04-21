import type { OrgSettings, PMDocument, PMPlan, PMStatus, CodexAuthStatus } from "@/lib/types";

export const DEFAULT_PRIORITY_WEIGHTS: Required<NonNullable<OrgSettings["priority_weights"]>> = {
  customer_impact: 0.35,
  severity: 0.25,
  recency: 0.2,
  revenue_risk: 0.2,
};

export function isAgentConnected(
  agentType: NonNullable<OrgSettings["default_agent_type"]>,
  agentConfig: Record<string, Record<string, string>>,
  codexAuthStatus?: CodexAuthStatus | null,
): boolean {
  switch (agentType) {
    case "codex":
      return codexAuthStatus?.status === "completed"
        || Boolean(agentConfig.codex?.OPENAI_API_KEY);
    case "claude_code":
      return Boolean(agentConfig.claude_code?.ANTHROPIC_API_KEY);
    case "gemini_cli":
      return Boolean(agentConfig.gemini_cli?.GEMINI_API_KEY);
    case "amp":
      return Boolean(agentConfig.amp?.AMP_API_KEY);
    case "pi":
      // Pi routes to other providers and reuses their keys by default.
      return Boolean(
        agentConfig.pi?.PI_MODEL
          || agentConfig.pi?.PI_MODEL_CUSTOM
          || agentConfig.claude_code?.ANTHROPIC_API_KEY
          || agentConfig.codex?.OPENAI_API_KEY
          || agentConfig.gemini_cli?.GEMINI_API_KEY
          || codexAuthStatus?.status === "completed",
      );
    default:
      return false;
  }
}

export type AutopilotHeroMode = "first_analysis" | "recommendation" | "attention";

export interface AutopilotViewModel {
  heroMode: AutopilotHeroMode;
  heroTitle: string;
  heroBody: string;
  primaryActionLabel: string;
  autonomyLabel: string;
  directionSummary: string;
  focusAreas: string[];
  weightsSummary: string;
  documentsSummary: string;
  evidence: Array<{ label: string; value: string }>;
  hasEvidence: boolean;
  statusLine: string;
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
    || settings.product_context?.philosophy?.trim()
    || settings.product_direction?.trim()
    || "";
}

function getDocumentsSummary(documents: PMDocument[]): string {
  return `${documents.length} attached`;
}

function truncateError(error?: string): string | undefined {
  if (!error) return undefined;
  if (error.length <= 150) return error;
  const cut = error.lastIndexOf(" ", 150);
  return error.slice(0, cut > 80 ? cut : 150).trim() + "...";
}

export function formatFreshness(lastRunAt?: string, now = Date.now()): string {
  if (!lastRunAt) return "No analysis yet";
  const delta = now - new Date(lastRunAt).getTime();
  const minutes = Math.floor(delta / 60_000);
  if (minutes < 1) return "Analyzed just now";
  if (minutes < 60) return `Analyzed ${minutes}m ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `Analyzed ${hours}h ago`;
  return `Last analyzed ${new Date(lastRunAt).toLocaleDateString("en-US", { month: "short", day: "numeric" })}`;
}

function formatNextRun(nextRunIn?: string): string {
  if (!nextRunIn) return "";
  const label = nextRunIn.replace(/^in\s+/i, "");
  return `Next in ${label}`;
}

export function deriveAutopilotViewModel({
  settings,
  pmStatus,
  latestPlan,
  documents,
}: {
  settings: OrgSettings;
  pmStatus: PMStatus;
  latestPlan: PMPlan | null;
  documents: PMDocument[];
}): AutopilotViewModel {
  const autonomyLabel = getAutonomyLabel(settings.autonomy_level);
  const directionSummary = getDirection(settings);
  const focusAreas = settings.product_context?.focus_areas ?? [];
  const weights = settings.priority_weights ?? {};
  const weightsSummary = [
    `Impact ${formatWeight(weights.customer_impact)}`,
    `Severity ${formatWeight(weights.severity)}`,
    `Recency ${formatWeight(weights.recency)}`,
    `Revenue ${formatWeight(weights.revenue_risk)}`,
  ].join(" · ");
  const documentsSummary = getDocumentsSummary(documents);

  // Build status line: "{autonomy} · {freshness} · {next run}"
  const freshness = formatFreshness(pmStatus.last_run_at);
  const nextRun = formatNextRun(pmStatus.next_run_in);
  const statusParts = [autonomyLabel, freshness];
  if (nextRun) statusParts.push(nextRun);
  const statusLine = statusParts.join(" \u00b7 ");

  // Determine hero mode (setup is now handled by /onboarding redirect)
  let heroMode: AutopilotHeroMode = "recommendation";
  let heroTitle = "";
  let heroBody = latestPlan?.analysis?.trim()
    || "Autopilot will summarize what matters most after the next analysis.";
  let primaryActionLabel = "Run analysis";

  if (pmStatus.last_error || pmStatus.last_run_status === "failed") {
    heroMode = "attention";
    heroTitle = "Attention needed";
    heroBody = truncateError(pmStatus.last_error) || "The last analysis failed. Review setup or rerun the PM agent.";
    primaryActionLabel = "Run analysis";
  } else if (!latestPlan) {
    heroMode = "first_analysis";
    heroTitle = "Ready for your first analysis";
    heroBody = "Autopilot will review your open issues, group related ones together, and tell you what\u2019s highest leverage to work on.";
    primaryActionLabel = "Run first analysis";
  } else {
    // Extract headline from the first sentence of the analysis.
    // Negative lookbehind avoids splitting on abbreviations like "e.g." or "Dr.".
    // Requires at least 2 word-characters before a period to skip single-letter abbrevs.
    const analysis = latestPlan.analysis?.trim() || "";
    const firstSentenceEnd = analysis.search(/(?<=\w{2})[.!?]\s/);
    if (firstSentenceEnd > 0 && firstSentenceEnd < 120) {
      heroTitle = analysis.slice(0, firstSentenceEnd + 1);
      heroBody = analysis.slice(firstSentenceEnd + 1).trim()
        || "Review the analysis details above for more context.";
    } else {
      heroTitle = analysis.length > 80
        ? analysis.slice(0, 80).trim() + "\u2026"
        : analysis || "Analysis complete";
      heroBody = analysis.length > 80 ? analysis : "";
    }
  }

  // Evidence: 3 metrics, hidden when all are zero
  const successRate = Math.round(pmStatus.success_rate ?? 0);
  const issuesReviewed = pmStatus.issues_reviewed ?? latestPlan?.issues_reviewed ?? 0;
  const totalDelegated = pmStatus.total_delegated ?? 0;
  const hasEvidence = successRate > 0 || issuesReviewed > 0 || totalDelegated > 0;

  const evidence: Array<{ label: string; value: string }> = [
    { label: "Success rate", value: `${successRate}%` },
    { label: "Issues reviewed", value: String(issuesReviewed) },
    { label: "Delegated", value: String(totalDelegated) },
  ];

  return {
    heroMode,
    heroTitle,
    heroBody,
    primaryActionLabel,
    autonomyLabel,
    directionSummary,
    focusAreas,
    weightsSummary,
    documentsSummary,
    evidence,
    hasEvidence,
    statusLine,
  };
}
