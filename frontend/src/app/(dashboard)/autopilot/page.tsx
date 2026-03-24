"use client";

import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Zap, RefreshCw } from "lucide-react";
import { api } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { PageContainer } from "@/components/page-container";
import { ControlStrip } from "@/components/autopilot/control-strip";
import { CurrentRecommendation } from "@/components/autopilot/current-recommendation";
import { DecisionsCard } from "@/components/autopilot/decisions-card";
import { PerformanceCard } from "@/components/autopilot/performance-card";
import { RecentActivity } from "@/components/autopilot/recent-activity";
import { DirectionSection } from "@/components/autopilot/direction-section";
import { useAnalyze } from "@/hooks/use-analyze";
import {
  AdditionalIntegrationCards,
  SourceControlIntegrationCard,
} from "@/components/integration-connection-cards";
import { AgentSettingsModal } from "@/components/agent-settings-modal";
import { NoReposWarning } from "@/components/no-repos-warning";
import { useDisconnectIntegration } from "@/hooks/use-disconnect-integration";
import { useGitHubRepoSync } from "@/hooks/use-github-repo-sync";
import { queryKeys } from "@/lib/query-keys";
import type { CodexAuthStatus, Integration, OrgSettings, PMDecisionsResponse } from "@/lib/types";

interface PreOnboardingStateProps {
  agentConnected: boolean;
  selectedAgentType: OrgSettings["default_agent_type"];
  integrations: Integration[];
  repos: { id: string; full_name: string }[];
}

function PreOnboardingState({ agentConnected, selectedAgentType, integrations, repos }: PreOnboardingStateProps) {
  const queryClient = useQueryClient();
  const [showSettingsModal, setShowSettingsModal] = useState(false);

  const disconnectMutation = useDisconnectIntegration();

  const githubRepoNames = repos.map((r) => r.full_name);

  const { sync: syncRepos, isSyncing: isSyncingRepos } = useGitHubRepoSync();

  const githubIntegration = integrations.find(
    (integration) => integration.provider === "github" && integration.status === "active"
  );
  const sentryIntegration = integrations.find(
    (integration) => integration.provider === "sentry" && integration.status === "active"
  );
  const linearIntegration = integrations.find(
    (integration) => integration.provider === "linear" && integration.status === "active"
  );
  const slackIntegration = integrations.find(
    (integration) => integration.provider === "slack" && integration.status === "active"
  );
  const notionIntegration = integrations.find(
    (integration) => integration.provider === "notion" && integration.status === "active"
  );

  return (
    <div className="space-y-6">
      <div className="space-y-2">
        <div className="flex items-center gap-2">
          <Zap className="h-5 w-5 text-primary" />
          <h1 className="text-2xl font-bold tracking-tight text-foreground">Autopilot</h1>
        </div>
        <p className="text-sm text-muted-foreground/80">
          Help the PM agent get started by connecting your tools.
        </p>
      </div>

      <div className="space-y-4">
        <div className="space-y-3">
          <h2 className="text-sm font-medium text-foreground">1. Connect a coding agent</h2>
          <div className="ml-4">
            {agentConnected ? (
              <div className="flex items-center gap-2 text-sm text-emerald-700 dark:text-emerald-400">
                <span className="inline-flex h-2 w-2 rounded-full bg-emerald-500" />
                Coding agent connected
                <Button variant="outline" size="sm" onClick={() => setShowSettingsModal(true)}>
                  Settings
                </Button>
              </div>
            ) : (
              <Button size="sm" onClick={() => setShowSettingsModal(true)}>
                Configure coding agent
              </Button>
            )}
          </div>
        </div>

        <div className="space-y-3">
          <h2 className="text-sm font-medium text-foreground">2. Connect integrations</h2>
          <div className="ml-4 space-y-3">
            <SourceControlIntegrationCard
              githubConnected={Boolean(githubIntegration)}
              githubRepoNames={githubRepoNames}
              onConnectGitHub={() => api.integrations.loginGitHub()}
              onDisconnect={(provider) => disconnectMutation.mutate(provider)}
              disconnectingProvider={disconnectMutation.isPending ? disconnectMutation.variables : null}
              disconnectErrorProvider={disconnectMutation.isError ? disconnectMutation.variables ?? null : null}
              disconnectError={disconnectMutation.isError ? "Failed to disconnect." : null}
              onSyncRepos={syncRepos}
              isSyncing={isSyncingRepos}
            />
            <NoReposWarning />
            <AdditionalIntegrationCards
              sentryConnected={Boolean(sentryIntegration)}
              linearConnected={Boolean(linearIntegration)}
              slackConnected={Boolean(slackIntegration)}
              notionConnected={Boolean(notionIntegration)}
              linearLoading={false}
              onConnectSentry={() => api.auth.loginSentry()}
              onConnectLinear={() => api.integrations.loginLinear()}
              onConnectSlack={() => api.integrations.loginSlack()}
              onConnectNotion={() => window.location.assign("/integrations")}
              onDisconnect={(provider) => disconnectMutation.mutate(provider)}
              disconnectingProvider={disconnectMutation.isPending ? disconnectMutation.variables : null}
              disconnectErrorProvider={disconnectMutation.isError ? disconnectMutation.variables ?? null : null}
              disconnectError={disconnectMutation.isError ? "Failed to disconnect." : null}
            />
          </div>
        </div>
      </div>

      {showSettingsModal && (
        <AgentSettingsModal
          initialAgentType={selectedAgentType}
          onClose={() => {
            setShowSettingsModal(false);
            queryClient.invalidateQueries({ queryKey: ["settings"] });
            queryClient.invalidateQueries({ queryKey: ["codex-auth-status"] });
            queryClient.invalidateQueries({ queryKey: ["agent-defaults"] });
          }}
        />
      )}
    </div>
  );
}

export default function AutopilotPage() {
  // Data queries — shared across states
  const { data: integrationsResp } = useQuery({
    queryKey: ["integrations"],
    queryFn: () => api.integrations.list(),
  });

  const { data: settingsResp } = useQuery({
    queryKey: ["settings"],
    queryFn: () => api.settings.get(),
  });

  const { data: codexAuthResp } = useQuery({
    queryKey: ["codex-auth-status"],
    queryFn: () => api.codexAuth.status(),
  });

  const { data: agentDefaultsResp } = useQuery({
    queryKey: ["agent-defaults"],
    queryFn: () => api.settings.getAgentDefaults(),
  });

  const { data: reposResp } = useQuery({
    queryKey: queryKeys.repositories.all,
    queryFn: () => api.repositories.list(),
  });

  // Derived state — used for state detection
  const orgSettings = (settingsResp?.data?.settings ?? {}) as OrgSettings;
  const agentConfig = orgSettings.agent_config ?? {};
  const serverDefaults = agentDefaultsResp?.data ?? {};
  const selectedAgentType = orgSettings.default_agent_type ?? "codex";
  const codexAuthStatus = codexAuthResp?.data as CodexAuthStatus | undefined;

  const githubIntegration = integrationsResp?.data?.find(
    (i) => i.provider === "github" && i.status === "active"
  );
  const repos = reposResp?.data ?? [];
  const githubConnected = Boolean(githubIntegration) && repos.length > 0;

  const isCodexConnected = codexAuthStatus?.status === "completed"
    || Boolean(agentConfig.codex?.OPENAI_API_KEY)
    || Boolean(serverDefaults.codex?.OPENAI_API_KEY);
  const isClaudeConnected = Boolean(agentConfig.claude_code?.ANTHROPIC_API_KEY)
    || Boolean(serverDefaults.claude_code?.ANTHROPIC_API_KEY);
  const isGeminiConnected = Boolean(agentConfig.gemini_cli?.GEMINI_API_KEY)
    || Boolean(serverDefaults.gemini_cli?.GEMINI_API_KEY);

  const agentConnected = selectedAgentType === "codex"
    ? isCodexConnected
    : selectedAgentType === "claude_code"
      ? isClaudeConnected
      : isGeminiConnected;

  // State detection
  const isPreOnboarding = !githubConnected || !agentConnected;

  // PM queries — only fire when onboarded
  const { data: pmStatusData } = useQuery({
    queryKey: ["pm", "status"],
    queryFn: () => api.pm.status(),
    refetchInterval: 15000,
    enabled: !isPreOnboarding,
  });

  const { data: latestPlanData } = useQuery({
    queryKey: ["pm", "latest"],
    queryFn: () => api.pm.latest(),
    retry: false,
    enabled: !isPreOnboarding,
  });

  const { data: decisionsData, isLoading: decisionsLoading } = useQuery<PMDecisionsResponse>({
    queryKey: ["pm", "decisions"],
    queryFn: () => api.pm.decisions({ limit: 50 }),
    refetchInterval: 30000,
    enabled: !isPreOnboarding,
  });

  const { data: plansHistoryData } = useQuery({
    queryKey: ["pm", "plans"],
    queryFn: () => api.pm.list({ limit: 5 }),
    retry: false,
    enabled: !isPreOnboarding,
  });

  const pmStatus = pmStatusData?.data;
  const latestPlan = latestPlanData?.data;
  const hasActivePlan = latestPlan?.status === "executing";
  const decisions = decisionsData?.data ?? [];
  const decisionSummary = decisionsData?.summary;
  const plansHistory = plansHistoryData?.data ?? [];

  const { isAnalyzing, isPending, analyzeError, handleAnalyze, dismissError } = useAnalyze(hasActivePlan);

  const isPostOnboardingNoAnalysis = !isPreOnboarding && !latestPlan;

  if (isPreOnboarding) {
    return (
      <PageContainer size="default">
        <PreOnboardingState
          agentConnected={agentConnected}
          selectedAgentType={selectedAgentType}
          integrations={integrationsResp?.data ?? []}
          repos={repos}
        />
      </PageContainer>
    );
  }

  return (
    <PageContainer size="default">
      <div className="space-y-6">
        {/* Header */}
        <div className="flex items-center gap-2">
          <Zap className="h-5 w-5 text-primary" />
          <h1 className="text-2xl font-bold tracking-tight text-foreground">Autopilot</h1>
        </div>

        {/* Zone 1: Control Strip */}
        <ControlStrip
          pmStatus={pmStatus}
          isAnalyzing={isAnalyzing}
          isPending={isPending}
          onAnalyze={handleAnalyze}
          analyzeError={analyzeError}
          dismissError={dismissError}
        />

        {isPostOnboardingNoAnalysis ? (
          /* Post-onboarding, no analysis yet */
          <div className="space-y-4">
            <div className="flex flex-col items-center justify-center py-12 text-center space-y-4">
              <div className="flex h-12 w-12 items-center justify-center rounded-full bg-muted/50 dark:bg-white/5 ring-1 ring-border/50">
                <Zap className="h-6 w-6 text-muted-foreground/70" />
              </div>
              <div>
                <p className="text-sm font-semibold text-foreground">Ready to analyze</p>
                <p className="mt-1.5 max-w-xs text-center text-[13px] text-muted-foreground/80">
                  Run your first analysis and the PM agent will tell you which issues matter most.
                </p>
              </div>
              <Button onClick={handleAnalyze} disabled={isPending || isAnalyzing}>
                <RefreshCw className={`mr-2 h-4 w-4 ${isPending || isAnalyzing ? "animate-spin" : ""}`} />
                {isPending ? "Starting..." : isAnalyzing ? "Analyzing..." : "Run First Analysis"}
              </Button>
            </div>

            {/* Still show direction section even before first analysis */}
            <div className="relative">
              <div className="flex items-center gap-3 py-4">
                <div className="h-px flex-1 bg-border" />
                <span className="text-[11px] font-medium text-muted-foreground uppercase tracking-wider">Your Direction</span>
                <div className="h-px flex-1 bg-border" />
              </div>
              <DirectionSection />
            </div>
          </div>
        ) : (
          /* Full workspace */
          <>
            {/* Zone 2: Current Recommendation */}
            <CurrentRecommendation plan={latestPlan} />

            {/* Zone 3: Decisions + Performance side by side */}
            <div className="grid gap-4 md:grid-cols-2">
              <DecisionsCard
                decisions={decisions}
                isLoading={decisionsLoading}
              />
              <PerformanceCard summary={decisionSummary} />
            </div>

            {/* Zone 4: Recent Activity */}
            <RecentActivity plans={plansHistory} summary={decisionSummary} />

            {/* Divider */}
            <div className="flex items-center gap-3 py-2">
              <div className="h-px flex-1 bg-border" />
              <span className="text-[11px] font-medium text-muted-foreground uppercase tracking-wider">Your Direction</span>
              <div className="h-px flex-1 bg-border" />
            </div>

            {/* Zone 5: Direction */}
            <DirectionSection />
          </>
        )}
      </div>
    </PageContainer>
  );
}
