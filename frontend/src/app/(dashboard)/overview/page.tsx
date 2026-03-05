"use client";

import { useEffect, useState, useCallback } from "react";
import { useQuery } from "@tanstack/react-query";
import { Bot, Check } from "lucide-react";
import { api } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent } from "@/components/ui/card";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  AdditionalIntegrationCards,
  SourceControlIntegrationCard,
} from "@/components/integration-connection-cards";
import { AgentSettingsEditor } from "@/components/agent-settings-editor";
import { CodexDeviceCodeModal } from "@/components/codex-device-code-modal";
import { PageContainer } from "@/components/page-container";
import type { CodexAuthStatus, OrgSettings } from "@/lib/types";

function AgentSettingsModal({ onClose, initialAgentType }: { onClose: () => void; initialAgentType?: OrgSettings["default_agent_type"] }) {
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50">
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
          className={`flex h-7 w-7 shrink-0 items-center justify-center rounded-full text-xs font-semibold ${
            completed
              ? "bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-400"
              : "bg-muted text-muted-foreground"
          }`}
        >
          {completed ? <Check className="h-4 w-4" /> : step}
        </div>
        <h2 className="text-sm font-medium text-foreground">{title}</h2>
      </div>
      <div className="ml-10">{children}</div>
    </div>
  );
}

function AgentSelectionSection({ onConnectedChange }: { onConnectedChange?: (connected: boolean) => void }) {
  type AgentType = NonNullable<OrgSettings["default_agent_type"]>;

  const AGENT_OPTIONS: Array<{
    value: AgentType;
    label: string;
    description: string;
    configureLabel: string;
    ctaLabel: string;
  }> = [
    {
      value: "codex",
      label: "Codex",
      description: "Sign in with ChatGPT for instant access to gpt-5.3-codex. No API key needed.",
      configureLabel: "Settings",
      ctaLabel: "Sign in with ChatGPT",
    },
    {
      value: "claude_code",
      label: "Claude Code",
      description: "Use your Anthropic API key for Claude-powered fixes.",
      configureLabel: "Configure",
      ctaLabel: "Configure",
    },
    {
      value: "gemini_cli",
      label: "Gemini CLI",
      description: "Use your Google Gemini API key for Gemini-powered fixes.",
      configureLabel: "Configure",
      ctaLabel: "Configure",
    },
  ];

  const [codexAuthStatus, setCodexAuthStatus] = useState<CodexAuthStatus | null>(null);
  const [agentConfig, setAgentConfig] = useState<Record<string, Record<string, string>>>({});
  const [agentDefaults, setAgentDefaults] = useState<Record<string, Record<string, string>>>({});
  const [showDeviceCodeModal, setShowDeviceCodeModal] = useState(false);
  const [showSettingsModal, setShowSettingsModal] = useState(false);
  const [settingsAgentType, setSettingsAgentType] = useState<OrgSettings["default_agent_type"]>("codex");
  const [selectedAgentType, setSelectedAgentType] = useState<AgentType>("codex");

  const fetchData = useCallback(() => {
    api.codexAuth.status().then((res) => setCodexAuthStatus(res.data)).catch(() => {});
    api.settings.get().then((res) => {
      const settings = res.data?.settings as OrgSettings | undefined;
      setAgentConfig(settings?.agent_config ?? {});
      setSelectedAgentType(settings?.default_agent_type ?? "codex");
    }).catch(() => {});
    api.settings.getAgentDefaults().then((res) => {
      setAgentDefaults(res.data ?? {});
    }).catch(() => {});
  }, []);

  useEffect(() => { fetchData(); }, [fetchData]);

  const isCodexConnected = codexAuthStatus?.status === "completed"
    || Boolean(agentConfig.codex?.OPENAI_API_KEY)
    || Boolean(agentDefaults.codex?.OPENAI_API_KEY);

  const isClaudeConnected = Boolean(agentConfig.claude_code?.ANTHROPIC_API_KEY)
    || Boolean(agentDefaults.claude_code?.ANTHROPIC_API_KEY);

  const isGeminiConnected = Boolean(agentConfig.gemini_cli?.GEMINI_API_KEY)
    || Boolean(agentDefaults.gemini_cli?.GEMINI_API_KEY);

  const isSelectedAgentConnected = selectedAgentType === "codex"
    ? isCodexConnected
    : selectedAgentType === "claude_code"
      ? isClaudeConnected
      : isGeminiConnected;

  const selectedAgent = AGENT_OPTIONS.find((agent) => agent.value === selectedAgentType) ?? AGENT_OPTIONS[0];

  // Notify parent of connection state changes
  useEffect(() => {
    onConnectedChange?.(isSelectedAgentConnected);
  }, [isSelectedAgentConnected, onConnectedChange]);

  return (
    <>
      <Card className={`py-0 ${selectedAgentType === "codex" && !isCodexConnected ? "border-primary" : ""}`} data-testid="agent-card-codex">
        <CardContent className="flex items-center justify-between gap-4 py-4">
          <div className="flex items-center gap-3 min-w-0 flex-1">
            <div className="flex shrink-0 items-center justify-center h-9 w-9 rounded-lg bg-muted text-muted-foreground">
              <Bot className="h-5 w-5" />
            </div>
            <div className="min-w-0 flex-1">
              <div className="flex items-center gap-2 mb-1">
                <Select
                  value={selectedAgentType}
                  onValueChange={(value) => setSelectedAgentType(value as AgentType)}
                >
                  <SelectTrigger aria-label="Coding agent provider" className="w-[180px] h-8">
                    <SelectValue placeholder="Select coding agent" />
                  </SelectTrigger>
                  <SelectContent>
                    {AGENT_OPTIONS.map((agent) => (
                      <SelectItem key={agent.value} value={agent.value}>
                        {agent.label}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
              <p className="text-sm text-muted-foreground">
                {selectedAgent.description}
              </p>
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
          onConnected={() => { setShowDeviceCodeModal(false); fetchData(); }}
        />
      )}
      {showSettingsModal && (
        <AgentSettingsModal
          initialAgentType={settingsAgentType}
          onClose={() => { setShowSettingsModal(false); fetchData(); }}
        />
      )}
    </>
  );
}

export default function Overview() {
  const [agentConnected, setAgentConnected] = useState(false);

  const { data: integrationsResp } = useQuery({
    queryKey: ["integrations"],
    queryFn: () => api.integrations.list(),
  });

  const githubIntegration = integrationsResp?.data?.find(
    (integration) => integration.provider === "github" && integration.status === "active"
  );
  const sentryIntegration = integrationsResp?.data?.find(
    (integration) => integration.provider === "sentry" && integration.status === "active"
  );
  const linearIntegration = integrationsResp?.data?.find(
    (integration) => integration.provider === "linear" && integration.status === "active"
  );

  // Count connected steps (agent + GitHub required; sentry/linear optional but counted)
  const connectedCount =
    (agentConnected ? 1 : 0) +
    (githubIntegration ? 1 : 0) +
    (sentryIntegration ? 1 : 0) +
    (linearIntegration ? 1 : 0);
  const totalCount = 4;
  const allRequiredConnected = agentConnected && Boolean(githubIntegration);

  return (
    <PageContainer size="default">
      <div className="space-y-6">
      {/* Hero header */}
      <div className="space-y-3">
        <div>
          <h1 className="text-lg font-semibold text-foreground">Get started</h1>
          <p className="text-sm text-muted-foreground">
            Connect your tools and start fixing issues automatically.
          </p>
        </div>
        <div className="space-y-1.5">
          <div className="flex items-center justify-between">
            <p className="text-xs text-muted-foreground">
              {connectedCount} of {totalCount} connected
            </p>
          </div>
          <div className="h-1.5 w-full rounded-full bg-muted overflow-hidden">
            <div
              className="h-full rounded-full bg-primary transition-all duration-500"
              style={{ width: `${(connectedCount / totalCount) * 100}%` }}
            />
          </div>
        </div>
      </div>

      {/* Step 1: Coding Agent */}
      <StepSection step={1} title="Coding agent" completed={agentConnected}>
        <AgentSelectionSection onConnectedChange={setAgentConnected} />
      </StepSection>

      {/* Step 2: Connect Integrations (consolidated) */}
      <StepSection step={2} title="Connect integrations" completed={Boolean(githubIntegration)}>
        <div className="space-y-3">
          <SourceControlIntegrationCard onConnectGitHub={() => api.auth.login()} />
          <AdditionalIntegrationCards
            linearConnected={Boolean(linearIntegration)}
            linearLoading={false}
            onConnectSentry={() => api.auth.loginSentry()}
            onConnectLinear={() => api.integrations.loginLinear()}
          />
        </div>
      </StepSection>

      {/* Success banner when all required steps are done */}
      {allRequiredConnected && (
        <div className="flex items-center gap-3 rounded-lg border border-green-200 bg-green-50 px-4 py-3 dark:border-green-800 dark:bg-green-950/30">
          <div className="flex h-6 w-6 shrink-0 items-center justify-center rounded-full bg-green-100 text-green-700 dark:bg-green-900/50 dark:text-green-400">
            <Check className="h-3.5 w-3.5" />
          </div>
          <p className="text-sm text-green-800 dark:text-green-300">
            You&apos;re all set! 143 will pick up issues and open PRs automatically.
          </p>
        </div>
      )}
      </div>
    </PageContainer>
  );
}
