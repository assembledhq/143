"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/lib/api";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import type { CodexDeviceAuth, OrgSettings, Organization, SingleResponse } from "@/lib/types";

interface AgentEnvVar {
  name: string;
  label: string;
  sensitive?: boolean;
  placeholder?: string;
}

const AGENT_TYPES: { key: string; label: string; envVars: AgentEnvVar[] }[] = [
  {
    key: "codex",
    label: "Codex",
    envVars: [
      { name: "OPENAI_API_KEY", label: "API Key", sensitive: true },
      { name: "OPENAI_MODEL", label: "Model", placeholder: "e.g. codex-mini, o3" },
      { name: "OPENAI_BASE_URL", label: "Base URL", placeholder: "Custom API endpoint (optional)" },
    ],
  },
  {
    key: "claude_code",
    label: "Claude Code",
    envVars: [
      { name: "ANTHROPIC_API_KEY", label: "API Key", sensitive: true },
      { name: "ANTHROPIC_MODEL", label: "Model", placeholder: "e.g. claude-sonnet-4-5, opus" },
      { name: "ANTHROPIC_BASE_URL", label: "Base URL", placeholder: "Custom API endpoint (optional)" },
    ],
  },
  {
    key: "gemini_cli",
    label: "Gemini CLI",
    envVars: [
      { name: "GEMINI_API_KEY", label: "API Key", sensitive: true },
      { name: "GEMINI_MODEL", label: "Model", placeholder: "e.g. gemini-2.5-pro, gemini-2.5-flash" },
    ],
  },
];

function DeviceCodeModal({ onClose }: { onClose: () => void }) {
  const [deviceAuth, setDeviceAuth] = useState<CodexDeviceAuth | null>(null);
  const [status, setStatus] = useState<string>("initiating");
  const [error, setError] = useState<string>("");
  const [timeLeft, setTimeLeft] = useState(0);
  const pollRef = useRef<NodeJS.Timeout | null>(null);
  const timerRef = useRef<NodeJS.Timeout | null>(null);
  const onCloseRef = useRef(onClose);
  const queryClient = useQueryClient();

  useEffect(() => {
    onCloseRef.current = onClose;
  }, [onClose]);

  const startAuth = async () => {
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
  };

  useEffect(() => {
    const id = setTimeout(() => {
      void startAuth();
    }, 0);
    return () => clearTimeout(id);
  }, []);

  useEffect(() => {
    if (status !== "pending") return;

    pollRef.current = setInterval(async () => {
      try {
        const resp = await api.codexAuth.status();
        if (resp.data.status === "completed") {
          setStatus("completed");
          queryClient.invalidateQueries({ queryKey: ["codex-auth-status"] });
          if (pollRef.current) clearInterval(pollRef.current);
          if (timerRef.current) clearInterval(timerRef.current);
          setTimeout(() => onCloseRef.current(), 1500);
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
      } catch {
      }
    }, 3000);

    timerRef.current = setInterval(() => {
      setTimeLeft((time) => Math.max(0, time - 1));
    }, 1000);

    return () => {
      if (pollRef.current) clearInterval(pollRef.current);
      if (timerRef.current) clearInterval(timerRef.current);
    };
  }, [status, queryClient]);

  const minutes = Math.floor(timeLeft / 60);
  const seconds = timeLeft % 60;

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50">
      <div className="w-full max-w-md rounded-lg border bg-background p-6 shadow-lg">
        <h3 className="text-lg font-medium">Connect your ChatGPT account</h3>

        {status === "initiating" && (
          <p className="mt-4 text-sm text-muted-foreground">Starting authentication...</p>
        )}

        {status === "pending" && deviceAuth && (
          <div className="mt-4 space-y-4">
            <div className="space-y-2">
              <p className="text-sm text-muted-foreground">1. Open this link:</p>
              <a
                href={deviceAuth.verification_uri}
                target="_blank"
                rel="noopener noreferrer"
                className="text-sm font-medium text-primary underline"
              >
                {deviceAuth.verification_uri}
              </a>
            </div>

            <div className="space-y-2">
              <p className="text-sm text-muted-foreground">2. Enter this code:</p>
              <div className="flex items-center gap-2">
                <code className="rounded-md border bg-muted px-4 py-2 text-2xl font-mono font-bold tracking-widest">
                  {deviceAuth.user_code}
                </code>
                <Button
                  size="sm"
                  variant="outline"
                  onClick={() => navigator.clipboard.writeText(deviceAuth.user_code)}
                >
                  Copy
                </Button>
              </div>
            </div>

            <div className="space-y-2">
              <p className="text-sm text-muted-foreground">Waiting for authentication...</p>
              <div className="h-1.5 w-full overflow-hidden rounded-full bg-muted">
                <div
                  className="h-full rounded-full bg-primary transition-all duration-1000"
                  style={{ width: `${deviceAuth ? Math.max(0, (timeLeft / deviceAuth.expires_in) * 100) : 0}%` }}
                />
              </div>
              <p className="text-xs text-muted-foreground">
                Expires in {minutes}:{seconds.toString().padStart(2, "0")}
              </p>
            </div>
          </div>
        )}

        {status === "completed" && (
          <div className="mt-4">
            <p className="text-sm font-medium text-green-600">Connected successfully!</p>
          </div>
        )}

        {(status === "error" || status === "expired") && (
          <div className="mt-4 space-y-3">
            <p className="text-sm text-destructive">{error}</p>
            <Button size="sm" onClick={() => void startAuth()}>
              Try Again
            </Button>
          </div>
        )}

        <div className="mt-6 flex justify-end">
          <Button variant="outline" size="sm" onClick={onClose}>
            {status === "completed" ? "Done" : "Cancel"}
          </Button>
        </div>
      </div>
    </div>
  );
}

export function AgentSettingsEditor({
  title,
  description,
  onClose,
}: {
  title: string;
  description: string;
  onClose?: () => void;
}) {
  const queryClient = useQueryClient();
  const [defaultAgentTypeOverride, setDefaultAgentTypeOverride] = useState<OrgSettings["default_agent_type"] | null>(null);
  const [agentConfigOverride, setAgentConfigOverride] = useState<Record<string, Record<string, string>> | null>(null);
  const [saveStatus, setSaveStatus] = useState<"idle" | "success" | "error">("idle");
  const [showDeviceCodeModal, setShowDeviceCodeModal] = useState(false);

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

  const mutation = useMutation({
    mutationFn: (payload: Record<string, unknown>) => api.settings.update(payload),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["settings"] });
      setSaveStatus("success");
      setTimeout(() => setSaveStatus("idle"), 2000);
    },
    onError: () => {
      setSaveStatus("error");
      setTimeout(() => setSaveStatus("idle"), 3000);
    },
  });

  const disconnectMutation = useMutation({
    mutationFn: () => api.codexAuth.disconnect(),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["codex-auth-status"] });
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
        <Label>Default Agent</Label>
        <RadioGroup
          value={defaultAgentType}
          onValueChange={(value) => setDefaultAgentTypeOverride(value as OrgSettings["default_agent_type"])}
          className="grid grid-cols-3 gap-3"
        >
          {AGENT_TYPES.map((agent) => (
            <label
              key={agent.key}
              className={`relative flex cursor-pointer flex-col rounded-lg border p-3 transition-colors ${
                defaultAgentType === agent.key
                  ? "border-primary bg-primary/5"
                  : "border-input hover:bg-muted/50"
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
          <div className="space-y-3 rounded-lg border p-4">
            <div className="flex items-center justify-between">
              <div>
                <h4 className="text-sm font-medium">Sign in with ChatGPT</h4>
                <p className="text-xs text-muted-foreground">
                  For gpt-5.3-codex model access.
                </p>
              </div>
              <Badge variant="secondary">Recommended</Badge>
            </div>

            <div className="flex items-center gap-2">
              {codexAuthStatus?.status === "completed" ? (
                <>
                  <Badge variant="outline" className="text-green-600 border-green-600">
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
                <Button
                  size="sm"
                  onClick={() => setShowDeviceCodeModal(true)}
                >
                  Sign in with ChatGPT
                </Button>
              )}
            </div>
          </div>

          <div className="space-y-3 rounded-lg border p-4">
            <div>
              <h4 className="text-sm font-medium">API Key</h4>
              <p className="text-xs text-muted-foreground">
                Pay-as-you-go. Does not support gpt-5.3-codex.
              </p>
            </div>
          </div>
        </div>
      )}

      <div className="space-y-3">
        {(() => {
          const serverVars = (agentDefaultsResponse?.data ?? {})[selectedAgent.key] ?? {};
          return selectedAgent.envVars.map((envVar) => {
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
              </div>
            );
          });
        })()}
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
        <DeviceCodeModal onClose={() => setShowDeviceCodeModal(false)} />
      )}
    </div>
  );
}
