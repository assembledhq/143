"use client";

import { useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { CheckCircle2, KeyRound, Sparkles } from "lucide-react";
import { api } from "@/lib/api";
import { captureError } from "@/lib/errors";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
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
import { CodexDeviceCodeModal } from "@/components/codex-device-code-modal";
import { AVAILABLE_CLAUDE_CODE_MODELS, AVAILABLE_CODEX_MODELS, AVAILABLE_GEMINI_CLI_MODELS } from "@/lib/model-constants";
import type { OrgSettings, Organization, SingleResponse } from "@/lib/types";

interface AgentEnvVar {
  name: string;
  label: string;
  sensitive?: boolean;
  placeholder?: string;
  options?: string[];
  advanced?: boolean;
  hideInSetup?: boolean;
}

const AGENT_TYPES: { key: string; label: string; envVars: AgentEnvVar[] }[] = [
  {
    key: "codex",
    label: "Codex",
    envVars: [
      { name: "OPENAI_API_KEY", label: "API Key", sensitive: true },
      { name: "OPENAI_MODEL", label: "Default model", options: [...AVAILABLE_CODEX_MODELS] },
      { name: "OPENAI_BASE_URL", label: "Base URL", placeholder: "Custom API endpoint (optional)" },
    ],
  },
  {
    key: "claude_code",
    label: "Claude Code",
    envVars: [
      { name: "ANTHROPIC_API_KEY", label: "API Key", sensitive: true },
      {
        name: "ANTHROPIC_MODEL",
        label: "Default model",
        options: [...AVAILABLE_CLAUDE_CODE_MODELS],
      },
      {
        name: "ANTHROPIC_BASE_URL",
        label: "Base URL",
        placeholder: "Custom API endpoint (optional)",
        advanced: true,
        hideInSetup: true,
      },
    ],
  },
  {
    key: "gemini_cli",
    label: "Gemini CLI",
    envVars: [
      { name: "GEMINI_API_KEY", label: "API Key", sensitive: true },
      {
        name: "GEMINI_MODEL",
        label: "Default model",
        options: [...AVAILABLE_GEMINI_CLI_MODELS],
      },
    ],
  },
];

export function AgentSettingsEditor({
  title,
  description,
  onClose,
  initialAgentType,
  setupMode = false,
}: {
  title: string;
  description: string;
  onClose?: () => void;
  initialAgentType?: OrgSettings["default_agent_type"];
  setupMode?: boolean;
}) {
  const queryClient = useQueryClient();
  const [defaultAgentTypeOverride, setDefaultAgentTypeOverride] = useState<OrgSettings["default_agent_type"] | null>(initialAgentType ?? null);
  const [agentConfigOverride, setAgentConfigOverride] = useState<Record<string, Record<string, string>> | null>(null);
  const [saveStatus, setSaveStatus] = useState<"idle" | "success" | "error">("idle");
  const [showDeviceCodeModal, setShowDeviceCodeModal] = useState(false);
  const [codexCredentialMethodOverride, setCodexCredentialMethodOverride] = useState<"chatgpt" | "api_key" | null>(null);
  const [showAdvancedSettings, setShowAdvancedSettings] = useState(false);

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

  const settings = settingsResponse?.data?.settings as OrgSettings | undefined;
  const defaultAgentType = defaultAgentTypeOverride ?? settings?.default_agent_type ?? "codex";
  const agentConfig = agentConfigOverride ?? settings?.agent_config ?? {};

  const hasCodexAPIKey = useMemo(() => {
    const codexServerDefaults = (agentDefaultsResponse?.data ?? {}).codex ?? {};
    const codexOrgConfig = agentConfig.codex ?? {};
    return Boolean(codexOrgConfig.OPENAI_API_KEY || codexServerDefaults.OPENAI_API_KEY);
  }, [agentConfig.codex, agentDefaultsResponse?.data]);

  const inferredCodexCredentialMethod: "chatgpt" | "api_key" =
    hasCodexAPIKey && codexAuthStatus?.status !== "completed" ? "api_key" : "chatgpt";
  const codexCredentialMethod = codexCredentialMethodOverride ?? inferredCodexCredentialMethod;

  const mutation = useMutation({
    mutationFn: (payload: Record<string, unknown>) => api.settings.update(payload),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["settings"] });
      setSaveStatus("success");
      setTimeout(() => setSaveStatus("idle"), 2000);
    },
    onError: (error) => {
      captureError(error, { feature: "agent-settings" });
      setSaveStatus("error");
      setTimeout(() => setSaveStatus("idle"), 3000);
    },
  });

  const disconnectMutation = useMutation({
    mutationFn: () => api.codexAuth.disconnect(),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["codex-auth-status"] });
    },
    onError: (error) => {
      captureError(error, { feature: "codex-disconnect" });
    },
  });

  const selectedAgent = useMemo(
    () => AGENT_TYPES.find((agent) => agent.key === defaultAgentType) ?? AGENT_TYPES[0],
    [defaultAgentType]
  );

  function handleSave() {
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

    mutation.mutate({
      settings: {
        default_agent_type: defaultAgentType,
        ...(Object.keys(cleanedAgentConfig).length > 0 && { agent_config: cleanedAgentConfig }),
      },
    });
  }

  return (
    <div className="space-y-4">
      <div className="space-y-1">
        <h3 className="text-base font-medium text-foreground">{title}</h3>
        <p className="text-sm text-muted-foreground">{description}</p>
      </div>

      <div className="space-y-3">
        <Label>Default coding agent</Label>
        <RadioGroup
          value={defaultAgentType}
          onValueChange={(value) => setDefaultAgentTypeOverride(value as OrgSettings["default_agent_type"])}
          className="grid grid-cols-3 gap-3"
        >
          {AGENT_TYPES.map((agent) => (
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
            </label>
          ))}
        </RadioGroup>
      </div>

      {defaultAgentType === "codex" && (
        <div className="space-y-4">
          <div className="space-y-3">
            <Label>Credential method</Label>
            <RadioGroup
              value={codexCredentialMethod}
              onValueChange={(value) => {
                setCodexCredentialMethodOverride(value as "chatgpt" | "api_key");
              }}
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
          </div>

          {codexCredentialMethod === "chatgpt" ? (
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
          ) : (
            <div className="space-y-2">
              <h4 className="text-sm font-medium">API key configuration</h4>
              <p className="text-xs text-muted-foreground">
                Enter API key, model, and optional base URL below.
              </p>
            </div>
          )}
        </div>
      )}

      <div className="space-y-3">
        {(() => {
          const envVarsToRender =
            selectedAgent.key === "codex" && codexCredentialMethod === "chatgpt"
              ? []
              : selectedAgent.envVars.filter((envVar) => !(setupMode && envVar.hideInSetup));
          const hasAdvancedSettings = !setupMode && envVarsToRender.some((envVar) => envVar.advanced);
          const serverVars = (agentDefaultsResponse?.data ?? {})[selectedAgent.key] ?? {};
          const visibleEnvVars = envVarsToRender.filter((envVar) => !envVar.advanced || showAdvancedSettings);
          return (
            <>
              {hasAdvancedSettings && (
                <Button
                  type="button"
                  size="sm"
                  variant="outline"
                  onClick={() => setShowAdvancedSettings((current) => !current)}
                >
                  {showAdvancedSettings ? "Hide advanced settings" : "Show advanced settings"}
                </Button>
              )}
              {visibleEnvVars.map((envVar) => {
            const serverDefault = serverVars[envVar.name] ?? "";
            const orgOverride = agentConfig[selectedAgent.key]?.[envVar.name] ?? "";
            const displayValue = orgOverride || serverDefault;
            const isServerDefault = !orgOverride && !!serverDefault;

            return (
              <div key={envVar.name} className="space-y-1">
                <div className="flex items-center justify-between">
                  <Label htmlFor={`${selectedAgent.key}-${envVar.name}`} className="text-xs text-muted-foreground">
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
                        [selectedAgent.key]: {
                          ...(agentConfigOverride ?? agentConfig)[selectedAgent.key],
                          [envVar.name]: value,
                        },
                      });
                    }}
                  >
                    <SelectTrigger
                      id={`${selectedAgent.key}-${envVar.name}`}
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
                    id={`${selectedAgent.key}-${envVar.name}`}
                    type={envVar.sensitive ? "password" : "text"}
                    placeholder={envVar.placeholder ?? "Not set"}
                    value={displayValue}
                    className={isServerDefault ? "text-muted-foreground" : ""}
                    onChange={(e) => {
                      setAgentConfigOverride({
                        ...(agentConfigOverride ?? agentConfig),
                        [selectedAgent.key]: {
                          ...(agentConfigOverride ?? agentConfig)[selectedAgent.key],
                          [envVar.name]: e.target.value,
                        },
                      });
                    }}
                  />
                )}
              </div>
            );
              })}
            </>
          );
        })()}
        {selectedAgent.key === "codex" && codexCredentialMethod === "chatgpt" && (
          <p className="text-xs text-muted-foreground">
            API key fields are hidden while ChatGPT sign-in is selected.
          </p>
        )}
      </div>

      <div className="flex items-center justify-between gap-2 pt-2">
        <div className="min-h-5 text-xs">
          {saveStatus === "success" && <span className="text-green-600">Saved.</span>}
          {saveStatus === "error" && <span className="text-destructive">Save failed.</span>}
        </div>
        <div className="flex items-center gap-2">
          {onClose && (
            <Button variant="outline" size="sm" onClick={onClose}>
              Cancel
            </Button>
          )}
          <Button size="sm" onClick={handleSave} disabled={mutation.isPending}>
            {mutation.isPending ? "Saving..." : "Save changes"}
          </Button>
        </div>
      </div>
      {showDeviceCodeModal && (
        <CodexDeviceCodeModal
          onClose={() => setShowDeviceCodeModal(false)}
          onConnected={() => {
            queryClient.invalidateQueries({ queryKey: ["codex-auth-status"] });
            setShowDeviceCodeModal(false);
          }}
        />
      )}
    </div>
  );
}
