"use client";

import { useEffect, useState, useCallback } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
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
import { PageHeader } from "@/components/page-header";
import {
  AdditionalIntegrationCards,
  SourceControlIntegrationCard,
} from "@/components/integration-connection-cards";
import { AgentSettingsEditor } from "@/components/agent-settings-editor";
import { CodexDeviceCodeModal } from "@/components/codex-device-code-modal";
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

function AgentSelectionSection() {
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

  return (
    <>
      <div className="space-y-3">
        <div className="space-y-1">
          <h2 className="text-sm font-medium text-foreground">Coding agent</h2>
          <p className="text-xs text-muted-foreground">
            Start with Codex (recommended), or pick the agent you already use. You can change this later in settings.
          </p>
        </div>

        <Card className={`py-0 ${selectedAgentType === "codex" && !isCodexConnected ? "border-primary" : ""}`} data-testid="agent-card-codex">
          <CardContent className="flex items-center justify-between gap-4 py-4">
            <div className="min-w-0 flex-1">
              <div className="flex items-center gap-2 mb-2">
                <Select
                  value={selectedAgentType}
                  onValueChange={(value) => setSelectedAgentType(value as AgentType)}
                >
                  <SelectTrigger aria-label="Coding agent provider" className="w-[220px]">
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
              <p className="mt-0.5 text-sm text-muted-foreground">
                {selectedAgent.description}
              </p>
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
      </div>

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
  const queryClient = useQueryClient();

  const { data: integrationsResp } = useQuery({
    queryKey: ["integrations"],
    queryFn: () => api.integrations.list(),
  });
  const linearIntegration = integrationsResp?.data?.find(
    (integration) => integration.provider === "linear" && integration.status === "active"
  );

  const connectLinearMutation = useMutation({
    mutationFn: () => api.integrations.connectLinear(),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["integrations"] });
    },
  });

  return (
    <div className="space-y-8">
      <PageHeader
        title="Overview"
        description="Set up your coding agent and connect your tools to start fixing issues automatically."
      />

      {/* Step 1: Coding Agent — the core of the product */}
      <AgentSelectionSection />

      {/* Step 2: Source Control — needed so the agent can access repos and open PRs */}
      <div className="space-y-3">
        <div className="space-y-1">
          <h2 className="text-sm font-medium text-foreground">Source control</h2>
          <p className="text-xs text-muted-foreground">
            Connect GitHub so the agent can access your repositories and open PRs.
          </p>
        </div>
        <SourceControlIntegrationCard onConnectGitHub={() => api.auth.login()} />
      </div>

      {/* Step 3: Additional Integrations — optional, lower priority */}
      <div className="space-y-3">
        <div className="space-y-1">
          <h2 className="text-sm font-medium text-muted-foreground">Additional integrations</h2>
          <p className="text-xs text-muted-foreground">
            Optional — connect issue and error sources to feed the agent automatically.
          </p>
        </div>
        <AdditionalIntegrationCards
          linearConnected={Boolean(linearIntegration)}
          linearLoading={connectLinearMutation.isPending}
          onConnectSentry={() => api.auth.loginSentry()}
          onConnectLinear={() => connectLinearMutation.mutate()}
        />
      </div>

      <p className="text-sm text-muted-foreground">
        Once integrations are connected, 143 picks up issues, generates fixes, and opens PRs automatically.
      </p>
    </div>
  );
}
