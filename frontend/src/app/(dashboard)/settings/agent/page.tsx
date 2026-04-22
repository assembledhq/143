"use client";

import { useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  AlertCircle,
  Check,
  CheckCircle2,
  Plus,
  Shield,
  Trash2,
} from "lucide-react";
import { toast } from "sonner";
import { api } from "@/lib/api";
import { captureError } from "@/lib/errors";
import { useAuth } from "@/hooks/use-auth";
import { AGENT_TYPES, providerDisplayName } from "@/lib/agent-constants";
import { AgentBadge } from "@/components/agent-badge";
import { AutosaveIndicator } from "@/components/AutosaveIndicator";
import { DebouncedInput } from "@/components/debounced-fields";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent } from "@/components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
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
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { PageHeader } from "@/components/page-header";
import { PageContainer } from "@/components/page-container";
import { RadioCard } from "@/components/radio-card";
import { CodexDeviceCodeModal } from "@/components/codex-device-code-modal";
import { ClaudeCodeAuthModal } from "@/components/claude-code-auth-modal";
import { useAutosave } from "@/hooks/useAutosave";
import { useAutosaveNumericField } from "@/hooks/useAutosaveNumericField";
import { queryKeys } from "@/lib/query-keys";
import {
  applyOrgSettingsPatch,
  coalesceSettingsPatch,
  type SettingsPatch,
} from "@/lib/settings-autosave";
import {
  MIN_CONCURRENT_RUNS,
  MAX_CONCURRENT_RUNS,
  MIN_SESSION_DURATION_MINUTES,
  MAX_SESSION_DURATION_MINUTES,
  clampNumber,
} from "@/lib/settings-constants";
import type {
  ClaudeCodeSubscription,
  CodexSubscription,
  ListResponse,
  Organization,
  OrgSettings,
  SingleResponse,
  UserCredentialSummary,
} from "@/lib/types";

const DEFAULT_EXECUTION_SETTINGS = {
  autonomy_level: "auto_simple" as const,
  execution_aggressiveness: 2,
  max_concurrent_runs: 5,
  max_session_duration_seconds: 25 * 60,
};

type DisplaySubscription = {
  id: string;
  label: string;
  status: string;
  account_type?: string;
  last_used_at?: string;
  created_at?: string;
};

export default function AgentPage() {
  const queryClient = useQueryClient();
  const { user } = useAuth();
  const isAdmin = user?.role === "admin";

  const { data: teamResp } = useQuery<ListResponse<UserCredentialSummary>>({
    queryKey: ["user-credentials", "team"],
    queryFn: () => api.userCredentials.listTeamDefaults(),
    enabled: isAdmin,
  });
  const teamDefaults = useMemo(() => teamResp?.data ?? [], [teamResp?.data]);

  const { data: codexSubscriptionsResp } = useQuery<ListResponse<CodexSubscription>>({
    queryKey: ["codex-subscriptions"],
    queryFn: () => api.codexAuth.listSubscriptions(),
  });
  const codexSubscriptions = useMemo(
    () => codexSubscriptionsResp?.data ?? [],
    [codexSubscriptionsResp?.data],
  );

  const { data: claudeCodeSubscriptionsResp } = useQuery<ListResponse<ClaudeCodeSubscription>>({
    queryKey: ["claude-code-subscriptions"],
    queryFn: () => api.claudeCodeAuth.listSubscriptions(),
  });
  const claudeCodeSubscriptions = useMemo(
    () => claudeCodeSubscriptionsResp?.data ?? [],
    [claudeCodeSubscriptionsResp?.data],
  );

  const { data: settingsResponse } = useQuery<SingleResponse<Organization>>({
    queryKey: queryKeys.settings.all,
    queryFn: () => api.settings.get(),
    enabled: isAdmin,
  });

  const orgSettings = (settingsResponse?.data?.settings ?? {}) as OrgSettings;
  const defaultAgentType = orgSettings.default_agent_type ?? "codex";
  const agentConfig = orgSettings.agent_config ?? {};
  const autonomyLevel = orgSettings.autonomy_level ?? DEFAULT_EXECUTION_SETTINGS.autonomy_level;
  const aggressiveness = String(
    orgSettings.execution_aggressiveness ?? DEFAULT_EXECUTION_SETTINGS.execution_aggressiveness,
  );
  const maxConcurrentServer =
    orgSettings.max_concurrent_runs ?? DEFAULT_EXECUTION_SETTINGS.max_concurrent_runs;
  const serverSessionSeconds =
    orgSettings.max_session_duration_seconds ?? DEFAULT_EXECUTION_SETTINGS.max_session_duration_seconds;
  const serverSessionMinutes = Math.round(serverSessionSeconds / 60);

  const selectedAgent = AGENT_TYPES.find((agent) => agent.key === defaultAgentType) ?? AGENT_TYPES[0];
  const selectedTeamDefault = teamDefaults.find((cred) => cred.provider === selectedAgent.providerKey);
  const selectedSubscriptions = useMemo<DisplaySubscription[]>(() => {
    if (selectedAgent.key === "codex") return codexSubscriptions;
    if (selectedAgent.key === "claude_code") return claudeCodeSubscriptions;
    return [];
  }, [claudeCodeSubscriptions, codexSubscriptions, selectedAgent.key]);

  const selectedSensitiveEnvVar = selectedAgent.envVars.find((envVar) => envVar.sensitive);
  const selectedModelEnvVar = selectedAgent.envVars.find((envVar) => envVar.options);
  const selectedHasDirectAPIKey = Boolean(
    selectedSensitiveEnvVar && agentConfig[selectedAgent.key]?.[selectedSensitiveEnvVar.name],
  );
  const selectedHasFallbackCredential = selectedHasDirectAPIKey || Boolean(selectedTeamDefault);
  const selectedFallbackSourceLabel = selectedTeamDefault ? "team default" : "organization key";
  const selectedModel = selectedModelEnvVar
    ? agentConfig[selectedAgent.key]?.[selectedModelEnvVar.name]
    : undefined;
  const selectedSupportsSubscriptions =
    selectedAgent.key === "codex" || selectedAgent.key === "claude_code";
  const selectedHasCredentialSettings =
    selectedAgent.envVars.length > 0 || Boolean(selectedTeamDefault);

  const summaryRows = selectedSubscriptions.filter((sub) => sub.status !== "disabled");

  const [showAdvancedPerAgent, setShowAdvancedPerAgent] = useState<Record<string, boolean>>({});
  const [showManageSubscriptionsDialog, setShowManageSubscriptionsDialog] = useState(false);
  const [showManageSettingsDialog, setShowManageSettingsDialog] = useState(false);
  const [showCodexAuthModal, setShowCodexAuthModal] = useState(false);
  const [showClaudeAuthModal, setShowClaudeAuthModal] = useState(false);
  const [codexAuthModalLabel, setCodexAuthModalLabel] = useState<string | undefined>(undefined);
  const [claudeAuthModalLabel, setClaudeAuthModalLabel] = useState<string | undefined>(undefined);
  const [removingTeamProvider, setRemovingTeamProvider] = useState<string | null>(null);
  const [removingSubscriptionId, setRemovingSubscriptionId] = useState<string | null>(null);
  const [removingClaudeSubscriptionId, setRemovingClaudeSubscriptionId] = useState<string | null>(null);

  const autosave = useAutosave<SettingsPatch>({
    queryKey: queryKeys.settings.all,
    mutationFn: (payload) => api.settings.update(payload),
    applyOptimistic: applyOrgSettingsPatch,
    coalesce: coalesceSettingsPatch,
  });

  const maxConcurrentField = useAutosaveNumericField({
    serverValue: maxConcurrentServer,
    autosave,
    toPatch: (value) => ({ settings: { max_concurrent_runs: value } }),
    clamp: (value) => clampNumber(value, MIN_CONCURRENT_RUNS, MAX_CONCURRENT_RUNS),
  });

  const maxSessionMinutesField = useAutosaveNumericField({
    serverValue: serverSessionMinutes,
    autosave,
    toPatch: (minutes) => ({ settings: { max_session_duration_seconds: minutes * 60 } }),
    clamp: (value) => clampNumber(value, MIN_SESSION_DURATION_MINUTES, MAX_SESSION_DURATION_MINUTES),
  });

  function readLatestAgentConfig(): Record<string, Record<string, string>> {
    const cached = queryClient.getQueryData<SingleResponse<Organization>>(queryKeys.settings.all);
    const latest = (cached?.data?.settings ?? {}) as OrgSettings;
    return latest.agent_config ?? {};
  }

  function saveAgentConfigField(agentKey: string, envVar: string, value: string) {
    const current = { ...readLatestAgentConfig() };
    const providerConfig = { ...(current[agentKey] ?? {}) };
    if (value) {
      providerConfig[envVar] = value;
    } else {
      delete providerConfig[envVar];
    }

    if (Object.keys(providerConfig).length > 0) {
      current[agentKey] = providerConfig;
    } else {
      delete current[agentKey];
    }

    autosave.save({ settings: { agent_config: current } });
  }

  const sensitiveSaveMutation = useMutation({
    mutationFn: ({
      agentKey,
      envVar,
      value,
    }: {
      agentKey: string;
      envVar: string;
      value: string;
    }) =>
      api.settings.update({
        settings: { agent_config: { [agentKey]: { [envVar]: value } } },
      }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: queryKeys.settings.all });
      toast.success("Key saved");
    },
    onError: (error) => {
      captureError(error, { feature: "org-agent-sensitive-key-save" });
      toast.error("Failed to save key");
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

  const removeSubscriptionMutation = useMutation({
    mutationFn: (id: string) => api.codexAuth.removeSubscription(id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["codex-subscriptions"] });
      setRemovingSubscriptionId(null);
    },
    onError: (error) => {
      captureError(error, { feature: "codex-subscription-remove" });
    },
  });

  const removeClaudeSubscriptionMutation = useMutation({
    mutationFn: (id: string) => api.claudeCodeAuth.removeSubscription(id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["claude-code-subscriptions"] });
      setRemovingClaudeSubscriptionId(null);
    },
    onError: (error) => {
      captureError(error, { feature: "claude-code-subscription-remove" });
    },
  });

  const manageSettingsLabel = selectedSensitiveEnvVar ? "Manage API key & settings" : "Manage settings";

  function openSubscriptionAuth(agentKey: string, label?: string) {
    if (agentKey === "codex") {
      setCodexAuthModalLabel(label);
      setShowCodexAuthModal(true);
      return;
    }
    if (agentKey === "claude_code") {
      setClaudeAuthModalLabel(label);
      setShowClaudeAuthModal(true);
    }
  }

  function closeCodexAuthModal() {
    setShowCodexAuthModal(false);
    setCodexAuthModalLabel(undefined);
  }

  function closeClaudeAuthModal() {
    setShowClaudeAuthModal(false);
    setClaudeAuthModalLabel(undefined);
  }

  return (
    <PageContainer size="default">
      <div className="space-y-8">
        <PageHeader
          title="Coding agents"
          description="Configure organization agent defaults and execution behavior."
          action={isAdmin ? <AutosaveIndicator status={autosave.status} /> : undefined}
        />

        {isAdmin && (
          <section className="space-y-4">
            <div>
              <h2 className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">
                Organization coding agents
              </h2>
              <p className="mt-1 text-xs text-muted-foreground">
                Default agent and credentials for your organization. Used when members don&apos;t
                have personal keys configured.
              </p>
            </div>

            <Card>
              <CardContent className="space-y-4">
                <div className="space-y-1">
                  <Label>Available coding agents</Label>
                  <p className="text-xs text-muted-foreground">
                    Set the default organization agent. The selected agent&apos;s credential
                    sources and fallback behavior are managed below.
                  </p>
                </div>
                <RadioGroup
                  value={defaultAgentType}
                  onValueChange={(value) =>
                    autosave.save({
                      settings: {
                        default_agent_type: value as OrgSettings["default_agent_type"],
                      },
                    })
                  }
                  className="grid gap-3 md:grid-cols-2 xl:grid-cols-3"
                >
                  {AGENT_TYPES.map((agent) => (
                    <RadioCard
                      key={agent.key}
                      value={agent.key}
                      label={agent.label}
                      selected={defaultAgentType === agent.key}
                    />
                  ))}
                </RadioGroup>
              </CardContent>
            </Card>

            <Card>
              <CardContent className="space-y-4">
                <div className="space-y-1">
                  <p className="text-xs font-medium text-muted-foreground">Selected agent</p>
                  <div className="flex flex-wrap items-center gap-2">
                    <AgentBadge agentType={selectedAgent.key} labelClassName="text-base font-medium" />
                    {selectedTeamDefault && (
                      <Badge variant="secondary" className="text-xs">
                        <Shield className="mr-1 h-3 w-3" />
                        Team default set
                      </Badge>
                    )}
                  </div>
                  {selectedModel && (
                    <p className="text-xs text-muted-foreground">
                      Default model: <span className="font-mono text-foreground">{selectedModel}</span>
                    </p>
                  )}
                  {selectedAgent.note && (
                    <p className="text-xs text-muted-foreground">{selectedAgent.note}</p>
                  )}
                </div>

                {selectedSupportsSubscriptions ? (
                  summaryRows.length > 0 ? (
                    <Card className="border-border/70 shadow-none">
                      <CardContent className="space-y-4">
                        <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
                          <div>
                            <p className="text-sm font-medium">{selectedAgent.label} subscriptions</p>
                            <p className="text-xs text-muted-foreground">
                              Active subscriptions are used in round-robin order before falling back
                              to the API key.
                            </p>
                          </div>
                          <div className="flex flex-wrap gap-2">
                            <Button variant="outline" size="sm" onClick={() => setShowManageSubscriptionsDialog(true)}>
                              Manage subscriptions
                            </Button>
                            {selectedHasCredentialSettings && (
                              <Button size="sm" onClick={() => setShowManageSettingsDialog(true)}>
                                {manageSettingsLabel}
                              </Button>
                            )}
                          </div>
                        </div>

                        <SubscriptionSummaryTable subscriptions={summaryRows} />

                        {selectedSensitiveEnvVar && (
                          <p className="text-xs text-muted-foreground">
                            API key fallback:{" "}
                            <span className="font-medium text-foreground">
                              {selectedHasFallbackCredential ? "Configured" : "Not configured"}
                            </span>
                          </p>
                        )}
                      </CardContent>
                    </Card>
                  ) : (
                    <Card className="border-dashed border-border/80 shadow-none">
                      <CardContent className="space-y-4 py-8">
                        <div className="space-y-1">
                          <p className="text-sm font-semibold">
                            No {selectedAgent.label} subscriptions connected yet
                          </p>
                          <p className="max-w-2xl text-xs text-muted-foreground">
                            Connect a {selectedAgent.label} subscription to use{" "}
                            {selectedAgent.label === "Claude Code"
                              ? "Anthropic-backed"
                              : "ChatGPT-backed"}{" "}
                            agent runs for your organization. Labels are generated for you
                            automatically.
                          </p>
                          {selectedSensitiveEnvVar && (
                            <p className="text-xs text-muted-foreground">
                              {selectedHasFallbackCredential
                                ? `API key fallback is configured via ${selectedFallbackSourceLabel}.`
                                : "Optional: add an API key too as a fallback source."}
                            </p>
                          )}
                        </div>
                        <div className="flex flex-wrap gap-2">
                          <Button onClick={() => openSubscriptionAuth(selectedAgent.key)}>
                            <Plus className="mr-1.5 h-3.5 w-3.5" />
                            Add {selectedAgent.label === "Claude Code" ? "Claude subscription" : "Codex subscription"}
                          </Button>
                          {selectedHasCredentialSettings && (
                            <Button variant="outline" onClick={() => setShowManageSettingsDialog(true)}>
                              {selectedSensitiveEnvVar && !selectedHasFallbackCredential
                                ? "Add API key fallback"
                                : manageSettingsLabel}
                            </Button>
                          )}
                        </div>
                      </CardContent>
                    </Card>
                  )
                ) : (
                  <Card className="border-dashed border-border/80 shadow-none">
                    <CardContent className="space-y-3 py-8">
                      <div className="space-y-1">
                        <p className="text-sm font-semibold">
                          {selectedAgent.inheritsProviderKeys
                            ? "This agent reuses credentials from other providers."
                            : "This agent uses API-key credentials only."}
                        </p>
                        <p className="max-w-2xl text-xs text-muted-foreground">
                          {selectedAgent.inheritsProviderKeys
                            ? "Configure the provider agents Pi depends on, then use settings here to control routing and model overrides."
                            : "Configure the API key and default settings for this agent. Those credentials are used for organization-level runs when members do not provide personal keys."}
                        </p>
                      </div>
                      {selectedHasCredentialSettings && (
                        <div className="flex flex-wrap gap-2">
                          <Button onClick={() => setShowManageSettingsDialog(true)}>
                            {manageSettingsLabel}
                          </Button>
                        </div>
                      )}
                    </CardContent>
                  </Card>
                )}
              </CardContent>
            </Card>
          </section>
        )}

        {isAdmin && (
          <section className="space-y-3">
            <div>
              <h2 className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">
                Execution
              </h2>
              <p className="mt-1 text-xs text-muted-foreground">
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
                      onValueChange={(value) =>
                        autosave.save({
                          settings: {
                            autonomy_level: value as "manual" | "auto_simple" | "auto_all",
                          },
                        })
                      }
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
                      onValueChange={(value) =>
                        autosave.save({
                          settings: { execution_aggressiveness: parseInt(value, 10) },
                        })
                      }
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
                      min={MIN_CONCURRENT_RUNS}
                      max={MAX_CONCURRENT_RUNS}
                      value={maxConcurrentField.value}
                      onChange={maxConcurrentField.onChange}
                      onBlur={maxConcurrentField.onBlur}
                    />
                  </div>

                  <div className="space-y-2">
                    <Label htmlFor="max-session-minutes">Max session duration (minutes)</Label>
                    <Input
                      id="max-session-minutes"
                      type="number"
                      min={MIN_SESSION_DURATION_MINUTES}
                      max={MAX_SESSION_DURATION_MINUTES}
                      value={maxSessionMinutesField.value}
                      onChange={maxSessionMinutesField.onChange}
                      onBlur={maxSessionMinutesField.onBlur}
                    />
                    <p className="text-xs text-muted-foreground">
                      Sessions that exceed this wall-clock limit are cancelled and marked failed.
                      Defaults to 25 minutes; allowed range {MIN_SESSION_DURATION_MINUTES}–
                      {MAX_SESSION_DURATION_MINUTES} minutes.
                    </p>
                  </div>
                </div>
              </CardContent>
            </Card>
          </section>
        )}
      </div>

      <Dialog open={showManageSubscriptionsDialog} onOpenChange={setShowManageSubscriptionsDialog}>
        <DialogContent className="sm:max-w-3xl">
          <DialogHeader>
            <DialogTitle>Manage {selectedAgent.label} subscriptions</DialogTitle>
            <DialogDescription>
              Active subscriptions are used in round-robin order before falling back to the API
              key when one is configured.
            </DialogDescription>
          </DialogHeader>

          {summaryRows.length > 0 ? (
            <SubscriptionManagementTable
              subscriptions={summaryRows}
              onResume={(subscription) => openSubscriptionAuth(selectedAgent.key, subscription.label)}
              onRemove={(subscriptionId) =>
                selectedAgent.key === "codex"
                  ? setRemovingSubscriptionId(subscriptionId)
                  : setRemovingClaudeSubscriptionId(subscriptionId)
              }
            />
          ) : (
            <p className="text-sm text-muted-foreground">
              No subscriptions connected yet for {selectedAgent.label}.
            </p>
          )}

          <DialogFooter className="sm:justify-between">
            <Button onClick={() => openSubscriptionAuth(selectedAgent.key)}>
              <Plus className="mr-1.5 h-3.5 w-3.5" />
              Add subscription
            </Button>
            <Button variant="outline" onClick={() => setShowManageSubscriptionsDialog(false)}>
              Close
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog open={showManageSettingsDialog} onOpenChange={setShowManageSettingsDialog}>
        <DialogContent className="sm:max-w-xl">
          <DialogHeader>
            <DialogTitle>
              {selectedSensitiveEnvVar
                ? `${selectedAgent.label} API key & settings`
                : `${selectedAgent.label} settings`}
            </DialogTitle>
            <DialogDescription>
              {selectedSupportsSubscriptions
                ? "Manage the fallback API key and provider settings used for organization runs."
                : selectedAgent.inheritsProviderKeys
                  ? "Adjust model routing and override settings for this meta-agent."
                  : "Manage the credentials and defaults used for organization runs."}
            </DialogDescription>
          </DialogHeader>

          <div className="space-y-4">
            {selectedTeamDefault?.masked_key && (
              <p className="text-xs text-muted-foreground">
                Team key: <span className="font-mono">{selectedTeamDefault.masked_key}</span>
                {selectedTeamDefault.set_by_user_name && (
                  <span> &middot; Set by {selectedTeamDefault.set_by_user_name}</span>
                )}
              </p>
            )}

            {selectedAgent.envVars
              .filter((envVar) => !envVar.advanced || showAdvancedPerAgent[selectedAgent.key])
              .map((envVar) => {
                const displayValue = agentConfig[selectedAgent.key]?.[envVar.name] ?? "";
                const isPendingSensitiveSave = Boolean(
                  envVar.sensitive &&
                    sensitiveSaveMutation.isPending &&
                    sensitiveSaveMutation.variables?.agentKey === selectedAgent.key &&
                    sensitiveSaveMutation.variables?.envVar === envVar.name,
                );

                return (
                  <div key={envVar.name} className="space-y-1.5">
                    <div className="flex items-center justify-between">
                      <Label htmlFor={`org-${selectedAgent.key}-${envVar.name}`}>{envVar.label}</Label>
                      {envVar.sensitive && displayValue && (
                        <span className="inline-flex items-center text-xs text-emerald-600 dark:text-emerald-400">
                          <Check className="mr-1 h-3 w-3" />
                          Configured
                        </span>
                      )}
                    </div>
                    {envVar.options ? (
                      <Select
                        value={displayValue || undefined}
                        onValueChange={(value) => saveAgentConfigField(selectedAgent.key, envVar.name, value)}
                      >
                        <SelectTrigger
                          id={`org-${selectedAgent.key}-${envVar.name}`}
                          aria-label={envVar.label}
                        >
                          <SelectValue placeholder="Select a value" />
                        </SelectTrigger>
                        <SelectContent>
                          {envVar.options.map((option) => (
                            <SelectItem key={option} value={option}>
                              {option}
                            </SelectItem>
                          ))}
                        </SelectContent>
                      </Select>
                    ) : envVar.sensitive ? (
                      <AgentConfigSensitiveField
                        id={`org-${selectedAgent.key}-${envVar.name}`}
                        placeholder={envVar.placeholder ?? "API key"}
                        hasExistingValue={Boolean(displayValue)}
                        isSaving={isPendingSensitiveSave}
                        onSave={(value) =>
                          sensitiveSaveMutation.mutateAsync({
                            agentKey: selectedAgent.key,
                            envVar: envVar.name,
                            value,
                          })
                        }
                      />
                    ) : (
                      <DebouncedInput
                        id={`org-${selectedAgent.key}-${envVar.name}`}
                        type="text"
                        className="font-mono text-xs"
                        placeholder={envVar.placeholder ?? "Not set"}
                        serverValue={displayValue}
                        onCommit={(value) => saveAgentConfigField(selectedAgent.key, envVar.name, value)}
                      />
                    )}
                    {envVar.helpText && (
                      <p className="text-xs text-muted-foreground">{envVar.helpText}</p>
                    )}
                  </div>
                );
              })}

            {selectedAgent.envVars.some((envVar) => envVar.advanced) && (
              <Button
                type="button"
                variant="ghost"
                size="sm"
                className="text-xs text-muted-foreground"
                onClick={() =>
                  setShowAdvancedPerAgent((prev) => ({
                    ...prev,
                    [selectedAgent.key]: !prev[selectedAgent.key],
                  }))
                }
              >
                {showAdvancedPerAgent[selectedAgent.key] ? "Hide advanced settings" : "Advanced settings"}
              </Button>
            )}
          </div>

          <DialogFooter className="sm:justify-between">
            {selectedTeamDefault ? (
              <Button
                variant="ghost"
                size="sm"
                className="text-destructive hover:text-destructive"
                onClick={() => setRemovingTeamProvider(selectedAgent.providerKey)}
                disabled={removeTeamMutation.isPending}
              >
                Remove team default
              </Button>
            ) : (
              <span />
            )}
            <Button variant="outline" onClick={() => setShowManageSettingsDialog(false)}>
              Close
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <AlertDialog
        open={!!removingTeamProvider}
        onOpenChange={(open) => !open && setRemovingTeamProvider(null)}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Remove team default</AlertDialogTitle>
            <AlertDialogDescription>
              Are you sure you want to remove the team default for{" "}
              {providerDisplayName(removingTeamProvider ?? "")}? Team members without personal keys
              will fall back to the organization credential.
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

      <AlertDialog
        open={!!removingSubscriptionId}
        onOpenChange={(open) => !open && setRemovingSubscriptionId(null)}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Remove subscription</AlertDialogTitle>
            <AlertDialogDescription>
              Are you sure you want to disconnect this ChatGPT subscription? Agents will no longer
              use it.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              onClick={() => {
                if (removingSubscriptionId) removeSubscriptionMutation.mutate(removingSubscriptionId);
              }}
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
            >
              Remove
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      <AlertDialog
        open={!!removingClaudeSubscriptionId}
        onOpenChange={(open) => !open && setRemovingClaudeSubscriptionId(null)}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Remove Claude subscription</AlertDialogTitle>
            <AlertDialogDescription>
              Are you sure you want to disconnect this Claude subscription? Agents will no longer
              use it.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction
              onClick={() => {
                if (removingClaudeSubscriptionId) {
                  removeClaudeSubscriptionMutation.mutate(removingClaudeSubscriptionId);
                }
              }}
              className="bg-destructive text-destructive-foreground hover:bg-destructive/90"
            >
              Remove
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>

      {showCodexAuthModal && (
        <CodexDeviceCodeModal
          label={codexAuthModalLabel}
          onClose={closeCodexAuthModal}
          onConnected={() => {
            queryClient.invalidateQueries({ queryKey: ["codex-subscriptions"] });
            closeCodexAuthModal();
          }}
        />
      )}

      {showClaudeAuthModal && (
        <ClaudeCodeAuthModal
          label={claudeAuthModalLabel}
          onClose={closeClaudeAuthModal}
          onConnected={() => {
            queryClient.invalidateQueries({ queryKey: ["claude-code-subscriptions"] });
            closeClaudeAuthModal();
          }}
        />
      )}
    </PageContainer>
  );
}

function SubscriptionSummaryTable({ subscriptions }: { subscriptions: DisplaySubscription[] }) {
  return (
    <Table>
      <TableHeader>
        <TableRow>
          <TableHead>Name</TableHead>
          <TableHead>Status</TableHead>
          <TableHead>Type</TableHead>
          <TableHead>Last used</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {subscriptions.map((subscription) => (
          <TableRow key={subscription.id}>
            <TableCell className="font-medium">{subscription.label || "Default"}</TableCell>
            <TableCell>
              <SubscriptionStatusBadge status={subscription.status} />
            </TableCell>
            <TableCell>{subscription.account_type ?? "Unknown"}</TableCell>
            <TableCell>{formatRelativeTimestamp(subscription.last_used_at)}</TableCell>
          </TableRow>
        ))}
      </TableBody>
    </Table>
  );
}

function SubscriptionManagementTable({
  subscriptions,
  onResume,
  onRemove,
}: {
  subscriptions: DisplaySubscription[];
  onResume: (subscription: DisplaySubscription) => void;
  onRemove: (subscriptionId: string) => void;
}) {
  return (
    <Table>
      <TableHeader>
        <TableRow>
          <TableHead>Name</TableHead>
          <TableHead>Status</TableHead>
          <TableHead>Type</TableHead>
          <TableHead>Last used</TableHead>
          <TableHead className="text-right">Actions</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {subscriptions.map((subscription) => (
          <TableRow key={subscription.id}>
            <TableCell className="font-medium">{subscription.label || "Default"}</TableCell>
            <TableCell>
              <SubscriptionStatusBadge status={subscription.status} />
            </TableCell>
            <TableCell>{subscription.account_type ?? "Unknown"}</TableCell>
            <TableCell>{formatRelativeTimestamp(subscription.last_used_at)}</TableCell>
            <TableCell className="text-right">
              <div className="flex justify-end gap-2">
                {subscription.status === "pending_auth" && (
                  <Button
                    variant="outline"
                    size="sm"
                    onClick={() => onResume(subscription)}
                    aria-label={`Resume setup ${subscription.label || "Default"}`}
                  >
                    Resume setup
                  </Button>
                )}
                <Button
                  variant="ghost"
                  size="sm"
                  className="text-xs text-muted-foreground hover:text-destructive"
                  onClick={() => onRemove(subscription.id)}
                  aria-label={`Remove subscription ${subscription.label || "Default"}`}
                >
                  <Trash2 className="h-3.5 w-3.5" />
                </Button>
              </div>
            </TableCell>
          </TableRow>
        ))}
      </TableBody>
    </Table>
  );
}

function SubscriptionStatusBadge({ status }: { status: string }) {
  if (status === "active") {
    return (
      <Badge variant="outline" className="border-green-600 text-green-600">
        <CheckCircle2 className="mr-1 h-3 w-3" />
        Active
      </Badge>
    );
  }
  if (status === "pending_auth") {
    return (
      <Badge variant="outline" className="border-amber-600 text-amber-700">
        <AlertCircle className="mr-1 h-3 w-3" />
        Needs attention
      </Badge>
    );
  }
  if (status === "invalid") {
    return (
      <Badge variant="outline" className="border-destructive text-destructive">
        <AlertCircle className="mr-1 h-3 w-3" />
        Needs attention
      </Badge>
    );
  }
  return <Badge variant="outline">{status}</Badge>;
}

function formatRelativeTimestamp(value?: string): string {
  if (!value) return "Never";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "Never";
  return new Intl.DateTimeFormat("en-US", {
    month: "short",
    day: "numeric",
  }).format(date);
}

interface AgentConfigSensitiveFieldProps {
  id: string;
  placeholder: string;
  hasExistingValue: boolean;
  isSaving: boolean;
  onSave: (value: string) => Promise<unknown>;
}

function AgentConfigSensitiveField({
  id,
  placeholder,
  hasExistingValue,
  isSaving,
  onSave,
}: AgentConfigSensitiveFieldProps) {
  const [value, setValue] = useState("");
  const trimmed = value.trim();
  const canSave = trimmed.length > 0 && !isSaving;

  const handleSave = async () => {
    if (!canSave) return;
    try {
      await onSave(trimmed);
      setValue("");
    } catch {
      // Parent mutation owns error handling and preserves the typed value.
    }
  };

  return (
    <div className="flex gap-2">
      <div className="flex-1">
        <Input
          id={id}
          type="password"
          placeholder={hasExistingValue ? "Replace existing key..." : placeholder}
          value={value}
          onChange={(event) => setValue(event.target.value)}
          className="font-mono text-xs"
          autoComplete="off"
          spellCheck={false}
        />
      </div>
      <Button size="sm" onClick={handleSave} disabled={!canSave}>
        {isSaving ? "Saving..." : "Save key"}
      </Button>
    </div>
  );
}
