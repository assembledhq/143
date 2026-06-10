"use client";

import { useEffect, useState } from "react";
import Link from "next/link";
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
import { CodexDeviceCodeModal } from "@/components/codex-device-code-modal";
import { useDisconnectIntegration } from "@/hooks/use-disconnect-integration";
import { useGitHubRepoSync } from "@/hooks/use-github-repo-sync";
import { queryKeys } from "@/lib/query-keys";
import { AGENTS_BY_KEY, isAgentAvailable } from "@/lib/agents";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import type { CodingCredentialSummary, ListResponse, OrgSettings } from "@/lib/types";

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
              ? "bg-success/10 text-success ring-success/20"
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
      description: "Sourcegraph Amp uses agent modes (smart/deep/large/rush) and stores auth in the shared coding-agent credential stack.",
      configureLabel: "Configure",
      ctaLabel: "Configure",
    },
    pi: {
      description: "Pi uses its own API key and lets you choose the provider/model pair it should target by default.",
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
  const [selectedAgentTypeOverride, setSelectedAgentType] = useState<AgentType | null>(null);

  const { data: codexAuthResponse } = useQuery({
    queryKey: [...queryKeys.codexAuth.status, "personal"],
    queryFn: () => api.codexAuth.status(undefined, "personal"),
  });
  const { data: settingsResponse } = useQuery({
    queryKey: queryKeys.settings.all,
    queryFn: () => api.settings.get(),
  });
  const { data: resolvedCredsResponse } = useQuery({
    queryKey: queryKeys.credentials.resolved,
    queryFn: () => api.userCredentials.listResolved(),
  });
  const { data: resolvedCodingCredentialsResponse } = useQuery<ListResponse<CodingCredentialSummary>>({
    queryKey: ["coding-credentials", "resolved"],
    queryFn: () => api.codingCredentials.list("resolved"),
  });
  const settings = settingsResponse?.data?.settings as OrgSettings | undefined;
  const resolvedCredentials = resolvedCredsResponse?.data ?? [];
  const resolvedCodingCredentials = resolvedCodingCredentialsResponse?.data ?? [];

  const selectedAgentType: AgentType = selectedAgentTypeOverride ?? settings?.default_agent_type ?? "codex";

  const isSelectedAgentConnected = isAgentAvailable(
    selectedAgentType,
    resolvedCredentials,
    codexAuthResponse?.data,
    resolvedCodingCredentials,
  );

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
            <Button size="sm" variant="outline" asChild>
              <Link href="/settings/agent">{selectedAgent.configureLabel}</Link>
            </Button>
          </div>
        </CardContent>
      </Card>

      {showDeviceCodeModal && (
        <CodexDeviceCodeModal
          scope="personal"
          onClose={() => setShowDeviceCodeModal(false)}
          onConnected={() => {
            setShowDeviceCodeModal(false);
            queryClient.invalidateQueries({ queryKey: queryKeys.codexAuth.status });
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
  const githubRepos = repositories.map((r) => ({
    id: r.id,
    full_name: r.full_name,
    status: r.status,
  }));
  const githubIntegration = integrations.find((integration) => integration.provider === "github" && integration.status === "active");
  const sentryIntegration = integrations.find((integration) => integration.provider === "sentry" && integration.status === "active");
  const linearIntegration = integrations.find((integration) => integration.provider === "linear" && integration.status === "active");
  const slackIntegration = integrations.find((integration) => integration.provider === "slack" && integration.status === "active");
  const notionIntegration = integrations.find((integration) => integration.provider === "notion" && integration.status === "active");
  const circleciIntegration = integrations.find((integration) => integration.provider === "circleci" && integration.status === "active");
  const mezmoIntegration = integrations.find((integration) => integration.provider === "mezmo" && integration.status === "active");

  const githubConnected = Boolean(githubIntegration);
  const githubReady = githubConnected && repositories.length > 0;

  return (
    <div id="autopilot-setup" className="space-y-6">
      <StepSection step={1} title="Coding agent" completed={agentConnected}>
        <AgentSelectionSection onConnectedChange={setAgentConnected} />
      </StepSection>

      <StepSection step={2} title="Connect integrations" completed={githubReady}>
        <div className="space-y-3">
          <SourceControlIntegrationCard
            githubConnected={githubConnected}
            githubRepos={githubRepos}
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
            circleciConnected={Boolean(circleciIntegration)}
            mezmoConnected={Boolean(mezmoIntegration)}
            linearLoading={false}
            onConnectSentry={() => api.integrations.loginSentry()}
            onConnectLinear={() => api.integrations.loginLinear()}
            onConnectSlack={() => api.integrations.loginSlack()}
            onConnectNotion={() => { /* Notion requires token input — use the Integrations page */ }}
            onConnectCircleCI={() => { /* CircleCI requires token + slug input — use the Integrations page */ }}
            onConnectMezmo={() => { /* Mezmo requires service-key input — use the Integrations page */ }}
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
