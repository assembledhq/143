"use client";

import { type ReactNode, useMemo, useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { CheckCircle2, KeyRound, Sparkles, Check, Eye, EyeOff, Shield } from "lucide-react";
import { api } from "@/lib/api";
import { captureError } from "@/lib/errors";
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

/** Key placeholder strings keyed by provider. */
const KEY_PLACEHOLDERS: Record<string, string> = {
  anthropic: "sk-ant-...",
  openai: "sk-...",
  gemini: "AIza...",
};

interface AgentEnvVar {
  name: string;
  label: string;
  sensitive?: boolean;
  placeholder?: string;
  options?: string[];
  advanced?: boolean;
}

const ORG_AGENT_TYPES: { key: string; label: string; description: string; providerKey: string; envVars: AgentEnvVar[] }[] = [
  {
    key: "codex",
    label: "Codex",
    description: "OpenAI Codex (GPT-5 models)",
    providerKey: "openai",
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
    providerKey: "anthropic",
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
    providerKey: "gemini",
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
/*  Shared RadioCard component                                        */
/* ------------------------------------------------------------------ */

function RadioCard({
  value,
  label,
  description,
  selected,
  icon,
  ariaLabel,
}: {
  value: string;
  label: string;
  description?: string;
  selected: boolean;
  icon?: ReactNode;
  ariaLabel?: string;
}) {
  const indent = icon ? "pl-10" : "pl-6";
  return (
    <label
      className={`relative flex cursor-pointer flex-col rounded-lg border p-3 shadow-sm transition-all duration-150 ${
        selected
          ? "border-primary bg-primary/5 ring-1 ring-primary/20 dark:shadow-[var(--glow-primary-sm)]"
          : "border-input hover:bg-muted/40 hover:border-border"
      }`}
    >
      <div className="flex items-center gap-2">
        <RadioGroupItem value={value} {...(ariaLabel ? { "aria-label": ariaLabel } : {})} />
        {icon}
        <span className="text-[13px] font-medium">{label}</span>
      </div>
      {description && (
        <span className={`mt-1 ${indent} text-xs text-muted-foreground`}>
          {description}
        </span>
      )}
    </label>
  );
}

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

/** Resolve a provider key to a display name. */
function providerDisplayName(providerKey: string): string {
  const agent = ORG_AGENT_TYPES.find((a) => a.providerKey === providerKey);
  return agent?.label ?? providerKey;
}

/* ------------------------------------------------------------------ */
/*  Page                                                              */
/* ------------------------------------------------------------------ */

export default function AgentPage() {
  const queryClient = useQueryClient();
  const { user } = useAuth();
  const isAdmin = user?.role === "admin";

  /* ---------- Personal credentials queries ---------- */

  const { data: personalResp, isSuccess: personalCredsLoaded } = useQuery<ListResponse<UserCredentialSummary>>({
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

  // Default to the first agent that already has a configured key, or claude_code.
  // Returns null until credentials have loaded to avoid a flash of the wrong agent.
  const initialPersonalAgent = useMemo(() => {
    if (!personalCredsLoaded) return null;
    const configured = ORG_AGENT_TYPES.find((a) =>
      personalCreds.some((c) => c.provider === a.providerKey && c.configured),
    );
    return configured?.key ?? "codex";
  }, [personalCreds, personalCredsLoaded]);

  const [personalAgentType, setPersonalAgentType] = useState<string | null>(null);
  const effectivePersonalAgentType = personalAgentType ?? initialPersonalAgent ?? "codex";
  const [apiKeys, setApiKeys] = useState<Record<string, string>>({});
  const [showKeys, setShowKeys] = useState<Record<string, boolean>>({});
  const [keySaveStatus, setKeySaveStatus] = useState<Record<string, "idle" | "saving" | "success" | "error">>({});
  const [removingProvider, setRemovingProvider] = useState<string | null>(null);
  const [removingTeamProvider, setRemovingTeamProvider] = useState<string | null>(null);
  const [personalCodexMethodOverride, setPersonalCodexMethodOverride] = useState<"chatgpt" | "api_key" | null>(null);

  const upsertMutation = useMutation({
    mutationFn: ({ provider, apiKey }: { provider: string; apiKey: string }) =>
      api.userCredentials.upsertPersonal(provider, { api_key: apiKey }),
    onSuccess: (_data, variables) => {
      queryClient.invalidateQueries({ queryKey: ["user-credentials"] });
      setKeySaveStatus((prev) => ({ ...prev, [variables.provider]: "success" }));
      setApiKeys((prev) => ({ ...prev, [variables.provider]: "" }));
      setTimeout(() => setKeySaveStatus((prev) => ({ ...prev, [variables.provider]: "idle" })), 2000);
    },
    onError: (err, variables) => {
      captureError(err, { feature: "agent-key-save" });
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
    onError: (error) => {
      captureError(error, { feature: "agent-key-delete" });
    },
  });

  const removeTeamMutation = useMutation({
    mutationFn: (provider: string) => api.userCredentials.removeTeamDefault(provider),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["user-credentials"] });
      setRemovingTeamProvider(null);
    },
    onError: (error) => {
      captureError(error, { feature: "agent-team-key-remove" });
    },
  });

  const setTeamDefaultMutation = useMutation({
    mutationFn: ({ provider, userId }: { provider: string; userId: string }) =>
      api.userCredentials.setTeamDefault(provider, userId),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["user-credentials"] });
    },
  });

  const { data: codexAuthStatusResp } = useQuery({
    queryKey: ["codex-auth-status"],
    queryFn: () => api.codexAuth.status(),
    refetchInterval: false,
  });
  const codexAuthStatus = codexAuthStatusResp?.data;

  const hasCodexChatGPTConnection = codexAuthStatus?.status === "completed";
  const inferredPersonalCodexMethod: "chatgpt" | "api_key" =
    hasCodexChatGPTConnection ? "chatgpt" : "api_key";
  const personalCodexMethod = personalCodexMethodOverride ?? inferredPersonalCodexMethod;

  function handleSavePersonalKey(provider: string) {
    const key = apiKeys[provider]?.trim();
    if (!key) return;
    setKeySaveStatus((prev) => ({ ...prev, [provider]: "saving" }));
    upsertMutation.mutate({ provider, apiKey: key });
  }

  /* ---------- Org settings queries (admin-gated) ---------- */

  const { data: settingsResponse } = useQuery<SingleResponse<Organization>>({
    queryKey: ["settings"],
    queryFn: () => api.settings.get(),
    enabled: isAdmin,
  });

  const { data: agentDefaultsResponse } = useQuery({
    queryKey: ["agent-defaults"],
    queryFn: () => api.settings.getAgentDefaults(),
    enabled: isAdmin,
  });

  const orgSettings = (settingsResponse?.data?.settings ?? {}) as OrgSettings;

  /* ---------- Org settings state (agent config + execution combined) ---------- */

  // Override-only state: null until the user edits, then holds the user's value.
  // Effective values are derived as: override ?? serverValue ?? default.
  const [defaultAgentTypeOverride, setDefaultAgentTypeOverride] = useState<OrgSettings["default_agent_type"] | null>(null);
  const [agentConfigOverride, setAgentConfigOverride] = useState<Record<string, Record<string, string>> | null>(null);
  const [codexCredentialMethodOverride, setCodexCredentialMethodOverride] = useState<"chatgpt" | "api_key" | null>(null);
  const [showAdvancedPerAgent, setShowAdvancedPerAgent] = useState<Record<string, boolean>>({});
  const [showDeviceCodeModal, setShowDeviceCodeModal] = useState(false);
  const [orgSaveStatus, setOrgSaveStatus] = useState<"idle" | "success" | "error">("idle");

  const [autonomyLevelOverride, setAutonomyLevelOverride] = useState<string | null>(null);
  const [aggressivenessOverride, setAggressivenessOverride] = useState<string | null>(null);
  const [maxConcurrentOverride, setMaxConcurrentOverride] = useState<string | null>(null);

  const defaultAgentType = defaultAgentTypeOverride ?? orgSettings?.default_agent_type ?? "codex";
  const agentConfig = agentConfigOverride ?? orgSettings?.agent_config ?? {};
  const autonomyLevel = autonomyLevelOverride ?? orgSettings?.autonomy_level ?? DEFAULT_EXECUTION_SETTINGS.autonomy_level;
  const aggressiveness = aggressivenessOverride ?? String(orgSettings?.execution_aggressiveness ?? DEFAULT_EXECUTION_SETTINGS.execution_aggressiveness);
  const maxConcurrent = maxConcurrentOverride ?? String(orgSettings?.max_concurrent_runs ?? DEFAULT_EXECUTION_SETTINGS.max_concurrent_runs);

  const hasCodexAPIKey = useMemo(() => {
    const codexServerDefaults = (agentDefaultsResponse?.data ?? {}).codex ?? {};
    const codexOrgConfig = agentConfig.codex ?? {};
    return Boolean(codexOrgConfig.OPENAI_API_KEY || codexServerDefaults.OPENAI_API_KEY);
  }, [agentConfig.codex, agentDefaultsResponse?.data]);

  const inferredCodexCredentialMethod: "chatgpt" | "api_key" =
    hasCodexAPIKey && codexAuthStatus?.status !== "completed" ? "api_key" : "chatgpt";
  const codexCredentialMethod = codexCredentialMethodOverride ?? inferredCodexCredentialMethod;

  // Single mutation for all org settings (agent config + execution)
  const orgMutation = useMutation({
    mutationFn: (payload: Record<string, unknown>) => api.settings.update(payload),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["settings"] });
      setOrgSaveStatus("success");
      setTimeout(() => setOrgSaveStatus("idle"), 2000);
    },
    onError: (error) => {
      captureError(error, { feature: "agent-org-settings" });
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

  function handleSaveOrgSettings() {
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
        autonomy_level: autonomyLevel,
        execution_aggressiveness: parseInt(aggressiveness, 10),
        max_concurrent_runs: parseInt(maxConcurrent, 10),
      },
    });
  }

  /* ---------- Render helpers ---------- */

  /** Shared ChatGPT auth status — used by both personal and org Codex sections. */
  function renderChatGPTAuthStatus(): ReactNode {
    return (
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
    );
  }

  /** Shared header row for agent config sections. */
  function renderAgentConfigHeader({
    title,
    badges,
    action,
  }: {
    title: string;
    badges: ReactNode;
    action?: ReactNode;
  }): ReactNode {
    return (
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-2">
          <span className="text-sm font-medium">{title}</span>
          {badges}
        </div>
        {action}
      </div>
    );
  }

  /** Shared Codex credential method toggle (ChatGPT vs API key). */
  function renderCodexCredentialToggle({
    method,
    onMethodChange,
  }: {
    method: "chatgpt" | "api_key";
    onMethodChange: (value: "chatgpt" | "api_key") => void;
  }): ReactNode {
    return (
      <div className="space-y-3">
        <Label className="text-xs text-muted-foreground">Credential method</Label>
        <RadioGroup
          value={method}
          onValueChange={(value) => onMethodChange(value as "chatgpt" | "api_key")}
          className="grid gap-3 md:grid-cols-2"
        >
          <RadioCard
            value="chatgpt"
            label="Sign in with ChatGPT"
            description="Best for gpt-5.3-codex model access."
            selected={method === "chatgpt"}
            icon={<Sparkles className="h-4 w-4 text-primary" />}
            ariaLabel="Sign in with ChatGPT"
          />
          <RadioCard
            value="api_key"
            label="Use API key"
            description="Pay-as-you-go credentials with configurable model/base URL."
            selected={method === "api_key"}
            icon={<KeyRound className="h-4 w-4 text-muted-foreground" />}
            ariaLabel="Use API key"
          />
        </RadioGroup>

        {method === "chatgpt" && renderChatGPTAuthStatus()}
      </div>
    );
  }

  function renderPersonalCredentialCard(): ReactNode {
    const agent = ORG_AGENT_TYPES.find((a) => a.key === effectivePersonalAgentType) ?? ORG_AGENT_TYPES[0];
    const providerKey = agent.providerKey;
    const cred = personalCreds.find((c) => c.provider === providerKey);
    const status = keySaveStatus[providerKey] ?? "idle";
    const r = resolved.find((c) => c.provider === providerKey);
    const source = r?.source ?? "none";
    const isCodex = agent.key === "codex";
    const hideApiKey = isCodex && personalCodexMethod === "chatgpt";

    return (
      <div className="space-y-3 border-t pt-3 mt-1">
        {renderAgentConfigHeader({
          title: agent.label,
          badges: (
            <>
              {cred?.configured && (
                <Badge variant="success" className="text-[10px] px-1.5 py-0">
                  <Check className="mr-0.5 h-3 w-3" />
                  Configured
                </Badge>
              )}
              <Badge variant={sourceBadgeVariant(source)} className="text-[10px] px-1.5 py-0">
                {sourceLabel(source)}
              </Badge>
            </>
          ),
          action: cred?.configured ? (
            <Button
              variant="ghost"
              size="sm"
              className="text-xs text-muted-foreground"
              onClick={() => setRemovingProvider(providerKey)}
              disabled={deleteMutation.isPending}
            >
              Remove
            </Button>
          ) : undefined,
        })}

        {isCodex && renderCodexCredentialToggle({
          method: personalCodexMethod,
          onMethodChange: setPersonalCodexMethodOverride,
        })}

        {cred?.configured && cred.masked_key && !hideApiKey && (
          <p className="text-xs text-muted-foreground font-mono">
            Key: {cred.masked_key}
          </p>
        )}

        {!hideApiKey && (
          <div className="flex gap-2">
            <div className="relative flex-1">
              <Input
                type={showKeys[providerKey] ? "text" : "password"}
                placeholder={cred?.configured ? "Replace existing key..." : KEY_PLACEHOLDERS[providerKey] ?? "API key"}
                value={apiKeys[providerKey] ?? ""}
                onChange={(e) =>
                  setApiKeys((prev) => ({ ...prev, [providerKey]: e.target.value }))
                }
                className="pr-9 font-mono text-xs"
              />
              <button
                type="button"
                onClick={() =>
                  setShowKeys((prev) => ({ ...prev, [providerKey]: !prev[providerKey] }))
                }
                className="absolute right-2.5 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground"
              >
                {showKeys[providerKey] ? (
                  <EyeOff className="h-3.5 w-3.5" />
                ) : (
                  <Eye className="h-3.5 w-3.5" />
                )}
              </button>
            </div>
            <Button
              size="sm"
              onClick={() => handleSavePersonalKey(providerKey)}
              disabled={!apiKeys[providerKey]?.trim() || status === "saving"}
            >
              {status === "saving" ? "Saving..." : "Save key"}
            </Button>
          </div>
        )}

        {hideApiKey && (
          <p className="text-xs text-muted-foreground">
            API key fields are hidden while ChatGPT sign-in is selected.
          </p>
        )}

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
            onClick={() => setTeamDefaultMutation.mutate({ provider: providerKey, userId: user.id })}
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
    );
  }

  function renderOrgAgentConfigCard(): ReactNode {
    const agent = ORG_AGENT_TYPES.find((a) => a.key === defaultAgentType) ?? ORG_AGENT_TYPES[0];
    const serverVars = (agentDefaultsResponse?.data ?? {})[agent.key] ?? {};
    const teamCred = teamDefaults.find((c) => c.provider === agent.providerKey);
    const r = resolved.find((c) => c.provider === agent.providerKey);
    const source = r?.source ?? "none";
    const showAdvanced = showAdvancedPerAgent[agent.key] ?? false;
    const isCodex = agent.key === "codex";
    const hideEnvVars = isCodex && codexCredentialMethod === "chatgpt";
    const envVarsToRender = hideEnvVars
      ? []
      : agent.envVars.filter((v) => !v.advanced || showAdvanced);
    const hasAdvanced = agent.envVars.some((v) => v.advanced);

    return (
      <div className="space-y-3 border-t pt-3 mt-1">
        {renderAgentConfigHeader({
          title: `${agent.label} settings`,
          badges: teamCred ? (
            <Badge variant="secondary" className="text-[10px] px-1.5 py-0">
              <Shield className="mr-0.5 h-3 w-3" />
              Team default set
            </Badge>
          ) : (
            <Badge variant={sourceBadgeVariant(source)} className="text-[10px] px-1.5 py-0">
              {sourceLabel(source)}
            </Badge>
          ),
          action: teamCred ? (
            <Button
              variant="ghost"
              size="sm"
              className="text-xs text-muted-foreground"
              onClick={() => setRemovingTeamProvider(agent.providerKey)}
              disabled={removeTeamMutation.isPending}
            >
              Remove team default
            </Button>
          ) : undefined,
        })}

        {teamCred?.masked_key && (
          <p className="text-xs text-muted-foreground font-mono">
            Team key: {teamCred.masked_key}
            {teamCred.set_by_user_name && <span> &middot; Set by {teamCred.set_by_user_name}</span>}
          </p>
        )}

        {isCodex && renderCodexCredentialToggle({
          method: codexCredentialMethod,
          onMethodChange: setCodexCredentialMethodOverride,
        })}

        {/* Env var fields */}
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

        {hideEnvVars && (
          <p className="text-xs text-muted-foreground">
            API key fields are hidden while ChatGPT sign-in is selected.
          </p>
        )}
        {hasAdvanced && !hideEnvVars && (
          <Button
            type="button"
            size="sm"
            variant="ghost"
            className="text-xs text-muted-foreground"
            onClick={() => setShowAdvancedPerAgent((prev) => ({ ...prev, [agent.key]: !prev[agent.key] }))}
          >
            {showAdvanced ? "Hide advanced settings" : "Advanced settings"}
          </Button>
        )}
      </div>
    );
  }

  /* ---------- Render ---------- */

  return (
    <PageContainer size="default">
      <div className="space-y-8">
        <PageHeader
          title="Coding agents"
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
              Your personal API keys. Personal keys are used first, falling back to organization defaults.
            </p>
          </div>

          <Card>
            <CardContent>
              <div className="space-y-3">
                <RadioGroup
                  value={effectivePersonalAgentType}
                  onValueChange={setPersonalAgentType}
                  className="grid grid-cols-3 gap-3"
                >
                  {ORG_AGENT_TYPES.map((agent) => {
                    const cred = personalCreds.find((c) => c.provider === agent.providerKey);
                    return (
                      <RadioCard
                        key={agent.key}
                        value={agent.key}
                        label={agent.label}
                        selected={effectivePersonalAgentType === agent.key}
                        icon={cred?.configured ? <Check className="h-3.5 w-3.5 text-emerald-600 dark:text-emerald-400" /> : undefined}
                      />
                    );
                  })}
                </RadioGroup>

                {/* Credential details for the selected personal agent */}
                {personalCredsLoaded && renderPersonalCredentialCard()}
              </div>
            </CardContent>
          </Card>
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
                      <RadioCard
                        key={agent.key}
                        value={agent.key}
                        label={agent.label}
                        description={agent.description}
                        selected={defaultAgentType === agent.key}
                      />
                    ))}
                  </RadioGroup>

                  {/* Config details for the selected agent */}
                  {renderOrgAgentConfigCard()}
                </div>
              </CardContent>
            </Card>
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
                      onValueChange={(v) => setAutonomyLevelOverride(v)}
                      className="grid grid-cols-3 gap-3"
                    >
                      {[
                        { value: "manual", label: "Manual", description: "Admin triggers all runs" },
                        { value: "auto_simple", label: "Auto (simple)", description: "Auto-run simple issues" },
                        { value: "auto_all", label: "Auto (all)", description: "Auto-run all eligible" },
                      ].map((option) => (
                        <RadioCard
                          key={option.value}
                          value={option.value}
                          label={option.label}
                          description={option.description}
                          selected={autonomyLevel === option.value}
                        />
                      ))}
                    </RadioGroup>
                  </div>

                  <div className="space-y-3">
                    <Label>Execution aggressiveness</Label>
                    <RadioGroup
                      value={aggressiveness}
                      onValueChange={setAggressivenessOverride}
                      className="grid grid-cols-4 gap-3"
                    >
                      {[
                        { value: "1", label: "Conservative", description: "Minimal changes" },
                        { value: "2", label: "Moderate", description: "Balanced approach" },
                        { value: "3", label: "Aggressive", description: "More changes" },
                        { value: "4", label: "Maximum", description: "Full autonomy" },
                      ].map((option) => (
                        <RadioCard
                          key={option.value}
                          value={option.value}
                          label={option.label}
                          description={option.description}
                          selected={aggressiveness === option.value}
                        />
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
                      onChange={(e) => setMaxConcurrentOverride(e.target.value)}
                    />
                  </div>
                </div>
              </CardContent>
            </Card>
          </section>
        )}
      </div>

      {/* Sticky save bar for org settings (admin only) */}
      {isAdmin && (
        <div className="sticky bottom-0 z-50 -mx-4 mt-6 border-t bg-background/95 px-4 py-3 backdrop-blur supports-[backdrop-filter]:bg-background/80">
          <div className="flex items-center justify-end gap-3">
            <Button onClick={handleSaveOrgSettings} disabled={orgMutation.isPending}>
              {orgMutation.isPending ? "Saving..." : "Save organization settings"}
            </Button>
            {orgSaveStatus === "success" && (
              <span className="text-[13px] text-emerald-600 dark:text-emerald-400">Settings saved.</span>
            )}
            {orgSaveStatus === "error" && (
              <span className="text-[13px] text-destructive">Failed to save settings.</span>
            )}
          </div>
        </div>
      )}

      {/* Remove Personal Key Dialog */}
      <AlertDialog open={!!removingProvider} onOpenChange={(open) => !open && setRemovingProvider(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Remove API key</AlertDialogTitle>
            <AlertDialogDescription>
              Are you sure you want to remove your {providerDisplayName(removingProvider ?? "")} API key?
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
              Are you sure you want to remove the team default for {providerDisplayName(removingTeamProvider ?? "")}?
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

      {/* Codex Device Code Modal (shared across personal + org) */}
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
