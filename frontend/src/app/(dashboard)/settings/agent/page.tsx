"use client";

import { type ReactNode, useMemo, useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { CheckCircle2, KeyRound, Sparkles, Shield } from "lucide-react";
import { api } from "@/lib/api";
import { captureError } from "@/lib/errors";
import { useAuth } from "@/hooks/use-auth";
import { AGENT_TYPES, sourceLabel, sourceBadgeVariant, providerDisplayName } from "@/lib/agent-constants";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { RadioGroup } from "@/components/ui/radio-group";
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
import { RadioCard } from "@/components/radio-card";
import { CodexDeviceCodeModal } from "@/components/codex-device-code-modal";
import type {
  UserCredentialSummary,
  ResolvedCredential,
  ListResponse,
  Organization,
  OrgSettings,
  SingleResponse,
} from "@/lib/types";

const DEFAULT_EXECUTION_SETTINGS: Pick<
  Required<OrgSettings>,
  "autonomy_level" | "execution_aggressiveness" | "max_concurrent_runs"
> = {
  autonomy_level: "auto_simple",
  execution_aggressiveness: 2,
  max_concurrent_runs: 5,
};

/* ------------------------------------------------------------------ */
/*  Page                                                              */
/* ------------------------------------------------------------------ */

export default function AgentPage() {
  const queryClient = useQueryClient();
  const { user } = useAuth();
  const isAdmin = user?.role === "admin";

  /* ---------- Credentials queries ---------- */

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

  const [removingTeamProvider, setRemovingTeamProvider] = useState<string | null>(null);

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

  const { data: codexAuthStatusResp } = useQuery({
    queryKey: ["codex-auth-status"],
    queryFn: () => api.codexAuth.status(),
    refetchInterval: false,
  });
  const codexAuthStatus = codexAuthStatusResp?.data;

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

  function renderOrgAgentConfigCard(): ReactNode {
    const agent = AGENT_TYPES.find((a) => a.key === defaultAgentType) ?? AGENT_TYPES[0];
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
            <Badge variant="secondary" className="text-xs px-1.5 py-0">
              <Shield className="mr-0.5 h-3 w-3" />
              Team default set
            </Badge>
          ) : (
            <Badge variant={sourceBadgeVariant(source)} className="text-xs px-1.5 py-0">
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
                  <span className="text-xs text-muted-foreground">server default</span>
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
          description="Configure organization agent defaults and execution behavior."
        />

        {/* ============================================================ */}
        {/*  SECTION 1 — Organization coding agents (admin only)        */}
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
                    {AGENT_TYPES.map((agent) => (
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
              <span className="text-xs text-emerald-600 dark:text-emerald-400">Settings saved.</span>
            )}
            {orgSaveStatus === "error" && (
              <span className="text-xs text-destructive">Failed to save settings.</span>
            )}
          </div>
        </div>
      )}

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
