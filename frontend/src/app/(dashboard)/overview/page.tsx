"use client";

import { useEffect, useState, useCallback, useRef } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent } from "@/components/ui/card";
import { PageHeader } from "@/components/page-header";
import { IntegrationsCard } from "@/components/integrations-card";
import { AgentSettingsEditor } from "@/components/agent-settings-editor";
import { INTEGRATIONS } from "@/lib/integrations";
import type { CodexAuthStatus, CodexDeviceAuth, OrgSettings } from "@/lib/types";

function OverviewDeviceCodeModal({ onClose, onConnected }: { onClose: () => void; onConnected: () => void }) {
  const [deviceAuth, setDeviceAuth] = useState<CodexDeviceAuth | null>(null);
  const [status, setStatus] = useState<string>("initiating");
  const [error, setError] = useState<string>("");
  const [timeLeft, setTimeLeft] = useState(0);
  const pollRef = useRef<NodeJS.Timeout | null>(null);
  const timerRef = useRef<NodeJS.Timeout | null>(null);
  const onConnectedRef = useRef(onConnected);

  useEffect(() => { onConnectedRef.current = onConnected; }, [onConnected]);

  const startAuth = useCallback(async () => {
    try {
      setStatus("initiating");
      setError("");
      const resp = await api.codexAuth.initiate();
      setDeviceAuth(resp.data);
      setTimeLeft(resp.data.expires_in);
      setStatus("pending");
    } catch {
      setError("Failed to start authentication. Please try again.");
      setStatus("error");
    }
  }, []);

  useEffect(() => { const id = setTimeout(() => { void startAuth(); }, 0); return () => clearTimeout(id); }, [startAuth]);

  useEffect(() => {
    if (status !== "pending") return;
    pollRef.current = setInterval(async () => {
      try {
        const resp = await api.codexAuth.status();
        if (resp.data.status === "completed") {
          setStatus("completed");
          if (pollRef.current) clearInterval(pollRef.current);
          if (timerRef.current) clearInterval(timerRef.current);
          setTimeout(() => { onConnectedRef.current(); }, 1500);
        } else if (resp.data.status === "expired") {
          setStatus("expired");
          setError("Code expired. Please try again.");
          if (pollRef.current) clearInterval(pollRef.current);
          if (timerRef.current) clearInterval(timerRef.current);
        } else if (resp.data.status === "error") {
          setStatus("error");
          setError(resp.data.message || "Authentication failed.");
          if (pollRef.current) clearInterval(pollRef.current);
          if (timerRef.current) clearInterval(timerRef.current);
        }
      } catch { /* ignore transient errors */ }
    }, 3000);
    timerRef.current = setInterval(() => { setTimeLeft((t) => Math.max(0, t - 1)); }, 1000);
    return () => { if (pollRef.current) clearInterval(pollRef.current); if (timerRef.current) clearInterval(timerRef.current); };
  }, [status]);

  const minutes = Math.floor(timeLeft / 60);
  const seconds = timeLeft % 60;

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50">
      <div className="w-full max-w-md rounded-lg border bg-background p-6 shadow-lg">
        <h3 className="text-lg font-medium">Connect your ChatGPT account</h3>
        {status === "initiating" && <p className="mt-4 text-sm text-muted-foreground">Starting authentication...</p>}
        {status === "pending" && deviceAuth && (
          <div className="mt-4 space-y-4">
            <div className="space-y-2">
              <p className="text-sm text-muted-foreground">1. Open this link:</p>
              <a href={deviceAuth.verification_uri} target="_blank" rel="noopener noreferrer" className="text-sm font-medium text-primary underline">{deviceAuth.verification_uri}</a>
            </div>
            <div className="space-y-2">
              <p className="text-sm text-muted-foreground">2. Enter this code:</p>
              <div className="flex items-center gap-2">
                <code className="rounded-md border bg-muted px-4 py-2 text-2xl font-mono font-bold tracking-widest">{deviceAuth.user_code}</code>
                <Button size="sm" variant="outline" onClick={() => navigator.clipboard.writeText(deviceAuth.user_code)}>Copy</Button>
              </div>
            </div>
            <div className="space-y-2">
              <p className="text-sm text-muted-foreground">Waiting for authentication...</p>
              <div className="h-1.5 w-full rounded-full bg-muted overflow-hidden">
                <div className="h-full rounded-full bg-primary transition-all duration-1000" style={{ width: `${Math.max(0, (timeLeft / deviceAuth.expires_in) * 100)}%` }} />
              </div>
              <p className="text-xs text-muted-foreground">Expires in {minutes}:{seconds.toString().padStart(2, "0")}</p>
            </div>
          </div>
        )}
        {status === "completed" && <div className="mt-4"><p className="text-sm font-medium text-green-600">Connected successfully!</p></div>}
        {(status === "error" || status === "expired") && (
          <div className="mt-4">
            <p className="text-sm text-destructive">{error}</p>
          </div>
        )}
        <div className="mt-6 flex items-center justify-end gap-2">
          <Button variant="outline" size="sm" onClick={onClose}>{status === "completed" ? "Done" : "Cancel"}</Button>
          {(status === "error" || status === "expired") && (
            <Button size="sm" onClick={startAuth}>Try Again</Button>
          )}
        </div>
      </div>
    </div>
  );
}

function AgentSettingsModal({ onClose, initialAgentType }: { onClose: () => void; initialAgentType?: OrgSettings["default_agent_type"] }) {
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50">
      <div className="w-full max-w-2xl rounded-lg border bg-background p-6 shadow-lg">
        <AgentSettingsEditor
          title="Configure coding agent"
          description="Set your default agent and configure credentials."
          initialAgentType={initialAgentType}
          onClose={onClose}
        />
      </div>
    </div>
  );
}

function AgentSelectionSection() {
  const [codexAuthStatus, setCodexAuthStatus] = useState<CodexAuthStatus | null>(null);
  const [agentConfig, setAgentConfig] = useState<Record<string, Record<string, string>>>({});
  const [agentDefaults, setAgentDefaults] = useState<Record<string, Record<string, string>>>({});
  const [showDeviceCodeModal, setShowDeviceCodeModal] = useState(false);
  const [showSettingsModal, setShowSettingsModal] = useState(false);
  const [settingsAgentType, setSettingsAgentType] = useState<OrgSettings["default_agent_type"]>("codex");

  const fetchData = useCallback(() => {
    api.codexAuth.status().then((res) => setCodexAuthStatus(res.data)).catch(() => {});
    api.settings.get().then((res) => {
      const settings = res.data?.settings as OrgSettings | undefined;
      setAgentConfig(settings?.agent_config ?? {});
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

  return (
    <>
      <div className="space-y-3">
        <div className="space-y-1">
          <h2 className="text-sm font-medium text-foreground">Coding agent</h2>
          <p className="text-xs text-muted-foreground">
            Choose the agent that fixes your issues. You can change this later in settings.
          </p>
        </div>

        {/* Featured: Codex (Recommended) */}
        <Card className={`py-0 ${!isCodexConnected ? "border-primary" : ""}`} data-testid="agent-card-codex">
          <CardContent className="flex items-center justify-between gap-4 py-4">
            <div className="min-w-0 flex-1">
              <div className="flex items-center gap-2">
                <p className="text-sm font-medium text-foreground">Codex</p>
                <Badge variant="secondary" className="text-xs">Recommended</Badge>
              </div>
              <p className="mt-0.5 text-sm text-muted-foreground">
                Sign in with ChatGPT for instant access to gpt-5.3-codex. No API key needed.
              </p>
            </div>
            <div className="flex shrink-0 gap-2">
              {isCodexConnected ? (
                <Badge variant="secondary">Connected</Badge>
              ) : (
                <>
                  <Button size="sm" onClick={() => setShowDeviceCodeModal(true)}>
                    Sign in with ChatGPT
                  </Button>
                  <Button
                    size="sm"
                    variant="outline"
                    onClick={() => {
                      setSettingsAgentType("codex");
                      setShowSettingsModal(true);
                    }}
                  >
                    Settings
                  </Button>
                </>
              )}
            </div>
          </CardContent>
        </Card>

        {/* Secondary agents: Claude Code + Gemini CLI */}
        <div className="grid gap-3 sm:grid-cols-2">
          <Card className="py-0" data-testid="agent-card-claude">
            <CardContent className="flex items-center justify-between gap-4 py-4">
              <div className="min-w-0 flex-1">
                <p className="text-sm font-medium text-foreground">Claude Code</p>
                <p className="mt-0.5 text-sm text-muted-foreground">
                  Use your Anthropic API key for Claude-powered fixes.
                </p>
              </div>
              <div className="shrink-0">
                {isClaudeConnected ? (
                  <Badge variant="secondary">Connected</Badge>
                ) : (
                  <Button
                    size="sm"
                    variant="outline"
                    onClick={() => {
                      setSettingsAgentType("claude_code");
                      setShowSettingsModal(true);
                    }}
                  >
                    Configure
                  </Button>
                )}
              </div>
            </CardContent>
          </Card>

          <Card className="py-0" data-testid="agent-card-gemini">
            <CardContent className="flex items-center justify-between gap-4 py-4">
              <div className="min-w-0 flex-1">
                <p className="text-sm font-medium text-foreground">Gemini CLI</p>
                <p className="mt-0.5 text-sm text-muted-foreground">
                  Use your Google Gemini API key for Gemini-powered fixes.
                </p>
              </div>
              <div className="shrink-0">
                {isGeminiConnected ? (
                  <Badge variant="secondary">Connected</Badge>
                ) : (
                  <Button
                    size="sm"
                    variant="outline"
                    onClick={() => {
                      setSettingsAgentType("gemini_cli");
                      setShowSettingsModal(true);
                    }}
                  >
                    Configure
                  </Button>
                )}
              </div>
            </CardContent>
          </Card>
        </div>
      </div>

      {showDeviceCodeModal && (
        <OverviewDeviceCodeModal
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
  const [github, sentry, linear] = INTEGRATIONS;
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
        <IntegrationsCard
          items={[
            {
              id: github.key,
              title: `Connect ${github.name}`,
              description: github.description,
              action: (
                <Button size="sm" onClick={() => api.auth.login()} aria-label="Connect GitHub">
                  Connect
                </Button>
              ),
            },
          ]}
        />
      </div>

      {/* Step 3: Additional Integrations — optional, lower priority */}
      <div className="space-y-3">
        <div className="space-y-1">
          <h2 className="text-sm font-medium text-muted-foreground">Additional integrations</h2>
          <p className="text-xs text-muted-foreground">
            Optional — connect issue and error sources to feed the agent automatically.
          </p>
        </div>
        <IntegrationsCard
          items={[
            {
              id: sentry.key,
              title: `Connect ${sentry.name}`,
              description: sentry.description,
              action: (
                <Button
                  size="sm"
                  variant="outline"
                  onClick={() => api.auth.loginSentry()}
                  aria-label="Connect Sentry"
                >
                  Connect
                </Button>
              ),
            },
            {
              id: linear.key,
              title: `Connect ${linear.name}`,
              description: linear.description,
              action: (
                <Button
                  size="sm"
                  variant="outline"
                  aria-label={linearIntegration ? "Linear Connected" : "Connect Linear"}
                  loading={connectLinearMutation.isPending}
                  disabled={Boolean(linearIntegration) || connectLinearMutation.isPending}
                  onClick={() => connectLinearMutation.mutate()}
                >
                  {linearIntegration ? "Connected" : "Connect"}
                </Button>
              ),
            },
          ]}
        />
      </div>

      <p className="text-sm text-muted-foreground">
        Once integrations are connected, 143 picks up issues, generates fixes, and opens PRs automatically.
      </p>
    </div>
  );
}
