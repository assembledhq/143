"use client";

import { useEffect, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Bot, Check } from "lucide-react";
import { api } from "@/lib/api";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { NoReposWarning } from "@/components/no-repos-warning";
import {
  AdditionalIntegrationCards,
  SourceControlIntegrationCard,
} from "@/components/integration-connection-cards";
import { AgentSettingsEditor } from "@/components/agent-settings-editor";
import { CodexDeviceCodeModal } from "@/components/codex-device-code-modal";
import { useDisconnectIntegration } from "@/hooks/use-disconnect-integration";
import { useGitHubRepoSync } from "@/hooks/use-github-repo-sync";
import { queryKeys } from "@/lib/query-keys";
import { isAgentConnected } from "@/components/autopilot/autopilot-helpers";
import { AGENTS_BY_KEY } from "@/lib/agents";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import type { OrgSettings } from "@/lib/types";

function AgentSettingsModal({ onClose, initialAgentType }: { onClose: () => void; initialAgentType?: OrgSettings["default_agent_type"] }) {
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 backdrop-blur-sm">
      <div className="w-full max-w-2xl rounded-lg border bg-background p-6 shadow-lg">
        <AgentSettingsEditor
          title="Configure coding agent"
          description="Set your default agent and configure credentials."
          initialAgentType={initialAgentType}
          setupMode
          onClose={onClose}
        />
      </div>
    </div>
  );
}

function StepSection({
  step,
  title,
  completed,
  children,
}: {
  step: number;
  title: string;
  completed: boolean;
  children: React.ReactNode;
}) {
  return (
    <div className="space-y-3">
      <div className="flex items-center gap-3">
        <div
          className={`flex h-8 w-8 shrink-0 items-center justify-center rounded-full text-xs font-semibold ring-1 ${
            completed
              ? "bg-emerald-500/10 text-emerald-700 ring-emerald-500/20 dark:text-emerald-400"
              : "bg-muted text-muted-foreground ring-border/50"
          }`}
        >
          {completed ? <Check className="h-4 w-4" /> : step}
        </div>
        <h2 className="text-sm font-medium text-foreground">{title}</h2>
      </div>
      <div className="ml-11">{children}</div>
    </div>
  );
}

function AgentSelectionSection({ onConnectedChange }: { onConnectedChange?: (connected: boolean) => void }) {
  type AgentType = NonNullable<OrgSettings["default_agent_type"]>;

  // Labels come from the shared AGENTS registry; the checklist layers on
  // setup-specific copy (Codex has a dedicated sign-in CTA, the rest reuse
  // the default "Configure" flow) and fine-tunes the description for the
  // checklist context.
  const checklistOverrides: Record<AgentType, { description: string; configureLabel: string; ctaLabel: string }> = {
    codex: {
      description: "Sign in with ChatGPT for instant access to gpt-5.3-codex. No API key needed.",
      configureLabel: "Settings",
      ctaLabel: "Sign in with ChatGPT",
    },
    claude_code: {
      description: "Your own Anthropic API key is required for agent sessions. Platform keys are used for internal features only.",
      configureLabel: "Configure",
      ctaLabel: "Configure",
    },
    gemini_cli: {
      description: "Your own Google Gemini API key is required for agent sessions. Platform keys are used for internal features only.",
      configureLabel: "Configure",
      ctaLabel: "Configure",
    },
    amp: {
      description: "Sourcegraph Amp uses agent modes (smart/deep/large/rush). Requires an AMP_API_KEY.",
      configureLabel: "Configure",
      ctaLabel: "Configure",
    },
    pi: {
      description: "Pi routes to many providers via one CLI. Reuses your other configured agent keys by default.",
      configureLabel: "Configure",
      ctaLabel: "Configure",
    },
  };

  const agentOptions: Array<{
    value: AgentType;
    label: string;
    description: string;
    configureLabel: string;
    ctaLabel: string;
  }> = (Object.keys(checklistOverrides) as AgentType[]).map((value) => ({
    value,
    label: AGENTS_BY_KEY[value]?.label ?? value,
    ...checklistOverrides[value],
  }));

  const queryClient = useQueryClient();
  const [showDeviceCodeModal, setShowDeviceCodeModal] = useState(false);
  const [showSettingsModal, setShowSettingsModal] = useState(false);
  const [settingsAgentType, setSettingsAgentType] = useState<OrgSettings["default_agent_type"]>("codex");
  const [selectedAgentTypeOverride, setSelectedAgentType] = useState<AgentType | null>(null);

  const { data: codexAuthResponse } = useQuery({
    queryKey: queryKeys.codexAuth.status,
    queryFn: () => api.codexAuth.status(),
  });
  const { data: settingsResponse } = useQuery({
    queryKey: queryKeys.settings.all,
    queryFn: () => api.settings.get(),
  });
  const settings = settingsResponse?.data?.settings as OrgSettings | undefined;
  const agentConfig = settings?.agent_config ?? {};

  const selectedAgentType: AgentType = selectedAgentTypeOverride ?? settings?.default_agent_type ?? "codex";

  const isSelectedAgentConnected = isAgentConnected(selectedAgentType, agentConfig, codexAuthResponse?.data);

  const selectedAgent = agentOptions.find((agent) => agent.value === selectedAgentType) ?? agentOptions[0];

  useEffect(() => {
    onConnectedChange?.(isSelectedAgentConnected);
  }, [isSelectedAgentConnected, onConnectedChange]);

  return (
    <>
      <Card className={`py-0 ${selectedAgentType === "codex" && !isSelectedAgentConnected ? "border-primary" : ""}`}>
        <CardContent className="flex items-center justify-between gap-4 py-4">
          <div className="flex min-w-0 flex-1 items-center gap-3">
            <div className="flex h-9 w-9 shrink-0 items-center justify-center rounded-lg bg-muted/50 text-muted-foreground ring-1 ring-border/50 dark:bg-white/5">
              <Bot className="h-5 w-5" />
            </div>
            <div className="min-w-0 flex-1">
              <div className="mb-1 flex items-center gap-2">
                <Select value={selectedAgentType} onValueChange={(value) => setSelectedAgentType(value as AgentType)}>
                  <SelectTrigger aria-label="Coding agent provider" className="h-8 w-[180px]">
                    <SelectValue placeholder="Select coding agent" />
                  </SelectTrigger>
                  <SelectContent>
                    {agentOptions.map((agent) => (
                      <SelectItem key={agent.value} value={agent.value}>
                        {agent.label}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
              <p className="text-xs text-muted-foreground">{selectedAgent.description}</p>
            </div>
          </div>
          <div className="flex shrink-0 gap-2">
            {isSelectedAgentConnected && <Badge variant="secondary">Connected</Badge>}
            {selectedAgentType === "codex" && !isSelectedAgentConnected && (
              <Button size="sm" onClick={() => setShowDeviceCodeModal(true)}>
                {selectedAgent.ctaLabel}
              </Button>
            )}
            <Button
              size="sm"
              variant="outline"
              onClick={() => {
                setSettingsAgentType(selectedAgentType);
                setShowSettingsModal(true);
              }}
            >
              {selectedAgent.configureLabel}
            </Button>
          </div>
        </CardContent>
      </Card>

      {showDeviceCodeModal && (
        <CodexDeviceCodeModal
          onClose={() => setShowDeviceCodeModal(false)}
          onConnected={() => {
            setShowDeviceCodeModal(false);
            queryClient.invalidateQueries({ queryKey: queryKeys.codexAuth.status });
          }}
        />
      )}
      {showSettingsModal && (
        <AgentSettingsModal
          initialAgentType={settingsAgentType}
          onClose={() => {
            setShowSettingsModal(false);
            queryClient.invalidateQueries({ queryKey: queryKeys.settings.all });
          }}
        />
      )}
    </>
  );
}

export function SetupChecklist() {
  const [agentConnected, setAgentConnected] = useState(false);
  const { data: integrationsResponse } = useQuery({
    queryKey: queryKeys.integrations.all,
    queryFn: () => api.integrations.list(),
  });
  const { data: repositoriesResponse } = useQuery({
    queryKey: queryKeys.repositories.all,
    queryFn: () => api.repositories.list(),
  });
  const disconnectMutation = useDisconnectIntegration();
  const { sync: syncRepos, isSyncing: isSyncingRepos } = useGitHubRepoSync();

  const integrations = integrationsResponse?.data ?? [];
  const repositories = repositoriesResponse?.data ?? [];
  const githubRepoNames = repositories.map((repository) => repository.full_name);
  const githubIntegration = integrations.find((integration) => integration.provider === "github" && integration.status === "active");
  const sentryIntegration = integrations.find((integration) => integration.provider === "sentry" && integration.status === "active");
  const linearIntegration = integrations.find((integration) => integration.provider === "linear" && integration.status === "active");
  const slackIntegration = integrations.find((integration) => integration.provider === "slack" && integration.status === "active");
  const notionIntegration = integrations.find((integration) => integration.provider === "notion" && integration.status === "active");

  const githubReady = Boolean(githubIntegration) && repositories.length > 0;

  return (
    <div id="autopilot-setup" className="space-y-6">
      <StepSection step={1} title="Coding agent" completed={agentConnected}>
        <AgentSelectionSection onConnectedChange={setAgentConnected} />
      </StepSection>

      <StepSection step={2} title="Connect integrations" completed={githubReady}>
        <div className="space-y-3">
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
            onConnectNotion={() => { /* Notion requires token input — use the Integrations page */ }}
            onDisconnect={(provider) => disconnectMutation.mutate(provider)}
            disconnectingProvider={disconnectMutation.isPending ? disconnectMutation.variables : null}
            disconnectErrorProvider={disconnectMutation.isError ? disconnectMutation.variables ?? null : null}
            disconnectError={disconnectMutation.isError ? "Failed to disconnect." : null}
          />
        </div>
      </StepSection>
    </div>
  );
}
