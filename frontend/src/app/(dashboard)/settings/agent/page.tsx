"use client";

import { type ReactNode, useMemo, useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { Check, CheckCircle2, Eye, EyeOff, KeyRound, Sparkles, Shield, Plus, Trash2 } from "lucide-react";
import { toast } from "sonner";
import { api } from "@/lib/api";
import { captureError } from "@/lib/errors";
import { useAuth } from "@/hooks/use-auth";
import { AGENT_TYPES, sourceLabel, sourceBadgeVariant, providerDisplayName } from "@/lib/agent-constants";
import { AutosaveIndicator } from "@/components/AutosaveIndicator";
import { DebouncedInput } from "@/components/debounced-fields";
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
  UserCredentialSummary,
  ResolvedCredential,
  CodexSubscription,
  ListResponse,
  Organization,
  OrgSettings,
  SingleResponse,
} from "@/lib/types";

// Bounds live in `@/lib/settings-constants`; defaults mirror
// `internal/models/org_settings.go`. The server clamps on save, so UI drift
// is visible (values snap) rather than corrupting state.
const DEFAULT_EXECUTION_SETTINGS = {
  autonomy_level: "auto_simple" as const,
  execution_aggressiveness: 2,
  max_concurrent_runs: 5,
  max_session_duration_seconds: 25 * 60,
};

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
    queryKey: queryKeys.codexAuth.status,
    queryFn: () => api.codexAuth.status(),
    refetchInterval: false,
  });
  const codexAuthStatus = codexAuthStatusResp?.data;

  const { data: codexSubscriptionsResp } = useQuery<ListResponse<CodexSubscription>>({
    queryKey: ["codex-subscriptions"],
    queryFn: () => api.codexAuth.listSubscriptions(),
  });
  const codexSubscriptions = codexSubscriptionsResp?.data ?? [];
  const activeSubscriptions = codexSubscriptions.filter((s) => s.status === "active");

  /* ---------- Org settings queries (admin-gated) ---------- */

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
  const maxConcurrentServer = orgSettings.max_concurrent_runs ?? DEFAULT_EXECUTION_SETTINGS.max_concurrent_runs;
  const serverSessionSeconds = orgSettings.max_session_duration_seconds ?? DEFAULT_EXECUTION_SETTINGS.max_session_duration_seconds;
  const serverSessionMinutes = Math.round(serverSessionSeconds / 60);

  const hasCodexAPIKey = useMemo(() => {
    const codexOrgConfig = agentConfig.codex ?? {};
    return Boolean(codexOrgConfig.OPENAI_API_KEY);
  }, [agentConfig.codex]);

  const inferredCodexCredentialMethod: "chatgpt" | "api_key" =
    hasCodexAPIKey && activeSubscriptions.length === 0 && codexAuthStatus?.status !== "completed" ? "api_key" : "chatgpt";

  // Pure UI toggle — the server has no `codex_credential_method` field.
  // Null means "follow the inference"; setting a value pins the user's choice.
  const [codexCredentialMethodOverride, setCodexCredentialMethodOverride] = useState<"chatgpt" | "api_key" | null>(null);
  const codexCredentialMethod = codexCredentialMethodOverride ?? inferredCodexCredentialMethod;

  const [showAdvancedPerAgent, setShowAdvancedPerAgent] = useState<Record<string, boolean>>({});
  const [showDeviceCodeModal, setShowDeviceCodeModal] = useState(false);
  const [newSubscriptionLabel, setNewSubscriptionLabel] = useState("");
  const [removingSubscriptionId, setRemovingSubscriptionId] = useState<string | null>(null);

  const autosave = useAutosave<SettingsPatch>({
    queryKey: queryKeys.settings.all,
    mutationFn: (payload) => api.settings.update(payload),
    applyOptimistic: applyOrgSettingsPatch,
    coalesce: coalesceSettingsPatch,
  });

  const maxConcurrentField = useAutosaveNumericField({
    serverValue: maxConcurrentServer,
    autosave,
    toPatch: (v) => ({ settings: { max_concurrent_runs: v } }),
    clamp: (v) => clampNumber(v, MIN_CONCURRENT_RUNS, MAX_CONCURRENT_RUNS),
  });

  const maxSessionMinutesField = useAutosaveNumericField({
    serverValue: serverSessionMinutes,
    autosave,
    toPatch: (minutes) => ({ settings: { max_session_duration_seconds: minutes * 60 } }),
    clamp: (v) => clampNumber(v, MIN_SESSION_DURATION_MINUTES, MAX_SESSION_DURATION_MINUTES),
  });

  // Read the latest `agent_config` from the React Query cache rather than a
  // render-time closure. The cache entry is advanced synchronously by each
  // autosave's `applyOptimistic`, so back-to-back saves (e.g. typing in two
  // providers' keys, or a debounced commit firing while a model select has
  // already advanced the cache) always start from the freshest snapshot —
  // a ref-based mirror would lag by a render.
  function readLatestAgentConfig(): Record<string, Record<string, string>> {
    const cached = queryClient.getQueryData<SingleResponse<Organization>>(queryKeys.settings.all);
    const latest = (cached?.data?.settings ?? {}) as OrgSettings;
    return latest.agent_config ?? {};
  }

  // Write an env var into agent_config. `mergeSettingsJSON` on the server now
  // deep-merges, so a sparse patch would also be safe, but we keep the full
  // merged object for clarity and to match `coalesceSettingsPatch`, which
  // shallow-merges at the `settings` level.
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

  // Sensitive env vars (API keys) intentionally skip the autosave pipeline:
  // we never prefill the plaintext into the input, never optimistically apply
  // a half-typed key to the cache, and never commit on keystroke/blur. The
  // caller must press "Save key" to dispatch. See settings/AGENTS.md for the
  // policy.
  //
  // The patch is sparse — just the one env var under its provider — and
  // relies on the server's `mergeSettingsJSON` to deep-merge into the
  // existing `agent_config`. Sending the full merged object (as an earlier
  // version did) would race with any in-flight autosave for an unrelated
  // field in the same provider: both requests would carry overlapping
  // sibling values and the later writer would silently clobber the earlier
  // one. Sparse + deep-merge keeps each write scoped to the field it owns.
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

  const removeSubscriptionMutation = useMutation({
    mutationFn: (id: string) => api.codexAuth.removeSubscription(id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ["codex-subscriptions"] });
      queryClient.invalidateQueries({ queryKey: queryKeys.codexAuth.status });
      setRemovingSubscriptionId(null);
    },
    onError: (error) => {
      captureError(error, { feature: "codex-subscription-remove" });
    },
  });

  /* ---------- Render helpers ---------- */

  /** Shared ChatGPT auth status — shows list of subscriptions with add/remove. */
  function renderChatGPTAuthStatus(): ReactNode {
    return (
      <div className="space-y-3">
        {activeSubscriptions.length > 0 && (
          <div className="space-y-2">
            <Label className="text-xs text-muted-foreground">
              Connected subscriptions ({activeSubscriptions.length}) &mdash; usage is distributed via round-robin
            </Label>
            {activeSubscriptions.map((sub) => (
              <div key={sub.id} className="flex items-center justify-between rounded-md border px-3 py-2">
                <div className="flex items-center gap-2">
                  <Badge variant="outline" className="border-green-600 text-green-600">
                    <CheckCircle2 className="mr-1 h-3.5 w-3.5" />
                    Active
                  </Badge>
                  <span className="text-sm font-medium">{sub.label || "Default"}</span>
                  {sub.account_type && (
                    <span className="text-xs text-muted-foreground">({sub.account_type})</span>
                  )}
                </div>
                <Button
                  size="sm"
                  variant="ghost"
                  className="text-xs text-muted-foreground hover:text-destructive"
                  onClick={() => setRemovingSubscriptionId(sub.id)}
                  aria-label={`Remove subscription ${sub.label || "Default"}`}
                >
                  <Trash2 className="h-3.5 w-3.5" />
                </Button>
              </div>
            ))}
          </div>
        )}

        <div className="flex items-center gap-2">
          <Input
            placeholder="Subscription label (e.g. Team A)"
            value={newSubscriptionLabel}
            onChange={(e) => setNewSubscriptionLabel(e.target.value.slice(0, 100))}
            maxLength={100}
            className="max-w-xs text-sm"
          />
          <Button size="sm" onClick={() => setShowDeviceCodeModal(true)} disabled={showDeviceCodeModal}>
            <Plus className="mr-1 h-3.5 w-3.5" />
            Add subscription
          </Button>
        </div>
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

        {envVarsToRender.map((envVar) => {
          const displayValue = agentConfig[agent.key]?.[envVar.name] ?? "";
          const isPendingSensitiveSave = Boolean(
            envVar.sensitive &&
              sensitiveSaveMutation.isPending &&
              sensitiveSaveMutation.variables?.agentKey === agent.key &&
              sensitiveSaveMutation.variables?.envVar === envVar.name,
          );

          return (
            <div key={envVar.name} className="space-y-1">
              <div className="flex items-center justify-between">
                <Label htmlFor={`org-${agent.key}-${envVar.name}`} className="text-xs text-muted-foreground">
                  {envVar.label}
                </Label>
                {envVar.sensitive && displayValue && (
                  <span className="inline-flex items-center text-xs text-emerald-600 dark:text-emerald-400">
                    <Check className="mr-0.5 h-3 w-3" />
                    Configured
                  </span>
                )}
              </div>
              {envVar.options ? (
                <Select
                  value={displayValue || undefined}
                  onValueChange={(value) => saveAgentConfigField(agent.key, envVar.name, value)}
                >
                  <SelectTrigger
                    id={`org-${agent.key}-${envVar.name}`}
                    aria-label={envVar.label}
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
              ) : envVar.sensitive ? (
                <AgentConfigSensitiveField
                  id={`org-${agent.key}-${envVar.name}`}
                  placeholder={envVar.placeholder ?? "API key"}
                  hasExistingValue={Boolean(displayValue)}
                  isSaving={isPendingSensitiveSave}
                  onSave={(value) =>
                    sensitiveSaveMutation.mutateAsync({
                      agentKey: agent.key,
                      envVar: envVar.name,
                      value,
                    })
                  }
                />
              ) : (
                <DebouncedInput
                  id={`org-${agent.key}-${envVar.name}`}
                  type="text"
                  className="font-mono text-xs"
                  placeholder={envVar.placeholder ?? "Not set"}
                  serverValue={displayValue}
                  onCommit={(value) => saveAgentConfigField(agent.key, envVar.name, value)}
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

  return (
    <PageContainer size="default">
      <div className="space-y-8">
        <PageHeader
          title="Coding agents"
          description="Configure organization agent defaults and execution behavior."
          action={isAdmin ? <AutosaveIndicator status={autosave.status} /> : undefined}
        />

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
                    onValueChange={(value) =>
                      autosave.save({
                        settings: {
                          default_agent_type: value as OrgSettings["default_agent_type"],
                        },
                      })
                    }
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

                  {renderOrgAgentConfigCard()}
                </div>
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
                      onValueChange={(v) =>
                        autosave.save({
                          settings: {
                            autonomy_level: v as "manual" | "auto_simple" | "auto_all",
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
                      onValueChange={(v) =>
                        autosave.save({ settings: { execution_aggressiveness: parseInt(v, 10) } })
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
                      Sessions that exceed this wall-clock limit are cancelled and marked failed. Defaults to 25 minutes; allowed range {MIN_SESSION_DURATION_MINUTES}–{MAX_SESSION_DURATION_MINUTES} minutes.
                    </p>
                  </div>
                </div>
              </CardContent>
            </Card>
          </section>
        )}
      </div>

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

      <AlertDialog open={!!removingSubscriptionId} onOpenChange={(open) => !open && setRemovingSubscriptionId(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Remove subscription</AlertDialogTitle>
            <AlertDialogDescription>
              Are you sure you want to disconnect this ChatGPT subscription? Agents will no longer use it.
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

      {showDeviceCodeModal && (
        <CodexDeviceCodeModal
          label={newSubscriptionLabel.trim() || undefined}
          onClose={() => { setShowDeviceCodeModal(false); setNewSubscriptionLabel(""); }}
          onConnected={() => {
            queryClient.invalidateQueries({ queryKey: queryKeys.codexAuth.status });
            queryClient.invalidateQueries({ queryKey: ["codex-subscriptions"] });
            setShowDeviceCodeModal(false);
            setNewSubscriptionLabel("");
          }}
        />
      )}
    </PageContainer>
  );
}

interface AgentConfigSensitiveFieldProps {
  id: string;
  placeholder: string;
  hasExistingValue: boolean;
  isSaving: boolean;
  onSave: (value: string) => Promise<unknown>;
}

// Sensitive fields own their input state — the plaintext key is never
// seeded from the server cache, so reloads and background refetches can't
// resurface a partially typed value or pre-populate the DOM. Save is
// explicit: the caller dispatches a full-object patch through the
// non-optimistic `sensitiveSaveMutation` on the parent page.
function AgentConfigSensitiveField({
  id,
  placeholder,
  hasExistingValue,
  isSaving,
  onSave,
}: AgentConfigSensitiveFieldProps) {
  const [value, setValue] = useState("");
  const [showKey, setShowKey] = useState(false);
  const trimmed = value.trim();
  const canSave = trimmed.length > 0 && !isSaving;

  // Only clear the input on a successful save. If the mutation rejects
  // (network error, server validation), the typed value is preserved so
  // the user doesn't have to retype the key.
  const handleSave = async () => {
    if (!canSave) return;
    try {
      await onSave(trimmed);
      setValue("");
      setShowKey(false);
    } catch {
      // Parent's mutation.onError surfaces the toast + captureError; we
      // deliberately keep the input populated here.
    }
  };

  return (
    <div className="flex gap-2">
      <div className="relative flex-1">
        <Input
          id={id}
          type={showKey ? "text" : "password"}
          placeholder={hasExistingValue ? "Replace existing key..." : placeholder}
          value={value}
          onChange={(e) => setValue(e.target.value)}
          className="pr-9 font-mono text-xs"
          autoComplete="off"
          spellCheck={false}
        />
        <button
          type="button"
          onClick={() => setShowKey((prev) => !prev)}
          className="absolute right-2.5 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground"
          aria-label={showKey ? "Hide key" : "Show key"}
        >
          {showKey ? <EyeOff className="h-3.5 w-3.5" /> : <Eye className="h-3.5 w-3.5" />}
        </button>
      </div>
      <Button
        size="sm"
        onClick={handleSave}
        disabled={!canSave}
      >
        {isSaving ? "Saving..." : "Save key"}
      </Button>
    </div>
  );
}
