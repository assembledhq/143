"use client";

import { useMemo, useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { CheckCircle2, KeyRound, Sparkles, Check, Eye, EyeOff, Shield } from "lucide-react";
import { api } from "@/lib/api";
import { useAuth } from "@/hooks/use-auth";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { PageHeader } from "@/components/page-header";
import { PageContainer } from "@/components/page-container";
import { CodexDeviceCodeModal } from "@/components/codex-device-code-modal";
import { AVAILABLE_CLAUDE_CODE_MODELS, AVAILABLE_CODEX_MODELS, AVAILABLE_GEMINI_CLI_MODELS } from "@/lib/model-constants";
import type {
  UserCredentialSummary,
  ResolvedCredential,
  ListResponse,
  Organization,
  OrgSettings,
  SingleResponse,
} from "@/lib/types";

/* ------------------------------------------------------------------ */
/*  Constants                                                         */
/* ------------------------------------------------------------------ */

const PERSONAL_PROVIDERS: {
  key: string;
  name: string;
  description: string;
  keyPlaceholder: string;
}[] = [
  { key: "anthropic", name: "Anthropic", description: "Claude Code (Opus, Sonnet, Haiku)", keyPlaceholder: "sk-ant-..." },
  { key: "openai", name: "OpenAI", description: "Codex (GPT-5 models)", keyPlaceholder: "sk-..." },
  { key: "gemini", name: "Google Gemini", description: "Gemini CLI (Pro, Flash)", keyPlaceholder: "AIza..." },
  { key: "openrouter", name: "OpenRouter", description: "Access all coding agents with a single key", keyPlaceholder: "sk-or-..." },
];

interface AgentEnvVar {
  name: string;
  label: string;
  sensitive?: boolean;
  placeholder?: string;
  options?: string[];
  advanced?: boolean;
}

const ORG_AGENT_TYPES: { key: string; label: string; description: string; envVars: AgentEnvVar[] }[] = [
  {
    key: "codex",
    label: "Codex",
    description: "OpenAI Codex (GPT-5 models)",
    envVars: [
      { name: "OPENAI_API_KEY", label: "API Key", sensitive: true },
      { name: "OPENAI_MODEL", label: "Default model", options: [...AVAILABLE_CODEX_MODELS] },
      { name: "OPENAI_BASE_URL", label: "Base URL", placeholder: "Custom API endpoint (optional)", advanced: true },
    ],
  },
  {
    key: "claude_code",
    label: "Claude Code",
    description: "Anthropic Claude (Opus, Sonnet, Haiku)",
    envVars: [
      { name: "ANTHROPIC_API_KEY", label: "API Key", sensitive: true },
      { name: "ANTHROPIC_MODEL", label: "Default model", options: [...AVAILABLE_CLAUDE_CODE_MODELS] },
      { name: "ANTHROPIC_BASE_URL", label: "Base URL", placeholder: "Custom API endpoint (optional)", advanced: true },
    ],
  },
  {
    key: "gemini_cli",
    label: "Gemini CLI",
    description: "Google Gemini (Pro, Flash)",
    envVars: [
      { name: "GEMINI_API_KEY", label: "API Key", sensitive: true },
      { name: "GEMINI_MODEL", label: "Default model", options: [...AVAILABLE_GEMINI_CLI_MODELS] },
    ],
  },
];

const DEFAULT_EXECUTION_SETTINGS: Pick<
  Required<OrgSettings>,
  "autonomy_level" | "execution_aggressiveness" | "max_concurrent_runs"
> = {
  autonomy_level: "auto_simple",
  execution_aggressiveness: 2,
  max_concurrent_runs: 5,
};

/* ------------------------------------------------------------------ */
/*  Helpers                                                           */
/* ------------------------------------------------------------------ */

function sourceLabel(source: string): string {
  switch (source) {
    case "personal": return "Your key";
    case "team_default": return "Team default";
    case "org": return "Organization";
    default: return "Not configured";
  }
}

function sourceBadgeVariant(source: string): "success" | "secondary" | "outline" | "destructive" {
  switch (source) {
    case "personal": return "success";
    case "team_default":
    case "org": return "secondary";
    default: return "outline";
  }
}

/* ------------------------------------------------------------------ */
/*  Page                                                              */
/* ------------------------------------------------------------------ */

export default function AgentPage() {
  const queryClient = useQueryClient();
  const { user } = useAuth();
  const isAdmin = user?.role === "admin";

  /* ---------- Personal credentials queries ---------- */

  const { data: personalResp } = useQuery<ListResponse<UserCredentialSummary>>({
    queryKey: ["user-credentials", "personal"],
    queryFn: () => api.userCredentials.listPersonal(),
  });
  const personalCreds = useMemo(() => personalResp?.data ?? [], [personalResp?.data]);

  const { data: resolvedResp } = useQuery<ListResponse<ResolvedCredential>>({
    queryKey: ["user-credentials", "resolved"],
    queryFn: () => api.userCredentials.listResolved(),
  });
  const resolved = useMemo(() => resolvedResp?.data ?? [], [resolvedResp?.data]);

  const { data: teamResp } = useQuery<ListResponse<UserCredentialSummary>>({
    queryKey: ["user-credentials", "team"],
    queryFn: () => api.userCredentials.listTeamDefaults(),
    enabled: isAdmin,
  });
  const teamDefaults = useMemo(() => teamResp?.data ?? [], [teamResp?.data]);

  /* ---------- Personal credentials state ---------- */

  const [apiKeys, setApiKeys] = useState<Record<string, string>>({});
  const [showKeys, setShowKeys] = useState<Record<string, boolean>>({});
  const [keySaveStatus, setKeySaveStatus] = useState<Record<string, "idle" | "saving" | "success" | "error">>({});
  const [removingProvider, setRemovingProvider] = useState<string | null>(null);
  const [removingTeamProvider, setRemovingTeamProvider] = useState<string | null>(null);

  const upsertMutation = useMutation({
    mutationFn: ({ provider, apiKey }: { provider: string; apiKey: string }) =>
      api.userCredentials.upsertPersonal(provider, { api_key: apiKey }),
    onSuccess: (_data, variables) => {
      queryClient.invalidateQueries({ queryKey: ["user-credentials"] });
      setKeySaveStatus((prev) => ({ ...prev, [variables.provider]: "success" }));
      setApiKeys((prev) => ({ ...prev, [variables.provider]: "" }));
      setTimeout(() => setKeySaveStatus((prev) => ({ ...prev, [variables.provider]: "idle" })), 2000);
    },
    onError: (_err, variables) => {
      setKeySaveStatus((prev) => ({ ...prev, [variables.provider]: "error" }));
      setTimeout(() => setKeySaveStatus((prev) => ({ ...prev, [variables.provider]: "idle" })), 3000);
    },
  });

  const deleteMutation = useMutation({
    mutationFn: (provider: string) => api.userCredentials.deletePersonal(provider),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["user-credentials"] });
      setRemovingProvider(null);
    },
  });

  const removeTeamMutation = useMutation({
    mutationFn: (provider: string) => api.userCredentials.removeTeamDefault(provider),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["user-credentials"] });
      setRemovingTeamProvider(null);
    },
  });

  const setTeamDefaultMutation = useMutation({
    mutationFn: ({ provider, userId }: { provider: string; userId: string }) =>
      api.userCredentials.setTeamDefault(provider, userId),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["user-credentials"] });
    },
  });

  function handleSavePersonalKey(provider: string) {
    const key = apiKeys[provider]?.trim();
    if (!key) return;
    setKeySaveStatus((prev) => ({ ...prev, [provider]: "saving" }));
    upsertMutation.mutate({ provider, apiKey: key });
  }

  /* ---------- Org settings queries ---------- */

  const { data: settingsResponse } = useQuery<SingleResponse<Organization>>({
    queryKey: ["settings"],
    queryFn: () => api.settings.get(),
  });

  const { data: agentDefaultsResponse } = useQuery({
    queryKey: ["agent-defaults"],
    queryFn: () => api.settings.getAgentDefaults(),
  });

  const { data: codexAuthStatusResp } = useQuery({
    queryKey: ["codex-auth-status"],
    queryFn: () => api.codexAuth.status(),
    refetchInterval: false,
  });
  const codexAuthStatus = codexAuthStatusResp?.data;

  const orgSettings = (settingsResponse?.data?.settings ?? {}) as OrgSettings;

  /* ---------- Org agent config state ---------- */

  const [defaultAgentTypeOverride, setDefaultAgentTypeOverride] = useState<OrgSettings["default_agent_type"] | null>(null);
  const [agentConfigOverride, setAgentConfigOverride] = useState<Record<string, Record<string, string>> | null>(null);
  const [codexCredentialMethodOverride, setCodexCredentialMethodOverride] = useState<"chatgpt" | "api_key" | null>(null);
  const [showAdvancedSettings, setShowAdvancedSettings] = useState(false);
  const [showDeviceCodeModal, setShowDeviceCodeModal] = useState(false);
  const [orgSaveStatus, setOrgSaveStatus] = useState<"idle" | "success" | "error">("idle");

  const defaultAgentType = defaultAgentTypeOverride ?? orgSettings?.default_agent_type ?? "codex";
  const agentConfig = agentConfigOverride ?? orgSettings?.agent_config ?? {};

  const hasCodexAPIKey = useMemo(() => {
    const codexServerDefaults = (agentDefaultsResponse?.data ?? {}).codex ?? {};
    const codexOrgConfig = agentConfig.codex ?? {};
    return Boolean(codexOrgConfig.OPENAI_API_KEY || codexServerDefaults.OPENAI_API_KEY);
  }, [agentConfig.codex, agentDefaultsResponse?.data]);

  const inferredCodexCredentialMethod: "chatgpt" | "api_key" =
    hasCodexAPIKey && codexAuthStatus?.status !== "completed" ? "api_key" : "chatgpt";
  const codexCredentialMethod = codexCredentialMethodOverride ?? inferredCodexCredentialMethod;

  const orgMutation = useMutation({
    mutationFn: (payload: Record<string, unknown>) => api.settings.update(payload),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["settings"] });
      setOrgSaveStatus("success");
      setTimeout(() => setOrgSaveStatus("idle"), 2000);
    },
    onError: () => {
      setOrgSaveStatus("error");
      setTimeout(() => setOrgSaveStatus("idle"), 3000);
    },
  });

  const disconnectMutation = useMutation({
    mutationFn: () => api.codexAuth.disconnect(),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["codex-auth-status"] });
    },
  });

  function handleSaveOrgAgentConfig() {
    const serverAgentDefaults = agentDefaultsResponse?.data ?? {};
    const cleanedAgentConfig: Record<string, Record<string, string>> = {};

    for (const [agentKey, vars] of Object.entries(agentConfig)) {
      const filtered: Record<string, string> = {};
      const serverVars = serverAgentDefaults[agentKey] ?? {};
      for (const [key, value] of Object.entries(vars)) {
        if (value && value !== serverVars[key]) {
          filtered[key] = value;
        }
      }
      if (Object.keys(filtered).length > 0) {
        cleanedAgentConfig[agentKey] = filtered;
      }
    }

    orgMutation.mutate({
      settings: {
        default_agent_type: defaultAgentType,
        ...(Object.keys(cleanedAgentConfig).length > 0 && { agent_config: cleanedAgentConfig }),
      },
    });
  }

  /* ---------- Execution settings state ---------- */

  const [autonomyLevel, setAutonomyLevel] = useState(DEFAULT_EXECUTION_SETTINGS.autonomy_level);
  const [aggressiveness, setAggressiveness] = useState(String(DEFAULT_EXECUTION_SETTINGS.execution_aggressiveness));
  const [maxConcurrent, setMaxConcurrent] = useState(String(DEFAULT_EXECUTION_SETTINGS.max_concurrent_runs));
  const [execSaveStatus, setExecSaveStatus] = useState<"idle" | "success" | "error">("idle");

  // Sync server data into execution form state
  const [prevSettingsRef, setPrevSettingsRef] = useState<unknown>(undefined);
  const settingsData = settingsResponse?.data?.settings;
  if (settingsData && settingsData !== prevSettingsRef) {
    setPrevSettingsRef(settingsData);
    const s = orgSettings;
    setAutonomyLevel(s.autonomy_level ?? DEFAULT_EXECUTION_SETTINGS.autonomy_level);
    setAggressiveness(String(s.execution_aggressiveness ?? DEFAULT_EXECUTION_SETTINGS.execution_aggressiveness));
    setMaxConcurrent(String(s.max_concurrent_runs ?? DEFAULT_EXECUTION_SETTINGS.max_concurrent_runs));
  }

  const execMutation = useMutation({
    mutationFn: (data: Record<string, unknown>) => api.settings.update(data),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["settings"] });
      setExecSaveStatus("success");
      setTimeout(() => setExecSaveStatus("idle"), 2000);
    },
    onError: () => {
      setExecSaveStatus("error");
      setTimeout(() => setExecSaveStatus("idle"), 3000);
    },
  });

  function handleSaveExecution() {
    execMutation.mutate({
      settings: {
        autonomy_level: autonomyLevel,
        execution_aggressiveness: parseInt(aggressiveness, 10),
        max_concurrent_runs: parseInt(maxConcurrent, 10),
      },
    });
  }

  /* ---------- Render ---------- */

  return (
    <PageContainer size="default">
      <div className="space-y-8">
        <PageHeader
          title="Coding agent"
          description="Configure coding agent credentials and execution behavior."
        />

        {/* ============================================================ */}
        {/*  SECTION 1 — My coding agents (personal credentials)        */}
        {/* ============================================================ */}
        <section className="space-y-3">
          <div>
            <h2 className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">
              My coding agents
            </h2>
            <p className="text-xs text-muted-foreground mt-1">
              Your personal API keys for coding agents. Personal keys are used first, falling back to organization defaults.
            </p>
          </div>

          <div className="space-y-3">
            {PERSONAL_PROVIDERS.map((provider) => {
              const cred = personalCreds.find((c) => c.provider === provider.key);
              const status = keySaveStatus[provider.key] ?? "idle";
              const r = resolved.find((c) => c.provider === provider.key);
              const source = r?.source ?? "none";

              return (
                <Card key={provider.key}>
                  <CardContent>
                    <div className="space-y-3">
                      <div className="flex items-center justify-between">
                        <div>
                          <div className="flex items-center gap-2">
                            <span className="text-sm font-medium">{provider.name}</span>
                            {cred?.configured && (
                              <Badge variant="success" className="text-[10px] px-1.5 py-0">
                                <Check className="mr-0.5 h-3 w-3" />
                                Configured
                              </Badge>
                            )}
                            <Badge variant={sourceBadgeVariant(source)} className="text-[10px] px-1.5 py-0">
                              {sourceLabel(source)}
                            </Badge>
                          </div>
                          <p className="text-xs text-muted-foreground mt-0.5">{provider.description}</p>
                        </div>
                        {cred?.configured && (
                          <Button
                            variant="ghost"
                            size="sm"
                            className="text-xs text-muted-foreground"
                            onClick={() => setRemovingProvider(provider.key)}
                            disabled={deleteMutation.isPending}
                          >
                            Remove
                          </Button>
                        )}
                      </div>

                      {cred?.configured && cred.masked_key && (
                        <p className="text-xs text-muted-foreground font-mono">
                          Key: {cred.masked_key}
                        </p>
                      )}

                      <div className="flex gap-2">
                        <div className="relative flex-1">
                          <Input
                            type={showKeys[provider.key] ? "text" : "password"}
                            placeholder={cred?.configured ? "Replace existing key..." : provider.keyPlaceholder}
                            value={apiKeys[provider.key] ?? ""}
                            onChange={(e) =>
                              setApiKeys((prev) => ({ ...prev, [provider.key]: e.target.value }))
                            }
                            className="pr-9 font-mono text-xs"
                          />
                          <button
                            type="button"
                            onClick={() =>
                              setShowKeys((prev) => ({ ...prev, [provider.key]: !prev[provider.key] }))
                            }
                            className="absolute right-2.5 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground"
                          >
                            {showKeys[provider.key] ? (
                              <EyeOff className="h-3.5 w-3.5" />
                            ) : (
                              <Eye className="h-3.5 w-3.5" />
                            )}
                          </button>
                        </div>
                        <Button
                          size="sm"
                          onClick={() => handleSavePersonalKey(provider.key)}
                          disabled={!apiKeys[provider.key]?.trim() || status === "saving"}
                        >
                          {status === "saving" ? "Saving..." : "Save key"}
                        </Button>
                      </div>

                      {status === "success" && (
                        <p className="text-xs text-emerald-600 dark:text-emerald-400">Key saved successfully.</p>
                      )}
                      {status === "error" && (
                        <p className="text-xs text-destructive">Failed to save key.</p>
                      )}

                      {isAdmin && cred?.configured && !cred.is_team_default && user && (
                        <Button
                          variant="outline"
                          size="sm"
                          className="text-xs"
                          onClick={() => setTeamDefaultMutation.mutate({ provider: provider.key, userId: user.id })}
                          disabled={setTeamDefaultMutation.isPending}
                        >
                          <Shield className="mr-1 h-3 w-3" />
                          Set as team default
                        </Button>
                      )}
                      {cred?.is_team_default && (
                        <Badge variant="secondary" className="text-[10px] px-1.5 py-0">
                          <Shield className="mr-0.5 h-3 w-3" />
                          Team default
                        </Badge>
                      )}
                    </div>
                  </CardContent>
                </Card>
              );
            })}
          </div>
        </section>

        {/* ============================================================ */}
        {/*  SECTION 2 — Organization coding agents (admin only)        */}
        {/* ============================================================ */}
        {isAdmin && (
          <section className="space-y-3">
            <div>
              <h2 className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">
                Organization coding agents
              </h2>
              <p className="text-xs text-muted-foreground mt-1">
                Default agent and credentials for your organization. Used when members don&apos;t have personal keys configured.
              </p>
            </div>

            {/* Default agent selector */}
            <Card>
              <CardContent>
                <div className="space-y-3">
                  <Label>Default coding agent</Label>
                  <RadioGroup
                    value={defaultAgentType}
                    onValueChange={(value) => setDefaultAgentTypeOverride(value as OrgSettings["default_agent_type"])}
                    className="grid grid-cols-3 gap-3"
                  >
                    {ORG_AGENT_TYPES.map((agent) => (
                      <label
                        key={agent.key}
                        className={`relative flex cursor-pointer flex-col rounded-lg border p-3 shadow-sm transition-all duration-150 ${
                          defaultAgentType === agent.key
                            ? "border-primary bg-primary/5 ring-1 ring-primary/20"
                            : "border-input hover:bg-muted/40 hover:border-border"
                        }`}
                      >
                        <div className="flex items-center gap-2">
                          <RadioGroupItem value={agent.key} />
                          <span className="text-sm font-medium">{agent.label}</span>
                        </div>
                        <span className="mt-1 pl-6 text-xs text-muted-foreground">
                          {agent.description}
                        </span>
                      </label>
                    ))}
                  </RadioGroup>
                </div>
              </CardContent>
            </Card>

            {/* Per-agent credential cards */}
            {ORG_AGENT_TYPES.map((agent) => {
              const isSelected = defaultAgentType === agent.key;
              const serverVars = (agentDefaultsResponse?.data ?? {})[agent.key] ?? {};
              const teamCred = teamDefaults.find((c) => {
                if (agent.key === "codex") return c.provider === "openai";
                if (agent.key === "claude_code") return c.provider === "anthropic";
                if (agent.key === "gemini_cli") return c.provider === "gemini";
                return false;
              });
              const teamProviderKey = agent.key === "codex" ? "openai" : agent.key === "claude_code" ? "anthropic" : "gemini";
              const envVarsToRender =
                agent.key === "codex" && codexCredentialMethod === "chatgpt"
                  ? []
                  : agent.envVars.filter((v) => !v.advanced || showAdvancedSettings);
              const hasAdvanced = agent.envVars.some((v) => v.advanced);

              return (
                <Card key={agent.key} className={!isSelected ? "opacity-60" : ""}>
                  <CardContent>
                    <div className="space-y-3">
                      <div className="flex items-center justify-between">
                        <div className="flex items-center gap-2">
                          <span className="text-sm font-medium">{agent.label}</span>
                          {isSelected && (
                            <Badge variant="success" className="text-[10px] px-1.5 py-0">
                              Default
                            </Badge>
                          )}
                          {teamCred && (
                            <Badge variant="secondary" className="text-[10px] px-1.5 py-0">
                              <Shield className="mr-0.5 h-3 w-3" />
                              Team default set
                            </Badge>
                          )}
                        </div>
                        {teamCred && (
                          <Button
                            variant="ghost"
                            size="sm"
                            className="text-xs text-muted-foreground"
                            onClick={() => setRemovingTeamProvider(teamProviderKey)}
                            disabled={removeTeamMutation.isPending}
                          >
                            Remove team default
                          </Button>
                        )}
                      </div>
                      <p className="text-xs text-muted-foreground">{agent.description}</p>

                      {teamCred?.masked_key && (
                        <p className="text-xs text-muted-foreground font-mono">
                          Team key: {teamCred.masked_key}
                          {teamCred.set_by_user_name && <span> &middot; Set by {teamCred.set_by_user_name}</span>}
                        </p>
                      )}

                      {/* Codex ChatGPT sign-in option */}
                      {agent.key === "codex" && (
                        <div className="space-y-3">
                          <Label className="text-xs text-muted-foreground">Credential method</Label>
                          <RadioGroup
                            value={codexCredentialMethod}
                            onValueChange={(value) => setCodexCredentialMethodOverride(value as "chatgpt" | "api_key")}
                            className="grid gap-3 md:grid-cols-2"
                          >
                            <label
                              className={`flex cursor-pointer items-start gap-3 rounded-lg border p-3 shadow-sm transition-all duration-150 ${
                                codexCredentialMethod === "chatgpt" ? "border-primary bg-primary/5 ring-1 ring-primary/20" : "border-input hover:bg-muted/40 hover:border-border"
                              }`}
                            >
                              <RadioGroupItem value="chatgpt" aria-label="Sign in with ChatGPT" />
                              <div className="space-y-1">
                                <div className="flex items-center gap-2">
                                  <Sparkles className="h-4 w-4 text-primary" />
                                  <p className="text-sm font-medium">Sign in with ChatGPT</p>
                                </div>
                                <p className="text-xs text-muted-foreground">Best for gpt-5.3-codex model access.</p>
                              </div>
                            </label>
                            <label
                              className={`flex cursor-pointer items-start gap-3 rounded-lg border p-3 shadow-sm transition-all duration-150 ${
                                codexCredentialMethod === "api_key" ? "border-primary bg-primary/5 ring-1 ring-primary/20" : "border-input hover:bg-muted/40 hover:border-border"
                              }`}
                            >
                              <RadioGroupItem value="api_key" aria-label="Use API key" />
                              <div className="space-y-1">
                                <div className="flex items-center gap-2">
                                  <KeyRound className="h-4 w-4 text-muted-foreground" />
                                  <p className="text-sm font-medium">Use API key</p>
                                </div>
                                <p className="text-xs text-muted-foreground">Pay-as-you-go credentials with configurable model/base URL.</p>
                              </div>
                            </label>
                          </RadioGroup>

                          {codexCredentialMethod === "chatgpt" && (
                            <div className="flex items-center gap-2">
                              {codexAuthStatus?.status === "completed" ? (
                                <>
                                  <Badge variant="outline" className="border-green-600 text-green-600">
                                    <CheckCircle2 className="mr-1 h-3.5 w-3.5" />
                                    Connected
                                  </Badge>
                                  <Button
                                    size="sm"
                                    variant="outline"
                                    onClick={() => disconnectMutation.mutate()}
                                    disabled={disconnectMutation.isPending}
                                  >
                                    {disconnectMutation.isPending ? "Disconnecting..." : "Disconnect"}
                                  </Button>
                                </>
                              ) : (
                                <Button size="sm" onClick={() => setShowDeviceCodeModal(true)}>
                                  Sign in with ChatGPT
                                </Button>
                              )}
                            </div>
                          )}
                        </div>
                      )}

                      {/* Env var fields */}
                      {hasAdvanced && (
                        <Button
                          type="button"
                          size="sm"
                          variant="outline"
                          onClick={() => setShowAdvancedSettings((c) => !c)}
                        >
                          {showAdvancedSettings ? "Hide advanced settings" : "Show advanced settings"}
                        </Button>
                      )}
                      {envVarsToRender.map((envVar) => {
                        const serverDefault = serverVars[envVar.name] ?? "";
                        const orgOverride = agentConfig[agent.key]?.[envVar.name] ?? "";
                        const displayValue = orgOverride || serverDefault;
                        const isServerDefault = !orgOverride && !!serverDefault;

                        return (
                          <div key={envVar.name} className="space-y-1">
                            <div className="flex items-center justify-between">
                              <Label htmlFor={`org-${agent.key}-${envVar.name}`} className="text-xs text-muted-foreground">
                                {envVar.label}
                              </Label>
                              {isServerDefault && (
                                <span className="text-[10px] text-muted-foreground">server default</span>
                              )}
                            </div>
                            {envVar.options ? (
                              <Select
                                value={displayValue || undefined}
                                onValueChange={(value) => {
                                  setAgentConfigOverride({
                                    ...(agentConfigOverride ?? agentConfig),
                                    [agent.key]: {
                                      ...(agentConfigOverride ?? agentConfig)[agent.key],
                                      [envVar.name]: value,
                                    },
                                  });
                                }}
                              >
                                <SelectTrigger
                                  id={`org-${agent.key}-${envVar.name}`}
                                  aria-label={envVar.label}
                                  className={isServerDefault ? "text-muted-foreground" : ""}
                                >
                                  <SelectValue placeholder="Select a model" />
                                </SelectTrigger>
                                <SelectContent>
                                  {envVar.options.map((option) => (
                                    <SelectItem key={option} value={option}>
                                      {option}
                                    </SelectItem>
                                  ))}
                                </SelectContent>
                              </Select>
                            ) : (
                              <Input
                                id={`org-${agent.key}-${envVar.name}`}
                                type={envVar.sensitive ? "password" : "text"}
                                placeholder={envVar.placeholder ?? "Not set"}
                                value={displayValue}
                                className={`font-mono text-xs ${isServerDefault ? "text-muted-foreground" : ""}`}
                                onChange={(e) => {
                                  setAgentConfigOverride({
                                    ...(agentConfigOverride ?? agentConfig),
                                    [agent.key]: {
                                      ...(agentConfigOverride ?? agentConfig)[agent.key],
                                      [envVar.name]: e.target.value,
                                    },
                                  });
                                }}
                              />
                            )}
                          </div>
                        );
                      })}
                      {agent.key === "codex" && codexCredentialMethod === "chatgpt" && (
                        <p className="text-xs text-muted-foreground">
                          API key fields are hidden while ChatGPT sign-in is selected.
                        </p>
                      )}
                    </div>
                  </CardContent>
                </Card>
              );
            })}

            <div className="flex items-center justify-end gap-3">
              <Button onClick={handleSaveOrgAgentConfig} disabled={orgMutation.isPending}>
                {orgMutation.isPending ? "Saving..." : "Save agent configuration"}
              </Button>
              {orgSaveStatus === "success" && (
                <span className="text-[13px] text-emerald-600 dark:text-emerald-400">Saved.</span>
              )}
              {orgSaveStatus === "error" && (
                <span className="text-[13px] text-destructive">Failed to save.</span>
              )}
            </div>
          </section>
        )}

        {/* ============================================================ */}
        {/*  SECTION 3 — Execution (admin only)                         */}
        {/* ============================================================ */}
        {isAdmin && (
          <section className="space-y-3">
            <div>
              <h2 className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">
                Execution
              </h2>
              <p className="text-xs text-muted-foreground mt-1">
                Control how the coding agent runs across your organization.
              </p>
            </div>

            <Card>
              <CardContent>
                <div className="space-y-6">
                  <div className="space-y-3">
                    <Label>Autonomy level</Label>
                    <RadioGroup
                      value={autonomyLevel}
                      onValueChange={(v) => setAutonomyLevel(v as OrgSettings["autonomy_level"] & string)}
                      className="grid grid-cols-3 gap-3"
                    >
                      {[
                        { value: "manual", label: "Manual", description: "Admin triggers all runs" },
                        { value: "auto_simple", label: "Auto (simple)", description: "Auto-run simple issues" },
                        { value: "auto_all", label: "Auto (all)", description: "Auto-run all eligible" },
                      ].map((option) => (
                        <label
                          key={option.value}
                          className={`relative flex cursor-pointer flex-col rounded-lg border p-3 shadow-sm transition-all duration-150 ${
                            autonomyLevel === option.value
                              ? "border-primary bg-primary/5 ring-1 ring-primary/20 dark:shadow-[var(--glow-primary-sm)]"
                              : "border-input hover:bg-muted/40 hover:border-border"
                          }`}
                        >
                          <div className="flex items-center gap-2">
                            <RadioGroupItem value={option.value} />
                            <span className="text-[13px] font-medium">{option.label}</span>
                          </div>
                          <span className="mt-1 pl-6 text-xs text-muted-foreground">
                            {option.description}
                          </span>
                        </label>
                      ))}
                    </RadioGroup>
                  </div>

                  <div className="space-y-3">
                    <Label>Execution aggressiveness</Label>
                    <RadioGroup
                      value={aggressiveness}
                      onValueChange={setAggressiveness}
                      className="grid grid-cols-4 gap-3"
                    >
                      {[
                        { value: "1", label: "Conservative", description: "Minimal changes" },
                        { value: "2", label: "Moderate", description: "Balanced approach" },
                        { value: "3", label: "Aggressive", description: "More changes" },
                        { value: "4", label: "Maximum", description: "Full autonomy" },
                      ].map((option) => (
                        <label
                          key={option.value}
                          className={`relative flex cursor-pointer flex-col rounded-lg border p-3 shadow-sm transition-all duration-150 ${
                            aggressiveness === option.value
                              ? "border-primary bg-primary/5 ring-1 ring-primary/20 dark:shadow-[var(--glow-primary-sm)]"
                              : "border-input hover:bg-muted/40 hover:border-border"
                          }`}
                        >
                          <div className="flex items-center gap-2">
                            <RadioGroupItem value={option.value} />
                            <span className="text-[13px] font-medium">{option.label}</span>
                          </div>
                          <span className="mt-1 pl-6 text-xs text-muted-foreground">
                            {option.description}
                          </span>
                        </label>
                      ))}
                    </RadioGroup>
                  </div>

                  <div className="space-y-2">
                    <Label htmlFor="max-concurrent">Max concurrent runs</Label>
                    <Input
                      id="max-concurrent"
                      type="number"
                      min={1}
                      max={10}
                      value={maxConcurrent}
                      onChange={(e) => setMaxConcurrent(e.target.value)}
                    />
                  </div>
                </div>
              </CardContent>
            </Card>

            <div className="flex items-center justify-end gap-3">
              <Button onClick={handleSaveExecution} disabled={execMutation.isPending}>
                {execMutation.isPending ? "Saving..." : "Save execution settings"}
              </Button>
              {execSaveStatus === "success" && (
                <span className="text-[13px] text-emerald-600 dark:text-emerald-400">Settings saved.</span>
              )}
              {execSaveStatus === "error" && (
                <span className="text-[13px] text-destructive">Failed to save settings.</span>
              )}
            </div>
          </section>
        )}
      </div>

      {/* Remove Personal Key Dialog */}
      <AlertDialog open={!!removingProvider} onOpenChange={(open) => !open && setRemovingProvider(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Remove API key</AlertDialogTitle>
            <AlertDialogDescription>
              Are you sure you want to remove your {removingProvider ? PERSONAL_PROVIDERS.find((p) => p.key === removingProvider)?.name : ""} API key?
              Sessions will fall back to the team default or organization key.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              onClick={() => {
                if (removingProvider) deleteMutation.mutate(removingProvider);
              }}
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
            >
              Remove
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      {/* Remove Team Default Dialog */}
      <AlertDialog open={!!removingTeamProvider} onOpenChange={(open) => !open && setRemovingTeamProvider(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Remove team default</AlertDialogTitle>
            <AlertDialogDescription>
              Are you sure you want to remove the team default for {removingTeamProvider ? PERSONAL_PROVIDERS.find((p) => p.key === removingTeamProvider)?.name : ""}?
              Team members without personal keys will fall back to the organization credential.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              onClick={() => {
                if (removingTeamProvider) removeTeamMutation.mutate(removingTeamProvider);
              }}
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
            >
              Remove
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      {/* Codex Device Code Modal */}
      {showDeviceCodeModal && (
        <CodexDeviceCodeModal
          onClose={() => setShowDeviceCodeModal(false)}
          onConnected={() => {
            queryClient.invalidateQueries({ queryKey: ["codex-auth-status"] });
            setShowDeviceCodeModal(false);
          }}
        />
      )}
    </PageContainer>
  );
}
